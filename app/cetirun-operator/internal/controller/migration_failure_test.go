// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestPlannedMigrationRejectsPreflightWithoutStopping(t *testing.T) {
	for _, fault := range []string{"disabled", "wrong UID", "zone", "taint", "CSI driver", "restart pending", "attacher missing"} {
		t.Run(fault, func(t *testing.T) {
			f := newMigrationFixture(t, "openclaw", false)
			ctx := context.Background()
			node := &corev1.Node{}
			if err := f.kube.Get(ctx, client.ObjectKey{Name: "node-b"}, node); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "disabled":
				f.r.SandboxPreparation.PlannedMigration = false
			case "zone":
				node.Labels["topology.kubernetes.io/zone"] = "different-zone"
				if err := f.kube.Update(ctx, node); err != nil {
					t.Fatal(err)
				}
			case "taint":
				node.Spec.Taints = []corev1.Taint{{Key: "example.com/dedicated", Value: "other", Effect: corev1.TaintEffectNoSchedule}}
				if err := f.kube.Update(ctx, node); err != nil {
					t.Fatal(err)
				}
			case "CSI driver":
				csi := &storagev1.CSINode{}
				if err := f.kube.Get(ctx, client.ObjectKey{Name: node.Name}, csi); err != nil {
					t.Fatal(err)
				}
				csi.Spec.Drivers = nil
				if err := f.kube.Update(ctx, csi); err != nil {
					t.Fatal(err)
				}
			case "attacher missing":
				attachment := &storagev1.VolumeAttachment{}
				if err := f.kube.Get(ctx, client.ObjectKey{Name: "source-attachment"}, attachment); err != nil {
					t.Fatal(err)
				}
				attachment.Finalizers = nil
				if err := f.kube.Update(ctx, attachment); err != nil {
					t.Fatal(err)
				}
			}
			requestMigration(t, f, "migration-00000001", "node-b")
			agent := f.current(t)
			if fault == "wrong UID" {
				agent.Spec.PlannedMigration.Target.UID = "different-node"
			}
			if fault == "restart pending" {
				agent.Spec.RestartRequestID = "restart-00000001"
			}
			if err := f.kube.Update(ctx, agent); err != nil {
				t.Fatal(err)
			}
			stages := f.installer.stages
			if _, err := f.reconcile(); err == nil {
				t.Fatal("invalid migration was not rejected")
			}
			if f.current(t).Status.PlannedMigration != nil || f.installer.stages != stages ||
				ptr.Deref(f.workload(t).Spec.Replicas, 0) != 1 {
				t.Fatal("failed migration preflight changed the source lifetime")
			}
		})
	}
}

func TestPlannedMigrationProtectionRetriesFailedAndLostWrites(t *testing.T) {
	for _, kind := range []string{"pod", "attachment"} {
		for _, committed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/committed=%t", kind, committed), func(t *testing.T) {
				f := newMigrationFixture(t, "openclaw", true)
				requestMigration(t, f, "migration-00000001", "node-b")
				f.reconcileOK(t)
				injected := errors.New("synthetic protection write failure")
				failed := false
				original := f.r.Client
				f.r.Client = interceptor.NewClient(f.kube, interceptor.Funcs{
					Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
						_, pod := object.(*corev1.Pod)
						_, attachment := object.(*storagev1.VolumeAttachment)
						if !failed && ((kind == "pod" && pod) || (kind == "attachment" && attachment)) {
							failed = true
							if committed {
								if err := c.Patch(ctx, object, patch, opts...); err != nil {
									return err
								}
							}
							return injected
						}
						return c.Patch(ctx, object, patch, opts...)
					},
				})
				if _, err := f.reconcile(); !errors.Is(err, injected) {
					t.Fatal("protection failure was hidden:", err)
				}
				migrationPhase(t, f, api.MigrationProtecting)
				if ptr.Deref(f.workload(t).Spec.Replicas, 0) != 1 {
					t.Fatal("partial protection stopped the unprotected source")
				}
				f.r.Client = original
				f.reconcileOK(t)
				migrationPhase(t, f, api.MigrationStopping)
			})
		}
	}
}

