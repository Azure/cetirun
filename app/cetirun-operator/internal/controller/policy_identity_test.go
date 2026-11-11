// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"reflect"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// Freeze the pre-fix wire input independently of the normalization helper.
func legacyOptionalCredentialRevision(t *testing.T, uid types.UID) string {
	t.Helper()
	revision, err := policyHash(struct {
		Version   string                       `json:"version"`
		PolicyUID types.UID                    `json:"policyUID"`
		Enabled   bool                         `json:"enabled"`
		Identity  api.AgentPolicyAgentIdentity `json:"identity"`
	}{"credential-policy-v2", uid, false, api.AgentPolicyAgentIdentity{Allowed: true, CredentialServices: []string{"azure-resource-manager"}}})
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestNativeOptionalIdentityDoesNotRestartRunningAgents(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		for _, legacy := range []bool{false, true} {
			name := kind + "/disabled"
			if legacy {
				name = kind + "/legacy-optional"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				f := readyPolicyRateLimitFixture(t, kind, true)
				policyKey := client.ObjectKey{Name: f.current(t).Status.Policy.EffectivePolicyRef.Name}
				optional := api.AgentPolicyAgentIdentity{Allowed: true, CredentialServices: []string{"azure-resource-manager"}}
				policies := &AgentPolicyReconciler{Client: f.kube, APIReader: f.kube, NativeExecution: true,
					Source: f.r.SandboxPreparation.Configuration, Now: f.r.SandboxPreparation.Now,
					MaxSnapshotAge: f.r.SandboxPreparation.MaxSnapshotAge}
				if legacy {
					policy := &api.AgentPolicy{}
					if err := f.kube.Get(ctx, policyKey, policy); err != nil {
						t.Fatal(err)
					}
					policy.Spec.AgentIdentity = optional
					policy.Generation++
					if err := f.kube.Update(ctx, policy); err != nil {
						t.Fatal(err)
					}
					if _, err := policies.Reconcile(ctx, ctrl.Request{NamespacedName: policyKey}); err != nil {
						t.Fatal(err)
					}
					agent := f.current(t)
					agent.Status.Policy.Enforcement.Credential.DesiredRevision = legacyOptionalCredentialRevision(t, policy.UID)
					if err := f.kube.Status().Update(ctx, agent); err != nil {
						t.Fatal(err)
					}
				}
				beforeAgent, beforeWorkload := f.current(t), f.workload(t)
				stages, stopWrites := f.installer.stages, 0
				token := aigateway.Token(f.gateway.generated[0].Key)
				f.gateway.keys[token].Spend = ptr.To(2.5)
				gateway := &policyLimitBlockRecorder{fakeAIGateway: f.gateway}
				f.r.aiGatewayClientFactory = func(context.Context) (aiGatewayKeyClient, error) { return gateway, nil }
				checkStop := func(obj client.Object) {
					if workload, ok := obj.(*appsv1.StatefulSet); ok && workload.Name == beforeWorkload.Name && ptr.Deref(workload.Spec.Replicas, 1) == 0 {
						stopWrites++
					}
				}
				writer := interceptor.NewClient(f.kube, interceptor.Funcs{
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						checkStop(obj)
						return c.Update(ctx, obj, opts...)
					},
					Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						checkStop(obj)
						return c.Patch(ctx, obj, patch, opts...)
					},
				})
				f.r.Client, f.r.APIReader = writer, writer
				for _, identity := range []api.AgentPolicyAgentIdentity{optional, {}, optional} {
					policy := &api.AgentPolicy{}
					if err := f.kube.Get(ctx, policyKey, policy); err != nil {
						t.Fatal(err)
					}
					policy.Spec.AgentIdentity = identity
					policy.Generation++
					if err := f.kube.Update(ctx, policy); err != nil {
						t.Fatal(err)
					}
					for _, accepted := range []bool{false, true, true} {
						if accepted {
							if _, err := policies.Reconcile(ctx, ctrl.Request{NamespacedName: policyKey}); err != nil {
								t.Fatal(err)
							}
						}
						f.reconcileOK(t)
						after, workload := f.current(t), f.workload(t)
						key := f.gateway.keys[token]
						if after.Status.Phase != api.PhaseReady || after.Status.Policy.Phase != "Applied" ||
							after.Status.AccessURL != beforeAgent.Status.AccessURL || after.Status.AIGateway.KeyID != beforeAgent.Status.AIGateway.KeyID ||
							!reflect.DeepEqual(after.Spec, beforeAgent.Spec) || workload.UID != beforeWorkload.UID || !reflect.DeepEqual(workload.Spec, beforeWorkload.Spec) ||
							f.installer.stages != stages || stopWrites != 0 || gateway.blocks != 0 || key == nil || key.Blocked || ptr.Deref(key.Spend, 0) != 2.5 ||
							len(f.gateway.generated) != 1 || len(f.gateway.deletedTokens) != 0 || len(f.gateway.deletedAliases) != 0 {
							t.Fatalf("optional=%t accepted=%t interrupted identity-free Agent: phase=%s stops=%d blocks=%d applies=%d/%d", identity.Allowed, accepted, after.Status.Phase, stopWrites, gateway.blocks, f.installer.stages, stages)
						}
						if legacy && after.Status.Policy.Enforcement.Credential.DesiredRevision == beforeAgent.Status.Policy.Enforcement.Credential.DesiredRevision {
							t.Fatal("legacy revision was not migrated")
						}
					}
				}
			})
		}
	}
}

