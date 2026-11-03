// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
