// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// PodMismatchError exposes bounded field paths only, never either Pod's values.
type PodMismatchError struct{ Fields []string }

func (e *PodMismatchError) Error() string {
	return "final Pod differs from the protected template at " + strings.Join(e.Fields, ", ")
}

// ValidatePod compares every final PodSpec field, not just selected security
// fields, with a controller-owned chart-rendered expectation. Only documented
// Kubernetes and StatefulSet defaults are normalized. It makes no lifecycle or
// authorization decision; callers must freshly verify persisted ownership.
func ValidatePod(pod *corev1.Pod, expected Expectation) error {
	if pod == nil || expected.Version != ExpectationVersion || expected.WorkloadKind != AgentWorkload ||
		expected.Workload.Name == "" || expected.Workload.UID == "" || expected.Agent.UID == "" ||
		expected.Namespace.UID == "" || expected.Revision == "" || expected.Binding.AgentUID != expected.Agent.UID ||
		expected.Template.Annotations[KindAnnotation] != AgentWorkload ||
		expected.Template.Annotations[AgentUIDAnnotation] != string(expected.Agent.UID) ||
		expected.Template.Annotations[RevisionAnnotation] != expected.Revision ||
		expected.Template.Annotations["sidecar.istio.io/inject"] != "false" {
		return fmt.Errorf("a complete Agent workload expectation is required; helper classification is not supported")
	}
	if expected.Placement != nil {
		if err := expected.Placement.MatchesExpectation(expected); err != nil {
			return err
		}
		if pod.Spec.NodeName != "" && !expected.Placement.containsNode(pod.Spec.NodeName) {
			return &PodMismatchError{[]string{"spec.nodeName"}}
		}
	}
	if err := validateExpectedInventory(expected.Template.Spec); err != nil {
		return err
	}
	name := expected.Workload.Name + "-0"
	if pod.Name != name || pod.Namespace != expected.Namespace.Name ||
		(pod.GenerateName != "" && pod.GenerateName != expected.Workload.Name+"-") {
		return fmt.Errorf("Pod does not match the expected single-Agent workload")
	}
	owner := metav1.OwnerReference{
		APIVersion: "apps/v1", Kind: "StatefulSet", Name: expected.Workload.Name, UID: expected.Workload.UID,
		Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
	}
	if !equality.Semantic.DeepEqual(pod.OwnerReferences, []metav1.OwnerReference{owner}) {
		return &PodMismatchError{[]string{"metadata.ownerReferences"}}
	}
	if len(pod.Finalizers) != 0 {
		return &PodMismatchError{[]string{"metadata.finalizers"}}
	}
	if !equalJSON(pod.Annotations, expected.Template.Annotations) {
		return &PodMismatchError{[]string{"metadata.annotations"}}
	}
	labels := map[string]string{}
	for key, value := range expected.Template.Labels {
		labels[key] = value
	}
	labels["statefulset.kubernetes.io/pod-name"] = name
	labels["apps.kubernetes.io/pod-index"] = "0"
	if expected.ControllerRevision != "" {
		labels["controller-revision-hash"] = expected.ControllerRevision
	}
	if !equality.Semantic.DeepEqual(pod.Labels, labels) {
		return &PodMismatchError{[]string{"metadata.labels"}}
	}
	want := expected.Template.Spec.DeepCopy()
	want.Hostname, want.Subdomain, want.Overhead = name, expected.Workload.Name, expected.Overhead.DeepCopy()
	got := pod.Spec.DeepCopy()
	if err := defaultPod(want); err != nil {
		return fmt.Errorf("normalize expected Pod: %w", err)
	}
	if err := defaultPod(got); err != nil {
		return fmt.Errorf("normalize final Pod: %w", err)
	}
	if !equality.Semantic.DeepEqual(want, got) {
		var fields []string
		podDifferencePaths(reflect.ValueOf(want), reflect.ValueOf(got), "spec", &fields)
		return &PodMismatchError{fields}
	}
	return nil
}

