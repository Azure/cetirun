// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/ingressmtls"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	istio "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestIngressMTLSPendingApprovalBootstrapsOnlyOwnedPrerequisites(t *testing.T) {
	ctx := context.Background()
	f, _ := newUnreconciledSandboxRuntimeFixture(t, true, nil, true, "openclaw", true, false,
		withIngressMTLS(t, "openclaw"),
		func(r *AgentReconciler, kube client.Client) {
			cm := &corev1.ConfigMap{}
			if err := kube.Get(ctx, client.ObjectKey{Namespace: r.OperatorNamespace, Name: "sandbox-platform"}, cm); err != nil {
				t.Fatal(err)
			}
			var config SandboxPlatformConfiguration
			if err := json.Unmarshal([]byte(cm.Data[SandboxPlatformConfigurationKey]), &config); err != nil {
				t.Fatal(err)
			}
			registration := config.Platforms[api.SandboxKata].IngressMTLS
			registration.Bindings = registration.Bindings[:0]
			raw, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			cm.Data[SandboxPlatformConfigurationKey] = string(raw)
			if err := kube.Update(ctx, cm); err != nil {
				t.Fatal(err)
			}
		})
	namespace := &corev1.Namespace{}
	var lastErr error
	for range 3 {
		_, lastErr = f.reconcile()
		if err := f.kube.Get(ctx, client.ObjectKey{Name: instanceNamespace(f.agent.Name)}, namespace); err == nil {
			break
		}
	}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: instanceNamespace(f.agent.Name)}, namespace); err != nil {
		t.Fatal("pending enrollment deadlocked owned namespace bootstrap:", err, lastErr)
	}
	if err := f.r.validateSandboxBootstrapNamespace(ctx, f.current(t), namespace); err != nil {
		t.Fatal("pending enrollment weakened namespace identity:", err)
	}
	if err := f.kube.Get(ctx, client.ObjectKey{Namespace: namespace.Name, Name: instanceServiceName(f.agent.Name)}, &appsv1.StatefulSet{}); !apierrors.IsNotFound(err) {
		t.Fatal("pending certificate enrollment created compute:", err)
	}
	f.assertNotReady(t)
}