func TestPlannedMigrationProtectingRecoversAuthorizationOutage(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		for _, dependency := range []string{"Team", "AgentPolicy", "catalog"} {
			for _, protected := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/protected=%t", kind, dependency, protected), func(t *testing.T) {
					ctx := context.Background()
					f := newMigrationFixture(t, kind, true)
					initial := reconciledExpectationForTest(t, f)
					requestMigration(t, f, "migration-00000001", "node-b")
					f.reconcileOK(t)
					state := f.current(t).Status.PlannedMigration.DeepCopy()
					keyID, stages := f.current(t).Status.AIGateway.KeyID, f.installer.stages
					podKey := client.ObjectKey{Namespace: state.Namespace.Name, Name: state.SourcePod.Name}
					if protected {
						for _, item := range []struct {
							object    client.Object
							key       client.ObjectKey
							finalizer string
						}{
							{&corev1.Pod{}, podKey, api.MigrationPodFinalizer},
							{&storagev1.VolumeAttachment{}, client.ObjectKey{Name: state.Attachment.Name}, api.MigrationAttachmentFinalizer},
						} {
							if err := f.kube.Get(ctx, item.key, item.object); err != nil {
								t.Fatal(err)
							}
							if err := f.r.migrationFinalizer(ctx, f.current(t), item.object, item.finalizer, true); err != nil {
								t.Fatal(err)
							}
						}
					}
					unavailable := errors.New("synthetic migration authorization dependency outage")
					down := true
					f.r.aiGatewayClientFactory = func(context.Context) (aiGatewayKeyClient, error) {
						if down && dependency == "catalog" {
							return &failingListGateway{fakeAIGateway: f.gateway}, nil
						}
						return f.gateway, nil
					}
					unprotectedStop := false
					f.r.Client = interceptor.NewClient(f.kube, interceptor.Funcs{
						Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
							_, team := object.(*api.Team)
							_, policy := object.(*api.AgentPolicy)
							if down && ((dependency == "Team" && team) || (dependency == "AgentPolicy" && policy)) {
								return unavailable
							}
							return c.Get(ctx, key, object, opts...)
						},
						Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
							if err := c.Patch(ctx, object, patch, opts...); err != nil {
								return err
							}
							workload, ok := object.(*appsv1.StatefulSet)
							if !ok || ptr.Deref(workload.Spec.Replicas, -1) != 0 {
								return nil
							}
							// Model the StatefulSet controller deleting the source immediately on scale-down.
							pod := &corev1.Pod{}
							if err := c.Get(ctx, podKey, pod); err != nil {
								return client.IgnoreNotFound(err)
							}
							unprotectedStop = !slices.Contains(pod.Finalizers, api.MigrationPodFinalizer)
							attachment := &storagev1.VolumeAttachment{}
							if err := c.Get(ctx, client.ObjectKey{Name: state.Attachment.Name}, attachment); err != nil {
								return err
							}
							unprotectedStop = unprotectedStop || !slices.Contains(attachment.Finalizers, api.MigrationAttachmentFinalizer)
							return c.Delete(ctx, pod)
						},
					})
					f.r.APIReader = f.r.Client
					if _, err := f.reconcile(); err == nil {
						t.Fatal("authorization outage was not surfaced")
					}
					pod := &corev1.Pod{}
					if unprotectedStop {
						t.Error("authorization failure stopped the source before protecting its observations")
					}
					if err := f.kube.Get(ctx, podKey, pod); err != nil {
						t.Fatal("authorization outage lost the captured source Pod:", err)
					}
					if !slices.Contains(pod.Finalizers, api.MigrationPodFinalizer) ||
						ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
						t.Fatal("outage did not retain source protection while stopping execution")
					}
					if isConditionTrue(f.current(t).Status.Conditions, api.ConditionSandboxReady) {
						t.Fatal("authorization outage retained a stale SandboxReady observation")
					}

					down = false
					f.reconcileOK(t)
					migrationPhase(t, f, api.MigrationStopping)
					acknowledgeMigrationSource(t, f)
					f.reconcileOK(t)
					f.reconcileOK(t)
					acknowledgeMigrationDetach(t, f)
					f.reconcileOK(t)
					f.reconcileOK(t)
					f.reconcileOK(t)
					migrationPhase(t, f, api.MigrationStarting)

					down = true
					for range 2 {
						result, err := f.reconcile()
						current := f.current(t)
						if err != nil || result.RequeueAfter == 0 || current.Status.Policy == nil ||
							current.Status.Policy.Phase != "Pending" ||
							isConditionTrue(current.Status.Conditions, api.ConditionPolicyResolved) {
							t.Fatalf("destination authorization outage was not reported as a retrying hold: result=%+v err=%v", result, err)
						}
						if f.installer.stages != stages || ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
							t.Fatal("migration restarted without fresh destination authorization")
						}
						for _, key := range f.gateway.keys {
							if !key.Blocked || ptr.Deref(key.Spend, 0) != 3.25 {
								t.Fatal("authorization outage lost the blocked accounting identity")
							}
						}
					}
					down = false
					for range 4 {
						f.reconcileOK(t)
					}
					if f.installer.stages != stages+1 {
						t.Fatal("recovered dependencies did not allow exactly one replacement apply")
					}
					migrationReadyPod(t, f, "replacement-pod", "node-b")
					migrationAttachment(t, f, "replacement-attachment", "node-b")
					migrationWorkloadStatus(t, f, 1)
					f.reconcileOK(t)
					migrationPhase(t, f, api.MigrationCompleted)
					current := f.current(t)
					observed := current.Status.PlannedMigration
					expected := reconciledExpectationForTest(t, f)
					if observed.PlannedMigrationRequest != state.PlannedMigrationRequest ||
						observed.SourcePod != state.SourcePod || observed.Attachment != state.Attachment ||
						!observed.StartedAt.Equal(&state.StartedAt) || expected.Home != initial.Home ||
						expected.Namespace != initial.Namespace || current.Status.AIGateway.KeyID != keyID ||
						current.Status.Phase != api.PhaseReady || len(f.gateway.generated) != 1 {
						t.Fatal("authorization recovery reset the captured migration or retained identity")
					}
				})
			}
		}
	}
}

