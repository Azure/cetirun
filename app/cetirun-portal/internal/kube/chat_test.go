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
	"github.com/Azure/CetiRun/app/internal/podtemplate"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
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
		"missing intent digest": func(_ *api.Agent, expected *commonChatExpectation) {
			expected.Sources[0].IntentDigest = ""
		},
		"namespaced Agent source": func(_ *api.Agent, expected *commonChatExpectation) {
			expected.Sources[0].Namespace = "unexpected"
		},
		"duplicate Agent source": func(_ *api.Agent, expected *commonChatExpectation) {
			expected.Sources = append(expected.Sources, expected.Sources[0])
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
			annotations[commonWorkloadRevisionAnnotation] = "old-revision"
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

func TestCommonChatTemplateAllowsOnlyAPIDefaults(t *testing.T) {
	cases := map[string]func(*corev1.PodTemplateSpec){
		"API defaults only": func(_ *corev1.PodTemplateSpec) {},
		"image": func(template *corev1.PodTemplateSpec) {
			template.Spec.Containers[0].Image = "registry.example.com/changed@sha256:example"
		},
		"environment": func(template *corev1.PodTemplateSpec) {
			template.Spec.Containers[0].Env = append(template.Spec.Containers[0].Env, corev1.EnvVar{Name: "CHANGED", Value: "true"})
		},
		"command": func(template *corev1.PodTemplateSpec) { template.Spec.Containers[0].Command = []string{"changed"} },
		"security context": func(template *corev1.PodTemplateSpec) {
			template.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: ptr.To(true)}
		},
		"resources": func(template *corev1.PodTemplateSpec) {
			template.Spec.Containers[0].Resources.Requests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")}
		},
		"volume": func(template *corev1.PodTemplateSpec) {
			template.Spec.Volumes = append(template.Spec.Volumes, corev1.Volume{Name: "changed", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
		},
		"probe": func(template *corev1.PodTemplateSpec) {
			template.Spec.Containers[0].LivenessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/changed", Port: intstr.FromInt(8080)}}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			agent, objects, state, expected := commonChatObjects(t)
			sts := objects[2].(*appsv1.StatefulSet)
			sts.Spec.Template.Spec = *objects[3].(*corev1.Pod).Spec.DeepCopy()
			expected.Workloads[0].Template = sts.Spec.Template
			if err := podtemplate.DefaultSpec(&sts.Spec.Template.Spec); err != nil {
				t.Fatal(err)
			}
			mutate(&sts.Spec.Template)
			writeCommonChatExpectation(t, state, expected)
			value, err := NewClient(newFakeClient(objects...)).ResolveChat(context.Background(), agent, chattest.Caller)
			if name == "API defaults only" {
				if err != nil || !value.Available {
					t.Fatalf("API-defaulted template was rejected: %+v %v", value, err)
				}
				return
			}
			if err != nil || value.Available {
				t.Fatalf("accepted changed template %s: %+v %v", name, value, err)
			}
		})
	}
}

func TestCommonChatRejectsRevisionKeyConfusion(t *testing.T) {
	cases := map[string]func(*corev1.ConfigMap, *corev1.Pod, *commonChatExpectation){
		"expectation uses policy key": func(state *corev1.ConfigMap, _ *corev1.Pod, _ *commonChatExpectation) {
			state.Annotations[commonPolicyRevisionAnnotation] = state.Annotations[commonWorkloadRevisionAnnotation]
			delete(state.Annotations, commonWorkloadRevisionAnnotation)
		},
		"Pod uses policy key": func(_ *corev1.ConfigMap, pod *corev1.Pod, _ *commonChatExpectation) {
			pod.Annotations[commonPolicyRevisionAnnotation] = pod.Annotations[commonWorkloadRevisionAnnotation]
			delete(pod.Annotations, commonWorkloadRevisionAnnotation)
		},
		"Cilium policy uses workload key": func(_ *corev1.ConfigMap, _ *corev1.Pod, expected *commonChatExpectation) {
			annotations := expected.Controls[0].GetAnnotations()
			annotations[commonWorkloadRevisionAnnotation] = annotations[commonPolicyRevisionAnnotation]
			delete(annotations, commonPolicyRevisionAnnotation)
			expected.Controls[0].SetAnnotations(annotations)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			agent, objects, state, expected := commonChatObjects(t)
			mutate(state, objects[3].(*corev1.Pod), &expected)
			writeCommonChatExpectation(t, state, expected)
			value, err := NewClient(newFakeClient(objects...)).ResolveChat(context.Background(), agent, chattest.Caller)
			if err != nil || value.Available {
				t.Fatalf("accepted confused revision key %s: %+v %v", name, value, err)
			}
		})
	}
}