func TestInitialIngressMTLSHoldAcknowledgesSuspension(t *testing.T) {
	for _, scenario := range []string{"missing approval", "stale Portal client"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			var approved []ingressmtls.Binding
			f, _ := newUnreconciledSandboxRuntimeFixture(t, true, nil, true, "custom", true, false,
				withCommonCustom(t, "chat"), withReconciledCoordination(t), withCustomMTLS(t, false),
				func(r *AgentReconciler, kube client.Client) {
					updateContainerPlatformForTest(t, r, kube, func(document *SandboxPlatformConfiguration) {
						registration := document.Platforms[api.SandboxKata].IngressMTLS
						approved = append([]ingressmtls.Binding(nil), registration.Bindings...)
						if scenario == "missing approval" {
							registration.Bindings = []ingressmtls.Binding{}
						} else {
							registration.Bindings[0].ChatClients = append([]ingressmtls.ChatClient(nil), approved[0].ChatClients...)
							registration.Bindings[0].ChatClients[0].Secret.UID = "wrong-portal-secret-uid"
						}
					})
				})
			for range 4 {
				_, err := f.reconcile()
				if err != nil && !errors.Is(err, ingressmtls.ErrApprovalPending) && !errors.Is(err, ingressmtls.ErrApprovalStale) {
					t.Fatal("unexpected enrollment failure:", err)
				}
			}
			agent := f.current(t)
			if err := f.r.verifySuspendedReconciledBootstrap(ctx, agent); err != nil {
				t.Fatal("initial enrollment did not complete owned bootstrap:", err)
			}
			namespace := &corev1.Namespace{}
			if err := f.kube.Get(ctx, client.ObjectKey{Name: instanceNamespace(agent.Name)}, namespace); err != nil {
				t.Fatal(err)
			}
			home := &corev1.PersistentVolumeClaim{}
			homeKey := client.ObjectKey{Namespace: namespace.Name, Name: f.r.SandboxPreparation.bootstrap.HomeClaimName}
			if err := f.kube.Get(ctx, homeKey, home); err != nil {
				t.Fatal(err)
			}
			homeUID := home.UID
			assertNeverSubmitted := func() {
				t.Helper()
				if f.installer.stages != 0 {
					t.Fatal("unapproved first enrollment submitted a workload")
				}
				if scenario == "missing approval" && len(f.gateway.generated) != 0 {
					t.Fatal("missing initial approval issued an AI Gateway key")
				}
				for _, object := range []client.Object{&appsv1.StatefulSet{}, &istio.VirtualService{}} {
					name := instanceServiceName(agent.Name)
					if _, route := object.(*istio.VirtualService); route {
						name = virtualServiceName(agent.Name)
					}
					if err := f.kube.Get(ctx, client.ObjectKey{Namespace: namespace.Name, Name: name}, object); !apierrors.IsNotFound(err) {
						t.Fatalf("unapproved enrollment created %T: %v", object, err)
					}
				}
				if err := f.kube.Get(ctx, client.ObjectKey{Namespace: f.r.OperatorNamespace,
					Name: w.PodExpectationName(namespace.Name, namespace.UID)}, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
					t.Fatal("unapproved enrollment published an execution expectation:", err)
				}
				if _, found, err := f.r.SandboxNativeCoordination.ReadNative(ctx, agent.UID); err != nil || found {
					t.Fatal("unapproved enrollment retained an execution reservation:", err)
				}
			}
			assertNeverSubmitted()
			agent.Spec.Suspended, agent.Generation = true, agent.Generation+1
			if err := f.kube.Update(ctx, agent); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				_, err := f.reconcile()
				if err != nil && !errors.Is(err, ingressmtls.ErrApprovalStale) {
					t.Fatal("initial enrollment suspension failed:", err)
				}
			}
			stopped := f.current(t)
			if stopped.Status.Phase != api.PhaseSuspended || stopped.Status.ObservedGeneration != stopped.Generation {
				t.Fatalf("first enrollment hold did not acknowledge suspension: %+v", stopped.Status)
			}
			for _, kind := range []string{api.ConditionReady, api.ConditionPodReady} {
				condition := apimeta.FindStatusCondition(stopped.Status.Conditions, kind)
				if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "Suspended" ||
					condition.ObservedGeneration != stopped.Generation {
					t.Fatalf("initial suspension did not record current %s: %+v", kind, condition)
				}
			}
			reason := "IngressMTLSApprovalPending"
			if scenario == "stale Portal client" {
				reason = "IngressMTLSApprovalStale"
			}
			for _, kind := range []string{api.ConditionSandboxReady, api.ConditionIngressReady} {
				condition := apimeta.FindStatusCondition(stopped.Status.Conditions, kind)
				if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != reason ||
					condition.ObservedGeneration != stopped.Generation {
					t.Fatalf("initial suspension lost the %s diagnosis: %+v", kind, condition)
				}
			}
			patches := 0
			original := f.r.Client
			f.r.Client = interceptor.NewClient(original.(client.WithWatch), interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
					if _, ok := object.(*api.Agent); ok && subresource == "status" {
						patches++
					}
					return c.SubResource(subresource).Patch(ctx, object, patch, options...)
				},
			})
			f.r = restartEntraReconciler(f.r)
			for range 3 {
				_, err := f.reconcile()
				if err != nil && !errors.Is(err, ingressmtls.ErrApprovalStale) {
					t.Fatal(err)
				}
			}

			if after := f.current(t); patches != 0 || after.ResourceVersion != stopped.ResourceVersion ||
				!reflect.DeepEqual(after.Status, stopped.Status) {
				t.Fatalf("unchanged first-enrollment suspension rewrote status: patches=%d", patches)
			}
			f.r.Client = original
			agent = f.current(t)
			agent.Spec.Suspended, agent.Generation = false, agent.Generation+1
			if err := f.kube.Update(ctx, agent); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				_, err := f.reconcile()
				if err != nil && !errors.Is(err, ingressmtls.ErrApprovalStale) && !errors.Is(err, ingressmtls.ErrApprovalPending) {
					t.Fatal(err)
				}
			}
			assertNeverSubmitted()
			if f.current(t).Status.Phase != api.PhasePending {
				t.Fatal("resume was not held pending certificate approval")
			}
			updateContainerPlatformForTest(t, f.r, f.kube, func(document *SandboxPlatformConfiguration) {
				document.Platforms[api.SandboxKata].IngressMTLS.Bindings = approved
			})
			for range 4 {
				f.reconcileOK(t)
			}
			readyCustomWorkload(t, f)
			f.reconcileOK(t)
			if f.current(t).Status.Phase != api.PhaseReady {
				t.Fatal("explicit correct approval did not start the same Agent")
			}
			if err := f.kube.Get(ctx, homeKey, home); err != nil || home.UID != homeUID {
				t.Fatal("certificate enrollment replaced the bootstrap HOME:", err)
			}
		})
	}
}

