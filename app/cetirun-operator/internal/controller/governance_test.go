// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
)

func TestGovernanceGatewayRevisionTracksLiveManagedBackend(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())

	first, err := r.resolveGovernance(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	// Retain the model name and policy, but replace the concrete Gateway
	// deployment/access group. A cached display catalog would miss this change.
	gateway.shared = []aigateway.Deployment{{
		ID: "cetirun-shared-replaced", ModelName: testSharedModelOne,
		AccessGroups: []string{aiGatewaySharedAccessGroup, "cetirun-model-replaced"}, DatabaseSourceKnown: true,
	}}
	second, err := r.resolveGovernance(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	if first.gatewayRevision == second.gatewayRevision {
		t.Fatal("same-name managed backend replacement did not change governance Gateway revision")
	}
}

func TestGovernanceReprojectionHoldsOnlyActiveOldProjection(t *testing.T) {
	resolution := &governanceResolution{authorizationRevision: "auth-new", gatewayRevision: "gateway-new", credentialRevision: "credential", sandboxRevision: "sandbox"}
	agent := &api.Agent{Status: api.AgentStatus{Governance: &api.GovernanceStatus{
		Authorization: &api.GovernanceAuthorizationStatus{Revision: "auth-old"},
		Projections: api.GovernanceProjectionsStatus{
			Sandbox:          api.GovernanceRevisionProjection{ExpectedRevision: "sandbox"},
			Gateway:          api.GovernanceRevisionProjection{ExpectedRevision: "gateway-old"},
			CredentialPolicy: api.GovernanceRevisionProjection{ExpectedRevision: "credential"},
		},
	}}}
	if governanceReprojectionMustHold(agent, resolution) {
		t.Fatal("a non-running Agent must be allowed to create its first current projection")
	}
	agent.Status.Phase = api.PhaseReady
	if !governanceReprojectionMustHold(agent, resolution) {
		t.Fatal("a running Agent retained an old Gateway projection")
	}
	// A matching verified projection has no hold transition.
	agent.Status.Governance.Authorization.Revision = resolution.authorizationRevision
	agent.Status.Governance.Projections.Gateway.ExpectedRevision = resolution.gatewayRevision
	agent.Status.Sandbox = &api.SandboxStatus{AppliedRevision: resolution.sandboxRevision}
	agent.Status.AIGateway = &api.AIGatewayStatus{Phase: api.AIGatewayPhaseReady, Routed: true, PolicyRevision: resolution.gatewayRevision}
	agent.Status.Conditions = []metav1.Condition{{Type: api.ConditionSandboxReady, Status: metav1.ConditionTrue}}
	if governanceReprojectionMustHold(agent, resolution) {
		t.Fatal("a running Agent with all current projections verified was unnecessarily held")
	}
}

func TestGovernanceDenialRevokesKeyAndStopsOwnedBuiltinWorkload(t *testing.T) {
	ctx := context.Background()
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Status.Phase = api.PhaseReady
	namespace := aiGatewayNamespace()
	owner := metav1.OwnerReference{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: agent.Name, UID: agent.UID}
	replicas := int32(1)
	workload := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
		Name: instanceServiceName(agent.Name), Namespace: namespace.Name, UID: types.UID("workload-uid"),
		OwnerReferences: []metav1.OwnerReference{owner},
		Annotations: map[string]string{
			customAgentUID:                   string(agent.UID),
			"meta.helm.sh/release-name":      helmReleaseName(agent.Name),
			"meta.helm.sh/release-namespace": namespace.Name,
		},
	}, Spec: appsv1.StatefulSetSpec{Replicas: &replicas}}
	route := &istiov1beta1.VirtualService{ObjectMeta: metav1.ObjectMeta{
		Name: virtualServiceName(agent.Name), Namespace: namespace.Name, UID: types.UID("route-uid"),
		OwnerReferences: []metav1.OwnerReference{owner},
		Annotations: map[string]string{
			customAgentUID:                   string(agent.UID),
			"meta.helm.sh/release-name":      helmReleaseName(agent.Name),
			"meta.helm.sh/release-namespace": namespace.Name,
		},
	}}
	r, kube, gateway := aiGatewayTestReconciler(t, agent, namespace, workload, route)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.keys) != 1 {
		t.Fatal("test setup did not create the governed virtual key")
	}

	team := &api.Team{}
	if err := kube.Get(ctx, types.NamespacedName{Name: testAIGatewayTeam}, team); err != nil {
		t.Fatal(err)
	}
	team.Spec.Membership.Subjects = nil
	if err := kube.Update(ctx, team); err != nil {
		t.Fatal(err)
	}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name}})
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("denied Agent must enter a retrying hold: result=%+v err=%v", result, err)
	}
	if len(gateway.keys) != 0 || len(gateway.deletedTokens) != 1 {
		t.Fatalf("denial did not revoke the virtual key: keys=%d deleted=%v", len(gateway.keys), gateway.deletedTokens)
	}
	stopped := &appsv1.StatefulSet{}
	if err := kube.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: workload.Name}, stopped); err != nil || !ptr.Equal(stopped.Spec.Replicas, ptr.To[int32](0)) {
		t.Fatalf("denial did not stop the owned workload: workload=%+v err=%v", stopped.Spec.Replicas, err)
	}
	if err := kube.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: route.Name}, &istiov1beta1.VirtualService{}); !apierrors.IsNotFound(err) {
		t.Fatalf("denial did not remove the owned route: %v", err)
	}
	current := &api.Agent{}
	if err := kube.Get(ctx, types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Governance == nil || current.Status.Governance.Phase != "Denied" {
		t.Fatalf("denial status was not persisted: %+v", current.Status.Governance)
	}
}

