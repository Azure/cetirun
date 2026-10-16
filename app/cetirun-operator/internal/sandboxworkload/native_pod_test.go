// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload_test

import (
	"slices"
	"testing"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	"helm.sh/helm/v3/pkg/chart/loader"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNativeInitProcessKeepsExactHelperBoundaries(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		t.Run(kind, func(t *testing.T) {
			fixture := testfixture.New(kind)
			bundle, err := w.Build(fixture.Input)
			if err != nil {
				t.Fatal(err)
			}
			chart, err := loader.Load("../../../cetirun-chart")
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := helm.PrepareSandboxChart(chart, map[string]any{
				"agentType": kind, "agentRequest": map[string]any{"type": kind},
				"secrets": map[string]any{"existingSecret": "runtime-secret", "provider": "openai", "model": "example-model"},
			}, nil, bundle)
			if err != nil {
				t.Fatal(err)
			}
			template, err := prepared.SandboxTemplate()
			if err != nil {
				t.Fatal(err)
			}
			for index, container := range template.Spec.InitContainers {
				command := slices.Clone(container.Command)
				if kind == "openclaw" {
					if len(command) < 4 || !slices.Equal(command[:3], []string{"/usr/bin/tini", "-g", "--"}) {
						t.Fatal("approved init supervision missing")
					}
					legacy := template.DeepCopy()
					legacy.Spec.InitContainers[index].Command = command[3:]
					if err := bundle.ValidateTemplate(legacy, template.Spec.Containers[0].Image); err != nil {
						t.Fatal("exact prior entrypoint rejected:", err)
					}
					for _, invalid := range [][]string{
						append([]string{"/usr/bin/tini", "--"}, command[3:]...),
						append([]string{"/usr/bin/tini", "-g", "-e", "1", "--"}, command[3:]...),
						append([]string{"/unapproved/tini", "-g", "--"}, command[3:]...),
						append(slices.Clone(command), "unapproved"),
					} {
						changed := template.DeepCopy()
						changed.Spec.InitContainers[index].Command = invalid
						if bundle.ValidateTemplate(changed, template.Spec.Containers[0].Image) == nil {
							t.Fatal("unapproved helper command accepted")
						}
					}
				} else {
					changed := template.DeepCopy()
					changed.Spec.InitContainers[index].Command = append([]string{"/usr/bin/tini", "-g", "--"}, command...)
					if bundle.ValidateTemplate(changed, template.Spec.Containers[0].Image) == nil {
						t.Fatal("unapproved wrapper accepted for another harness")
					}
				}
			}
		})
	}
}

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
	expected.Template.Finalizers = []string{w.NativePodFinalizer}
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
		func(p *corev1.Pod) { p.Spec.InitContainers[0].Command = p.Spec.InitContainers[0].Command[3:] },
	} {
		changed := pod.DeepCopy()
		change(changed)
		if err := w.ValidatePodOnNode(changed, node, expected); err == nil {
			t.Fatal("changed Pod identity, placement or runtime was accepted")
		}
	}
}
