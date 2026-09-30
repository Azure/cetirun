// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	strictjson "sigs.k8s.io/json"
)

const (
	PodValidationPath = "/validate-sandbox-pods"
	PodExpectationKey = "expectation.json"
)

type PodConfig struct {
	// Only the controller may write expectation ConfigMaps in this namespace.
	// It must not be an Agent workload namespace. Admission matching must
	// independently protect that boundary and exclude its own repair resources.
	StateNamespace string
}

type PodHandler struct {
	reader client.Reader
	config PodConfig
}

func (h *PodHandler) Handle(ctx context.Context, request admission.Request) admission.Response {
	return h.Validate(ctx, request)
}

// NewPodHandler requires mgr.GetAPIReader (or another uncached API reader).
// An informer, in-memory expectation or caller-supplied label is not authority.
func NewPodHandler(reader client.Reader, config PodConfig) (*PodHandler, error) {
	if reader == nil || config.StateNamespace == "" || len(validation.IsDNS1123Label(config.StateNamespace)) != 0 {
		return nil, fmt.Errorf("Pod admission requires an uncached reader and protected controller state namespace")
	}
	return &PodHandler{reader: reader, config: config}, nil
}

func PodExpectationName(namespace string, uid types.UID) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + string(uid)))
	return "sandbox-pod-" + hex.EncodeToString(sum[:20])
}

// PodExpectationObject produces controller state, not an execution permit.
// The Bundle's Expect method always starts held. Persisting this object requires
// separate controller authorization and must never be exposed as a tenant API.
func PodExpectationObject(stateNamespace string, expected sandboxworkload.Expectation) (*corev1.ConfigMap, error) {
	if stateNamespace == "" || stateNamespace == expected.Namespace.Name ||
		expected.Version != sandboxworkload.ContractVersion || expected.Agent.Name == "" || expected.Agent.UID == "" ||
		expected.Namespace.Name == "" || expected.Namespace.UID == "" {
		return nil, fmt.Errorf("invalid controller-owned Pod expectation")
	}
	raw, err := json.Marshal(expected)
	if err != nil {
		return nil, err
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: stateNamespace, Name: PodExpectationName(expected.Namespace.Name, expected.Namespace.UID),
			OwnerReferences: []metav1.OwnerReference{sandboxworkload.AgentOwner(expected.Agent.Name, expected.Agent.UID)},
			Annotations: map[string]string{
				sandboxworkload.RevisionAnnotation: expected.Revision,
				sandboxworkload.AgentUIDAnnotation: string(expected.Agent.UID),
			},
		},
		Data: map[string]string{PodExpectationKey: string(raw)},
	}, nil
}

