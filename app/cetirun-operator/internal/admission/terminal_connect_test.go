// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/terminal"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const terminalServiceAccount = "example-terminal"

// reconciledTerminalFixture creates the controller-owned state that a terminal
// CONNECT needs. It deliberately uses ordinary fake API objects so the test
// verifies the admission readback path rather than an in-memory allowance.
func reconciledTerminalFixture(t *testing.T) podFixture {
	t.Helper()
	f := newPodFixture(t)
	ctx := context.Background()
	owner := w.AgentOwner(f.fixture.Agent.Name, f.fixture.Agent.UID)
	if err := storagev1.AddToScheme(f.scheme); err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(f.scheme); err != nil {
		t.Fatal(err)
	}

	mainAccount := f.fixture.ServiceAccount.DeepCopy()
	mainAccount.OwnerReferences = []metav1.OwnerReference{owner}
	if err := f.client.Update(ctx, mainAccount); err != nil {
		t.Fatal(err)
	}
	terminalAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: terminalServiceAccount, Namespace: f.fixture.Namespace.Name, UID: "terminal-serviceaccount-uid",
		OwnerReferences: []metav1.OwnerReference{owner},
	}, AutomountServiceAccountToken: ptr.To(false)}
	if err := f.client.Create(ctx, terminalAccount); err != nil {
		t.Fatal(err)
	}
	terminalBindings := []*rbacv1.RoleBinding{
		{ObjectMeta: metav1.ObjectMeta{Name: terminalServiceAccount, Namespace: f.fixture.Namespace.Name, OwnerReferences: []metav1.OwnerReference{owner}},
			Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: terminalServiceAccount, Namespace: f.fixture.Namespace.Name}},
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: terminalServiceAccount}},
		{ObjectMeta: metav1.ObjectMeta{Name: terminalServiceAccount + "-exec", Namespace: f.fixture.Namespace.Name, OwnerReferences: []metav1.OwnerReference{owner}},
			Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: terminalServiceAccount, Namespace: f.fixture.Namespace.Name}},
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: terminal.ExecRole}},
	}
	for _, binding := range terminalBindings {
		if err := f.client.Create(ctx, binding); err != nil {
			t.Fatal(err)
		}
	}
	platform := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: "platform-input", Namespace: "operator-state", UID: "platform-input-uid"},
		Data: map[string]string{"configuration": "approved"}}
	storage := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "example-csi", UID: "storageclass-uid"},
		Provisioner: "example.test/csi"}
	if err := f.client.Create(ctx, platform); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Create(ctx, storage); err != nil {
		t.Fatal(err)
	}

	main := &appsv1.StatefulSet{}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.workload), main); err != nil {
		t.Fatal(err)
	}
	main.Generation = 1
	if err := f.client.Update(ctx, main); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(main), main); err != nil {
		t.Fatal(err)
	}
	main.Status.ObservedGeneration, main.Status.ReadyReplicas = 1, 1
	if err := f.client.Status().Update(ctx, main); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(main), main); err != nil {
		t.Fatal(err)
	}
	f.workload = main

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: main.Name + "-0", Namespace: main.Namespace, UID: "main-pod-uid",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: main.Name, UID: main.UID,
			Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}},
	}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "cetirun"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "cetirun", Ready: true}}}}
	if err := f.client.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
		t.Fatal(err)
	}

	terminalTemplate := corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		ServiceAccountName: terminalServiceAccount, AutomountServiceAccountToken: ptr.To(false),
		Containers: []corev1.Container{{Name: "terminal", Image: "registry.example.test/terminal@sha256:" + strings.Repeat("a", 64)}},
	}}
	expected := w.ReconcileExpectation{
		Version: w.ReconcileExpectationVersion,
		Agent:   w.ObjectIdentity{Name: f.fixture.Agent.Name, UID: f.fixture.Agent.UID}, AgentGeneration: f.fixture.Agent.Generation,
		Namespace: w.ObjectIdentity{Name: f.fixture.Namespace.Name, UID: f.fixture.Namespace.UID}, Binding: f.expected.Binding,
		Revision: strings.Repeat("b", 64), PlanHash: strings.Repeat("c", 64),
		Home: w.ObjectIdentity{Name: f.fixture.Input.Home.Name, UID: f.fixture.Input.Home.UID},
		Workloads: []w.RenderedWorkload{
			{Kind: "StatefulSet", Identity: w.ObjectIdentity{Name: main.Name, UID: main.UID}, Replicas: 1, Template: main.Spec.Template, Overhead: f.expected.Overhead},
			{Kind: "Deployment", Identity: w.ObjectIdentity{Name: terminalServiceAccount, UID: "terminal-deployment-uid"}, Replicas: 1, Template: terminalTemplate},
		},
		Terminal: &w.TerminalTarget{ServiceAccountName: terminalServiceAccount, TargetWorkload: main.Name, Container: "cetirun"},
	}
	expected.Controls = []unstructured.Unstructured{
		terminalControl(t, "ServiceAccount", mainAccount),
		terminalControl(t, "ServiceAccount", terminalAccount),
		terminalRoleBindingControl(t, terminalBindings[0]),
		terminalRoleBindingControl(t, terminalBindings[1]),
		{Object: map[string]any{
			"apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy",
			"metadata": map[string]any{"name": "terminal-network", "namespace": f.fixture.Namespace.Name,
				"ownerReferences": []any{terminalOwner(owner)}},
		}},
	}
	expected.Sources = append(expected.Sources, f.expected.Sources...)
	expected.Sources = append(expected.Sources,
		w.SourceReference{APIVersion: "v1", Kind: "ConfigMap", Namespace: platform.Namespace, Name: platform.Name, UID: platform.UID, ResourceVersion: platform.ResourceVersion},
		w.SourceReference{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass", Name: storage.Name, UID: storage.UID, ResourceVersion: storage.ResourceVersion},
	)
	refreshTerminalSources(t, &f, &expected)
	if err := expected.Validate(); err != nil {
		t.Fatalf("terminal fixture expectation is invalid: %v", err)
	}
	if err := f.client.Delete(ctx, f.state); err != nil {
		t.Fatal(err)
	}
	state, err := expected.Object("operator-state")
	if err != nil {
		t.Fatal(err)
	}
	state.UID = "reconciled-state-uid"
	if err := f.client.Create(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(state), state); err != nil {
		t.Fatal(err)
	}
	f.expected = w.Expectation{} // The legacy snapshot must not be used by this fixture.
	f.state = state
	return f
}