func TestPlannedMigrationProtectionCheckpointPrecedesAuthorizationHold(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed=%t", committed), func(t *testing.T) {
			ctx := context.Background()
			f := newMigrationFixture(t, "openclaw", true)
			requestMigration(t, f, "migration-00000001", "node-b")
			f.reconcileOK(t)
			state := f.current(t).Status.PlannedMigration.DeepCopy()
			lost := errors.New("synthetic stop checkpoint acknowledgement failure")
			unavailable := errors.New("synthetic Team lookup failure")
			failCheckpoint := true
			f.r.Client = interceptor.NewClient(f.kube, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
					if _, team := object.(*api.Team); team {
						return unavailable
					}
					return c.Get(ctx, key, object, opts...)
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, object client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					agent, ok := object.(*api.Agent)
					if failCheckpoint && sub == "status" && ok && agent.Status.PlannedMigration != nil &&
						agent.Status.PlannedMigration.Phase == api.MigrationStopping {
						if committed {
							if err := c.SubResource(sub).Patch(ctx, object, patch, opts...); err != nil {
								return err
							}
						}
						return lost
					}
					return c.SubResource(sub).Patch(ctx, object, patch, opts...)
				},
			})
			if _, err := f.reconcile(); !errors.Is(err, lost) || errors.Is(err, unavailable) {
				t.Fatal("authorization hold ran before the stop checkpoint was acknowledged:", err)
			}
			wantPhase := api.MigrationProtecting
			if committed {
				wantPhase = api.MigrationStopping
			}
			migrationPhase(t, f, wantPhase)
			if ptr.Deref(f.workload(t).Spec.Replicas, -1) != 1 {
				t.Fatal("an unacknowledged stop checkpoint stopped the source")
			}
			failCheckpoint = false
			_, err := f.reconcile()
			if (err != nil) != !committed {
				t.Fatal("checkpoint recovery did not resume its recorded transition:", err)
			}
			migrationPhase(t, f, api.MigrationStopping)
			pod := &corev1.Pod{}
			if err := f.kube.Get(ctx, client.ObjectKey{Namespace: state.Namespace.Name, Name: state.SourcePod.Name}, pod); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(pod.Finalizers, api.MigrationPodFinalizer) ||
				ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
				t.Fatal("recovery did not stop the protected source")
			}
		})
	}
}

