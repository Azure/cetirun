// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package skillmarketplace

import (
	"context"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func claimFixture(t *testing.T, protected bool, policy string) (*ClaimReconciler, client.Client) {
	t.Helper()
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "alice", UID: "agent-uid"}, Spec: api.AgentSpec{Team: "research"}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cetirun-alice",
		Annotations: map[string]string{agentUIDAnnotation: "agent-uid", agentNameAnnotation: "alice"}}}
	if protected {
		ns.Labels = map[string]string{protectedLabel: "true"}
	}
	kube := newClient(t, agent, ns, team("research", "00000000-0000-4000-8000-000000000001", policy))
	return &ClaimReconciler{Client: kube, Reader: kube, ClaimName: "skills",
		Storage: StorageSource{ResourceGroup: "rg", StorageAccount: "account", ContainerName: "marketplace", StorageClassName: "azureblob-nfs-premium"}}, kube
}

var alice = reconcile.Request{NamespacedName: types.NamespacedName{Name: "alice"}}

func TestClaimIsCreatedForProtectedAgentNamespaces(t *testing.T) {
	r, kube := claimFixture(t, true, "policy")
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, alice); err != nil {
		t.Fatal(err)
	}
	claim := &corev1.PersistentVolumeClaim{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: "cetirun-alice", Name: "skills"}, claim); err != nil {
		t.Fatal(err)
	}
	if claim.Spec.VolumeName != VolumeName("agent-uid") || claim.OwnerReferences[0].Kind != "Agent" ||
		claim.Annotations[agentUIDAnnotation] != "agent-uid" || claim.Labels[api.SkillMarketplaceManagedLabel] != "true" ||
		claim.Annotations[api.SkillMarketplaceTeamUIDAnnotation] != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("claim %+v", claim)
	}
	volume := &corev1.PersistentVolume{}
	if err := kube.Get(ctx, client.ObjectKey{Name: VolumeName("agent-uid")}, volume); err != nil {
		t.Fatal(err)
	}
	csi := volume.Spec.CSI
	if !csi.ReadOnly || volume.Spec.MountOptions[0] != "ro" || csi.VolumeAttributes["containerName"] != "marketplace" ||
		csi.VolumeAttributes["storageAccount"] != "account" || csi.VolumeAttributes["protocol"] != "nfs" ||
		volume.Spec.ClaimRef.Name != "skills" || volume.Spec.ClaimRef.Namespace != "cetirun-alice" ||
		volume.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Fatalf("volume %+v", volume.Spec)
	}
	// A second pass changes nothing.
	if _, err := r.Reconcile(ctx, alice); err != nil {
		t.Fatal(err)
	}
}

func TestClaimWaitsForTheAgentNamespace(t *testing.T) {
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "alice", UID: "agent-uid"}, Spec: api.AgentSpec{Team: "research"}}
	kube := newClient(t, agent, team("research", "00000000-0000-4000-8000-000000000001", "policy"))
	r := &ClaimReconciler{Client: kube, Reader: kube, ClaimName: "skills"}
	result, err := r.Reconcile(context.Background(), alice)
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("must try again until the namespace exists: %+v %v", result, err)
	}
}

func TestClaimSkipsOtherAgents(t *testing.T) {
	for name, fixture := range map[string]struct {
		protected bool
		policy    string
	}{
		"unprotected namespace": {false, "policy"},
		"Team without a policy": {true, ""},
	} {
		t.Run(name, func(t *testing.T) {
			r, kube := claimFixture(t, fixture.protected, fixture.policy)
			if _, err := r.Reconcile(context.Background(), alice); err != nil {
				t.Fatal(err)
			}
			claims := &corev1.PersistentVolumeClaimList{}
			if err := kube.List(context.Background(), claims); err != nil || len(claims.Items) != 0 {
				t.Fatalf("no claim may be created: %+v", claims.Items)
			}
		})
	}
}

func TestClaimLeavesAForeignClaimAlone(t *testing.T) {
	r, kube := claimFixture(t, true, "policy")
	ctx := context.Background()
	foreign := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "cetirun-alice", Name: "skills"}}
	if err := kube.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKey{Name: VolumeName("agent-uid")}, &corev1.PersistentVolume{}); err == nil {
		t.Fatal("no volume may be created for a claim that the marketplace did not create")
	}
}

func TestVolumeCollectorRemovesOrphans(t *testing.T) {
	r, kube := claimFixture(t, true, "policy")
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, alice); err != nil {
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
