// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type completionOperation struct {
	agentUID  types.UID
	operation controller.CustomApplyOperation
	done      bool
	outcome   controller.CustomApplyOutcome
}

// Models the provider's durable store, independently of the reconciler process.
type completionBackend struct {
	held                                      string
	operations                                map[string]*completionOperation
	approvals, resumes, completions, failures int
}

type recoveryProvider struct {
	customProvider
	store         *completionBackend
	beforeApprove func(controller.CustomApplyOperation)
}

type recoveryLease struct {
	store     *completionBackend
	agentUID  types.UID
	operation controller.CustomApplyOperation
}

func (p *recoveryProvider) ApproveApply(ctx context.Context, instance *api.Agent, current *appsv1.StatefulSet, operation controller.CustomApplyOperation) (controller.CustomApplyLease, error) {
	p.store.approvals++
	if p.beforeApprove != nil {
		p.beforeApprove(operation)
	}
	if p.store.held != "" {
		return nil, fmt.Errorf("previous coordination is still held")
	}
	p.store.operations[operation.ID] = &completionOperation{agentUID: instance.UID, operation: operation}
	p.store.held = operation.ID
	if p.approve != nil {
		if err := p.approve(current); err != nil {
			return nil, err
		}
	}
	return recoveryLease{p.store, instance.UID, operation}, nil
}

func (p *recoveryProvider) ResumeApply(_ context.Context, instance *api.Agent, operation controller.CustomApplyOperation) (controller.CustomApplyLease, error) {
	p.store.resumes++
	if previous := p.store.operations[operation.ID]; previous != nil &&
		(previous.agentUID != instance.UID || previous.operation != operation) {
		return nil, fmt.Errorf("operation identity mismatch")
	}
	return recoveryLease{p.store, instance.UID, operation}, nil
}

func (l recoveryLease) Check(context.Context) error {
	if l.store.held != l.operation.ID {
		return fmt.Errorf("lease is not held")
	}
	return nil
}

func (l recoveryLease) Complete(_ context.Context, outcome controller.CustomApplyOutcome) error {
	l.store.completions++
	if l.store.failures > 0 {
		l.store.failures--
		return fmt.Errorf("temporary completion-store failure")
	}
	previous := l.store.operations[l.operation.ID]
	if previous == nil {
		if outcome != controller.CustomApplyNotStarted {
			return fmt.Errorf("unknown operation cannot be reported as applied")
		}
		previous = &completionOperation{agentUID: l.agentUID, operation: l.operation}
		l.store.operations[l.operation.ID] = previous
	}
	if previous.done && previous.outcome != outcome {
		return fmt.Errorf("completion outcome changed")
	}
	previous.done, previous.outcome = true, outcome
	if l.store.held == l.operation.ID {
		l.store.held = ""
	}
	return nil
}

type completionClient struct {
	client.Client
	failCreate, failDelete bool
	failOutcome            controller.CustomApplyOutcome
}

func isCompletion(obj client.Object) bool {
	record, ok := obj.(*corev1.ConfigMap)
	return ok && record.Data["completion"] != ""
}

func (c *completionClient) Create(ctx context.Context, obj client.Object, options ...client.CreateOption) error {
	if c.failCreate && isCompletion(obj) {
		c.failCreate = false
		return fmt.Errorf("synthetic outbox create failure")
	}
	return c.Client.Create(ctx, obj, options...)
}

func (c *completionClient) Update(ctx context.Context, obj client.Object, options ...client.UpdateOption) error {
	if c.failOutcome != "" && isCompletion(obj) {
		var value struct{ Outcome controller.CustomApplyOutcome }
		_ = json.Unmarshal([]byte(obj.(*corev1.ConfigMap).Data["completion"]), &value)
		if value.Outcome == c.failOutcome {
			c.failOutcome = ""
			return fmt.Errorf("synthetic outcome write failure")
		}
	}
	return c.Client.Update(ctx, obj, options...)
}

func (c *completionClient) Delete(ctx context.Context, obj client.Object, options ...client.DeleteOption) error {
	if c.failDelete && isCompletion(obj) {
		c.failDelete = false
		return fmt.Errorf("synthetic outbox delete failure")
	}
	return c.Client.Delete(ctx, obj, options...)
}

type completionScenario struct {
	t          *testing.T
	client     *completionClient
	agent      *api.Agent
	provider   *recoveryProvider
	helm       *fakeHelmClient
	reconciler *controller.AgentReconciler
}

func newCompletionScenario(t *testing.T) *completionScenario {
	t.Helper()
	instance, bindings, objects := customFixture("completion", nil)
	scheme := testScheme()
	c := &completionClient{Client: fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&api.Agent{}).WithObjects(objects...).Build()}
	p := &recoveryProvider{customProvider: customProvider{bindings: bindings},
		store: &completionBackend{operations: map[string]*completionOperation{}}}
	s := &completionScenario{t: t, client: c, agent: instance, provider: p, helm: &fakeHelmClient{useHarness: true}}
	s.restart()
	return s
}

func (s *completionScenario) restart() {
	s.reconciler = &controller.AgentReconciler{Client: s.client, Scheme: testScheme(),
		HelmClient: s.helm, CustomBindings: s.provider, EventRecorder: record.NewFakeRecorder(100)}
}

func (s *completionScenario) step() error {
	_, err := s.reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: s.agent.Name}})
	return err
}

func (s *completionScenario) current() *api.Agent {
	value := &api.Agent{}
	if err := s.client.Get(context.Background(), types.NamespacedName{Name: s.agent.Name}, value); err != nil {
		s.t.Fatal(err)
	}
	return value
}

func (s *completionScenario) pending() *corev1.ConfigMap {
	list := &corev1.ConfigMapList{}
	if err := s.client.List(context.Background(), list, client.InNamespace("cetirun-system")); err != nil {
		s.t.Fatal(err)
	}
	for i := range list.Items {
		if isCompletion(&list.Items[i]) {
			return &list.Items[i]
		}
	}
	return nil
}

func (s *completionScenario) observeApplied() {
	sts := statefulSet(s.agent.Name, 1)
	sts.OwnerReferences = []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: s.agent.Name, UID: s.agent.UID}}
	sts.Annotations = map[string]string{"cetirun.microsoft.com/agent-uid": string(s.agent.UID),
		"cetirun.microsoft.com/custom-plan-hash": s.helm.lastValues["runtime"].(map[string]any)["workloadHash"].(string)}
	sts.Status.CurrentRevision, sts.Status.UpdateRevision = "applied", "applied"
	if err := s.client.Create(context.Background(), sts); err != nil {
		s.t.Fatal(err)
	}
}
