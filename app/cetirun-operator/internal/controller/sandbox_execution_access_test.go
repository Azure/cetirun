// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"google.golang.org/protobuf/proto"
	istioapi "istio.io/api/networking/v1beta1"
	istio "istio.io/client-go/pkg/apis/networking/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type accessFixture struct {
	*preparationFixture
	access      controller.SandboxExecutionAccess
	target      sandboxcoordination.EnrolledTarget
	intent      sandboxcoordination.Intent
	ticket      sandboxcoordination.Ticket
	observation sandbox.Observation
}

func newAccessFixture(t *testing.T, authenticated bool) *accessFixture {
	t.Helper()
	ctx := context.Background()
	f, target := executionIntentFixture(t, "openclaw", func(f *preparationFixture) {
		agent := f.current(t)
		agent.Spec.Agent.Port = ptr.To(int32(18789))
		if err := f.client.Update(ctx, agent); err != nil {
			t.Fatal(err)
		}
		if authenticated {
			registration := f.config.Registrations[api.SandboxKata]
			registration.Capabilities.AuthenticatedIngress = true
			f.config.Registrations[api.SandboxKata] = registration
			policy := &api.AgentPolicy{}
			if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.fixture.Policy), policy); err != nil {
				t.Fatal(err)
			}
			policy.Spec.Network.Ingress.Mode = api.AgentPolicyIngressAuthenticated
			if err := f.client.Update(ctx, policy); err != nil {
				t.Fatal(err)
			}
		}
	})
	f.reconciler.IstioGateway = "gateway-system/native-gateway"
	gateway := &istio.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "native-gateway", Namespace: "gateway-system"},
		Spec: istioapi.Gateway{
			Selector: map[string]string{"app": "synthetic-gateway"},
			Servers: []*istioapi.Server{{
				Port:  &istioapi.Port{Name: "https", Number: 443, Protocol: "HTTPS"},
				Hosts: []string{"example.com"},
				Tls:   &istioapi.ServerTLSSettings{Mode: istioapi.ServerTLSSettings_SIMPLE, CredentialName: "synthetic-tls"},
			}},
		},
	}
	if err := f.client.Create(ctx, gateway); err != nil {
		t.Fatal(err)
	}
	intent, err := f.reconciler.SandboxExecutionIntent(ctx, f.current(t), target)
	if err != nil {
		t.Fatal(err)
	}
	state, err := sandboxcoordination.NewState(f.reconciler.OperatorNamespace, "access-coordination")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.client.Create(ctx, state); err != nil {
		t.Fatal(err)
	}
	result := &accessFixture{preparationFixture: f, target: target, intent: intent}
	result.access = controller.SandboxExecutionAccess{
		Reconciler: f.reconciler,
		Coordination: sandboxcoordination.Store{Client: f.client, APIReader: f.client, Namespace: state.Namespace,
			Identity: w.ObjectIdentity{Name: state.Name, UID: state.UID}},
		Now: func() time.Time { return f.now }, MaxAge: 15 * time.Second,
	}
	return result
}

func (f *accessFixture) enroll(t *testing.T) {
	t.Helper()
	before := f.current(t)
	intent, err := f.access.Enroll(context.Background(), f.intent)
	if err != nil {
		t.Fatal(err)
	}
	f.intent = intent
	if !reflect.DeepEqual(before, f.current(t)) || !controllerutil.ContainsFinalizer(f.current(t), sandbox.HoldFinalizer) {
		t.Fatal("access enrollment changed the Agent or lifted its execution hold")
	}
}

func (f *accessFixture) run(t *testing.T) {
	t.Helper()
	f.enroll(t)
	ctx := context.Background()
	ticket, err := f.access.Coordination.AcquireExecution(ctx, f.intent)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err = f.access.Coordination.BeginStart(ctx, ticket)
	if err != nil {
		t.Fatal(err)
	}
	completeRunningFixture(t, f.preparationFixture, f.target, f.intent)
	ticket, err = f.access.Coordination.MarkRunning(ctx, ticket)
	if err != nil {
		t.Fatal(err)
	}
	f.ticket = ticket
	f.observation = sandbox.Observation{ExecutionIdentity: f.intent.Expected, ObservedAt: f.now,
		RuntimeVerified: true, PodVerified: true, ControlsVerified: true}
}

