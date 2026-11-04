// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/publish"
	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/skills"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func newClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func policyTeam(name, uid string, skills ...string) *api.Team {
	return &api.Team{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid)},
		Spec: api.TeamSpec{AgentPolicyRef: "policy", Skills: skills}}
}

func repoSkill(name, version, body string) skills.Skill {
	return skills.Skill{Name: name, Version: version, Description: name, Files: []skills.File{{RelPath: "SKILL.md",
		Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(body)), nil }}}}
}

func TestTeamPublicationRetainsAndReportsMissing(t *testing.T) {
	root := t.TempDir()
	publisher := &publish.Publisher{Root: root}
	if err := publisher.Init(); err != nil {
		t.Fatal(err)
	}
	team := policyTeam("research", "00000000-0000-4000-8000-000000000001", "pdf-tools", "web", "npm:@scope/pkg")
	kube := newClient(t, team, policyTeam("legacy", "00000000-0000-4000-8000-000000000002", "pdf-tools"))
	store := &Store{}
	store.set("c1", []skills.Skill{repoSkill("pdf-tools", "0123456789ab", "pdf"), repoSkill("web", "ba9876543210", "web")})
	catalog := NewCatalog(kube, kube, "cetirun-system", api.SkillMarketplaceRepository{URL: "https://git.example.invalid/x.git"})
	r := &TeamReconciler{Client: kube, Publisher: publisher, Store: store, Catalog: catalog}
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "research"}}); err != nil {
		t.Fatal(err)
	}
	id, release, err := publisher.ReadCurrent(string(team.UID))
	if err != nil || release == nil || len(release.Skills) != 2 || release.SelectionDigest != api.SkillMarketplaceSelectionDigest(team.Spec.Skills) {
		t.Fatalf("first release %q %+v %v", id, release, err)
	}
	// The repository drops web; the Team keeps its last published version.
	store.set("c2", []skills.Skill{repoSkill("pdf-tools", "0123456789ac", "pdf2")})
	team.Spec.Skills = append(team.Spec.Skills, "never-published")
	if err := kube.Update(ctx, team); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "research"}}); err != nil {
		t.Fatal(err)
	}
	_, release, _ = publisher.ReadCurrent(string(team.UID))
	versions := map[string]string{}
	for _, skill := range release.Skills {
		versions[skill.Name] = skill.Version
	}
	if versions["web"] != "ba9876543210" || versions["pdf-tools"] != "0123456789ac" || len(versions) != 2 {
		t.Fatalf("second release %+v", release.Skills)
	}
	raw, err := catalog.Document()
	if err != nil {
		t.Fatal(err)
	}
	var document api.SkillMarketplaceCatalog
	if err := json.Unmarshal(raw, &document); err != nil || len(document.Teams) != 1 {
		t.Fatalf("catalog %s %v", raw, err)
	}
	state := document.Teams[0]
	if state.ReleaseSelectionDigest != state.SelectionDigest || len(state.Retained) != 1 || state.Retained[0] != "web" ||
		len(state.Missing) != 1 || state.Missing[0] != "never-published" {
		t.Fatalf("Team state %+v", state)
	}
	if _, err := os.Stat(filepath.Join(root, "teams", "00000000-0000-4000-8000-000000000002")); !os.IsNotExist(err) {
		t.Fatalf("an unreconciled Team must not be published")
	}
}

