// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestSuspendedPolicyHoldDoesNotWriteRepeatedReceipts(t *testing.T) {
	for _, unavailable := range []bool{true, false} {
		name := "denied policy"
		if unavailable {
			name = "unavailable source"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := readyPolicyRateLimitFixture(t, "openclaw", true)
			initial := f.current(t)
			token := aigateway.Token(f.gateway.generated[0].Key)
			f.gateway.keys[token].Spend = ptr.To(2.5)
			initial.Spec.Suspended = true
			initial.Generation++
			if err := f.kube.Update(ctx, initial); err != nil {
				t.Fatal(err)
			}
			if unavailable {
				// A pre-upgrade Gateway selector cannot pass current source validation.
				updateContainerPlatformForTest(t, f.r, f.kube, func(document *SandboxPlatformConfiguration) {
					for backend, platform := range document.Platforms {
						platform.Topology.Gateway[0].Peer.PodSelector = map[string]string{"app.kubernetes.io/instance": "example-gateway"}
						document.Platforms[backend] = platform
					}
				})
			} else {
				team := &api.Team{}
				if err := f.kube.Get(ctx, client.ObjectKey{Name: initial.Spec.Team}, team); err != nil {
					t.Fatal(err)
				}
				team.Spec.SandboxRefs = nil
				if err := f.kube.Update(ctx, team); err != nil {
					t.Fatal(err)
				}
			}
			f.reconcileOK(t)
			current := f.current(t)
			resolved := apimeta.FindStatusCondition(current.Status.Conditions, api.ConditionPolicyResolved)
			if resolved == nil || resolved.Status != metav1.ConditionFalse ||
				(unavailable && resolved.Reason != "DependencyUnavailable") ||
				(!unavailable && resolved.Reason != "PolicyDenied") {
				t.Fatalf("fixture did not reach the expected policy hold: %+v", resolved)
			}
			patches := 0
			f.r.Client = interceptor.NewClient(f.r.Client.(client.WithWatch), interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if _, ok := obj.(*api.Agent); ok && subresource == "status" {
						patches++
					}
					return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
				},
			})
			// A final-status comparison alone misses intermediate writes that enqueue
			// more work. Count every status patch, including across reconciler restarts.
			for range 3 {
				f.r = restartEntraReconciler(f.r)
				f.reconcileOK(t)
			}
			after := f.current(t)
			if patches != 0 || after.ResourceVersion != current.ResourceVersion {
				t.Fatalf("unchanged suspension wrote %d status patches; resourceVersion %s -> %s", patches, current.ResourceVersion, after.ResourceVersion)
			}
			if after.Status.AIGateway.Reason != aiGatewayReasonSuspended || !after.Status.AIGateway.Blocked ||
				after.Status.AIGateway.KeyID != initial.Status.AIGateway.KeyID || !f.gateway.keys[token].Blocked ||
				*f.gateway.keys[token].Spend != 2.5 || len(f.gateway.generated) != 1 || len(f.gateway.deletedTokens) != 0 ||
				ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
				t.Fatal("policy hold lost the stopped workload or retained blocked key")
			}
		})
	}
}

func TestSuspendedPolicyHoldRecoversFailedWrites(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		name := "intent"
		if failAt == 2 {
			name = "receipt"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			agent := aiGatewayTestAgent(testAIGatewayTeam)
			r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
			if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
				t.Fatal(err)
			}
			if err := r.updateStatus(ctx, agent); err != nil {
				t.Fatal(err)
			}
			agent.Spec.Suspended = true
			if err := kube.Update(ctx, agent); err != nil {
				t.Fatal(err)
			}
			token := aigateway.Token(string(readKeySecret(t, kube).Data[aiGatewayKeySecretField]))
			injected := errors.New("synthetic suspension policy hold write failure")
			r.Client = &aiGatewayNthStatusErrorClient{Client: kube, err: injected, failAt: failAt}
			if err := r.beginAIGatewayPolicyReprojection(ctx, agent); !errors.Is(err, injected) {
				t.Fatalf("expected injected write failure: %v", err)
			}
			if gateway.keys[token].Blocked != (failAt == 2) {
				t.Fatal("external block did not follow the durable intent")
			}
			if err := kube.Get(ctx, client.ObjectKeyFromObject(agent), agent); err != nil {
				t.Fatal(err)
			}
			if agent.Status.AIGateway.Blocked || (failAt == 2 && agent.Status.AIGateway.Reason != aiGatewayReasonSuspended) {
				t.Fatal("failed write incorrectly acknowledged the block")
			}
			r.Client = kube
			r = restartEntraReconciler(r)
			if err := r.beginAIGatewayPolicyReprojection(ctx, agent); err != nil {
				t.Fatal("restart could not complete the block receipt:", err)
			}
			if !gateway.keys[token].Blocked || !agent.Status.AIGateway.Blocked || agent.Status.AIGateway.Reason != aiGatewayReasonSuspended {
				t.Fatal("restart did not confirm the suspended block")
			}
			// An unchanged receipt must still verify the external key and repair drift.
			gateway.keys[token].Blocked = false
			if err := r.beginAIGatewayPolicyReprojection(ctx, agent); err != nil || !gateway.keys[token].Blocked {
				t.Fatal("cached receipt skipped external block verification:", err)
			}
			agent.Spec.Suspended = false
			if err := kube.Update(ctx, agent); err != nil {
				t.Fatal(err)
			}
			if err := r.beginAIGatewayPolicyReprojection(ctx, agent); err != nil ||
				!gateway.keys[token].Blocked || agent.Status.AIGateway.Reason != aiGatewayReasonPolicyReprojection {
				t.Fatal("resume did not retain the block pending policy verification:", err)
			}
			if err := r.reconcileAIGateway(ctx, agent, true); err != nil || gateway.keys[token].Blocked || len(gateway.generated) != 1 {
				t.Fatal("verified grant did not resume the retained key:", err)
			}
		})
	}
}
