// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strconv"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/terminal"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

func (p *PreparedRelease) validateReconciledObject(object unstructured.Unstructured) error {
	input := p.sandboxBundle.Identity()
	var workload *w.RenderedWorkload
	if object.GetKind() == "Deployment" || object.GetKind() == "StatefulSet" {
		annotations, _, err := unstructured.NestedStringMap(object.Object, "spec", "template", "metadata", "annotations")
		if err != nil {
			return err
		}
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[w.ReconciledAnnotation] = "true"
		if err := unstructured.SetNestedStringMap(object.Object, annotations, "spec", "template", "metadata", "annotations"); err != nil {
			return err
		}
	}
	switch object.GetAPIVersion() + "/" + object.GetKind() {
	case "apps/v1/StatefulSet":
		var stateful appsv1.StatefulSet
		if err := decodeObject(object.Object, &stateful); err != nil {
			return err
		}
		if stateful.Name != input.WorkloadName || len(stateful.Spec.VolumeClaimTemplates) != 0 ||
			stateful.Spec.ServiceName != stateful.Name || stateful.Spec.Selector == nil {
			return fmt.Errorf("reconciled Sandbox must use the owned Agent workload and existing HOME")
		}
		// Memory enablement was resolved from typed platform options before this
		// protected render and is sealed with its chart values. Do not infer it
		// from the rendered Pod, which is the object being validated.
		memoryValues, _ := p.Values["memoryContext"].(map[string]any)
		memoryEnabled := memoryValues["enabled"] == true
		if err := p.sandboxBundle.ValidateReconciledTemplate(&stateful.Spec.Template, p.Agent.Image, memoryEnabled); err != nil {
			return err
		}
		if request := input.PlannedMigration; request != nil {
			if err := runtimebindings.PinMigrationNode(&stateful.Spec.Template.Spec, request.Target.Name); err != nil {
				return err
			}
			stateful.Spec.Template.Annotations[api.AnnotationMigrationRequest] = request.RequestID
		}
		helpers := input.Resolution.Sandbox.Resources.Helpers
		helperResources := corev1.ResourceRequirements{Requests: helpers.Requests, Limits: helpers.Limits}
		for index := range stateful.Spec.Template.Spec.InitContainers {
			stateful.Spec.Template.Spec.InitContainers[index].Resources = *helperResources.DeepCopy()
		}
		for index := 1; index < len(stateful.Spec.Template.Spec.Containers); index++ {
			stateful.Spec.Template.Spec.Containers[index].Resources = *helperResources.DeepCopy()
		}
		accounting := w.AccountResources(stateful.Spec.Template.Spec, p.sandboxBundle.Overhead())
		stateful.Spec.Template.Spec.Resources = &accounting.Pod
		template, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&stateful.Spec.Template)
		if err != nil {
			return err
		}
		if err := unstructured.SetNestedMap(object.Object, template, "spec", "template"); err != nil {
			return err
		}
		p.sandboxTemplate = stateful.Spec.Template.DeepCopy()
		workload = &w.RenderedWorkload{
			Kind: "StatefulSet", Identity: w.ObjectIdentity{Name: stateful.Name},
			Replicas: ptr.Deref(stateful.Spec.Replicas, 1), Template: stateful.Spec.Template,
			Overhead: p.sandboxBundle.Overhead(),
		}
	case "apps/v1/Deployment":
		var deployment appsv1.Deployment
		if err := decodeObject(object.Object, &deployment); err != nil {
			return err
		}
		spec := deployment.Spec.Template.Spec
		if spec.HostNetwork || spec.HostPID || spec.HostIPC || ptr.Deref(spec.ShareProcessNamespace, false) ||
			ptr.Deref(spec.AutomountServiceAccountToken, true) || spec.SecurityContext == nil ||
			len(spec.Containers) == 0 || deployment.Spec.Selector == nil {
			return fmt.Errorf("Sandbox auxiliary workload has unapproved host or identity access")
		}
		terminal := input.Terminal != nil && deployment.Name == input.Terminal.ServiceAccountName
		if terminal {
			if err := p.validateTerminalDeployment(&deployment); err != nil {
				return err
			}
		} else if err := w.ValidateReconciledVolumes(spec, nil, nil, input.Resolution.Sandbox.Storage.Scratch.SizeLimit); err != nil {
			return err
		}
		for _, source := range w.PodSourceReferences(spec, input.Namespace.Name) {
			if source.Kind == "Secret" && (slices.Contains(input.AuthorizedSecrets, source.Name) ||
				(input.ImagePullSecret != nil && source.Name == input.ImagePullSecret.Name)) {
				return fmt.Errorf("Sandbox auxiliary workload cannot receive Agent credentials")
			}
		}
		for _, containers := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
			for _, container := range containers {
				sc := container.SecurityContext
				if sc == nil || ptr.Deref(sc.Privileged, false) || ptr.Deref(sc.AllowPrivilegeEscalation, true) ||
					!ptr.Deref(sc.RunAsNonRoot, ptr.Deref(spec.SecurityContext.RunAsNonRoot, false)) ||
					ptr.Deref(sc.RunAsUser, ptr.Deref(spec.SecurityContext.RunAsUser, 0)) <= 0 ||
					sc.Capabilities == nil || len(sc.Capabilities.Add) != 0 ||
					!slices.Contains(sc.Capabilities.Drop, corev1.Capability("ALL")) {
					return fmt.Errorf("Sandbox auxiliary workload privileges are unapproved")
				}
			}
		}
		workload = &w.RenderedWorkload{
			Kind: "Deployment", Identity: w.ObjectIdentity{Name: deployment.Name},
			Replicas: ptr.Deref(deployment.Spec.Replicas, 1), Template: deployment.Spec.Template,
		}
		// RuntimeClass admission injects PodFixed overhead into the terminal Pod.
		// Persist that exact value for the already validated terminal only, so the
		// final-Pod check accepts the Kubernetes-owned mutation without granting
		// the same allowance to auxiliary Deployments.
		if terminal {
			workload.Overhead = p.sandboxBundle.Overhead()
		}
	case "v1/Service":
		var service corev1.Service
		if err := decodeObject(object.Object, &service); err != nil {
			return err
		}
		if service.Spec.Type != corev1.ServiceTypeClusterIP || service.Spec.ExternalName != "" || len(service.Spec.ExternalIPs) != 0 {
			return fmt.Errorf("Sandbox services must remain private")
		}
	case "rbac.authorization.k8s.io/v1/RoleBinding":
		if err := p.validateTerminalRoleBinding(object); err != nil {
			return err
		}
	case "v1/ConfigMap", "v1/ServiceAccount", "networking.k8s.io/v1/NetworkPolicy",
		"networking.istio.io/v1beta1/Sidecar", "security.istio.io/v1beta1/AuthorizationPolicy",
		"security.istio.io/v1beta1/PeerAuthentication":
	default:
		return fmt.Errorf("unsupported chart-owned Sandbox resource %s", object.GetKind())
	}
	if workload != nil {
		if workload.Replicas < 0 || workload.Replicas > 1 {
			return fmt.Errorf("Sandbox workloads require at most one active replica")
		}
		p.sandboxReconcile.Workloads = append(p.sandboxReconcile.Workloads, *workload)
	}
	return nil
}

