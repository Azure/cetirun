// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestCopilotRegisteredProxyReadiness(t *testing.T) {
	for _, failure := range []string{"no ready replicas", "deployment missing", "read failed"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			agent := aiGatewayTestAgent(testAIGatewayTeam)
			r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), readyProxy(1))
			if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
				t.Fatal(err)
			}
			signIn(t, kube, agent, testCopilotModels)
			if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
				t.Fatal(err)
			}
			before := agent.DeepCopy().Status.AIGateway
			if before.Copilot.Phase != api.AIGatewayCopilotPhaseReady || len(before.Copilot.Registered) == 0 {
				t.Fatalf("fixture has no ready registered routes: %+v", before.Copilot)
			}
			key := readKeySecret(t, kube)
			token := readSecret(t, kube, api.CopilotTokenSecretName)
			proxyKey := readSecret(t, kube, aiGatewayProxySecretName)
			added, removed, generated := len(gateway.added), len(gateway.deletedModels), len(gateway.generated)
			proxy := &appsv1.Deployment{}
			proxyName := client.ObjectKeyFromObject(readyProxy(1))
			if err := kube.Get(ctx, proxyName, proxy); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "no ready replicas":
				proxy.Status.ReadyReplicas = 0
				if err := kube.Status().Update(ctx, proxy); err != nil {
					t.Fatal(err)
				}
			case "deployment missing":
				if err := kube.Delete(ctx, proxy); err != nil {
					t.Fatal(err)
				}
			case "read failed":
				r.Client = interceptor.NewClient(kube.(client.WithWatch), interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, name client.ObjectKey, object client.Object, options ...client.GetOption) error {
						if _, ok := object.(*appsv1.Deployment); ok && name == proxyName {
							return errors.New("synthetic proxy read failure")
						}
						return c.Get(ctx, name, object, options...)
					},
				})
			}
			assertPreserved := func() {
				t.Helper()
				current := agent.Status.AIGateway
				if !reflect.DeepEqual(current.Copilot.Registered, before.Copilot.Registered) ||
					!reflect.DeepEqual(current.Copilot.Models, before.Copilot.Models) ||
					!reflect.DeepEqual(current.Copilot.Routes, before.Copilot.Routes) ||
					!reflect.DeepEqual(current.Models, before.Models) || current.DefaultModel != before.DefaultModel {
					t.Fatal("proxy readiness changed the registered routes or Agent model configuration")
				}
				if current.Phase != api.AIGatewayPhaseReady || !meta.IsStatusConditionTrue(agent.Status.Conditions, api.ConditionAIGatewayReady) {
					t.Fatal("proxy readiness blocked the shared Gateway key")
				}
				if len(gateway.added) != added || len(gateway.deletedModels) != removed || len(gateway.generated) != generated {
					t.Fatal("proxy readiness re-registered routes or replaced the key")
				}
				for _, secret := range []struct {
					name string
					data map[string][]byte
				}{{key.Name, key.Data}, {token.Name, token.Data}, {proxyKey.Name, proxyKey.Data}} {
					if !reflect.DeepEqual(readSecret(t, kube, secret.name).Data, secret.data) {
						t.Fatalf("proxy readiness changed Secret %s", secret.name)
					}
				}
			}
			for range 2 {
				if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
					t.Fatal(err)
				}
				copilot := agent.Status.AIGateway.Copilot
				if copilot.Phase != api.AIGatewayCopilotPhaseStarting || copilot.Reason != "ProxyStarting" {
					t.Fatalf("unavailable proxy still reports ready: %+v", copilot)
				}
				assertPreserved()
			}
			r.Client = kube
			if failure == "deployment missing" {
				if err := kube.Create(ctx, readyProxy(1)); err != nil {
					t.Fatal(err)
				}
			} else if failure == "no ready replicas" {
				if err := kube.Get(ctx, proxyName, proxy); err != nil {
					t.Fatal(err)
				}
				proxy.Status.ReadyReplicas = 1
				if err := kube.Status().Update(ctx, proxy); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
				t.Fatal(err)
			}
			if current := agent.Status.AIGateway.Copilot; current.Phase != api.AIGatewayCopilotPhaseReady || current.Reason != "" {
				t.Fatalf("proxy recovery was not reported: %+v", current)
			}
			assertPreserved()
		})
	}
}

