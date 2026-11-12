// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload_test

import (
	"encoding/json"
	"strings"
	"testing"

	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestImmutableSecretSourcesPinUIDOnly(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "client-tls", UID: "client-uid", ResourceVersion: "3"},
		Data:       map[string][]byte{"tls.key": []byte("synthetic")},
	}
	if _, err := w.ImmutableSecretReference(secret); err == nil {
		t.Fatal("a mutable Secret received a UID-only pin")
	}
	secret.Immutable = ptr.To(true)
	source, err := w.ImmutableSecretReference(secret)
	if err != nil || !source.ValidIdentity() || source.ResourceVersion != "" || !source.ImmutableSecret {
		t.Fatal("immutable Secret was not UID pinned:", source, err)
	}
	raw, err := json.Marshal(source)
	if err != nil || strings.Contains(string(raw), "synthetic") || strings.Contains(string(raw), "resourceVersion") {
		t.Fatal("UID-only Secret source serialized data or a version:", string(raw))
	}
	secret.ResourceVersion, secret.Labels = "4", map[string]string{"example.com/owner": "platform"}
	if !w.ImmutableSecretMatches(source, secret) {
		t.Fatal("a metadata change broke the UID-only Secret pin")
	}
	replaced := secret.DeepCopy()
	replaced.UID = "replacement-uid"
	mutable := secret.DeepCopy()
	mutable.Immutable = nil
	for _, live := range []*corev1.Secret{replaced, mutable} {
		if w.ImmutableSecretMatches(source, live) {
			t.Fatal("UID-only Secret pin accepted a replaced or mutable Secret")
		}
	}
	for _, invalid := range []w.SourceReference{
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: "platform", Name: "x", UID: "u", ImmutableSecret: true},
		{APIVersion: "v1", Kind: "Secret", Namespace: "platform", Name: "x", UID: "u", ResourceVersion: "1", ImmutableSecret: true},
		{APIVersion: "v1", Kind: "Secret", Namespace: "platform", Name: "x", UID: "u"},
	} {
		if invalid.ValidIdentity() {
			t.Fatal("invalid source identity accepted:", invalid)
		}
	}
	if !source.MatchesApproval("platform", "client-tls", "client-uid", "") ||
		source.MatchesApproval("platform", "client-tls", "client-uid", "3") {
		t.Fatal("UID-only Secret source does not follow its approval form")
	}
	versioned := w.SourceReference{APIVersion: "v1", Kind: "Secret", Namespace: "platform", Name: "client-tls", UID: "client-uid", ResourceVersion: "3"}
	if !versioned.MatchesApproval("platform", "client-tls", "client-uid", "3") ||
		versioned.MatchesApproval("platform", "client-tls", "client-uid", "") {
		t.Fatal("a version-pinned Secret source satisfied a UID-only approval or lost its exact match")
	}
	gateway := w.SourceReference{APIVersion: "networking.istio.io/v1beta1", Kind: "Gateway", Namespace: "platform", Name: "ingress", UID: "gw", ResourceVersion: "9"}
	if !gateway.MatchesApproval("platform", "ingress", "gw", "") || gateway.MatchesApproval("platform", "ingress", "gw", "8") {
		t.Fatal("Gateway source does not follow its approval form")
	}
}
