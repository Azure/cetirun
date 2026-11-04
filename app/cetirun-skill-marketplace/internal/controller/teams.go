// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/publish"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// TeamReconciler publishes the skill selection of each policy-bound Team.
type TeamReconciler struct {
	Client    client.Client
	Publisher *publish.Publisher
	Store     *Store
	Catalog   *Catalog
	Now       func() time.Time
	// GCGrace is how long the releases of a deleted Team stay after the
	// Team's last publication.
	GCGrace time.Duration
}

func (r *TeamReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile publishes one Team.
func (r *TeamReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	if req.Name == "" {
		return r.collect(ctx)
	}
	team := &api.Team{}
	if err := r.Client.Get(ctx, req.NamespacedName, team); err != nil {
		if apierrors.IsNotFound(err) {
			// The Team is deleted. The catalog stops listing it now; its
			// releases stay until GCGrace after its last publication.
			r.Catalog.RemoveTeamNamed(req.Name)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	uid := string(team.UID)
	if team.Spec.AgentPolicyRef == "" || !team.DeletionTimestamp.IsZero() {
		r.Catalog.RemoveTeam(uid)
		return reconcile.Result{}, nil
	}
	if err := r.Publisher.EnsureTeam(uid); err != nil {
		return reconcile.Result{}, err
	}
	selection := api.SkillMarketplaceSelection(team.Spec.Skills)
	digest := api.SkillMarketplaceSelectionDigest(team.Spec.Skills)
	state := api.SkillMarketplaceTeam{Name: team.Name, UID: uid, SelectionDigest: digest}
	commit, available := r.Store.Snapshot()
	if available == nil && len(selection) > 0 {
		state.Error = errNotSynced.Error()
		r.Catalog.SetTeam(r.withCurrent(state))
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	}
	// A selected skill that the repository no longer offers keeps the version
	// of the current release. The next release must not leave it out because
	// that release could not be read: retry instead, and keep CURRENT.
	var currentID string
	var current *api.SkillMarketplaceRelease
	if slices.ContainsFunc(selection, func(name string) bool { _, ok := available[name]; return !ok }) {
		var err error
		if currentID, current, err = r.Publisher.ReadCurrent(uid); err != nil {
			return r.publicationBlocked(state, err)
		}
	}
	var content []publish.SkillContent
	for _, name := range selection {
		if skill, ok := available[name]; ok {
			content = append(content, contentFor(skill))
			continue
		}
		if version := releaseVersion(current, name); version != "" {
			files, err := r.Publisher.ReleaseSkillFiles(uid, currentID, name)
			if err != nil {
				return r.publicationBlocked(state, fmt.Errorf("read the retained skill %s: %w", name, err))
			}
			content = append(content, publish.SkillContent{Name: name, Version: version, Files: files})
			state.Retained = append(state.Retained, name)
			continue
		}
		state.Missing = append(state.Missing, name)
	}
	release, changed, err := r.Publisher.Publish(publish.TeamRef{Name: team.Name, UID: uid}, digest, content, commit)
	if err != nil {
		state.Error = "publication failed: " + err.Error()
		r.Catalog.SetTeam(r.withCurrent(state))
		return reconcile.Result{}, err
	}
	state.Release, state.ReleaseSelectionDigest, state.PublishedAt = release.ID, release.SelectionDigest, release.CreatedAt
	state.Skills = release.Skills
	r.Catalog.SetTeam(state)
	if changed {
		log.FromContext(ctx).Info("published Team skills", "team", team.Name, "release", release.ID, "skills", len(release.Skills),
			"retained", len(state.Retained), "missing", len(state.Missing))
	}
	if err := r.Publisher.GC(uid, 2, 10*time.Minute); err != nil {
		log.FromContext(ctx).Error(err, "remove old Team releases", "team", team.Name)
	}
	return reconcile.Result{}, nil
}

// withCurrent fills a Team state from its existing release.
func (r *TeamReconciler) withCurrent(state api.SkillMarketplaceTeam) api.SkillMarketplaceTeam {
	if _, release, err := r.Publisher.ReadCurrent(state.UID); err == nil && release != nil {
		state.Release, state.ReleaseSelectionDigest, state.PublishedAt = release.ID, release.SelectionDigest, release.CreatedAt
		state.Skills = release.Skills
	}
	return state
}

// publicationBlocked records why a Team's publication waits, and returns the
// error so that the Team is reconciled again with backoff. CURRENT and the
// Team's releases stay as they are.
func (r *TeamReconciler) publicationBlocked(state api.SkillMarketplaceTeam, err error) (reconcile.Result, error) {
	state.Retained, state.Missing = nil, nil
	state.Error = "publication waits for the current release, which could not be read: " + err.Error()
	r.Catalog.SetTeam(r.withCurrent(state))
	return reconcile.Result{}, fmt.Errorf("Team %s: %w", state.Name, err)
}

func releaseVersion(release *api.SkillMarketplaceRelease, name string) string {
	if release == nil {
		return ""
	}
	for _, skill := range release.Skills {
		if skill.Name == name {
			return skill.Version
		}
	}
	return ""
}

// collect republishes every Team and removes the releases of deleted Teams.
func (r *TeamReconciler) collect(ctx context.Context) (reconcile.Result, error) {
	teams := &api.TeamList{}
	if err := r.Client.List(ctx, teams); err != nil {
		return reconcile.Result{}, err
	}
	live := map[string]bool{}
	for _, team := range teams.Items {
		live[string(team.UID)] = true
		if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: team.Name}}); err != nil {
			log.FromContext(ctx).Error(err, "publish Team skills", "team", team.Name)
		}
	}
	uids, err := r.Publisher.ListTeams()
	if err != nil {
		return reconcile.Result{}, err
	}
	for _, uid := range uids {
		if live[uid] {
			continue
		}
		r.Catalog.RemoveTeam(uid)
		modified, err := r.Publisher.TeamModTime(uid)
		if err == nil && r.now().Sub(modified) > r.GCGrace {
			if err := r.Publisher.RemoveTeam(uid); err != nil {
				log.FromContext(ctx).Error(err, "remove a deleted Team's releases", "teamUID", uid)
			}
		}
	}
	return reconcile.Result{RequeueAfter: 10 * time.Minute}, nil
}

// SetupWithManager watches Teams, and republishes every Team after a new commit.
func (r *TeamReconciler) SetupWithManager(mgr ctrl.Manager, changed <-chan event.GenericEvent) error {
	all := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("team-skills").
		For(&api.Team{}).
		WatchesRawSource(source.Channel(changed, all)).
		WithOptions(controllerOptions()).
		Complete(r)
}

// teamNames lists Team names in a stable order (for tests and logs).
func teamNames(teams []api.Team) []string {
	names := make([]string, 0, len(teams))
	for _, team := range teams {
		names = append(names, team.Name)
	}
	sort.Strings(names)
	return names
}