func TestCommonChatRetainsRequestOnlyIntentAndRejectsChangedExecution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*api.Agent)
		ready  bool
	}{
		{name: "failed preflight request only", mutate: func(*api.Agent) {}, ready: true},
		{name: "observed failed preflight", mutate: func(a *api.Agent) { a.Status.ObservedGeneration = a.Generation }, ready: true},
		{name: "new request after rejected attempt", mutate: func(a *api.Agent) {
			a.Status.ObservedGeneration = a.Generation
			a.Generation++
			a.Spec.TeamChangeRequest = a.Spec.TeamChangeRequest.DeepCopy()
			a.Spec.TeamChangeRequest.RequestID = "synthetic-second-request"
		}, ready: true},
		{name: "model changed", mutate: func(a *api.Agent) { a.Spec.Model = "different-model" }},
		{name: "chart upgrade", mutate: func(a *api.Agent) { a.Spec.Helm.Version = "0.1.1-synthetic" }},
		{name: "runtime upgrade", mutate: func(a *api.Agent) { a.Spec.Agent.Image = ptr.To("registry.example.com/custom@sha256:changed") }},
		{name: "Team changed", mutate: func(a *api.Agent) { a.Spec.Team = "changed-team" }},
		{name: "paused", mutate: func(a *api.Agent) { a.Spec.Suspended = true }},
		{name: "authorization changed", mutate: func(a *api.Agent) {
			a.Annotations = maps.Clone(a.Annotations)
			if a.Annotations == nil {
				a.Annotations = map[string]string{}
			}
			a.Annotations["cetirun.microsoft.com/sandbox-authorization"] = "changed"
		}},
		{name: "reauthorization requested", mutate: func(a *api.Agent) {
			a.Annotations = maps.Clone(a.Annotations)
			if a.Annotations == nil {
				a.Annotations = map[string]string{}
			}
			a.Annotations["cetirun.microsoft.com/sandbox-reauthorize"] = "requested"
		}},
		{name: "held", mutate: func(a *api.Agent) { a.Finalizers = append(a.Finalizers, "cetirun.microsoft.com/sandbox-hold") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent, objects, state, expected := commonChatObjects(t)
			generation := agent.Generation
			agent.Spec.TeamChangeRequest = &api.TeamChangeRequest{
				RequestID: "synthetic-failed-request", ExpectedUID: agent.UID, ExpectedResourceVersion: "1", TargetTeam: "incompatible-team",
			}
			agent.Status.TeamChange = &api.TeamChangeStatus{
				TeamChangeRequest: *agent.Spec.TeamChangeRequest, Phase: api.TeamChangeFailed,
				SourceGeneration: generation, ObservedGeneration: generation + 1, FailureReason: "PreflightFailed",
			}
			agent.Generation++
			tc.mutate(agent)
			if agent.ExecutionGeneration() != generation {
				t.Fatal("fixture must keep the original execution generation")
			}
			writeCommonChatExpectation(t, state, expected)
			c := NewClient(newFakeClient(objects...))
			// Check the common proof directly so changed Team lookup or outer
			// lifecycle gates cannot conceal a stale-source acceptance.
			_, verified, err := c.verifyCommonChatExpectation(context.Background(), agent, objects[1].(*corev1.Namespace), objects[2].(*appsv1.StatefulSet), chattest.Caller)
			if err != nil || verified != tc.ready {
				t.Fatalf("current execution proof = %v, want %v: %v", verified, tc.ready, err)
			}
			if tc.ready {
				target, err := c.ResolveChat(context.Background(), agent, chattest.Caller)
				if err != nil || !target.Available {
					t.Fatalf("request-only failure interrupted Chat: %+v %v", target, err)
				}
			}
		})
	}
}

