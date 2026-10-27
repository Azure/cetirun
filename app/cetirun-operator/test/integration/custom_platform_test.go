// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package integration_test

import (
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	"helm.sh/helm/v3/pkg/chart/loader"
	corev1 "k8s.io/api/core/v1"
)

func TestCustomUsesTheCommonSandboxContract(t *testing.T) {
	fixture := testfixture.New("openclaw")
	input := fixture.Input
	input.AgentType, input.Reconciled = "custom", true
	input.Helpers.Doctor = false
	input.Resolution.AgentPolicy.Harnesses.Allowed = append(input.Resolution.AgentPolicy.Harnesses.Allowed, api.AgentTypeCustom)
	input.GatewayAPIBase = "http://gateway.example.svc.cluster.local:4000/v1"
	input.CustomAgent = &w.CustomAgent{
		Image: "registry.example.test/worker@sha256:" + strings.Repeat("a", 64), Username: "worker", Home: "/home/worker",
		Environment: []w.CustomEnvironment{
			{Name: "HOME", Value: "/home/worker"},
			{Name: "OPENAI_BASE_URL", Value: input.GatewayAPIBase},
			{Name: "OPENAI_API_KEY", CredentialSlot: "model"},
		},
	}
	bundle, err := w.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	chrt, err := loader.Load("../../../cetirun-chart")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{
		"agentType": "custom", "agentRequest": map[string]any{"type": "custom"},
		"secrets": map[string]any{"existingSecret": "runtime-secret", "provider": "openai", "model": "example-model", "apiBase": input.GatewayAPIBase},
		"aiGateway": map[string]any{
			"enabled": true, "keySecretName": input.GatewayKey.Name, "keySecretKey": "api-key",
			"models": []any{map[string]any{"id": "example-model", "name": "Example"}},
		},
	}
	prepared, err := helm.PrepareSandboxChart(chrt, values, nil, bundle)
	if err != nil {
		t.Fatal(err)
	}
	template, err := prepared.SandboxTemplate()
	if err != nil {
		t.Fatal(err)
	}
	main := template.Spec.Containers[0]
	if main.Image != input.CustomAgent.Image {
		t.Fatalf("Custom image = %q, want %q", main.Image, input.CustomAgent.Image)
	}
	if !hasSecretEnv(main.Env, "OPENAI_API_KEY", input.GatewayKey.Name, "api-key") {
		t.Fatal("Custom workload did not project the authorized Gateway key")
	}
	if hasSecretEnv(main.Env, "MODEL_API_KEY", "runtime-secret", "MODEL_API_KEY") {
		t.Fatal("Custom workload retained a direct model credential")
	}
}

func hasSecretEnv(env []corev1.EnvVar, name, secret, key string) bool {
	for _, value := range env {
		if value.Name == name && value.ValueFrom != nil && value.ValueFrom.SecretKeyRef != nil &&
			value.ValueFrom.SecretKeyRef.Name == secret && value.ValueFrom.SecretKeyRef.Key == key {
			return true
		}
	}
	return false
}