func terminalRoleBindingControl(t *testing.T, binding *rbacv1.RoleBinding) unstructured.Unstructured {
	t.Helper()
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding",
		"metadata": map[string]any{"name": binding.Name, "namespace": binding.Namespace,
			"ownerReferences": []any{terminalOwner(binding.OwnerReferences[0])}},
		"subjects": []any{map[string]any{"kind": binding.Subjects[0].Kind, "name": binding.Subjects[0].Name, "namespace": binding.Subjects[0].Namespace}},
		"roleRef":  map[string]any{"apiGroup": binding.RoleRef.APIGroup, "kind": binding.RoleRef.Kind, "name": binding.RoleRef.Name},
	}}
}

func terminalOwner(owner metav1.OwnerReference) any {
	return map[string]any{"apiVersion": owner.APIVersion, "kind": owner.Kind, "name": owner.Name, "uid": string(owner.UID),
		"controller": true, "blockOwnerDeletion": true}
}

func terminalControl(t *testing.T, kind string, object client.Object) unstructured.Unstructured {
	t.Helper()
	value := map[string]any{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{
		"name": object.GetName(), "namespace": object.GetNamespace(), "uid": string(object.GetUID()),
		"ownerReferences": []any{terminalOwner(object.GetOwnerReferences()[0])},
	}}
	return unstructured.Unstructured{Object: value}
}

func refreshTerminalSources(t *testing.T, f *podFixture, expected *w.ReconcileExpectation) {
	t.Helper()
	sources := make([]w.SourceReference, 0, len(expected.Sources))
	expected.SourceDigests = map[string]string{}
	seen := map[string]bool{}
	for _, source := range expected.Sources {
		key := w.SourceKey(source)
		if seen[key] {
			continue
		}
		seen[key] = true
		object := terminalSourceObject(t, source)
		if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: source.Namespace, Name: source.Name}, object); err != nil {
			t.Fatalf("read %s: %v", key, err)
		}
		if source.IsAgent() {
			current, err := w.AgentSourceReference(object.(*api.Agent))
			if err != nil {
				t.Fatal(err)
			}
			source = current
		} else {
			source.UID, source.ResourceVersion = object.GetUID(), object.GetResourceVersion()
		}
		if w.EffectiveSource(source) {
			digest, err := w.ExecutionSourceDigest(object, f.fixture.Sandbox.Name)
			if claim, ok := object.(*corev1.PersistentVolumeClaim); ok {
				digest, err = w.NativeClaimDigest(claim)
			}
			if err != nil {
				t.Fatalf("digest %s: %v", key, err)
			}
			expected.SourceDigests[w.SourceKey(source)] = digest
		}
		sources = append(sources, source)
	}
	expected.Sources = sources
}

