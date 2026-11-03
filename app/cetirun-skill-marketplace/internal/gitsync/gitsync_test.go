// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/skills"
	git "github.com/go-git/go-git/v5"
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
