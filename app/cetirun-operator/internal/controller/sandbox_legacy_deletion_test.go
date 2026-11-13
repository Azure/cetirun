// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestHeldLegacyBuiltinDeletion(t *testing.T) {
	for _, mutation := range []string{"owned", "namespace-owner", "release", "release-namespace", "workload-manager", "workload-owner", "workload-uid", "route-owner", "route-label", "live-hold", "bootstrap", "sandbox-status", "sandbox-selection"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			r, agent, _ := legacyGatewayFixture(t)
			r.AIGateway = AIGatewayConfig{}
			agent.Spec.Agent.Type = api.AgentTypeHermes
			agent.Finalizers = append(agent.Finalizers, sandbox.HoldFinalizer)
			if mutation == "bootstrap" {
				agent.Finalizers = append(agent.Finalizers, sandbox.BootstrapFinalizer)
			}
			if mutation == "sandbox-selection" {
				agent.Spec.SandboxRef = "governed"
			}
			if err := r.Update(ctx, agent); err != nil {
				t.Fatal(err)
			}
			if mutation == "sandbox-status" {
				agent.Status.Sandbox = &api.SandboxStatus{}
				if err := r.Status().Update(ctx, agent); err != nil {
					t.Fatal(err)
				}
			}
			ns := &corev1.Namespace{}
			if err := r.Get(ctx, client.ObjectKey{Name: instanceNamespace(agent.Name)}, ns); err != nil {
				t.Fatal(err)
			}
			ns.Finalizers = []string{"test.example/namespace-controller"}
			if mutation == "namespace-owner" {
				ns.OwnerReferences[0].UID = "foreign-agent"
			}
			if err := r.Update(ctx, ns); err != nil {
				t.Fatal(err)
			}
			workload := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
				Name: instanceServiceName(agent.Name), Namespace: ns.Name, UID: "workload-uid",
				Labels:      map[string]string{"app.kubernetes.io/managed-by": "Helm"},
				Annotations: map[string]string{"meta.helm.sh/release-name": helmReleaseName(agent.Name), "meta.helm.sh/release-namespace": ns.Name},
			}, Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1), PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{WhenScaled: appsv1.DeletePersistentVolumeClaimRetentionPolicyType}}}
			route := &istiov1beta1.VirtualService{ObjectMeta: metav1.ObjectMeta{
				Name: virtualServiceName(agent.Name), Namespace: ns.Name, UID: "route-uid",
				Labels: map[string]string{"app.kubernetes.io/managed-by": "cetirun-operator", "app.kubernetes.io/instance": agent.Name},
			}}
			switch mutation {
			case "release":
				workload.Annotations["meta.helm.sh/release-name"] = "foreign"
			case "release-namespace":
				workload.Annotations["meta.helm.sh/release-namespace"] = "foreign"
			case "workload-manager":
				workload.Labels["app.kubernetes.io/managed-by"] = "foreign"
			case "workload-uid":
				workload.Annotations[customAgentUID] = "foreign-agent"
			case "workload-owner":
				workload.OwnerReferences = []metav1.OwnerReference{{Kind: "Agent", Name: agent.Name, UID: "foreign-agent"}}
			case "route-owner":
				route.OwnerReferences = []metav1.OwnerReference{{Kind: "Agent", Name: agent.Name, UID: "foreign-agent"}}
			case "route-label":
				route.Labels["app.kubernetes.io/instance"] = "foreign"
			}
			for _, obj := range []client.Object{workload, route} {
				if err := r.Create(ctx, obj); err != nil {
					t.Fatal(err)
				}
			}
			if mutation == "live-hold" {
				if err := r.stopSandboxBuiltinWorkload(ctx, agent); err == nil {
					t.Fatal("legacy deletion exception reached a live hold")
				}
				return
			}
			if err := r.Delete(ctx, agent); err != nil {
				t.Fatal(err)
			}
			_, err := r.deleteSandboxBuiltin(ctx, agent)
			if mutation != "owned" {
				if err == nil {
					t.Fatal("unconfirmed legacy cleanup must stay held")
				}
				if err := r.Get(ctx, client.ObjectKeyFromObject(ns), ns); err != nil || !ns.DeletionTimestamp.IsZero() {
					t.Fatal("namespace cleanup escaped ownership/lifetime checks", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(workload), workload); err != nil {
				t.Fatal(err)
			}
			if *workload.Spec.Replicas != 0 || workload.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled != appsv1.RetainPersistentVolumeClaimRetentionPolicyType {
				t.Fatal("workload did not stop with retained storage")
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(route), route); !apierrors.IsNotFound(err) {
				t.Fatal("owned route survived cleanup", err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(ns), ns); err != nil || ns.DeletionTimestamp.IsZero() {
				t.Fatal("namespace deletion was not requested", err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(agent), agent); err != nil || len(agent.Finalizers) != 2 {
				t.Fatal("Agent finalized before namespace deletion", err)
			}
			ns.Finalizers = nil
			if err := r.Update(ctx, ns); err != nil {
				t.Fatal(err)
			}
			if _, err := r.deleteSandboxBuiltin(ctx, agent); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(agent), agent); !apierrors.IsNotFound(err) {
				t.Fatal("Agent did not finalize after namespace deletion", err)
			}
		})
	}
}
