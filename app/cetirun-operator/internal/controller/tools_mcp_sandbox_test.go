// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReconciledSandboxIgnoresGlobalToolsMCP(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		t.Run(kind, func(t *testing.T) {
			f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, kind, true, false,
				func(r *AgentReconciler, _ client.Client) {
					r.ToolsMCPURL = "https://agent-tools-mcp.cetirun-tools.svc.cluster.local/mcp"
					r.ToolsMCPCASecretName = toolsMCPTestSecretName
					r.ToolsMCPBrokerNamespace = "cetirun-credentials"
					r.ToolsMCPBrokerServiceAccount = "cetirun-agent-broker"
				})
			// No MCP CA is installed: Sandbox must not depend on or receive this
			// ordinary-runtime integration when using the common reconcile path.
			for range 3 {
				f.reconcileOK(t)
			}
			if f.installer.stages != 1 {
				t.Fatalf("expected one Sandbox apply with global MCP enabled, got %d", f.installer.stages)
			}
			tools := f.installer.prepared.Values["toolsMCP"].(map[string]any)
			if tools["enabled"] != false || tools["url"] != "" {
				t.Fatalf("Sandbox received an MCP connection: %#v", tools)
			}
			workload := f.workload(t)
			if _, present := workload.Spec.Template.Annotations["cetirun.microsoft.com/tools-ca-hash"]; present {
				t.Fatal("Sandbox received an ordinary-runtime MCP rollout annotation")
			}
			for _, object := range []client.Object{&corev1.Secret{}, &rbacv1.Role{}, &rbacv1.RoleBinding{}} {
				name := toolsMCPTestSecretName
				switch object.(type) {
				case *rbacv1.Role:
					name = toolsMCPBrokerRoleName
				case *rbacv1.RoleBinding:
					name = toolsMCPBrokerRoleBindingName
				}
				if err := f.kube.Get(context.Background(), client.ObjectKey{Namespace: workload.Namespace, Name: name}, object); !apierrors.IsNotFound(err) {
					t.Fatalf("Sandbox unexpectedly received MCP resource %T: %v", object, err)
				}
			}
		})
	}
}
