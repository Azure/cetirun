// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	writerExclusionAgentName = "example"
	writerExclusionNamespace = "cetirun-example"
	writerExclusionWorkload  = "openclaw-example-cetirun"
)

type kubernetesWriterExclusionFixture struct {
	agent         *api.Agent
	namespace     *corev1.Namespace
	home          *corev1.PersistentVolumeClaim
	workload      *appsv1.StatefulSet
	prerequisites controller.SandboxPrerequisites
}

func newKubernetesWriterExclusionFixture() *kubernetesWriterExclusionFixture {
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{
		Name: writerExclusionAgentName, UID: "agent-uid", Generation: 1,
	}}
	owner := []metav1.OwnerReference{sandboxworkload.AgentOwner(agent.Name, agent.UID)}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: writerExclusionNamespace, UID: "namespace-uid", ResourceVersion: "1",
		OwnerReferences: owner,
		Annotations: map[string]string{
			sandboxworkload.AgentUIDAnnotation: string(agent.UID),
		},
	}}
	home := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "home", Namespace: namespace.Name, UID: "home-uid", ResourceVersion: "1",
			Annotations: map[string]string{sandboxworkload.AgentUIDAnnotation: string(agent.UID)},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod},
		},
	}
	workload := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: writerExclusionWorkload, Namespace: namespace.Name, UID: "workload-uid", ResourceVersion: "1",
			OwnerReferences: owner,
			Annotations:     map[string]string{sandboxworkload.AgentUIDAnnotation: string(agent.UID)},
		},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(0))},
	}
	return &kubernetesWriterExclusionFixture{
		agent: agent, namespace: namespace, home: home, workload: workload,
		prerequisites: controller.SandboxPrerequisites{
			AgentUID: agent.UID, AgentGeneration: agent.Generation, NamespaceUID: namespace.UID,
			Home: sandboxworkload.ObjectIdentity{Name: home.Name, UID: home.UID},
		},
	}
}

func (f *kubernetesWriterExclusionFixture) deleting() {
	now := metav1.NewTime(time.Date(2026, time.October, 9, 0, 0, 0, 0, time.UTC))
	f.agent.DeletionTimestamp = &now
}

func (f *kubernetesWriterExclusionFixture) terminatingNamespace() {
	f.namespace.DeletionTimestamp = f.agent.DeletionTimestamp.DeepCopy()
	f.namespace.Finalizers = []string{sandbox.BootstrapFinalizer}
}

func (f *kubernetesWriterExclusionFixture) check(objects ...client.Object) error {
	reader := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(objects...).Build()
	source := &controller.KubernetesRWOPWriterExclusion{APIReader: reader}
	return source.CheckQuiesced(context.Background(), f.agent, f.prerequisites)
}

func TestKubernetesRWOPWriterExclusionRequiresHomeOutsideDeletionGC(t *testing.T) {
	for name, deleting := range map[string]bool{
		"running Agent":                        false,
		"deleting Agent with active namespace": true,
	} {
		t.Run(name, func(t *testing.T) {
			f := newKubernetesWriterExclusionFixture()
			if deleting {
				f.deleting()
			}
			if err := f.check(f.namespace); !apierrors.IsNotFound(err) {
				t.Fatalf("missing HOME did not fail closed: %v", err)
			}
		})
	}
}

func TestKubernetesRWOPWriterExclusionAllowsTerminatingNamespaceAfterHomeGC(t *testing.T) {
	f := newKubernetesWriterExclusionFixture()
	f.deleting()
	f.terminatingNamespace()
	if err := f.check(f.namespace, f.workload); err != nil {
		t.Fatalf("terminating owned namespace with no HOME or Pod was not quiescent: %v", err)
	}
}

func TestKubernetesRWOPWriterExclusionAllowsDeletedNamespaceCleanup(t *testing.T) {
	f := newKubernetesWriterExclusionFixture()
	f.deleting()
	if err := f.check(); err != nil {
		t.Fatalf("fully deleted namespace did not prove cleanup quiescence: %v", err)
	}
}

func TestKubernetesRWOPWriterExclusionDeletionRecoveryStillRequiresStoppedWorkloadAndNoPods(t *testing.T) {
	tests := map[string]func(*kubernetesWriterExclusionFixture) []client.Object{
		"active workload": func(f *kubernetesWriterExclusionFixture) []client.Object {
			f.workload.Spec.Replicas = ptr.To(int32(1))
			return []client.Object{f.namespace, f.workload}
		},
		"remaining Pod": func(f *kubernetesWriterExclusionFixture) []client.Object {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name: writerExclusionWorkload + "-0", Namespace: f.namespace.Name, UID: types.UID("pod-uid"),
			}}
			return []client.Object{f.namespace, f.workload, pod}
		},
	}
	for name, objects := range tests {
		t.Run(name, func(t *testing.T) {
			f := newKubernetesWriterExclusionFixture()
			f.deleting()
			f.terminatingNamespace()
			if err := f.check(objects(f)...); err == nil {
				t.Fatal("deletion recovery accepted a possible writer")
			}
		})
	}
}

func TestKubernetesRWOPWriterExclusionNormalStoppedWorkload(t *testing.T) {
	f := newKubernetesWriterExclusionFixture()
	if err := f.check(f.namespace, f.home, f.workload); err != nil {
		t.Fatalf("exact RWOP HOME and stopped workload were rejected: %v", err)
	}
}
