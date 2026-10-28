// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/internal/chataccess"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func suspensionWorkload(agent *api.Agent) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: chataccess.ServiceName(agent.Name), Namespace: InstanceNamespace(agent.Name),
			UID: "workload-uid",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(),
				Kind: "Agent", Name: agent.Name, UID: agent.UID, Controller: ptr.To(true)}},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To[int32](1),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "cetirun",
					VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/home/vibe"}}}},
				Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "home"},
				}}},
			}},
		},
	}
}

func TestOwnerSuspensionRejectsEphemeralHome(t *testing.T) {
	for _, kind := range []api.AgentType{api.AgentTypeOpenClaw, api.AgentTypeHermes} {
		t.Run(string(kind), func(t *testing.T) {
			agent := restartableInstance()
			agent.Spec.Agent.Type = kind
			workload := suspensionWorkload(agent)
			workload.Spec.Template.Spec.Volumes[0].VolumeSource = corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
			fc := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(agent, workload).Build()
			if err := fc.Get(context.Background(), client.ObjectKeyFromObject(agent), agent); err != nil {
				t.Fatal(err)
			}
			before := agent.DeepCopy()
			_, err := NewClient(fc).SetInstanceSuspendedByName(context.Background(), agent.Name,
				InstanceVersion{UID: agent.UID, ResourceVersion: agent.ResourceVersion}, true, nil)
			if err == nil {
				t.Fatal("retained suspension accepted emptyDir HOME")
			}
			if err := fc.Get(context.Background(), client.ObjectKeyFromObject(agent), agent); err != nil ||
				!reflect.DeepEqual(agent.Spec, before.Spec) {
				t.Fatal("rejected suspension changed intent:", err)
			}
		})
	}
}

func TestSuspensionHomeRequiresTheOwnedEffectiveMount(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*appsv1.StatefulSet)
		want   error
	}{
		{"existing claim", func(_ *appsv1.StatefulSet) {}, nil},
		{"claim template", func(s *appsv1.StatefulSet) {
			s.Spec.Template.Spec.Volumes = nil
			s.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data"}}}
		}, nil},
		{"empty claim", func(s *appsv1.StatefulSet) {
			s.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = ""
		}, ErrSuspensionHomeRequired},
		{"unmounted persistent data", func(s *appsv1.StatefulSet) {
			s.Spec.Template.Spec.Containers[0].VolumeMounts[0].Name = "ephemeral"
			s.Spec.Template.Spec.Volumes = append(s.Spec.Template.Spec.Volumes, corev1.Volume{Name: "ephemeral",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
		}, ErrSuspensionHomeRequired},
		{"unknown HOME mount", func(s *appsv1.StatefulSet) {
			s.Spec.Template.Spec.Containers[0].VolumeMounts = nil
		}, ErrSuspensionHomeUnverified},
		{"replaced Agent", func(s *appsv1.StatefulSet) {
			s.OwnerReferences[0].UID = "previous-agent"
		}, ErrSuspensionHomeUnverified},
		{"unowned workload", func(s *appsv1.StatefulSet) { s.OwnerReferences = nil }, ErrSuspensionHomeUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := restartableInstance()
			workload := suspensionWorkload(agent)
			tc.change(workload)
			fc := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(agent, workload).Build()
			err := NewClient(fc).CheckSuspensionHome(context.Background(), agent)
			if !errors.Is(err, tc.want) {
				t.Fatalf("effective HOME check=%v, want %v", err, tc.want)
			}
		})
	}
}

