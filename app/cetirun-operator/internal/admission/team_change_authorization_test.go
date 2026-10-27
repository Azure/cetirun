// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestControllerTeamChangeCommitIsNarrow(t *testing.T) {
	request := api.TeamChangeRequest{RequestID: "team-change-0001", ExpectedUID: "agent-uid", ExpectedResourceVersion: "1", TargetTeam: "new"}
	state := &api.TeamChangeStatus{TeamChangeRequest: request, Phase: api.TeamChangeCommitting, Fenced: true, Stopped: true, TransitionID: "transition-0001",
		TargetTeamUID: "team-new", TargetSandboxUID: "box-new", TargetPolicyUID: "policy-new"}
	state.TargetSandbox = "new-box"
	old := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: "agent-uid", ResourceVersion: "2", Finalizers: []string{sandbox.HoldFinalizer}},
		Spec: api.AgentSpec{Team: "old", SandboxRef: "old-box", TeamChangeRequest: &request}, Status: api.AgentStatus{TeamChange: state}}
	next := old.DeepCopy()
	next.Spec.Team, next.Spec.SandboxRef = "new", "new-box"
	next.Annotations = map[string]string{}
	reauth, err := sandboxbinding.FormatReauthorization(request.RequestID, sandbox.Binding{AgentUID: old.UID, TeamUID: state.TargetTeamUID, SandboxUID: state.TargetSandboxUID, AgentPolicyUID: state.TargetPolicyUID})
	if err != nil {
		t.Fatal(err)
	}
	next.Annotations[sandboxbinding.ReauthorizeAnnotation] = reauth
	h := &Handler{config: Config{NativeExecution: true, ControllerUsername: testController}}
	ar := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Update, UserInfo: authenticationv1.UserInfo{Username: testController}}}
	if !h.controllerTeamChangeCommit(ar, next, old) {
		t.Fatal("exact controller commit was rejected")
	}
	next.Spec.User.Team = "forged"
	if h.controllerTeamChangeCommit(ar, next, old) {
		t.Fatal("controller commit accepted an unrelated spec mutation")
	}
}

