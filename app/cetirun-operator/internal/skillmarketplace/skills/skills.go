// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package skills finds and validates the skills of a repository tree. It
// reads content only; it never executes anything from the repository.
package skills

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"sigs.k8s.io/yaml"
)

// Mode is the kind of a tree entry.
type Mode int

const (
	Regular Mode = iota
	Executable
	Dir
	Symlink
	Submodule
)

// Entry is one entry of a repository tree. Path is slash-separated and
// relative to the repository root.
type Entry struct {
	Path string
	Mode Mode
	Size int64
	Open func() (io.ReadCloser, error)
}

// Tree is a read-only repository tree.
type Tree interface {
	// Walk visits every entry below root (root itself excluded), parents first.
	Walk(root string, fn func(Entry) error) error
	// TreeHash returns the content hash of a directory.
	TreeHash(dir string) (string, error)
}

// Limits bound what a single skill may contain.
type Limits struct {
	MaxFiles   int
	MaxBytes   int64
	MaxSkillMD int64
	MaxDepth   int
	// MaxDescription is the longest SKILL.md description, in characters.
	MaxDescription int
}

// DefaultLimits are the limits used unless configured otherwise. The
// description limit is the one of the Agent Skills specification; the others
// keep a skill small enough to copy into the Pod of each Agent.
var DefaultLimits = Limits{MaxFiles: 500, MaxBytes: 5 << 20, MaxSkillMD: 256 << 10, MaxDepth: 8, MaxDescription: 1024}

// DefaultReserved are the names of skills that CetiRun or its images supply.
var DefaultReserved = []string{"cetirun-memory", "connected-services"}

// File is one file of a skill, relative to the skill directory.
type File struct {
	RelPath    string
	Executable bool
	Size       int64
	Open       func() (io.ReadCloser, error)
}

// Skill is one valid skill.
type Skill struct {
	Name        string
	Description string
	Path        string
	Version     string
	Files       []File
	Bytes       int64
}

const skillFile = "SKILL.md"

// Discover returns the valid skills below root and the rejected directories.
func Discover(tree Tree, root string, limits Limits, reserved []string) ([]Skill, []api.SkillMarketplaceProblem, error) {
	root = strings.Trim(root, "/")
	var all []Entry
	if err := tree.Walk(root, func(entry Entry) error {
		all = append(all, entry)
		return nil
	}); err != nil {
		return nil, nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Path < all[j].Path })

	// Skill directories: directories that hold SKILL.md, not below a hidden
	// directory, outermost first.
	var candidates []string
	for _, entry := range all {
		if path.Base(entry.Path) != skillFile || (entry.Mode != Regular && entry.Mode != Executable) {
			continue
		}
		dir := path.Dir(entry.Path)
		if dir == "." {
			dir = ""
		}
		if hiddenBelow(root, dir) {
			continue
		}
		candidates = append(candidates, dir)
	}
	sort.Strings(candidates)

	var problems []api.SkillMarketplaceProblem
	var outer []string
	for _, dir := range candidates {
		if parent := enclosing(outer, dir); parent != "" {
			problems = append(problems, api.SkillMarketplaceProblem{Path: dir, Name: path.Base(dir),
				Reason: fmt.Sprintf("nested inside skill %s; only the outer skill is published", path.Base(parent))})
			continue
		}
		if dir == root {
			problems = append(problems, api.SkillMarketplaceProblem{Path: displayPath(dir),
				Reason: "SKILL.md must be in a skill directory below the repository path, not at the path itself"})
			continue
		}
		outer = append(outer, dir)
	}

	byName := map[string][]int{}
	var found []Skill
	for _, dir := range outer {
		skill, reason := validate(tree, all, dir, limits, reserved)
		if reason != "" {
			problems = append(problems, api.SkillMarketplaceProblem{Path: dir, Name: path.Base(dir), Reason: reason})
			continue
		}
		byName[skill.Name] = append(byName[skill.Name], len(found))
		found = append(found, skill)
	}
	var skills []Skill
	for _, skill := range found {
		if len(byName[skill.Name]) > 1 {
			problems = append(problems, api.SkillMarketplaceProblem{Path: skill.Path, Name: skill.Name,
				Reason: "duplicate skill name; every directory with this name is rejected"})
			continue
		}
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	sort.Slice(problems, func(i, j int) bool { return problems[i].Path < problems[j].Path })
	return skills, problems, nil
}

func displayPath(dir string) string {
	if dir == "" {
		return "/"
	}
	return dir
}

