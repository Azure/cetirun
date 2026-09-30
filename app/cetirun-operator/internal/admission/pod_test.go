// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	"helm.sh/helm/v3/pkg/chart/loader"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type podFixture struct {
	fixture  testfixture.Fixture
	scheme   *runtime.Scheme
	client   client.Client
	handler  *PodHandler
	pod      *corev1.Pod
	expected w.Expectation
	state    *corev1.ConfigMap
	workload *appsv1.StatefulSet
	objects  []client.Object
}

func newPodFixture(t *testing.T) podFixture {
	t.Helper()
	fixture := testfixture.New("hermes")
	bundle, err := w.Build(fixture.Input)
	if err != nil {
		t.Fatal(err)
	}
	chrt, err := loader.Load("../../../cetirun-chart")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := helm.PrepareSandboxChart(chrt, map[string]any{
		"agentType": "hermes", "agentRequest": map[string]any{"type": "hermes"},
		"secrets": map[string]any{"existingSecret": "runtime-secret", "provider": "openai", "model": "example-model"},
	}, nil, bundle)
	if err != nil {
		t.Fatal(err)
	}
	template, err := prepared.SandboxTemplate()
	if err != nil {
		t.Fatal(err)
	}
	workload := testfixture.Workload(fixture.Input, *template)
	expected, err := bundle.Expect(*template, workload, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Models a future controller-approved state using persisted API objects.
	// Production rendering remains held and cannot construct an active state.
	expected.ExecutionHeld, expected.ControllerRevision = false, workload.Status.UpdateRevision
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, appsv1.AddToScheme, corev1.AddToScheme, nodev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	objects := []client.Object{
		fixture.Agent, fixture.Team, fixture.Sandbox, fixture.Policy, fixture.Namespace, fixture.ServiceAccount,
		fixture.Input.RuntimeClass, fixture.Input.Home, fixture.Input.Skills, workload,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "example-config", Namespace: fixture.Namespace.Name, UID: "config-uid", ResourceVersion: "1"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "runtime-secret", Namespace: fixture.Namespace.Name, UID: "secret-uid", ResourceVersion: "1"}},
	}
	for _, object := range objects {
		kinds, _, err := scheme.ObjectKinds(object)
		if err != nil {
			t.Fatal(err)
		}
		expected.Sources = append(expected.Sources, w.SourceReference{
			APIVersion: kinds[0].GroupVersion().String(), Kind: kinds[0].Kind, Namespace: object.GetNamespace(), Name: object.GetName(),
			UID: object.GetUID(), ResourceVersion: object.GetResourceVersion(),
		})
	}
	state, err := PodExpectationObject("operator-state", expected)
	if err != nil {
		t.Fatal(err)
	}
	state.UID, state.ResourceVersion = "state-uid", "1"
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objects, state)...).Build()
	handler, err := NewPodHandler(cl, PodConfig{StateNamespace: "operator-state"})
	if err != nil {
		t.Fatal(err)
	}
	return podFixture{fixture: fixture, scheme: scheme, client: cl, handler: handler, pod: testfixture.Pod(expected),
		expected: expected, state: state, workload: workload, objects: objects}
}

func podRequest(t *testing.T, pod *corev1.Pod, operation admissionv1.Operation, old *corev1.Pod) admission.Request {
	t.Helper()
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	request := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		UID: "request-uid", Operation: operation, Namespace: pod.Namespace, Name: pod.Name,
		Resource: metav1.GroupVersionResource{Version: "v1", Resource: "pods"},
		Kind:     metav1.GroupVersionKind{Version: "v1", Kind: "Pod"}, Object: runtime.RawExtension{Raw: raw},
	}}
	if old != nil {
		request.OldObject.Raw, err = json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}
	}
	return request
}

func TestPodAdmissionRequiresPersistedCurrentAuthority(t *testing.T) {
	f := newPodFixture(t)
	request := podRequest(t, f.pod, admissionv1.Create, nil)
	if response := f.handler.Validate(context.Background(), request); !response.Allowed {
		t.Fatalf("valid persisted future-contract state rejected: %+v", response.Result)
	}
	if err := f.client.Delete(context.Background(), f.state); err != nil {
		t.Fatal(err)
	}
	if response := f.handler.Validate(context.Background(), request); response.Allowed {
		t.Fatal("an in-memory expected template authorized a Pod without persisted controller state")
	}
	foreign := f.state.DeepCopy()
	foreign.Namespace = f.pod.Namespace
	foreign.ResourceVersion = ""
	if err := f.client.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	if response := f.handler.Validate(context.Background(), request); response.Allowed {
		t.Fatal("tenant namespace state substituted for protected controller state")
	}
}

