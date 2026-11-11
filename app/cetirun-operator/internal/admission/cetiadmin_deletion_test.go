// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/cetiadmin"
	"github.com/Azure/CetiRun/app/internal/registryauth"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCetiAdminExecBindingMutationAndDeletion(t *testing.T) {
	for _, actor := range []string{"administrator", "portal", "ordinary-writer", "operator", "unauthenticated-operator", "cetiadmin"} {
		for _, operation := range []string{"create", "add", "replace", "remove", "unchanged", "delete"} {
			if actor == "unauthenticated-operator" && (operation == "unchanged" || operation == "delete") {
				continue // Kubernetes authentication/RBAC rejects these before admission.
			}
			t.Run(actor+"/"+operation, func(t *testing.T) {
				f := newCetiAdminFixture(t)
				user := authenticationv1.UserInfo{Username: actor, Groups: []string{"system:authenticated"}}
				switch actor {
				case "operator", "unauthenticated-operator":
					user.Username = testController
					if actor == "unauthenticated-operator" {
						user.Groups = []string{"system:unauthenticated"}
					}
				case "cetiadmin":
					user = f.actor
				}
				old := f.target.DeepCopy()
				old.Spec.Suspended = true
				old.Spec.Helm.Version = "0.1.0"
				old.Annotations = map[string]string{registryauth.ExecAdminBindingAnnotation: string(adminJSON(t, registryauth.ExecAdminBinding{
					TargetAgentUID: old.UID, NamespaceUID: f.targetIdentity.Namespace.UID,
					Username: f.actor.Username, ServiceAccountUID: f.state.ServiceAccount.UID, SourceUID: f.state.Source.UID,
				}))}
				next := old.DeepCopy()
				op := admissionv1.Update
				want := actor == "operator"
				switch operation {
				case "create":
					op, old = admissionv1.Create, nil
				case "add":
					delete(old.Annotations, registryauth.ExecAdminBindingAnnotation)
				case "replace":
					next.Annotations[registryauth.ExecAdminBindingAnnotation] = "replacement"
				case "remove":
					delete(next.Annotations, registryauth.ExecAdminBindingAnnotation)
				case "unchanged":
					want = true
				case "delete":
					// The API server sends OldObject and no Object on DELETE.
					old.Annotations[nativeAuthRotationAnnotation] = "retained-rotation-12345678"
					op, next = admissionv1.Delete, nil
					want = actor != "cetiadmin"
				}
				var nextObject, oldObject client.Object
				if next != nil {
					nextObject = next
				}
				if old != nil {
					oldObject = old
				}
				response := f.guard.Validate(context.Background(), adminRequest(t, op, api.GroupVersion.Group, "Agent", "agents", nextObject, oldObject, user))
				if response.Allowed != want {
					t.Fatalf("allowed=%v, want %v: %v", response.Allowed, want, response.Result)
				}
			})
		}
	}
}

func TestCetiAdminDeletionPreservesAuthorityGuards(t *testing.T) {
	for _, target := range []string{"installation", "source", "invalid-installation", "stale-caller", "receipt-secret", "marked-secret"} {
		t.Run(target, func(t *testing.T) {
			f := newCetiAdminFixture(t)
			ctx := context.Background()
			user := authenticationv1.UserInfo{Username: "administrator"}
			var object client.Object = f.target
			group, kind, resource := api.GroupVersion.Group, "Agent", "agents"
			switch target {
			case "installation":
				agent := &api.Agent{}
				if err := f.kube.Get(ctx, client.ObjectKey{Name: f.state.Agent.Name}, agent); err != nil {
					t.Fatal(err)
				}
				object = agent
			case "source":
				group, kind, resource, object = "", "ConfigMap", "configmaps", f.source
			case "invalid-installation":
				checkpoint := &corev1.ConfigMap{}
				key := client.ObjectKey{Namespace: f.source.Namespace, Name: cetiadmin.InstallationName(f.source.Namespace, f.source.Name)}
				if err := f.kube.Get(ctx, key, checkpoint); err != nil {
					t.Fatal(err)
				}
				checkpoint.Data[cetiadmin.BindingKey] = "invalid"
				if err := f.kube.Update(ctx, checkpoint); err != nil {
					t.Fatal(err)
				}
			case "stale-caller":
				user = f.actor
				user.UID = "previous-service-account"
			case "receipt-secret", "marked-secret":
				group, kind, resource = "", "Secret", "secrets"
				annotations := map[string]string{nativeAuthRotationAnnotation: "retained-rotation-12345678"}
				if target == "marked-secret" {
					annotations[cetiadmin.AnnotationSourceUID] = string(f.source.UID)
				} else {
					user = f.actor
				}
				object = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: f.target.Spec.GatewayTokenSecretRef,
					Namespace: f.target.Status.Namespace, Annotations: annotations}, Type: corev1.SecretTypeOpaque}
			}
			response := f.guard.Validate(ctx, adminRequest(t, admissionv1.Delete, group, kind, resource, nil, object, user))
			if response.Allowed {
				t.Fatal("DELETE bypassed CetiAdmin authority protection")
			}
		})
	}
}

