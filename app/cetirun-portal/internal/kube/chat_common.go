// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/internal/chataccess"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	commonExpectationKey       = "expectation.json"
	commonReconciledAnnotation = "sandbox.cetirun.microsoft.com/reconciled"
	commonRevisionAnnotation   = "sandbox.cetirun.microsoft.com/revision"
)

type commonChatIdentity struct {
	Name string    `json:"name"`
	UID  types.UID `json:"uid"`
}

type commonChatBinding struct {
	AgentUID       types.UID `json:"agentUID"`
	TeamUID        types.UID `json:"teamUID"`
	SandboxUID     types.UID `json:"sandboxUID"`
	AgentPolicyUID types.UID `json:"agentPolicyUID"`
}

type commonChatWorkload struct {
	Kind     string                 `json:"kind"`
	Identity commonChatIdentity     `json:"identity"`
	Replicas int32                  `json:"replicas"`
	Template corev1.PodTemplateSpec `json:"template"`
}

type commonChatSource struct {
	APIVersion      string    `json:"apiVersion"`
	Kind            string    `json:"kind"`
	Namespace       string    `json:"namespace,omitempty"`
	Name            string    `json:"name"`
	UID             types.UID `json:"uid"`
	ResourceVersion string    `json:"resourceVersion,omitempty"`
	Generation      int64     `json:"generation,omitempty"`
	IntentDigest    string    `json:"intentDigest,omitempty"`
}

type commonChatExpectation struct {
	Version         string                      `json:"version"`
	Agent           commonChatIdentity          `json:"agent"`
	AgentGeneration int64                       `json:"agentGeneration"`
	Namespace       commonChatIdentity          `json:"namespace"`
	Binding         commonChatBinding           `json:"binding"`
	Revision        string                      `json:"revision"`
	PlanHash        string                      `json:"planHash"`
	Workloads       []commonChatWorkload        `json:"workloads"`
	Sources         []commonChatSource          `json:"sources"`
	Controls        []unstructured.Unstructured `json:"controls"`
}

func commonExpectationName(namespace string, uid types.UID) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + string(uid)))
	return "sandbox-pod-" + hex.EncodeToString(sum[:20])
}

func commonChatCandidate(agent *api.Agent, team *api.Team) bool {
	if agent.Spec.SandboxRef == "" || agent.Status.Sandbox == nil || team == nil || !team.Spec.PolicyConfigured() ||
		slices.Contains(agent.Finalizers, "cetirun.microsoft.com/sandbox-hold") || slices.Contains(agent.Finalizers, "cetirun.microsoft.com/sandbox-preparation") {
		return false
	}
	condition := meta.FindStatusCondition(agent.Status.Conditions, api.ConditionSandboxReady)
	return condition != nil && condition.Status == metav1.ConditionTrue &&
		(condition.ObservedGeneration == agent.Generation || condition.ObservedGeneration == agent.ExecutionGeneration()) &&
		agent.Status.Sandbox.TeamUID != "" && agent.Status.Sandbox.SandboxUID != "" && agent.Status.Sandbox.AgentPolicyUID != "" && agent.Status.Sandbox.AppliedRevision != ""
}

