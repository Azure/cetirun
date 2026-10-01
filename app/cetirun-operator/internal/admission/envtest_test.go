// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package sandboxadmission

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestAdmissionAPIServerBoundaries(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	for _, binary := range []string{"kube-apiserver", "etcd"} {
		info, err := os.Stat(filepath.Join(assets, binary))
		if assets == "" || err != nil || info.IsDir() || info.Mode()&0111 == 0 {
			t.Fatal("set KUBEBUILDER_ASSETS to preinstalled Kubernetes 1.37 envtest binaries")
		}
	}
	env := &envtest.Environment{
		BinaryAssetsDirectory: assets, UseExistingCluster: ptr.To(false),
		CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true,
		ControlPlaneStartTimeout: 45 * time.Second, ControlPlaneStopTimeout: 30 * time.Second,
	}
	env.ControlPlane.GetAPIServer().Configure().Set("authorization-mode", "RBAC")
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
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, admissionregistrationv1.AddToScheme, apiextensionsv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	newClient := func(config *rest.Config) client.Client {
		result, err := client.New(config, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	cl := newClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	create := func(object client.Object) {
		t.Helper()
		if err := cl.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	userClient := func(name string, status bool) client.Client {
		user, err := env.AddUser(envtest.User{Name: name}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		role := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "admission-test-"},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{api.GroupVersion.Group}, Resources: []string{"agents", "teams", "sandboxes", "agentpolicies"},
				Verbs: []string{"get", "list", "create", "update", "patch", "delete"},
			}},
		}
		if status {
			role.Rules = append(role.Rules, rbacv1.PolicyRule{
				APIGroups: []string{api.GroupVersion.Group}, Resources: []string{"agents/status"}, Verbs: []string{"get", "update", "patch"},
			})
		}
		create(role)
		create(&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: role.Name},
			Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: name}},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
		})
		return newClient(user.Config())
	}
	member, controller := userClient("member-test", false), userClient(testController, true)
	f := newFixture(t)
	for _, object := range []client.Object{f.box, f.policy, f.team} {
		object.SetUID("")
		object.SetResourceVersion("")
		create(object)
	}
	t.Run("schema defaults and CEL are enforced by the API server", func(t *testing.T) {
		if f.box.Spec.Type != api.SandboxKata || f.policy.Spec.Security.ReadOnlyRootFilesystem == nil ||
			!*f.policy.Spec.Security.ReadOnlyRootFilesystem || f.policy.Spec.Network.Ingress.Mode != api.AgentPolicyIngressAuthenticated {
			t.Fatal("Sandbox/AgentPolicy schema defaults were not persisted")
		}
		box := f.box.DeepCopy()
		box.ObjectMeta = metav1.ObjectMeta{Name: "invalid-resource-budget"}
		box.Spec.Resources.Agent.Requests[corev1.ResourceCPU] = resource.MustParse("3")
		if err := cl.Create(ctx, box); !apierrors.IsInvalid(err) {
			t.Fatalf("request above limit was not rejected by CEL: %v", err)
		}
		policy := &api.AgentPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "explicit-false"},
			Spec: api.AgentPolicySpec{
				Security: api.AgentPolicySecurity{ReadOnlyRootFilesystem: ptr.To(false)},
				Network:  api.AgentPolicyNetwork{BlockedPorts: []api.AgentPolicyPort{{Port: 25}}},
			},
		}
		create(policy)
		if policy.Spec.Security.ReadOnlyRootFilesystem == nil || *policy.Spec.Security.ReadOnlyRootFilesystem ||
			policy.Spec.Network.BlockedPorts[0].Protocol != corev1.ProtocolTCP {
			t.Fatal("explicit false or nested protocol default was lost")
		}
	})
	config := testConfig()
	config.AdminGroups = []string{"system:masters"}
	handler, err := NewHandler(cl, config)
	if err != nil {
		t.Fatal(err)
	}
	handler.registrations = f.handler.registrations
	mux := http.NewServeMux()
	mux.Handle(ValidationPath, &admission.Webhook{Handler: admission.HandlerFunc(handler.Validate)})
	mux.Handle(DefaultingPath, &admission.Webhook{Handler: admission.HandlerFunc(handler.DefaultAgent)})
	var unavailable atomic.Bool
	var calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if unavailable.Load() {
			http.Error(w, "synthetic admission outage", http.StatusServiceUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	validating, mutating := renderedWebhooks(t, ca)
	for i := range validating.Webhooks {
		hook := &validating.Webhooks[i]
		url := server.URL + *hook.ClientConfig.Service.Path
		hook.ClientConfig.Service, hook.ClientConfig.URL = nil, &url
	}
	for i := range mutating.Webhooks {
		hook := &mutating.Webhooks[i]
		url := server.URL + *hook.ClientConfig.Service.Path
		hook.ClientConfig.Service, hook.ClientConfig.URL = nil, &url
	}
	create(validating)
	create(mutating)

	newAgent := func(name string, governed bool) *api.Agent {
		agent := f.agent.DeepCopy()
		agent.ObjectMeta = metav1.ObjectMeta{Name: name, Finalizers: []string{api.Finalizer}}
		agent.Spec.SandboxRef, agent.Spec.PolicyClass = "", ""
		if !governed {
			agent.Spec.Team = ""
		}
		return agent
	}
	if err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		probe := newAgent("admission-ready-probe", true)
		err := cl.Create(ctx, probe, client.DryRunAll)
		return err == nil && probe.Spec.SandboxRef == f.box.Name, nil
	}); err != nil {
		t.Fatal("mutating/validating admission did not become active:", err)
	}

	t.Run("persist defaults without elevating a human administrator", func(t *testing.T) {
		agent := newAgent("governed-agent", true)
		create(agent)
		var stored api.Agent
		if err := cl.Get(ctx, client.ObjectKeyFromObject(agent), &stored); err != nil {
			t.Fatal(err)
		}
		if stored.Spec.SandboxRef != f.box.Name || stored.Spec.PolicyClass != api.AgentPolicyClassNormal {
			t.Fatal("creation defaults were not persisted, or human authority elevated the workload")
		}
	})
	t.Run("generated names pass mutation and validation before assignment", func(t *testing.T) {
		for _, governed := range []bool{false, true} {
			agent := newAgent("", governed)
			agent.GenerateName = "generated-agent-"
			writer := member
			if governed {
				writer = cl
			}
			if err := writer.Create(ctx, agent); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(agent.Name, agent.GenerateName) {
				t.Fatalf("API server did not assign the requested generated name: %q", agent.Name)
			}
			if governed && (agent.Spec.SandboxRef != f.box.Name || agent.Spec.PolicyClass != api.AgentPolicyClassNormal) {
				t.Fatal("governed generated-name Agent did not persist creation defaults")
			}
			if !governed && (agent.Spec.Team != "" || agent.Spec.SandboxRef != "" ||
				agent.Spec.PolicyClass != api.AgentPolicyClassNormal) {
				t.Fatal("legacy generated-name Agent did not preserve the schema-only normal policy default")
			}
		}
	})
	t.Run("dry run performs defaulting and validation without persistence", func(t *testing.T) {
		agent := newAgent("dry-run-governed", true)
		if err := cl.Create(ctx, agent, client.DryRunAll); err != nil {
			t.Fatal(err)
		}
		if agent.Spec.SandboxRef != f.box.Name || agent.Spec.PolicyClass != api.AgentPolicyClassNormal {
			t.Fatal("dry run did not apply normal creation defaults")
		}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(agent), &api.Agent{}); !apierrors.IsNotFound(err) {
			t.Fatalf("dry-run Agent persisted: %v", err)
		}
	})
	t.Run("legacy Team deletion preserves member-authorized behavior", func(t *testing.T) {
		team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: "legacy-delete-team"}}
		if err := member.Create(ctx, team); err != nil {
			t.Fatal(err)
		}
		agent := newAgent("legacy-team-member", false)
		agent.Spec.Team = team.Name
		if err := member.Create(ctx, agent); err != nil {
			t.Fatal(err)
		}
		if err := member.Delete(ctx, team); err != nil {
			t.Fatal("legacy Team deletion gained Sandbox authority or consumer requirements:", err)
		}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(agent), &api.Agent{}); err != nil {
			t.Fatal("legacy Team deletion removed its Agent:", err)
		}
	})
	t.Run("member cannot create governance or write status despite broad spec RBAC", func(t *testing.T) {
		agent := newAgent("member-governed", true)
		if err := member.Create(ctx, agent); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "administrator") {
			t.Fatalf("expected webhook authority denial, got %v", err)
		}
		legacy := newAgent("member-legacy", false)
		if err := member.Create(ctx, legacy); err != nil {
			t.Fatal(err)
		}
		legacy.Status.Phase = api.PhaseReady
		if err := member.Status().Update(ctx, legacy); !apierrors.IsForbidden(err) {
			t.Fatalf("member status must be blocked by RBAC: %v", err)
		}
	})
	t.Run("unknown fields are explicitly rejected", func(t *testing.T) {
		object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(newAgent("unknown-field", false))
		if err != nil {
			t.Fatal(err)
		}
		obj := &unstructured.Unstructured{Object: object}
		obj.SetAPIVersion(api.GroupVersion.String())
		obj.SetKind("Agent")
		if err := unstructured.SetNestedField(obj.Object, true, "spec", "unknownGovernance"); err != nil {
			t.Fatal(err)
		}
		if err := cl.Create(ctx, obj, client.FieldValidation(metav1.FieldValidationStrict)); !apierrors.IsBadRequest(err) {
			t.Fatalf("unknown field was not explicitly rejected: %v", err)
		}
	})
	t.Run("newer CRD fields fail explicitly while known writes remain available", func(t *testing.T) {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := cl.Get(ctx, client.ObjectKey{Name: "agentpolicies." + api.GroupVersion.Group}, &crd); err != nil {
			t.Fatal(err)
		}
		for i := range crd.Spec.Versions {
			schema := crd.Spec.Versions[i].Schema.OpenAPIV3Schema
			spec := schema.Properties["spec"]
			spec.Properties["futureGovernance"] = apiextensionsv1.JSONSchemaProps{Type: "boolean"}
			schema.Properties["spec"] = spec
		}
		if err := cl.Update(ctx, &crd); err != nil {
			t.Fatal(err)
		}
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": api.GroupVersion.String(), "kind": "AgentPolicy",
			"metadata": map[string]any{"name": "future-policy"},
			"spec":     map[string]any{"futureGovernance": true},
		}}
		if err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
			before := calls.Load()
			err := cl.Create(ctx, obj.DeepCopy(), client.DryRunAll, client.FieldValidation(metav1.FieldValidationStrict))
			return err != nil && calls.Load() > before && strings.Contains(err.Error(), `unknown field "futureGovernance"`), nil
		}); err != nil {
			t.Fatal("older admission did not explicitly reject a field admitted by the newer schema:", err)
		}
		known := &api.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: "known-during-skew"}}
		create(known)
		if err := cl.Delete(ctx, known); err != nil {
			t.Fatal("known-field objects must remain writable during skew:", err)
		}
	})
	t.Run("shared config deletion remains guarded", func(t *testing.T) {
		if err := cl.Delete(ctx, f.policy); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "referenced") {
			t.Fatalf("referenced policy deletion was not rejected: %v", err)
		}
	})
	t.Run("foreground deletion permits only garbage collector finalization", func(t *testing.T) {
		// Envtest has no garbage collector; authenticated clients issue its finalizer patch.
		for i, username := range []string{
			"system:kube-controller-manager",
			"system:serviceaccount:kube-system:generic-garbage-collector",
		} {
			collector := userClient(username, false)
			policy := &api.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("gc-policy-%d", i)}}
			if err := collector.Create(ctx, policy.DeepCopy()); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "administrator") {
				t.Fatalf("garbage collector acquired configuration administration: %v", err)
			}
			box := f.box.DeepCopy()
			box.ObjectMeta = metav1.ObjectMeta{Name: fmt.Sprintf("gc-sandbox-%d", i)}
			for _, object := range []client.Object{box, policy, newAgent(fmt.Sprintf("gc-agent-%d", i), true)} {
				t.Run(object.GetName(), func(t *testing.T) {
					if err := cl.Create(ctx, object); err != nil {
						t.Fatal(err)
					}
					key := client.ObjectKeyFromObject(object)
					if err := cl.Delete(ctx, object, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil {
						t.Fatal(err)
					}
					if err := cl.Get(ctx, key, object); err != nil {
						t.Fatal(err)
					}
					if object.GetDeletionTimestamp().IsZero() || !slices.Contains(object.GetFinalizers(), metav1.FinalizerDeleteDependents) {
						t.Fatal("foreground deletion did not persist the built-in finalizer")
					}
					candidate := object.DeepCopyObject().(client.Object)
					candidate.SetFinalizers(slices.DeleteFunc(candidate.GetFinalizers(), func(value string) bool {
						return value == metav1.FinalizerDeleteDependents
					}))
					patch := client.MergeFromWithOptions(object, client.MergeFromWithOptimisticLock{})
					if err := member.Patch(ctx, candidate, patch); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "administrator") {
						t.Fatalf("ordinary member could finalize governed configuration: %v", err)
					}
					if err := collector.Patch(ctx, candidate, patch); err != nil {
						t.Fatalf("garbage collector could not remove foregroundDeletion: %v", err)
					}
					if agent, ok := candidate.(*api.Agent); ok {
						if err := cl.Get(ctx, key, agent); err != nil {
							t.Fatal(err)
						}
						if !slices.Equal(agent.Finalizers, []string{api.Finalizer}) || agent.Spec.SandboxRef != f.box.Name {
							t.Fatal("garbage collector changed the Agent's application finalizer or Sandbox binding")
						}
						before := agent.DeepCopy()
						agent.Finalizers = nil
						if err := controller.Patch(ctx, agent, client.MergeFrom(before)); err != nil {
							t.Fatal("CetiRun controller could not complete application cleanup:", err)
						}
					}
					if err := cl.Get(ctx, key, object); !apierrors.IsNotFound(err) {
						t.Fatalf("foreground deletion did not complete: %v", err)
					}
				})
			}
		}
	})

	cleanup := newAgent("cleanup-agent", false)
	cleanup.Finalizers = append(cleanup.Finalizers, "example.com/another-controller")
	create(cleanup)
	cleanup.Finalizers = append(cleanup.Finalizers, sandbox.HoldFinalizer, sandbox.PreparationFinalizer)
	if err := controller.Update(ctx, cleanup); err != nil {
		t.Fatal("controller could not persist the initial hold:", err)
	}
	if err := cl.Delete(ctx, cleanup); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(cleanup), cleanup); err != nil {
		t.Fatal(err)
	}
	deleteDuringOutage := newAgent("delete-during-outage", false)
	create(deleteDuringOutage)
	create(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "default"}})
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "delete-pod-during-outage", Namespace: "default"},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: ptr.To(false),
			Containers:                   []corev1.Container{{Name: "example", Image: "registry.example.com/example:1"}},
		},
	}
	create(pod)
	unavailable.Store(true)

	t.Run("outage fails creates and protected writes closed", func(t *testing.T) {
		for _, operation := range []func() error{
			func() error { return cl.Create(ctx, newAgent("blocked-during-outage", false)) },
			func() error { return cl.Delete(ctx, f.policy) },
		} {
			if err := operation(); err == nil || !strings.Contains(err.Error(), "failed calling webhook") {
				t.Fatalf("expected fail-closed unavailable-webhook error: %v", err)
			}
		}
	})
	t.Run("outage allows authorized status and deletion without a webhook call", func(t *testing.T) {
		before := calls.Load()
		cleanup.Status.Phase = api.PhaseDeleting
		if err := controller.Status().Update(ctx, cleanup); err != nil {
			t.Fatal(err)
		}
		if err := cl.Delete(ctx, deleteDuringOutage); err != nil {
			t.Fatal(err)
		}
		if err := cl.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != before {
			t.Fatal("status or Agent/Pod deletion depended on the unavailable webhook")
		}
	})
	t.Run("cleanup predicate has no blanket controller or admin exemption", func(t *testing.T) {
		for _, edit := range []func(*api.Agent){
			func(a *api.Agent) { a.Spec.Model = "changed" },
			func(a *api.Agent) { a.Labels = map[string]string{"changed": "true"} },
			func(a *api.Agent) { a.Annotations = map[string]string{"changed": "true"} },
			func(a *api.Agent) {
				a.OwnerReferences = []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "Team", Name: f.team.Name, UID: f.team.UID}}
			},
			func(a *api.Agent) { a.Finalizers = nil },
		} {
			candidate := cleanup.DeepCopy()
			candidate.Finalizers = []string{"example.com/another-controller"}
			edit(candidate)
			if err := controller.Update(ctx, candidate); err == nil || !strings.Contains(err.Error(), "failed calling webhook") {
				t.Fatalf("unsafe controller update did not reach the unavailable webhook: %v", err)
			}
		}
		candidate := cleanup.DeepCopy()
		candidate.Finalizers = []string{"example.com/another-controller"}
		if err := cl.Update(ctx, candidate); err == nil || !strings.Contains(err.Error(), "failed calling webhook") {
			t.Fatalf("admin identity must not bypass cleanup matching: %v", err)
		}
	})
	t.Run("narrow controller finalizer cleanup survives outage", func(t *testing.T) {
		before := calls.Load()
		candidate := cleanup.DeepCopy()
		candidate.Finalizers = []string{"example.com/another-controller"}
		if err := controller.Update(ctx, candidate); err != nil {
			t.Fatal("API-server cleanup predicate did not preserve finalization:", err)
		}
		if calls.Load() != before {
			t.Fatal("cleanup called the unavailable webhook")
		}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(candidate), candidate); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(candidate.Finalizers, []string{"example.com/another-controller"}) {
			t.Fatal("foreign finalizer was not preserved")
		}
	})
	unavailable.Store(false)
	t.Run("requests recover after service restoration", func(t *testing.T) {
		create(newAgent("after-recovery", false))
	})
	t.Run("independent serving process and overlapping CA rotation", func(t *testing.T) {
		testServingProcess(t, cfg, newAgent("tls-process-probe", false))
	})
}