func (f *accessFixture) route(t *testing.T) *istio.VirtualService {
	t.Helper()
	route := &istio.VirtualService{}
	if err := f.client.Get(context.Background(), client.ObjectKey{
		Namespace: f.intent.Namespace.Name, Name: "cetirun-" + f.intent.Agent.Name,
	}, route); err != nil {
		t.Fatal(err)
	}
	return route
}

func TestSandboxAccessPublishesOnlyNativeUIAfterEvidence(t *testing.T) {
	ctx := context.Background()
	f := newAccessFixture(t, true)
	f.run(t)
	slot := f.route(t)
	if f.intent.Access == nil || slot.UID != f.intent.Access.Route.UID || len(slot.Spec.Http) != 1 ||
		slot.Spec.Http[0].DirectResponse.GetStatus() != 404 || len(slot.Spec.Http[0].Route) != 0 {
		t.Fatal("enrollment did not retain a nonforwarding exact-UID slot")
	}
	before := f.current(t)
	if err := f.access.Publish(ctx, f.ticket, f.observation, statusTestGuard); err != nil {
		t.Fatal(err)
	}
	current := f.route(t)
	if current.UID != slot.UID || current.ResourceVersion == slot.ResourceVersion || len(current.Spec.Http) != 3 {
		t.Fatal("publication did not conditionally update the enrolled slot")
	}
	path := "/ui/" + f.intent.Agent.Name
	for _, route := range current.Spec.Http {
		for _, match := range route.Match {
			if match.Uri.GetExact() != path && match.Uri.GetPrefix() != path+"/" {
				t.Fatal("native route exposed another path, helper or anonymous webhook")
			}
		}
		for _, destination := range route.Route {
			if destination.Destination.Port.Number != uint32(f.intent.Access.Port) ||
				destination.Destination.Host != f.intent.Workload.Name+"."+f.intent.Namespace.Name+".svc.cluster.local" {
				t.Fatal("native route escaped the pinned backend")
			}
		}
	}
	if !reflect.DeepEqual(before, f.current(t)) {
		t.Fatal("route publication changed Agent status or access readiness")
	}
	if err := f.access.Publish(ctx, f.ticket, f.observation, statusTestGuard); err != nil {
		t.Fatal(err)
	}
	if got := f.route(t); got.ResourceVersion != current.ResourceVersion {
		t.Fatal("unchanged access publication rewrote the route")
	}
}

func TestSandboxAccessFailsClosedOnEvidenceOrSourceChanges(t *testing.T) {
	for _, scenario := range []string{
		"stale", "mismatched", "incomplete", "missing guard", "changed authority", "wrong phase",
		"service", "gateway", "domain", "wrong UID", "missing slot", "held", "missing access",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			f := newAccessFixture(t, true)
			f.run(t)
			before := f.route(t)
			guard := statusTestGuard
			observation := f.observation
			ticket := f.ticket.DeepCopy()
			switch scenario {
			case "stale":
				f.now = f.now.Add(f.access.MaxAge + time.Nanosecond)
			case "mismatched":
				observation.PodUID = "other-pod"
			case "incomplete":
				observation.ControlsVerified = false
			case "missing guard":
				guard = nil
			case "changed authority":
				ticket.AuthorityUID = "other-authority"
			case "wrong phase":
				ticket.Execution.Phase = sandboxcoordination.Starting
			case "service":
				service := &corev1.Service{}
				if err := f.client.Get(ctx, client.ObjectKey{Namespace: f.intent.Namespace.Name, Name: f.intent.Workload.Name}, service); err != nil {
					t.Fatal(err)
				}
				service.Spec.Selector = map[string]string{"app": "another"}
				if err := f.client.Update(ctx, service); err != nil {
					t.Fatal(err)
				}
			case "gateway":
				gateway := &istio.Gateway{}
				if err := f.client.Get(ctx, client.ObjectKey{Namespace: "gateway-system", Name: "native-gateway"}, gateway); err != nil {
					t.Fatal(err)
				}
				gateway.Spec.Servers[0].Tls = nil
				if err := f.client.Update(ctx, gateway); err != nil {
					t.Fatal(err)
				}
			case "domain":
				f.reconciler.DomainSuffix = "changed.example.com"
			case "wrong UID", "missing slot":
				if err := f.client.Delete(ctx, before); err != nil {
					t.Fatal(err)
				}
				if scenario == "wrong UID" {
					replacement := before.DeepCopy()
					replacement.UID, replacement.ResourceVersion = "replacement-route", ""
					if err := f.client.Create(ctx, replacement); err != nil {
						t.Fatal(err)
					}
				}
			case "held":
				agent := f.current(t)
				controllerutil.AddFinalizer(agent, sandbox.HoldFinalizer)
				if err := f.client.Update(ctx, agent); err != nil {
					t.Fatal(err)
				}
			case "missing access":
				ticket.Execution.Intent.Access = nil
			}
			if err := f.access.Publish(ctx, ticket, observation, guard); err == nil {
				t.Fatal("invalid access publication succeeded")
			}
			if scenario != "missing slot" && scenario != "wrong UID" {
				if after := f.route(t); !reflect.DeepEqual(before, after) {
					t.Fatal("rejected publication changed the slot")
				}
			}
		})
	}
}

