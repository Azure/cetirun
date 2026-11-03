// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package gitsync reads one commit of a remote Git repository into memory.
// Nothing is checked out to disk and no repository content is executed.
package gitsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/skills"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Auth selects how the repository is accessed.
type Auth string

const (
	AuthNone  Auth = "none"
	AuthToken Auth = "token"
	AuthSSH   Auth = "ssh"
)

// Source describes the repository and its credentials.
type Source struct {
	URL               string
	Ref               string
	Auth              Auth
	TokenUsername     string
	CredentialsDir    string
	AllowInsecureHTTP bool
}

var scpLike = regexp.MustCompile(`^([A-Za-z0-9._-]+)@([A-Za-z0-9.-]+):([^/].*)$`)

// Validate checks the URL and authentication before any network use.
func (s Source) Validate() error {
	switch s.Auth {
	case AuthNone, AuthToken:
		parsed, err := url.Parse(s.URL)
		if err != nil || parsed.Host == "" {
			return fmt.Errorf("the repository URL must be an absolute https:// URL")
		}
		if parsed.User != nil {
			return fmt.Errorf("the repository URL must not contain credentials; use the credentials Secret")
		}
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && s.AllowInsecureHTTP) {
			return fmt.Errorf("the repository URL must use https://")
		}
	case AuthSSH:
		if strings.HasPrefix(s.URL, "ssh://") {
			parsed, err := url.Parse(s.URL)
			if err != nil || parsed.Host == "" {
				return fmt.Errorf("invalid ssh:// repository URL")
			}
			if _, hasPassword := parsed.User.Password(); hasPassword {
				return fmt.Errorf("the repository URL must not contain a password")
			}
		} else if !scpLike.MatchString(s.URL) {
			return fmt.Errorf("SSH repositories need an ssh:// URL or the user@host:path form")
		}
	default:
		return fmt.Errorf("auth must be none, token or ssh")
	}
	if s.Ref != "" && (strings.ContainsAny(s.Ref, " ~^:?*[\\") || strings.Contains(s.Ref, "..")) {
		return fmt.Errorf("invalid ref %q", s.Ref)
	}
	return nil
}

// Redacted is the URL without user information, for logs and the catalog.
func Redacted(raw string) string {
	if match := scpLike.FindStringSubmatch(raw); match != nil {
		return match[2] + ":" + match[3]
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<invalid URL>"
	}
	parsed.User = nil
	return parsed.String()
}

func (s Source) auth() (transport.AuthMethod, error) {
	switch s.Auth {
	case AuthToken:
		token, err := readSecret(filepath.Join(s.CredentialsDir, "token"))
		if err != nil {
			return nil, err
		}
		username := s.TokenUsername
		if username == "" {
			username = "x-access-token"
		}
		return &githttp.BasicAuth{Username: username, Password: token}, nil
	case AuthSSH:
		key, err := os.ReadFile(filepath.Join(s.CredentialsDir, "ssh-privatekey"))
		if err != nil {
			return nil, fmt.Errorf("read the SSH private key: %w", err)
		}
		user := "git"
		if match := scpLike.FindStringSubmatch(s.URL); match != nil {
			user = match[1]
		} else if parsed, err := url.Parse(s.URL); err == nil && parsed.User != nil && parsed.User.Username() != "" {
			user = parsed.User.Username()
		}
		auth, err := gitssh.NewPublicKeys(user, key, "")
		if err != nil {
			return nil, fmt.Errorf("parse the SSH private key: %w", err)
		}
		callback, err := knownhosts.New(filepath.Join(s.CredentialsDir, "known_hosts"))
		if err != nil {
			return nil, fmt.Errorf("read known_hosts (required for SSH): %w", err)
		}
		auth.HostKeyCallback = callback
		return auth, nil
	}
	return nil, nil
}

func readSecret(name string) (string, error) {
	raw, err := os.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("read the repository token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("the repository token is empty")
	}
	return token, nil
}

// Resolve returns the commit that the configured ref names on the remote.
func (s Source) Resolve(ctx context.Context) (plumbing.ReferenceName, plumbing.Hash, error) {
	auth, err := s.auth()
	if err != nil {
		return "", plumbing.ZeroHash, err
	}
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{s.URL}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
	if err != nil {
		return "", plumbing.ZeroHash, s.sanitize(err)
	}
	byName := map[plumbing.ReferenceName]*plumbing.Reference{}
	for _, ref := range refs {
		byName[ref.Name()] = ref
	}
	var candidates []plumbing.ReferenceName
	if s.Ref == "" {
		head, ok := byName[plumbing.HEAD]
		if !ok {
			return "", plumbing.ZeroHash, fmt.Errorf("the repository has no HEAD; set repository.ref")
		}
		if head.Type() == plumbing.SymbolicReference {
			candidates = []plumbing.ReferenceName{head.Target()}
		} else {
			for name, ref := range byName {
				if name.IsBranch() && ref.Hash() == head.Hash() {
					candidates = append(candidates, name)
				}
			}
			if len(candidates) == 0 {
				return "", plumbing.ZeroHash, fmt.Errorf("the repository HEAD is not a branch; set repository.ref")
			}
		}
	} else if strings.HasPrefix(s.Ref, "refs/") {
		candidates = []plumbing.ReferenceName{plumbing.ReferenceName(s.Ref)}
	} else {
		candidates = []plumbing.ReferenceName{plumbing.NewBranchReferenceName(s.Ref), plumbing.NewTagReferenceName(s.Ref)}
	}
	for _, name := range candidates {
		if ref, ok := byName[name]; ok {
			return name, ref.Hash(), nil
		}
	}
	return "", plumbing.ZeroHash, fmt.Errorf("the ref %q is not in the repository", s.Ref)
}