func TestPlannedMigrationRecoveredDependencyStillChecksPolicy(t *testing.T) {
	f := newMigrationFixture(t, "hermes", true)
	ctx := context.Background()
	requestMigration(t, f, "migration-00000001", "node-b")
	f.reconcileOK(t)
	original := f.r.Client
	f.r.Client = interceptor.NewClient(f.kube, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
			if _, policy := object.(*api.AgentPolicy); policy {
				return errors.New("synthetic AgentPolicy lookup failure")
			}
			return c.Get(ctx, key, object, opts...)
		},
	})
	if _, err := f.reconcile(); err == nil {
		t.Fatal("source authorization failure was hidden")
	}
	f.r.Client = original
	acknowledgeMigrationSource(t, f)
	f.reconcileOK(t)
	f.reconcileOK(t)
	acknowledgeMigrationDetach(t, f)
	f.reconcileOK(t)
	f.reconcileOK(t)
	f.reconcileOK(t)
	migrationPhase(t, f, api.MigrationStarting)

	policy := &api.AgentPolicy{}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: f.current(t).Status.Policy.EffectivePolicyRef.Name}, policy); err != nil {
		t.Fatal(err)
	}
	setCondition(&policy.Status.Conditions, api.ConditionAgentPolicyAccepted, metav1.ConditionFalse,
		"Denied", "synthetic current policy denial")
	if err := f.kube.Status().Update(ctx, policy); err != nil {
		t.Fatal(err)
	}
	stages := f.installer.stages
	for range 2 {
		f.reconcileOK(t)
		current := f.current(t)
		if current.Status.Policy == nil || current.Status.Policy.Phase != "Denied" ||
			isConditionTrue(current.Status.Conditions, api.ConditionReady) ||
			f.installer.stages != stages || ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
			t.Fatal("a recovered dependency permitted a destination denied by current policy")
		}
		migrationPhase(t, f, api.MigrationStarting)
	}
}

func TestPlannedMigrationSuspensionAndDisabledGateRetainProgress(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		for _, captured := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/captured=%t", kind, captured), func(t *testing.T) {
				f := newMigrationFixture(t, kind, true)
				initial := reconciledExpectationForTest(t, f)
				requestMigration(t, f, "migration-00000001", "node-b")
				if captured {
					f.reconcileOK(t)
				}
				agent := f.current(t)
				request := *agent.Spec.PlannedMigration
				keyID := agent.Status.AIGateway.KeyID
				agent.Spec.Suspended = true
				agent.Generation++
				if err := f.kube.Update(context.Background(), agent); err != nil {
					t.Fatal(err)
				}
				if !captured {
					f.reconcileOK(t)
					migrationPhase(t, f, api.MigrationProtecting)
				}
				f.r.SandboxPreparation.PlannedMigration = false
				f.reconcileOK(t)
				migrationPhase(t, f, api.MigrationStopping)
				if ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
					t.Fatal("suspension waited for migration completion")
				}
				if f.current(t).Status.Phase == api.PhaseSuspended {
					t.Fatal("a stop request acknowledged suspension before source termination")
				}
				acknowledgeMigrationSource(t, f)
				f.reconcileOK(t)
				f.reconcileOK(t)
				acknowledgeMigrationDetach(t, f)
				f.reconcileOK(t)
				f.reconcileOK(t)
				f.reconcileOK(t)
				migrationPhase(t, f, api.MigrationStarting)
				stages := f.installer.stages
				for range 2 {
					f.reconcileOK(t)
				}
				if f.installer.stages != stages || ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
					t.Fatal("a suspended Agent started the migration destination")
				}
				agent = f.current(t)
				agent.Spec.Suspended = false
				agent.Generation++
				if err := f.kube.Update(context.Background(), agent); err != nil {
					t.Fatal(err)
				}
				for range 3 {
					f.reconcileOK(t)
				}
				if f.installer.stages != stages+1 {
					t.Fatal("disabling new requests stranded an accepted migration")
				}
				migrationReadyPod(t, f, "replacement-pod", "node-b")
				migrationAttachment(t, f, "replacement-attachment", "node-b")
				migrationWorkloadStatus(t, f, 1)
				f.reconcileOK(t)
				migrationPhase(t, f, api.MigrationCompleted)
				current := f.current(t)
				expected := reconciledExpectationForTest(t, f)
				if current.Status.Phase != api.PhaseReady || *current.Spec.PlannedMigration != request ||
					current.UID != initial.Agent.UID || expected.Home != initial.Home || expected.Namespace != initial.Namespace ||
					current.Status.AIGateway.KeyID != keyID || len(f.gateway.generated) != 1 {
					t.Fatal("resume did not complete the original migration with retained identities")
				}
			})
		}
	}
}

