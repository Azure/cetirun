// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package publish

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

func content(name, version string, files map[string]string) SkillContent {
	skill := SkillContent{Name: name, Version: version}
	for rel, data := range files {
		data := data
		skill.Files = append(skill.Files, File{RelPath: rel, Executable: strings.HasSuffix(rel, ".sh"),
			Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(data)), nil }})
	}
	return skill
}

const uid = "00000000-0000-4000-8000-000000000001"

func TestReleaseIDIsContentAddressed(t *testing.T) {
	a := ReleaseID("sha256:x", []api.SkillMarketplaceReleaseSkill{{Name: "a", Version: "1"}, {Name: "b", Version: "2"}})
	b := ReleaseID("sha256:x", []api.SkillMarketplaceReleaseSkill{{Name: "b", Version: "2"}, {Name: "a", Version: "1"}})
	c := ReleaseID("sha256:y", []api.SkillMarketplaceReleaseSkill{{Name: "a", Version: "1"}, {Name: "b", Version: "2"}})
	if a != b || a == c || !api.ValidSkillMarketplaceReleaseID(a) {
		t.Fatalf("release IDs %q %q %q", a, b, c)
	}
}

func TestPublishLayoutAndIdempotence(t *testing.T) {
	root := t.TempDir()
	p := &Publisher{Root: root}
	if err := p.Init(); err != nil {
		t.Fatal(err)
	}
	digest := api.SkillMarketplaceSelectionDigest([]string{"pdf-tools"})
	release, changed, err := p.Publish(TeamRef{Name: "research", UID: uid}, digest,
		[]SkillContent{content("pdf-tools", "0123456789ab", map[string]string{"SKILL.md": "x", "scripts/run.sh": "#!/bin/sh"})}, "c1")
	if err != nil || !changed {
		t.Fatalf("publish: %v %v", changed, err)
	}
	team := filepath.Join(root, "teams", uid)
	current, err := os.ReadFile(filepath.Join(team, "CURRENT"))
	if err != nil || strings.TrimSpace(string(current)) != release.ID {
		t.Fatalf("CURRENT %q, release %q, err %v", current, release.ID, err)
	}
	selection, _ := os.ReadFile(filepath.Join(team, "releases", release.ID, ".cetirun-selection"))
	if string(selection) != digest+"\n" {
		t.Fatalf("selection file %q", selection)
	}
	info, err := os.Stat(filepath.Join(team, "releases", release.ID, "pdf-tools", "scripts", "run.sh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("executable file: %v %v", info, err)
	}
	if _, changed, err := p.Publish(TeamRef{Name: "research", UID: uid}, digest,
		[]SkillContent{content("pdf-tools", "0123456789ab", map[string]string{"SKILL.md": "x"})}, "c2"); err != nil || changed {
		t.Fatalf("same content must not publish again: %v %v", changed, err)
	}
	id, read, err := p.ReadCurrent(uid)
	if err != nil || id != release.ID || read == nil || len(read.Skills) != 1 {
		t.Fatalf("ReadCurrent %q %+v %v", id, read, err)
	}
	files, err := p.ReleaseSkillFiles(uid, release.ID, "pdf-tools")
	if err != nil || len(files) != 2 {
		t.Fatalf("release files %+v %v", files, err)
	}
	digest2 := api.SkillMarketplaceSelectionDigest([]string{"pdf-tools", "web"})
	second, changed, err := p.Publish(TeamRef{Name: "research", UID: uid}, digest2,
		[]SkillContent{{Name: "pdf-tools", Version: "0123456789ab", Files: files}, content("web", "ba9876543210", map[string]string{"SKILL.md": "w"})}, "c3")
	if err != nil || !changed || second.ID == release.ID {
		t.Fatalf("new selection: %v %v %v", second.ID, changed, err)
	}
	current, _ = os.ReadFile(filepath.Join(team, "CURRENT"))
	if strings.TrimSpace(string(current)) != second.ID {
		t.Fatalf("CURRENT must move to the new release")
	}
	if data, _ := os.ReadFile(filepath.Join(team, "releases", second.ID, "pdf-tools", "SKILL.md")); string(data) != "x" {
		t.Fatalf("retained skill copied from the previous release: %q", data)
	}
}

func TestGC(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	p := &Publisher{Root: root, Now: func() time.Time { return now }}
	if err := p.Init(); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, name := range []string{"a", "b", "c", "d"} {
		release, _, err := p.Publish(TeamRef{Name: "t", UID: uid}, "sha256:"+name, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, release.ID)
		old := now.Add(-time.Duration(4-i) * time.Hour)
		if err := os.Chtimes(filepath.Join(root, "teams", uid, "releases", release.ID), old, old); err != nil {
			t.Fatal(err)
		}
	}
	tmp := filepath.Join(root, "teams", uid, "releases", ".tmp-abandoned")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-2 * time.Hour)
	_ = os.Chtimes(tmp, old, old)
	if err := p.GC(uid, 2, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		_, err := os.Stat(filepath.Join(root, "teams", uid, "releases", id))
		if exists := err == nil; exists != (i >= 1) {
			t.Errorf("release %d exists=%v", i, exists)
		}
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("abandoned temporary release must be removed")
	}
}

func TestInitRefusesForeignRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "other"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&Publisher{Root: root}).Init(); err == nil {
		t.Fatal("a root with foreign data must be refused")
	}
}

func TestInvalidUIDIsRefused(t *testing.T) {
	p := &Publisher{Root: t.TempDir()}
	for _, bad := range []string{"", "../x", "a/b", ".hidden"} {
		if err := p.EnsureTeam(bad); err == nil {
			t.Errorf("UID %q must be refused", bad)
		}
	}
}
