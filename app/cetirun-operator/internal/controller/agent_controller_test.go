// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
)

// --- Fake HelmClient ---

type fakeHelmClient struct {
	installCalled       bool
	installCalls        int
	installErr          error
	lastChartVersion    string
	lastValues          map[string]interface{}
	useHarness          bool
	appliedKey          string
	prepareCalls        int
	prepareErr          error
	disableChatContract bool
}

func (f *fakeHelmClient) Prepare(namespace string, values map[string]any, chartVersion string) (*helm.PreparedRelease, error) {
	f.prepareCalls++
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	if f.useHarness {
		chrt, err := loader.Load("../../../cetirun-chart")
		if err != nil {
			return nil, err
		}
		chrt.Metadata.Version = chartVersion
		if f.disableChatContract {
			delete(chrt.Metadata.Annotations, "cetirun.microsoft.com/portal-chat")
		}
		return helm.PrepareChart(chrt, values)
	}
	return &helm.PreparedRelease{Values: values, ChartVersion: chartVersion}, nil
}

func (f *fakeHelmClient) Refresh(prepared *helm.PreparedRelease, values map[string]any) (*helm.PreparedRelease, error) {
	if f.useHarness {
		return prepared.Rebuild(values)
	}
	prepared.Values = values
	return prepared, nil
}

func (f *fakeHelmClient) InstallOrUpgrade(namespace, releaseName string, prepared *helm.PreparedRelease) error {
	f.installCalled = true
	f.installCalls++
	f.lastChartVersion = prepared.ChartVersion
	f.lastValues = prepared.Values
	if f.installErr == nil {
		f.appliedKey = prepared.CacheKey()
	}
	return f.installErr
}

func (f *fakeHelmClient) CustomApplied(namespace, releaseName string, prepared *helm.PreparedRelease) (bool, error) {
	return f.appliedKey != "" && f.appliedKey == prepared.CacheKey(), nil
}

func (f *fakeHelmClient) Uninstall(namespace, releaseName string) error {
	return nil
}

// --- Test helpers ---

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(cetirunv1alpha1.AddToScheme(s))
	utilruntime.Must(appsv1.AddToScheme(s))
	utilruntime.Must(istiov1beta1.AddToScheme(s))
	// corev1 and rbacv1 are registered by clientgoscheme.AddToScheme
	_ = rbacv1.AddToScheme(s)
	return s
}

// newCR builds a minimal Agent for tests. The finalizer is pre-set
// so the reconciler does not spend a loop adding it.
func newCR(alias string, phase string, conditions []metav1.Condition, generation int64, observedGen int64, observedHash string) *cetirunv1alpha1.Agent {
	cr := &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:       alias,
			UID:        types.UID("agent-" + alias),
			Generation: generation,
			Finalizers: []string{cetirunv1alpha1.Finalizer},
		},
		Spec: cetirunv1alpha1.AgentSpec{
			Agent: cetirunv1alpha1.HarnessSpec{Type: cetirunv1alpha1.AgentTypeOpenClaw},
			User: cetirunv1alpha1.UserSpec{
				Alias:       alias,
				DisplayName: "Test User",
				Team:        "test-team",
			},
			GatewayTokenSecretRef: "gateway-token",
			Helm: cetirunv1alpha1.HelmSpec{
				Version: "0.1.28", // Pre-set to avoid backfill loop in tests.
				Values:  map[string]runtime.RawExtension{},
			},
		},
		Status: cetirunv1alpha1.AgentStatus{
			Phase:                phase,
			Conditions:           conditions,
			ObservedGeneration:   observedGen,
			ObservedOperatorHash: observedHash,
		},
	}
	return cr
}

func gatewayTokenSecret(alias string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway-token",
			Namespace: "cetirun-" + alias,
		},
		Data: map[string][]byte{
			"OPENCLAW_GATEWAY_TOKEN": []byte("test-token"),
			"CETIRUN_AUTH_KEY":       []byte("test-auth-key"),
		},
	}
}

func instanceNamespace(alias string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cetirun-" + alias,
		},
	}
}

func statefulSet(alias string, readyReplicas int32) *appsv1.StatefulSet {
	return statefulSetFull(alias, readyReplicas, readyReplicas, 1, 1)
}

func statefulSetFull(alias string, readyReplicas, updatedReplicas int32, generation, observedGen int64) *appsv1.StatefulSet {
	replicas := int32(1)
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "openclaw-" + alias + "-cetirun",
			Namespace:  "cetirun-" + alias,
			Generation: generation,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
		},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas:      readyReplicas,
			UpdatedReplicas:    updatedReplicas,
			ObservedGeneration: observedGen,
		},
	}
}

func statefulSetSuspended(alias string) *appsv1.StatefulSet {
	replicas := int32(0)
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "openclaw-" + alias + "-cetirun",
			Namespace:  "cetirun-" + alias,
			Generation: 1,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
		},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas:      0,
			UpdatedReplicas:    0,
			ObservedGeneration: 1,
		},
	}
}

func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// --- Tests ---

// TestPhaseConstants verifies phase constant values.
func TestPhaseConstants(t *testing.T) {
	phases := []string{
		cetirunv1alpha1.PhasePending,
		cetirunv1alpha1.PhaseProvisioning,
		cetirunv1alpha1.PhaseReady,
		cetirunv1alpha1.PhaseFailed,
		cetirunv1alpha1.PhaseDeleting,
		cetirunv1alpha1.PhaseSuspended,
	}
	for _, p := range phases {
		if p == "" {
			t.Errorf("phase constant is empty")
		}
	}
}