func podDifferencePaths(a, b reflect.Value, path string, fields *[]string) {
	if len(*fields) >= 8 || equality.Semantic.DeepEqual(a.Interface(), b.Interface()) {
		return
	}
	switch a.Kind() {
	case reflect.Pointer:
		if !a.IsNil() && !b.IsNil() {
			podDifferencePaths(a.Elem(), b.Elem(), path, fields)
			return
		}
	case reflect.Struct:
		if !a.Type().Implements(reflect.TypeFor[json.Marshaler]()) &&
			!reflect.PointerTo(a.Type()).Implements(reflect.TypeFor[json.Marshaler]()) {
			before := len(*fields)
			for i := range a.NumField() {
				field := a.Type().Field(i)
				name := strings.Split(field.Tag.Get("json"), ",")[0]
				if field.IsExported() && name != "" && name != "-" {
					podDifferencePaths(a.Field(i), b.Field(i), path+"."+name, fields)
				}
			}
			if len(*fields) != before {
				return
			}
		}
	case reflect.Slice:
		if a.Len() == b.Len() && a.IsNil() == b.IsNil() {
			for i := range a.Len() {
				podDifferencePaths(a.Index(i), b.Index(i), fmt.Sprintf("%s[%d]", path, i), fields)
			}
			return
		}
	}
	// Maps are leaves: user-controlled map keys are not diagnostic field names.
	*fields = append(*fields, path)
}

func validateExpectedInventory(spec corev1.PodSpec) error {
	if ptr.Deref(spec.RuntimeClassName, "") == "" || spec.Resources == nil || spec.HostNetwork || spec.HostPID || spec.HostIPC ||
		spec.DNSPolicy != corev1.DNSNone || spec.DNSConfig == nil ||
		ptr.Deref(spec.ShareProcessNamespace, false) || spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken ||
		len(spec.EphemeralContainers) != 0 || len(spec.Containers) == 0 || spec.Containers[0].Name != "cetirun" ||
		len(spec.InitContainers) == 0 || spec.InitContainers[0].Name != "init-home-data" {
		return fmt.Errorf("controller expectation is not a protected native workload")
	}
	if err := validateDNSIPs(spec.DNSConfig.Nameservers); err != nil {
		return err
	}
	if len(spec.DNSConfig.Searches) != 0 || len(spec.DNSConfig.Options) != 0 {
		return fmt.Errorf("controller expectation contains unsupported DNS search or option overrides")
	}
	seen := map[string]bool{}
	for _, group := range []struct {
		containers []corev1.Container
		allowed    []string
	}{
		{spec.Containers, []string{"cetirun", "logger", "desktop"}},
		{spec.InitContainers, []string{"init-home-data", "doctor"}},
	} {
		for _, container := range group.containers {
			sc := container.SecurityContext
			if seen[container.Name] || !slices.Contains(group.allowed, container.Name) || sc == nil ||
				!ptr.Deref(sc.RunAsNonRoot, false) || ptr.Deref(sc.RunAsUser, 0) <= 0 ||
				sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation || ptr.Deref(sc.Privileged, false) ||
				sc.ReadOnlyRootFilesystem == nil || sc.Capabilities == nil ||
				!slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) || len(sc.Capabilities.Add) != 0 ||
				sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
				return fmt.Errorf("controller expectation contains an unapproved helper or security context")
			}
			for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory, corev1.ResourceEphemeralStorage} {
				request, limit := container.Resources.Requests[name], container.Resources.Limits[name]
				if request.Sign() <= 0 || limit.Sign() <= 0 || request.Cmp(limit) > 0 {
					return fmt.Errorf("controller expectation has an incomplete container resource budget")
				}
			}
			seen[container.Name] = true
		}
	}
	return nil
}

