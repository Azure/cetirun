// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func configureBootstrap(t *testing.T, f *preparationCase) {
	t.Helper()
	snapshot := f.source.configuration
	data, err := json.Marshal(controller.SandboxPlatformConfiguration{
		Version: "v1", Registrations: snapshot.Registrations, Platforms: snapshot.Platforms,
		RuntimeClasses: snapshot.RuntimeClasses, StorageClasses: snapshot.StorageClasses,
	})
	if err != nil {
		t.Fatal(err)
	}
	name := f.fixture.Agent.Name + "-platform"
	f.h.create(t, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: preparationStateNamespace},
		Data:       map[string]string{controller.SandboxPlatformConfigurationKey: string(data)},
	})
	preparation, err := controller.NewKubernetesSandboxPreparation(controller.SandboxSourcesConfig{
		Enabled: true, BootstrapOwnedPrerequisites: true, PlatformConfigMapName: name,
		HomeClaimName: "home", SkillsClaimName: "skills", Helpers: f.fixture.Input.Helpers,
		AdmissionServiceAccount: preparationAdmissionAccount,
	}, f.h.client, f.installer, preparationStateNamespace, f.h.chart.Metadata.Version)
	if err != nil {
		t.Fatal(err)
	}
	f.reconciler.SandboxPreparation = preparation
	f.reconciler.OperatorSAName = "cetirun-operator"
	f.reconciler.ChartRegistry = "registry.example.com"
}

// The API server, identities, admission and resource lifecycle here are real.
// Runtime/storage declarations remain synthetic; no execution or fencing is qualified.
func TestSandboxBootstrap(t *testing.T) {
	h := newPreparationHarness(t)
	for _, agentType := range []string{"openclaw", "hermes"} {
		t.Run(agentType, func(t *testing.T) {
			f := h.newCaseWithPrerequisites(t, agentType, false)
			configureBootstrap(t, f)
			if _, err := f.reconciler.Reconcile(h.ctx, f.request()); !apierrors.IsNotFound(err) {
				t.Fatal("bootstrap should wait for published skills:", err)
			}
			namespace := &corev1.Namespace{}
			h.get(t, client.ObjectKey{Name: f.fixture.Namespace.Name}, namespace)
			if namespace.Labels[w.ProtectedNamespaceLabel] != "true" || !slices.Contains(namespace.Finalizers, sandbox.BootstrapFinalizer) {
				t.Fatal("new namespace is not protected throughout bootstrap")
			}
			home := &corev1.PersistentVolumeClaim{}
			h.get(t, client.ObjectKey{Namespace: namespace.Name, Name: "home"}, home)
			secret := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}}
			h.get(t, client.ObjectKey{Namespace: namespace.Name, Name: f.fixture.Agent.Spec.GatewayTokenSecretRef}, secret)
			if home.Annotations[w.AgentUIDAnnotation] != string(f.fixture.Agent.UID) ||
				secret.Annotations[w.AgentUIDAnnotation] != string(f.fixture.Agent.UID) {
				t.Fatal("created storage or native credentials lack exact Agent ownership")
			}
			original := home.DeepCopy()
			f.h.create(t, f.fixture.ServiceAccount)
			f.fixture.Input.Skills.OwnerReferences = []metav1.OwnerReference{{
				APIVersion: api.GroupVersion.String(), Kind: "Team", Name: f.fixture.Team.Name, UID: f.fixture.Team.UID,
			}}
			h.create(t, f.fixture.Input.Skills)
			if _, err := f.reconciler.Reconcile(h.ctx, f.request()); !errors.Is(err, sandbox.ErrObservationUnavailable) {
				t.Fatal("real Kubernetes bootstrap bypassed missing writer evidence:", err)
			}
			before := h.webhookCalls.Load()
			probe := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-probe", Namespace: namespace.Name},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					Containers:                   []corev1.Container{{Name: "probe", Image: "registry.example.com/unused:v1"}},
				},
			}
			if err := wait.PollUntilContextTimeout(h.ctx, 50*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
				err := h.client.Create(ctx, probe.DeepCopy(), client.DryRunAll)
				return err != nil && h.webhookCalls.Load() > before, nil
			}); err != nil {
				t.Fatal("protected namespace did not reject a pre-start Pod through admission:", err)
			}
			h.assertUnselectedNamespace(t)
			preparation := f.reconciler.SandboxPreparation
			f.restart()
			f.reconciler.SandboxPreparation = preparation
			f.reconciler.OperatorSAName = "cetirun-operator"
			f.reconciler.ChartRegistry = "registry.example.com"
			if _, err := f.reconciler.Reconcile(h.ctx, f.request()); !errors.Is(err, sandbox.ErrObservationUnavailable) {
				t.Fatal("restart failed to recover bootstrapped inputs:", err)
			}
			h.get(t, client.ObjectKeyFromObject(home), home)
			if home.UID != original.UID || home.ResourceVersion != original.ResourceVersion {
				t.Fatal("restart replaced or rewrote HOME")
			}
			deleteBootstrapCase(t, f)
		})
	}
}