func TestOwnerSuspensionPreservesPendingOperationsAndRetainedResources(t *testing.T) {
	for _, kind := range []api.AgentType{api.AgentTypeOpenClaw, api.AgentTypeHermes, api.AgentTypeCustom} {
		for _, suspended := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/suspended=%t", kind, suspended), func(t *testing.T) {
				ctx := context.Background()
				agent := restartableInstance()
				agent.Spec.Agent.Type, agent.Spec.Suspended = kind, !suspended
				agent.Spec.RestartRequestID = firstRestartID
				agent.Spec.PlannedMigration = &api.PlannedMigrationRequest{RequestID: "migration-00000001",
					Target: api.MigrationObject{Name: "destination", UID: "destination-uid"}}
				agent.Status.PlannedMigration = &api.PlannedMigrationStatus{
					PlannedMigrationRequest: *agent.Spec.PlannedMigration, Phase: api.MigrationStarting}
				home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "cetirun-alice", UID: "home-uid"}}
				key := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "model-key", Namespace: "cetirun-alice", UID: "key-uid"},
					Data: map[string][]byte{"key": []byte("synthetic-model-key")}}
				writes := 0
				lost := errors.New("synthetic response lost after persistence")
				fc := fake.NewClientBuilder().WithScheme(newScheme()).WithGlobalResourceVersionCounter().
					WithStatusSubresource(&api.Agent{}).WithObjects(agent, home, key, suspensionWorkload(agent)).
					WithInterceptorFuncs(interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						writes++
						if _, ok := obj.(*api.Agent); !ok {
							t.Fatalf("suspension wrote %T instead of only the Agent", obj)
						}
						if err := c.Patch(ctx, obj, patch, opts...); err != nil {
							return err
						}
						return lost
					}}).Build()
				for _, obj := range []client.Object{agent, home, key} {
					if err := fc.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
						t.Fatal(err)
					}
				}
				before := agent.DeepCopy()
				version := InstanceVersion{UID: agent.UID, ResourceVersion: agent.ResourceVersion}
				if _, err := NewClient(fc).SetInstanceSuspendedByName(ctx, agent.Name, version, suspended, nil); !errors.Is(err, lost) {
					t.Fatalf("uncertain write was not surfaced: %v", err)
				}
				current, err := NewClient(fc).SetInstanceSuspendedByName(ctx, agent.Name, version, suspended, nil)
				if err != nil || writes != 1 {
					t.Fatalf("retry wrote another operation: writes=%d err=%v", writes, err)
				}
				wantSpec := before.Spec.DeepCopy()
				wantSpec.Suspended = suspended
				if current.UID != before.UID || !reflect.DeepEqual(current.Spec, *wantSpec) ||
					!reflect.DeepEqual(current.Status, before.Status) || !reflect.DeepEqual(current.Annotations, before.Annotations) {
					t.Fatal("changed retained identity, unrelated intent or observed status")
				}
				if CanSuspendInstance(current) == suspended || CanResumeInstance(current) != suspended || CanRestartInstance(current) {
					t.Fatal("capabilities must permit reversing suspension, not another pending restart")
				}
				for _, obj := range []client.Object{home, key} {
					want := obj.DeepCopyObject()
					if err := fc.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil || !reflect.DeepEqual(want, obj) {
						t.Fatalf("retained HOME/key changed: %v", err)
					}
				}
				current.Spec.Suspended = !suspended
				if err := fc.Update(ctx, current); err != nil {
					t.Fatal(err)
				}
				if _, err := NewClient(fc).SetInstanceSuspendedByName(ctx, agent.Name, version, suspended, nil); !errors.Is(err, ErrInstanceChanged) || writes != 1 {
					t.Fatalf("late retry reversed newer intent: writes=%d err=%v", writes, err)
				}
			})
		}
	}
}

func TestOwnerSuspensionRejectsUnsafeRequests(t *testing.T) {
	denied := errors.New("synthetic owner denial")
	for _, tc := range []struct {
		name      string
		change    func(*api.Agent, *InstanceVersion)
		authorize func(*api.Agent) error
		want      error
	}{
		{"missing UID", func(_ *api.Agent, v *InstanceVersion) { v.UID = "" }, nil, ErrInstanceVersionRequired},
		{"missing version", func(_ *api.Agent, v *InstanceVersion) { v.ResourceVersion = "" }, nil, ErrInstanceVersionRequired},
		{"replaced Agent", func(_ *api.Agent, v *InstanceVersion) { v.UID = "old-uid" }, nil, ErrInstanceChanged},
		{"stale version", func(_ *api.Agent, v *InstanceVersion) { v.ResourceVersion = "old-version" }, nil, ErrInstanceChanged},
		{"foreign owner", nil, func(*api.Agent) error { return denied }, denied},
		{"foreign duplicate", func(a *api.Agent, _ *InstanceVersion) { a.Spec.Suspended = true }, func(*api.Agent) error { return denied }, denied},
		{"deleting duplicate", func(a *api.Agent, _ *InstanceVersion) {
			now := metav1.Now()
			a.Spec.Suspended, a.Finalizers, a.DeletionTimestamp = true, []string{api.Finalizer}, &now
		}, nil, ErrInstanceDeleting},
		{"legacy single-start", func(a *api.Agent, _ *InstanceVersion) {
			a.Finalizers = []string{api.SandboxPreparationFinalizer}
		}, nil, ErrSuspensionUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := restartableInstance()
			agent.ResourceVersion = "7"
			version := InstanceVersion{UID: agent.UID, ResourceVersion: agent.ResourceVersion}
			if tc.change != nil {
				tc.change(agent, &version)
			}
			fc := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(agent).Build()
			_, err := NewClient(fc).SetInstanceSuspendedByName(context.Background(), agent.Name, version, true, tc.authorize)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			current := &api.Agent{}
			if err := fc.Get(context.Background(), client.ObjectKeyFromObject(agent), current); err != nil ||
				!reflect.DeepEqual(current.Spec, agent.Spec) || current.ResourceVersion != agent.ResourceVersion {
				t.Fatalf("rejected request wrote the Agent: %v", err)
			}
			if errors.Is(tc.want, ErrInstanceDeleting) || errors.Is(tc.want, ErrSuspensionUnsupported) {
				if CanSuspendInstance(current) || CanResumeInstance(current) {
					t.Fatal("unavailable lifecycle action was advertised")
				}
			}
		})
	}
}