func TestSandboxAccessWithdrawalUsesHistoricalIdentity(t *testing.T) {
	ctx := context.Background()
	f := newAccessFixture(t, true)
	f.run(t)
	if err := f.access.Publish(ctx, f.ticket, f.observation, statusTestGuard); err != nil {
		t.Fatal(err)
	}
	stopping, err := f.access.Coordination.BeginStop(ctx, f.ticket)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.client.Delete(ctx, f.fixture.Policy); err != nil {
		t.Fatal(err)
	}
	f.reconciler.SandboxPreparation = nil
	f.reconciler.DomainSuffix, f.reconciler.IstioGateway = "", ""
	if err := f.access.Withdraw(ctx, stopping, statusTestGuard); err != nil {
		t.Fatal("withdrawal depended on current policy/configuration:", err)
	}
	err = f.client.Get(ctx, client.ObjectKey{Namespace: f.intent.Namespace.Name, Name: f.intent.Access.Route.Name}, &istio.VirtualService{})
	if !apierrors.IsNotFound(err) {
		t.Fatal("withdrawn route did not reach observed absence:", err)
	}
	if err := f.access.Withdraw(ctx, stopping, statusTestGuard); err != nil {
		t.Fatal("repeated withdrawal was not idempotent:", err)
	}
	if err := f.access.Coordination.Check(ctx, stopping); err != nil {
		t.Fatal("route withdrawal released the execution reservation:", err)
	}
}

func TestSandboxAccessRejectsDelayedPublicationAfterWithdrawal(t *testing.T) {
	ctx := context.Background()
	f := newAccessFixture(t, true)
	f.run(t)
	original := f.route(t)
	calls := 0
	f.reconciler.Client = interceptor.NewClient(f.client.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.UpdateOption) error {
			route, ok := object.(*istio.VirtualService)
			if !ok {
				t.Fatal("publication attempted a non-route write")
			}
			calls++
			stopping, err := f.access.Coordination.BeginStop(ctx, f.ticket)
			if err != nil {
				return err
			}
			f.reconciler.Client = c
			if err := f.access.Withdraw(ctx, stopping, statusTestGuard); err != nil {
				return err
			}
			err = c.Update(ctx, route, options...)
			if !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
				t.Fatal("delayed publication could recreate the retired route:", err)
			}
			return err
		},
	})
	if err := f.access.Publish(ctx, f.ticket, f.observation, statusTestGuard); err == nil || calls != 1 {
		t.Fatal("delayed publication did not remain failed after stop:", err)
	}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(original), &istio.VirtualService{}); !apierrors.IsNotFound(err) {
		t.Fatal("old route was recreated:", err)
	}
}

