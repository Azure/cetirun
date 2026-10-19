// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type sharedSandboxInstaller struct {
	*nativeSandboxInstaller
}

func (i *sharedSandboxInstaller) Prepare(_ string, values map[string]any, version string) (*helm.PreparedRelease, error) {
	if version != i.chart.Metadata.Version {
		return nil, fmt.Errorf("unexpected chart version")
	}
	return helm.PrepareChart(i.chart, values)
}

func (i *sharedSandboxInstaller) Refresh(prepared *helm.PreparedRelease, values map[string]any) (*helm.PreparedRelease, error) {
	return prepared.Rebuild(values)
}

func (i *sharedSandboxInstaller) InstallOrUpgrade(namespace, _ string, prepared *helm.PreparedRelease) error {
	manifest, err := prepared.SandboxManifest()
	if err != nil {
		return err
	}
	ctx := context.Background()
	decoder := kubeyaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	for {
		object := &unstructured.Unstructured{}
		if err := decoder.Decode(object); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		if len(object.Object) == 0 {
			continue
		}
		if object.GetNamespace() != namespace {
			return fmt.Errorf("unexpected rendered namespace")
		}
		current := &unstructured.Unstructured{}
		current.SetGroupVersionKind(object.GroupVersionKind())
		if err := i.kube.Get(ctx, client.ObjectKeyFromObject(object), current); apierrors.IsNotFound(err) {
			if err := i.kube.Create(ctx, object); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			object.SetUID(current.GetUID())
			object.SetResourceVersion(current.GetResourceVersion())
			if err := i.kube.Update(ctx, object); err != nil {
				return err
			}
		}
	}
	i.prepared = prepared
	i.stages++
	return nil
}

func (i *sharedSandboxInstaller) CustomApplied(_, _ string, _ *helm.PreparedRelease) (bool, error) {
	return false, fmt.Errorf("fixture is not a Custom runtime")
}

func (i *sharedSandboxInstaller) Uninstall(namespace, _ string) error {
	for _, workload := range []client.Object{
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "cetirun-example", Namespace: namespace}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "cetirun-example-copilot-proxy", Namespace: namespace}},
	} {
		if err := i.kube.Delete(context.Background(), workload); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

func TestReconciledSandboxUsesCommonApplyWithoutOneShotState(t *testing.T) {
	f := newSandboxRuntimeFixture(t, true, nil, true)
	ctx := context.Background()
	for range 3 {
		f.reconcileOK(t)
	}
	if f.installer.stages != 1 {
		t.Fatalf("expected one common apply, got %d", f.installer.stages)
	}
	agent := f.current(t)
	workload := &appsv1.StatefulSet{}
	if err := f.kube.Get(ctx, client.ObjectKey{Namespace: instanceNamespace(agent.Name), Name: instanceServiceName(agent.Name)}, workload); err != nil {
		t.Fatal(err)
	}
	if ptr.Deref(workload.Spec.Replicas, 0) != 1 || len(workload.Spec.Template.Finalizers) != 0 {
		t.Fatal("new shared workload retained the legacy zero-replica/single-Pod hold")
	}
	namespace := &corev1.Namespace{}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: workload.Namespace}, namespace); err != nil {
		t.Fatal(err)
	}
	state := &corev1.ConfigMap{}
	if err := f.kube.Get(ctx, client.ObjectKey{Namespace: f.r.OperatorNamespace,
		Name: w.PodExpectationName(namespace.Name, namespace.UID)}, state); err != nil {
		t.Fatal(err)
	}
	expected, err := w.ReadReconcileExpectation(state)
	if err != nil {
		t.Fatal(err)
	}
	if expected.Agent.UID != agent.UID || len(expected.Workloads) != 1 {
		t.Fatal("common expectation did not bind the current owned workload")
	}
	homeUID := expected.Home.UID
	oldPlan := expected.PlanHash
	secret := &corev1.Secret{}
	if err := f.kube.Get(ctx, client.ObjectKey{Namespace: workload.Namespace, Name: agent.Spec.GatewayTokenSecretRef}, secret); err != nil {
		t.Fatal(err)
	}
	secret.Data["CETIRUN_AUTH_KEY"] = []byte("synthetic-replacement-auth-key")
	if err := f.kube.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	f.reconcileOK(t)
	if err := f.kube.Get(ctx, client.ObjectKeyFromObject(state), state); err != nil {
		t.Fatal(err)
	}
	expected, err = w.ReadReconcileExpectation(state)
	if err != nil {
		t.Fatal(err)
	}
	if expected.Home.UID != homeUID || expected.PlanHash == oldPlan || f.installer.stages != 2 {
		t.Fatal("common credential update failed to preserve HOME or reconcile the new template")
	}
	if _, _, err := f.r.readSandboxPreparationReceipt(ctx, agent); !apierrors.IsNotFound(err) {
		t.Fatalf("new common flow unexpectedly created a legacy single-start receipt: %v", err)
	}
}

