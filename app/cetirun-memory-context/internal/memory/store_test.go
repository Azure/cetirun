// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	store *Store
	home  string
}

func (s *Store) open() (*snapshot, error) {
	return s.openSnapshot(context.Background(), 0)
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	home := t.TempDir()
	s := New(Config{Root: filepath.Join(home, ".cetirun", "memory")})
	s.now = func() time.Time { return time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC) }
	f := fixture{s, home}
	r := f.request(t, struct{}{})
	r.RequestID = "initialize"
	response, code := s.Execute(context.Background(), "init", r)
	if code != 0 {
		t.Fatalf("init: %+v", response)
	}
	return f
}
func (f fixture) request(t *testing.T, payload any) Request {
	t.Helper()
	var generation int64
	v, err := f.store.open()
	if err == nil {
		generation = v.m.Generation
		v.fs.close()
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return Request{ContractVersion: ContractVersion, RequestID: newID("request"), ExpectedGeneration: &generation, RequestContext: RequestContext{Audience: "owner-private", Destination: Destination{Kind: "owner-private"}, OpportunityID: "fixture-task", Scope: Scope{"project": "fixture"}}, Payload: b}
}
func (f fixture) call(t *testing.T, command string, payload any) Response {
	t.Helper()
	response, code := f.store.Execute(context.Background(), command, f.request(t, payload))
	if code != 0 {
		t.Fatalf("%s: code %d, %+v", command, code, response)
	}
	return response
}
func testNote(title string) Note {
	at := "2026-10-07T00:00:00Z"
	version := "v1"
	return Note{Kind: "fact", Title: title, Scope: Scope{"project": "fixture"}, Status: "active", TemporalClass: "stable", Revalidation: Revalidation{Mode: "on-change"}, Sources: []Source{{Kind: "fixture", Ref: "source-" + title, Origin: "tool-observed", Assurance: "reported", ObservedAt: &at, Version: &version}}, Body: "This is synthetic fixture knowledge."}
}
func (f fixture) note(t *testing.T, n Note) Ref {
	return f.call(t, "capture", captureInput{Note: &n}).Data.(writeResult).Refs[0]
}

func TestCaptureQueryReviseAndReplay(t *testing.T) {
	f := newFixture(t)
	n := testNote("release")
	n.Aliases = []string{"launch"}
	r := f.request(t, captureInput{Note: &n})
	a, code := f.store.Execute(context.Background(), "capture", r)
	if code != 0 {
		t.Fatal(a)
	}
	b, code := f.store.Execute(context.Background(), "capture", r)
	if code != 0 || b.Generation != a.Generation {
		t.Fatalf("replay: %+v", b)
	}
	query := f.call(t, "query", Query{QueryTerms: []string{"launch"}}).Data.(QueryResult)
	if len(query.Hits) != 1 || query.Stage != "alias" || query.Hits[0].Review.NeedsRecheck {
		t.Fatalf("query: %+v", query)
	}
	old := query.Hits[0]
	n = old.Note
	n.Body = "Corrected fixture fact."
	changed := f.call(t, "revise", reviseInput{Ref: old.Ref, Note: n, Reason: "fixture correction"}).Data.(writeResult).Refs[0]
	if changed.Revision != 2 || changed.Digest == old.Ref.Digest {
		t.Fatal("missing immutable revision")
	}
	v, err := f.store.open()
	if err != nil {
		t.Fatal(err)
	}
	defer v.fs.close()
	var original Note
	if err := v.read(old.Ref, &original); err != nil || original.Body == n.Body {
		t.Fatal("old object was overwritten", err)
	}
}
func TestCrashRecoveryAtCommitPoints(t *testing.T) {
	for _, point := range []string{"transaction", "objects", "manifest", "head"} {
		t.Run(point, func(t *testing.T) {
			f := newFixture(t)
			n := testNote("crash")
			r := f.request(t, captureInput{Note: &n})
			f.store.fault = func(at string) error {
				if at == point {
					return problem(8, "InjectedFailure")
				}
				return nil
			}
			response, code := f.store.Execute(context.Background(), "capture", r)
			if code == 0 || response.Data != nil {
				t.Fatal("unconfirmed write reported success")
			}
			f.store.fault = nil
			again, code := f.store.Execute(context.Background(), "capture", r)
			if code != 0 || !again.Data.(writeResult).Saved {
				t.Fatalf("recovery: %+v", again)
			}
			v, err := f.store.open()
			if err != nil {
				t.Fatal(err)
			}
			defer v.fs.close()
			if len(v.refs("note")) != 1 || v.m.Generation != 2 {
				t.Fatalf("duplicated commit: %+v", v.m)
			}
		})
	}
}

func TestConsecutiveInterruptedCommitsRecoverViews(t *testing.T) {
	f := newFixture(t)
	f.note(t, testNote("before-interruption"))
	for i := 0; i < 2; i++ {
		f.store.fault = func(point string) error {
			if point == "head" {
				return problem(8, "InjectedFailure")
			}
			return nil
		}
		note := testNote(fmt.Sprintf("interrupted-%d", i))
		response, code := f.store.Execute(context.Background(), "capture", f.request(t, captureInput{Note: &note}))
		if code != 8 || response.Reason != "CommitOutcomeUnknown" {
			t.Fatalf("interruption %d: %+v", i, response)
		}
	}
	f.store.fault = nil
	f.call(t, "repair-index", struct{}{})
	f.note(t, testNote("after-interruption"))
	v, err := f.store.open()
	if err != nil {
		t.Fatal(err)
	}
	defer v.fs.close()
	if len(v.refs("note")) != 4 {
		t.Fatal("lost a committed note")
	}
	for name, hash := range v.m.Views {
		data, err := v.fs.read(name, MaxInput)
		if err != nil || digest(data) != hash.Current {
			t.Fatalf("view %s was not recovered: %v", name, err)
		}
	}
}

func TestForgottenCaptureCannotReplaySavedResult(t *testing.T) {
	f := newFixture(t)
	n := testNote("forget-replay")
	r := f.request(t, captureInput{Note: &n})
	saved, code := f.store.Execute(context.Background(), "capture", r)
	if code != 0 {
		t.Fatal(saved)
	}
	ref := saved.Data.(writeResult).Refs[0]
	plan := f.call(t, "forget-plan", forgetInput{Refs: []Ref{ref}}).Data.(ForgetPlan)
	f.call(t, "forget-apply", forgetApplyInput{Plan: plan})
	if response, code := f.store.Execute(context.Background(), "capture", r); code != 6 || response.Reason != "SourceForgotten" {
		t.Fatalf("forgotten capture replayed: %+v", response)
	}
	status := f.call(t, "status", map[string]string{"lookupRequestId": r.RequestID}).Data.(map[string]any)
	if status["requestState"] != "forgotten" || status["result"] != nil {
		t.Fatalf("status returned forgotten result: %+v", status)
	}
}

func TestForgetIncludesInterruptedDerivatives(t *testing.T) {
	f := newFixture(t)
	source := f.note(t, testNote("forget-interrupted-source"))
	derived := testNote("interrupted-derivative")
	derived.DerivedFrom = []Derived{{Ref: source, Role: "support"}}
	f.store.fault = func(point string) error {
		if point == "objects" {
			return problem(8, "InjectedFailure")
		}
		return nil
	}
	request := f.request(t, captureInput{Note: &derived})
	if response, code := f.store.Execute(context.Background(), "capture", request); code != 8 {
		t.Fatal(response)
	}
	f.store.fault = nil
	plan := f.call(t, "forget-plan", forgetInput{Refs: []Ref{source}}).Data.(ForgetPlan)
	notes := 0
	for _, id := range plan.ObjectIDs {
		if strings.HasPrefix(id, "note-") {
			notes++
		}
	}
	if notes != 2 {
		t.Fatal("interrupted derivative missing from the forget plan", plan)
	}
	result := f.call(t, "forget-apply", forgetApplyInput{Plan: plan}).Data.(writeResult)
	if result.PurgePending {
		t.Fatal("interrupted derivative purge did not finish")
	}
	for _, file := range plan.Files {
		if _, err := os.Stat(filepath.Join(f.store.Config.Root, file)); !os.IsNotExist(err) {
			t.Fatalf("left forgotten file %s: %v", file, err)
		}
	}
	if response, code := f.store.Execute(context.Background(), "capture", request); code != 3 {
		t.Fatalf("interrupted request resurrected forgotten data: %+v", response)
	}
}

func TestCancellationDoesNotInitializeOrCapture(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.store.Config.Root = filepath.Join(f.home, "cancelled", "memory")
	r := f.request(t, struct{}{})
	if response, code := f.store.Execute(ctx, "init", r); code != 5 || response.Reason != "Cancelled" {
		t.Fatal(response)
	}
	if _, err := os.Stat(filepath.Join(f.home, "cancelled")); !os.IsNotExist(err) {
		t.Fatal("cancelled init created the root")
	}
	text := f.store.NativeContext(ctx, "pi", []byte(`{"platform":"pi-http","cwd":"fixture","user_message":"Remember my preference: do not write this."}`), "contract")
	if !strings.Contains(text, "cancelled") {
		t.Fatal(text)
	}
}

func TestBuiltInDefaultsWithoutPolicy(t *testing.T) {
	f := newFixture(t)
	f.note(t, testNote("no policy required"))
	status := f.call(t, "status", map[string]any{}).Data.(map[string]any)
	learning := status["learning"].(Learning)
	if status["mode"] != "enabled" || learning.Mode != "bounded-auto" || !learning.AllowModelEvaluation || learning.MaxExtraTurnsPer24h != 4 || len(learning.Profiles) != 2 || learning.Profiles[0] != RetrievalProfile || learning.Profiles[1] != ArtifactProfile {
		t.Fatal("unexpected built-in defaults", status)
	}
	data, err := os.ReadFile(filepath.Join(f.store.Config.Root, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"ownerBinding", "namespaceUID", "installationId"} {
		if _, exists := metadata[field]; exists {
			t.Fatalf("store contains retired ownership field %s", field)
		}
	}
}

func TestStaleGenerationAndConcurrentWriters(t *testing.T) {
	f := newFixture(t)
	n := testNote("concurrency")
	request := f.request(t, captureInput{Note: &n})
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := request
			r.RequestID = fmt.Sprintf("request-%d", i)
			_, code := f.store.Execute(context.Background(), "capture", r)
			results <- code
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for code := range results {
		if code == 0 {
			success++
		} else if code != 3 {
			t.Fatalf("unexpected writer error: %d", code)
		}
	}
	if success != 1 {
		t.Fatalf("committed %d writers", success)
	}
}
func TestCancellationBeforeCommit(t *testing.T) {
	f := newFixture(t)
	n := testNote("cancelled")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.store.fault = func(point string) error {
		if point == "manifest" {
			cancel()
		}
		return nil
	}
	response, code := f.store.Execute(ctx, "capture", f.request(t, captureInput{Note: &n}))
	if code != 5 || response.Reason != "Cancelled" {
		t.Fatal(response)
	}
	v, err := f.store.open()
	if err != nil {
		t.Fatal(err)
	}
	defer v.fs.close()
	if v.m.Generation != 1 {
		t.Fatal("cancellation did not prevent HEAD switch")
	}
}

func TestRequestRestrictionsAndForgedAssurance(t *testing.T) {
	f := newFixture(t)
	n := testNote("private")
	for _, change := range []func(*Request){func(r *Request) { r.RequestContext.NoCapture = true }, func(r *Request) { r.RequestContext.NoMemory = true }, func(r *Request) { r.RequestContext.Audience = "shared" }, func(r *Request) { r.RequestContext.Destination.Kind = "public" }} {
		r := f.request(t, captureInput{Note: &n})
		change(&r)
		if response, code := f.store.Execute(context.Background(), "capture", r); code != 4 {
			t.Fatal(response)
		}
	}
	n.Sources[0].Assurance = "external-verified"
	if response, code := f.store.Execute(context.Background(), "capture", f.request(t, captureInput{Note: &n})); code != 2 {
		t.Fatal(response)
	}
}
func TestExternalEditsArePreserved(t *testing.T) {
	f := newFixture(t)
	ref := f.note(t, testNote("owner"))
	name := filepath.Join(f.store.Config.Root, "knowledge", ref.ID+".md")
	edited := []byte("owner content")
	if err := os.WriteFile(name, edited, 0600); err != nil {
		t.Fatal(err)
	}
	n := testNote("another")
	response, code := f.store.Execute(context.Background(), "capture", f.request(t, captureInput{Note: &n}))
	if code != 3 || response.Reason != "DataModifiedOutsideHelper" {
		t.Fatal(response)
	}
	b, _ := os.ReadFile(name)
	if string(b) != string(edited) {
		t.Fatal("overwrote owner edit")
	}
}
func TestSourceReviewCannotRefreshItself(t *testing.T) {
	f := newFixture(t)
	n := testNote("live")
	n.TemporalClass = "live"
	n.Revalidation.Mode = "on-use"
	ref := f.note(t, n)
	f.call(t, "record-source-check", sourceCheckInput{Check: SourceCheck{TargetRef: ref, SourceRef: n.Sources[0].Ref, Result: "matches"}, Summary: "reported only"})
	q := f.call(t, "query", Query{QueryTerms: []string{"live"}}).Data.(QueryResult)
	if len(q.Hits) != 1 || !q.Hits[0].Review.NeedsRecheck || q.Hits[0].Review.Assurance != "reported" || q.Hits[0].Review.LastCheckedAt == nil {
		t.Fatalf("forged qualification: %+v", q)
	}
	n = q.Hits[0].Note
	f.call(t, "revise", reviseInput{Ref: ref, Note: n, Reason: "copy-edit"})
	q = f.call(t, "query", Query{QueryTerms: []string{"live"}}).Data.(QueryResult)
	if q.Hits[0].Review.LastCheckedAt != nil || !q.Hits[0].Review.NeedsRecheck {
		t.Fatal("copy-edit reused check")
	}
}
func TestForgetPurgesDerivedVersionsAndLateWrites(t *testing.T) {
	f := newFixture(t)
	source := f.note(t, testNote("source"))
	derived := testNote("derived")
	derived.DerivedFrom = []Derived{{Ref: source, Role: "counterexample"}}
	child := f.note(t, derived)
	late := f.request(t, captureInput{Note: &derived})
	plan := f.call(t, "forget-plan", forgetInput{Refs: []Ref{source}}).Data.(ForgetPlan)
	if !strings.Contains(strings.Join(plan.ObjectIDs, ","), child.ID) {
		t.Fatal("forgot counterexample dependency")
	}
	result := f.call(t, "forget-apply", forgetApplyInput{Plan: plan}).Data.(writeResult)
	if result.PurgePending {
		t.Fatal("purge did not complete")
	}
	if response, code := f.store.Execute(context.Background(), "capture", late); code != 3 {
		t.Fatalf("late write resurrected memory: %+v", response)
	}
	for _, file := range plan.Files {
		if _, err := os.Stat(filepath.Join(f.store.Config.Root, file)); !os.IsNotExist(err) {
			t.Fatalf("not purged: %s (%v)", file, err)
		}
	}
	v, err := f.store.open()
	if err != nil {
		t.Fatal(err)
	}
	defer v.fs.close()
	if !v.m.Tombstones[source.ID] || !v.m.Tombstones[child.ID] {
		t.Fatal("tombstones missing")
	}
}
func TestImportDoesNotInheritNativeAuthority(t *testing.T) {
	f := newFixture(t)
	file := filepath.Join(f.home, "old-memory.md")
	os.WriteFile(file, []byte("A historical statement, not permission."), 0600)
	plan := f.call(t, "import-plan", importInput{Files: []string{file}}).Data.(ImportPlan)
	f.call(t, "import-apply", importApplyInput{Plan: plan})
	q := f.call(t, "query", Query{QueryTerms: []string{"old-memory.md"}}).Data.(QueryResult)
	if len(q.Hits) != 1 || q.Hits[0].Note.Sources[0].Assurance != "reported" || !q.Hits[0].Review.NeedsRecheck {
		t.Fatal(q)
	}
	if response, code := f.store.Execute(context.Background(), "import-plan", f.request(t, importInput{Files: []string{"/etc/passwd"}})); code != 2 {
		t.Fatal(response)
	}
}
