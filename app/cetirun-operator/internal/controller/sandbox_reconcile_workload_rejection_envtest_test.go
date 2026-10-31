// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package controller

import (
	"context"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestContainerSandboxFirstWorkloadRejectionAPIServer(t *testing.T) {
	h := newUnreconciledKubernetesAccessAPIHarnessWithRuntime(t, "openclaw", api.SandboxContainer)
	config := rest.CopyConfig(h.localHelm.config)
	config.Impersonate = rest.ImpersonationConfig{UserName: "unprivileged-workload-writer", Groups: []string{"system:authenticated"}}
	writer, err := client.NewWithWatch(config, client.Options{Scheme: h.admin.Scheme()})
	if err != nil {
		t.Fatal(err)
	}
	installer := h.r.HelmClient.(*sharedSandboxInstaller)
	original := installer.kube
	grants, rejected := 0, false
	installer.kube = interceptor.NewClient(h.kube, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
			kind := object.GetObjectKind().GroupVersionKind().Kind
			if kind == "StatefulSet" {
				err := writer.Create(ctx, object, opts...)
				rejected = apierrors.IsForbidden(err)
				return err
			}
			err := c.Create(ctx, object, opts...)
			if err == nil && (kind == "RoleBinding" || kind == "ClusterRoleBinding") {
				grants++
			}
			return err
		},
	})
	var failed error
	for range 5 {
		_, failed = h.reconcile()
		if failed != nil {
			break
		}
	}
	if !apierrors.IsForbidden(failed) || !rejected || grants < 2 {
		t.Fatal("API server did not reject the first StatefulSet after binding success:", failed)
	}
	expected := reconciledExpectationForTest(t, h.nativeSandboxFixture)
	state := &corev1.ConfigMap{}
	if err := h.admin.Get(h.ctx, client.ObjectKey{Namespace: h.r.OperatorNamespace,
		Name: w.PodExpectationName(expected.Namespace.Name, expected.Namespace.UID)}, state); err != nil ||
		state.Annotations[sandboxApplyStateAnnotation] != sandboxApplyCancelled {
		t.Fatal("served rejection did not record a fenced cancellation:", err)
	}
	if _, found, err := h.r.SandboxNativeCoordination.ReadNative(h.ctx, h.agent.UID); err != nil || found {
		t.Fatal("served rejection retained the stopping reservation:", err)
	}
	if err := h.admin.Get(h.ctx, client.ObjectKey{Namespace: expected.Namespace.Name,
		Name: instanceServiceName(h.agent.Name)}, &appsv1.StatefulSet{}); !apierrors.IsNotFound(err) {
		t.Fatal("rejected CREATE nevertheless produced a workload:", err)
	}
	installer.kube = original
	for range 5 {
		h.reconcileOK(t)
	}
	assertKubernetesFixtureGrants(t, h.nativeSandboxFixture, true)
	current := reconciledExpectationForTest(t, h.nativeSandboxFixture)
	if current.Agent != expected.Agent || current.Namespace != expected.Namespace || current.Home != expected.Home {
		t.Fatal("restored workload authority replaced the retained lifetime")
	}
}
