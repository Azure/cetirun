// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload_test

import (
	"testing"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	"helm.sh/helm/v3/pkg/chart/loader"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNativePodObservationPinsUIDAndNodeTopology(t *testing.T) {
	f := testfixture.New("openclaw")
	bundle, err := w.Build(f.Input)
	if err != nil {
		t.Fatal(err)
	}
	chart, err := loader.Load("../../../cetirun-chart")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := helm.PrepareSandboxChart(chart, map[string]any{
		"agentType": "openclaw", "agentRequest": map[string]any{"type": "openclaw"},
		"secrets": map[string]any{"existingSecret": "runtime-secret", "provider": "openai", "model": "example-model"},
	}, nil, bundle)
	if err != nil {
		t.Fatal(err)
	}
	template, err := prepared.SandboxTemplate()
	if err != nil {
		t.Fatal(err)
	}
	workload := testfixture.Workload(f.Input, *template)
	expected, err := bundle.Expect(*template, workload, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected.SinglePod, expected.PodUID = true, "first-pod-uid"
	pod := testfixture.Pod(expected)
	pod.UID, pod.Spec.NodeName = expected.PodUID, "example-node"
	pod.Labels[corev1.LabelTopologyZone] = "example-zone"
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "example-node", UID: "node-uid", Labels: map[string]string{corev1.LabelTopologyZone: "example-zone"},
	}}
	if err := w.ValidatePodOnNode(pod, node, expected); err != nil {
		t.Fatal(err)
	}
	if expected.Template.Spec.NodeName != "" || expected.Template.Labels[corev1.LabelTopologyZone] != "" {
		t.Fatal("observation mutated the persisted expectation")
	}
	for _, change := range []func(*corev1.Pod){
		func(p *corev1.Pod) { p.UID = "replacement-pod-uid" },
		func(p *corev1.Pod) { p.Spec.NodeName = "different-node" },
		func(p *corev1.Pod) { p.Labels[corev1.LabelTopologyZone] = "different-zone" },
		func(p *corev1.Pod) { p.Spec.Containers[0].Image = "registry.example.com/unapproved:1" },
	} {
		changed := pod.DeepCopy()
		change(changed)
		if err := w.ValidatePodOnNode(changed, node, expected); err == nil {
			t.Fatal("changed Pod identity, placement or runtime was accepted")
		}
	}
}
