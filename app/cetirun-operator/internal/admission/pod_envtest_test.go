// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package sandboxadmission

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	"helm.sh/helm/v3/pkg/chart/loader"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestPodAdmissionAPIServerBoundaries(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	for _, name := range []string{"kube-apiserver", "etcd"} {
		if info, err := os.Stat(filepath.Join(assets, name)); assets == "" || err != nil || info.Mode()&0111 == 0 {
			t.Fatal("preinstalled Kubernetes envtest assets are required")
		}
	}
	env := &envtest.Environment{
		BinaryAssetsDirectory: assets, UseExistingCluster: ptr.To(false),
		CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true,
		ControlPlaneStartTimeout: time.Minute, ControlPlaneStopTimeout: 30 * time.Second,
	}
	cfg, err := env.Start()
	if err != nil {
		_ = env.Stop()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	f := newPodFixture(t)
	if err := admissionregistrationv1.AddToScheme(f.scheme); err != nil {
		t.Fatal(err)
	}
	cl, err := client.New(cfg, client.Options{Scheme: f.scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	create := func(object client.Object) {
		t.Helper()
		object.SetUID("")
		object.SetResourceVersion("")
		object.SetGeneration(0)
		if err := cl.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	create(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator-state"}})
	f.fixture.Namespace.Labels, f.fixture.Namespace.Annotations, f.fixture.Namespace.OwnerReferences = nil, nil, nil
	create(f.fixture.Namespace)
	create(f.fixture.Sandbox)
	create(f.fixture.Policy)
	create(f.fixture.Team)
	create(f.fixture.Agent)
	f.fixture.Namespace.Labels = map[string]string{w.ProtectedNamespaceLabel: "true"}
	f.fixture.Namespace.Annotations = map[string]string{
		w.AgentNameAnnotation: f.fixture.Agent.Name, w.AgentUIDAnnotation: string(f.fixture.Agent.UID),
	}
	f.fixture.Namespace.OwnerReferences = []metav1.OwnerReference{w.AgentOwner(f.fixture.Agent.Name, f.fixture.Agent.UID)}
	if err := cl.Update(ctx, f.fixture.Namespace); err != nil {
		t.Fatal(err)
	}
	create(f.fixture.ServiceAccount)
	create(f.fixture.Input.RuntimeClass)
	f.fixture.Input.Home.Annotations[w.AgentUIDAnnotation] = string(f.fixture.Agent.UID)
	create(f.fixture.Input.Home)
	f.fixture.Input.Skills.Spec = corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, StorageClassName: ptr.To("example-csi"),
		Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
	}
	create(f.fixture.Input.Skills)
	create(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "example-config", Namespace: f.fixture.Namespace.Name}})
	create(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "runtime-secret", Namespace: f.fixture.Namespace.Name}})
	resources := sandbox.Resources{
		Sandboxes:     map[string]*api.Sandbox{f.fixture.Sandbox.Name: f.fixture.Sandbox},
		AgentPolicies: map[string]*api.AgentPolicy{f.fixture.Policy.Name: f.fixture.Policy},
	}
	record, err := sandboxbinding.Snapshot(f.fixture.Agent, f.fixture.Team, resources)
	if err != nil {
		t.Fatal(err)
	}
	f.fixture.Agent.Annotations[sandboxbinding.AuthorizationAnnotation] = record
	if err := cl.Update(ctx, f.fixture.Agent); err != nil {
		t.Fatal(err)
	}
	binding, err := sandboxbinding.AuthorizedBinding(f.fixture.Agent)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := sandbox.Resolve(sandbox.ResolveInput{
		Agent: f.fixture.Agent, Team: f.fixture.Team, Resources: resources, AuthorizedBinding: binding,
		Registrations: map[api.SandboxType]sandbox.Registration{api.SandboxKata: {
			Capabilities: f.fixture.Input.Resolution.Capabilities, Available: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := f.fixture.Input
	input.Resolution, input.AgentGeneration, input.Namespace.UID = resolution, f.fixture.Agent.Generation, f.fixture.Namespace.UID
	input.Controls.Binding, input.Controls.ResolutionRevision = binding, resolution.Revision
	bindingJSON, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	for key := range input.Controls.PodLabels {
		input.Controls.PodLabels[key] = string(binding.AgentUID)
	}
	for i := range input.Controls.Objects {
		object := &input.Controls.Objects[i]
		object.SetOwnerReferences([]metav1.OwnerReference{w.AgentOwner(input.AgentName, binding.AgentUID)})
		object.SetLabels(map[string]string{sandboxpolicy.AgentUIDLabel: string(binding.AgentUID)})
		annotations := object.GetAnnotations()
		annotations[sandboxpolicy.BindingAnnotation] = string(bindingJSON)
		object.SetAnnotations(annotations)
		if object.GetKind() == "CiliumNetworkPolicy" {
			object.Object["spec"].(map[string]any)["endpointSelector"] = map[string]any{"matchLabels": map[string]any{
				"k8s:" + sandboxpolicy.AgentUIDLabel: string(binding.AgentUID), "k8s:io.kubernetes.pod.namespace": input.Namespace.Name,
			}}
		}
	}
	bundle, err := w.Build(input)
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
	workload := testfixture.Workload(input, *template)
	create(workload)
	workload.Status.UpdateRevision = "example-revision"
	if err := cl.Status().Update(ctx, workload); err != nil {
		t.Fatal(err)
	}
	expected, err := bundle.Expect(*template, workload, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected.ExecutionHeld, expected.ControllerRevision = false, workload.Status.UpdateRevision
	// Refresh each actual source UID/RV; no credentials or external cluster are
	// read. The API client requests metadata only, including Secret metadata.
	for _, source := range f.expected.Sources {
		object := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: source.APIVersion, Kind: source.Kind}}
		if err := cl.Get(ctx, client.ObjectKey{Namespace: source.Namespace, Name: source.Name}, object); err != nil {
			t.Fatal(err)
		}
		source.UID, source.ResourceVersion = object.UID, object.ResourceVersion
		expected.Sources = append(expected.Sources, source)
	}
	state, err := PodExpectationObject("operator-state", expected)
	if err != nil {
		t.Fatal(err)
	}
	create(state)
	handler, err := NewPodHandler(cl, PodConfig{StateNamespace: "operator-state"})
	if err != nil {
		t.Fatal(err)
	}
	var unavailable atomic.Bool
	var calls atomic.Int64
	webhook := &admission.Webhook{Handler: handler}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if unavailable.Load() {
			http.Error(w, "synthetic outage", http.StatusServiceUnavailable)
			return
		}
		webhook.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	url := server.URL + PodValidationPath
	rules := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "sandbox-pod-test"},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name: "sandbox-pods.example.com", AdmissionReviewVersions: []string{"v1"},
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL: &url, CABundle: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}),
			},
			FailurePolicy: ptr.To(admissionregistrationv1.Fail), SideEffects: ptr.To(admissionregistrationv1.SideEffectClassNone),
			TimeoutSeconds:  ptr.To(int32(2)),
			MatchConditions: []admissionregistrationv1.MatchCondition{{Name: "test-workload", Expression: "request.namespace == 'cetirun-example'"}},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
				Rule:       admissionregistrationv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods", "pods/ephemeralcontainers", "pods/resize"}},
			}},
		}},
	}
	create(rules)
	var probeError error
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		probe := testfixture.Pod(expected)
		probe.Spec.Overhead = nil
		probe.Annotations[w.KindAnnotation] = "unapproved"
		err := cl.Create(ctx, probe)
		probeError = err
		if err == nil {
			return false, cl.Delete(ctx, probe, client.GracePeriodSeconds(0))
		}
		return calls.Load() > 0 && strings.Contains(err.Error(), "denied"), nil
	}); err != nil {
		t.Fatalf("webhook did not reject probe: %v; calls=%d; last=%v", err, calls.Load(), probeError)
	}
	foreignDNS := testfixture.Pod(expected)
	foreignDNS.Spec.Overhead = nil
	foreignDNS.Spec.DNSConfig.Nameservers = []string{"192.0.2.53"}
	beforeDNS := calls.Load()
	if err := cl.Create(ctx, foreignDNS); err == nil || !strings.Contains(err.Error(), "denied") || calls.Load() <= beforeDNS {
		t.Fatalf("final-Pod webhook did not reject a schema-valid DNS substitution: %v", err)
	}
	pod := testfixture.Pod(expected)
	pod.Spec.Overhead = nil // RuntimeClass admission supplies the registered overhead.
	if err := cl.Create(ctx, pod); err != nil {
		t.Fatalf("API-defaulted protected Pod was rejected: %v", err)
	}
	if pod.Spec.DNSPolicy != corev1.DNSNone || pod.Spec.DNSConfig == nil ||
		len(pod.Spec.DNSConfig.Nameservers) != 1 || pod.Spec.DNSConfig.Nameservers[0] != "10.96.0.10" {
		t.Fatal("API defaulting replaced the protected DNS configuration")
	}
	helper := testfixture.Pod(expected)
	helper.Name, helper.Spec.Overhead = "copilot-helper", nil
	helper.Annotations[w.KindAnnotation] = "copilot-helper"
	if err := cl.Create(ctx, helper); err == nil {
		t.Fatal("undeclared helper bypassed final-Pod admission")
	}
	unavailable.Store(true)
	rejected := testfixture.Pod(expected)
	rejected.Name, rejected.Spec.Overhead = "outage-probe", nil
	if err := cl.Create(ctx, rejected); err == nil || apierrors.IsAlreadyExists(err) {
		t.Fatalf("protected creation did not fail closed during outage: %v", err)
	}
	before := calls.Load()
	pod.Status.Phase = corev1.PodPending
	if err := cl.Status().Update(ctx, pod); err != nil {
		t.Fatalf("Pod status depended on unavailable webhook: %v", err)
	}
	if err := cl.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil {
		t.Fatalf("Pod deletion depended on unavailable webhook: %v", err)
	}
	if calls.Load() != before {
		t.Fatal("status/delete reached protected-write webhook")
	}
	unavailable.Store(false)
	if err := cl.Delete(ctx, state); err != nil {
		t.Fatal(err)
	}
	missing := testfixture.Pod(expected)
	missing.Spec.Overhead = nil
	if err := cl.Create(ctx, missing); err == nil {
		t.Fatal("missing persisted expectations allowed creation")
	}
}
