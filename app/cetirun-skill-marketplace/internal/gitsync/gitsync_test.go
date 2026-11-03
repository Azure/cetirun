// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/skills"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func commitFiles(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err == git.ErrRepositoryNotExists {
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

func TestResolveAndFetchLocalRepository(t *testing.T) {
	dir := t.TempDir()
	first := commitFiles(t, dir, map[string]string{
		"skills/pdf-tools/SKILL.md": "---\nname: pdf-tools\ndescription: PDF\n---\n",
		"README.md":                 "x",
	})
	source := Source{URL: dir, Auth: AuthNone}
	ctx := context.Background()
	ref, hash, err := source.Resolve(ctx)
	if err != nil || hash.String() != first {
		t.Fatalf("resolve: %v %v %v", ref, hash, err)
	}
	snapshot, err := source.Fetch(ctx, ref, 1<<20)
	if err != nil || snapshot.Commit != first {
		t.Fatalf("fetch: %v", err)
	}
	found, problems, err := skills.Discover(snapshot, "skills", skills.DefaultLimits, nil)
	if err != nil || len(found) != 1 || len(problems) != 0 || found[0].Name != "pdf-tools" {
		t.Fatalf("discover: %+v %+v %v", found, problems, err)
	}
	second := commitFiles(t, dir, map[string]string{"skills/pdf-tools/notes.md": "more"})
	_, hash, err = source.Resolve(ctx)
	if err != nil || hash.String() != second {
		t.Fatalf("a new commit must resolve: %v %v", hash, err)
	}
	if _, err := source.Fetch(ctx, ref, 4); err == nil {
		t.Fatalf("a commit larger than the limit must be refused")
	}
	if _, _, err := (Source{URL: dir, Ref: "no-such-branch", Auth: AuthNone}).Resolve(ctx); err == nil {
		t.Fatalf("an unknown ref must fail")
	}
}

func TestValidateAndRedact(t *testing.T) {
	for _, tc := range []struct {
		source Source
		ok     bool
	}{
		{Source{URL: "https://git.example.invalid/skills.git", Auth: AuthToken}, true},
		{Source{URL: "https://user:pass@git.example.invalid/skills.git", Auth: AuthToken}, false},
		{Source{URL: "http://git.example.invalid/skills.git", Auth: AuthNone}, false},
		{Source{URL: "http://git.example.invalid/skills.git", Auth: AuthNone, AllowInsecureHTTP: true}, true},
		{Source{URL: "git@github.com:org/skills.git", Auth: AuthSSH}, true},
		{Source{URL: "ssh://git@github.com/org/skills.git", Auth: AuthSSH}, true},
		{Source{URL: "https://git.example.invalid/skills.git", Auth: "basic"}, false},
		{Source{URL: "https://git.example.invalid/skills.git", Auth: AuthNone, Ref: "a..b"}, false},
	} {
		if err := tc.source.Validate(); (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc.source, err)
		}
	}
	if got := Redacted("https://user:pass@git.example.invalid/x.git"); got != "https://git.example.invalid/x.git" {
		t.Fatalf("redacted %q", got)
	}
	if got := Redacted("git@github.com:org/skills.git"); got != "github.com:org/skills.git" {
		t.Fatalf("redacted %q", got)
	}
}