// Snapshot is one commit's tree, held in memory.
type Snapshot struct {
	Commit string
	tree   *object.Tree
	store  *memory.Storage
}

// Fetch reads the commit that ref names into memory (depth 1).
func (s Source) Fetch(ctx context.Context, ref plumbing.ReferenceName, maxBytes int64) (*Snapshot, error) {
	auth, err := s.auth()
	if err != nil {
		return nil, err
	}
	storage := memory.NewStorage()
	repo, err := git.CloneContext(ctx, storage, nil, &git.CloneOptions{
		URL: s.URL, Auth: auth, ReferenceName: ref, SingleBranch: true, Depth: 1,
		Tags: git.NoTags, NoCheckout: true, RecurseSubmodules: git.NoRecurseSubmodules,
	})
	if err != nil {
		return nil, s.sanitize(err)
	}
	head, err := repo.Reference(ref, true)
	if err != nil {
		head, err = repo.Head()
		if err != nil {
			return nil, err
		}
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		// An annotated tag points at a tag object.
		tag, tagErr := repo.TagObject(head.Hash())
		if tagErr != nil {
			return nil, err
		}
		if commit, err = tag.Commit(); err != nil {
			return nil, err
		}
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	var total int64
	if err := walkTree(tree, func(_ string, entry object.TreeEntry) error {
		if !entry.Mode.IsFile() {
			return nil
		}
		size, err := storage.EncodedObjectSize(entry.Hash)
		if err != nil {
			return err
		}
		total += size
		if total > maxBytes {
			return fmt.Errorf("the repository commit holds more than %d bytes of files", maxBytes)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return &Snapshot{Commit: commit.Hash.String(), tree: tree, store: storage}, nil
}

// walkTree visits every entry of tree, parents first. Unlike go-git's
// checkout walks, it does not stop at a name that is unsafe to write to a
// file system: the skills package rejects the skill that holds it, and the
// service writes no other entry.
func walkTree(tree *object.Tree, fn func(name string, entry object.TreeEntry) error) error {
	walker := object.NewTreeWalker(tree, true, nil)
	defer walker.Close()
	walker.SkipPathValidation()
	for {
		name, entry, err := walker.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(name, entry); err != nil {
			return err
		}
	}
}

func (s Source) sanitize(err error) error {
	message := err.Error()
	if s.URL != "" {
		message = strings.ReplaceAll(message, s.URL, Redacted(s.URL))
	}
	if errors.Is(err, transport.ErrAuthenticationRequired) || errors.Is(err, transport.ErrAuthorizationFailed) {
		return fmt.Errorf("repository access was refused: check the credentials Secret (%s)", message)
	}
	return errors.New(message)
}

// Walk implements skills.Tree.
func (s *Snapshot) Walk(root string, fn func(skills.Entry) error) error {
	tree := s.tree
	root = strings.Trim(root, "/")
	if root != "" {
		sub, err := s.tree.Tree(root)
		if err != nil {
			return fmt.Errorf("the repository path %q does not exist in the commit", root)
		}
		tree = sub
	}
	return walkTree(tree, func(name string, entry object.TreeEntry) error {
		full := name
		if root != "" {
			full = path.Join(root, name)
		}
		item := skills.Entry{Path: full}
		switch entry.Mode {
		case filemode.Dir:
			item.Mode = skills.Dir
		case filemode.Symlink:
			item.Mode = skills.Symlink
		case filemode.Submodule:
			item.Mode = skills.Submodule
		case filemode.Executable:
			item.Mode = skills.Executable
		default:
			item.Mode = skills.Regular
		}
		if item.Mode == skills.Regular || item.Mode == skills.Executable {
			blob, err := object.GetBlob(s.store, entry.Hash)
			if err != nil {
				return err
			}
			item.Size = blob.Size
			item.Open = func() (io.ReadCloser, error) { return blob.Reader() }
		}
		return fn(item)
	})
}

// TreeHash implements skills.Tree.
func (s *Snapshot) TreeHash(dir string) (string, error) {
	dir = strings.Trim(dir, "/")
	if dir == "" {
		return s.tree.Hash.String(), nil
	}
	sub, err := s.tree.Tree(dir)
	if err != nil {
		return "", err
	}
	return sub.Hash.String(), nil
}