func TestPlannedMigrationRejectedPreflightStillSuspends(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		for _, fault := range []string{"disabled", "wrong UID"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				f := newMigrationFixture(t, kind, true)
				requestMigration(t, f, "migration-00000001", "node-b")
				agent := f.current(t)
				agent.Spec.Suspended = true
				agent.Generation++
				if fault == "disabled" {
					f.r.SandboxPreparation.PlannedMigration = false
				} else {
					agent.Spec.PlannedMigration.Target.UID = "different-node"
				}
				if err := f.kube.Update(context.Background(), agent); err != nil {
					t.Fatal(err)
				}
				request := *agent.Spec.PlannedMigration
				keyID := agent.Status.AIGateway.KeyID
				stages := f.installer.stages
				for range 2 {
					if _, err := f.reconcile(); err == nil {
						t.Fatal("migration preflight rejection was hidden")
					}
					current := f.current(t)
					if ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 ||
						current.Status.Phase != api.PhasePending || current.Status.AccessURL != "" ||
						isConditionTrue(current.Status.Conditions, api.ConditionReady) {
						t.Fatal("migration preflight prevented suspension or reported shutdown complete")
					}
					if current.Status.PlannedMigration != nil || *current.Spec.PlannedMigration != request ||
						f.installer.stages != stages || current.Status.AIGateway.KeyID != keyID || len(f.gateway.generated) != 1 {
						t.Fatal("rejected migration changed the retained request or execution identity")
					}
					for _, key := range f.gateway.keys {
						if !key.Blocked || ptr.Deref(key.Spend, 0) != 3.25 {
							t.Fatal("suspension did not block and retain the existing Gateway key")
						}
					}
				}
			})
		}
	}
}

func TestPlannedMigrationRecoversDetachStatusWriteLoss(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		for _, cleanup := range []bool{false, true} {
			for _, staleError := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/cleanup=%t/staleError=%t", kind, cleanup, staleError), func(t *testing.T) {
					f := newMigrationFixture(t, kind, true)
					initial := reconciledExpectationForTest(t, f)
					ctx := context.Background()
					requestMigration(t, f, "migration-00000001", "node-b")
					f.reconcileOK(t)
					f.reconcileOK(t)
					f.reconcileOK(t)
					acknowledgeMigrationSource(t, f)
					f.reconcileOK(t)
					f.reconcileOK(t)
					migrationPhase(t, f, api.MigrationSourceTerminated)
					state := f.current(t).Status.PlannedMigration
					stages := f.installer.stages
					attachment := &storagev1.VolumeAttachment{}
					key := client.ObjectKey{Name: state.Attachment.Name}
					if err := f.kube.Get(ctx, key, attachment); err != nil {
						t.Fatal(err)
					}
					if staleError {
						attachment.Status.DetachError = &storagev1.VolumeError{Message: "synthetic earlier detach error"}
						if err := f.kube.Status().Update(ctx, attachment); err != nil {
							t.Fatal(err)
						}
					}
					acknowledgeMigrationDetachFinalizer(t, f)
					if cleanup {
						if err := f.kube.Delete(ctx, f.current(t)); err != nil {
							t.Fatal(err)
						}
						f.reconcileOK(t)
						if done, err := f.r.cleanupMigration(ctx, f.current(t)); err != nil || !done {
							t.Fatalf("completed CSI detach stranded cleanup: done=%v err=%v", done, err)
						}
					} else {
						f.reconcileOK(t)
						migrationPhase(t, f, api.MigrationDetached)
						if err := f.kube.Get(ctx, key, attachment); err != nil {
							t.Fatal("detach checkpoint must precede protection release:", err)
						}
						if !attachment.Status.Attached || (attachment.Status.DetachError != nil) != staleError ||
							!slices.Contains(attachment.Finalizers, api.MigrationAttachmentFinalizer) {
							t.Fatal("migration rewrote CSI status or released protection before its checkpoint")
						}
						f.reconcileOK(t)
					}
					if err := f.kube.Get(ctx, key, attachment); !apierrors.IsNotFound(err) {
						t.Fatal("completed CSI detach retained migration protection:", err)
					}
					if f.installer.stages != stages || ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
						t.Fatal("detach acknowledgement prematurely started a replacement")
					}
					if cleanup {
						return
					}
					for range 4 {
						f.reconcileOK(t)
					}
					migrationReadyPod(t, f, "replacement-pod", "node-b")
					migrationAttachment(t, f, "replacement-attachment", "node-b")
					migrationWorkloadStatus(t, f, 1)
					f.reconcileOK(t)
					migrationPhase(t, f, api.MigrationCompleted)
					expected := reconciledExpectationForTest(t, f)
					if f.installer.stages != stages+1 || expected.Home != initial.Home || expected.Agent != initial.Agent ||
						expected.Namespace != initial.Namespace || len(f.gateway.generated) != 1 {
						t.Fatal("detach recovery changed retained identities or repeated the replacement")
					}
				})
			}
		}
	}
}

