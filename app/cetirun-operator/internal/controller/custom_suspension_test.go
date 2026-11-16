// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/internal/chataccess"
	"github.com/Azure/CetiRun/app/internal/terminalpolicy"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestOrdinaryCustomSuspensionRetainsPrivateChatRoute(t *testing.T) {
	requireLegacyCustomRuntime(t)
	ctx := context.Background()
	s := newCompletionScenario(t)
	agent := s.current()
	agent.Spec.Agent.Port = ptr.To[int32](8080)
	if err := s.client.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	s.observeApplied()
	s.reconciler.ChatAccess = chataccess.Config{Enabled: true,
		Callers: []chataccess.Caller{{Namespace: "portal", PodLabels: map[string]string{"app": "portal"}}}}
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	agent = s.current()
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(chataccess.RouteGVK)
	if err := s.client.Get(ctx, client.ObjectKey{Namespace: agent.Status.Namespace, Name: chataccess.RouteName(agent.Name)}, route); err != nil {
		t.Fatal(err)
	}
	agent.Spec.Suspended = true
	agent.Generation++
	if err := s.client.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	workload := s.workload()
	workload.Status = appsv1.StatefulSetStatus{ObservedGeneration: workload.Generation}
	if err := s.client.Status().Update(ctx, workload); err != nil {
		t.Fatal(err)
	}
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	if s.current().Status.Phase != api.PhaseSuspended {
		t.Fatal("ordinary Custom shutdown was not confirmed")
	}
	retained := &unstructured.Unstructured{}
	retained.SetGroupVersionKind(chataccess.RouteGVK)
	if err := s.client.Get(ctx, client.ObjectKeyFromObject(route), retained); err != nil ||
		retained.GetUID() != route.GetUID() || !reflect.DeepEqual(retained.Object["spec"], route.Object["spec"]) {
		t.Fatal("ordinary suspension changed the private Chat route:", err)
	}
}

