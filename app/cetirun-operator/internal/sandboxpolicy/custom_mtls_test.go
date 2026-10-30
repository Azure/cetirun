// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxpolicy

import (
	"slices"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/ingressmtls"
	corev1 "k8s.io/api/core/v1"
)

func TestCustomMTLSRequiresExactClientAndBlocksPlaintext(t *testing.T) {
	input := ingressMTLSFixture(t, "custom")
	input.Resolution.AgentPolicy.Harnesses.Allowed = []api.AgentType{api.AgentTypeCustom}
	input.CustomIngress = &CustomIngress{Port: 8080, ChatCallers: []IngressCaller{{
		Namespace: "portal", PodLabels: map[string]string{"app": "portal"},
	}}}
	binding := &input.Platform.IngressMTLS.Bindings[0]
	binding.Port, binding.Terminal = 8080, false
	binding.ChatClients = []ingressmtls.ChatClient{{
		Secret: ingressmtls.Reference{Namespace: "portal", Name: "chat-client", UID: "chat-client-uid", ResourceVersion: "1"},
		URI:    "spiffe://example.com/portal/" + string(binding.AgentUID), PodLabels: map[string]string{"app": "portal"},
	}}
	plan := mustCompile(t, input)
	spec := plan.Network.Policy.Spec
	spec.Ingress = append(slices.Clone(spec.Ingress), IngressRule{FromEntities: []string{"all"}})
	portal := map[string]string{"k8s:io.kubernetes.pod.namespace": "portal", "k8s:app": "portal"}
	if !syntheticIngressAllowed(spec, portal, "", ingressmtls.APIPort, corev1.ProtocolTCP) ||
		syntheticIngressAllowed(spec, portal, "", 8080, corev1.ProtocolTCP) {
		t.Fatal("Custom Chat can bypass TLS or cannot reach its approved listener")
	}
	portal["k8s:app"] = "other"
	if syntheticIngressAllowed(spec, portal, "", ingressmtls.APIPort, corev1.ProtocolTCP) {
		t.Fatal("unapproved Portal workload acquired ingress")
	}
	binding.ChatClients[0].PodLabels["app"] = "other"
	if _, err := Compile(input); err == nil {
		t.Fatal("mismatched client approval accepted")
	}
	binding.ChatClients = nil
	if _, err := Compile(input); err == nil {
		t.Fatal("Custom Chat was allowed to borrow Gateway credentials")
	}
}