// A selected skill that the repository dropped keeps its last published
// version. While that version or the current release cannot be read, the Team
// keeps its current release: a new release would silently leave the skill out.
func TestARetainedSkillWaitsForItsRelease(t *testing.T) {
	const uid = "00000000-0000-4000-8000-000000000001"
	root := t.TempDir()
	publisher := &publish.Publisher{Root: root}
	if err := publisher.Init(); err != nil {
		t.Fatal(err)
	}
	kube := newClient(t, policyTeam("research", uid, "pdf-tools", "web"))
	store := &Store{}
	store.set("c1", []skills.Skill{repoSkill("pdf-tools", "0123456789ab", "pdf"), repoSkill("web", "ba9876543210", "web")})
	catalog := NewCatalog(kube, kube, "cetirun-system", api.SkillMarketplaceRepository{})
	r := &TeamReconciler{Client: kube, Publisher: publisher, Store: store, Catalog: catalog}
	ctx := context.Background()
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: "research"}}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	first, _, err := publisher.ReadCurrent(uid)
	if err != nil || first == "" {
		t.Fatalf("first release %q %v", first, err)
	}
	// The repository drops web, whose published files cannot be read.
	store.set("c2", []skills.Skill{repoSkill("pdf-tools", "0123456789ac", "pdf2")})
	skillMD := filepath.Join(root, "teams", uid, "releases", first, "web", "SKILL.md")
	if err := os.Rename(skillMD, skillMD+".away"); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(root, "teams", uid, "CURRENT")
	blocked := func(step string) {
		t.Helper()
		if _, err := r.Reconcile(ctx, request); err == nil {
			t.Fatalf("%s: the publication must fail and be retried", step)
		}
		if data, err := os.ReadFile(current); err != nil || !strings.HasPrefix(string(data), first) && step != "CURRENT" {
			t.Fatalf("%s: CURRENT must stay: %q %v", step, data, err)
		}
		if state := catalogTeams(t, catalog)[0]; !strings.Contains(state.Error, "could not be read") || len(state.Retained) != 0 {
			t.Fatalf("%s: Team state %+v", step, state)
		}
	}
	blocked("retained files")
	// The current release itself cannot be read.
	if err := os.WriteFile(current, []byte("damaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocked("CURRENT")
	if data, _ := os.ReadFile(current); string(data) != "damaged\n" {
		t.Fatalf("a damaged CURRENT must stay until a release can replace it: %q", data)
	}
	// Once storage is readable again, the next release keeps web.
	if err := os.WriteFile(current, []byte(first+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(skillMD+".away", skillMD); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	_, release, err := publisher.ReadCurrent(uid)
	if err != nil || release == nil || release.ID == first {
		t.Fatalf("a new release must follow: %+v %v", release, err)
	}
	versions := map[string]string{}
	for _, skill := range release.Skills {
		versions[skill.Name] = skill.Version
	}
	if versions["web"] != "ba9876543210" || versions["pdf-tools"] != "0123456789ac" {
		t.Fatalf("release %+v", release.Skills)
	}
	if state := catalogTeams(t, catalog)[0]; state.Error != "" || len(state.Retained) != 1 {
		t.Fatalf("Team state %+v", state)
	}
}

// A Team whose selected skills are all in the repository needs nothing from
// its current release: a damaged CURRENT does not stop its next release.
func TestACompleteSelectionReplacesADamagedCURRENT(t *testing.T) {
	const uid = "00000000-0000-4000-8000-000000000001"
	root := t.TempDir()
	publisher := &publish.Publisher{Root: root}
	if err := publisher.Init(); err != nil {
		t.Fatal(err)
	}
	kube := newClient(t, policyTeam("research", uid, "pdf-tools"))
	store := &Store{}
	store.set("c1", []skills.Skill{repoSkill("pdf-tools", "0123456789ab", "pdf")})
	catalog := NewCatalog(kube, kube, "cetirun-system", api.SkillMarketplaceRepository{})
	r := &TeamReconciler{Client: kube, Publisher: publisher, Store: store, Catalog: catalog}
	ctx := context.Background()
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: "research"}}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "teams", uid, "CURRENT"), []byte("damaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store.set("c2", []skills.Skill{repoSkill("pdf-tools", "0123456789ac", "pdf2")})
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatalf("a complete selection must publish: %v", err)
	}
	if _, release, err := publisher.ReadCurrent(uid); err != nil || release == nil || release.Skills[0].Version != "0123456789ac" {
		t.Fatalf("release %+v %v", release, err)
	}
}