func TestReconciledSandboxDoesNotAdoptForeignWorkload(t *testing.T) {
	f := newSandboxRuntimeFixture(t, true, nil, true)
	workload := &appsv1.StatefulSet{}
	key := client.ObjectKey{Namespace: instanceNamespace(f.agent.Name), Name: instanceServiceName(f.agent.Name)}
	if err := f.kube.Get(context.Background(), key, workload); err != nil {
		t.Fatal(err)
	}
	workload.OwnerReferences[0].UID = types.UID("another-agent")
	if err := f.kube.Update(context.Background(), workload); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconcile(); err == nil {
		t.Fatal("common reconciliation adopted a foreign workload")
	}
}

func TestReconciledSandboxStatusFailureIsNotAuthorizationDenial(t *testing.T) {
	f := newSandboxRuntimeFixture(t, true, nil, true)
	ctx := context.Background()
	agent := f.current(t)
	agent.Spec.Model = testSharedModelOne
	agent.Generation++
	if err := f.kube.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	watch, ok := f.kube.(client.WithWatch)
	if !ok {
		t.Fatal("fixture requires watch support")
	}
	f.r.Client = interceptor.NewClient(watch, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, object client.Object,
			patch client.Patch, options ...client.SubResourcePatchOption,
		) error {
			if subresource == "status" {
				return apierrors.NewConflict(api.GroupVersion.WithResource("agents").GroupResource(), object.GetName(), errors.New("synthetic observation conflict"))
			}
			return c.SubResource(subresource).Patch(ctx, object, patch, options...)
		},
	})
	result, err := f.r.reconcilePolicy(ctx, agent)
	if err == nil || !result.authorized || !result.dependencyUnavailable {
		t.Fatalf("valid policy observation failure became a destructive authorization denial: result=%+v error=%v", result, err)
	}
}

func TestReconciledSandboxRouteOwnershipSurvivesHold(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreign=%v", foreign), func(t *testing.T) {
			f := newSandboxRuntimeFixture(t, true, nil, true)
			ctx := context.Background()
			agent := f.current(t)
			if _, err := f.r.ensureVirtualService(ctx, agent); err != nil {
				t.Fatal(err)
			}
			route := &istiov1beta1.VirtualService{}
			key := client.ObjectKey{Namespace: instanceNamespace(agent.Name), Name: virtualServiceName(agent.Name)}
			if err := f.kube.Get(ctx, key, route); err != nil {
				t.Fatal(err)
			}
			if !sandboxBuiltinOwned(route, agent) {
				t.Fatal("the common route cannot be safely withdrawn by its own controller")
			}
			if foreign {
				route.OwnerReferences[0].UID = "another-agent"
				if err := f.kube.Update(ctx, route); err != nil {
					t.Fatal(err)
				}
				if err := f.r.stopSandboxBuiltin(ctx, agent); err == nil {
					t.Fatal("hold cleanup accepted a foreign route")
				}
				if _, err := f.r.ensureVirtualService(ctx, agent); err == nil {
					t.Fatal("normal reconciliation adopted a foreign route")
				}
			} else {
				if err := f.r.stopSandboxBuiltin(ctx, agent); err != nil {
					t.Fatal("the common route blocked its own normal hold:", err)
				}
				if err := f.kube.Get(ctx, key, route); !apierrors.IsNotFound(err) {
					t.Fatal("owned route was not withdrawn:", err)
				}
			}
		})
	}
}

