// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

type governanceActionErrorReader struct {
	client.Reader
	err error
}

func (r governanceActionErrorReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return r.err
}

func TestGovernanceActionExecutionRetriesMissingMutation(t *testing.T) {
	action, agent := executingActionFixture(api.GovernanceActionSuspend)
	kube := governanceActionTestClient(t, action, agent)
	r := &GovernanceActionReconciler{Client: kube, APIReader: kube}

	result, err := r.observeExecution(context.Background(), action)
	if err != nil || result.RequeueAfter != governanceActionObserveInterval {
		t.Fatalf("observe missing mutation = (%+v, %v), want retry", result, err)
	}
	current := &api.Agent{}
	if err := kube.Get(context.Background(), types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	if !current.Spec.Suspended {
		t.Fatal("Executing action did not reapply its missing Suspend mutation")
	}
	stored := &api.GovernanceAction{}
	if err := kube.Get(context.Background(), types.NamespacedName{Name: action.Name}, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != "Executing" || stored.Status.ObservedGeneration != stored.Generation {
		t.Fatalf("retry must remain Executing with current observed generation: %+v", stored.Status)
	}
}

func TestGovernanceActionExecutionFailsOnProjectionDrift(t *testing.T) {
	action, agent := executingActionFixture(api.GovernanceActionRestart)
	agent.Status.Governance.Projections.Gateway.ExpectedRevision = "gateway-drifted"
	// The action marker is present, so a result other than Failed would mean
	// that a persisted operation was accepted despite its approval becoming
	// stale during a controller restart.
	agent.Annotations[api.AnnotationGovernanceAction] = string(action.UID)
	kube := governanceActionTestClient(t, action, agent)
	r := &GovernanceActionReconciler{Client: kube, APIReader: kube}

	result, err := r.observeExecution(context.Background(), action)
	if err != nil || result.Requeue {
		t.Fatalf("observe drift = (%+v, %v), want terminal failure", result, err)
	}
	stored := &api.GovernanceAction{}
	if err := kube.Get(context.Background(), types.NamespacedName{Name: action.Name}, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != "Failed" {
		t.Fatalf("projection drift must fail an executing action: %+v", stored.Status)
	}
	current := &api.Agent{}
	if err := kube.Get(context.Background(), types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	if current.Annotations[api.AnnotationGovernanceActionClaim] != "" {
		t.Fatal("terminal drift failure did not release the action claim")
	}
}

func TestGovernanceActionStartRetriesTransientTargetRead(t *testing.T) {
	action, agent := executingActionFixture(api.GovernanceActionSuspend)
	action.Status.Phase = "Approved"
	kube := governanceActionTestClient(t, action, agent)
	r := &GovernanceActionReconciler{
		Client:    kube,
		APIReader: governanceActionErrorReader{Reader: kube, err: errors.New("temporary API failure")},
	}

	result, err := r.startExecution(context.Background(), action)
	if err == nil || result.RequeueAfter != governanceActionObserveInterval {
		t.Fatalf("start transient read = (%+v, %v), want retry", result, err)
	}
	stored := &api.GovernanceAction{}
	if getErr := kube.Get(context.Background(), types.NamespacedName{Name: action.Name}, stored); getErr != nil {
		t.Fatal(getErr)
	}
	if stored.Status.Phase != "Approved" {
		t.Fatalf("transient target read incorrectly became terminal: %+v", stored.Status)
	}
}

func TestGovernanceActionStartRejectsResumeWithoutReadyGovernance(t *testing.T) {
	action, agent := executingActionFixture(api.GovernanceActionResume)
	action.Status.Phase = "Approved"
	agent.Spec.Suspended = true
	delete(agent.Annotations, api.AnnotationGovernanceActionClaim)
	kube := governanceActionTestClient(t, action, agent)
	r := &GovernanceActionReconciler{Client: kube, APIReader: kube}

	result, err := r.startExecution(context.Background(), action)
	if err != nil || result.RequeueAfter != 0 {
		t.Fatalf("start held resume = (%+v, %v), want terminal rejection", result, err)
	}
	stored := &api.GovernanceAction{}
	if getErr := kube.Get(context.Background(), types.NamespacedName{Name: action.Name}, stored); getErr != nil {
		t.Fatal(getErr)
	}
	if stored.Status.Phase != "Failed" || stored.Status.Conditions[0].Reason != "GovernanceNotReady" {
		t.Fatalf("held resume was not rejected: %+v", stored.Status)
	}
	current := &api.Agent{}
	if getErr := kube.Get(context.Background(), types.NamespacedName{Name: agent.Name}, current); getErr != nil {
		t.Fatal(getErr)
	}
	if !current.Spec.Suspended || current.Annotations[api.AnnotationGovernanceActionClaim] != "" {
		t.Fatalf("rejected resume changed its Agent: %+v", current)
	}
}

func TestVerifyExecutingActionTargetRejectsUnexpectedGeneration(t *testing.T) {
	action, agent := executingActionFixture(api.GovernanceActionRestart)
	agent.Generation++
	if err := verifyExecutingActionTarget(agent, action); err == nil {
		t.Fatal("metadata-only action accepted a changed target generation")
	}
	action.Spec.Operation = api.GovernanceActionSuspend
	agent.Generation = action.Spec.TargetRef.Generation + 1 // the action's own spec mutation
	if err := verifyExecutingActionTarget(agent, action); err != nil {
		t.Fatalf("Suspend action rejected its one permitted generation change: %v", err)
	}
	agent.Generation++
	if err := verifyExecutingActionTarget(agent, action); err == nil {
		t.Fatal("Suspend action accepted an additional target generation change")
	}
}

func executingActionFixture(operation api.GovernanceActionOperation) (*api.GovernanceAction, *api.Agent) {
	projections := api.GovernanceActionProjections{
		AuthorizationRevision: "authorization", SandboxRevision: "sandbox", GatewayPolicyRevision: "gateway", CredentialPolicyRevision: "credential",
	}
	action := &api.GovernanceAction{
		ObjectMeta: metav1.ObjectMeta{Name: "action", UID: "action-uid", Generation: 1},
		Spec: api.GovernanceActionSpec{
			TargetRef: api.GovernanceActionTargetRef{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: "agent", UID: "agent-uid", Generation: 7},
			Operation: operation, ExpectedProjections: projections, ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)),
		},
		Status: api.GovernanceActionStatus{Phase: "Executing"},
	}
	agent := &api.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: "agent-uid", Generation: 7, Annotations: map[string]string{api.AnnotationGovernanceActionClaim: string(action.UID)}},
		Status: api.AgentStatus{Governance: &api.GovernanceStatus{
			Authorization: &api.GovernanceAuthorizationStatus{Revision: projections.AuthorizationRevision},
			Projections: api.GovernanceProjectionsStatus{
				Sandbox:          api.GovernanceRevisionProjection{ExpectedRevision: projections.SandboxRevision},
				Gateway:          api.GovernanceRevisionProjection{ExpectedRevision: projections.GatewayPolicyRevision},
				CredentialPolicy: api.GovernanceRevisionProjection{ExpectedRevision: projections.CredentialPolicyRevision},
			},
		}},
	}
	return action, agent
}

func governanceActionTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&api.Agent{}, &api.GovernanceAction{}).
		WithObjects(objects...).Build()
}
