// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package controller

import (
	"context"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestReconciledSandboxKubernetesRejectedGrantRecoveryAPIServer(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		t.Run(kind, func(t *testing.T) {
			h := newKubernetesAccessAPIHarness(t, kind)
			initial := reconciledExpectationForTest(t, h.nativeSandboxFixture)
			writerRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "grant-create-example"},
				Rules: []rbacv1.PolicyRule{{
					APIGroups: []string{rbacv1.GroupName}, Resources: []string{"rolebindings", "clusterrolebindings"},
					Verbs: []string{"create"},
				}},
			}
			h.create(t, writerRole)
			h.create(t, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: writerRole.Name},
				RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: writerRole.Name},
				Subjects: []rbacv1.Subject{{
					APIGroup: rbacv1.GroupName, Kind: "User", Name: "grant-writer-example",
				}},
			})
			config := rest.CopyConfig(h.localHelm.config)
			config.Impersonate = rest.ImpersonationConfig{
				UserName: "grant-writer-example", Groups: []string{"system:authenticated"},
			}
			writer, err := client.NewWithWatch(config, client.Options{Scheme: h.admin.Scheme()})
			if err != nil {
				t.Fatal(err)
			}
			installer := h.r.HelmClient.(*sharedSandboxInstaller)
			var rejectedKind string
			installer.kube = interceptor.NewClient(h.kube, interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
					if binding, ok := object.(*unstructured.Unstructured); ok && w.KubernetesBindingResource(*binding) {
						err := writer.Create(ctx, object, opts...)
						if apierrors.IsForbidden(err) {
							rejectedKind = binding.GetKind()
						}
						return err
					}
					return c.Create(ctx, object, opts...)
				},
			})
			for _, bindingKind := range []string{"RoleBinding", "ClusterRoleBinding"} {
				t.Run(bindingKind, func(t *testing.T) {
					agent := h.current(t)
					agent.Spec.Suspended = true
					if err := h.admin.Update(h.ctx, agent); err != nil {
						t.Fatal(err)
					}
					if _, err := h.reconcile(); err != nil && !strings.Contains(err.Error(), "waits for acknowledged controller shutdown") {
						t.Fatal(err)
					}
					workload := h.workload(t)
					if workload.Spec.Replicas == nil || *workload.Spec.Replicas != 0 {
						t.Fatal("controller did not request a stop before acknowledgement")
					}
					// Envtest has no StatefulSet controller; acknowledge only the requested stop.
					workload.Status.ObservedGeneration = workload.Generation
					workload.Status.Replicas, workload.Status.CurrentReplicas, workload.Status.ReadyReplicas = 0, 0, 0
					workload.Status.UpdatedReplicas = 0
					if err := h.admin.Status().Update(h.ctx, workload); err != nil {
						t.Fatal(err)
					}
					for range 3 {
						h.reconcileOK(t)
					}
					if h.current(t).Status.Phase != api.PhaseSuspended {
						t.Fatal("served fixture must resume an already-stopped Agent")
					}
					assertKubernetesFixtureGrants(t, h.nativeSandboxFixture, false)

					deniedRole, otherRole := "kubernetes-pods-reader", "kubernetes-nodes-reader"
					bindingNamespace := "operations"
					if bindingKind == "ClusterRoleBinding" {
						deniedRole, otherRole = otherRole, deniedRole
						bindingNamespace = ""
					}
					writerRole.Rules = append(writerRole.Rules[:1], rbacv1.PolicyRule{
						APIGroups: []string{rbacv1.GroupName}, Resources: []string{"clusterroles"},
						ResourceNames: []string{otherRole}, Verbs: []string{"bind"},
					})
					if err := h.admin.Update(h.ctx, writerRole); err != nil {
						t.Fatal(err)
					}
					access := authorizationv1.ResourceAttributes{
						Group: rbacv1.GroupName, Resource: "clusterroles", Name: deniedRole,
						Namespace: bindingNamespace, Verb: "bind",
					}
					h.assertAccess(t, writer, access, false)
					rejectedKind = ""
					agent = h.current(t)
					agent.Spec.Suspended = false
					if err := h.admin.Update(h.ctx, agent); err != nil {
						t.Fatal(err)
					}
					var failed error
					for range 4 {
						_, failed = h.reconcile()
						if failed != nil {
							break
						}
					}
					if !apierrors.IsForbidden(failed) || rejectedKind != bindingKind ||
						!strings.Contains(failed.Error(), "attempting to grant RBAC permissions") {
						t.Fatal("real API RBAC did not reject the grant for missing binding authority:", failed)
					}
					recorded, err := h.r.sandboxGrantJournal().Read(h.ctx, agent.Name, agent.UID)
					if err != nil || len(unconfirmedKubernetesGrants(recorded)) != 0 {
						t.Fatal("served rejection retained a submitted non-creation outcome:", err)
					}
					writerRole.Rules[1].ResourceNames = append(writerRole.Rules[1].ResourceNames, deniedRole)
					if err := h.admin.Update(h.ctx, writerRole); err != nil {
						t.Fatal(err)
					}
					h.assertAccess(t, writer, access, true)
					for range 5 {
						h.reconcileOK(t)
					}
					assertKubernetesFixtureGrants(t, h.nativeSandboxFixture, true)
					current := reconciledExpectationForTest(t, h.nativeSandboxFixture)
					if current.Agent != initial.Agent || current.Namespace != initial.Namespace || current.Home != initial.Home {
						t.Fatal("served rejection recovery replaced the lifetime")
					}
				})
			}
		})
	}
}