func TestTeamWaitsForTheFirstSync(t *testing.T) {
	publisher := &publish.Publisher{Root: t.TempDir()}
	_ = publisher.Init()
	kube := newClient(t, policyTeam("research", "00000000-0000-4000-8000-000000000001", "pdf-tools"))
	r := &TeamReconciler{Client: kube, Publisher: publisher, Store: &Store{},
		Catalog: NewCatalog(kube, kube, "cetirun-system", api.SkillMarketplaceRepository{})}
	result, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "research"}})
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("must requeue until synchronized: %+v %v", result, err)
	}
	if id, _, _ := publisher.ReadCurrent("00000000-0000-4000-8000-000000000001"); id != "" {
		t.Fatalf("nothing may be published before the first sync")
	}
}

func TestADeletedTeamLeavesTheCatalogAtOnce(t *testing.T) {
	publisher := &publish.Publisher{Root: t.TempDir()}
	if err := publisher.Init(); err != nil {
		t.Fatal(err)
	}
	team := policyTeam("research", "00000000-0000-4000-8000-000000000001", "pdf-tools")
	other := policyTeam("support", "00000000-0000-4000-8000-000000000002", "pdf-tools")
	kube := newClient(t, team, other)
	store := &Store{}
	store.set("c1", []skills.Skill{repoSkill("pdf-tools", "0123456789ab", "pdf")})
	catalog := NewCatalog(kube, kube, "cetirun-system", api.SkillMarketplaceRepository{})
	r := &TeamReconciler{Client: kube, Publisher: publisher, Store: store, Catalog: catalog, GCGrace: time.Hour}
	ctx := context.Background()
	for _, name := range []string{"research", "support"} {
		if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := kube.Delete(ctx, team); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "research"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := catalog.Document()
	if err != nil {
		t.Fatal(err)
	}
	var document api.SkillMarketplaceCatalog
	if err := json.Unmarshal(raw, &document); err != nil || len(document.Teams) != 1 || document.Teams[0].Name != "support" {
		t.Fatalf("the deleted Team must leave the catalog: %s %v", raw, err)
	}
	// Its releases stay until the grace period ends.
	if id, _, _ := publisher.ReadCurrent(string(team.UID)); id == "" {
		t.Fatalf("the deleted Team's releases must stay for the grace period")
	}
}

// catalogTeams returns the Team entries of the catalog document.
func catalogTeams(t *testing.T, catalog *Catalog) []api.SkillMarketplaceTeam {
	t.Helper()
	raw, err := catalog.Document()
	if err != nil {
		t.Fatal(err)
	}
	var document api.SkillMarketplaceCatalog
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	return document.Teams
}

// teamRelease is what an Agent of a Team copies: the current release, and the
// skill directories in it.
type teamRelease struct {
	id, published string
	versions      map[string]string
	dirs          []string
}

func readTeamRelease(t *testing.T, publisher *publish.Publisher, uid string) teamRelease {
	t.Helper()
	id, release, err := publisher.ReadCurrent(uid)
	if err != nil || release == nil {
		t.Fatalf("Team %s has no current release: %q %v", uid, id, err)
	}
	state := teamRelease{id: id, published: release.CreatedAt, versions: map[string]string{}}
	for _, skill := range release.Skills {
		state.versions[skill.Name] = skill.Version
	}
	entries, err := os.ReadDir(filepath.Join(publisher.Root, "teams", uid, "releases", id))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			state.dirs = append(state.dirs, entry.Name())
		}
	}
	return state
}