func TestInitialIngressMTLSSuspensionRequiresBootstrapOnlyProof(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"bootstrap drift", "prior execution", "remaining Pod", "remaining workload", "route deletion", "read outage"} {
		t.Run(scenario, func(t *testing.T) {
			f, _ := newUnreconciledSandboxRuntimeFixture(t, true, nil, true, "custom", true, false,
				withCommonCustom(t, "chat"), withReconciledCoordination(t), withCustomMTLS(t, false),
				func(r *AgentReconciler, kube client.Client) {
					updateContainerPlatformForTest(t, r, kube, func(document *SandboxPlatformConfiguration) {
						document.Platforms[api.SandboxKata].IngressMTLS.Bindings = []ingressmtls.Binding{}
					})
				})
			for range 3 {
				f.reconcileOK(t)
			}
			agent := f.current(t)
			namespace := instanceNamespace(agent.Name)
			switch scenario {
			case "bootstrap drift":
				home := &corev1.PersistentVolumeClaim{}
				if err := f.kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: f.r.SandboxPreparation.bootstrap.HomeClaimName}, home); err != nil {
					t.Fatal(err)
				}
				home.OwnerReferences = nil
				if err := f.kube.Update(ctx, home); err != nil {
					t.Fatal(err)
				}
			case "prior execution":
				agent.Status.HelmReleaseName = "prior-execution"
				if err := f.kube.Status().Update(ctx, agent); err != nil {
					t.Fatal(err)
				}
			case "remaining Pod":
				if err := f.kube.Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "remaining", Namespace: namespace}}); err != nil {
					t.Fatal(err)
				}
			case "remaining workload":
				if err := f.kube.Create(ctx, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "unexpected", Namespace: namespace}}); err != nil {
					t.Fatal(err)
				}
			case "route deletion":
				route := &istio.VirtualService{ObjectMeta: metav1.ObjectMeta{
					Name: virtualServiceName(agent.Name), Namespace: namespace,
					OwnerReferences: []metav1.OwnerReference{w.AgentOwner(agent.Name, agent.UID)},
					Finalizers:      []string{"example.com/hold"},
				}}
				if err := f.kube.Create(ctx, route); err != nil {
					t.Fatal(err)
				}
			case "read outage":
				f.r.APIReader = interceptor.NewClient(f.kube.(client.WithWatch), interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, options ...client.ListOption) error {
						if _, ok := list.(*corev1.PodList); ok {
							return errors.New("synthetic Pod observation outage")
						}
						return c.List(ctx, list, options...)
					},
				})
			}
			agent = f.current(t)
			agent.Spec.Suspended, agent.Generation = true, agent.Generation+1
			if err := f.kube.Update(ctx, agent); err != nil {
				t.Fatal(err)
			}
			if _, err := f.reconcile(); err == nil {
				t.Fatal("unproven first-enrollment suspension did not surface the failed observation")
			}
			current := f.current(t)
			if current.Status.Phase != api.PhasePending || current.Status.ObservedGeneration == current.Generation {
				t.Fatal("incomplete or conflicting first-enrollment evidence acknowledged suspension")
			}
		})
	}
}