// TestFinalizerConstant verifies the stable finalizer name.
func TestFinalizerConstant(t *testing.T) {
	if cetirunv1alpha1.Finalizer != "cetirun.microsoft.com/finalizer" {
		t.Errorf("unexpected finalizer: %s", cetirunv1alpha1.Finalizer)
	}
}

func TestReconcile_RejectsReservedUIInstance(t *testing.T) {
	alias := "ui"
	cr := newCR(alias, cetirunv1alpha1.PhaseReady, nil, 1, 1, "")
	vs := &istiov1beta1.VirtualService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cetirun-ui",
			Namespace: "cetirun-ui",
		},
	}

	s := testScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(cr, vs).
		WithStatusSubresource(cr).
		Build()
	helmFake := &fakeHelmClient{}
	r := &controller.AgentReconciler{
		Client:            fakeClient,
		Scheme:            s,
		EventRecorder:     record.NewFakeRecorder(10),
		HelmClient:        helmFake,
		OperatorNamespace: "cetirun-system",
		OperatorSAName:    "cetirun-operator-controller-manager",
		ChartVersion:      "0.1.28",
		DomainSuffix:      "example.com",
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: alias}}); err == nil {
		t.Fatal("expected reconcile to reject the reserved instance name")
	}
	if helmFake.installCalled {
		t.Fatal("reserved instance must not run Helm deployment")
	}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "cetirun-ui", Namespace: "cetirun-ui"}, &istiov1beta1.VirtualService{}); err == nil {
		t.Fatal("VirtualService for reserved instance was not deleted")
	}

	updated := &cetirunv1alpha1.Agent{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: alias}, updated); err != nil {
		t.Fatalf("get reserved instance: %v", err)
	}
	if updated.Status.Phase != cetirunv1alpha1.PhaseFailed {
		t.Fatalf("phase = %q, want Failed", updated.Status.Phase)
	}
}

// computeOperatorHash reproduces the operatorConfigHash logic for test seeding.
// The azureTeamsConfig variadic matches the operator's configHash method, which
// appends Azure config (subscriptionID, resourceGroup, botNamePrefix, tenantID,
// oidcIssuerURL) followed by the referenced Team's ResourceVersion to
// the hash so Teams-enabled instances and team-referenced instances can use the
// skip-cache.
func computeOperatorHash(domainSuffix, secretHash, skillsStorageAccount, cetirunHelmValuesHash, chartRegistry string, azureTeamsConfig ...string) string {
	var payload string
	if skillsStorageAccount == "" && cetirunHelmValuesHash == "" && chartRegistry == "" && len(azureTeamsConfig) == 0 {
		payload = domainSuffix + "\x00" + secretHash
	} else {
		payload = domainSuffix + "\x00" + secretHash + "\x00" + skillsStorageAccount + "\x00" + cetirunHelmValuesHash + "\x00" + chartRegistry
		for _, v := range azureTeamsConfig {
			payload += "\x00" + v
		}
	}
	h := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(h[:8])
}

// computeSecretHash reproduces the secretContentHash logic for test seeding.
func computeSecretHash(data map[string][]byte) string {
	if len(data) == 0 {
		return ""
	}
	h := sha256.New()
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write(data[k])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// TestConditionTailscaleReady verifies the TailscaleReady condition constant.
func TestConditionTailscaleReady(t *testing.T) {
	if cetirunv1alpha1.ConditionTailscaleReady != "TailscaleReady" {
		t.Errorf("unexpected condition: %s", cetirunv1alpha1.ConditionTailscaleReady)
	}
}

// TestTailscaleSpec_TypeConstruction verifies CRD struct with Tailscale fields.
func TestTailscaleSpec_TypeConstruction(t *testing.T) {
	instance := &cetirunv1alpha1.Agent{
		Spec: cetirunv1alpha1.AgentSpec{
			Team: "example-team",
			User: cetirunv1alpha1.UserSpec{
				Alias:       "alice",
				DisplayName: "Alice Smith",
			},
			GatewayTokenSecretRef: "gateway-token",
			Tailscale: &cetirunv1alpha1.TailscaleSpec{
				AuthKeySecretRef: "tailscale-auth",
			},
		},
	}

	if instance.Spec.Tailscale == nil {
		t.Fatal("Tailscale spec should not be nil")
	}
	if instance.Spec.Tailscale.AuthKeySecretRef != "tailscale-auth" {
		t.Errorf("AuthKeySecretRef = %q, want %q", instance.Spec.Tailscale.AuthKeySecretRef, "tailscale-auth")
	}

	// Verify nil Tailscale is valid (feature disabled).
	instanceNoTS := &cetirunv1alpha1.Agent{
		Spec: cetirunv1alpha1.AgentSpec{
			Team: "example-team",
			User: cetirunv1alpha1.UserSpec{
				Alias:       "bob",
				DisplayName: "Bob",
			},
			GatewayTokenSecretRef: "gateway-token",
		},
	}
	if instanceNoTS.Spec.Tailscale != nil {
		t.Error("Tailscale spec should be nil when not configured")
	}
}

// staleAgentClient serves an Agent as the informer can during a pass that
// starts before the previous pass's status arrived: an older revision.
type staleAgentClient struct {
	client.Client
	stale *cetirunv1alpha1.Agent
}

func (c staleAgentClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if agent, ok := obj.(*cetirunv1alpha1.Agent); ok && key.Name == c.stale.Name {
		c.stale.DeepCopyInto(agent)
		return nil
	}
	return c.Client.Get(ctx, key, obj, opts...)
}