func TestControllerTeamChangeCommitRequiresCurrentChangingTransition(t *testing.T) {
	ctx := context.Background()
	f, store := coordinatedFixture(t)
	intent := sandboxcoordination.NativeIntent{
		Reconciled: true, Agent: w.ObjectIdentity{Name: "agent", UID: "agent-uid"}, AgentGeneration: 1,
		Namespace: w.ObjectIdentity{Name: "agent-ns", UID: "namespace-uid"}, Home: w.ObjectIdentity{Name: "agent-home", UID: "home-uid"},
		Workload: w.ObjectIdentity{Name: "agent-workload"}, Revision: "test-revision",
		Binding: sandbox.Binding{AgentUID: "agent-uid", TeamUID: f.team.UID, SandboxUID: f.box.UID, AgentPolicyUID: f.policy.UID},
		Sources: []w.SourceReference{
			{APIVersion: api.GroupVersion.String(), Kind: "Team", Name: f.team.Name, UID: f.team.UID, ResourceVersion: "1"},
			{APIVersion: api.GroupVersion.String(), Kind: "Sandbox", Name: f.box.Name, UID: f.box.UID, ResourceVersion: "1"},
			{APIVersion: api.GroupVersion.String(), Kind: "AgentPolicy", Name: f.policy.Name, UID: f.policy.UID, ResourceVersion: "1"},
		},
		SourceDigests: map[string]string{},
	}
	for _, source := range intent.Sources {
		intent.SourceDigests[w.SourceKey(source)] = strings.Repeat("a", 64)
	}
	h := f.handler
	h.config.NativeExecution = true
	request := api.TeamChangeRequest{RequestID: "team-change-transition-0001", ExpectedUID: intent.Agent.UID, ExpectedResourceVersion: "1", TargetTeam: intent.Sources[0].Name}
	native, err := store.AcquireNative(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := store.AcquireTransition(ctx, request.RequestID, native, intent)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err = store.BeginTransitionStop(ctx, ticket)
	if err != nil {
		t.Fatal(err)
	}
	native, err = store.BeginNativeStop(ctx, native)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseNative(ctx, native); err != nil {
		t.Fatal(err)
	}
	ticket, err = store.MarkTransitionStopped(ctx, ticket)
	if err != nil || ticket.Transition.Phase != sandboxcoordination.Changing {
		t.Fatalf("fixture did not create a changing transition: phase=%s err=%v", ticket.Transition.Phase, err)
	}
	state := &api.TeamChangeStatus{TeamChangeRequest: request, Phase: api.TeamChangeCommitting, Fenced: true, Stopped: true,
		TransitionID: ticket.Transition.ID, TargetTeamUID: intent.Binding.TeamUID, TargetSandboxUID: intent.Binding.SandboxUID, TargetPolicyUID: intent.Binding.AgentPolicyUID}
	state.TargetTeam, state.TargetSandbox = request.TargetTeam, intent.SelectedSandbox()
	old := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: intent.Agent.Name, UID: intent.Agent.UID, Finalizers: []string{sandbox.HoldFinalizer}},
		Spec: api.AgentSpec{Team: intent.Sources[0].Name, SandboxRef: intent.SelectedSandbox(), TeamChangeRequest: &request}, Status: api.AgentStatus{TeamChange: state}}
	if err := h.validateControllerTeamChangeTransition(ctx, old, old); err != nil {
		t.Fatalf("exact changing transition was rejected: %v", err)
	}

	for name, mutate := range map[string]func(*api.TeamChangeStatus, *sandboxcoordination.Store){
		"missing transition": func(state *api.TeamChangeStatus, _ *sandboxcoordination.Store) {
			state.TransitionID = "other-transition"
		},
		"target binding mismatch": func(state *api.TeamChangeStatus, _ *sandboxcoordination.Store) {
			state.TargetPolicyUID = "other-policy"
		},
		"authority replaced": func(_ *api.TeamChangeStatus, store *sandboxcoordination.Store) {
			store.Identity.UID = "replacement-authority"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := old.DeepCopy()
			candidate.Status.TeamChange = old.Status.TeamChange.DeepCopy()
			candidateStore := *h.coordination
			mutate(candidate.Status.TeamChange, &candidateStore)
			original := h.coordination
			h.coordination = &candidateStore
			defer func() { h.coordination = original }()
			if err := h.validateControllerTeamChangeTransition(ctx, candidate, candidate); err == nil {
				t.Fatal("invalid controller Team change transition was accepted")
			}
		})
	}
	starting, err := store.StartTransitionTarget(ctx, ticket)
	if err != nil {
		t.Fatal(err)
	}
	if starting.Transition.Phase != sandboxcoordination.Starting {
		t.Fatalf("fixture did not advance to Starting: %s", starting.Transition.Phase)
	}
	if err := h.validateControllerTeamChangeTransition(ctx, old, old); err == nil {
		t.Fatal("Starting transition was accepted for controller Team commit")
	}
}

func TestControllerTeamChangeCommitRejectsNonChangingTransition(t *testing.T) {
	for _, phase := range []sandboxcoordination.Phase{sandboxcoordination.Reserved, sandboxcoordination.Stopping, sandboxcoordination.Starting} {
		t.Run(string(phase), func(t *testing.T) {
			h, old := controllerTeamChangeTransitionFixture(t, phase)
			if err := h.validateControllerTeamChangeTransition(context.Background(), old, old); err == nil {
				t.Fatalf("%s transition was accepted for controller Team commit", phase)
			}
		})
	}
}