// PodSourceReferences lists names whose current UID/resourceVersion must be
// added to the controller's source snapshot. It never reads Secret contents.
func PodSourceReferences(spec corev1.PodSpec, namespace string) []SourceReference {
	seen := map[string]SourceReference{}
	add := func(kind, name string) {
		if name != "" {
			seen[kind+"/"+name] = SourceReference{APIVersion: "v1", Kind: kind, Namespace: namespace, Name: name}
		}
	}
	add("ServiceAccount", spec.ServiceAccountName)
	for _, volume := range spec.Volumes {
		if volume.ConfigMap != nil {
			add("ConfigMap", volume.ConfigMap.Name)
		}
		if volume.Secret != nil {
			add("Secret", volume.Secret.SecretName)
		}
		if volume.PersistentVolumeClaim != nil {
			add("PersistentVolumeClaim", volume.PersistentVolumeClaim.ClaimName)
		}
		if volume.Projected != nil {
			for _, projection := range volume.Projected.Sources {
				if projection.ConfigMap != nil {
					add("ConfigMap", projection.ConfigMap.Name)
				}
				if projection.Secret != nil {
					add("Secret", projection.Secret.Name)
				}
			}
		}
	}
	for _, container := range append(slices.Clone(spec.Containers), spec.InitContainers...) {
		for _, env := range container.Env {
			if env.ValueFrom == nil {
				continue
			}
			if env.ValueFrom.SecretKeyRef != nil {
				add("Secret", env.ValueFrom.SecretKeyRef.Name)
			}
			if env.ValueFrom.ConfigMapKeyRef != nil {
				add("ConfigMap", env.ValueFrom.ConfigMapKeyRef.Name)
			}
		}
	}
	refs := make([]SourceReference, 0, len(seen))
	for _, ref := range seen {
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b SourceReference) int { return strings.Compare(a.Kind+"/"+a.Name, b.Kind+"/"+b.Name) })
	return refs
}

func SamePodTemplate(actual, expected corev1.PodTemplateSpec) bool {
	a, b := actual.DeepCopy(), expected.DeepCopy()
	if defaultPod(&a.Spec) != nil || defaultPod(&b.Spec) != nil {
		return false
	}
	return equalJSON(a.Labels, b.Labels) && equalJSON(a.Annotations, b.Annotations) &&
		len(a.Finalizers) == 0 && len(a.OwnerReferences) == 0 && equality.Semantic.DeepEqual(a.Spec, b.Spec)
}

func defaultPod(spec *corev1.PodSpec) error {
	if spec.RestartPolicy == "" {
		spec.RestartPolicy = corev1.RestartPolicyAlways
	}
	if spec.DNSPolicy == "" {
		spec.DNSPolicy = corev1.DNSClusterFirst
	}
	if spec.SchedulerName == "" {
		spec.SchedulerName = corev1.DefaultSchedulerName
	}
	if spec.TerminationGracePeriodSeconds == nil {
		spec.TerminationGracePeriodSeconds = ptr.To(int64(30))
	}
	if spec.DeprecatedServiceAccount == "" {
		spec.DeprecatedServiceAccount = spec.ServiceAccountName
	}
	if spec.Priority == nil {
		spec.Priority = ptr.To(int32(0))
	}
	if spec.PreemptionPolicy == nil {
		spec.PreemptionPolicy = ptr.To(corev1.PreemptLowerPriority)
	}
	for i := range spec.Containers {
		defaultContainer(&spec.Containers[i])
	}
	for i := range spec.InitContainers {
		defaultContainer(&spec.InitContainers[i])
	}
	for i := range spec.Volumes {
		source := &spec.Volumes[i].VolumeSource
		if source.ConfigMap != nil && source.ConfigMap.DefaultMode == nil {
			source.ConfigMap.DefaultMode = ptr.To(int32(0644))
		}
		if source.Secret != nil && source.Secret.DefaultMode == nil {
			source.Secret.DefaultMode = ptr.To(int32(0644))
		}
		if source.Projected != nil && source.Projected.DefaultMode == nil {
			source.Projected.DefaultMode = ptr.To(int32(0644))
		}
		if source.DownwardAPI != nil {
			if source.DownwardAPI.DefaultMode == nil {
				source.DownwardAPI.DefaultMode = ptr.To(int32(0644))
			}
			defaultDownwardItems(source.DownwardAPI.Items)
		}
		if source.Projected != nil {
			for _, projection := range source.Projected.Sources {
				if projection.DownwardAPI != nil {
					defaultDownwardItems(projection.DownwardAPI.Items)
				}
			}
		}
	}
	for _, key := range []string{"node.kubernetes.io/not-ready", "node.kubernetes.io/unreachable"} {
		found := false
		for _, toleration := range spec.Tolerations {
			found = found || toleration.Key == key && toleration.Effect == corev1.TaintEffectNoExecute
		}
		if !found {
			spec.Tolerations = append(spec.Tolerations, corev1.Toleration{
				Key: key, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: ptr.To(int64(300)),
			})
		}
	}
	slices.SortFunc(spec.Tolerations, func(a, b corev1.Toleration) int {
		return strings.Compare(a.Key+"/"+string(a.Effect)+"/"+string(a.Operator)+"/"+a.Value, b.Key+"/"+string(b.Effect)+"/"+string(b.Operator)+"/"+b.Value)
	})
	// JSON's omitempty makes zero-length slices/maps equivalent to omitted
	// fields, without discarding any non-empty or newly added Pod field.
	normalized, err := clone(*spec)
	if err != nil {
		return err
	}
	*spec = normalized
	return nil
}

