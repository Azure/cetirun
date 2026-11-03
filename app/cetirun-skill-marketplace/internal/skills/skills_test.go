// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package skills

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
	"path"
	"sort"
	"strings"
	"testing"
)

// fakeTree is an in-memory Tree. Directories are implied by file paths.
type fakeTree struct {
	files map[string]string
	modes map[string]Mode
}

func newTree(files map[string]string) *fakeTree {
	return &fakeTree{files: files, modes: map[string]Mode{}}
}

func (t *fakeTree) Walk(root string, fn func(Entry) error) error {
	dirs := map[string]bool{}
	var names []string
	for name := range t.files {
		if root != "" && !strings.HasPrefix(name, root+"/") {
			continue
		}
		names = append(names, name)
		for dir := path.Dir(name); dir != "." && dir != root; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	for dir := range dirs {
		names = append(names, dir+"/")
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.HasSuffix(name, "/") {
			if err := fn(Entry{Path: strings.TrimSuffix(name, "/"), Mode: Dir}); err != nil {
				return err
			}
			continue
		}
		content := t.files[name]
		mode, ok := t.modes[name]
		if !ok {
			mode = Regular
		}
		if err := fn(Entry{Path: name, Mode: mode, Size: int64(len(content)),
			Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(content)), nil }}); err != nil {
			return err
		}
	}
	return nil
}

func (t *fakeTree) TreeHash(dir string) (string, error) {
	hash := sha1.New()
	var names []string
	for name := range t.files {
		if strings.HasPrefix(name, dir+"/") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		hash.Write([]byte(name + "\x00" + t.files[name] + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func skillMD(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n# " + name + "\n"
}

func TestDiscover(t *testing.T) {
	tree := newTree(map[string]string{
		"skills/pdf-tools/SKILL.md":            skillMD("pdf-tools", "Read and write PDF files"),
		"skills/pdf-tools/scripts/convert.sh":  "#!/bin/sh\n",
		"skills/pdf-tools/nested/SKILL.md":     skillMD("nested", "Inner skill"),
		"skills/wrong/SKILL.md":                skillMD("other", "Name differs"),
		"skills/no-front/SKILL.md":             "# no frontmatter\n",
		"skills/linked/SKILL.md":               skillMD("linked", "Has a link"),
		"skills/linked/link":                   "target",
		"skills/cetirun-memory/SKILL.md":       skillMD("cetirun-memory", "Reserved"),
		"skills/a/dup/SKILL.md":                skillMD("dup", "First"),
		"skills/b/dup/SKILL.md":                skillMD("dup", "Second"),
		"skills/.hidden/secret/SKILL.md":       skillMD("secret", "Hidden"),
		"skills/Upper/SKILL.md":                skillMD("Upper", "Invalid name"),
		"skills/no-description/SKILL.md":       "---\nname: no-description\n---\n",
		"README.md":                            "outside",
		"skills/pdf-tools/references/notes.md": "notes",
	})
	tree.modes["skills/linked/link"] = Symlink
	tree.modes["skills/pdf-tools/scripts/convert.sh"] = Executable
	found, problems, err := Discover(tree, "skills", DefaultLimits, DefaultReserved)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Name != "pdf-tools" || found[0].Description != "Read and write PDF files" || len(found[0].Version) != 12 {
		t.Fatalf("unexpected skills: %+v", found)
	}
	var executable bool
	for _, file := range found[0].Files {
		if file.RelPath == "scripts/convert.sh" {
			executable = file.Executable
		}
	}
	if !executable || len(found[0].Files) != 4 {
		t.Fatalf("files: %+v", found[0].Files)
	}
	reasons := map[string]string{}
	for _, problem := range problems {
		reasons[problem.Path] = problem.Reason
	}
	for dir, want := range map[string]string{
		"skills/pdf-tools/nested": "nested inside skill pdf-tools",
		"skills/wrong":            "differs from the directory name",
		"skills/no-front":         "frontmatter",
		"skills/linked":           "symbolic link",
		"skills/cetirun-memory":   "reserved",
		"skills/a/dup":            "duplicate",
		"skills/b/dup":            "duplicate",
		"skills/Upper":            "not a valid skill name",
		"skills/no-description":   "description",
	} {
		if !strings.Contains(reasons[dir], want) {
			t.Errorf("%s: reason %q, want %q", dir, reasons[dir], want)
		}
	}
	if _, ok := reasons["skills/.hidden/secret"]; ok {
		t.Fatalf("hidden directories must not be searched")
	}
}

func TestDiscoverLimits(t *testing.T) {
	files := map[string]string{"big/SKILL.md": skillMD("big", "Too many files")}
	for i := 0; i < 3; i++ {
		files["big/f"+string(rune('a'+i))] = "x"
	}
	limits := DefaultLimits
	limits.MaxFiles = 3
	_, problems, err := Discover(newTree(files), "", limits, nil)
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0].Reason, "files") {
		t.Fatalf("file limit: %v %+v", err, problems)
	}
	limits = DefaultLimits
	limits.MaxDepth = 2
	_, problems, _ = Discover(newTree(map[string]string{"deep/SKILL.md": skillMD("deep", "d"), "deep/a/b/c": "x"}), "", limits, nil)
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "deeper") {
		t.Fatalf("depth limit: %+v", problems)
	}
}

func TestDiscoverRootSkillIsRejected(t *testing.T) {
	_, problems, _ := Discover(newTree(map[string]string{"skills/SKILL.md": skillMD("skills", "root")}), "skills", DefaultLimits, nil)
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "below the repository path") {
		t.Fatalf("root SKILL.md: %+v", problems)
	}
}