func TestPodNamespaceTupleMustMatchAuthoritativeExpectation(t *testing.T) {
	for name, mutate := range map[string]func(*corev1.Namespace){
		"marker removed":       func(ns *corev1.Namespace) { delete(ns.Labels, w.ProtectedNamespaceLabel) },
		"marker false":         func(ns *corev1.Namespace) { ns.Labels[w.ProtectedNamespaceLabel] = "false" },
		"Agent name changed":   func(ns *corev1.Namespace) { ns.Annotations[w.AgentNameAnnotation] = "foreign" },
		"Agent UID changed":    func(ns *corev1.Namespace) { ns.Annotations[w.AgentUIDAnnotation] = "foreign" },
		"owner removed":        func(ns *corev1.Namespace) { ns.OwnerReferences = nil },
		"owner changed":        func(ns *corev1.Namespace) { ns.OwnerReferences[0].UID = "foreign" },
		"non-controller owner": func(ns *corev1.Namespace) { ns.OwnerReferences[0].Controller = ptr.To(false) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newPodFixture(t)
			mutate(f.fixture.Namespace)
			if err := f.client.Update(context.Background(), f.fixture.Namespace); err != nil {
				t.Fatal(err)
			}
			// Keep source RVs current so this tests the ownership tuple itself,
			// not merely the stale-source check exercised elsewhere.
			for i := range f.expected.Sources {
				if f.expected.Sources[i].Kind == "Namespace" {
					f.expected.Sources[i].ResourceVersion = f.fixture.Namespace.ResourceVersion
				}
			}
			raw, err := json.Marshal(f.expected)
			if err != nil {
				t.Fatal(err)
			}
			f.state.Data[PodExpectationKey] = string(raw)
			if err := f.client.Update(context.Background(), f.state); err != nil {
				t.Fatal(err)
			}
			if response := f.handler.Validate(context.Background(), podRequest(t, f.pod, admissionv1.Create, nil)); response.Allowed {
				t.Fatal("current expected-state RVs authorized an unowned namespace")
			}
		})
	}
}

func TestPodAdmissionRejectsDNSMutations(t *testing.T) {
	f := newPodFixture(t)
	for name, mutate := range map[string]func(*corev1.Pod){
		"cluster defaults": func(p *corev1.Pod) { p.Spec.DNSPolicy = corev1.DNSClusterFirst },
		"node defaults":    func(p *corev1.Pod) { p.Spec.DNSPolicy = corev1.DNSDefault },
		"missing config":   func(p *corev1.Pod) { p.Spec.DNSConfig = nil },
		"foreign server":   func(p *corev1.Pod) { p.Spec.DNSConfig.Nameservers[0] = "192.0.2.53" },
		"fallback server": func(p *corev1.Pod) {
			p.Spec.DNSConfig.Nameservers = append(p.Spec.DNSConfig.Nameservers, "192.0.2.53")
		},
		"search domains": func(p *corev1.Pod) { p.Spec.DNSConfig.Searches = []string{"example.com"} },
	} {
		t.Run(name, func(t *testing.T) {
			pod := f.pod.DeepCopy()
			mutate(pod)
			if response := f.handler.Validate(context.Background(), podRequest(t, pod, admissionv1.Create, nil)); response.Allowed {
				t.Fatal("Pod admission accepted mutated DNS despite a current protected expectation")
			}
		})
	}
}

func TestPodAdmissionRejectsStaleHoldAndSpoofedState(t *testing.T) {
	cases := map[string]func(*podFixture){
		"Agent hold": func(f *podFixture) {
			f.fixture.Agent.Finalizers = append(f.fixture.Agent.Finalizers, sandbox.HoldFinalizer)
		},
		"suspension":            func(f *podFixture) { f.fixture.Agent.Spec.Suspended = true },
		"binding removed":       func(f *podFixture) { delete(f.fixture.Agent.Annotations, sandboxbinding.AuthorizationAnnotation) },
		"Agent generation":      func(f *podFixture) { f.fixture.Agent.Generation++ },
		"policy revision":       func(f *podFixture) { f.fixture.Policy.Spec.Security.ReadOnlyRootFilesystem = ptr.To(false) },
		"HOME ownership":        func(f *podFixture) { f.fixture.Input.Home.Annotations[w.AgentUIDAnnotation] = "foreign" },
		"namespace replacement": func(f *podFixture) { f.fixture.Namespace.UID = "replaced" },
		"workload owner":        func(f *podFixture) { f.workload.OwnerReferences[0].UID = "foreign" },
		"workload stopped":      func(f *podFixture) { f.workload.Spec.Replicas = ptr.To(int32(0)) },
		"controller revision":   func(f *podFixture) { f.workload.Status.UpdateRevision = "new" },
		"helper state":          func(f *podFixture) { f.expected.WorkloadKind = "copilot-helper" },
		"held expectation":      func(f *podFixture) { f.expected.ExecutionHeld = true },
		"missing sources":       func(f *podFixture) { f.expected.Sources = f.expected.Sources[:len(f.expected.Sources)-1] },
		"state owner":           func(f *podFixture) { f.state.OwnerReferences[0].UID = "foreign" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newPodFixture(t)
			if response := f.handler.Validate(context.Background(), podRequest(t, f.pod, admissionv1.Create, nil)); !response.Allowed {
				t.Fatalf("test starts without valid authority: %+v", response.Result)
			}
			before := make([]client.Object, len(f.objects))
			for i, object := range f.objects {
				before[i] = object.DeepCopyObject().(client.Object)
			}
			change(&f)
			for i, object := range f.objects {
				if equality.Semantic.DeepEqual(before[i], object) {
					continue
				}
				if err := f.client.Update(context.Background(), object); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := json.Marshal(f.expected)
			if err != nil {
				t.Fatal(err)
			}
			f.state.Data[PodExpectationKey] = string(raw)
			if err := f.client.Update(context.Background(), f.state); err != nil {
				t.Fatal(err)
			}
			if response := f.handler.Validate(context.Background(), podRequest(t, f.pod, admissionv1.Create, nil)); response.Allowed {
				t.Fatal("stale, held or spoofed controller state accepted")
			}
		})
	}
}

