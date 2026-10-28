// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCustomRequireMTLSIsDeniedBeforeExecution(t *testing.T) {
	ctx := context.Background()
	f, input := newUnreconciledSandboxRuntimeFixture(t, true, nil, true, "custom", true, false)
	policy := &api.AgentPolicy{}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: input.Policy.Name}, policy); err != nil {
		t.Fatal(err)
	}
	policy.Spec.Network.Ingress.RequireMTLS = true
	if err := f.kube.Update(ctx, policy); err != nil {
		t.Fatal(err)
	}
	// Supply an otherwise valid built-in registration shape. Custom must still
	// be denied by policy resolution rather than reaching source stop/apply.
	withIngressMTLS(t, "openclaw")(f.r, f.kube)
	if _, err := f.r.resolvePolicy(ctx, f.current(t)); err == nil || !strings.Contains(err.Error(), "managed ingress mTLS does not support the Custom Harness") {
		t.Fatalf("Custom mTLS policy did not fail shared resolution: %v", err)
	}
}
