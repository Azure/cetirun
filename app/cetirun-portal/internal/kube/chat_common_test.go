// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/chatproxy"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/chattest"
	"github.com/Azure/CetiRun/app/internal/chataccess"
	"github.com/Azure/CetiRun/app/internal/podtemplate"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func commonChatObjects(t *testing.T) (*api.Agent, []client.Object, *corev1.ConfigMap, commonChatExpectation) {
	t.Helper()
	agent, objects := chattest.Objects()
	agent.Spec.SandboxRef = "sandbox-example"
	agent.Status.Sandbox = &api.SandboxStatus{
		TeamUID: "synthetic-team", SandboxUID: "synthetic-sandbox", AgentPolicyUID: "synthetic-policy", AppliedRevision: "current-revision",
	}
	for _, kind := range []string{api.ConditionSandboxReady, api.ConditionPolicyApplied} {
		agent.Status.Conditions = append(agent.Status.Conditions, metav1.Condition{
			Type: kind, Status: metav1.ConditionTrue, ObservedGeneration: agent.Generation, Reason: "SyntheticReady", LastTransitionTime: metav1.Now(),
		})
	}
	team := objects[6].(*api.Team)
	team.Spec.AgentPolicyRef = "example-policy"
	team.Spec.SandboxRefs, team.Spec.DefaultSandboxRef = []string{"sandbox-example"}, "sandbox-example"
	owner := *metav1.NewControllerRef(agent, api.GroupVersion.WithKind("Agent"))
	ns := objects[1].(*corev1.Namespace)
	ns.OwnerReferences = []metav1.OwnerReference{owner}
	objects[4].(*corev1.Service).OwnerReferences = []metav1.OwnerReference{owner}
	sts := objects[2].(*appsv1.StatefulSet)
	sts.Spec.Template.Annotations = map[string]string{
		commonReconciledAnnotation: "true", commonWorkloadRevisionAnnotation: agent.Status.Sandbox.AppliedRevision,
		chataccess.AgentUIDAnnotation: string(agent.UID),
	}
	pod := objects[3].(*corev1.Pod)
	sts.Spec.Template.Spec = *pod.Spec.DeepCopy()
	pod.Annotations = maps.Clone(sts.Spec.Template.Annotations)
	control := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy",
		"spec": map[string]any{
			"endpointSelector": map[string]any{"matchLabels": map[string]any{
				"k8s:sandbox.cetirun.microsoft.com/agent-uid": string(agent.UID), "k8s:io.kubernetes.pod.namespace": ns.Name,
			}},
			"ingress": []any{map[string]any{
				"fromEndpoints": []any{map[string]any{"matchLabels": map[string]any{
					"k8s:io.kubernetes.pod.namespace": chattest.Caller.Namespace, "k8s:app": "test-portal",
				}}},
				"toPorts": []any{map[string]any{"ports": []any{map[string]any{"port": "8080", "protocol": "TCP"}}}},
			}},
		},
	}}
	control.SetName("chat-network")
	control.SetNamespace(ns.Name)
	control.SetUID("synthetic-policy-object")
	control.SetOwnerReferences([]metav1.OwnerReference{owner})
	control.SetAnnotations(map[string]string{commonPolicyRevisionAnnotation: agent.Status.Sandbox.AppliedRevision})
	digest, err := agent.SandboxExecutionIntentDigest()
	if err != nil {
		t.Fatal(err)
	}
	expected := commonChatExpectation{
		Version: "v2", Agent: commonChatIdentity{Name: agent.Name, UID: agent.UID}, AgentGeneration: agent.Generation,
		Namespace: commonChatIdentity{Name: ns.Name, UID: ns.UID},
		Binding: commonChatBinding{AgentUID: agent.UID, TeamUID: agent.Status.Sandbox.TeamUID,
			SandboxUID: agent.Status.Sandbox.SandboxUID, AgentPolicyUID: agent.Status.Sandbox.AgentPolicyUID},
		Revision: agent.Status.Sandbox.AppliedRevision, PlanHash: "synthetic-plan",
		Workloads: []commonChatWorkload{{Kind: "StatefulSet", Identity: commonChatIdentity{Name: sts.Name, UID: sts.UID},
			Replicas: 1, Template: *sts.Spec.Template.DeepCopy()}},
		Sources: []commonChatSource{{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: agent.Name,
			UID: agent.UID, Generation: agent.Generation, IntentDigest: digest}},
		Controls: []unstructured.Unstructured{*control.DeepCopy()},
	}
	state := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: commonExpectationName(ns.Name, ns.UID), Namespace: "cetirun-system", UID: "synthetic-expectation",
		Annotations: map[string]string{chataccess.AgentUIDAnnotation: string(agent.UID),
			commonWorkloadRevisionAnnotation: agent.Status.Sandbox.AppliedRevision},
		OwnerReferences: []metav1.OwnerReference{owner},
	}}
	writeCommonChatExpectation(t, state, expected)
	return agent, append(objects, state, &control), state, expected
}

