// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/chattest"
	"github.com/Azure/CetiRun/app/internal/chataccess"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestResolveCustomChat(t *testing.T) {
	agent, objects := chattest.Objects()
	c := NewClient(newFakeClient(objects...))
	value, err := c.ResolveChat(context.Background(), agent, chattest.Caller)
	if err != nil || !value.Available || value.Revision == "" || value.BaseURL == "" {
		t.Fatalf("target not resolved: %+v %v", value, err)
	}
	before := value.Revision
	agent.ResourceVersion = "unrelated-status-change"
	value, err = c.ResolveChat(context.Background(), agent, chattest.Caller)
	if err != nil || value.Revision != before {
		t.Fatal("metadata churn changed Chat binding")
	}
}

func TestResolveChatWithoutTeamStillHonorsAgentPolicy(t *testing.T) {
	agent, objects := chattest.Objects()
	agent.Spec.Team = ""
	objects = append(objects[:6], objects[7:]...)
	c := NewClient(newFakeClient(objects...))
	value, err := c.ResolveChat(context.Background(), agent, chattest.Caller)
	if err != nil || !value.Available {
		t.Fatalf("normal unassigned Agent cannot chat: %+v %v", value, err)
	}
	agent.Spec.SandboxRef = "held-sandbox"
	value, err = c.ResolveChat(context.Background(), agent, chattest.Caller)
	if err != nil || value.Available || value.Reason != "sandbox_held" {
		t.Fatalf("unassigned Agent escaped its Sandbox hold: %+v %v", value, err)
	}
}

func TestResolveChatRejectsStaleForeignAndUnsupportedTargets(t *testing.T) {
	cases := map[string]func(*api.Agent, []client.Object){
		"built-in":                 func(a *api.Agent, _ []client.Object) { a.Spec.Agent.Type = api.AgentTypeHermes },
		"headless":                 func(a *api.Agent, _ []client.Object) { a.Spec.Agent.Port = nil },
		"web UI":                   func(a *api.Agent, _ []client.Object) { a.Spec.Agent.WebUI = true },
		"suspended":                func(a *api.Agent, _ []client.Object) { a.Spec.Suspended = true },
		"desired port not applied": func(a *api.Agent, _ []client.Object) { a.Spec.Agent.Port = ptr.To[int32](9000); a.Generation++ },
		"namespace replaced": func(_ *api.Agent, o []client.Object) {
			o[1].(*corev1.Namespace).Annotations["cetirun.microsoft.com/agent-uid"] = "foreign"
		},
		"workload owner": func(_ *api.Agent, o []client.Object) { o[2].(*appsv1.StatefulSet).OwnerReferences[0].UID = "foreign" },
		"old pod": func(_ *api.Agent, o []client.Object) {
			o[3].(*corev1.Pod).Annotations["cetirun.microsoft.com/custom-plan-hash"] = "previous"
		},
		"external service": func(_ *api.Agent, o []client.Object) {
			o[4].(*corev1.Service).Spec.Type = corev1.ServiceTypeExternalName
			o[4].(*corev1.Service).Spec.ExternalName = "outside.example.com"
		},
		"foreign selector": func(_ *api.Agent, o []client.Object) {
			o[4].(*corev1.Service).Spec.Selector = map[string]string{"app": "foreign"}
		},
		"broad caller": func(_ *api.Agent, o []client.Object) {
			o[5].(*networkv1.NetworkPolicy).Spec.Ingress[0].From[0].PodSelector = nil
		},
		"policy-bound Team": func(_ *api.Agent, o []client.Object) { o[6].(*api.Team).Spec.AgentPolicyRef = "held-policy" },
		"missing peer":      func(_ *api.Agent, o []client.Object) { o[5].(*networkv1.NetworkPolicy).Spec.Ingress = nil },
		"retrying route": func(_ *api.Agent, o []client.Object) {
			o[7].(*unstructured.Unstructured).Object["spec"].(map[string]any)["http"].([]any)[0].(map[string]any)["retries"] = map[string]any{"attempts": int64(2)}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			agent, objects := chattest.Objects()
			mutate(agent, objects)
			c := NewClient(newFakeClient(objects...))
			value, err := c.ResolveChat(context.Background(), agent, chattest.Caller)
			if err == nil && value.Available {
				t.Fatalf("accepted %s", name)
			}
		})
	}
}

func TestChatRequiresPrivateRoute(t *testing.T) {
	agent, objects := chattest.Objects()
	c := NewClient(newFakeClient(objects[:7]...))
	value, err := c.ResolveChat(context.Background(), agent, chattest.Caller)
	if err == nil || value.Available {
		t.Fatal("missing route allowed chat forwarding")
	}
}