func TestSandboxAccessNeverDeletesReplacementOrClaimsUnobservedWrites(t *testing.T) {
	for _, scenario := range []string{"replacement", "withdrawal error", "lost publication", "expired publication"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			f := newAccessFixture(t, true)
			f.run(t)
			before := f.route(t)
			failure := errors.New("synthetic write outcome unavailable")
			if scenario == "replacement" {
				if err := f.client.Delete(ctx, before); err != nil {
					t.Fatal(err)
				}
				before.UID, before.ResourceVersion = "other-route", ""
				if err := f.client.Create(ctx, before); err != nil {
					t.Fatal(err)
				}
			} else {
				f.reconciler.Client = interceptor.NewClient(f.client.(client.WithWatch), interceptor.Funcs{
					Update: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.UpdateOption) error {
						if scenario == "withdrawal error" {
							return failure
						}
						if err := c.Update(ctx, object, options...); err != nil {
							return err
						}
						if scenario == "expired publication" {
							f.now = f.now.Add(f.access.MaxAge + time.Nanosecond)
							return nil
						}
						return failure
					},
				})
			}
			if scenario == "replacement" || scenario == "withdrawal error" {
				stopping, err := f.access.Coordination.BeginStop(ctx, f.ticket)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.access.Withdraw(ctx, stopping, statusTestGuard); err == nil {
					t.Fatal("unverified withdrawal succeeded")
				}
				if !proto.Equal(&before.Spec, &f.route(t).Spec) {
					t.Fatal("failed withdrawal changed a foreign or protected route")
				}
			} else if err := f.access.Publish(ctx, f.ticket, f.observation, statusTestGuard); err == nil {
				t.Fatal("uncertain or expired publication was reported as verified")
			}
		})
	}
}

func TestSandboxAccessDisabledIngressCreatesNothing(t *testing.T) {
	ctx := context.Background()
	f := newAccessFixture(t, false)
	f.run(t)
	if f.intent.Access != nil {
		t.Fatal("disabled ingress enrolled an access route")
	}
	if err := f.access.Publish(ctx, f.ticket, f.observation, statusTestGuard); err != nil {
		t.Fatal(err)
	}
	stopping, err := f.access.Coordination.BeginStop(ctx, f.ticket)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.access.Withdraw(ctx, stopping, statusTestGuard); err != nil {
		t.Fatal(err)
	}
	if f.current(t).Status.AccessURL != "" {
		t.Fatal("disabled ingress published a URL")
	}
	route := &istio.VirtualService{ObjectMeta: metav1.ObjectMeta{
		Namespace: f.intent.Namespace.Name, Name: "cetirun-" + f.intent.Agent.Name,
	}}
	if err := f.client.Create(ctx, route); err != nil {
		t.Fatal(err)
	}
	if err := f.access.Withdraw(ctx, stopping, statusTestGuard); err == nil {
		t.Fatal("disabled ingress treated a foreign route as absent")
	}
}

func TestSandboxAccessEnrollmentRejectsMisconfiguredInputs(t *testing.T) {
	for _, scenario := range []string{"host", "gateway", "foreign slot", "wrong TLS"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAccessFixture(t, true)
			switch scenario {
			case "host":
				f.reconciler.DomainSuffix = "*.example.com"
			case "gateway":
				f.reconciler.IstioGateway = "implicit-default"
			case "foreign slot":
				if err := f.client.Create(context.Background(), &istio.VirtualService{
					ObjectMeta: metav1.ObjectMeta{Name: "cetirun-" + f.intent.Agent.Name, Namespace: f.intent.Namespace.Name},
				}); err != nil {
					t.Fatal(err)
				}
			case "wrong TLS":
				gateway := &istio.Gateway{}
				if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "gateway-system", Name: "native-gateway"}, gateway); err != nil {
					t.Fatal(err)
				}
				gateway.Spec.Servers[0].Port.Protocol = "HTTP"
				if err := f.client.Update(context.Background(), gateway); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.access.Enroll(context.Background(), f.intent); err == nil {
				t.Fatalf("invalid enrollment %s succeeded", scenario)
			}
		})
	}
}
