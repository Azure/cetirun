// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package memory

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Azure/CetiRun/app/cetirun-memory-context/assets"
)

func TestAssetInstallIsVerifiedAndDoesNotOverwriteForeignFiles(t *testing.T) {
	dir := t.TempDir()
	source := fstest.MapFS{"AGENTS.md": {Data: []byte("fixed contract")}, "skills/cetirun-memory/SKILL.md": {Data: []byte("fixed skill")}}
	if err := InstallAssets(dir, source, []byte("fixed executable")); err != nil {
		t.Fatal(err)
	}
	if err := InstallAssets(dir, source, []byte("fixed executable")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "bin/cetirun-memory"))
	if err != nil || info.Mode().Perm() != 0555 {
		t.Fatal("binary permissions", err)
	}
	info, err = os.Stat(filepath.Join(dir, "skills/cetirun-memory"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("asset directory permissions", err)
	}
	if err := InstallAssets(dir, source, []byte("different executable")); err == nil {
		t.Fatal("replaced immutable asset")
	}
	foreign := t.TempDir()
	os.WriteFile(filepath.Join(foreign, "owner-file"), []byte("keep"), 0600)
	if err := InstallAssets(foreign, source, []byte("binary")); err == nil {
		t.Fatal("adopted foreign directory")
	}
}

func TestCanonicalInstructionsReferenceInstalledSkill(t *testing.T) {
	dir := t.TempDir()
	if err := InstallAssets(dir, assets.Files, []byte("fixture executable")); err != nil {
		t.Fatal(err)
	}
	instructions, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile("`([^`]+/SKILL\\.md)`").FindSubmatch(instructions)
	if len(match) != 2 {
		t.Fatal("canonical instructions must identify the installed skill")
	}
	const mount = "/opt/cetirun-memory/"
	reference := string(match[1])
	if !strings.HasPrefix(reference, mount) {
		t.Fatal("canonical instructions direct the agent outside the immutable assets")
	}
	relative := strings.TrimPrefix(reference, mount)
	if !fs.ValidPath(relative) {
		t.Fatal("canonical skill reference escapes the asset root")
	}
	info, err := os.Stat(filepath.Join(dir, relative))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0444 {
		t.Fatal("canonical skill is not installed read-only", err)
	}
}