func TestTokenIsReadFromTheCredentialsDirectory(t *testing.T) {
	dir := t.TempDir()
	source := Source{URL: "https://git.example.invalid/x.git", Auth: AuthToken, CredentialsDir: dir}
	if _, err := source.auth(); err == nil {
		t.Fatal("a missing token must fail")
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("synthetic-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := source.auth()
	if err != nil || auth.String() == "" {
		t.Fatalf("token auth: %v", err)
	}
	if _, err := (Source{URL: "git@h:x", Auth: AuthSSH, CredentialsDir: dir}).auth(); err == nil {
		t.Fatal("SSH without a key and known_hosts must fail")
	}
}

// commitRawTree commits files without go-git's worktree, which refuses the
// unsafe names that a crafted repository can still contain.
func commitRawTree(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	store := func(object interface {
		Encode(plumbing.EncodedObject) error
	}, kind plumbing.ObjectType) plumbing.Hash {
		encoded := repo.Storer.NewEncodedObject()
		encoded.SetType(kind)
		if err := object.Encode(encoded); err != nil {
			t.Fatal(err)
		}
		hash, err := repo.Storer.SetEncodedObject(encoded)
		if err != nil {
			t.Fatal(err)
		}
		return hash
	}
	var build func(prefix string) plumbing.Hash
	build = func(prefix string) plumbing.Hash {
		entries := map[string]object.TreeEntry{}
		for name, content := range files {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			part, _, nested := strings.Cut(strings.TrimPrefix(name, prefix), "/")
			if nested {
				if _, ok := entries[part]; !ok {
					entries[part] = object.TreeEntry{Name: part, Mode: filemode.Dir, Hash: build(prefix + part + "/")}
				}
				continue
			}
			blob := repo.Storer.NewEncodedObject()
			blob.SetType(plumbing.BlobObject)
			writer, _ := blob.Writer()
			_, _ = writer.Write([]byte(content))
			_ = writer.Close()
			hash, err := repo.Storer.SetEncodedObject(blob)
			if err != nil {
				t.Fatal(err)
			}
			entries[part] = object.TreeEntry{Name: part, Mode: filemode.Regular, Hash: hash}
		}
		tree := &object.Tree{}
		for _, entry := range entries {
			tree.Entries = append(tree.Entries, entry)
		}
		sort.Slice(tree.Entries, func(i, j int) bool {
			key := func(e object.TreeEntry) string {
				if e.Mode == filemode.Dir {
					return e.Name + "/"
				}
				return e.Name
			}
			return key(tree.Entries[i]) < key(tree.Entries[j])
		})
		return store(tree, plumbing.TreeObject)
	}
	signature := object.Signature{Name: "test", Email: "test@example.invalid", When: time.Now()}
	commit := store(&object.Commit{Author: signature, Committer: signature, Message: "raw", TreeHash: build("")}, plumbing.CommitObject)
	if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.Master, commit)); err != nil {
		t.Fatal(err)
	}
	return commit.String()
}

func TestAnUnsafeNameRejectsOnlyItsSkill(t *testing.T) {
	dir := t.TempDir()
	commit := commitRawTree(t, dir, map[string]string{
		"skills/good/SKILL.md":       "---\nname: good\ndescription: Valid\n---\n",
		"skills/control/SKILL.md":    "---\nname: control\ndescription: Unsafe file name\n---\n",
		"skills/control/bad\x01name": "x",
		"skills/dot-git/SKILL.md":    "---\nname: dot-git\ndescription: Holds a .git directory\n---\n",
		"skills/dot-git/.git/config": "[core]\n",
		"docs/bad\x02name.md":        "outside every skill",
	})
	source := Source{URL: dir, Auth: AuthNone}
	ctx := context.Background()
	ref, _, err := source.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Fetch(ctx, ref, 1<<20)
	if err != nil || snapshot.Commit != commit {
		t.Fatalf("an unsafe name must not stop the sync: %v", err)
	}
	found, problems, err := skills.Discover(snapshot, "skills", skills.DefaultLimits, nil)
	if err != nil || len(found) != 1 || found[0].Name != "good" {
		t.Fatalf("discover: %+v %v", found, err)
	}
	rejected := map[string]string{}
	for _, problem := range problems {
		rejected[problem.Path] = problem.Reason
	}
	if len(rejected) != 2 || !strings.Contains(rejected["skills/control"], "not allowed") ||
		!strings.Contains(rejected["skills/dot-git"], "not allowed") {
		t.Fatalf("problems: %+v", problems)
	}
}