func TestCetiAdminRetainedRotationMarkerProtection(t *testing.T) {
	for _, kind := range []string{"Agent", "Secret"} {
		for _, mutation := range []string{"create", "replace", "remove", "delete"} {
			t.Run(kind+"/"+mutation, func(t *testing.T) {
				f := newCetiAdminFixture(t)
				group, resource := api.GroupVersion.Group, "agents"
				var old client.Object = f.target.DeepCopy()
				if kind == "Secret" {
					group, resource = "", "secrets"
					old = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "native-auth", Namespace: f.target.Status.Namespace}}
				}
				old.SetAnnotations(map[string]string{nativeAuthRotationAnnotation: "retained-rotation-12345678"})
				next := old.DeepCopyObject().(client.Object)
				operation := admissionv1.Update
				switch mutation {
				case "create":
					operation, old = admissionv1.Create, nil
				case "replace":
					next.SetAnnotations(map[string]string{nativeAuthRotationAnnotation: "replacement-rotation-12345678"})
				case "remove":
					next.SetAnnotations(nil)
				case "delete":
					operation, next = admissionv1.Delete, nil
				}
				user := authenticationv1.UserInfo{Username: "portal", Groups: []string{"system:authenticated"}}
				if kind == "Secret" {
					user.Username = "system:serviceaccount:kube-system:namespace-controller"
				}
				request := adminRequest(t, operation, group, kind, resource, next, old, user)
				response := f.guard.Validate(context.Background(), request)
				if want := mutation == "delete"; response.Allowed != want {
					t.Fatalf("allowed=%v, want %v: %v", response.Allowed, want, response.Result)
				}
			})
		}
	}
}

func TestCetiAdminLiveNamespaceSecretReceiptProtection(t *testing.T) {
	for _, mutation := range []string{"delete", "data", "create", "replace", "remove"} {
		t.Run(mutation, func(t *testing.T) {
			f := newCetiAdminFixture(t)
			ctx := context.Background()
			namespace := &corev1.Namespace{}
			if err := f.kube.Get(ctx, client.ObjectKey{Name: f.target.Status.Namespace}, namespace); err != nil {
				t.Fatal(err)
			}
			if !namespace.DeletionTimestamp.IsZero() {
				t.Fatal("this case requires a live namespace")
			}
			old := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: f.target.Spec.GatewayTokenSecretRef, Namespace: namespace.Name,
				Annotations: map[string]string{nativeAuthRotationAnnotation: "retained-rotation-12345678"}},
				Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"CETIRUN_AUTH_KEY": []byte("synthetic-key")}}
			next := old.DeepCopy()
			var nextObject, oldObject client.Object = next, old
			operation := admissionv1.Update
			switch mutation {
			case "delete":
				operation, nextObject = admissionv1.Delete, nil
			case "data":
				next.Data["CETIRUN_AUTH_KEY"] = []byte("synthetic-replacement")
			case "create":
				operation, oldObject = admissionv1.Create, nil
			case "replace":
				next.Annotations[nativeAuthRotationAnnotation] = "replacement-rotation-12345678"
			case "remove":
				next.Annotations = nil
			}
			user := authenticationv1.UserInfo{Username: "system:serviceaccount:operator-test:broker", Groups: []string{"system:authenticated"}}
			response := f.guard.Validate(ctx, adminRequest(t, operation, "", "Secret", "secrets", nextObject, oldObject, user))
			if want := mutation == "delete" || mutation == "data"; response.Allowed != want {
				t.Fatalf("allowed=%v, want %v: %v", response.Allowed, want, response.Result)
			}
		})
	}
}