func TestResolveCommonCustomChatRequiresCurrentExpectationAndCiliumCallerRule(t *testing.T) {
	cases := map[string]func(*api.Agent, *commonChatExpectation){
		"current expectation": func(_ *api.Agent, _ *commonChatExpectation) {},
		"stale agent generation": func(agent *api.Agent, expected *commonChatExpectation) {
			expected.AgentGeneration = agent.Generation - 1
		},
		"foreign binding": func(_ *api.Agent, expected *commonChatExpectation) {
			expected.Binding.TeamUID = "foreign-team"
		},
		"wrong caller selector": func(_ *api.Agent, expected *commonChatExpectation) {
			labels := expected.Controls[0].Object["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)["fromEndpoints"].([]any)[0].(map[string]any)["matchLabels"].(map[string]any)
			labels["k8s:app"] = "foreign-portal"
		},
		"wrong port": func(_ *api.Agent, expected *commonChatExpectation) {
			ports := expected.Controls[0].Object["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)["toPorts"].([]any)[0].(map[string]any)["ports"].([]any)
			ports[0].(map[string]any)["port"] = "9000"
		},
		"workload template differs": func(_ *api.Agent, expected *commonChatExpectation) {
			annotations := maps.Clone(expected.Workloads[0].Template.Annotations)
			annotations[commonRevisionAnnotation] = "old-revision"
			expected.Workloads[0].Template.Annotations = annotations
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			agent, objects, state, expected := commonChatObjects(t)
			mutate(agent, &expected)
			writeCommonChatExpectation(t, state, expected)
			c := NewClient(newFakeClient(objects...))
			value, err := c.ResolveChat(context.Background(), agent, chattest.Caller)
			if name == "current expectation" {
				if err != nil || !value.Available {
					t.Fatalf("common target not resolved: %+v %v", value, err)
				}
				return
			}
			if err != nil || value.Available {
				t.Fatalf("accepted invalid common target: %+v %v", value, err)
			}
		})
	}
}

func commonChatObjects(t *testing.T) (*api.Agent, []client.Object, *corev1.ConfigMap, commonChatExpectation) {
	t.Helper()
	agent, objects := chattest.Objects()
	agent.Spec.SandboxRef = "sandbox-example"
	agent.Status.Sandbox = &api.SandboxStatus{
		TeamUID: "synthetic-team", SandboxUID: "synthetic-sandbox", AgentPolicyUID: "synthetic-policy", AppliedRevision: "current-revision",
	}
	agent.Status.Conditions = append(agent.Status.Conditions, metav1.Condition{
		Type: api.ConditionSandboxReady, Status: metav1.ConditionTrue, ObservedGeneration: agent.Generation, Reason: "SyntheticReady", LastTransitionTime: metav1.Now(),
	})
	team := objects[6].(*api.Team)
	team.Spec.AgentPolicyRef = "example-policy"
	team.Spec.SandboxRefs = []string{"sandbox-example"}
	team.Spec.DefaultSandboxRef = "sandbox-example"

	ns := objects[1].(*corev1.Namespace)
	sts := objects[2].(*appsv1.StatefulSet)
	sts.Annotations[commonReconciledAnnotation] = "true"
	sts.Spec.Template.Annotations = map[string]string{
		commonReconciledAnnotation: "true", commonRevisionAnnotation: agent.Status.Sandbox.AppliedRevision, chataccess.AgentUIDAnnotation: string(agent.UID),
	}
	pod := objects[3].(*corev1.Pod)
	pod.Annotations = map[string]string{
		commonReconciledAnnotation: "true", commonRevisionAnnotation: agent.Status.Sandbox.AppliedRevision, chataccess.AgentUIDAnnotation: string(agent.UID),
	}

	owner := *metav1.NewControllerRef(agent, api.GroupVersion.WithKind("Agent"))
	control := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy",
		"metadata": map[string]any{"name": "chat-network", "namespace": ns.Name, "annotations": map[string]any{commonRevisionAnnotation: agent.Status.Sandbox.AppliedRevision}, "ownerReferences": []any{map[string]any{"apiVersion": owner.APIVersion, "kind": owner.Kind, "name": owner.Name, "uid": string(owner.UID), "controller": true}}},
		"spec": map[string]any{"ingress": []any{map[string]any{
			"fromEndpoints": []any{map[string]any{"matchLabels": map[string]any{"k8s:io.kubernetes.pod.namespace": chattest.Caller.Namespace, "k8s:app": "test-portal"}}},
			"toPorts":       []any{map[string]any{"ports": []any{map[string]any{"port": "8080", "protocol": "TCP"}}}},
		}}},
	}}
	expected := commonChatExpectation{
		Version: "v2", Agent: commonChatIdentity{Name: agent.Name, UID: agent.UID}, AgentGeneration: agent.Generation,
		Namespace: commonChatIdentity{Name: ns.Name, UID: ns.UID},
		Binding:   commonChatBinding{AgentUID: agent.UID, TeamUID: agent.Status.Sandbox.TeamUID, SandboxUID: agent.Status.Sandbox.SandboxUID, AgentPolicyUID: agent.Status.Sandbox.AgentPolicyUID},
		Revision:  agent.Status.Sandbox.AppliedRevision, PlanHash: "synthetic-plan",
		Workloads: []commonChatWorkload{{Kind: "StatefulSet", Identity: commonChatIdentity{Name: sts.Name, UID: sts.UID}, Replicas: 1, Template: sts.Spec.Template}},
		Sources:   []commonChatSource{{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: agent.Name, UID: agent.UID, Generation: agent.Generation}},
		Controls:  []unstructured.Unstructured{control},
	}
	state := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: commonExpectationName(ns.Name, ns.UID), Namespace: "cetirun-system", UID: "synthetic-expectation",
		Annotations: map[string]string{chataccess.AgentUIDAnnotation: string(agent.UID), commonRevisionAnnotation: agent.Status.Sandbox.AppliedRevision}, OwnerReferences: []metav1.OwnerReference{owner},
	}}
	writeCommonChatExpectation(t, state, expected)
	objects = append(objects, state)
	return agent, objects, state, expected
}

func writeCommonChatExpectation(t *testing.T, state *corev1.ConfigMap, expected commonChatExpectation) {
	t.Helper()
	raw, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	state.Data = map[string]string{commonExpectationKey: string(raw)}
}
