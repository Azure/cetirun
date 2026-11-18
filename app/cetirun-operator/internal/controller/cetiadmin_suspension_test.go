// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/cetiadmin"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCetiAdminAgentDisabledReconcile(t *testing.T) {
	for _, reason := range []string{"Disabled", "UserSuspended"} {
		t.Run(reason, func(t *testing.T) {
			f := newCetiAdminFixture(t)
			f.converge()
			f.r.Runtime.CetiAdmin = f.r
			agent, state := f.agent(), f.state()
			value := "sk-" + strings.Repeat("x", 43)
			token := aigateway.Token(value)
			gateway := newFakeAIGateway()
			gateway.keys[token] = &aigateway.Key{Alias: aiGatewayKeyAlias(agent), MaxBudget: ptr.To(10.25), BudgetDuration: "1mo"}
			f.r.Runtime.aiGatewayClientFactory = func(context.Context) (aiGatewayKeyClient, error) { return gateway, nil }
			f.seed(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: aiGatewayKeySecretName, Namespace: state.Namespace.Name,
				Labels: map[string]string{aiGatewayAgentUIDLabel: string(agent.UID)}}, Data: map[string][]byte{aiGatewayKeySecretField: []byte(value)}})
			agent.Status.AIGateway = &api.AIGatewayStatus{KeyID: token, BudgetUSD: "10.25", SpendUSD: "2.50", Routed: true}
			if err := f.kube.Status().Update(f.ctx, agent); err != nil {
				t.Fatal(err)
			}
			workload := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
				Name: instanceServiceName(agent.Name), Namespace: state.Namespace.Name,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(agent, api.GroupVersion.WithKind("Agent"))},
			}, Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(1))}}
			f.seed(workload)
			home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: state.Namespace.Name}}
			f.seed(home)
			homeUID := home.UID
			if reason == "Disabled" {
				f.config.Enabled = false
				f.updateConfig()
			} else {
				agent.Spec.Suspended = true
				if err := f.kube.Update(f.ctx, agent); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.run(); err != nil {
				t.Fatal(err)
			}
			if err := f.kube.Get(f.ctx, client.ObjectKeyFromObject(workload), workload); err != nil {
				t.Fatal(err)
			}
			if ptr.Deref(workload.Spec.Replicas, -1) != 0 {
				t.Fatal("CetiAdmin workload was not stopped")
			}
			workload.Status.ObservedGeneration = workload.Generation
			if err := f.kube.Status().Update(f.ctx, workload); err != nil {
				t.Fatal(err)
			}
			if err := f.run(); err != nil {
				t.Fatal(err)
			}
			if state := f.state(); state.Phase != cetiadmin.PhaseDisabled || state.Reason != reason {
				t.Fatalf("installation did not settle: phase=%s reason=%s", state.Phase, state.Reason)
			}
			f.noBindings()

			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(agent)}
			if result, err := f.r.Runtime.Reconcile(f.ctx, request); err != nil || result != (ctrl.Result{}) {
				t.Fatalf("disabled Agent keeps retrying: result=%+v err=%v", result, err)
			}
			stopped := f.agent()
			ready := meta.FindStatusCondition(stopped.Status.Conditions, api.ConditionReady)
			if stopped.Status.Phase != api.PhaseSuspended || ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "CetiAdminDisabled" {
				t.Fatalf("disabled Agent status does not explain the stop: %+v", stopped.Status)
			}
			if stopped.UID != agent.UID || !reflect.DeepEqual(stopped.Spec, agent.Spec) || stopped.Status.AccessURL != "" {
				t.Fatal("disable changed the retained Agent or kept its route")
			}
			if !gateway.keys[token].Blocked || !stopped.Status.AIGateway.Blocked || stopped.Status.AIGateway.Routed ||
				stopped.Status.AIGateway.KeyID != token || stopped.Status.AIGateway.SpendUSD != "2.50" ||
				len(gateway.generated) != 0 || len(gateway.deletedTokens) != 0 || ptr.Deref(gateway.keys[token].MaxBudget, 0) != 10.25 {
				t.Fatal("disable did not block and retain the existing Gateway key")
			}
			for range 3 {
				if err := f.run(); err != nil {
					t.Fatal(err)
				}
				if result, err := f.r.Runtime.Reconcile(f.ctx, request); err != nil || result != (ctrl.Result{}) {
					t.Fatalf("settled Agent keeps retrying: result=%+v err=%v", result, err)
				}
				if current := f.agent(); current.ResourceVersion != stopped.ResourceVersion {
					t.Fatal("installation and Agent controllers keep rewriting the stopped status")
				}
			}
			if err := f.kube.Get(f.ctx, client.ObjectKeyFromObject(home), home); err != nil || home.UID != homeUID {
				t.Fatal("disable did not retain HOME", err)
			}
			if current := f.state(); current.RecordUID != state.RecordUID || current.Agent != state.Agent {
				t.Fatal("disable replaced the installation record")
			}
			imitation := stopped.DeepCopy()
			imitation.UID = "another-agent-uid"
			if _, handled, err := f.r.BindingForAgent(f.ctx, imitation); handled || err != nil {
				t.Fatalf("a same-name Agent was treated as the retained installation: handled=%v err=%v", handled, err)
			}

			f.config.Enabled = true
			f.updateConfig()
			stopped.Spec.Suspended = false
			if err := f.kube.Update(f.ctx, stopped); err != nil {
				t.Fatal(err)
			}
			queued := f.r.EnqueueRuntimeAccess(f.ctx, f.source)
			if len(queued) != 1 || queued[0] != request {
				t.Fatalf("source change did not wake the retained Agent: %+v", queued)
			}
			f.converge()
			if binding, handled, err := f.r.BindingForAgent(f.ctx, f.agent()); err != nil || !handled || binding.ServiceAccountUsername() == "" {
				t.Fatalf("resume did not restore the retained binding: handled=%v err=%v", handled, err)
			}
		})
	}
}