func TestCopilotProxyReadinessThroughSteadyReconcile(t *testing.T) {
	f := newDeployedAgent(t)
	ctx := context.Background()
	f.r.AIGateway.CopilotProxyImage = testProxyImage
	agent := f.agent(t)
	agent.Spec.ModelAccess = modelAccess(true, true)
	agent.Generation++
	if err := f.r.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.Reconcile(ctx, f.req); err != nil {
		t.Fatal(err)
	}
	agent = f.agent(t)
	signIn(t, f.r.Client, agent, testCopilotModels)
	if err := f.r.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	proxy := readyProxy(1)
	proxy.Labels = map[string]string{"app.kubernetes.io/component": "copilot-proxy"}
	if err := f.r.Create(ctx, proxy); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := f.r.Reconcile(ctx, f.req); err != nil {
			t.Fatal(err)
		}
	}
	before := f.agent(t)
	if before.Status.Phase != api.PhaseReady || before.Status.AIGateway.Copilot.Phase != api.AIGatewayCopilotPhaseReady ||
		aiGatewayVerificationDue(before, true, time.Now()) {
		t.Fatalf("fixture is not at steady state: %+v", before.Status)
	}
	installs := f.chart.installs
	if err := f.r.Get(ctx, client.ObjectKeyFromObject(readyProxy(1)), proxy); err != nil {
		t.Fatal(err)
	}
	for _, ready := range []int32{0, 0, 1} {
		proxy.Status.ReadyReplicas = ready
		if err := f.r.Status().Update(ctx, proxy); err != nil {
			t.Fatal(err)
		}
		requests := f.r.enqueueCopilotProxy(ctx, proxy)
		if len(requests) != 1 || requests[0] != f.req {
			t.Fatalf("Helm-managed proxy event did not enqueue its Agent: %+v", requests)
		}
		result, err := f.r.Reconcile(ctx, requests[0])
		if err != nil {
			t.Fatal(err)
		}
		current := f.agent(t)
		want := api.AIGatewayCopilotPhaseReady
		if ready == 0 {
			want = api.AIGatewayCopilotPhaseStarting
			if result.RequeueAfter > 30*time.Second {
				t.Fatalf("unready proxy used the steady-state delay: %+v", result)
			}
		}
		if current.Status.AIGateway.Copilot.Phase != want {
			t.Fatalf("ready replicas %d: phase %s, want %s", ready, current.Status.AIGateway.Copilot.Phase, want)
		}
		if f.chart.installs != installs || current.Status.Phase != api.PhaseReady ||
			current.Status.AIGateway.KeyID != before.Status.AIGateway.KeyID ||
			!reflect.DeepEqual(current.Status.AIGateway.Models, before.Status.AIGateway.Models) {
			t.Fatal("proxy readiness changed the Agent workload, key or models")
		}
	}
}

func TestCopilotProxyReadinessWithoutRegisteredModels(t *testing.T) {
	ctx := context.Background()
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, _ := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	signIn(t, kube, agent, `[{"id":"grok-code-fast-1"}]`)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	copilot := agent.Status.AIGateway.Copilot
	if copilot.Phase != api.AIGatewayCopilotPhaseReady || len(copilot.Registered) != 0 || len(copilot.Unpriced) != 1 ||
		r.copilotProxyReadinessLost(ctx, agent) {
		t.Fatalf("unpriced models must not wait for an unused proxy: %+v", copilot)
	}
}

func TestCopilotProxyWatchTargetsOnlyItsAgent(t *testing.T) {
	for _, scenario := range []string{"managed proxy", "another workload", "wrong namespace", "wrong name", "missing Agent", "suspended", "Copilot disabled", "Gateway disabled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			agent := aiGatewayTestAgent(testAIGatewayTeam)
			proxy := readyProxy(0)
			proxy.Labels = map[string]string{"app.kubernetes.io/component": "copilot-proxy"}
			switch scenario {
			case "another workload":
				proxy.Labels = nil
			case "wrong namespace":
				proxy.Namespace = "another-namespace"
			case "wrong name":
				proxy.Name = "another-proxy"
			case "suspended":
				agent.Spec.Suspended = true
			case "Copilot disabled":
				agent.Spec.ModelAccess = modelAccess(true, false)
			}
			r, kube, _ := aiGatewayTestReconciler(t, agent)
			if scenario == "missing Agent" {
				agent.Finalizers = nil
				if err := kube.Update(ctx, agent); err != nil {
					t.Fatal(err)
				}
				if err := kube.Delete(ctx, agent); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "Gateway disabled" {
				r.AIGateway.Enabled = false
			}
			requests := r.enqueueCopilotProxy(ctx, proxy)
			if scenario == "managed proxy" {
				if len(requests) != 1 || requests[0].Name != agent.Name || requests[0].Namespace != "" {
					t.Fatalf("proxy without an owner reference did not enqueue its Agent: %+v", requests)
				}
			} else if len(requests) != 0 {
				t.Fatalf("irrelevant proxy event queued an Agent: %+v", requests)
			}
		})
	}
}