func writeCommonChatExpectation(t *testing.T, state *corev1.ConfigMap, expected commonChatExpectation) {
	t.Helper()
	raw, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	state.Data = map[string]string{commonExpectationKey: string(raw)}
}

func TestCommonChatRequiresCurrentOwnedExecution(t *testing.T) {
	for name, mutate := range map[string]func(*api.Agent, []client.Object, *commonChatExpectation){
		"current":         func(*api.Agent, []client.Object, *commonChatExpectation) {},
		"old generation":  func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) { e.AgentGeneration-- },
		"foreign binding": func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) { e.Binding.TeamUID = "other" },
		"missing intent":  func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) { e.Sources[0].IntentDigest = "" },
		"namespaced Agent": func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) {
			e.Sources[0].Namespace = "foreign"
		},
		"duplicate Agent": func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) {
			e.Sources = append(e.Sources, e.Sources[0])
		},
		"reauthorization changed": func(a *api.Agent, _ []client.Object, _ *commonChatExpectation) {
			a.Annotations = map[string]string{api.AnnotationSandboxReauthorize: "changed"}
		},
		"foreign namespace": func(_ *api.Agent, o []client.Object, _ *commonChatExpectation) {
			o[1].SetOwnerReferences(nil)
		},
		"replaced Team":   func(_ *api.Agent, o []client.Object, _ *commonChatExpectation) { o[6].SetUID("other") },
		"foreign service": func(_ *api.Agent, o []client.Object, _ *commonChatExpectation) { o[4].SetOwnerReferences(nil) },
		"unready policy": func(a *api.Agent, _ []client.Object, _ *commonChatExpectation) {
			for i := range a.Status.Conditions {
				if a.Status.Conditions[i].Type == api.ConditionPolicyApplied {
					a.Status.Conditions[i].Status = metav1.ConditionFalse
				}
			}
		},
		"live policy drift": func(_ *api.Agent, o []client.Object, _ *commonChatExpectation) {
			o[len(o)-1].(*unstructured.Unstructured).Object["spec"].(map[string]any)["ingress"] = []any{}
		},
		"wrong workload revision": func(_ *api.Agent, o []client.Object, _ *commonChatExpectation) {
			o[3].(*corev1.Pod).Annotations[commonWorkloadRevisionAnnotation] = "old-revision"
		},
		"wrong caller": func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) {
			rule := e.Controls[0].Object["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)
			rule["fromEndpoints"] = []any{map[string]any{"matchLabels": map[string]any{"k8s:io.kubernetes.pod.namespace": "foreign"}}}
		},
		"wrong port": func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) {
			rule := e.Controls[0].Object["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)
			rule["toPorts"] = []any{map[string]any{"ports": []any{map[string]any{"port": "9000", "protocol": "TCP"}}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			agent, objects, state, expected := commonChatObjects(t)
			mutate(agent, objects, &expected)
			writeCommonChatExpectation(t, state, expected)
			target, err := NewClient(newFakeClient(objects...)).ResolveChat(context.Background(), agent, chattest.Caller)
			if err != nil || target.Available != (name == "current") {
				t.Fatalf("available=%v reason=%s err=%v", target.Available, target.Reason, err)
			}
		})
	}
}

func TestCommonChatMTLSCompilerOutput(t *testing.T) {
	agent, objects := chattest.MTLSObjects(t)
	c := NewClient(newFakeClient(objects...))
	for _, resolve := range []func(context.Context, *api.Agent, chataccess.Caller) (chatproxy.Target, error){c.ResolveChat, c.ObserveChat} {
		target, err := resolve(context.Background(), agent, chattest.Caller)
		if err != nil || !target.Available || target.BaseURL != "http://openclaw-chat-example-cetirun.cetirun-chat-example.svc.cluster.local:8080" {
			t.Fatalf("compiler-approved mTLS Chat unavailable: %+v %v", target, err)
		}
	}
}

