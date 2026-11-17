// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestReconciledSandboxSuspensionProgressPreservesFailures(t *testing.T) {
	for _, failure := range []string{"gateway", "status"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			f := customTerminalFixture(t, "container")
			main := f.workload(t)
			main.Status.Replicas = 1
			if err := f.kube.Status().Update(ctx, main); err != nil {
				t.Fatal(err)
			}
			agent := f.current(t)
			agent.Spec.Suspended = true
			agent.Generation++
			if err := f.kube.Update(ctx, agent); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("synthetic suspension failure")
			if failure == "gateway" {
				f.gateway.getErr = injected
			} else {
				f.r.Client = interceptor.NewClient(f.kube, interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, object client.Object,
						patch client.Patch, options ...client.SubResourcePatchOption,
					) error {
						if _, ok := object.(*api.Agent); ok && subresource == "status" {
							return injected
						}
						return c.SubResource(subresource).Patch(ctx, object, patch, options...)
					},
				})
			}
			result, err := f.reconcile()
			if !errors.Is(err, injected) || !result.IsZero() {
				t.Fatalf("shutdown progress hid a real %s failure: result=%+v, err=%v", failure, result, err)
			}
			if f.current(t).Status.ObservedGeneration == agent.Generation {
				t.Fatal("incomplete suspension acknowledged its generation")
			}
			ticket, found, err := f.r.SandboxNativeCoordination.ReadNative(ctx, agent.UID)
			if err != nil || !found || ticket.Reservation.Phase != sandboxcoordination.Stopping {
				t.Fatalf("failed suspension lost its stopping reservation: found=%t, err=%v", found, err)
			}
		})
	}
}