func TestGovernanceDependencyFailureBlocksButPreservesKeyAndRecovers(t *testing.T) {
	ctx := context.Background()
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Status.Phase = api.PhaseReady
	namespace := aiGatewayNamespace()
	owner := metav1.OwnerReference{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: agent.Name, UID: agent.UID}
	replicas := int32(1)
	workload := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
		Name: instanceServiceName(agent.Name), Namespace: namespace.Name, UID: types.UID("workload-uid"), OwnerReferences: []metav1.OwnerReference{owner},
		Annotations: map[string]string{customAgentUID: string(agent.UID), "meta.helm.sh/release-name": helmReleaseName(agent.Name), "meta.helm.sh/release-namespace": namespace.Name},
	}, Spec: appsv1.StatefulSetSpec{Replicas: &replicas}}
	route := &istiov1beta1.VirtualService{ObjectMeta: metav1.ObjectMeta{
		Name: virtualServiceName(agent.Name), Namespace: namespace.Name, UID: types.UID("route-uid"), OwnerReferences: []metav1.OwnerReference{owner},
		Annotations: map[string]string{customAgentUID: string(agent.UID), "meta.helm.sh/release-name": helmReleaseName(agent.Name), "meta.helm.sh/release-namespace": namespace.Name},
	}}
	r, kube, gateway := aiGatewayTestReconciler(t, agent, namespace, workload, route)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if err := r.updateStatus(ctx, agent); err != nil {
		t.Fatal(err)
	}
	secret := readKeySecret(t, kube)
	token := aigateway.Token(string(secret.Data[aiGatewayKeySecretField]))
	if gateway.keys[token] == nil {
		t.Fatal("test setup did not create the governed key")
	}

	dependencyDown := true
	r.Client = interceptor.NewClient(kube.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
			if _, ok := object.(*api.Team); ok && dependencyDown {
				return errors.New("synthetic Team API outage")
			}
			return c.Get(ctx, key, object, options...)
		},
	})
	r.APIReader = r.Client
	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name}})
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("dependency outage must enter a retrying hold: result=%+v err=%v", result, err)
	}
	if gateway.keys[token] == nil || !gateway.keys[token].Blocked || len(gateway.deletedTokens) != 0 || len(gateway.deletedAliases) != 0 {
		t.Fatalf("dependency outage deleted or failed to block the existing key: key=%+v tokens=%v aliases=%v", gateway.keys[token], gateway.deletedTokens, gateway.deletedAliases)
	}
	current := &api.Agent{}
	if err := kube.Get(ctx, types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Governance == nil || current.Status.Governance.Phase != "Pending" || current.Status.Governance.Coverage != governanceCoverageNone {
		t.Fatalf("dependency outage was reported as a denial: %+v", current.Status.Governance)
	}

	dependencyDown = false
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name}}); err != nil {
		t.Fatal(err)
	}
	if gateway.keys[token] == nil || gateway.keys[token].Blocked || len(gateway.deletedTokens) != 0 || len(gateway.deletedAliases) != 0 {
		t.Fatalf("recovery did not reuse and unblock the same key: key=%+v tokens=%v aliases=%v", gateway.keys[token], gateway.deletedTokens, gateway.deletedAliases)
	}
}