func TestPlannedMigrationDetachStillRequiresCapturedEvidence(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, fault := range []string{"not deleting", "attacher pending", "detach failed", "UID", "node", "driver", "volume"} {
			t.Run(fmt.Sprintf("cleanup=%t/%s", cleanup, fault), func(t *testing.T) {
				f := newMigrationFixture(t, "hermes", true)
				ctx := context.Background()
				requestMigration(t, f, "migration-00000001", "node-b")
				f.reconcileOK(t)
				f.reconcileOK(t)
				f.reconcileOK(t)
				acknowledgeMigrationSource(t, f)
				f.reconcileOK(t)
				f.reconcileOK(t)
				state := f.current(t).Status.PlannedMigration
				stages := f.installer.stages
				if cleanup {
					if err := f.kube.Delete(ctx, f.current(t)); err != nil {
						t.Fatal(err)
					}
				}
				f.r.APIReader = interceptor.NewClient(f.kube, interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
						if err := c.Get(ctx, key, object, opts...); err != nil {
							return err
						}
						attachment, ok := object.(*storagev1.VolumeAttachment)
						if !ok || attachment.Name != state.Attachment.Name {
							return nil
						}
						attachment.DeletionTimestamp = ptr.To(metav1.Now())
						attachment.Status.Attached = false
						attachment.Finalizers = []string{api.MigrationAttachmentFinalizer}
						switch fault {
						case "not deleting":
							attachment.DeletionTimestamp = nil
						case "attacher pending", "detach failed":
							attachment.Finalizers = append(attachment.Finalizers, migrationAttacherFinalizer(state.Driver))
							if fault == "detach failed" {
								attachment.Status.DetachError = &storagev1.VolumeError{Message: "synthetic detach rejection"}
							}
						case "UID":
							attachment.UID = "replacement-attachment"
						case "node":
							attachment.Spec.NodeName = "node-b"
						case "driver":
							attachment.Spec.Attacher = "different.example.com"
						case "volume":
							attachment.Spec.Source.PersistentVolumeName = ptr.To("different-volume")
						}
						return nil
					},
				})
				_, err := f.reconcile()
				if (err != nil) != (fault != "not deleting" && fault != "attacher pending") {
					t.Fatal("detach evidence produced the wrong failure result:", err)
				}
				migrationPhase(t, f, api.MigrationSourceTerminated)
				attachment := &storagev1.VolumeAttachment{}
				if err := f.kube.Get(ctx, client.ObjectKey{Name: state.Attachment.Name}, attachment); err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(attachment.Finalizers, api.MigrationAttachmentFinalizer) || f.installer.stages != stages ||
					ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
					t.Fatal("incomplete or foreign detach evidence released protection or started a replacement")
				}
			})
		}
	}
}