// Validate has no namespace-wide or platform-helper exemption. The API server
// must send only protected namespaces here; matching is a separate repair-safe
// control. Missing/stale controller state denies all protected writes.
func (h *PodHandler) Validate(ctx context.Context, request admission.Request) admission.Response {
	if request.Operation == admissionv1.Delete || request.SubResource == "status" {
		return admission.Allowed("Pod deletion and status remain independent of governance admission")
	}
	if request.Resource.Group != "" || request.Resource.Resource != "pods" || request.Resource.Version != "v1" ||
		(request.Operation != admissionv1.Create && request.Operation != admissionv1.Update) ||
		(request.SubResource != "" && request.SubResource != "ephemeralcontainers" && request.SubResource != "resize") {
		return admission.Denied("unsupported protected Pod request")
	}
	var pod corev1.Pod
	if err := decodePod(request.Object.Raw, &pod); err != nil || pod.Namespace != request.Namespace || request.Namespace == "" ||
		(request.Name != "" && request.Name != pod.Name) {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("invalid or ambiguous protected Pod"))
	}
	expected, state, err := h.readExpectation(ctx, request.Namespace)
	if err != nil {
		return admission.Errored(http.StatusServiceUnavailable, fmt.Errorf("authoritative protected Pod expectations are unavailable"))
	}
	agent, binding, err := sandboxbinding.Read(ctx, h.reader, client.ObjectKey{Name: expected.Agent.Name})
	if err != nil || agent.UID != expected.Agent.UID || agent.Generation != expected.AgentGeneration ||
		binding != expected.Binding || expected.Binding.AgentUID != expected.Agent.UID {
		return admission.Denied("Pod binding is missing, stale or no longer authorized")
	}
	if expected.ExecutionHeld || agent.Spec.Suspended || slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) {
		return admission.Denied(sandboxworkload.ErrExecutionHeld.Error())
	}
	if err := h.validatePodSources(ctx, expected, agent); err != nil {
		return admission.Denied("Pod source identities or revisions are missing or stale")
	}
	workload := &appsv1.StatefulSet{}
	if err := h.reader.Get(ctx, client.ObjectKey{Namespace: expected.Namespace.Name, Name: expected.Workload.Name}, workload); err != nil {
		return admission.Errored(http.StatusServiceUnavailable, fmt.Errorf("authoritative workload is unavailable"))
	}
	owner := sandboxworkload.AgentOwner(agent.Name, agent.UID)
	if workload.UID != expected.Workload.UID || !workload.DeletionTimestamp.IsZero() ||
		!equality.Semantic.DeepEqual(workload.OwnerReferences, []metav1.OwnerReference{owner}) ||
		workload.Annotations[sandboxworkload.RevisionAnnotation] != expected.Revision ||
		workload.Annotations[sandboxworkload.AgentUIDAnnotation] != string(agent.UID) ||
		ptr.Deref(workload.Spec.Replicas, 0) != 1 || workload.Spec.ServiceName != workload.Name ||
		len(workload.Spec.VolumeClaimTemplates) != 0 ||
		!sandboxworkload.SamePodTemplate(workload.Spec.Template, expected.Template) {
		return admission.Denied("current workload ownership, lifecycle intent or template differs from the expectation")
	}
	if expected.ControllerRevision != workload.Status.UpdateRevision || expected.ControllerRevision == "" {
		return admission.Denied("StatefulSet controller revision has not been observed")
	}
	if request.Operation == admissionv1.Update {
		var old corev1.Pod
		if err := decodePod(request.OldObject.Raw, &old); err != nil {
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("invalid previous Pod"))
		}
		current := &corev1.Pod{}
		if err := h.reader.Get(ctx, client.ObjectKeyFromObject(&pod), current); err != nil {
			return admission.Errored(http.StatusServiceUnavailable, fmt.Errorf("authoritative previous Pod is unavailable"))
		}
		if pod.UID == "" || current.UID != pod.UID || old.UID != current.UID || old.ResourceVersion != current.ResourceVersion ||
			pod.ResourceVersion != current.ResourceVersion || old.Name != pod.Name || old.Namespace != pod.Namespace ||
			old.Spec.NodeName != current.Spec.NodeName {
			return admission.Denied("Pod update does not match the authoritative previous UID and revision")
		}
		// Scheduling is only grandfathered from the persisted Pod, not supplied
		// by a new Pod or inferred from its caller, labels or ServiceAccount.
		expected.Template.Spec.NodeName = current.Spec.NodeName
	}
	if err := sandboxworkload.ValidatePod(&pod, expected); err != nil {
		return admission.Denied(err.Error())
	}
	currentState := &corev1.ConfigMap{}
	if err := h.reader.Get(ctx, client.ObjectKeyFromObject(state), currentState); err != nil ||
		currentState.UID != state.UID || currentState.ResourceVersion != state.ResourceVersion {
		return admission.Denied("Pod expectation changed during validation")
	}
	return admission.Allowed("final Pod matches current controller-owned expectations")
}

func decodePod(raw []byte, target *corev1.Pod) error {
	problems, err := strictjson.UnmarshalStrict(raw, target)
	if err != nil || len(problems) != 0 || target.Kind != "Pod" || target.APIVersion != "v1" {
		return fmt.Errorf("invalid Pod representation")
	}
	return nil
}

func (h *PodHandler) readExpectation(ctx context.Context, namespace string) (sandboxworkload.Expectation, *corev1.ConfigMap, error) {
	var expected sandboxworkload.Expectation
	if namespace == h.config.StateNamespace {
		return expected, nil, fmt.Errorf("controller state is not an Agent namespace")
	}
	ns := &corev1.Namespace{}
	if err := h.reader.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		return expected, nil, err
	}
	if ns.UID == "" || !ns.DeletionTimestamp.IsZero() ||
		ns.Labels[sandboxworkload.ProtectedNamespaceLabel] != "true" {
		return expected, nil, fmt.Errorf("namespace is unprotected, absent or terminating")
	}
	state := &corev1.ConfigMap{}
	if err := h.reader.Get(ctx, client.ObjectKey{Namespace: h.config.StateNamespace, Name: PodExpectationName(namespace, ns.UID)}, state); err != nil {
		return expected, nil, err
	}
	raw := state.Data[PodExpectationKey]
	problems, err := strictjson.UnmarshalStrict([]byte(raw), &expected)
	if err != nil || len(problems) != 0 || len(raw) == 0 || len(raw) > 512*1024 ||
		len(state.Data) != 1 || len(state.BinaryData) != 0 || state.UID == "" || state.ResourceVersion == "" ||
		!state.DeletionTimestamp.IsZero() || expected.Version != sandboxworkload.ContractVersion ||
		expected.Namespace.Name != ns.Name || expected.Namespace.UID != ns.UID ||
		expected.WorkloadKind != sandboxworkload.AgentWorkload || expected.Workload.Name == "" || expected.Workload.UID == "" ||
		expected.Agent.Name == "" || expected.Agent.UID == "" || expected.Revision == "" ||
		state.Annotations[sandboxworkload.RevisionAnnotation] != expected.Revision ||
		state.Annotations[sandboxworkload.AgentUIDAnnotation] != string(expected.Agent.UID) ||
		!equality.Semantic.DeepEqual(state.OwnerReferences, []metav1.OwnerReference{sandboxworkload.AgentOwner(expected.Agent.Name, expected.Agent.UID)}) {
		return expected, nil, fmt.Errorf("invalid controller-owned expectation")
	}
	if ns.Annotations[sandboxworkload.AgentNameAnnotation] != expected.Agent.Name ||
		ns.Annotations[sandboxworkload.AgentUIDAnnotation] != string(expected.Agent.UID) ||
		!equality.Semantic.DeepEqual(ns.OwnerReferences, []metav1.OwnerReference{sandboxworkload.AgentOwner(expected.Agent.Name, expected.Agent.UID)}) {
		return expected, nil, fmt.Errorf("namespace ownership differs from the controller-owned expectation")
	}
	return expected, state, nil
}