func controllerTeamChangeTransitionFixture(t *testing.T, phase sandboxcoordination.Phase) (*Handler, *api.Agent) {
	t.Helper()
	ctx := context.Background()
	f, store := coordinatedFixture(t)
	intent := sandboxcoordination.NativeIntent{
		Reconciled: true, Agent: w.ObjectIdentity{Name: "agent", UID: "agent-uid"}, AgentGeneration: 1,
		Namespace: w.ObjectIdentity{Name: "agent-ns", UID: "namespace-uid"}, Home: w.ObjectIdentity{Name: "agent-home", UID: "home-uid"},
		Workload: w.ObjectIdentity{Name: "agent-workload"}, Revision: "test-revision",
		Binding: sandbox.Binding{AgentUID: "agent-uid", TeamUID: f.team.UID, SandboxUID: f.box.UID, AgentPolicyUID: f.policy.UID},
		Sources: []w.SourceReference{
			{APIVersion: api.GroupVersion.String(), Kind: "Team", Name: f.team.Name, UID: f.team.UID, ResourceVersion: "1"},
			{APIVersion: api.GroupVersion.String(), Kind: "Sandbox", Name: f.box.Name, UID: f.box.UID, ResourceVersion: "1"},
			{APIVersion: api.GroupVersion.String(), Kind: "AgentPolicy", Name: f.policy.Name, UID: f.policy.UID, ResourceVersion: "1"},
		}, SourceDigests: map[string]string{},
	}
	for _, source := range intent.Sources {
		intent.SourceDigests[w.SourceKey(source)] = strings.Repeat("a", 64)
	}
	native, err := store.AcquireNative(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	request := api.TeamChangeRequest{RequestID: "team-change-non-changing-0001", ExpectedUID: intent.Agent.UID, ExpectedResourceVersion: "1", TargetTeam: intent.Sources[0].Name}
	ticket, err := store.AcquireTransition(ctx, request.RequestID, native, intent)
	if err != nil {
		t.Fatal(err)
	}
	if phase != sandboxcoordination.Reserved {
		ticket, err = store.BeginTransitionStop(ctx, ticket)
		if err != nil {
			t.Fatal(err)
		}
	}
	if phase == sandboxcoordination.Starting {
		native, err = store.BeginNativeStop(ctx, native)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ReleaseNative(ctx, native); err != nil {
			t.Fatal(err)
		}
		ticket, err = store.MarkTransitionStopped(ctx, ticket)
		if err != nil {
			t.Fatal(err)
		}
		ticket, err = store.StartTransitionTarget(ctx, ticket)
		if err != nil {
			t.Fatal(err)
		}
	}
	state := &api.TeamChangeStatus{TeamChangeRequest: request, Phase: api.TeamChangeCommitting, Fenced: true, Stopped: true,
		TransitionID: ticket.Transition.ID, TargetTeamUID: intent.Binding.TeamUID, TargetSandboxUID: intent.Binding.SandboxUID, TargetPolicyUID: intent.Binding.AgentPolicyUID}
	state.TargetTeam, state.TargetSandbox = request.TargetTeam, intent.SelectedSandbox()
	old := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: intent.Agent.Name, UID: intent.Agent.UID, Finalizers: []string{sandbox.HoldFinalizer}},
		Spec: api.AgentSpec{Team: intent.Sources[0].Name, SandboxRef: intent.SelectedSandbox(), TeamChangeRequest: &request}, Status: api.AgentStatus{TeamChange: state}}
	h := f.handler
	h.config.NativeExecution = true
	return h, old
}

