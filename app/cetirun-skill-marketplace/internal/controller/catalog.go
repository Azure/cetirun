// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// MaxCatalogBytes keeps the catalog well below the ConfigMap size limit.
const MaxCatalogBytes = 900 << 10

// Catalog holds the published state and writes it to the catalog ConfigMap.
type Catalog struct {
	Client    client.Client
	Reader    client.Reader
	Namespace string
	Now       func() time.Time

	mu        sync.Mutex
	state     api.SkillMarketplaceCatalog
	teams     map[string]api.SkillMarketplaceTeam
	firstSeen map[string]string
	written   []byte
	dirty     chan struct{}
}

// NewCatalog creates an empty catalog for the repository.
func NewCatalog(c client.Client, reader client.Reader, namespace string, repository api.SkillMarketplaceRepository) *Catalog {
	return &Catalog{
		Client: c, Reader: reader, Namespace: namespace,
		state:     api.SkillMarketplaceCatalog{Version: api.SkillMarketplaceCatalogVersion, Repository: repository},
		teams:     map[string]api.SkillMarketplaceTeam{},
		firstSeen: map[string]string{},
		dirty:     make(chan struct{}, 1),
	}
}

func (c *Catalog) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Seed restores first-seen times and Team states from the published catalog.
func (c *Catalog) Seed(ctx context.Context) error {
	existing := &corev1.ConfigMap{}
	err := c.Reader.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: api.SkillMarketplaceCatalogConfigMap}, existing)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var previous api.SkillMarketplaceCatalog
	if json.Unmarshal([]byte(existing.Data[api.SkillMarketplaceCatalogKey]), &previous) != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, skill := range previous.Skills {
		if skill.UpdatedAt != "" {
			c.firstSeen[skill.Name+"@"+skill.Version] = skill.UpdatedAt
		}
	}
	if previous.Repository == c.state.Repository {
		c.state.Commit, c.state.SyncedAt = previous.Commit, previous.SyncedAt
	}
	return nil
}

// FirstSeen returns, and records, when a skill version first appeared.
func (c *Catalog) FirstSeen(name, version string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := name + "@" + version
	if value, ok := c.firstSeen[key]; ok {
		return value
	}
	value := c.now().UTC().Format(time.RFC3339)
	c.firstSeen[key] = value
	return value
}

// SetSync records a successful synchronization.
func (c *Catalog) SetSync(commit string, skills []api.SkillMarketplaceSkill, invalid []api.SkillMarketplaceProblem) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().UTC().Format(time.RFC3339)
	c.state.Commit, c.state.SyncedAt, c.state.CheckedAt, c.state.Error = commit, now, now, ""
	c.state.Skills, c.state.Invalid = skills, invalid
	c.markDirty()
}

// SetChecked records a check that found no change.
func (c *Catalog) SetChecked() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.CheckedAt, c.state.Error = c.now().UTC().Format(time.RFC3339), ""
	c.markDirty()
}

// SetError records a failed check, keeping the last good content.
func (c *Catalog) SetError(message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.CheckedAt, c.state.Error = c.now().UTC().Format(time.RFC3339), message
	c.markDirty()
}

// SetTeam records a Team's publication state.
func (c *Catalog) SetTeam(team api.SkillMarketplaceTeam) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, ok := c.teams[team.UID]; ok && reflect.DeepEqual(previous, team) {
		return
	}
	c.teams[team.UID] = team
	c.markDirty()
}

// RemoveTeam forgets a Team.
func (c *Catalog) RemoveTeam(uid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.teams[uid]; ok {
		delete(c.teams, uid)
		c.markDirty()
	}
}

// Synced reports whether a repository commit is known.
func (c *Catalog) Synced() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.Commit != ""
}

func (c *Catalog) markDirty() {
	select {
	case c.dirty <- struct{}{}:
	default:
	}
}

// Document returns the catalog as it would be written.
func (c *Catalog) Document() ([]byte, error) {
	c.mu.Lock()
	document := c.state
	document.Skills = append([]api.SkillMarketplaceSkill(nil), c.state.Skills...)
	document.Invalid = append([]api.SkillMarketplaceProblem(nil), c.state.Invalid...)
	document.Teams = make([]api.SkillMarketplaceTeam, 0, len(c.teams))
	for _, team := range c.teams {
		document.Teams = append(document.Teams, team)
	}
	c.mu.Unlock()
	sort.Slice(document.Teams, func(i, j int) bool { return document.Teams[i].Name < document.Teams[j].Name })
	return fitDocument(document)
}

// fitDocument shortens descriptions and drops invalid entries when the
// catalog would not fit in its ConfigMap.
func fitDocument(document api.SkillMarketplaceCatalog) ([]byte, error) {
	raw, err := json.Marshal(document)
	if err != nil || len(raw) <= MaxCatalogBytes {
		return raw, err
	}
	document.Truncated = true
	for i := range document.Skills {
		if runes := []rune(document.Skills[i].Description); len(runes) > 160 {
			document.Skills[i].Description = string(runes[:157]) + "..."
		}
	}
	for len(raw) > MaxCatalogBytes && len(document.Invalid) > 0 {
		document.Invalid = document.Invalid[:len(document.Invalid)/2]
		if raw, err = json.Marshal(document); err != nil {
			return nil, err
		}
	}
	if raw, err = json.Marshal(document); err != nil {
		return nil, err
	}
	if len(raw) > MaxCatalogBytes {
		return nil, fmt.Errorf("the catalog needs %d bytes, more than %d", len(raw), MaxCatalogBytes)
	}
	return raw, nil
}

// Write stores the catalog when it changed.
func (c *Catalog) Write(ctx context.Context) error {
	raw, err := c.Document()
	if err != nil {
		return err
	}
	c.mu.Lock()
	unchanged := string(raw) == string(c.written)
	c.mu.Unlock()
	if unchanged {
		return nil
	}
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: api.SkillMarketplaceCatalogConfigMap, Namespace: c.Namespace,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "cetirun-skill-marketplace"},
		},
		Data: map[string]string{api.SkillMarketplaceCatalogKey: string(raw)},
	}
	existing := &corev1.ConfigMap{}
	err = c.Reader.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	switch {
	case apierrors.IsNotFound(err):
		err = c.Client.Create(ctx, desired)
	case err == nil:
		existing.Data, existing.BinaryData = desired.Data, nil
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		existing.Labels["app.kubernetes.io/managed-by"] = "cetirun-skill-marketplace"
		err = c.Client.Update(ctx, existing)
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.written = raw
	c.mu.Unlock()
	return nil
}

// Start writes the catalog whenever it changes, at most every two seconds.
func (c *Catalog) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("catalog")
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	pending := true
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.dirty:
			pending = true
		case <-ticker.C:
			if !pending {
				continue
			}
			if err := c.Write(ctx); err != nil {
				logger.Error(err, "write the Skill Marketplace catalog")
				continue
			}
			pending = false
		}
	}
}

// NeedLeaderElection keeps one writer.
func (c *Catalog) NeedLeaderElection() bool { return true }