// Teams share the repository, not their releases: each Team's subtree holds
// only the skills that the Team selects, and a repository change publishes
// again only for the Teams that select a changed skill.
func TestTeamsArePublishedIndependently(t *testing.T) {
	clock := time.Now()
	publisher := &publish.Publisher{Root: t.TempDir(), Now: func() time.Time { return clock }}
	if err := publisher.Init(); err != nil {
		t.Fatal(err)
	}
	research := policyTeam("research", "00000000-0000-4000-8000-000000000001", "pdf-tools", "zh-notes")
	support := policyTeam("support", "00000000-0000-4000-8000-000000000002", "zh-notes")
	kube := newClient(t, research, support)
	store := &Store{}
	catalog := NewCatalog(kube, kube, "cetirun-system", api.SkillMarketplaceRepository{})
	r := &TeamReconciler{Client: kube, Publisher: publisher, Store: store, Catalog: catalog, GCGrace: time.Hour}
	ctx := context.Background()
	newCommit := func(commit, pdf, zh string) {
		t.Helper()
		store.set(commit, []skills.Skill{repoSkill("pdf-tools", pdf, "pdf "+pdf), repoSkill("zh-notes", zh, "zh "+zh),
			repoSkill("unselected", "cccccccccccc", "unselected")})
		clock = clock.Add(time.Minute)
		// A new commit reconciles every Team, as the synchronizer's event does.
		if _, err := r.Reconcile(ctx, reconcile.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	releaseCount := func(team *api.Team) int {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(publisher.Root, "teams", string(team.UID), "releases"))
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}

	newCommit("c1", "aaaaaaaaaaa1", "bbbbbbbbbbb1")
	first := map[string]teamRelease{"research": readTeamRelease(t, publisher, string(research.UID)),
		"support": readTeamRelease(t, publisher, string(support.UID))}
	if !reflect.DeepEqual(first["research"].dirs, []string{"pdf-tools", "zh-notes"}) ||
		!reflect.DeepEqual(first["support"].dirs, []string{"zh-notes"}) {
		t.Fatalf("each release must hold only its Team's selection: %+v", first)
	}

	// Only research selects pdf-tools.
	newCommit("c2", "aaaaaaaaaaa2", "bbbbbbbbbbb1")
	second := map[string]teamRelease{"research": readTeamRelease(t, publisher, string(research.UID)),
		"support": readTeamRelease(t, publisher, string(support.UID))}
	if second["research"].id == first["research"].id || second["research"].versions["pdf-tools"] != "aaaaaaaaaaa2" {
		t.Fatalf("research must get a release with the new pdf-tools: %+v", second["research"])
	}
	if !reflect.DeepEqual(second["support"], first["support"]) || releaseCount(support) != 1 {
		t.Fatalf("support must keep its release: %+v, want %+v", second["support"], first["support"])
	}

	// Both select zh-notes.
	newCommit("c3", "aaaaaaaaaaa2", "bbbbbbbbbbb2")
	third := map[string]teamRelease{"research": readTeamRelease(t, publisher, string(research.UID)),
		"support": readTeamRelease(t, publisher, string(support.UID))}
	for name, release := range third {
		if release.id == second[name].id || release.versions["zh-notes"] != "bbbbbbbbbbb2" {
			t.Fatalf("%s must get a release with the new zh-notes: %+v", name, release)
		}
	}
	if !reflect.DeepEqual(third["support"].dirs, []string{"zh-notes"}) {
		t.Fatalf("support must still hold only zh-notes: %+v", third["support"])
	}

	// The catalog lists each Team once, with its own release.
	teams := catalogTeams(t, catalog)
	if len(teams) != 2 || teams[0].Name != "research" || teams[0].Release != third["research"].id ||
		teams[1].Name != "support" || teams[1].Release != third["support"].id {
		t.Fatalf("catalog Teams %+v", teams)
	}
}

// A Team created again under the name of a deleted Team is a new Team: it gets
// its own subtree, and the catalog lists it once, also when the marketplace
// sees the new Team before the deletion. The deleted Team's releases stay
// until the grace period ends, and other Teams keep theirs.
func TestATeamCreatedAgainUnderItsNameIsANewTeam(t *testing.T) {
	publisher := &publish.Publisher{Root: t.TempDir()}
	if err := publisher.Init(); err != nil {
		t.Fatal(err)
	}
	const (
		researchUID = "00000000-0000-4000-8000-000000000001"
		deletedUID  = "00000000-0000-4000-8000-000000000002"
		createdUID  = "00000000-0000-4000-8000-000000000003"
	)
	deleted := policyTeam("support", deletedUID, "zh-notes")
	kube := newClient(t, policyTeam("research", researchUID, "pdf-tools"), deleted)
	store := &Store{}
	store.set("c1", []skills.Skill{repoSkill("pdf-tools", "aaaaaaaaaaa1", "pdf"), repoSkill("zh-notes", "bbbbbbbbbbb1", "zh")})
	catalog := NewCatalog(kube, kube, "cetirun-system", api.SkillMarketplaceRepository{})
	r := &TeamReconciler{Client: kube, Publisher: publisher, Store: store, Catalog: catalog, GCGrace: time.Hour}
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, reconcile.Request{}); err != nil {
		t.Fatal(err)
	}
	research := readTeamRelease(t, publisher, researchUID)
	if err := kube.Delete(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	if err := kube.Create(ctx, policyTeam("support", createdUID, "pdf-tools")); err != nil {
		t.Fatal(err)
	}
	// The marketplace sees the new Team before the deletion.
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "support"}}); err != nil {
		t.Fatal(err)
	}
	teams := catalogTeams(t, catalog)
	if len(teams) != 2 || teams[1].Name != "support" || teams[1].UID != createdUID {
		t.Fatalf("the catalog must list the new Team once, without its predecessor: %+v", teams)
	}
	if created := readTeamRelease(t, publisher, createdUID); !reflect.DeepEqual(created.dirs, []string{"pdf-tools"}) {
		t.Fatalf("the new Team must get its own release: %+v", created)
	}
	if old := readTeamRelease(t, publisher, deletedUID); !reflect.DeepEqual(old.dirs, []string{"zh-notes"}) {
		t.Fatalf("the deleted Team's release must stay for the grace period: %+v", old)
	}

	// After the grace period, a full pass removes only the deleted Team's subtree.
	r.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := r.Reconcile(ctx, reconcile.Request{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(publisher.Root, "teams", deletedUID)); !os.IsNotExist(err) {
		t.Fatalf("the deleted Team's subtree must be removed after the grace period: %v", err)
	}
	if got := readTeamRelease(t, publisher, researchUID); !reflect.DeepEqual(got, research) {
		t.Fatalf("another Team must keep its release: %+v, want %+v", got, research)
	}
	readTeamRelease(t, publisher, createdUID)
	if teams := catalogTeams(t, catalog); len(teams) != 2 || teams[1].UID != createdUID {
		t.Fatalf("catalog Teams after the grace period %+v", teams)
	}
}