func TestPodAdmissionUpdateAndExtraContainerBoundary(t *testing.T) {
	f := newPodFixture(t)
	f.pod.UID, f.pod.ResourceVersion, f.pod.Spec.NodeName = "pod-uid", "", "scheduled-node"
	if err := f.client.Create(context.Background(), f.pod); err != nil {
		t.Fatal(err)
	}
	current := &corev1.Pod{}
	if err := f.client.Get(context.Background(), client.ObjectKeyFromObject(f.pod), current); err != nil {
		t.Fatal(err)
	}
	current.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
	request := podRequest(t, current.DeepCopy(), admissionv1.Update, current)
	if response := f.handler.Validate(context.Background(), request); !response.Allowed {
		t.Fatalf("unchanged scheduled Pod update denied: %+v", response.Result)
	}
	changed := current.DeepCopy()
	changed.Spec.NodeName = "injected-node"
	if response := f.handler.Validate(context.Background(), podRequest(t, changed, admissionv1.Update, current)); response.Allowed {
		t.Fatal("an update changed authoritative placement")
	}
	changed = current.DeepCopy()
	changed.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: "example.com/debug:v1"}}}
	request = podRequest(t, changed, admissionv1.Update, current)
	request.SubResource = "ephemeralcontainers"
	if response := f.handler.Validate(context.Background(), request); response.Allowed {
		t.Fatal("ephemeral container injection accepted")
	}
}

type unavailablePodReader struct{ calls int }

func (r *unavailablePodReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	r.calls++
	return fmt.Errorf("synthetic API outage")
}
func (r *unavailablePodReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	r.calls++
	return fmt.Errorf("synthetic API outage")
}

func TestPodCleanupDoesNotReadDependencies(t *testing.T) {
	reader := &unavailablePodReader{}
	handler, err := NewPodHandler(reader, PodConfig{StateNamespace: "operator-state"})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []admission.Request{
		{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Delete}},
		{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Update, SubResource: "status"}},
	} {
		if response := handler.Validate(context.Background(), request); !response.Allowed {
			t.Fatal("Pod cleanup/status depended on the webhook's resource reader")
		}
	}
	if reader.calls != 0 {
		t.Fatal("cleanup/status consulted dependencies")
	}
}

func TestPodHTTPWebhookAndStrictDecoding(t *testing.T) {
	f := newPodFixture(t)
	request := podRequest(t, f.pod, admissionv1.Create, nil)
	review := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"}, Request: &request.AdmissionRequest,
	}
	body, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	httpRequest := httptest.NewRequest(http.MethodPost, PodValidationPath, bytes.NewReader(body))
	httpRequest.Header.Set("Content-Type", "application/json")
	(&admission.Webhook{Handler: f.handler}).ServeHTTP(recorder, httpRequest)
	var result admissionv1.AdmissionReview
	if json.Unmarshal(recorder.Body.Bytes(), &result) != nil || result.Response == nil || !result.Response.Allowed {
		t.Fatalf("real AdmissionReview handler failed: %s", recorder.Body.String())
	}
	request.Object.Raw = bytes.Replace(request.Object.Raw, []byte(`"spec":{`), []byte(`"spec":{"futurePrivilege":true,`), 1)
	if response := f.handler.Validate(context.Background(), request); response.Allowed {
		t.Fatal("unknown final Pod field was ignored")
	}
}