func TestCetiAdminAgentBindingFailuresDoNotReturnRequeue(t *testing.T) {
	for _, failure := range []string{"installing", "stopping", "blocked", "failed", "configuration-changing", "operator-upgrade", "runtime-upgrade", "admission", "conflict"} {
		t.Run(failure, func(t *testing.T) {
			f := newCetiAdminFixture(t)
			f.converge()
			f.r.Runtime.CetiAdmin = f.r
			object, state, err := f.r.readState(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "installing":
				state.Phase, state.Hold = cetiadmin.PhaseInstalling, true
			case "stopping":
				state.Phase, state.Hold, state.Reason = cetiadmin.PhaseStopping, true, "Disabled"
			case "blocked":
				state.Phase, state.Hold = cetiadmin.PhaseBlocked, true
			case "failed":
				state.Phase, state.Hold, state.Reason = cetiadmin.PhaseDisabled, true, "ReconcileFailed"
			case "configuration-changing":
				state.Phase, state.Hold, state.Reason = cetiadmin.PhaseDisabled, true, "ConfigurationChanging"
			case "operator-upgrade":
				f.r.OperatorImage = "registry.example.com/operator:next"
			case "runtime-upgrade":
				f.r.ChartVersion = "0.1.1-test.2"
			case "admission":
				f.r.AdmissionReady = func(context.Context) error { return errors.New("admission unavailable") }
			case "conflict":
				f.config.Model.Default = "changed-model"
				f.updateConfig()
			}
			if err := f.r.saveState(f.ctx, object, state); err != nil {
				t.Fatal(err)
			}
			if result, err := f.r.Runtime.Reconcile(f.ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.agent())}); err == nil || result != (ctrl.Result{}) {
				t.Fatalf("binding failure must return only an error: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestCetiAdminAgentDisabledKeepsStopFailures(t *testing.T) {
	for _, failure := range []string{"workload", "gateway"} {
		t.Run(failure, func(t *testing.T) {
			f := newCetiAdminFixture(t)
			f.converge()
			f.r.Runtime.CetiAdmin = f.r
			f.config.Enabled = false
			f.updateConfig()
			if err := f.run(); err != nil {
				t.Fatal(err)
			}
			f.getError = func(key client.ObjectKey, object client.Object) error {
				if _, ok := object.(*appsv1.StatefulSet); ok && failure == "workload" {
					return errors.New("workload read unavailable")
				}
				if key.Name == aiGatewayKeySecretName && failure == "gateway" {
					return errors.New("Gateway key unavailable")
				}
				return nil
			}
			if result, err := f.r.Runtime.Reconcile(f.ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.agent())}); err == nil || result != (ctrl.Result{}) {
				t.Fatalf("disabled state hid a stop failure: result=%+v err=%v", result, err)
			}
		})
	}
}
