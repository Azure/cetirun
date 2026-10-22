// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package controller

import (
	"errors"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestNativeSandboxSharedPolicyAPIServer(t *testing.T) {
	h := newNativeStartAPIHarness(t, true, true)
	agent := h.agent.DeepCopy()
	agent.ObjectMeta = metav1.ObjectMeta{Name: "second"}
	agent.Status = api.AgentStatus{}
	agent.Spec.User.Alias, agent.Spec.Team, agent.Spec.SandboxRef = agent.Name, "", ""
	if err := h.member.Create(h.ctx, agent); err != nil {
		t.Fatalf("create another member Agent through current single-policy admission: %v", err)
	}
	peer := *h
	peer.nativeSandboxFixture = &nativeSandboxFixture{
		r: h.r, kube: h.kube, gateway: h.gateway, agent: agent,
	}
	type lifetime struct {
		harness        *nativeStartAPIHarness
		state, receipt *corev1.ConfigMap
		expected       *w.Expectation
		pod            *corev1.Pod
	}
	lifetimes := []lifetime{{harness: h}, {harness: &peer}}
	for i := range lifetimes {
		run := &lifetimes[i]
		current := run.harness
		current.seedRetainedBootstrap(t)
		current.create(t, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
			Namespace: instanceNamespace(current.agent.Name), Name: "default",
		}})
		current.prepare(t)
		current.reconcileOK(t)
		run.state, run.expected = current.expectation(t)
		current.assertHoldAdmission(t, run.expected.Revision, true)
		var err error
		run.receipt, _, err = current.r.readSandboxPreparationReceipt(h.ctx, current.current(t))
		if err != nil {
			t.Fatal(err)
		}
		run.pod = testfixture.Pod(*run.expected)
		run.pod.Spec.Overhead = nil
		current.create(t, run.pod)
		current.observeReadyPod(t, run.pod)
		current.reconcileOK(t)
		current.assertReady(t)
	}
	first, second := lifetimes[0], lifetimes[1]
	if first.expected.Version != "v1" || second.expected.Version != "v1" ||
		first.expected.Binding.TeamUID != second.expected.Binding.TeamUID ||
		first.expected.Binding.AgentPolicyUID != second.expected.Binding.AgentPolicyUID ||
		first.expected.Namespace.UID == second.expected.Namespace.UID || first.expected.Home.UID == second.expected.Home.UID {
		t.Fatal("single-policy Agents did not retain shared configuration with independent private lifetimes")
	}
	_, binding, err := sandboxbinding.Read(h.ctx, h.member, client.ObjectKeyFromObject(agent))
	if err != nil || binding != second.expected.Binding {
		t.Fatal("served member authorization differs from the started single-policy binding:", err)
	}
	team := &api.Team{}
	if err := h.admin.Get(h.ctx, client.ObjectKey{Name: h.agent.Spec.Team}, team); err != nil {
		t.Fatal(err)
	}
	before := &api.AgentPolicy{}
	if err := h.admin.Get(h.ctx, client.ObjectKey{Name: team.Spec.AgentPolicyRef}, before); err != nil {
		t.Fatal(err)
	}
	after := before.DeepCopy()
	after.Spec.Security.ReadOnlyRootFilesystem = ptr.To(false)
	store := h.r.SandboxNativeCoordination
	if store == nil {
		t.Fatal("rendered configuration did not install native coordination")
	}
	assertChangeBlocked := func() {
		t.Helper()
		if _, err := store.AcquireConfigurationChange(h.ctx, before, after); !errors.Is(err, sandboxcoordination.ErrConflict) {
			t.Fatalf("active native lifetime did not reserve its selected policy: %v", err)
		}
		if err := h.admin.Update(h.ctx, after.DeepCopy()); err == nil || !strings.Contains(err.Error(), "CoordinationRequired") {
			t.Fatalf("served policy admission accepted an unreserved shared-policy change: %v", err)
		}
	}
	assertChangeBlocked()
	for _, run := range lifetimes {
		ticket, found, err := store.ReadNative(h.ctx, run.expected.Agent.UID)
		if err != nil || !found || ticket.Reservation.Intent.Binding != run.expected.Binding {
			t.Fatal("current native lifetime has no matching durable reservation:", err)
		}
	}
	generatedKeys := len(h.gateway.generated)
	if len(h.gateway.keys) != 2 {
		t.Fatal("shared-policy Agents did not receive independent model keys")
	}
	h.cleanupAgent(t, first.pod, first.state, first.receipt)
	if _, found, err := store.ReadNative(h.ctx, first.expected.Agent.UID); err != nil || found {
		t.Fatal("first Agent cleanup retained its native reservation:", err)
	}
	assertChangeBlocked()
	peer.reconcileOK(t)
	peer.assertReady(t)
	if peer.workload(t).UID != second.expected.Workload.UID ||
		ptr.Deref(peer.workload(t).Spec.Replicas, -1) != 1 || len(h.gateway.keys) != 1 ||
		len(h.gateway.generated) != generatedKeys {
		t.Fatal("first Agent cleanup stopped or recreated the remaining Agent or its model key")
	}

	// Reconstruct the production factories with new starts disabled; cleanup
	// must use the persisted single-policy lifetime, not the old process.
	config := h.sources
	config.NativeExecution = false
	h.r.SandboxPreparation, err = NewKubernetesSandboxPreparation(config, h.kube, h.localHelm, h.r.OperatorNamespace, h.r.ChartVersion)
	if err != nil {
		t.Fatal(err)
	}
	h.r.SandboxNativeCoordination, err = NewNativeSandboxCoordination(config, h.kube, h.kube, h.r.OperatorNamespace)
	if err != nil {
		t.Fatal(err)
	}
	peer.cleanupAgent(t, second.pod, second.state, second.receipt)
	if _, found, err := store.ReadNative(h.ctx, second.expected.Agent.UID); err != nil || found || len(h.gateway.keys) != 0 {
		t.Fatal("restart with native starts disabled lost cleanup of the second lifetime:", err)
	}
	change, err := store.AcquireConfigurationChange(h.ctx, before, after)
	if err != nil {
		t.Fatal("shared policy remained locked after both lifetimes completed:", err)
	}
	adapter, err := sandboxcoordination.NewKubernetesChangeAdapter(h.admin, h.admin, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileChange(h.ctx, change, adapter); err != nil {
		t.Fatal("coordinated policy update did not pass served admission after cleanup:", err)
	}
	current := &api.AgentPolicy{}
	if err := h.admin.Get(h.ctx, client.ObjectKeyFromObject(before), current); err != nil ||
		current.UID != before.UID || current.ResourceVersion == before.ResourceVersion ||
		ptr.Deref(current.Spec.Security.ReadOnlyRootFilesystem, true) {
		t.Fatal("completed policy update lost identity or failed to apply its exact change:", err)
	}
}