func (c *Client) verifyCommonChatExpectation(ctx context.Context, agent *api.Agent, namespace *corev1.Namespace, sts *appsv1.StatefulSet, caller chataccess.Caller) (commonChatExpectation, bool, error) {
	var expected commonChatExpectation
	state := &corev1.ConfigMap{}
	if err := c.client.Get(ctx, client.ObjectKey{Namespace: c.sandboxStateNamespace, Name: commonExpectationName(namespace.Name, namespace.UID)}, state); err != nil {
		return expected, false, err
	}
	if state.UID == "" || !state.DeletionTimestamp.IsZero() || len(state.Data) != 1 || len(state.BinaryData) != 0 ||
		state.Annotations[chataccess.AgentUIDAnnotation] != string(agent.UID) || state.Annotations[commonRevisionAnnotation] == "" {
		return expected, false, nil
	}
	owner := metav1.GetControllerOf(state)
	if owner == nil || owner.APIVersion != api.GroupVersion.String() || owner.Kind != "Agent" || owner.Name != agent.Name || owner.UID != agent.UID {
		return expected, false, nil
	}
	raw := state.Data[commonExpectationKey]
	intentDigest, err := agent.ExecutionIntentDigest()
	if err != nil {
		return expected, false, err
	}
	if len(raw) == 0 || len(raw) > 512*1024 || json.Unmarshal([]byte(raw), &expected) != nil ||
		expected.Version != "v2" || expected.Agent != (commonChatIdentity{Name: agent.Name, UID: agent.UID}) ||
		expected.AgentGeneration != agent.ExecutionGeneration() || expected.Namespace != (commonChatIdentity{Name: namespace.Name, UID: namespace.UID}) ||
		expected.Revision != state.Annotations[commonRevisionAnnotation] || expected.Revision != agent.Status.Sandbox.AppliedRevision || expected.PlanHash == "" ||
		expected.Binding.AgentUID != agent.UID || expected.Binding.TeamUID != agent.Status.Sandbox.TeamUID || expected.Binding.SandboxUID != agent.Status.Sandbox.SandboxUID || expected.Binding.AgentPolicyUID != agent.Status.Sandbox.AgentPolicyUID {
		return expected, false, nil
	}
	workloadOK, agentSources := false, 0
	for _, source := range expected.Sources {
		if source.APIVersion != api.GroupVersion.String() || source.Kind != "Agent" {
			continue
		}
		if source.Namespace != "" || source.ResourceVersion != "" || source.Name != agent.Name || source.UID != agent.UID ||
			source.Generation != agent.ExecutionGeneration() || source.IntentDigest != intentDigest {
			return expected, false, nil
		}
		agentSources++
	}
	for _, workload := range expected.Workloads {
		if workload.Kind == "StatefulSet" && workload.Identity.Name == sts.Name && workload.Identity.UID == sts.UID && workload.Replicas == 1 &&
			workload.Template.Annotations[commonReconciledAnnotation] == "true" && workload.Template.Annotations[chataccess.AgentUIDAnnotation] == string(agent.UID) &&
			equality.Semantic.DeepEqual(workload.Template, sts.Spec.Template) {
			workloadOK = true
		}
	}
	if agentSources != 1 || !workloadOK || !commonChatCiliumAllows(expected.Controls, agent, namespace.Name, caller, *agent.Spec.Agent.Port) {
		return expected, false, nil
	}
	return expected, true, nil
}

func commonChatCiliumAllows(controls []unstructured.Unstructured, agent *api.Agent, namespace string, caller chataccess.Caller, port int32) bool {
	for _, control := range controls {
		if control.GetAPIVersion() != "cilium.io/v2" || control.GetKind() != "CiliumNetworkPolicy" || control.GetNamespace() != namespace ||
			control.GetAnnotations()[commonRevisionAnnotation] != agent.Status.Sandbox.AppliedRevision {
			continue
		}
		owner := metav1.GetControllerOf(&control)
		if owner == nil || owner.UID != agent.UID {
			continue
		}
		rules, found, _ := unstructured.NestedSlice(control.Object, "spec", "ingress")
		if !found {
			continue
		}
		for _, raw := range rules {
			rule, ok := raw.(map[string]any)
			if !ok || !commonChatRuleAllows(rule, caller, port) {
				continue
			}
			return true
		}
	}
	return false
}

func commonChatRuleAllows(rule map[string]any, caller chataccess.Caller, port int32) bool {
	endpoints, found, _ := unstructured.NestedSlice(rule, "fromEndpoints")
	if !found {
		return false
	}
	want := map[string]string{"k8s:io.kubernetes.pod.namespace": caller.Namespace}
	for key, value := range caller.PodLabels {
		want["k8s:"+key] = value
	}
	matched := false
	for _, raw := range endpoints {
		selector, ok := raw.(map[string]any)
		labels, ok := selector["matchLabels"].(map[string]any)
		if !ok || len(labels) != len(want) {
			continue
		}
		actual := map[string]string{}
		for key, value := range labels {
			text, ok := value.(string)
			if !ok {
				actual = nil
				break
			}
			actual[key] = text
		}
		if maps.Equal(actual, want) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	toPorts, found, _ := unstructured.NestedSlice(rule, "toPorts")
	for _, raw := range toPorts {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ports, ok := entry["ports"].([]any)
		if !ok {
			continue
		}
		for _, portValue := range ports {
			item, ok := portValue.(map[string]any)
			if !ok {
				continue
			}
			if item["port"] == fmt.Sprintf("%d", port) && item["protocol"] == "TCP" {
				return true
			}
		}
	}
	return false
}