func TestDiscoverRejectsOnlyTheSkillWithAnUnsafeName(t *testing.T) {
	files := map[string]string{
		"skills/good/SKILL.md":    skillMD("good", "Valid"),
		"docs/bad\x01name.md":     "outside every skill",
		"docs/.git/config":        "outside every skill",
		"skills/good/.github/x":   "a dot directory that is not .git",
		"skills/good/notes\u00e9": "a non-ASCII name",
	}
	bad := map[string]string{
		"control":   "bad\x01name.txt",
		"delete":    "bad\x7fname.txt",
		"c1":        "bad\u0085name.txt",
		"dot-git":   ".git/config",
		"upper-git": ".GIT/config",
		"git-dots":  ".git. /hooks/post-checkout",
		"short-git": "GIT~1/config",
		"backslash": "a\\b.txt",
	}
	for name, file := range bad {
		files["skills/"+name+"/SKILL.md"] = skillMD(name, "Has an unsafe name")
		files["skills/"+name+"/"+file] = "x"
	}
	found, problems, err := Discover(newTree(files), "skills", DefaultLimits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Name != "good" || len(found[0].Files) != 3 {
		t.Fatalf("only the valid skill must be found: %+v", found)
	}
	reasons := map[string]string{}
	for _, problem := range problems {
		reasons[problem.Path] = problem.Reason
	}
	for name := range bad {
		if !strings.Contains(reasons["skills/"+name], "is not allowed") {
			t.Errorf("%s: reason %q", name, reasons["skills/"+name])
		}
	}
	if len(problems) != len(bad) {
		t.Fatalf("names outside every skill must not be problems: %+v", problems)
	}
}

func TestFrontmatter(t *testing.T) {
	for _, tc := range []struct{ content, reason string }{
		{"---\r\nname: a\r\ndescription: b\r\n---\r\n", ""},
		{"\ufeff---\nname: a\ndescription: b\n---\n", ""},
		{"---\nname: a\ndescription: b\n", "not closed"},
		{"---\nname: [a\n---\n", "not valid YAML"},
		{"---\nname: a\ndescription: " + strings.Repeat("x", 1025) + "\n---\n", "longer"},
		{"---\nname: a\ndescription: \"bad\\u0007\"\n---\n", "control"},
	} {
		_, _, reason := frontmatter([]byte(tc.content))
		if (tc.reason == "") != (reason == "") || !strings.Contains(reason, tc.reason) {
			t.Errorf("%q: reason %q, want %q", tc.content, reason, tc.reason)
		}
	}
}