func renderedWebhooks(t *testing.T, ca []byte) (*admissionregistrationv1.ValidatingWebhookConfiguration, *admissionregistrationv1.MutatingWebhookConfiguration) {
	t.Helper()
	command := exec.Command("helm", "template", "admission-test", "../../chart", "--kube-version=1.37.0",
		"--set", "namespace=operator-test", "--set", "serviceAccount.name=controller",
		"--set", "sandboxAdmission.enabled=true", "--set", "sandboxAdmission.enforce=true",
		"--set", "sandboxAdmission.tlsSecretName=admission-test-tls", "--set", "sandboxAdmission.adminUsers[0]=administrator",
		"--set-string", "sandboxAdmission.caBundle="+base64.StdEncoding.EncodeToString(ca))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("render admission chart (use Helm 3): %v\n%s", err, output)
	}
	decoder := kyaml.NewYAMLOrJSONDecoder(strings.NewReader(string(output)), 4096)
	var validating *admissionregistrationv1.ValidatingWebhookConfiguration
	var mutating *admissionregistrationv1.MutatingWebhookConfiguration
	for {
		var object unstructured.Unstructured
		if err := decoder.Decode(&object); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		switch object.GetKind() {
		case "ValidatingWebhookConfiguration":
			validating = &admissionregistrationv1.ValidatingWebhookConfiguration{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, validating); err != nil {
				t.Fatal(err)
			}
		case "MutatingWebhookConfiguration":
			mutating = &admissionregistrationv1.MutatingWebhookConfiguration{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, mutating); err != nil {
				t.Fatal(err)
			}
		}
	}
	if validating == nil || mutating == nil || len(validating.Webhooks) == 0 || len(mutating.Webhooks) == 0 {
		t.Fatalf("chart omitted admission configurations: validating=%v mutating=%v", validating != nil, mutating != nil)
	}
	return validating, mutating
}

