// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func legacyGatewayFixture(t *testing.T) (*AgentReconciler, *api.Agent, *fakeAIGateway) {
	t.Helper()
	agent := aiGatewayTestAgent("legacy-team")
	agent.Spec.SandboxRef, agent.Spec.ModelAccess, agent.Status.Policy = "", nil, nil
	team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: agent.Spec.Team}}
	r, kube, gateway := aiGatewayTestReconciler(t, agent, team)
	r.AIGateway.CopilotProxyImage = ""
	namespace := aiGatewayNamespace()
	namespace.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(agent, api.GroupVersion.WithKind("Agent"))}
	if err := kube.Create(context.Background(), namespace); err != nil {
		t.Fatal(err)
	}
	return r, agent, gateway
}

func TestLegacyCompatibilityDoesNotBypassPolicyIntent(t *testing.T) {
	for name, mutate := range map[string]func(*api.Agent){
		"unconfigured legacy Team": func(a *api.Agent) {},
		"missing Team":             func(a *api.Agent) { a.Spec.Team = "" },
		"selected sandbox":         func(a *api.Agent) { a.Spec.SandboxRef = "missing-sandbox" },
		"policy status":            func(a *api.Agent) { a.Status.Policy = &api.PolicyStatus{Phase: "Pending"} },
		"policy grant":             func(a *api.Agent) { a.Status.AIGateway = &api.AIGatewayStatus{PolicyRevision: "previous"} },
		"held lifetime":            func(a *api.Agent) { a.Finalizers = append(a.Finalizers, sandbox.HoldFinalizer) },
		"policy budget": func(a *api.Agent) {
			a.Spec.ModelAccess = &api.ModelAccessSpec{MonthlyBudgetUSD: "5"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, agent, gateway := legacyGatewayFixture(t)
			mutate(agent)
			required, err := r.agentPolicyRequired(context.Background(), agent)
			if err != nil || !required {
				t.Fatalf("policy intent classified as legacy: %v %v", required, err)
			}
			if _, err := r.resolveAIGatewayGrant(context.Background(), gateway, agent); err == nil || len(gateway.generated) != 0 {
				t.Fatal("an invalid governed binding received a legacy Gateway grant")
			}
		})
	}
	t.Run("incomplete Team configuration", func(t *testing.T) {
		r, agent, gateway := legacyGatewayFixture(t)
		team := &api.Team{}
		if err := r.Get(context.Background(), client.ObjectKey{Name: agent.Spec.Team}, team); err != nil {
			t.Fatal(err)
		}
		team.Spec.AgentPolicyRef = "missing-policy"
		if err := r.Update(context.Background(), team); err != nil {
			t.Fatal(err)
		}
		if _, err := r.resolveAIGatewayGrant(context.Background(), gateway, agent); err == nil {
			t.Fatal("partial Team policy silently used a legacy grant")
		}
	})
	t.Run("unreadable Team", func(t *testing.T) {
		r, agent, gateway := legacyGatewayFixture(t)
		r.APIReader = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
				if _, ok := object.(*api.Team); ok {
					return errors.New("synthetic Team read outage")
				}
				return c.Get(ctx, key, object, options...)
			},
		})
		if _, err := r.resolveAIGatewayGrant(context.Background(), gateway, agent); err == nil {
			t.Fatal("Team read failure silently used a legacy grant")
		}
	})
}

func governedGatewayFixture(t *testing.T) (*AgentReconciler, *api.Agent, *fakeAIGateway) {
	t.Helper()
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Spec.ModelAccess = modelAccess(true, true)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	return r, agent, gateway
}
