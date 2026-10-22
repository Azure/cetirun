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
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestPlannedMigrationRejectsPreflightWithoutStopping(t *testing.T) {
	for _, fault := range []string{"disabled", "wrong UID", "zone", "taint", "CSI driver", "restart pending"} {
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