func testServingProcess(t *testing.T, restConfig *rest.Config, agent *api.Agent) {
	t.Helper()
	freePort := func() int {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		return port
	}
	directory := t.TempDir()
	first, second := newServingCertificate(t, 1), newServingCertificate(t, 2)
	first.write(t, directory)
	port, healthPort := freePort(), freePort()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, restConfig, testConfig(), ServerOptions{
			CertDirectory: directory, Host: "127.0.0.1", Port: port, MetricsAddress: "0",
			ProbeAddress:      fmt.Sprintf("127.0.0.1:%d", healthPort),
			PodStateNamespace: "operator-state",
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("admission server did not stop")
		}
	})
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(first.ca) || !roots.AppendCertsFromPEM(second.ca) {
		t.Fatal("invalid synthetic CA fixtures")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	httpClient := &http.Client{Transport: transport, Timeout: time.Second}
	probeRequest := requestFor(t, admissionv1.Create, agent, nil, "portal")
	review := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Request:  &probeRequest.AdmissionRequest,
	}
	body, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	var probeError error
	probe := func(serial *big.Int) bool {
		response, err := httpClient.Post(fmt.Sprintf("https://127.0.0.1:%d%s", port, ValidationPath),
			"application/json", bytes.NewReader(body))
		if err != nil {
			probeError = err
			return false
		}
		defer response.Body.Close()
		var result admissionv1.AdmissionReview
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			probeError = err
			return false
		}
		matches := result.Response != nil &&
			result.Response.Allowed && result.Response.UID == review.Request.UID &&
			response.TLS != nil && response.TLS.PeerCertificates[0].SerialNumber.Cmp(serial) == 0
		if !matches {
			probeError = fmt.Errorf("unexpected admission response: HTTP %d, %#v, peer serial %v, expected %v",
				response.StatusCode, result.Response, response.TLS.PeerCertificates[0].SerialNumber, serial)
		}
		return matches
	}
	if err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 10*time.Second, true, func(context.Context) (bool, error) {
		return probe(first.serial), nil
	}); err != nil {
		t.Fatalf("TLS admission process did not become responsive: %v; last probe: %v", err, probeError)
	}
	podReview := admissionv1.AdmissionReview{
		TypeMeta: review.TypeMeta,
		Request: &admissionv1.AdmissionRequest{
			UID: "pod-route-probe", Operation: admissionv1.Delete, Name: "unused", Namespace: "workload",
			Kind:     metav1.GroupVersionKind{Version: "v1", Kind: "Pod"},
			Resource: metav1.GroupVersionResource{Version: "v1", Resource: "pods"},
		},
	}
	podBody, err := json.Marshal(podReview)
	if err != nil {
		t.Fatal(err)
	}
	podResponse, err := httpClient.Post(fmt.Sprintf("https://127.0.0.1:%d%s", port, PodValidationPath),
		"application/json", bytes.NewReader(podBody))
	if err != nil {
		t.Fatal(err)
	}
	defer podResponse.Body.Close()
	var podResult admissionv1.AdmissionReview
	if err := json.NewDecoder(podResponse.Body).Decode(&podResult); err != nil ||
		podResponse.StatusCode != http.StatusOK || podResult.Response == nil ||
		!podResult.Response.Allowed || podResult.Response.UID != podReview.Request.UID {
		t.Fatalf("optional Pod endpoint was not served by the independent process: HTTP %d, %+v, %v",
			podResponse.StatusCode, podResult.Response, err)
	}
	healthClient := &http.Client{Timeout: time.Second}
	readiness := func() int {
		response, err := healthClient.Get(fmt.Sprintf("http://127.0.0.1:%d/readyz", healthPort))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if readiness() != http.StatusOK {
		t.Fatal("started TLS process was not ready")
	}
	second.write(t, directory)
	if err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 10*time.Second, true, func(context.Context) (bool, error) {
		return probe(second.serial), nil
	}); err != nil {
		t.Fatalf("serving certificate did not rotate with overlapping trust: %v; last probe: %v", err, probeError)
	}
	if readiness() != http.StatusOK {
		t.Fatal("rotated TLS process was not ready")
	}
	newRoots := x509.NewCertPool()
	if !newRoots.AppendCertsFromPEM(second.ca) {
		t.Fatal("invalid synthetic replacement CA")
	}
	replacementTransport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: newRoots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	t.Cleanup(replacementTransport.CloseIdleConnections)
	httpClient = &http.Client{Transport: replacementTransport, Timeout: time.Second}
	if !probe(second.serial) {
		t.Fatal("replacement certificate failed after retiring the old CA")
	}
	if err := os.WriteFile(filepath.Join(directory, "tls.key"), []byte("invalid serving material"), 0600); err != nil {
		t.Fatal(err)
	}
	if readiness() == http.StatusOK {
		t.Fatal("readiness accepted invalid on-disk serving material")
	}
	if probe(second.serial) {
		t.Fatal("a fresh TLS connection accepted invalid on-disk serving material")
	}
}