func TestCustomSuspensionAcknowledgesStoppedGeneration(t *testing.T) {
	requireLegacyCustomRuntime(t)
	s := runningCustomReview(t)
	enableTerminal(t, s, false)
	if err := s.step(); err != nil {
		t.Fatal(err)
	}

	initial := s.current()
	if initial.Status.Phase != api.PhaseReady {
		t.Fatal("fixture did not reach Ready before suspension")
	}
	initialUID, initialNamespace := initial.UID, initial.Status.Namespace
	home := s.provider.bindings.Home
	initialHome := &corev1.PersistentVolumeClaim{}
	homeKey := client.ObjectKey{Namespace: initialNamespace, Name: home.Name}
	if err := s.client.Get(context.Background(), homeKey, initialHome); err != nil {
		t.Fatal(err)
	}
	workload := s.workload()
	workloadUID := workload.UID

	for cycle := 0; cycle != 2; cycle++ {
		current := s.current()
		previousObserved := current.Status.ObservedGeneration
		current.Spec.Suspended = true
		current.Generation++
		if err := s.client.Update(context.Background(), current); err != nil {
			t.Fatal(err)
		}
		wantGeneration := current.Generation

		if err := s.step(); err != nil {
			t.Fatal(err)
		}
		current = s.current()
		if current.Status.Phase != api.PhasePending || current.Status.ObservedGeneration != previousObserved {
			t.Fatalf("cycle %d: unacknowledged shutdown completed generation: %#v", cycle, current.Status)
		}

		workload = s.workload()
		if ptr.Deref(workload.Spec.Replicas, -1) != 0 {
			t.Fatalf("cycle %d: suspension did not scale the Custom workload to zero", cycle)
		}
		workload.Status = appsv1.StatefulSetStatus{ObservedGeneration: workload.Generation, Replicas: 0}
		if err := s.client.Status().Update(context.Background(), workload); err != nil {
			t.Fatal(err)
		}
		if err := s.step(); err != nil {
			t.Fatal(err)
		}
		if current := s.current(); current.Status.Phase != api.PhasePending || current.Status.ObservedGeneration != previousObserved {
			t.Fatal("unacknowledged terminal shutdown completed the suspension generation")
		}

		terminal := &appsv1.Deployment{}
		terminalKey := types.NamespacedName{
			Namespace: workload.Namespace,
			Name:      terminalpolicy.WorkloadName(workload.Name),
		}
		if err := s.client.Get(context.Background(), terminalKey, terminal); err != nil {
			t.Fatal(err)
		}
		if ptr.Deref(terminal.Spec.Replicas, -1) != 0 {
			t.Fatalf("cycle %d: suspension did not scale the Custom terminal to zero", cycle)
		}
		terminal.Status = appsv1.DeploymentStatus{ObservedGeneration: terminal.Generation, Replicas: 0}
		if err := s.client.Status().Update(context.Background(), terminal); err != nil {
			t.Fatal(err)
		}

		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "completion-stopping", Namespace: workload.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(workload, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
		}}
		if err := s.client.Create(context.Background(), pod); err != nil {
			t.Fatal(err)
		}
		if err := s.step(); err != nil {
			t.Fatal(err)
		}
		current = s.current()
		if current.Status.Phase != api.PhasePending || current.Status.ObservedGeneration != previousObserved {
			t.Fatalf("cycle %d: remaining Pod completed suspension: %#v", cycle, current.Status)
		}
		if err := s.client.Delete(context.Background(), pod); err != nil {
			t.Fatal(err)
		}

		if err := s.step(); err != nil {
			t.Fatal(err)
		}
		current = s.current()
		if current.Status.Phase != api.PhaseSuspended || current.Status.ObservedGeneration != wantGeneration {
			t.Fatalf("cycle %d: completed suspension did not acknowledge generation %d: %#v", cycle, wantGeneration, current.Status)
		}

		current.Spec.Suspended = false
		current.Generation++
		if err := s.client.Update(context.Background(), current); err != nil {
			t.Fatal(err)
		}
		s.provider.bindings.Generation = current.Generation
		s.helm.installCalled = false
		if err := s.step(); err != nil {
			t.Fatal(err)
		}
		if !s.helm.installCalled {
			t.Fatalf("cycle %d: resume did not reapply the stopped Custom workload", cycle)
		}
		workload = s.workload()
		if workload.UID != workloadUID {
			t.Fatalf("cycle %d: resume replaced the Custom workload", cycle)
		}
		workload.Spec.Replicas = ptr.To[int32](1)
		workload.Annotations["cetirun.microsoft.com/custom-plan-hash"] = s.helm.lastValues["runtime"].(map[string]any)["workloadHash"].(string)
		markGatewayCustomWorkload(workload, s.reconciler.AIGateway.URL)
		if err := s.client.Update(context.Background(), workload); err != nil {
			t.Fatal(err)
		}
		workload = s.workload()
		workload.Status = appsv1.StatefulSetStatus{
			ObservedGeneration: workload.Generation, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1,
		}
		if err := s.client.Status().Update(context.Background(), workload); err != nil {
			t.Fatal(err)
		}
		if err := s.client.Get(context.Background(), terminalKey, terminal); err != nil {
			t.Fatal(err)
		}
		terminal.Spec.Replicas = ptr.To[int32](1)
		if err := s.client.Update(context.Background(), terminal); err != nil {
			t.Fatal(err)
		}
		if err := s.client.Get(context.Background(), terminalKey, terminal); err != nil {
			t.Fatal(err)
		}
		terminal.Status = appsv1.DeploymentStatus{
			ObservedGeneration: terminal.Generation, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1,
		}
		if err := s.client.Status().Update(context.Background(), terminal); err != nil {
			t.Fatal(err)
		}
		if err := s.step(); err != nil {
			t.Fatal(err)
		}
		current = s.current()
		if current.UID != initialUID || current.Status.Namespace != initialNamespace || s.provider.bindings.Home != home || current.Status.Phase != api.PhaseReady {
			t.Fatalf("cycle %d: resume did not retain the Custom lifetime: %#v", cycle, current.Status)
		}
		currentHome := &corev1.PersistentVolumeClaim{}
		if err := s.client.Get(context.Background(), homeKey, currentHome); err != nil ||
			currentHome.UID != initialHome.UID || !reflect.DeepEqual(currentHome.Spec, initialHome.Spec) {
			t.Fatalf("cycle %d: resume changed the retained HOME: %v", cycle, err)
		}
	}
}

