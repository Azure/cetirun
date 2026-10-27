// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	"github.com/Azure/CetiRun/app/internal/terminalpolicy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReconciledCustomTerminalWaitsWithoutHoldingMainWorkloadAndSuspendsBothWorkloads(t *testing.T) {
	ctx := context.Background()
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "custom", true, false,
		func(r *AgentReconciler, kube client.Client) {
			policy := &api.AgentPolicy{}
			if err := kube.Get(ctx, client.ObjectKey{Name: "policy-example"}, policy); err != nil {
				t.Fatal(err)
			}
			policy.Spec.Harnesses.Allowed = append(policy.Spec.Harnesses.Allowed, api.AgentTypeCustom)
			if err := kube.Update(ctx, policy); err != nil {
				t.Fatal(err)
			}
			agent := &api.Agent{}
			if err := kube.Get(ctx, client.ObjectKey{Name: "example"}, agent); err != nil {
				t.Fatal(err)
			}
			r.CustomApprovedImages = []string{*agent.Spec.Agent.Image}
			configureReconciledCustomTerminalPlatform(t, ctx, r, kube)
		},
	)
	// Configure terminal input only after the fixture's bootstrap pass. That
	// keeps bootstrap coverage independent from the separate terminal rollout.
	f.r.CetiRunHelmValues = map[string]any{
		"ttyd": map[string]any{
			"enabled": true,
			"image": map[string]any{
				"repository": "registry.example.test/cetirun-terminal",
				"digest":     "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			},
			"port":           7681,
			"sessionSeconds": 1800,
		},
	}
	if err := f.kube.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace: instanceNamespace(f.agent.Name), Name: "kube-root-ca.crt",
	}, Data: map[string]string{"ca.crt": "synthetic-ca"}}); err != nil {
		t.Fatal(err)
	}

	// The first post-bootstrap pass consumes the Gateway projection and applies
	// the common workload bundle, including the terminal Deployment.
	var deployment *appsv1.Deployment
	var reconcileErr error
	for attempt := 0; attempt < 4; attempt++ {
		_, reconcileErr = f.reconcile()
		candidate := &appsv1.Deployment{}
		err := f.kube.Get(ctx, client.ObjectKey{
			Namespace: instanceNamespace(f.agent.Name), Name: terminalpolicy.WorkloadName(instanceServiceName(f.agent.Name)),
		}, candidate)
		if err == nil {
			deployment = candidate
			break
		}
		if reconcileErr != nil {
			t.Fatalf("apply common Custom terminal bundle: %v", reconcileErr)
		}
	}
	if deployment == nil {
		current := f.current(t)
		t.Fatalf("common Custom did not render its terminal Deployment: phase=%s conditions=%v", current.Status.Phase, current.Status.Conditions)
	}
	if ptr.Deref(deployment.Spec.Replicas, -1) != 1 {
		t.Fatalf("common Custom terminal Deployment replicas = %v, want one", deployment.Spec.Replicas)
	}
	if _, err := f.reconcile(); err != nil {
		t.Fatalf("release common Custom expectation: %v", err)
	}
	setReconciledCustomWorkloadReady(t, f)

	_, err := f.reconcile()
	if err == nil || !strings.Contains(err.Error(), "common Custom terminal is not ready") {
		t.Fatalf("terminal-pending reconcile error = %v, want common terminal readiness wait", err)
	}
	current := f.current(t)
	terminal := meta.FindStatusCondition(current.Status.Conditions, api.ConditionTerminalConfigured)
	if terminal == nil || terminal.Status != metav1.ConditionFalse || terminal.Reason != "PlatformTerminalPending" {
		t.Fatalf("terminal-pending condition = %#v; conditions=%v", terminal, current.Status.Conditions)
	}
	if main := f.workload(t); ptr.Deref(main.Spec.Replicas, -1) != 1 {
		t.Fatalf("terminal wait held the primary Custom workload: replicas=%v", main.Spec.Replicas)
	}
	if deployment := reconciledCustomTerminalDeployment(t, f); ptr.Deref(deployment.Spec.Replicas, -1) != 1 {
		t.Fatalf("terminal Deployment replicas = %v, want one while it is pending", deployment.Spec.Replicas)
	}

	setReconciledCustomTerminalAvailable(t, f, true)
	for attempt := 0; attempt < 3; attempt++ {
		f.reconcileOK(t)
		current = f.current(t)
		if current.Status.Phase == api.PhaseReady {
			break
		}
	}
	if current.Status.Phase != api.PhaseReady {
		t.Fatalf("common Custom did not become Ready after terminal availability: phase=%s conditions=%v", current.Status.Phase, current.Status.Conditions)
	}

	current.Spec.Suspended = true
	current.Generation++
	if err := f.kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	f.reconcileOK(t)
	if current = f.current(t); current.Status.Phase != api.PhaseSuspended {
		t.Fatalf("suspended common Custom phase = %s; conditions=%v", current.Status.Phase, current.Status.Conditions)
	}
	if main := f.workload(t); ptr.Deref(main.Spec.Replicas, -1) != 0 {
		t.Fatalf("suspension left primary Custom workload running: replicas=%v", main.Spec.Replicas)
	}
	if deployment := reconciledCustomTerminalDeployment(t, f); ptr.Deref(deployment.Spec.Replicas, -1) != 0 {
		t.Fatalf("suspension left Custom terminal Deployment running: replicas=%v", deployment.Spec.Replicas)
	}
	assertNoLegacyCustomCompletion(t, f)
}

