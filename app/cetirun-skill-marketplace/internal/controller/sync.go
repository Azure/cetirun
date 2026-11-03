// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/gitsync"
	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/publish"
	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/skills"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Store holds the skills of the last synchronized commit.
type Store struct {
	mu     sync.RWMutex
	commit string
	skills map[string]skills.Skill
}

// Snapshot returns the commit and its valid skills.
func (s *Store) Snapshot() (string, map[string]skills.Skill) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.commit, s.skills
}

func (s *Store) set(commit string, valid []skills.Skill) {
	byName := make(map[string]skills.Skill, len(valid))
	for _, skill := range valid {
		byName[skill.Name] = skill
	}
	s.mu.Lock()
	s.commit, s.skills = commit, byName
	s.mu.Unlock()
}

// Syncer periodically synchronizes the repository.
type Syncer struct {
	Source   gitsync.Source
	Path     string
	Interval time.Duration
	Limits   skills.Limits
	Reserved []string
	MaxBytes int64
	Store    *Store
	Catalog  *Catalog
	// Changed receives one event after each new commit, to republish Teams.
	Changed chan event.GenericEvent
}

// NeedLeaderElection keeps one synchronizer.
func (s *Syncer) NeedLeaderElection() bool { return true }

// Start runs the synchronization loop.
func (s *Syncer) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("sync")
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		if err := s.SyncOnce(ctx); err != nil {
			logger.Error(err, "synchronize the skills repository", "repository", gitsync.Redacted(s.Source.URL))
			s.Catalog.SetError(err.Error())
			timer.Reset(minDuration(s.Interval, time.Minute))
			continue
		}
		timer.Reset(s.Interval)
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// SyncOnce checks the remote and loads a new commit.
func (s *Syncer) SyncOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ref, hash, err := s.Source.Resolve(ctx)
	if err != nil {
		return err
	}
	current, _ := s.Store.Snapshot()
	if current == hash.String() {
		s.Catalog.SetChecked()
		return nil
	}
	snapshot, err := s.Source.Fetch(ctx, ref, s.MaxBytes)
	if err != nil {
		return err
	}
	valid, problems, err := skills.Discover(snapshot, s.Path, s.Limits, s.Reserved)
	if err != nil {
		return err
	}
	entries := make([]api.SkillMarketplaceSkill, 0, len(valid))
	for _, skill := range valid {
		entries = append(entries, api.SkillMarketplaceSkill{
			Name: skill.Name, Description: skill.Description, Version: skill.Version, Path: skill.Path,
			UpdatedAt: s.Catalog.FirstSeen(skill.Name, skill.Version), Files: len(skill.Files), Bytes: skill.Bytes,
		})
	}
	s.Store.set(snapshot.Commit, valid)
	s.Catalog.SetSync(snapshot.Commit, entries, problems)
	log.FromContext(ctx).Info("synchronized the skills repository", "commit", snapshot.Commit, "skills", len(valid), "rejected", len(problems))
	if s.Changed != nil {
		select {
		case s.Changed <- event.GenericEvent{}:
		default:
		}
	}
	return nil
}

// contentFor turns a repository skill into publishable content.
func contentFor(skill skills.Skill) publish.SkillContent {
	content := publish.SkillContent{Name: skill.Name, Version: skill.Version}
	for _, file := range skill.Files {
		content.Files = append(content.Files, publish.File{RelPath: file.RelPath, Executable: file.Executable, Open: file.Open})
	}
	return content
}

// errNotSynced is returned while no commit has been loaded yet.
var errNotSynced = fmt.Errorf("the skills repository has not been synchronized yet")
