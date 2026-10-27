// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestSandboxSourceAccessIncludesPinnedCustomRuntimeSecret(t *testing.T) {
	const namespace = "cetirun-custom"
	attempt := sandboxSourceAccessAttempt(namespace, []string{"custom-runtime", "gateway-key", "approved-but-unpinned"}, []sandboxworkload.SourceReference{
		validSecretSource(namespace, "custom-runtime"),
		validSecretSource(namespace, "gateway-key"),
	})

	role, binding, _, err := sandboxSourceAccessReconciler().sandboxSourceAccess(attempt, corev1.PodSpec{
		Volumes: []corev1.Volume{{Name: "gateway-key", VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: "gateway-key"},
		}}},
	})
	if err != nil {
		t.Fatalf("build source-read grant for pinned Custom runtime Secret: %v", err)
	}
	if binding == nil || len(role.Rules) != 1 {
		t.Fatalf("unexpected source-read grant: role=%+v binding=%+v", role, binding)
	}
	rule := role.Rules[0]
	if got, want := rule.Resources, []string{"secrets"}; !sandboxSourceAccessEqualStrings(got, want) {
		t.Fatalf("source-read resources = %v, want %v", got, want)
	}
	if got, want := rule.ResourceNames, []string{"custom-runtime", "gateway-key"}; !sandboxSourceAccessEqualStrings(got, want) {
		t.Fatalf("source-read Secret names = %v, want %v", got, want)
	}
	if got, want := rule.Verbs, []string{"get"}; !sandboxSourceAccessEqualStrings(got, want) {
		t.Fatalf("source-read verbs = %v, want exact get only", got)
	}
	if strings.Contains(strings.Join(rule.Verbs, ","), "list") || strings.Contains(strings.Join(rule.Verbs, ","), "watch") {
		t.Fatalf("source-read grant unexpectedly permits list or watch: %+v", rule)
	}
}

func TestSandboxSourceAccessAcceptsPinnedImagePullSecretByExactIdentity(t *testing.T) {
	const namespace = "cetirun-custom"
	pullSecret := validSecretSource(namespace, "private-image-pull")
	attempt := sandboxSourceAccessAttempt(namespace, []string{"custom-runtime"}, []sandboxworkload.SourceReference{
		validSecretSource(namespace, "custom-runtime"), pullSecret,
	})
	attempt.input.ImagePullSecret = &sandboxworkload.ObjectIdentity{Name: pullSecret.Name, UID: pullSecret.UID}

	role, _, _, err := sandboxSourceAccessReconciler().sandboxSourceAccess(attempt)
	if err != nil {
		t.Fatalf("accept exact pinned image pull Secret: %v", err)
	}
	if len(role.Rules) != 1 || !sandboxSourceAccessEqualStrings(role.Rules[0].ResourceNames, []string{"custom-runtime", "private-image-pull"}) ||
		!sandboxSourceAccessEqualStrings(role.Rules[0].Verbs, []string{"get"}) {
		t.Fatalf("image pull Secret grant was not exact get-only access: %+v", role.Rules)
	}

	attempt.sources[1].UID = "replacement-pull-secret-uid"
	role, binding, _, err := sandboxSourceAccessReconciler().sandboxSourceAccess(attempt)
	if err == nil || !strings.Contains(err.Error(), "unapproved Secret source") || role != nil || binding != nil {
		t.Fatalf("same-name replacement image pull Secret was accepted: role=%+v binding=%+v error=%v", role, binding, err)
	}
}

func TestSandboxSourceAccessRejectsInvalidPinnedSecretIdentity(t *testing.T) {
	const namespace = "cetirun-custom"
	for _, test := range []struct {
		name       string
		authorized []string
		source     sandboxworkload.SourceReference
	}{
		{
			name: "unapproved", authorized: []string{"custom-runtime"},
			source: validSecretSource(namespace, "unapproved-secret"),
		},
		{
			name: "cross namespace", authorized: []string{"custom-runtime"},
			source: validSecretSource("other-namespace", "custom-runtime"),
		},
		{
			name: "incomplete identity", authorized: []string{"custom-runtime"},
			source: sandboxworkload.SourceReference{APIVersion: "v1", Kind: "Secret", Namespace: namespace, Name: "custom-runtime"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempt := sandboxSourceAccessAttempt(namespace, test.authorized, []sandboxworkload.SourceReference{test.source})
			role, binding, _, err := sandboxSourceAccessReconciler().sandboxSourceAccess(attempt)
			if err == nil || !strings.Contains(err.Error(), "unapproved Secret source") {
				t.Fatalf("invalid pinned Secret source returned role=%+v binding=%+v error=%v", role, binding, err)
			}
			if role != nil || binding != nil {
				t.Fatalf("invalid pinned Secret source emitted a grant: role=%+v binding=%+v", role, binding)
			}
		})
	}
}

func sandboxSourceAccessReconciler() *AgentReconciler {
	return &AgentReconciler{
		OperatorNamespace:  "cetirun-system",
		SandboxPreparation: &SandboxPreparation{AdmissionServiceAccount: "cetirun-sandbox-admission"},
	}
}

func sandboxSourceAccessAttempt(namespace string, authorized []string, sources []sandboxworkload.SourceReference) *sandboxPreparationAttempt {
	return &sandboxPreparationAttempt{
		agent: &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "custom", UID: types.UID("custom-agent-uid")}},
		input: sandboxworkload.Input{
			Namespace:         sandboxworkload.ObjectIdentity{Name: namespace, UID: types.UID("custom-namespace-uid")},
			AuthorizedSecrets: authorized,
		},
		sources: sources,
	}
}

func validSecretSource(namespace, name string) sandboxworkload.SourceReference {
	return sandboxworkload.SourceReference{
		APIVersion: "v1", Kind: "Secret", Namespace: namespace, Name: name,
		UID: types.UID(name + "-uid"), ResourceVersion: "1",
	}
}

func sandboxSourceAccessEqualStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