func configureReconciledCustomTerminalPlatform(t *testing.T, ctx context.Context, r *AgentReconciler, kube client.Client) {
	t.Helper()
	authority := &corev1.ConfigMap{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: r.OperatorNamespace, Name: "sandbox-platform"}, authority); err != nil {
		t.Fatal(err)
	}
	document := SandboxPlatformConfiguration{}
	if err := json.Unmarshal([]byte(authority.Data[SandboxPlatformConfigurationKey]), &document); err != nil {
		t.Fatal(err)
	}
	platform := document.Platforms[api.SandboxKata]
	platform.Topology.KubernetesAPI = []sandboxpolicy.Destination{{
		Peer:  sandboxpolicy.Peer{Name: "api", UID: "example-api-uid", IPs: []string{"203.0.113.10"}},
		Ports: []api.AgentPolicyPort{{Port: 6443, Protocol: corev1.ProtocolTCP}},
	}}
	document.Platforms[api.SandboxKata] = platform
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	authority.Data[SandboxPlatformConfigurationKey] = string(raw)
	if err := kube.Update(ctx, authority); err != nil {
		t.Fatal(err)
	}
}

func reconciledCustomTerminalDeployment(t *testing.T, f *nativeSandboxFixture) *appsv1.Deployment {
	t.Helper()
	deployment := &appsv1.Deployment{}
	if err := f.kube.Get(context.Background(), client.ObjectKey{
		Namespace: instanceNamespace(f.agent.Name), Name: terminalpolicy.WorkloadName(instanceServiceName(f.agent.Name)),
	}, deployment); err != nil {
		t.Fatal(err)
	}
	return deployment
}

func setReconciledCustomTerminalAvailable(t *testing.T, f *nativeSandboxFixture, available bool) {
	t.Helper()
	deployment := reconciledCustomTerminalDeployment(t, f)
	deployment.Status.ObservedGeneration = deployment.Generation
	if available {
		deployment.Status.Replicas, deployment.Status.ReadyReplicas, deployment.Status.AvailableReplicas = 1, 1, 1
	} else {
		deployment.Status.Replicas, deployment.Status.ReadyReplicas, deployment.Status.AvailableReplicas = 0, 0, 0
	}
	if err := f.kube.Status().Update(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
}
