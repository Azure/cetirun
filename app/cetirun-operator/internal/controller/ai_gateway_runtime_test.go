// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func gatewayRuntimePod(agentType api.AgentType) corev1.PodSpec {
	keyName := "OPENAI_API_KEY"
	if agentType == api.AgentTypeHermes {
		keyName = "CETIRUN_GATEWAY_API_KEY"
	}
	return corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "init-home-data", Env: []corev1.EnvVar{{Name: "CETIRUN_AI_GATEWAY", Value: "true"}}}},
		Containers: []corev1.Container{{Name: "cetirun", Env: []corev1.EnvVar{
			{Name: keyName, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: aiGatewayKeySecretName}, Key: aiGatewayKeySecretField}}},
			{Name: "OPENAI_BASE_URL", Value: testAIGatewayURL + "/v1"}, {Name: "CETIRUN_AI_GATEWAY", Value: "true"},
		}}},
	}
}

func TestGatewayRuntimeDetectsAppliedBindings(t *testing.T) {
	for _, kind := range []api.AgentType{api.AgentTypeOpenClaw, api.AgentTypeHermes, api.AgentTypeCustom} {
		t.Run(string(kind), func(t *testing.T) {
			spec := gatewayRuntimePod(kind)
			if !gatewayPodConfigured(spec, kind, testAIGatewayURL) {
				t.Fatal("valid Gateway workload rejected")
			}
			spec.Containers[0].Env = append(spec.Containers[0].Env, spec.Containers[0].Env[0])
			if gatewayPodConfigured(spec, kind, testAIGatewayURL) {
				t.Fatal("duplicate model key environment accepted")
			}
			spec = gatewayRuntimePod(kind)
			spec.Containers[0].Env[0].ValueFrom.SecretKeyRef.Optional = ptr.To(true)
			if gatewayPodConfigured(spec, kind, testAIGatewayURL) {
				t.Fatal("optional Gateway key accepted")
			}
		})
	}
}

func TestGatewayMigrationStopsOldPodDespiteRoutedStatus(t *testing.T) {
	for _, kind := range []api.AgentType{api.AgentTypeOpenClaw, api.AgentTypeHermes, api.AgentTypeCustom} {
		for _, templateRouted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/template-routed=%v", kind, templateRouted), func(t *testing.T) {
				ctx := context.Background()
				agent := aiGatewayTestAgent(testAIGatewayTeam)
				agent.Spec.Agent.Type = kind
				agent.Status.AIGateway = &api.AIGatewayStatus{Routed: true, KeyID: "existing-key", KeySecretName: aiGatewayKeySecretName}
				agent.Status.ObservedGeneration = 1
				agent.Status.ObservedOperatorHash = "old-config"
				r, kube, _ := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
				sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: instanceServiceName(agent.Name), Namespace: instanceNamespace(agent.Name), UID: "old-sts",
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(agent, api.GroupVersion.WithKind("Agent"))},
					Annotations:     map[string]string{customAgentUID: string(agent.UID)}},
					Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1), PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{WhenScaled: appsv1.DeletePersistentVolumeClaimRetentionPolicyType}}}
				if templateRouted {
					sts.Spec.Template.Spec = gatewayRuntimePod(kind)
				}
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: sts.Name + "-0", Namespace: sts.Namespace, OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: sts.Name, UID: sts.UID}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "direct-provider"}}}}
				for _, obj := range []client.Object{sts, pod} {
					if err := kube.Create(ctx, obj); err != nil {
						t.Fatal(err)
					}
				}
				if allowed, _, err := r.migrateDirectGatewayRuntime(ctx, agent); err != nil || allowed {
					t.Fatalf("old direct Pod survived guard: %v %v", allowed, err)
				}
				if err := kube.Get(ctx, client.ObjectKeyFromObject(sts), sts); err != nil {
					t.Fatal(err)
				}
				if *sts.Spec.Replicas != 0 || sts.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled != appsv1.RetainPersistentVolumeClaimRetentionPolicyType {
					t.Fatal("old workload not stopped with HOME retained")
				}
				if agent.Status.ObservedGeneration != 0 || agent.Status.ObservedOperatorHash != "" {
					t.Fatal("old skip cache survived")
				}
				if allowed, _, err := r.migrateDirectGatewayRuntime(ctx, agent); err != nil || allowed {
					t.Fatalf("guard must wait for old Pod disappearance: %v %v", allowed, err)
				}
				if err := kube.Delete(ctx, pod); err != nil {
					t.Fatal(err)
				}
				if allowed, _, err := r.migrateDirectGatewayRuntime(ctx, agent); err != nil || !allowed {
					t.Fatalf("stopped template prevents migration: %v %v", allowed, err)
				}
			})
		}
	}
}

