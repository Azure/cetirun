// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxpolicy

import (
	"fmt"
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/ingressmtls"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func mtlsDestination(destination Destination, plan ingressmtls.Plan) (Destination, error) {
	if destination.Peer.Namespace != plan.Gateway.Namespace || len(destination.Peer.PodSelector) == 0 {
		return destination, fmt.Errorf("%w: mTLS requires the approved Gateway's explicit ingress Pod selector", ErrConflict)
	}
	ports := make([]api.AgentPolicyPort, 0, len(plan.Ports()))
	for _, port := range plan.Ports() {
		if !slices.ContainsFunc(destination.Ports, func(allowed api.AgentPolicyPort) bool {
			return allowed.Protocol == corev1.ProtocolTCP && port.Backend == allowed.Port
		}) {
			return destination, fmt.Errorf("%w: mTLS cannot add an unapproved application ingress port", ErrConflict)
		}
		ports = append(ports, api.AgentPolicyPort{Port: port.TLS, Protocol: corev1.ProtocolTCP})
	}
	destination.Ports = ports
	return destination, nil
}

func IngressMTLSResources(input CompileInput, plan ingressmtls.Plan, revision string) ([]unstructured.Unstructured, error) {
	metadata, err := ownedMetadata(input, ingressmtls.ConfigName, input.Namespace)
	if err != nil {
		return nil, err
	}
	metadata.Annotations[RevisionAnnotation] = revision
	config, err := plan.Bootstrap()
	if err != nil {
		return nil, err
	}
	cm, err := manifest(corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metadata,
		Data: map[string]string{"envoy.json": string(config)},
	})
	if err != nil {
		return nil, err
	}
	dr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.istio.io/v1beta1", "kind": "DestinationRule",
		"spec": map[string]any{
			"host": plan.ServiceHost, "exportTo": []any{plan.Gateway.Namespace},
			"trafficPolicy": map[string]any{
				"tls": map[string]any{
					"mode": "MUTUAL", "credentialName": plan.ClientSecret.Name,
					"sni": plan.ServerName, "subjectAltNames": []any{plan.ServerName},
					"insecureSkipVerify": false,
				},
				"connectionPool": map[string]any{"tcp": map[string]any{"maxConnectionDuration": "300s"}},
			},
		},
	}}
	dr.SetName(metadata.Name)
	dr.SetNamespace(metadata.Namespace)
	dr.SetLabels(metadata.Labels)
	dr.SetAnnotations(metadata.Annotations)
	dr.SetOwnerReferences(metadata.OwnerReferences)
	return []unstructured.Unstructured{cm, *dr}, nil
}