func terminalSourceObject(t *testing.T, source w.SourceReference) client.Object {
	t.Helper()
	switch source.APIVersion + "/" + source.Kind {
	case api.GroupVersion.String() + "/Agent":
		return &api.Agent{}
	case api.GroupVersion.String() + "/Team":
		return &api.Team{}
	case api.GroupVersion.String() + "/Sandbox":
		return &api.Sandbox{}
	case api.GroupVersion.String() + "/AgentPolicy":
		return &api.AgentPolicy{}
	case "v1/Namespace":
		return &corev1.Namespace{}
	case "v1/ServiceAccount":
		return &corev1.ServiceAccount{}
	case "v1/PersistentVolumeClaim":
		return &corev1.PersistentVolumeClaim{}
	case "v1/ConfigMap":
		return &corev1.ConfigMap{}
	case "v1/Secret":
		return &corev1.Secret{}
	case "apps/v1/StatefulSet":
		return &appsv1.StatefulSet{}
	case "node.k8s.io/v1/RuntimeClass":
		return &nodev1.RuntimeClass{}
	case "storage.k8s.io/v1/StorageClass":
		return &storagev1.StorageClass{}
	default:
		version, err := schema.ParseGroupVersion(source.APIVersion)
		if err != nil {
			t.Fatal(err)
		}
		value := &unstructured.Unstructured{}
		value.SetGroupVersionKind(version.WithKind(source.Kind))
		return value
	}
}

func terminalConnectRequest(t *testing.T, f podFixture, options corev1.PodExecOptions) admission.Request {
	t.Helper()
	raw, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		UID: "terminal-connect", Operation: admissionv1.Connect, Namespace: f.fixture.Namespace.Name, Name: f.workload.Name + "-0",
		Resource: metav1.GroupVersionResource{Version: "v1", Resource: "pods"},
		Kind:     metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}, SubResource: "exec", RequestSubResource: "exec",
		UserInfo: authenticationv1.UserInfo{Username: "system:serviceaccount:" + f.fixture.Namespace.Name + ":" + terminalServiceAccount},
		Object:   runtime.RawExtension{Raw: raw},
	}}
}

func TestTerminalConnectRequiresCurrentReconciledAuthority(t *testing.T) {
	valid := func() corev1.PodExecOptions {
		return corev1.PodExecOptions{Container: "cetirun", Command: []string{"sh", "-i"}, Stdout: true, Stdin: true, TTY: true}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *podFixture, *admission.Request)
		allow  bool
	}{
		{name: "current isolated terminal", allow: true},
		{name: "wrong service account", mutate: func(_ *testing.T, _ *podFixture, request *admission.Request) {
			request.UserInfo.Username = "system:serviceaccount:cetirun-example:other"
		}},
		{name: "wrong target pod", mutate: func(_ *testing.T, _ *podFixture, request *admission.Request) { request.Name = "other-0" }},
		{name: "wrong container", mutate: func(t *testing.T, _ *podFixture, request *admission.Request) {
			var options corev1.PodExecOptions
			if err := json.Unmarshal(request.Object.Raw, &options); err != nil {
				t.Fatal(err)
			}
			options.Container = "other"
			request.Object.Raw, _ = json.Marshal(options)
		}},
		{name: "arbitrary command", mutate: func(t *testing.T, _ *podFixture, request *admission.Request) {
			var options corev1.PodExecOptions
			if err := json.Unmarshal(request.Object.Raw, &options); err != nil {
				t.Fatal(err)
			}
			options.Command = []string{"sh", "-c", "id"}
			request.Object.Raw, _ = json.Marshal(options)
		}},
		{name: "changed effective config", mutate: func(t *testing.T, f *podFixture, _ *admission.Request) {
			config := &corev1.ConfigMap{}
			if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: f.fixture.Namespace.Name, Name: "example-config"}, config); err != nil {
				t.Fatal(err)
			}
			config.Data = map[string]string{"setting": "changed"}
			if err := f.client.Update(context.Background(), config); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "changed Agent generation", mutate: func(t *testing.T, f *podFixture, _ *admission.Request) {
			agent := &api.Agent{}
			if err := f.client.Get(context.Background(), client.ObjectKey{Name: f.fixture.Agent.Name}, agent); err != nil {
				t.Fatal(err)
			}
			agent.Generation++
			if err := f.client.Update(context.Background(), agent); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing main pod", mutate: func(t *testing.T, f *podFixture, _ *admission.Request) {
			if err := f.client.Delete(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: f.workload.Name + "-0", Namespace: f.fixture.Namespace.Name}}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "replaced main workload", mutate: func(t *testing.T, f *podFixture, _ *admission.Request) {
			current := &appsv1.StatefulSet{}
			if err := f.client.Get(context.Background(), client.ObjectKeyFromObject(f.workload), current); err != nil {
				t.Fatal(err)
			}
			if err := f.client.Delete(context.Background(), current); err != nil {
				t.Fatal(err)
			}
			current.UID, current.ResourceVersion = types.UID("replacement-statefulset-uid"), ""
			if err := f.client.Create(context.Background(), current); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := reconciledTerminalFixture(t)
			request := terminalConnectRequest(t, f, valid())
			if tc.mutate != nil {
				tc.mutate(t, &f, &request)
			}
			response := f.handler.Validate(context.Background(), request)
			if response.Allowed != tc.allow {
				t.Fatalf("allowed=%v want=%v response=%+v", response.Allowed, tc.allow, response.Result)
			}
		})
	}
}