func TestPlannedMigrationDeletionReleasesItsProtectedObjects(t *testing.T) {
	f := newMigrationFixture(t, "openclaw", true)
	requestMigration(t, f, "migration-00000001", "node-b")
	f.reconcileOK(t)
	f.reconcileOK(t)
	f.reconcileOK(t)
	ctx := context.Background()
	agent := f.current(t)
	if err := f.kube.Delete(ctx, agent); err != nil {
		t.Fatal(err)
	}
	acknowledgeMigrationSource(t, f)
	f.reconcileOK(t)
	migrationPhase(t, f, api.MigrationSourceTerminated)
	f.reconcileOK(t)
	acknowledgeMigrationDetach(t, f)
	f.reconcileOK(t)
	if done, err := f.r.cleanupMigration(ctx, f.current(t)); err != nil || !done {
		t.Fatalf("migration protections survived ordinary deletion: done=%v err=%v", done, err)
	}
	attachments := &storagev1.VolumeAttachmentList{}
	if err := f.kube.List(ctx, attachments); err != nil {
		t.Fatal(err)
	}
	for _, attachment := range attachments.Items {
		if slices.Contains(attachment.Finalizers, api.MigrationAttachmentFinalizer) {
			t.Fatal("migration left an attachment finalizer")
		}
	}
}

func TestPlannedMigrationDeletionAfterDestinationSubmissionUsesNormalCleanup(t *testing.T) {
	f := newMigrationFixture(t, "openclaw", true)
	completeMigration(t, f, "migration-00000001", "node-b")
	ctx := context.Background()
	agent := f.current(t)
	agent.Status.PlannedMigration.Phase = api.MigrationStarting
	if err := f.kube.Status().Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := f.kube.Delete(ctx, agent); err != nil {
		t.Fatal(err)
	}
	agent = f.current(t)
	if done, err := f.r.cleanupMigration(ctx, agent); err != nil || !done {
		t.Fatalf("destination submission re-entered old source cleanup: done=%v err=%v", done, err)
	}
	_, err := f.reconcile()
	if err != nil && !strings.Contains(err.Error(), "common Sandbox source reservation waits for acknowledged controller shutdown") {
		t.Fatal("normal deletion failed outside the expected controller-shutdown wait:", err)
	}
	if ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
		t.Fatal("normal deletion did not stop the replacement workload")
	}
}

func TestMigrationLeaseFreshnessBounds(t *testing.T) {
	f := newMigrationFixture(t, "openclaw", false)
	now := time.Now().Truncate(time.Microsecond)
	f.r.SandboxPreparation.Now = func() time.Time { return now }
	for _, test := range []struct {
		duration int32
		age      time.Duration
		allowed  bool
	}{
		{40, 40 * time.Second, true},
		{40, 40*time.Second + time.Microsecond, false},
		{40, -5 * time.Second, true},
		{40, -5*time.Second - time.Microsecond, false},
		{120, 60 * time.Second, true},
		{120, 60*time.Second + time.Microsecond, false},
		{0, 0, false},
	} {
		lease := &coordinationv1.Lease{}
		if err := f.kube.Get(context.Background(), client.ObjectKey{Namespace: corev1.NamespaceNodeLease, Name: "node-a"}, lease); err != nil {
			t.Fatal(err)
		}
		lease.Spec.LeaseDurationSeconds = ptr.To(test.duration)
		lease.Spec.RenewTime = &metav1.MicroTime{Time: now.Add(-test.age)}
		if err := f.kube.Update(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
		_, _, err := f.r.migrationNode(context.Background(), api.MigrationObject{Name: "node-a", UID: "node-a-uid"}, migrationTestDriver)
		if (err == nil) != test.allowed {
			t.Fatalf("duration=%d age=%s allowed=%v err=%v", test.duration, test.age, test.allowed, err)
		}
	}
}

func TestMigrationTerminationRequiresNamedContainerAcknowledgements(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: ptr.To(metav1.Now())},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent"}, {Name: "helper"}}},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "agent", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.Now()}}},
			{Name: "helper", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.Now()}}},
		}}}
	if !migrationPodTerminated(pod) {
		t.Fatal("complete named termination was rejected")
	}
	pod.Status.ContainerStatuses[1].Name = "agent"
	if migrationPodTerminated(pod) {
		t.Fatal("duplicate status hid an unacknowledged container")
	}
}
