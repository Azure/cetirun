// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"fmt"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	"github.com/Azure/CetiRun/app/internal/terminalpolicy"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

func enableTerminal(t *testing.T, s *completionScenario, desktop bool) {
	t.Helper()
	s.provider.bindings.Network.APIServer = []runtimebindings.APIServerEndpoint{{IP: "192.0.2.10", Port: 443}}
	s.reconciler.DomainSuffix = "platform.example.com"
	current := s.current()
	current.Spec.Helm.Values = map[string]runtime.RawExtension{
		"ttyd":    {Raw: []byte(`{"enabled":true}`)},
		"desktop": {Raw: []byte(fmt.Sprintf(`{"enabled":%v}`, desktop))},
	}
	if err := s.client.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: terminalpolicy.WorkloadName("openclaw-" + s.agent.Name + "-cetirun"), Namespace: "cetirun-" + s.agent.Name,
		Generation:      1,
		Annotations:     map[string]string{"cetirun.microsoft.com/agent-uid": string(s.agent.UID)},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s.agent, api.GroupVersion.WithKind("Agent"))},
	}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1)},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, AvailableReplicas: 1}}
	if err := s.client.Create(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
}