func initialIngressMTLSDeletionFixture(t *testing.T, kind string) *nativeSandboxFixture {
	t.Helper()
	setup := []func(*AgentReconciler, client.Client){withReconciledCoordination(t)}
	if kind == "custom" {
		setup = append(setup, withCommonCustom(t, "chat"), withCustomMTLS(t, false))
	} else {
		setup = append(setup, withIngressMTLS(t, kind))
	}
	setup = append(setup, func(r *AgentReconciler, kube client.Client) {
		agent := &api.Agent{}
		if err := kube.Get(context.Background(), client.ObjectKey{Name: "example"}, agent); err != nil {
			t.Fatal(err)
		}
		// The shared fixture presets the ordinary finalizer. Start before that
		// controller write, which a real first-approval hold never reaches.
		agent.Finalizers = nil
		if err := kube.Update(context.Background(), agent); err != nil {
			t.Fatal(err)
		}
		updateContainerPlatformForTest(t, r, kube, func(document *SandboxPlatformConfiguration) {
			document.Platforms[api.SandboxKata].IngressMTLS.Bindings = []ingressmtls.Binding{}
		})
	})
	f, _ := newUnreconciledSandboxRuntimeFixture(t, true, nil, true, kind, true, false, setup...)
	for range 3 {
		f.reconcileOK(t)
	}
	agent := f.current(t)
	if agent.Status.Phase != api.PhasePending || slices.Contains(agent.Finalizers, api.Finalizer) ||
		!slices.Contains(agent.Finalizers, sandbox.BootstrapFinalizer) || !slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) {
		t.Fatalf("fixture did not retain the real first-approval finalizer shape: %+v", agent)
	}
	if err := f.r.verifySuspendedReconciledBootstrap(context.Background(), agent); err != nil {
		t.Fatal("fixture has no complete owned bootstrap:", err)
	}
	return f
}

func TestInitialIngressMTLSHoldDeletesWithoutOrdinaryFinalizer(t *testing.T) {
	for _, kind := range []string{"custom", "openclaw", "hermes"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			f := initialIngressMTLSDeletionFixture(t, kind)
			agent := f.current(t)
			bootstrapKey := f.r.sandboxBootstrapKey(agent)
			namespace := &corev1.Namespace{}
			namespaceKey := client.ObjectKey{Name: instanceNamespace(agent.Name)}
			if err := f.kube.Get(ctx, namespaceKey, namespace); err != nil {
				t.Fatal(err)
			}
			for _, suspended := range []bool{true, false, true} {
				agent = f.current(t)
				agent.Spec.Suspended, agent.Generation = suspended, agent.Generation+1
				if err := f.kube.Update(ctx, agent); err != nil {
					t.Fatal(err)
				}
				f.reconcileOK(t)
				current := f.current(t)
				phase := api.PhasePending
				if suspended {
					phase = api.PhaseSuspended
				}
				if current.Status.Phase != phase || suspended && current.Status.ObservedGeneration != current.Generation ||
					slices.Contains(current.Finalizers, api.Finalizer) {
					t.Fatal("initial suspension/resume changed its finalizer or lifecycle contract")
				}
			}
			if f.installer.stages != 0 || len(f.gateway.generated) != 0 {
				t.Fatal("first-approval hold submitted execution or issued a model key")
			}
			stateKey := client.ObjectKey{Namespace: f.r.OperatorNamespace, Name: w.PodExpectationName(namespace.Name, namespace.UID)}
			if err := f.kube.Get(ctx, stateKey, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
				t.Fatal("bootstrap-only fixture published an execution expectation:", err)
			}
			if _, found, err := f.r.SandboxNativeCoordination.ReadNative(ctx, agent.UID); err != nil || found {
				t.Fatal("bootstrap-only fixture has an execution reservation:", err)
			}
			agent = f.current(t)
			if err := f.kube.Delete(ctx, agent, client.Preconditions{UID: &agent.UID, ResourceVersion: &agent.ResourceVersion}); err != nil {
				t.Fatal(err)
			}
			f.r = restartEntraReconciler(f.r)
			result, err := f.reconcile()
			if err != nil || result.RequeueAfter != 5*time.Second || f.current(t).Status.Phase != api.PhaseDeleting {
				t.Fatalf("already-terminating first-approval Agent never entered cleanup: %+v %v", result, err)
			}
			if err := f.kube.Get(ctx, namespaceKey, namespace); !apierrors.IsNotFound(err) {
				t.Fatal("bootstrap-only deletion did not remove the namespace:", err)
			}
			home := &corev1.PersistentVolumeClaim{}
			if err := f.kube.Get(ctx, client.ObjectKey{Namespace: namespaceKey.Name, Name: f.r.SandboxPreparation.bootstrap.HomeClaimName}, home); err != nil ||
				slices.Contains(home.Finalizers, sandbox.BootstrapFinalizer) {
				t.Fatal("bootstrap-only cleanup did not release its HOME protection:", err)
			}
			f.reconcileOK(t)
			for _, object := range []client.Object{
				&api.Agent{ObjectMeta: metav1.ObjectMeta{Name: agent.Name}},
				&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: bootstrapKey.Name, Namespace: bootstrapKey.Namespace}},
			} {
				if err := f.kube.Get(ctx, client.ObjectKeyFromObject(object), object); !apierrors.IsNotFound(err) {
					t.Fatalf("bootstrap-only cleanup retained %T: %v", object, err)
				}
			}
		})
	}
}