func deleteBootstrapCase(t *testing.T, f *preparationCase) {
	t.Helper()
	h := f.h
	if err := h.client.Delete(h.ctx, f.agent(t)); err != nil {
		t.Fatal(err)
	}
	f.reconciler.SandboxPreparation = nil
	if _, err := f.reconciler.Reconcile(h.ctx, f.request()); err != nil {
		t.Fatal("bootstrap-only deletion required missing sources or fencing:", err)
	}
	namespace := &corev1.Namespace{}
	h.get(t, client.ObjectKey{Name: f.fixture.Namespace.Name}, namespace)
	if namespace.DeletionTimestamp.IsZero() || slices.Contains(namespace.Finalizers, sandbox.BootstrapFinalizer) {
		t.Fatal("cleanup did not authorize the exact namespace for deletion")
	}
	// envtest has no namespace controller. Complete only its native finalization
	// after the tested reconciler has released its own bootstrap protection.
	namespace.Spec.Finalizers = nil
	if _, err := h.kubernetes.CoreV1().Namespaces().Finalize(h.ctx, namespace, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconciler.Reconcile(h.ctx, f.request()); err != nil {
		t.Fatal("bootstrap finalizer cleanup did not complete:", err)
	}
	if err := h.client.Get(h.ctx, client.ObjectKeyFromObject(f.fixture.Agent), &api.Agent{}); !apierrors.IsNotFound(err) {
		t.Fatal("Agent still has bootstrap ownership obligations:", err)
	}
}

type bootstrapNamespaceRaceClient struct {
	client.Client
	beforeRole func() error
}

func (c *bootstrapNamespaceRaceClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if _, ok := object.(*rbacv1.Role); ok && c.beforeRole != nil {
		if err := c.beforeRole(); err != nil {
			return err
		}
		c.beforeRole = nil
	}
	return c.Client.Create(ctx, object, options...)
}

func TestSandboxBootstrapNamespaceLifetime(t *testing.T) {
	h := newPreparationHarness(t)
	f := h.newCaseWithPrerequisites(t, "hermes", false)
	configureBootstrap(t, f)
	var namespace corev1.Namespace
	var attempted bool
	f.reconciler.Client = &bootstrapNamespaceRaceClient{
		Client: h.client,
		beforeRole: func() error {
			attempted = true
			h.get(t, client.ObjectKey{Name: f.fixture.Namespace.Name}, &namespace)
			if err := h.client.Delete(h.ctx, &namespace); err != nil {
				return err
			}
			err := h.client.Create(h.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace.Name}})
			if !apierrors.IsAlreadyExists(err) {
				t.Fatal("namespace lifetime protection allowed replacement between check and create:", err)
			}
			return nil
		},
	}
	if _, err := f.reconciler.Reconcile(h.ctx, f.request()); err == nil || !attempted {
		t.Fatal("namespace termination did not stop the checked child create:", err)
	}
	current := &corev1.Namespace{}
	h.get(t, client.ObjectKey{Name: namespace.Name}, current)
	if current.UID != namespace.UID || current.DeletionTimestamp.IsZero() ||
		!slices.Contains(current.Finalizers, sandbox.BootstrapFinalizer) {
		t.Fatal("interrupted bootstrap lost namespace identity protection")
	}
	deleteBootstrapCase(t, f)
}