func claimFixture(t *testing.T, protected bool) (*ClaimReconciler, client.Client) {
	t.Helper()
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "alice", UID: "agent-uid"}, Spec: api.AgentSpec{Team: "research"}}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cetirun-alice",
		Annotations: map[string]string{agentUIDAnnotation: "agent-uid", agentNameAnnotation: "alice"}}}
	if protected {
		namespace.Labels = map[string]string{protectedLabel: "true"}
	}
	kube := newClient(t, agent, namespace, policyTeam("research", "00000000-0000-4000-8000-000000000001"))
	publisher := &publish.Publisher{Root: t.TempDir()}
	_ = publisher.Init()
	return &ClaimReconciler{Client: kube, Reader: kube, ClaimName: "skills", ClaimSize: resource.MustParse("1Gi"), Publisher: publisher,
		Storage: StorageSource{ResourceGroup: "rg", StorageAccount: "account", ContainerName: "marketplace", StorageClassName: "azureblob-nfs-premium"}}, kube
}

func TestClaimIsCreatedForProtectedAgentNamespaces(t *testing.T) {
	r, kube := claimFixture(t, true)
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "alice"}}); err != nil {
		t.Fatal(err)
	}
	claim := &corev1.PersistentVolumeClaim{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: "cetirun-alice", Name: "skills"}, claim); err != nil {
		t.Fatal(err)
	}
	if claim.Spec.VolumeName != VolumeName("agent-uid") || claim.OwnerReferences[0].Kind != "Agent" ||
		claim.Annotations[agentUIDAnnotation] != "agent-uid" || claim.Labels[api.SkillMarketplaceManagedLabel] != "true" {
		t.Fatalf("claim %+v", claim)
	}
	volume := &corev1.PersistentVolume{}
	if err := kube.Get(ctx, client.ObjectKey{Name: VolumeName("agent-uid")}, volume); err != nil {
		t.Fatal(err)
	}
	csi := volume.Spec.CSI
	if !csi.ReadOnly || volume.Spec.MountOptions[0] != "ro" || csi.VolumeAttributes["containerName"] != "marketplace" ||
		volume.Spec.ClaimRef.Name != "skills" || volume.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Fatalf("volume %+v", volume.Spec)
	}
	if _, err := os.Stat(filepath.Join(r.Publisher.Root, "teams", "00000000-0000-4000-8000-000000000001", "releases")); err != nil {
		t.Fatalf("the Team directory must exist before the Agent mounts it: %v", err)
	}
	// A second pass changes nothing.
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "alice"}}); err != nil {
		t.Fatal(err)
	}
}