func TestReconciledSandboxRepairsCurrentStateDrift(t *testing.T) {
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, true)
	ctx := context.Background()
	expected := reconciledExpectationForTest(t, f)
	before := f.installer.stages
	for _, workload := range expected.Workloads {
		if workload.Kind == "StatefulSet" {
			object := &appsv1.StatefulSet{}
			if err := f.kube.Get(ctx, client.ObjectKey{Namespace: expected.Namespace.Name, Name: workload.Identity.Name}, object); err != nil {
				t.Fatal(err)
			}
			object.Spec.Replicas = ptr.To[int32](0)
			if err := f.kube.Update(ctx, object); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.reconcileOK(t)
	if f.installer.stages != before+1 {
		t.Fatal("the shared apply cache concealed a stopped workload")
	}
	after := reconciledExpectationForTest(t, f)
	if after.Home != expected.Home || after.PlanHash != expected.PlanHash {
		t.Fatal("repair changed the authorized plan or retained HOME")
	}
}

func TestReconciledSandboxSuspensionKeepsHomeAndKey(t *testing.T) {
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "hermes", true, true)
	ctx := context.Background()
	initial := reconciledExpectationForTest(t, f)
	keyID := f.current(t).Status.AIGateway.KeyID
	for _, suspended := range []bool{true, false} {
		agent := f.current(t)
		agent.Spec.Suspended = suspended
		agent.Generation++
		if err := f.kube.Update(ctx, agent); err != nil {
			t.Fatal(err)
		}
		f.reconcileOK(t)
		current := f.current(t)
		expected := reconciledExpectationForTest(t, f)
		if expected.Home != initial.Home || current.Status.AIGateway.KeyID != keyID || len(f.gateway.generated) != 1 {
			t.Fatal("suspension/reconciliation replaced HOME or reset the Gateway key")
		}
		want := int32(1)
		if suspended {
			want = 0
			if current.Status.Phase != api.PhaseSuspended || !current.Status.AIGateway.Blocked {
				t.Fatal("suspension did not stop access before acknowledging completion")
			}
		} else if current.Status.AIGateway.Blocked {
			t.Fatal("authorized reconciliation did not release its own suspension block")
		}
		for _, workload := range expected.Workloads {
			if workload.Replicas != want {
				t.Fatal("Agent and auxiliary suspension states differ")
			}
		}
	}
}