func TestTeamChangeRequestAdmissionIsNarrowAndImmutable(t *testing.T) {
	old := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: "agent-uid", ResourceVersion: "17"},
		Spec: api.AgentSpec{Team: "source", SandboxRef: "source-box"}}
	change := &api.TeamChangeRequest{RequestID: "team-change-request-0001", ExpectedUID: old.UID,
		ExpectedResourceVersion: old.ResourceVersion, TargetTeam: "target"}
	h := &Handler{config: Config{AdminUsers: []string{"admin"}}}
	admin := authenticationv1.UserInfo{Username: "admin"}

	t.Run("accepts exact isolated request", func(t *testing.T) {
		next := old.DeepCopy()
		next.Spec.TeamChangeRequest = change.DeepCopy()
		if err := h.validateTeamChangeRequest(teamChangeAdmissionRequest(t, old, next, admin), next, old); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("rejects stale precondition", func(t *testing.T) {
		next := old.DeepCopy()
		next.Spec.TeamChangeRequest = change.DeepCopy()
		next.Spec.TeamChangeRequest.ExpectedResourceVersion = "16"
		if err := h.validateTeamChangeRequest(teamChangeAdmissionRequest(t, old, next, admin), next, old); err == nil {
			t.Fatal("stale resourceVersion was accepted")
		}
	})
	t.Run("rejects request plus configuration mutation", func(t *testing.T) {
		next := old.DeepCopy()
		next.Spec.TeamChangeRequest = change.DeepCopy()
		next.Spec.Agent.Type = api.AgentTypeHermes
		if err := h.validateTeamChangeRequest(teamChangeAdmissionRequest(t, old, next, admin), next, old); err == nil {
			t.Fatal("request was allowed to carry an unrelated Agent mutation")
		}
	})
	t.Run("rejects a budget override in the request", func(t *testing.T) {
		next := old.DeepCopy()
		next.Spec.TeamChangeRequest = change.DeepCopy()
		next.Spec.TeamChangeRequest.ModelAccess = &api.ModelAccessSpec{MonthlyBudgetUSD: "100"}
		if err := h.validateTeamChangeRequest(teamChangeAdmissionRequest(t, old, next, admin), next, old); err == nil {
			t.Fatal("Team change request was allowed to set a budget override")
		}
	})
	t.Run("rejects payload replacement for the same request ID", func(t *testing.T) {
		oldWithRequest := old.DeepCopy()
		oldWithRequest.Spec.TeamChangeRequest = change.DeepCopy()
		next := oldWithRequest.DeepCopy()
		next.Spec.TeamChangeRequest.TargetTeam = "other-target"
		if err := h.validateTeamChangeRequest(teamChangeAdmissionRequest(t, oldWithRequest, next, admin), next, oldWithRequest); err == nil {
			t.Fatal("same request ID with a changed payload was accepted")
		}
	})
}

func TestControllerTeamChangeCommitPreservesExistingBudgetOverride(t *testing.T) {
	request := api.TeamChangeRequest{RequestID: "team-change-budget-0001", ExpectedUID: "agent-uid", ExpectedResourceVersion: "1", TargetTeam: "new",
		ModelAccess: &api.ModelAccessSpec{SharedModels: ptr.To(false), GitHubCopilot: ptr.To(true)}}
	state := &api.TeamChangeStatus{TeamChangeRequest: request, Phase: api.TeamChangeCommitting, Fenced: true, Stopped: true, TransitionID: "transition-0001",
		TargetTeamUID: "team-new", TargetSandboxUID: "box-new", TargetPolicyUID: "policy-new"}
	state.TargetSandbox = "new-box"
	old := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: "agent-uid", ResourceVersion: "2", Finalizers: []string{sandbox.HoldFinalizer}},
		Spec: api.AgentSpec{Team: "old", SandboxRef: "old-box", ModelAccess: &api.ModelAccessSpec{MonthlyBudgetUSD: "7"}, TeamChangeRequest: &request}, Status: api.AgentStatus{TeamChange: state}}
	next := old.DeepCopy()
	next.Spec.Team, next.Spec.SandboxRef = "new", "new-box"
	next.Spec.ModelAccess = teamChangeModelAccess(old, state)
	if next.Spec.ModelAccess.MonthlyBudgetUSD != "7" || !ptr.Deref(next.Spec.ModelAccess.GitHubCopilot, false) || ptr.Deref(next.Spec.ModelAccess.SharedModels, true) {
		t.Fatalf("controller Team change did not merge source switches with the retained budget: %+v", next.Spec.ModelAccess)
	}
	next.Annotations = map[string]string{}
	reauth, err := sandboxbinding.FormatReauthorization(request.RequestID, sandbox.Binding{AgentUID: old.UID, TeamUID: state.TargetTeamUID, SandboxUID: state.TargetSandboxUID, AgentPolicyUID: state.TargetPolicyUID})
	if err != nil {
		t.Fatal(err)
	}
	next.Annotations[sandboxbinding.ReauthorizeAnnotation] = reauth
	h := &Handler{config: Config{NativeExecution: true, ControllerUsername: testController}}
	ar := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Update, UserInfo: authenticationv1.UserInfo{Username: testController}}}
	if !h.controllerTeamChangeCommit(ar, next, old) {
		t.Fatal("controller commit rejected the retained budget override")
	}
	forged := next.DeepCopy()
	forged.Spec.ModelAccess.MonthlyBudgetUSD = "100"
	if h.controllerTeamChangeCommit(ar, forged, old) {
		t.Fatal("controller Team change commit accepted a changed budget override")
	}
	if err := h.protectedPolicyBudget(ar, forged, old); err == nil {
		t.Fatal("controller Team change commit bypassed budget authorization")
	}
}