func TestClaimSkipsUnprotectedNamespaces(t *testing.T) {
	r, kube := claimFixture(t, false)
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "alice"}}); err != nil {
		t.Fatal(err)
	}
	claims := &corev1.PersistentVolumeClaimList{}
	if err := kube.List(context.Background(), claims); err != nil || len(claims.Items) != 0 {
		t.Fatalf("no claim may be created in an unprotected namespace: %+v", claims.Items)
	}
}

func TestVolumeCollectorRemovesOrphans(t *testing.T) {
	r, kube := claimFixture(t, true)
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "alice"}}); err != nil {
		t.Fatal(err)
	}
	collector := &VolumeCollector{Client: kube, Reader: kube}
	if err := collector.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKey{Name: VolumeName("agent-uid")}, &corev1.PersistentVolume{}); err != nil {
		t.Fatalf("a bound volume must stay: %v", err)
	}
	if err := kube.Delete(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "cetirun-alice", Name: "skills"}}); err != nil {
		t.Fatal(err)
	}
	if err := collector.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKey{Name: VolumeName("agent-uid")}, &corev1.PersistentVolume{}); err == nil {
		t.Fatalf("an orphaned volume must be removed")
	}
}

func TestCatalogIsWrittenOnlyWhenChanged(t *testing.T) {
	kube := newClient(t)
	catalog := NewCatalog(kube, kube, "cetirun-system", api.SkillMarketplaceRepository{URL: "https://git.example.invalid/x.git"})
	catalog.SetSync("c1", []api.SkillMarketplaceSkill{{Name: "pdf-tools", Version: "0123456789ab"}}, nil)
	ctx := context.Background()
	if err := catalog.Write(ctx); err != nil {
		t.Fatal(err)
	}
	stored := &corev1.ConfigMap{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: "cetirun-system", Name: api.SkillMarketplaceCatalogConfigMap}, stored); err != nil {
		t.Fatal(err)
	}
	version := stored.ResourceVersion
	if err := catalog.Write(ctx); err != nil {
		t.Fatal(err)
	}
	_ = kube.Get(ctx, client.ObjectKeyFromObject(stored), stored)
	if stored.ResourceVersion != version {
		t.Fatalf("an unchanged catalog must not be written again")
	}
	var document api.SkillMarketplaceCatalog
	if err := json.Unmarshal([]byte(stored.Data[api.SkillMarketplaceCatalogKey]), &document); err != nil || document.Commit != "c1" {
		t.Fatalf("catalog %+v %v", document, err)
	}
}

func TestCatalogFitsItsConfigMap(t *testing.T) {
	document := api.SkillMarketplaceCatalog{Version: "v1"}
	for i := 0; i < 2000; i++ {
		document.Skills = append(document.Skills, api.SkillMarketplaceSkill{Name: "s", Description: strings.Repeat("d", 600)})
	}
	raw, err := fitDocument(document)
	if err != nil || len(raw) > MaxCatalogBytes || !strings.Contains(string(raw), `"truncated":true`) {
		t.Fatalf("fitDocument: %d bytes, %v", len(raw), err)
	}
}