func TestReconciledSandboxFinishesStoppingReservationBeforeResume(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		for _, acknowledged := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/acknowledged=%v", kind, acknowledged), func(t *testing.T) {
				ctx := context.Background()
				f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, kind, true, true,
					func(r *AgentReconciler, kube client.Client) {
						authority, err := sandboxcoordination.NewState(r.OperatorNamespace, "resume-coordination")
						if err != nil {
							t.Fatal(err)
						}
						if err := kube.Create(ctx, authority); err != nil {
							t.Fatal(err)
						}
						r.SandboxNativeCoordination = &sandboxcoordination.Store{
							Client: kube, APIReader: kube, Namespace: authority.Namespace,
							Identity: w.ObjectIdentity{Name: authority.Name, UID: authority.UID},
						}
					})
				agent := f.current(t)
				if agent.Spec.Suspended || slices.Contains(agent.Finalizers, sandboxHoldFinalizer) {
					t.Fatal("fixture must start with current resume intent and no hold marker")
				}
				expected := reconciledExpectationForTest(t, f)
				keyID, stages := agent.Status.AIGateway.KeyID, f.installer.stages
				store := f.r.SandboxNativeCoordination
				ticket, found, err := store.ReadNative(ctx, agent.UID)
				if err != nil || !found {
					t.Fatal("missing initial source reservation:", err)
				}
				if _, err := store.BeginNativeStop(ctx, ticket); err != nil {
					t.Fatal(err)
				}
				for _, desired := range expected.Workloads {
					key := client.ObjectKey{Namespace: expected.Namespace.Name, Name: desired.Identity.Name}
					if desired.Kind == "StatefulSet" {
						workload := &appsv1.StatefulSet{}
						if err := f.kube.Get(ctx, key, workload); err != nil {
							t.Fatal(err)
						}
						workload.Spec.Replicas = ptr.To[int32](0)
						if err := f.kube.Update(ctx, workload); err != nil {
							t.Fatal(err)
						}
						workload.Status.ObservedGeneration = workload.Generation
						workload.Status.Replicas = 0
						if !acknowledged {
							workload.Status.Replicas = 1
						}
						if err := f.kube.Status().Update(ctx, workload); err != nil {
							t.Fatal(err)
						}
					} else {
						workload := &appsv1.Deployment{}
						if err := f.kube.Get(ctx, key, workload); err != nil {
							t.Fatal(err)
						}
						workload.Spec.Replicas = ptr.To[int32](0)
						if err := f.kube.Update(ctx, workload); err != nil {
							t.Fatal(err)
						}
						workload.Status.ObservedGeneration = workload.Generation
						workload.Status.Replicas = 0
						if err := f.kube.Status().Update(ctx, workload); err != nil {
							t.Fatal(err)
						}
					}
				}
				_, reconcileErr := f.reconcile()
				if !slices.Contains(f.current(t).Finalizers, sandboxHoldFinalizer) || f.installer.stages != stages {
					t.Fatal("pending stop did not restore the hold before any new workload apply")
				}
				current, found, err := store.ReadNative(ctx, agent.UID)
				if err != nil {
					t.Fatal(err)
				}
				if !acknowledged {
					if reconcileErr == nil || !found || current.Reservation.ID != ticket.Reservation.ID ||
						current.Reservation.Phase != sandboxcoordination.Stopping {
						t.Fatal("unacknowledged shutdown released or replaced its stopping reservation:", reconcileErr)
					}
					return
				}
				if reconcileErr != nil || found {
					t.Fatal("acknowledged stop did not finish before resume:", reconcileErr)
				}
				for range 3 {
					f.reconcileOK(t)
				}
				current, found, err = store.ReadNative(ctx, agent.UID)
				updated := reconciledExpectationForTest(t, f)
				if err != nil || !found || current.Reservation.Phase != sandboxcoordination.Reserved ||
					current.Reservation.ID == ticket.Reservation.ID || updated.Home != expected.Home ||
					f.current(t).Status.AIGateway.KeyID != keyID || len(f.gateway.generated) != 1 ||
					slices.Contains(f.current(t).Finalizers, sandboxHoldFinalizer) {
					t.Fatal("resume did not reacquire current authority while preserving HOME and key identity:", err)
				}
				for _, workload := range updated.Workloads {
					if workload.Replicas != 1 {
						t.Fatal("resume did not restore the Agent and auxiliary workloads")
					}
				}
			})
		}
	}
}

func reconciledExpectationForTest(t *testing.T, f *nativeSandboxFixture) w.ReconcileExpectation {
	t.Helper()
	ctx := context.Background()
	ns := &corev1.Namespace{}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: instanceNamespace(f.agent.Name)}, ns); err != nil {
		t.Fatal(err)
	}
	state := &corev1.ConfigMap{}
	if err := f.kube.Get(ctx, client.ObjectKey{Namespace: f.r.OperatorNamespace, Name: w.PodExpectationName(ns.Name, ns.UID)}, state); err != nil {
		t.Fatal(err)
	}
	expected, err := w.ReadReconcileExpectation(state)
	if err != nil {
		t.Fatal(err)
	}
	return expected
}

