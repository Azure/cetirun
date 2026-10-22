// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package kube

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestRetainedRestartThroughServedAPI(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	for _, name := range []string{"kube-apiserver", "etcd"} {
		info, err := os.Stat(filepath.Join(assets, name))
		if assets == "" || err != nil || info.IsDir() || info.Mode()&0111 == 0 {
			t.Fatal("set KUBEBUILDER_ASSETS to preinstalled isolated test binaries")
		}
	}
	env := &envtest.Environment{BinaryAssetsDirectory: assets, UseExistingCluster: ptr.To(false),
		CRDDirectoryPaths: []string{"../../../cetirun-operator/config/crd/bases"}, ErrorIfCRDPathMissing: true,
		ControlPlaneStartTimeout: 30 * time.Second, ControlPlaneStopTimeout: 30 * time.Second}
	config, err := env.Start()
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	kc, err := client.New(config, client.Options{Scheme: newScheme()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	agent := restartableInstance()
	agent.UID, agent.ResourceVersion, agent.Generation, agent.Status = "", "", 0, api.AgentStatus{}
	if err := kc.Create(ctx, agent, client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		t.Fatal(err)
	}
	agent.Status = api.AgentStatus{Phase: api.PhaseReady, ObservedGeneration: agent.Generation,
		Conditions: []metav1.Condition{{Type: api.ConditionReady, Status: metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(), Reason: "TestReady", Message: "Synthetic readiness for API validation"}}}
	if err := kc.Status().Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	version := InstanceVersion{UID: agent.UID, ResourceVersion: agent.ResourceVersion}
	startGeneration := agent.Generation
	current, err := NewClient(kc).RestartInstanceByName(ctx, agent.Name, version, firstRestartID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if current.UID != agent.UID || current.Generation != startGeneration+1 || current.Spec.RestartRequestID != firstRestartID ||
		current.Status.ObservedRestartRequestID != "" {
		t.Fatal("served API did not persist a pending restart on the existing Agent")
	}
	retried, err := NewClient(kc).RestartInstanceByName(ctx, agent.Name, version, firstRestartID, nil)
	if err != nil || retried.ResourceVersion != current.ResourceVersion || retried.Generation != current.Generation {
		t.Fatalf("Portal restart retry wrote a second request: %v", err)
	}
	current.Spec.Suspended = true
	if err := kc.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(kc).RestartInstanceByName(ctx, agent.Name, version, firstRestartID, nil); !errors.Is(err, ErrInstanceSuspended) {
		t.Fatalf("duplicate bypassed suspension: %v", err)
	}
	for _, id := range []string{secondRestartID, "", "invalid"} {
		change := current.DeepCopy()
		change.Spec.RestartRequestID = id
		if err := kc.Update(ctx, change, client.FieldValidation(metav1.FieldValidationStrict)); !apierrors.IsInvalid(err) {
			t.Fatalf("served validation accepted restart ID %q during suspension: %v", id, err)
		}
	}
	if err := kc.Delete(ctx, current, client.Preconditions{UID: &current.UID, ResourceVersion: &current.ResourceVersion}); err != nil {
		t.Fatal(err)
	}
}