func TestCustomSuspensionWaitsForAccessAndObservations(t *testing.T) {
	for _, failure := range []string{"gateway", "pod-list", "route-finalizer"} {
		t.Run(failure, func(t *testing.T) {
			s := runningCustomReview(t)
			ctx := context.Background()
			current := s.current()
			previousObserved := current.Status.ObservedGeneration
			current.Spec.Suspended = true
			current.Generation++
			if err := s.client.Update(ctx, current); err != nil {
				t.Fatal(err)
			}
			if err := s.step(); err != nil {
				t.Fatal(err)
			}
			workload := s.workload()
			workload.Status = appsv1.StatefulSetStatus{ObservedGeneration: workload.Generation}
			if err := s.client.Status().Update(ctx, workload); err != nil {
				t.Fatal(err)
			}
			route := &istiov1beta1.VirtualService{ObjectMeta: metav1.ObjectMeta{
				Namespace: workload.Namespace, Name: "cetirun-" + current.Name,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(current, api.GroupVersion.WithKind("Agent"))},
				Finalizers:      []string{"example.com/retain-route"},
			}}
			if failure == "route-finalizer" {
				if err := s.client.Create(ctx, route); err != nil {
					t.Fatal(err)
				}
			}
			injected := errors.New("synthetic dependency diagnostic")
			s.reconciler.Client = interceptor.NewClient(s.client.Client.(client.WithWatch), interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
					if _, ok := object.(*corev1.Secret); ok && failure == "gateway" && key.Name == "ai-gateway-virtual-key" {
						return injected
					}
					return c.Get(ctx, key, object, opts...)
				},
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*corev1.PodList); ok && failure == "pod-list" {
						return injected
					}
					return c.List(ctx, list, opts...)
				},
			})
			err := s.step()
			if failure == "route-finalizer" && err != nil || failure != "route-finalizer" && !errors.Is(err, injected) {
				t.Fatal("unexpected suspension result:", err)
			}
			current = s.current()
			if current.Status.Phase != api.PhasePending || current.Status.ObservedGeneration != previousObserved {
				t.Fatal("unconfirmed access or observations acknowledged suspension")
			}
			for _, condition := range current.Status.Conditions {
				if strings.Contains(condition.Message, injected.Error()) {
					t.Fatal("dependency diagnostics were copied into user-visible suspension conditions")
				}
			}
			if failure == "route-finalizer" {
				if err := s.client.Get(ctx, client.ObjectKeyFromObject(route), route); err != nil || route.DeletionTimestamp.IsZero() {
					t.Fatal("fixture did not retain a terminating route:", err)
				}
				route.Finalizers = nil
				if err := s.client.Update(ctx, route); err != nil {
					t.Fatal(err)
				}
			}
			s.reconciler.Client = s.client
			if err := s.step(); err != nil {
				t.Fatal(err)
			}
			if current := s.current(); current.Status.Phase != api.PhaseSuspended || current.Status.ObservedGeneration != current.Generation {
				t.Fatal("suspension did not acknowledge completion after recovery")
			}
		})
	}
}

func TestCustomSuspensionDoesNotOverwriteConcurrentResume(t *testing.T) {
	requireLegacyCustomRuntime(t)
	s := runningCustomReview(t)
	current := s.current()
	previousObserved := current.Status.ObservedGeneration
	current.Spec.Suspended = true
	current.Generation++
	if err := s.client.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	workload := s.workload()
	workload.Status = appsv1.StatefulSetStatus{ObservedGeneration: workload.Generation, Replicas: 0}
	if err := s.client.Status().Update(context.Background(), workload); err != nil {
		t.Fatal(err)
	}

	conflicted := false
	backing, ok := s.client.Client.(client.WithWatch)
	if !ok {
		t.Fatal("fixture client does not support interceptor watches")
	}
	s.reconciler.Client = interceptor.NewClient(backing, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, object client.Object,
			patch client.Patch, opts ...client.SubResourcePatchOption,
		) error {
			agent, ok := object.(*api.Agent)
			if ok && subresource == "status" && agent.Status.Phase == api.PhaseSuspended && !conflicted {
				latest := s.current()
				latest.Spec.Suspended = false
				latest.Generation++
				if err := c.Update(ctx, latest); err != nil {
					return err
				}
				conflicted = true
			}
			return c.SubResource(subresource).Patch(ctx, object, patch, opts...)
		},
	})
	if err := s.step(); !conflicted || !apierrors.IsConflict(err) {
		t.Fatalf("suspension status patch did not reject a concurrent resume: %v", err)
	}
	current = s.current()
	if current.Spec.Suspended || current.Status.Phase == api.PhaseSuspended || current.Status.ObservedGeneration != previousObserved {
		t.Fatalf("stale suspension acknowledgement overwrote the concurrent resume: %#v", current.Status)
	}
}