func TestGovernanceStaleConfigurationObservationPreservesKeyUntilAccepted(t *testing.T) {
	for _, kind := range []string{"Team", "AgentPolicy"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			agent := aiGatewayTestAgent(testAIGatewayTeam)
			namespace := aiGatewayNamespace()
			r, kube, gateway := aiGatewayTestReconciler(t, agent, namespace)
			if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
				t.Fatal(err)
			}
			if err := r.updateStatus(ctx, agent); err != nil {
				t.Fatal(err)
			}
			secret := readKeySecret(t, kube)
			token := aigateway.Token(string(secret.Data[aiGatewayKeySecretField]))

			switch kind {
			case "Team":
				team := &api.Team{}
				if err := kube.Get(ctx, types.NamespacedName{Name: testAIGatewayTeam}, team); err != nil {
					t.Fatal(err)
				}
				team.Generation++
				if err := kube.Update(ctx, team); err != nil {
					t.Fatal(err)
				}
			case "AgentPolicy":
				policy := &api.AgentPolicy{}
				if err := kube.Get(ctx, types.NamespacedName{Name: testPolicyName}, policy); err != nil {
					t.Fatal(err)
				}
				policy.Generation++
				if err := kube.Update(ctx, policy); err != nil {
					t.Fatal(err)
				}
			}
			r.APIReader = r.Client
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name}}); err != nil {
				t.Fatalf("stale %s observation did not enter a controlled hold: %v", kind, err)
			}
			if gateway.keys[token] == nil || !gateway.keys[token].Blocked || len(gateway.deletedTokens) != 0 || len(gateway.deletedAliases) != 0 {
				t.Fatalf("stale %s observation deleted or failed to block the key: key=%+v tokens=%v aliases=%v", kind, gateway.keys[token], gateway.deletedTokens, gateway.deletedAliases)
			}

			switch kind {
			case "Team":
				team := &api.Team{}
				if err := kube.Get(ctx, types.NamespacedName{Name: testAIGatewayTeam}, team); err != nil {
					t.Fatal(err)
				}
				team.Status.ObservedGeneration = team.Generation
				team.Status.Conditions = []metav1.Condition{{Type: api.ConditionTeamAccepted, Status: metav1.ConditionTrue, ObservedGeneration: team.Generation, Reason: "Accepted"}}
				if err := kube.Status().Update(ctx, team); err != nil {
					t.Fatal(err)
				}
			case "AgentPolicy":
				policy := &api.AgentPolicy{}
				if err := kube.Get(ctx, types.NamespacedName{Name: testPolicyName}, policy); err != nil {
					t.Fatal(err)
				}
				policy.Status.ObservedGeneration = policy.Generation
				policy.Status.Conditions = []metav1.Condition{{Type: api.ConditionAgentPolicyAccepted, Status: metav1.ConditionTrue, ObservedGeneration: policy.Generation, Reason: "Accepted"}}
				if err := kube.Status().Update(ctx, policy); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name}}); err != nil {
				t.Fatalf("accepted %s observation did not recover: %v", kind, err)
			}
			if gateway.keys[token] == nil || gateway.keys[token].Blocked || len(gateway.deletedTokens) != 0 || len(gateway.deletedAliases) != 0 {
				t.Fatalf("accepted %s observation did not reuse the same key: key=%+v tokens=%v aliases=%v", kind, gateway.keys[token], gateway.deletedTokens, gateway.deletedAliases)
			}
		})
	}
}