func TestTerminalConnectRequiresCurrentNativeReservation(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		stop       bool
		stopDuring bool
		allow      bool
	}{
		{name: "current reservation", allow: true},
		{name: "stopping reservation", stop: true},
		{name: "reservation stopped during target validation", stopDuring: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			f := reconciledTerminalFixture(t)
			expected, err := w.ReadReconcileExpectation(f.state)
			if err != nil {
				t.Fatal(err)
			}
			authority, err := sandboxcoordination.NewState("operator-state", "coordination")
			if err != nil {
				t.Fatal(err)
			}
			authority.UID = "coordination-uid"
			if err := f.client.Create(ctx, authority); err != nil {
				t.Fatal(err)
			}
			if err := f.client.Get(ctx, client.ObjectKeyFromObject(authority), authority); err != nil {
				t.Fatal(err)
			}
			store := sandboxcoordination.Store{Client: f.client, APIReader: f.client, Namespace: authority.Namespace,
				Identity: w.ObjectIdentity{Name: authority.Name, UID: authority.UID}}
			intent, err := sandboxcoordination.ReconciledIntentFor(expected)
			if err != nil {
				t.Fatal(err)
			}
			ticket, err := store.AcquireNative(ctx, intent)
			if err != nil {
				t.Fatal(err)
			}
			if scenario.stop {
				if _, err := store.BeginNativeStop(ctx, ticket); err != nil {
					t.Fatal(err)
				}
			}
			var reader client.Reader = f.client
			if scenario.stopDuring {
				reader = &terminalStopDuringRead{Reader: f.client, stop: func() error {
					_, err := store.BeginNativeStop(ctx, ticket)
					return err
				}}
			}
			f.handler, err = NewPodHandler(reader, PodConfig{StateNamespace: "operator-state",
				CoordinationConfigMapName: authority.Name, CoordinationConfigMapUID: string(authority.UID)})
			if err != nil {
				t.Fatal(err)
			}
			response := f.handler.Validate(ctx, terminalConnectRequest(t, f, corev1.PodExecOptions{
				Container: "cetirun", Command: []string{"sh", "-i"}, Stdout: true, Stdin: true, TTY: true,
			}))
			if response.Allowed != scenario.allow {
				t.Fatalf("allowed=%v want=%v response=%+v", response.Allowed, scenario.allow, response.Result)
			}
		})
	}
}

// Stop only after the initial reservation check, while the actual target Pod
// is read. Source and expectation revisions remain unchanged during this race.
type terminalStopDuringRead struct {
	client.Reader
	stop func() error
}

func (r *terminalStopDuringRead) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if err := r.Reader.Get(ctx, key, object, options...); err != nil {
		return err
	}
	if _, ok := object.(*corev1.Pod); ok && r.stop != nil {
		stop := r.stop
		r.stop = nil
		return stop()
	}
	return nil
}