func TestOwnerSuspensionDoesNotRetryConcurrentChanges(t *testing.T) {
	for _, suspended := range []bool{true, false} {
		for _, deleting := range []bool{false, true} {
			t.Run(fmt.Sprintf("suspended=%t/deleting=%t", suspended, deleting), func(t *testing.T) {
				ctx := context.Background()
				agent := restartableInstance()
				agent.Spec.Suspended, agent.Finalizers = !suspended, []string{api.Finalizer}
				writes := 0
				fc := fake.NewClientBuilder().WithScheme(newScheme()).WithGlobalResourceVersionCounter().WithObjects(agent, suspensionWorkload(agent)).
					WithInterceptorFuncs(interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						writes++
						current := &api.Agent{}
						if err := c.Get(ctx, client.ObjectKeyFromObject(agent), current); err != nil {
							return err
						}
						if deleting {
							if err := c.Delete(ctx, current); err != nil {
								return err
							}
						} else {
							current.Spec.User.DisplayName = "Concurrent update"
							if err := c.Update(ctx, current); err != nil {
								return err
							}
						}
						return c.Patch(ctx, obj, patch, opts...)
					}}).Build()
				if err := fc.Get(ctx, client.ObjectKeyFromObject(agent), agent); err != nil {
					t.Fatal(err)
				}
				_, err := NewClient(fc).SetInstanceSuspendedByName(ctx, agent.Name,
					InstanceVersion{UID: agent.UID, ResourceVersion: agent.ResourceVersion}, suspended, nil)
				if !apierrors.IsConflict(err) || writes != 1 {
					t.Fatalf("concurrent change was retried or overwritten: writes=%d err=%v", writes, err)
				}
			})
		}
	}
}

func TestOwnerResumeDoesNotRequireMigrationCanStartOrObservedGeneration(t *testing.T) {
	for _, phase := range []string{"", api.MigrationProtecting, api.MigrationStopping, api.MigrationSourceTerminated, api.MigrationDetached, api.MigrationStarting, api.MigrationCompleted} {
		t.Run(phase, func(t *testing.T) {
			agent := restartableInstance()
			agent.Spec.Suspended, agent.ResourceVersion = true, "7"
			agent.Spec.PlannedMigration = &api.PlannedMigrationRequest{RequestID: "migration-00000001"}
			if phase != "" {
				agent.Status.PlannedMigration = &api.PlannedMigrationStatus{
					PlannedMigrationRequest: *agent.Spec.PlannedMigration, Phase: phase}
			}
			agent.Status.Phase, agent.Status.ObservedGeneration = api.PhaseSuspended, 0
			fc := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(agent).Build()
			current, err := NewClient(fc).SetInstanceSuspendedByName(context.Background(), agent.Name,
				InstanceVersion{UID: agent.UID, ResourceVersion: agent.ResourceVersion}, false, nil)
			if err != nil || current.Spec.Suspended || !reflect.DeepEqual(current.Spec.PlannedMigration, agent.Spec.PlannedMigration) ||
				!reflect.DeepEqual(current.Status, agent.Status) || CanRestartInstance(current) {
				t.Fatalf("resume stranded or advanced the original operation: %v", err)
			}
		})
	}
}

func TestOwnerResumeAdmissionFailureKeepsSuspensionAndFailedStatus(t *testing.T) {
	ctx := context.Background()
	agent := restartableInstance()
	agent.Spec.Suspended, agent.ResourceVersion = true, "7"
	agent.Spec.RestartRequestID = firstRestartID
	agent.Status.Phase, agent.Status.ObservedGeneration = api.PhaseFailed, 0
	agent.Status.Conditions[0].Status = metav1.ConditionFalse
	writes := 0
	fc := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(agent).
		WithInterceptorFuncs(interceptor.Funcs{Patch: func(_ context.Context, _ client.WithWatch, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
			writes++
			return apierrors.NewForbidden(schema.GroupResource{Group: api.GroupVersion.Group, Resource: "agents"},
				agent.Name, errors.New("synthetic current authorization denial"))
		}}).Build()
	if !CanResumeInstance(agent) {
		t.Fatal("failed status prevented a versioned resume attempt")
	}
	_, err := NewClient(fc).SetInstanceSuspendedByName(ctx, agent.Name,
		InstanceVersion{UID: agent.UID, ResourceVersion: agent.ResourceVersion}, false, nil)
	if !apierrors.IsForbidden(err) || writes != 1 {
		t.Fatalf("admission rejection was hidden or retried: writes=%d err=%v", writes, err)
	}
	current := &api.Agent{}
	if err := fc.Get(ctx, client.ObjectKeyFromObject(agent), current); err != nil ||
		!reflect.DeepEqual(current.Spec, agent.Spec) || !reflect.DeepEqual(current.Status, agent.Status) {
		t.Fatalf("rejected resume changed intent or claimed recovery: %v", err)
	}
}