func (h *PodHandler) validatePodSources(ctx context.Context, expected sandboxworkload.Expectation, agent *api.Agent) error {
	if len(expected.Sources) < 9 || len(expected.Sources) > 128 {
		return fmt.Errorf("incomplete source inventory")
	}
	type objectKey struct{ version, kind, namespace, name string }
	required := map[objectKey]types.UID{
		{api.GroupVersion.String(), "Agent", "", agent.Name}:                                           expected.Agent.UID,
		{api.GroupVersion.String(), "Team", "", agent.Spec.Team}:                                       expected.Binding.TeamUID,
		{api.GroupVersion.String(), "Sandbox", "", agent.Spec.SandboxRef}:                              expected.Binding.SandboxUID,
		{"v1", "Namespace", "", expected.Namespace.Name}:                                               expected.Namespace.UID,
		{"apps/v1", "StatefulSet", expected.Namespace.Name, expected.Workload.Name}:                    expected.Workload.UID,
		{"v1", "PersistentVolumeClaim", expected.Namespace.Name, expected.Home.Name}:                   expected.Home.UID,
		{"v1", "PersistentVolumeClaim", expected.Namespace.Name, expected.Skills.Name}:                 expected.Skills.UID,
		{"v1", "ServiceAccount", expected.Namespace.Name, expected.Template.Spec.ServiceAccountName}:   "",
		{"node.k8s.io/v1", "RuntimeClass", "", ptr.Deref(expected.Template.Spec.RuntimeClassName, "")}: "",
	}
	for _, source := range sandboxworkload.PodSourceReferences(expected.Template.Spec, expected.Namespace.Name) {
		key := objectKey{source.APIVersion, source.Kind, source.Namespace, source.Name}
		if _, exists := required[key]; !exists {
			required[key] = ""
		}
	}
	seen := map[objectKey]bool{}
	policy := false
	for _, source := range expected.Sources {
		key := objectKey{source.APIVersion, source.Kind, source.Namespace, source.Name}
		if source.UID == "" || source.ResourceVersion == "" || source.Name == "" || seen[key] {
			return fmt.Errorf("invalid source identity")
		}
		version, err := schema.ParseGroupVersion(source.APIVersion)
		if err != nil {
			return err
		}
		object := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: source.APIVersion, Kind: source.Kind}}
		object.SetGroupVersionKind(version.WithKind(source.Kind))
		if err := h.reader.Get(ctx, client.ObjectKey{Namespace: source.Namespace, Name: source.Name}, object); err != nil {
			return err
		}
		if object.UID != source.UID || object.ResourceVersion != source.ResourceVersion || !object.DeletionTimestamp.IsZero() {
			return fmt.Errorf("source changed")
		}
		if uid, exists := required[key]; exists {
			if uid != "" && uid != object.UID {
				return fmt.Errorf("required source binding changed")
			}
			delete(required, key)
		}
		if source.APIVersion == api.GroupVersion.String() && source.Kind == "AgentPolicy" &&
			source.Namespace == "" && source.UID == expected.Binding.AgentPolicyUID {
			policy = true
		}
		if source.APIVersion == "v1" && source.Kind == "PersistentVolumeClaim" &&
			source.Namespace == expected.Namespace.Name && source.Name == expected.Home.Name &&
			object.Annotations[sandboxworkload.AgentUIDAnnotation] != string(expected.Agent.UID) {
			return fmt.Errorf("HOME ownership changed")
		}
		seen[key] = true
	}
	if len(required) != 0 || !policy {
		return fmt.Errorf("required source identities are missing")
	}
	runtimeClass := &nodev1.RuntimeClass{}
	if err := h.reader.Get(ctx, client.ObjectKey{Name: ptr.Deref(expected.Template.Spec.RuntimeClassName, "")}, runtimeClass); err != nil {
		return err
	}
	var overhead corev1.ResourceList
	if runtimeClass.Overhead != nil && len(runtimeClass.Overhead.PodFixed) != 0 {
		overhead = runtimeClass.Overhead.PodFixed
	}
	if !equality.Semantic.DeepEqual(overhead, expected.Overhead) {
		return fmt.Errorf("RuntimeClass overhead changed")
	}
	return nil
}