func TestGatewayMigrationPreservesRoutedWorkloadDuringOutage(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	gateway.getErr = fmt.Errorf("synthetic management outage")
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: instanceServiceName(agent.Name), Namespace: instanceNamespace(agent.Name)}, Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1), Template: corev1.PodTemplateSpec{Spec: gatewayRuntimePod(agent.Spec.Agent.Type)}}}
	if err := kube.Create(context.Background(), sts); err != nil {
		t.Fatal(err)
	}
	if allowed, _, err := r.migrateDirectGatewayRuntime(context.Background(), agent); err != nil || !allowed {
		t.Fatalf("Gateway outage triggered direct migration: %v", err)
	}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(sts), sts); err != nil {
		t.Fatal(err)
	}
	if *sts.Spec.Replicas != 1 {
		t.Fatal("routed workload stopped")
	}
}

func TestGatewayMigrationNeverAdoptsForeignWorkload(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, _ := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: instanceServiceName(agent.Name), Namespace: instanceNamespace(agent.Name), OwnerReferences: []metav1.OwnerReference{{Kind: "Agent", UID: types.UID("other-agent")}}}, Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1)}}
	if err := kube.Create(context.Background(), sts); err != nil {
		t.Fatal(err)
	}
	if allowed, _, err := r.migrateDirectGatewayRuntime(context.Background(), agent); err == nil || allowed {
		t.Fatal("foreign workload was adopted")
	}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(sts), sts); err != nil {
		t.Fatal(err)
	}
	if *sts.Spec.Replicas != 1 {
		t.Fatal("foreign workload was modified")
	}
}

func TestCustomSuspensionPreservesGatewayKeyAndSpend(t *testing.T) {
	p, agent, gateway, b := gatewayCustomFixture(t)
	r := p.reconciler
	ctx := context.Background()
	if err := r.bindCustomGateway(ctx, agent, &b); err != nil {
		t.Fatal(err)
	}
	keyID := agent.Status.AIGateway.KeyID
	for _, key := range gateway.keys {
		key.Spend = ptr.To(4.25)
	}
	agent.Spec.Suspended = true
	if err := r.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := r.suspendGatewayKey(ctx, agent); err != nil {
		t.Fatal(err)
	}
	for _, key := range gateway.keys {
		if !key.Blocked || *key.Spend != 4.25 {
			t.Fatal("suspension replaced accounting or did not block key")
		}
	}
	agent.Spec.Suspended = false
	if err := r.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := r.bindCustomGateway(ctx, agent, &b); err != nil {
		t.Fatal(err)
	}
	if len(gateway.generated) != 1 || agent.Status.AIGateway.KeyID != keyID || agent.Status.AIGateway.SpendUSD != "4.25" {
		t.Fatal("resume replaced Gateway accounting")
	}
}

func TestGatewayMigrationHoldsOrphanedDirectPod(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, _ := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: instanceServiceName(agent.Name), Namespace: instanceNamespace(agent.Name), UID: "replacement-sts",
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(agent, api.GroupVersion.WithKind("Agent"))}},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1), Template: corev1.PodTemplateSpec{Spec: gatewayRuntimePod(api.AgentTypeOpenClaw)}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: sts.Name + "-0", Namespace: sts.Namespace, OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: sts.Name, UID: "previous-sts"}}}}
	for _, obj := range []client.Object{sts, pod} {
		if err := kube.Create(context.Background(), obj); err != nil {
			t.Fatal(err)
		}
	}
	if allowed, _, err := r.migrateDirectGatewayRuntime(context.Background(), agent); err != nil || allowed {
		t.Fatalf("orphaned direct Pod was ignored: %v %v", allowed, err)
	}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(pod), pod); err != nil {
		t.Fatal("guard must preserve unknown old Pod for recovery")
	}
}

func TestLegacyCustomConfigStopsDirectWorkloadBeforeGatewayOutage(t *testing.T) {
	p, agent, gateway, _ := gatewayCustomFixture(t)
	p.config.AgentManagedModel = true
	raw, _ := json.Marshal(p.config)
	cfg, err := ParseCustomRuntimeConfig(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	r := p.reconciler
	r.CustomBindings = NewKubernetesCustomBindings(r, cfg)
	if err := istiov1beta1.AddToScheme(r.Scheme); err != nil {
		t.Fatal(err)
	}
	gateway.getErr = fmt.Errorf("synthetic Gateway outage")
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: instanceServiceName(agent.Name), Namespace: instanceNamespace(agent.Name), UID: "direct-sts",
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(agent, api.GroupVersion.WithKind("Agent"))}, Annotations: map[string]string{customAgentUID: string(agent.UID)}},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1)}}
	if err := r.Create(context.Background(), sts); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(agent)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(sts), sts); err != nil {
		t.Fatal(err)
	}
	if *sts.Spec.Replicas != 0 {
		t.Fatal("old direct workload survived unavailable replacement configuration")
	}
	if len(gateway.generated) != 0 {
		t.Fatal("migration must precede key provisioning")
	}
}