func (p *PreparedRelease) validateTerminalRoleBinding(object unstructured.Unstructured) error {
	input := p.sandboxBundle.Identity()
	terminalInput := input.Terminal
	if terminalInput == nil {
		return fmt.Errorf("Sandbox RoleBindings require a platform terminal plan")
	}
	var binding rbacv1.RoleBinding
	if err := decodeObject(object.Object, &binding); err != nil {
		return err
	}
	terminalAccount := terminalInput.ServiceAccountName
	if binding.Namespace != input.Namespace.Name || len(binding.Subjects) != 1 ||
		binding.Subjects[0] != (rbacv1.Subject{Kind: "ServiceAccount", Name: terminalAccount, Namespace: input.Namespace.Name}) {
		return fmt.Errorf("Sandbox terminal RoleBinding subject differs from the platform plan")
	}
	want := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: terminalAccount}
	switch binding.Name {
	case terminalAccount:
	case terminalAccount + "-exec":
		want = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: terminal.ExecRole}
	default:
		return fmt.Errorf("Sandbox RoleBinding is not an approved terminal binding")
	}
	if binding.RoleRef != want {
		return fmt.Errorf("Sandbox terminal RoleBinding role differs from the platform plan")
	}
	return nil
}

func (p *PreparedRelease) validateTerminalDeployment(deployment *appsv1.Deployment) error {
	input := p.sandboxBundle.Identity()
	terminal := input.Terminal
	if terminal == nil || deployment.Name != terminal.ServiceAccountName || deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType ||
		deployment.Spec.Selector == nil || ptr.Deref(deployment.Spec.Replicas, 0) != 1 {
		return fmt.Errorf("Sandbox terminal deployment differs from its platform plan")
	}
	labels := map[string]string{
		"app.kubernetes.io/name": "cetirun-terminal", "app.kubernetes.io/instance": p.releaseName,
		"cetirun.microsoft.com/terminal":          "true",
		"sandbox.cetirun.microsoft.com/agent-uid": string(input.Resolution.Binding.AgentUID),
	}
	spec := deployment.Spec.Template.Spec
	var selectors map[string]string
	var tolerations []corev1.Toleration
	if input.RuntimeClass.Scheduling != nil {
		selectors, tolerations = input.RuntimeClass.Scheduling.NodeSelector, input.RuntimeClass.Scheduling.Tolerations
	}
	if !equality.Semantic.DeepEqual(deployment.Spec.Selector.MatchLabels, labels) || !equality.Semantic.DeepEqual(deployment.Spec.Template.Labels, labels) ||
		deployment.Spec.Selector.MatchExpressions != nil || spec.ServiceAccountName != terminal.ServiceAccountName ||
		ptr.Deref(spec.AutomountServiceAccountToken, true) || ptr.Deref(spec.EnableServiceLinks, true) ||
		ptr.Deref(spec.RuntimeClassName, "") != input.RuntimeClass.Name || spec.DNSPolicy != corev1.DNSNone ||
		!equality.Semantic.DeepEqual(spec.DNSConfig, p.sandboxBundle.DNSConfig()) || len(spec.InitContainers) != 0 || len(spec.Containers) != 1 ||
		len(spec.ImagePullSecrets) != 0 || !equality.Semantic.DeepEqual(spec.NodeSelector, selectors) ||
		!equality.Semantic.DeepEqual(spec.Tolerations, tolerations) {
		return fmt.Errorf("Sandbox terminal runtime, identity or scheduling differs from the platform plan")
	}
	container := spec.Containers[0]
	resources := corev1.ResourceRequirements{Requests: input.Resolution.Sandbox.Resources.Helpers.Requests, Limits: input.Resolution.Sandbox.Resources.Helpers.Limits}
	expectedSecurity := &corev1.SecurityContext{RunAsUser: ptr.To[int64](65532), RunAsGroup: ptr.To[int64](65532), RunAsNonRoot: ptr.To(true),
		ReadOnlyRootFilesystem: ptr.To(true), AllowPrivilegeEscalation: ptr.To(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
	expectedEnv := []corev1.EnvVar{
		{Name: "PORT", Value: strconv.Itoa(int(terminal.Port))},
		{Name: "CETIRUN_TERMINAL_AGENT_NAME", Value: input.AgentName},
		{Name: "CETIRUN_TERMINAL_AGENT_UID", Value: string(input.Resolution.Binding.AgentUID)},
		{Name: "CETIRUN_TERMINAL_NAMESPACE", Value: input.Namespace.Name},
		{Name: "CETIRUN_TERMINAL_STATEFULSET", Value: input.WorkloadName},
		{Name: "CETIRUN_TERMINAL_CONTAINER", Value: "cetirun"},
		{Name: "CETIRUN_TERMINAL_SESSION_SECONDS", Value: strconv.Itoa(int(terminal.SessionSeconds))},
	}
	if container.Name != "terminal" || container.Image != terminal.Image || container.ImagePullPolicy != corev1.PullIfNotPresent ||
		!slices.Equal(container.Command, []string{"/terminal"}) || !slices.Equal(container.Args, []string{"serve"}) ||
		!equality.Semantic.DeepEqual(container.SecurityContext, expectedSecurity) || !equality.Semantic.DeepEqual(container.Resources, resources) ||
		!equality.Semantic.DeepEqual(container.Env, expectedEnv) || !equality.Semantic.DeepEqual(container.Ports, []corev1.ContainerPort{{Name: "terminal", ContainerPort: terminal.Port, Protocol: corev1.ProtocolTCP}}) ||
		len(container.EnvFrom) != 0 || len(container.VolumeDevices) != 0 || !terminalProbesMatch(container) {
		return fmt.Errorf("Sandbox terminal container differs from the platform plan")
	}
	expectedVolumes := []corev1.Volume{
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(input.Resolution.Sandbox.Storage.Scratch.SizeLimit.DeepCopy())}}},
		{Name: "api-access", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{DefaultMode: ptr.To[int32](0440), Sources: []corev1.VolumeProjection{
			// Omitted audience asks Kubernetes for its API server's configured
			// audience. A Service DNS name is not necessarily an accepted audience.
			{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token", ExpirationSeconds: ptr.To[int64](3600)}},
			{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"}, Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}},
			{DownwardAPI: &corev1.DownwardAPIProjection{Items: []corev1.DownwardAPIVolumeFile{{Path: "namespace", FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.namespace"}}}}},
		}}}},
	}
	if !equality.Semantic.DeepEqual(spec.Volumes, expectedVolumes) || !equality.Semantic.DeepEqual(container.VolumeMounts, []corev1.VolumeMount{
		{Name: "tmp", MountPath: "/tmp"}, {Name: "api-access", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", ReadOnly: true},
	}) {
		return fmt.Errorf("Sandbox terminal must only mount its scratch and Kubernetes API projections")
	}
	return nil
}

func terminalProbesMatch(container corev1.Container) bool {
	probe := func(path string) *corev1.Probe {
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromString("terminal")}},
			PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 3}
	}
	return equality.Semantic.DeepEqual(container.ReadinessProbe, probe("/healthz")) && equality.Semantic.DeepEqual(container.LivenessProbe, probe("/livez")) &&
		container.StartupProbe == nil
}

type reconciledSandboxRenderer struct {
	prepared *PreparedRelease
}

func (r reconciledSandboxRenderer) Run(_ *bytes.Buffer) (*bytes.Buffer, error) {
	if err := r.prepared.AuthorizeSandboxApply(context.Background()); err != nil {
		return nil, err
	}
	return bytes.NewBuffer(bytes.Clone(r.prepared.manifest)), nil
}

// AuthorizeSandboxApply rechecks authority and records submission intent before
// an installer may write workloads. A failed or uncertain apply remains submitted.
func (p *PreparedRelease) AuthorizeSandboxApply(ctx context.Context) error {
	if p == nil || p.sandboxReconcile == nil || p.sandboxGuard == nil || p.sealedInput != p.inputDigest() {
		return fmt.Errorf("Sandbox apply requires unchanged current render and controller authority")
	}
	return p.sandboxGuard(ctx)
}