func TestReconciledSandboxCommonModelCombinations(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		for _, models := range []struct {
			name            string
			shared, copilot bool
		}{{"shared", true, false}, {"copilot", false, true}, {"mixed", true, true}} {
			t.Run(kind+"/"+models.name, func(t *testing.T) {
				f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, kind, models.shared, models.copilot)
				ctx := context.Background()
				for range 3 {
					f.reconcileOK(t)
				}
				expected := reconciledExpectationForTest(t, f)
				count := 1
				if models.copilot {
					count++
				}
				if len(expected.Workloads) != count {
					t.Fatalf("common inventory contains %d workloads, want %d", len(expected.Workloads), count)
				}
				for _, workload := range expected.Workloads {
					if workload.Replicas != 1 || len(workload.Template.Finalizers) != 0 {
						t.Fatalf("%s was unnecessarily held before model sign-in", workload.Kind)
					}
					if workload.Kind == "StatefulSet" {
						container := workload.Template.Spec.Containers[0]
						for _, probe := range []*corev1.Probe{container.StartupProbe, container.ReadinessProbe, container.LivenessProbe} {
							if probe == nil || probe.Exec == nil || probe.HTTPGet != nil {
								t.Fatal("protected readiness must not require forbidden host-to-Pod ingress")
							}
							if len(probe.Exec.Command) != 5 || probe.Exec.Command[2] != fmt.Sprint(*f.current(t).Spec.Agent.Port) {
								t.Fatal("protected readiness did not use the actual resolved runtime port")
							}
						}
					}
				}
				if !models.copilot {
					return
				}
				agent := f.current(t)
				if agent.Status.AIGateway.Copilot == nil || agent.Status.AIGateway.Copilot.Phase != api.AIGatewayCopilotPhaseSignInRequired {
					t.Fatal("pre-login state did not preserve the common Portal sign-in workflow")
				}
				token := &corev1.Secret{}
				if err := f.kube.Get(ctx, client.ObjectKey{Namespace: expected.Namespace.Name, Name: api.CopilotTokenSecretName}, token); err != nil {
					t.Fatal(err)
				}
				token.Data = map[string][]byte{api.CopilotTokenSecretKey: []byte("gho_synthetic")}
				token.Annotations = map[string]string{
					api.AnnotationCopilotSignInGeneration: "0a1b2c3d",
					api.AnnotationCopilotSignInCompleted:  "0a1b2c3d",
				}
				if err := f.kube.Update(ctx, token); err != nil {
					t.Fatal(err)
				}
				agent.Annotations[api.AnnotationCopilotModels] = testCopilotModels
				agent.Annotations[api.AnnotationCopilotSignInGeneration] = "0a1b2c3d"
				if err := f.kube.Update(ctx, agent); err != nil {
					t.Fatal(err)
				}
				for _, workload := range expected.Workloads {
					if workload.Kind != "Deployment" {
						continue
					}
					proxy := &appsv1.Deployment{}
					if err := f.kube.Get(ctx, client.ObjectKey{Namespace: expected.Namespace.Name, Name: workload.Identity.Name}, proxy); err != nil {
						t.Fatal(err)
					}
					proxy.Status.ReadyReplicas = 1
					if err := f.kube.Status().Update(ctx, proxy); err != nil {
						t.Fatal(err)
					}
				}
				f.reconcileOK(t)
				updated := reconciledExpectationForTest(t, f)
				if updated.Home != expected.Home || updated.PlanHash == expected.PlanHash {
					t.Fatal("completed sign-in did not update the common deployment while preserving HOME")
				}
				if f.current(t).Status.AIGateway.Copilot.Phase != api.AIGatewayCopilotPhaseReady {
					t.Fatal("completed synthetic sign-in did not register Copilot through the common Gateway reconciler")
				}
				for _, workload := range updated.Workloads {
					if workload.Kind == "Deployment" && workload.Template.Annotations[api.AnnotationCopilotSignInGeneration] != "0a1b2c3d" {
						t.Fatal("common proxy template lost the sign-in generation")
					}
				}
			})
		}
	}
}