func TestSandboxHoldContinuesShutdownWhenFinalizerPatchFails(t *testing.T) {
	ctx := context.Background()
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	namespace := aiGatewayNamespace()
	owner := metav1.OwnerReference{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: agent.Name, UID: agent.UID}
	replicas := int32(1)
	workload := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
		Name: instanceServiceName(agent.Name), Namespace: namespace.Name, UID: types.UID("workload-uid"), OwnerReferences: []metav1.OwnerReference{owner},
		Annotations: map[string]string{customAgentUID: string(agent.UID), "meta.helm.sh/release-name": helmReleaseName(agent.Name), "meta.helm.sh/release-namespace": namespace.Name},
	}, Spec: appsv1.StatefulSetSpec{Replicas: &replicas}}
	route := &istiov1beta1.VirtualService{ObjectMeta: metav1.ObjectMeta{
		Name: virtualServiceName(agent.Name), Namespace: namespace.Name, UID: types.UID("route-uid"), OwnerReferences: []metav1.OwnerReference{owner},
		Annotations: map[string]string{customAgentUID: string(agent.UID), "meta.helm.sh/release-name": helmReleaseName(agent.Name), "meta.helm.sh/release-namespace": namespace.Name},
	}}
	r, kube, _ := aiGatewayTestReconciler(t, agent, namespace, workload, route)
	injected := errors.New("synthetic finalizer patch failure")
	r.Client = interceptor.NewClient(kube.(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, options ...client.PatchOption) error {
			if _, ok := object.(*api.Agent); ok {
				return injected
			}
			return c.Patch(ctx, object, patch, options...)
		},
	})
	if _, err := r.holdSandbox(ctx, agent); !errors.Is(err, injected) {
		t.Fatalf("finalizer persistence failure was hidden: %v", err)
	}
	stopped := &appsv1.StatefulSet{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(workload), stopped); err != nil || !ptr.Equal(stopped.Spec.Replicas, ptr.To[int32](0)) {
		t.Fatalf("finalizer failure skipped workload shutdown: replicas=%v err=%v", stopped.Spec.Replicas, err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(route), &istiov1beta1.VirtualService{}); !apierrors.IsNotFound(err) {
		t.Fatalf("finalizer failure skipped route removal: %v", err)
	}
	persisted := &api.Agent{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(agent), persisted); err != nil {
		t.Fatal(err)
	}
	if controllerContainsFinalizer(persisted, sandboxHoldFinalizer) || controllerContainsFinalizer(agent, sandboxHoldFinalizer) {
		t.Fatal("an unpersisted hold finalizer was retained in local or stored state")
	}
}

func controllerContainsFinalizer(object client.Object, finalizer string) bool {
	for _, current := range object.GetFinalizers() {
		if current == finalizer {
			return true
		}
	}
	return false
}

func TestPolicyReprojectionBlockClearsOnlyAfterGrantReadback(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if err := r.updateStatus(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := r.beginAIGatewayPolicyReprojection(ctx, agent); err != nil {
		t.Fatal(err)
	}
	secret := readKeySecret(t, kube)
	token := aigateway.Token(string(secret.Data[aiGatewayKeySecretField]))
	if !gateway.keys[token].Blocked || agent.Status.AIGateway.Reason != aiGatewayReasonPolicyReprojection {
		t.Fatalf("old grant was not recorded as an operator-owned reprojection block: %+v", agent.Status.AIGateway)
	}
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if gateway.keys[token].Blocked || agent.Status.AIGateway.Reason != "" {
		t.Fatalf("verified current grant did not clear the temporary reprojection block: %+v", agent.Status.AIGateway)
	}
}

func TestManagedGatewayGroupPollutionBlocksAnExistingGrant(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if err := r.updateStatus(ctx, agent); err != nil {
		t.Fatal(err)
	}
	secret := readKeySecret(t, kube)
	token := aigateway.Token(string(secret.Data[aiGatewayKeySecretField]))
	gateway.shared = append(gateway.shared, aigateway.Deployment{
		ID: "database-copy", ModelName: "unauthorized-model", AccessGroups: []string{testManagedGroup},
		FromDatabase: true, DatabaseSourceKnown: true,
	})
	if _, err := r.resolveGovernance(ctx, agent); err == nil || !isGovernanceDependencyError(err) {
		t.Fatalf("a polluted managed group must invalidate current authorization evidence: %v", err)
	}
	// The Agent controller routes that dependency failure through this hold
	// path. Workload teardown may have no fixture resources here, but key
	// revocation must happen before it is attempted.
	_, _ = r.holdGovernanceDependencyUnavailable(ctx, agent)
	if !gateway.keys[token].Blocked || agent.Status.AIGateway.Reason != aiGatewayReasonPolicyReprojection {
		t.Fatalf("the old grant remained usable after catalog pollution: key=%+v status=%+v", gateway.keys[token], agent.Status.AIGateway)
	}
	if err := r.reconcileAIGateway(ctx, agent, true); err == nil {
		t.Fatal("a polluted managed group unexpectedly produced a grant")
	}
	if !gateway.keys[token].Blocked || agent.Status.AIGateway.Reason != aiGatewayReasonPolicyReprojection {
		t.Fatalf("a failed replacement grant lost ownership of the temporary block: key=%+v status=%+v", gateway.keys[token], agent.Status.AIGateway)
	}
	gateway.shared = gateway.shared[:len(gateway.shared)-1]
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal("the same key did not recover after the collision was removed:", err)
	}
	if gateway.keys[token].Blocked || agent.Status.AIGateway.Reason != "" || agent.Status.AIGateway.KeyID != token[:aiGatewayKeyIDLength] {
		t.Fatalf("the verified grant did not restore the same key: key=%+v status=%+v", gateway.keys[token], agent.Status.AIGateway)
	}
}