func TestInactiveCredentialUpgradeKeepsEnforcementGuards(t *testing.T) {
	policy := &api.AgentPolicy{ObjectMeta: metav1.ObjectMeta{UID: "policy-uid"}}
	normalized, err := inactiveCredentialPolicyRevision(policy.UID, api.AgentPolicyAgentIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	resolution := &policyResolution{policy: policy, team: &api.Team{ObjectMeta: metav1.ObjectMeta{UID: "team-uid"}}, credentialRevision: normalized, sandboxRevision: "sandbox", gatewayRevision: "gateway"}
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Generation: 3}, Status: api.AgentStatus{
		Phase: api.PhaseReady,
		Policy: &api.PolicyStatus{EffectivePolicyRef: &api.EffectivePolicyReference{PolicyObjectReference: api.PolicyObjectReference{UID: policy.UID}}, Enforcement: api.PolicyEnforcementStatus{
			Sandbox: api.PolicyRevisionExpectation{DesiredRevision: "sandbox"}, Gateway: api.PolicyRevisionExpectation{DesiredRevision: "gateway"},
			Credential: api.PolicyRevisionExpectation{DesiredRevision: legacyOptionalCredentialRevision(t, policy.UID)},
		}},
		Sandbox:    &api.SandboxStatus{AppliedRevision: "sandbox", TeamUID: resolution.team.UID, AgentPolicyUID: policy.UID},
		AIGateway:  &api.AIGatewayStatus{Phase: api.AIGatewayPhaseReady, Routed: true, PolicyRevision: "gateway"},
		Conditions: []metav1.Condition{{Type: api.ConditionSandboxReady, Status: metav1.ConditionTrue, ObservedGeneration: 3}},
	}}
	for _, explicit := range []bool{false, true} {
		current := agent.DeepCopy()
		if explicit {
			current.Spec.AgentIdentity = &api.AgentIdentitySpec{Enabled: false}
			current.Status.AgentIdentity = &api.AgentIdentityStatus{Phase: api.AgentIdentityPhaseDisabled}
		}
		if policyReprojectionMustHold(current, resolution) {
			t.Fatal("verified inactive legacy revision was held")
		}
	}
	for name, mutate := range map[string]func(*api.Agent){
		"unknown revision":   func(a *api.Agent) { a.Status.Policy.Enforcement.Credential.DesiredRevision = "unknown" },
		"old policy":         func(a *api.Agent) { a.Status.Policy.EffectivePolicyRef.UID = "old-policy" },
		"missing binding":    func(a *api.Agent) { a.Status.Policy.EffectivePolicyRef = nil },
		"identity requested": func(a *api.Agent) { a.Spec.AgentIdentity = &api.AgentIdentitySpec{Enabled: true} },
		"cleanup pending": func(a *api.Agent) {
			a.Status.AgentIdentity = &api.AgentIdentityStatus{Phase: api.AgentIdentityPhaseDisabled, ClientID: "old-client"}
		},
		"creation attempted": func(a *api.Agent) {
			a.Status.AgentIdentity = &api.AgentIdentityStatus{Phase: api.AgentIdentityPhaseDisabled, CreationAttempted: true}
		},
		"credential receipt": func(a *api.Agent) {
			a.Status.AgentIdentity = &api.AgentIdentityStatus{Phase: api.AgentIdentityPhaseDisabled, CredentialPolicyRevision: "old-grant"}
		},
		"changed gateway": func(a *api.Agent) { a.Status.Policy.Enforcement.Gateway.DesiredRevision = "old-gateway" },
		"changed sandbox": func(a *api.Agent) { a.Status.Policy.Enforcement.Sandbox.DesiredRevision = "old-sandbox" },
		"stale evidence":  func(a *api.Agent) { a.Status.Conditions[0].ObservedGeneration-- },
	} {
		t.Run(name, func(t *testing.T) {
			current := agent.DeepCopy()
			mutate(current)
			if !policyReprojectionMustHold(current, resolution) {
				t.Fatal("legacy compatibility bypassed enforcement")
			}
		})
	}
	policy.Spec.AgentIdentity = api.AgentPolicyAgentIdentity{Allowed: true, Required: true, CredentialServices: []string{"azure-resource-manager"}}
	if validatePolicyIdentity(agent, policy) == nil || !policyReprojectionMustHold(agent, resolution) {
		t.Fatal("Required accepted an identity-free Agent")
	}
	policy.Spec.AgentIdentity = api.AgentPolicyAgentIdentity{}
	agent.Spec.AgentIdentity = &api.AgentIdentitySpec{Enabled: true}
	if validatePolicyIdentity(agent, policy) == nil {
		t.Fatal("Disabled accepted an identity-enabled Agent")
	}
}