func TestInitialIngressMTLSDeletionKeepsBootstrapGuards(t *testing.T) {
	for _, scenario := range []string{"foreign HOME", "missing receipt", "remaining Pod", "namespace deletion failure"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			f := initialIngressMTLSDeletionFixture(t, "custom")
			agent := f.current(t)
			namespaceKey := client.ObjectKey{Name: instanceNamespace(agent.Name)}
			switch scenario {
			case "foreign HOME":
				home := &corev1.PersistentVolumeClaim{}
				if err := f.kube.Get(ctx, client.ObjectKey{Namespace: namespaceKey.Name, Name: f.r.SandboxPreparation.bootstrap.HomeClaimName}, home); err != nil {
					t.Fatal(err)
				}
				home.OwnerReferences[0].UID = "different-agent-uid"
				if err := f.kube.Update(ctx, home); err != nil {
					t.Fatal(err)
				}
			case "missing receipt":
				f.r.APIReader = &mtlsPrerequisiteReadOutage{
					Reader: f.kube, key: f.r.sandboxBootstrapKey(agent), active: true,
					failure: apierrors.NewNotFound(corev1.Resource("configmaps"), f.r.sandboxBootstrapKey(agent).Name),
				}
			case "remaining Pod":
				if err := f.kube.Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "remaining", Namespace: namespaceKey.Name}}); err != nil {
					t.Fatal(err)
				}
			case "namespace deletion failure":
				f.r.Client = interceptor.NewClient(f.kube, interceptor.Funcs{
					Delete: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.DeleteOption) error {
						if _, ok := object.(*corev1.Namespace); ok {
							return errors.New("synthetic namespace deletion failure")
						}
						return c.Delete(ctx, object, options...)
					},
				})
			}
			if err := f.kube.Delete(ctx, agent); err != nil {
				t.Fatal(err)
			}
			result, err := f.reconcile()
			if scenario == "remaining Pod" {
				if err != nil || result.RequeueAfter != cleanupWaitRequeue {
					t.Fatalf("bootstrap-only cleanup did not wait for Pods: %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatal("bootstrap-only cleanup ignored unavailable or conflicting evidence")
			}
			if current := f.current(t); !slices.Contains(current.Finalizers, sandbox.BootstrapFinalizer) ||
				!slices.Contains(current.Finalizers, sandbox.HoldFinalizer) || slices.Contains(current.Finalizers, api.Finalizer) {
				t.Fatal("blocked cleanup changed the retained bootstrap-only finalizer shape")
			}
			namespace := &corev1.Namespace{}
			if err := f.kube.Get(ctx, namespaceKey, namespace); err != nil || !namespace.DeletionTimestamp.IsZero() {
				t.Fatal("blocked cleanup deleted the namespace:", err)
			}
		})
	}
}