func TestCommonChatMTLSRejectsChangedEndpoint(t *testing.T) {
	for name, mutate := range map[string]func(*api.Agent, []client.Object, *commonChatExpectation){
		"missing TLS approval": func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) { e.CustomIngressTLS = nil },
		"foreign Agent":        func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) { e.CustomIngressTLS.AgentUID = "other" },
		"foreign namespace": func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) {
			e.CustomIngressTLS.Namespace = "other"
		},
		"wrong harness": func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) { e.CustomIngressTLS.Harness = "hermes" },
		"wrong backend": func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) { e.CustomIngressTLS.Port = 9000 },
		"wrong Service": func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) {
			e.CustomIngressTLS.ServiceHost = "other.example.com"
		},
		"wrong serving name": func(_ *api.Agent, _ []client.Object, e *commonChatExpectation) {
			e.CustomIngressTLS.ServerName = "other.example.com"
		},
		"plaintext Service": func(_ *api.Agent, objects []client.Object, _ *commonChatExpectation) {
			objects[4].(*corev1.Service).Spec.Ports[0].TargetPort = intstr.FromInt32(8080)
		},
		"named plaintext Service": func(_ *api.Agent, objects []client.Object, _ *commonChatExpectation) {
			objects[4].(*corev1.Service).Spec.Ports[0].TargetPort = intstr.FromString("http")
		},
		"unapproved TLS port": func(_ *api.Agent, objects []client.Object, _ *commonChatExpectation) {
			objects[4].(*corev1.Service).Spec.Ports[0].TargetPort = intstr.FromInt32(15444)
		},
		"plaintext caller rule": func(_ *api.Agent, objects []client.Object, e *commonChatExpectation) {
			for index := range e.Controls {
				control := &e.Controls[index]
				if control.GetKind() != "CiliumNetworkPolicy" {
					continue
				}
				rules, _, err := unstructured.NestedSlice(control.Object, "spec", "ingress")
				if err != nil {
					t.Fatal(err)
				}
				for _, raw := range rules {
					rule, ok := raw.(map[string]any)
					if ok && commonChatRuleAllows(rule, chattest.Caller, chataccess.MTLSAPIPort) {
						rule["toPorts"] = []any{map[string]any{"ports": []any{map[string]any{"port": "8080", "protocol": "TCP"}}}}
					}
				}
				if err := unstructured.SetNestedSlice(control.Object, rules, "spec", "ingress"); err != nil {
					t.Fatal(err)
				}
				for _, object := range objects {
					actual, ok := object.(*unstructured.Unstructured)
					if ok && actual.GetKind() == control.GetKind() && actual.GetName() == control.GetName() {
						actual.Object["spec"] = control.DeepCopy().Object["spec"]
					}
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			agent, objects := chattest.MTLSObjects(t)
			var state *corev1.ConfigMap
			for _, object := range objects {
				if config, ok := object.(*corev1.ConfigMap); ok && config.Data[commonExpectationKey] != "" {
					state = config
				}
			}
			if state == nil {
				t.Fatal("compiler fixture has no protected expectation")
			}
			var expected commonChatExpectation
			if err := json.Unmarshal([]byte(state.Data[commonExpectationKey]), &expected); err != nil {
				t.Fatal(err)
			}
			mutate(agent, objects, &expected)
			writeCommonChatExpectation(t, state, expected)
			c := NewClient(newFakeClient(objects...))
			for _, resolve := range []func(context.Context, *api.Agent, chataccess.Caller) (chatproxy.Target, error){c.ResolveChat, c.ObserveChat} {
				target, err := resolve(context.Background(), agent, chattest.Caller)
				if err != nil || target.Available {
					t.Fatalf("changed mTLS endpoint accepted: %+v %v", target, err)
				}
			}
		})
	}
}

func TestCommonChatTemplateAllowsOnlyAPIDefaults(t *testing.T) {
	for name, mutate := range map[string]func(*corev1.PodTemplateSpec){
		"defaults": func(*corev1.PodTemplateSpec) {},
		"image":    func(p *corev1.PodTemplateSpec) { p.Spec.Containers[0].Image = "registry.example.com/changed:1" },
		"command":  func(p *corev1.PodTemplateSpec) { p.Spec.Containers[0].Command = []string{"changed"} },
		"privilege": func(p *corev1.PodTemplateSpec) {
			p.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: ptr.To(true)}
		},
		"environment": func(p *corev1.PodTemplateSpec) {
			p.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "CHANGED", Value: "true"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			agent, objects, _, _ := commonChatObjects(t)
			template := &objects[2].(*appsv1.StatefulSet).Spec.Template
			if err := podtemplate.DefaultSpec(&template.Spec); err != nil {
				t.Fatal(err)
			}
			mutate(template)
			target, err := NewClient(newFakeClient(objects...)).ResolveChat(context.Background(), agent, chattest.Caller)
			if err != nil || target.Available != (name == "defaults") {
				t.Fatalf("available=%v err=%v", target.Available, err)
			}
		})
	}
}