// hiddenBelow reports whether dir has a hidden segment below root.
func hiddenBelow(root, dir string) bool {
	rel := strings.TrimPrefix(strings.TrimPrefix(dir, root), "/")
	if rel == "" {
		return false
	}
	for _, segment := range strings.Split(rel, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func enclosing(outer []string, dir string) string {
	for _, candidate := range outer {
		if candidate == "" || strings.HasPrefix(dir, candidate+"/") {
			return candidate
		}
	}
	return ""
}

func validate(tree Tree, all []Entry, dir string, limits Limits, reserved []string) (Skill, string) {
	name := path.Base(dir)
	if !api.ValidSkillMarketplaceName(name) {
		return Skill{}, "the directory name is not a valid skill name: use at most 64 lowercase letters and digits, in words joined by single hyphens"
	}
	for _, value := range reserved {
		if value == name {
			return Skill{}, "the skill name is reserved by CetiRun"
		}
	}
	skill := Skill{Name: name, Path: dir}
	prefix := dir + "/"
	var skillMD *Entry
	for i := range all {
		entry := all[i]
		if !strings.HasPrefix(entry.Path, prefix) {
			continue
		}
		rel := strings.TrimPrefix(entry.Path, prefix)
		for _, segment := range strings.Split(rel, "/") {
			if unsafeName(segment) {
				return Skill{}, fmt.Sprintf("the file name %q is not allowed", rel)
			}
		}
		if strings.Count(rel, "/")+1 > limits.MaxDepth {
			return Skill{}, fmt.Sprintf("%s is nested deeper than %d levels", rel, limits.MaxDepth)
		}
		switch entry.Mode {
		case Symlink:
			return Skill{}, fmt.Sprintf("%s is a symbolic link; skills cannot contain links", rel)
		case Submodule:
			return Skill{}, fmt.Sprintf("%s is a Git submodule; skills cannot contain submodules", rel)
		case Dir:
			continue
		}
		skill.Files = append(skill.Files, File{RelPath: rel, Executable: entry.Mode == Executable, Size: entry.Size, Open: entry.Open})
		skill.Bytes += entry.Size
		if rel == skillFile {
			skillMD = &all[i]
		}
	}
	if len(skill.Files) > limits.MaxFiles {
		return Skill{}, fmt.Sprintf("the skill has %d files; at most %d are allowed", len(skill.Files), limits.MaxFiles)
	}
	if skill.Bytes > limits.MaxBytes {
		return Skill{}, fmt.Sprintf("the skill has %d bytes; at most %d are allowed", skill.Bytes, limits.MaxBytes)
	}
	if skillMD == nil {
		return Skill{}, "SKILL.md is missing"
	}
	if skillMD.Size > limits.MaxSkillMD {
		return Skill{}, fmt.Sprintf("SKILL.md has %d bytes; at most %d are allowed", skillMD.Size, limits.MaxSkillMD)
	}
	content, err := readAll(skillMD.Open, limits.MaxSkillMD)
	if err != nil {
		return Skill{}, "SKILL.md cannot be read"
	}
	frontName, description, reason := frontmatter(content, limits.MaxDescription)
	if reason != "" {
		return Skill{}, reason
	}
	if frontName != name {
		return Skill{}, fmt.Sprintf("the SKILL.md name %q differs from the directory name", frontName)
	}
	skill.Description = description
	hash, err := tree.TreeHash(dir)
	if err != nil || len(hash) < 12 {
		return Skill{}, "the skill version cannot be computed"
	}
	skill.Version = hash[:12]
	sort.Slice(skill.Files, func(i, j int) bool { return skill.Files[i].RelPath < skill.Files[j].RelPath })
	return skill, ""
}

// unsafeName reports whether a file or directory name of a skill must not be
// published: it is empty, a dot name, not valid UTF-8, contains a control
// character or a backslash, or Git would treat it as its own directory
// (.git, also in other case or with trailing dots or spaces, and git~1).
func unsafeName(name string) bool {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) ||
		strings.IndexFunc(name, unicode.IsControl) >= 0 || strings.ContainsRune(name, '\\') {
		return true
	}
	git := strings.ToLower(strings.TrimRight(name, ". "))
	return git == ".git" || git == "git~1"
}

func readAll(open func() (io.ReadCloser, error), limit int64) ([]byte, error) {
	reader, err := open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	return io.ReadAll(io.LimitReader(reader, limit+1))
}

// frontmatter returns the name and description of a SKILL.md, or a reason. A
// description may have at most maxDescription characters.
func frontmatter(content []byte, maxDescription int) (string, string, string) {
	content = bytes.TrimPrefix(content, []byte("\ufeff"))
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", "", "SKILL.md must start with YAML frontmatter between --- lines"
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return "", "", "SKILL.md frontmatter is not closed with a --- line"
	}
	var fields map[string]any
	if err := yaml.Unmarshal([]byte(text[4:4+end]), &fields); err != nil {
		return "", "", "SKILL.md frontmatter is not valid YAML"
	}
	name, ok := fields["name"].(string)
	if !ok || name == "" {
		return "", "", "SKILL.md frontmatter needs a name"
	}
	description, ok := fields["description"].(string)
	description = strings.TrimSpace(description)
	if !ok || description == "" {
		return "", "", "SKILL.md frontmatter needs a description"
	}
	if utf8.RuneCountInString(description) > maxDescription {
		return "", "", fmt.Sprintf("the description is longer than %d characters", maxDescription)
	}
	if strings.IndexFunc(description, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0 {
		return "", "", "the description contains control characters"
	}
	return name, description, ""
}