func TestCommonChatResumeRequiresCurrentExecutionProof(t *testing.T) {
	agent, objects, state, expected := commonChatObjects(t)
	agent.Spec.TeamChangeRequest = &api.TeamChangeRequest{RequestID: "synthetic-paused-request"}
	agent.Status.TeamChange = &api.TeamChangeStatus{
		TeamChangeRequest: *agent.Spec.TeamChangeRequest, Phase: api.TeamChangeFailed,
		SourceGeneration: agent.Generation, FailureReason: "Paused",
	}
	agent.Spec.Suspended = true
	agent.Finalizers = append(agent.Finalizers, api.SandboxHoldFinalizer)
	pausedDigest, err := agent.ExecutionIntentDigest()
	if err != nil {
		t.Fatal(err)
	}
	expected.Sources[0].IntentDigest = pausedDigest
	// Simulate completed resume reconciliation, but retain the paused proof
	// first. A matching execution generation alone cannot authorize Chat.
	agent.Spec.Suspended = false
	agent.Finalizers = nil
	agent.Generation += 2
	agent.Status.ObservedGeneration = agent.Generation
	writeCommonChatExpectation(t, state, expected)
	c := NewClient(newFakeClient(objects...))
	target, err := c.ResolveChat(context.Background(), agent, chattest.Caller)
	if err != nil || target.Available {
		t.Fatalf("resumed Agent accepted paused proof: %+v %v", target, err)
	}
	expected.Sources[0].IntentDigest, err = agent.ExecutionIntentDigest()
	if err != nil {
		t.Fatal(err)
	}
	writeCommonChatExpectation(t, state, expected)
	c = NewClient(newFakeClient(objects...))
	target, err = c.ResolveChat(context.Background(), agent, chattest.Caller)
	if err != nil || !target.Available {
		t.Fatalf("fresh resumed execution proof rejected: %+v %v", target, err)
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
		commonReconciledAnnotation: "true", commonWorkloadRevisionAnnotation: agent.Status.Sandbox.AppliedRevision, chataccess.AgentUIDAnnotation: string(agent.UID),
	}
	pod := objects[3].(*corev1.Pod)
	pod.Annotations = map[string]string{
		commonReconciledAnnotation: "true", commonWorkloadRevisionAnnotation: agent.Status.Sandbox.AppliedRevision, chataccess.AgentUIDAnnotation: string(agent.UID),
	}

	owner := *metav1.NewControllerRef(agent, api.GroupVersion.WithKind("Agent"))
	control := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy",
		"metadata": map[string]any{"name": "chat-network", "namespace": ns.Name, "annotations": map[string]any{commonPolicyRevisionAnnotation: agent.Status.Sandbox.AppliedRevision}, "ownerReferences": []any{map[string]any{"apiVersion": owner.APIVersion, "kind": owner.Kind, "name": owner.Name, "uid": string(owner.UID), "controller": true}}},
		"spec": map[string]any{"ingress": []any{map[string]any{
			"fromEndpoints": []any{map[string]any{"matchLabels": map[string]any{"k8s:io.kubernetes.pod.namespace": chattest.Caller.Namespace, "k8s:app": "test-portal"}}},
			"toPorts":       []any{map[string]any{"ports": []any{map[string]any{"port": "8080", "protocol": "TCP"}}}},
		}}},
	}}
	intentDigest, err := agent.ExecutionIntentDigest()
	if err != nil {
		t.Fatal(err)
	}
	expected := commonChatExpectation{
		Version: "v2", Agent: commonChatIdentity{Name: agent.Name, UID: agent.UID}, AgentGeneration: agent.Generation,
		Namespace: commonChatIdentity{Name: ns.Name, UID: ns.UID},
		Binding:   commonChatBinding{AgentUID: agent.UID, TeamUID: agent.Status.Sandbox.TeamUID, SandboxUID: agent.Status.Sandbox.SandboxUID, AgentPolicyUID: agent.Status.Sandbox.AgentPolicyUID},
		Revision:  agent.Status.Sandbox.AppliedRevision, PlanHash: "synthetic-plan",
		Workloads: []commonChatWorkload{{Kind: "StatefulSet", Identity: commonChatIdentity{Name: sts.Name, UID: sts.UID}, Replicas: 1, Template: sts.Spec.Template}},
		Sources:   []commonChatSource{{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: agent.Name, UID: agent.UID, Generation: agent.Generation, IntentDigest: intentDigest}},
		Controls:  []unstructured.Unstructured{control},
	}
	state := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: commonExpectationName(ns.Name, ns.UID), Namespace: "cetirun-system", UID: "synthetic-expectation",
		Annotations: map[string]string{chataccess.AgentUIDAnnotation: string(agent.UID), commonWorkloadRevisionAnnotation: agent.Status.Sandbox.AppliedRevision}, OwnerReferences: []metav1.OwnerReference{owner},
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