func TestCompletedTeamChangeRequestIDRequiresOriginalPayload(t *testing.T) {
	original := api.TeamChangeRequest{RequestID: "team-change-completed-0001", ExpectedUID: "agent-uid",
		ExpectedResourceVersion: "17", TargetTeam: "target"}
	old := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: "agent-uid", ResourceVersion: "23"},
		Spec: api.AgentSpec{Team: "target", SandboxRef: "selected-box"},
		Status: api.AgentStatus{TeamChange: &api.TeamChangeStatus{
			// The inline field is the resolved execution selection. It differs
			// from the caller's omitted targetSandbox and must not be used for
			// idempotency.
			TeamChangeRequest: api.TeamChangeRequest{RequestID: original.RequestID, ExpectedUID: original.ExpectedUID,
				ExpectedResourceVersion: original.ExpectedResourceVersion, TargetTeam: original.TargetTeam, TargetSandbox: "selected-box"},
			OriginalRequest: original.DeepCopy(), Phase: api.TeamChangeCompleted,
		}}}
	h := &Handler{config: Config{AdminUsers: []string{"admin"}}}
	admin := authenticationv1.UserInfo{Username: "admin"}

	t.Run("accepts exact retry after request was cleared", func(t *testing.T) {
		next := old.DeepCopy()
		next.Spec.TeamChangeRequest = original.DeepCopy()
		if err := h.validateTeamChangeRequest(teamChangeAdmissionRequest(t, old, next, admin), next, old); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("rejects explicit sandbox substituted for omitted sandbox", func(t *testing.T) {
		next := old.DeepCopy()
		next.Spec.TeamChangeRequest = original.DeepCopy()
		next.Spec.TeamChangeRequest.TargetSandbox = "selected-box"
		if err := h.validateTeamChangeRequest(teamChangeAdmissionRequest(t, old, next, admin), next, old); err == nil {
			t.Fatal("same request ID with a resolved sandbox was accepted")
		}
	})
	t.Run("rejects changed target", func(t *testing.T) {
		next := old.DeepCopy()
		next.Spec.TeamChangeRequest = original.DeepCopy()
		next.Spec.TeamChangeRequest.TargetTeam = "other-target"
		if err := h.validateTeamChangeRequest(teamChangeAdmissionRequest(t, old, next, admin), next, old); err == nil {
			t.Fatal("same request ID with a changed target was accepted")
		}
	})
}

func TestPendingTeamChangeAllowsOnlySuspension(t *testing.T) {
	change := api.TeamChangeRequest{RequestID: "team-change-pending-0001", ExpectedUID: "agent-uid", ExpectedResourceVersion: "17", TargetTeam: "target"}
	old := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: "agent-uid", ResourceVersion: "17"},
		Spec:   api.AgentSpec{Team: "source", SandboxRef: "source-box", TeamChangeRequest: &change},
		Status: api.AgentStatus{TeamChange: &api.TeamChangeStatus{TeamChangeRequest: change, Phase: api.TeamChangeStopping, Fenced: true}}}
	h := &Handler{config: Config{AdminUsers: []string{"admin"}}}
	admin := authenticationv1.UserInfo{Username: "admin"}

	next := old.DeepCopy()
	next.Spec.Model = "forged"
	if err := h.validateTeamChangeRequest(teamChangeAdmissionRequest(t, old, next, admin), next, old); err == nil {
		t.Fatal("pending Team change allowed a model mutation")
	}
	next = old.DeepCopy()
	next.Spec.Suspended = true
	if err := h.validateTeamChangeRequest(teamChangeAdmissionRequest(t, old, next, admin), next, old); err != nil {
		t.Fatalf("pending Team change rejected suspension: %v", err)
	}
}

func teamChangeAdmissionRequest(t *testing.T, old, next *api.Agent, user authenticationv1.UserInfo) admission.Request {
	t.Helper()
	oldRaw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	nextRaw, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Update, UserInfo: user,
		Object: runtime.RawExtension{Raw: nextRaw}, OldObject: runtime.RawExtension{Raw: oldRaw}}}
}
