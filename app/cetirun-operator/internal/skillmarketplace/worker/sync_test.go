// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/skillmarketplace/gitsync"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/skillmarketplace/skills"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-logr/logr/funcr"
)

// commitFiles commits files to the repository in dir, which it creates when
// needed. An empty content removes the file. It returns the commit.
func commitFiles(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		repo, err = git.PlainInit(dir, false)
	}
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if content == "" {
			if _, err := tree.Remove(name); err != nil {
				t.Fatal(err)
			}
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := tree.Add(name); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := tree.Commit("update", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@example.invalid", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	return hash.String()
}

// gitServer serves the repositories below root over smart HTTP with git
// http-backend. It counts the fetches, the upload-pack requests, and refuses
// them while failFetch is set, as a server that is not available.
func gitServer(t *testing.T, root string) (url string, fetches *atomic.Int32, failFetch *atomic.Bool) {
	t.Helper()
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not installed")
	}
	backend := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not installed")
	}
	handler := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	fetches, failFetch = &atomic.Int32{}, &atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/git-upload-pack") {
			fetches.Add(1)
			if failFetch.Load() {
				http.Error(w, "synthetic outage", http.StatusServiceUnavailable)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server.URL, fetches, failFetch
}

// A commit that cannot be loaded because of its own content is read once:
// later checks report its error without reading it again. A commit after a
// failed connection is read again at the next check.
func TestACommitThatCannotBeLoadedIsReadOnce(t *testing.T) {
	skill := "---\nname: pdf-tools\ndescription: PDF\n---\n"
	notes, removed := map[string]string{}, map[string]string{}
	for i := range 5 {
		name := fmt.Sprintf("skills/pdf-tools/notes-%d.md", i)
		notes[name], removed[name] = fmt.Sprintf("synthetic note %d\n", i), ""
	}
	for _, tc := range []struct {
		name   string
		fetch  gitsync.Limits
		limits func(*skills.Limits)
		reason string
	}{
		// With the notes, the commit has 10 objects.
		{"fetch limit", gitsync.Limits{MaxBytes: 1 << 20, MaxObjects: 8}, func(*skills.Limits) {}, "more than 8"},
		// With the notes, the repository path holds 7 files and directories.
		{"tree limit", gitsync.Limits{MaxBytes: 1 << 20, MaxObjects: 100}, func(l *skills.Limits) { l.MaxTreeEntries = 4 },
			"more than 4 files and directories"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "skills")
			first := commitFiles(t, dir, map[string]string{"skills/pdf-tools/SKILL.md": skill})
			url, fetches, failFetch := gitServer(t, root)
			limits := skills.DefaultLimits
			tc.limits(&limits)
			fetch := tc.fetch
			fetch.TempDir = t.TempDir()
			kube := newClient(t)
			s := &Syncer{
				Source: gitsync.Source{URL: url + "/skills", Auth: gitsync.AuthNone, AllowInsecureHTTP: true},
				Path:   "skills", Limits: limits, Fetch: fetch, Store: &Store{},
				Catalog: NewCatalog(kube, kube, namespace, api.SkillMarketplaceRepository{URL: url + "/skills"}),
			}
			ctx := context.Background()
			loaded := func(want string) {
				t.Helper()
				if commit, _ := s.Store.Snapshot(); commit != want {
					t.Fatalf("loaded commit %q, want %q", commit, want)
				}
			}
			if err := s.SyncOnce(ctx); err != nil {
				t.Fatal(err)
			}
			loaded(first)

			commitFiles(t, dir, notes)
			before := fetches.Load()
			var errs []error
			for range 3 {
				errs = append(errs, s.SyncOnce(ctx))
			}
			for _, err := range errs {
				if !errors.Is(err, gitsync.ErrLimit) || !strings.Contains(err.Error(), tc.reason) || err.Error() != errs[0].Error() {
					t.Fatalf("each check of the refused commit must report its limit: %v", errs)
				}
			}
			if read := fetches.Load() - before; read != 1 {
				t.Fatalf("the refused commit was read %d times, want once", read)
			}
			loaded(first)

			// A failed connection is no property of the commit.
			second := commitFiles(t, dir, removed)
			failFetch.Store(true)
			if err := s.SyncOnce(ctx); err == nil || errors.Is(err, gitsync.ErrLimit) {
				t.Fatalf("a failed fetch must fail without a limit: %v", err)
			}
			failFetch.Store(false)
			before = fetches.Load()
			if err := s.SyncOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if read := fetches.Load() - before; read != 1 {
				t.Fatalf("the commit after a failed fetch was read %d times, want once", read)
			}
			loaded(second)
		})
	}
}

// A lasting failure logs its error once, one short line for each repeated
// check, and its end once.
func TestARepeatedSyncFailureIsLoggedOnce(t *testing.T) {
	var lines []string
	logger := funcr.New(func(_, args string) { lines = append(lines, args) }, funcr.Options{})
	unreachable := errors.New("repository access was refused: check the credentials Secret")
	refused := fmt.Errorf("%w: the Git server sent more than 1024 bytes for the commit", gitsync.ErrLimit)
	var failures failureLog
	for _, err := range []error{nil, unreachable, unreachable, unreachable, refused, refused, nil, nil} {
		failures.report(logger, err, "https://git.example.invalid/skills.git")
	}
	const again, start = `"msg"="the skills repository check failed again with the same error"`, `"msg"="synchronize the skills repository"`
	want := [][]string{
		{start, `"error"="` + unreachable.Error() + `"`},
		{again, `"failedChecks"=2`},
		{again, `"failedChecks"=3`},
		{start, `"error"="` + refused.Error() + `"`},
		{again, `"failedChecks"=5`},
		{`"msg"="the skills repository check succeeds again"`, `"failedChecks"=5`},
	}
	if len(lines) != len(want) {
		t.Fatalf("logged %d lines, want %d:\n%s", len(lines), len(want), strings.Join(lines, "\n"))
	}
	for i, fragments := range want {
		for _, fragment := range append(fragments, `"repository"="https://git.example.invalid/skills.git"`) {
			if !strings.Contains(lines[i], fragment) {
				t.Errorf("line %d %s lacks %s", i, lines[i], fragment)
			}
		}
		// A repeated failure does not log the error again.
		if fragments[0] == again && strings.Contains(lines[i], `"error"=`) {
			t.Errorf("line %d repeats the error: %s", i, lines[i])
		}
	}
}