func defaultDownwardItems(items []corev1.DownwardAPIVolumeFile) {
	for i := range items {
		defaultFieldSelectors(items[i].FieldRef, items[i].ResourceFieldRef)
	}
}

func defaultFieldSelectors(field *corev1.ObjectFieldSelector, resourceField *corev1.ResourceFieldSelector) {
	if field != nil && field.APIVersion == "" {
		field.APIVersion = "v1"
	}
	if resourceField != nil && resourceField.Divisor.IsZero() {
		resourceField.Divisor = resource.MustParse("1")
	}
}

func defaultContainer(container *corev1.Container) {
	if container.ImagePullPolicy == "" {
		container.ImagePullPolicy = corev1.PullIfNotPresent
		tag := container.Image[strings.LastIndex(container.Image, "/")+1:]
		if !strings.Contains(container.Image, "@") && (!strings.Contains(tag, ":") || strings.HasSuffix(tag, ":latest")) {
			container.ImagePullPolicy = corev1.PullAlways
		}
	}
	if container.TerminationMessagePath == "" {
		container.TerminationMessagePath = corev1.TerminationMessagePathDefault
	}
	if container.TerminationMessagePolicy == "" {
		container.TerminationMessagePolicy = corev1.TerminationMessageReadFile
	}
	for _, env := range container.Env {
		if env.ValueFrom != nil {
			defaultFieldSelectors(env.ValueFrom.FieldRef, env.ValueFrom.ResourceFieldRef)
		}
	}
	for i := range container.Ports {
		if container.Ports[i].Protocol == "" {
			container.Ports[i].Protocol = corev1.ProtocolTCP
		}
	}
	for _, probe := range []*corev1.Probe{container.LivenessProbe, container.ReadinessProbe, container.StartupProbe} {
		if probe == nil {
			continue
		}
		if probe.TimeoutSeconds == 0 {
			probe.TimeoutSeconds = 1
		}
		if probe.PeriodSeconds == 0 {
			probe.PeriodSeconds = 10
		}
		if probe.SuccessThreshold == 0 {
			probe.SuccessThreshold = 1
		}
		if probe.FailureThreshold == 0 {
			probe.FailureThreshold = 3
		}
		if probe.HTTPGet != nil {
			if probe.HTTPGet.Scheme == "" {
				probe.HTTPGet.Scheme = corev1.URISchemeHTTP
			}
			if probe.HTTPGet.Path == "" {
				probe.HTTPGet.Path = "/"
			}
		}
	}
}
