// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// chartOutageHelm leaves the common governed Sandbox renderer and apply path
// intact while making only chart preparation unavailable.
type chartOutageHelm struct {
	HelmInstaller
	err      error
	prepares int
}

func (h *chartOutageHelm) Prepare(namespace string, values map[string]any, version string) (*helm.PreparedRelease, error) {
	h.prepares++
	if h.err != nil {
		return nil, h.err
	}
	return h.HelmInstaller.Prepare(namespace, values, version)
}

func chartOutageError(version string) error {
	return &helm.ChartDownloadError{Chart: "oci://registry.example.com/helm/cetirun", Version: version,
		Err: errors.New("synthetic registry outage")}
}

func chartWarningEvents(recorder *record.FakeRecorder) []string {
	var warnings []string
	for {
		select {
		case event := <-recorder.Events:
			if strings.HasPrefix(event, "Warning ") {
				warnings = append(warnings, event)
			}
		default:
			return warnings
		}
	}
}

func setCommonWorkloadReady(t *testing.T, f *nativeSandboxFixture, replicas int32) {
	t.Helper()
	workload := f.workload(t)
	workload.Status.ReadyReplicas = replicas
	workload.Status.UpdatedReplicas = replicas
	workload.Status.ObservedGeneration = workload.Generation
	if err := f.kube.Status().Update(context.Background(), workload); err != nil {
		t.Fatal(err)
	}
}

func setCommonAgentReady(t *testing.T, f *nativeSandboxFixture) {
	t.Helper()
	ctx := context.Background()
	agent := f.current(t)
	resolution, err := f.r.resolvePolicy(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	sandbox := &api.Sandbox{}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: agent.Spec.SandboxRef}, sandbox); err != nil {
		t.Fatal(err)
	}
	agent.Status.Phase = api.PhaseReady
	agent.Status.Policy = &api.PolicyStatus{Phase: "Applied", ObservedGeneration: agent.Generation,
		Enforcement: api.PolicyEnforcementStatus{
			Sandbox:    api.PolicyRevisionExpectation{DesiredRevision: resolution.sandboxRevision},
			Gateway:    api.PolicyRevisionExpectation{DesiredRevision: resolution.gatewayRevision},
			Credential: api.PolicyRevisionExpectation{DesiredRevision: resolution.credentialRevision},
		}}
	agent.Status.Sandbox = &api.SandboxStatus{TeamUID: resolution.team.UID, SandboxUID: sandbox.UID,
		AgentPolicyUID: resolution.policy.UID, AppliedRevision: resolution.sandboxRevision}
	agent.Status.AIGateway = &api.AIGatewayStatus{Phase: api.AIGatewayPhaseReady, Routed: true,
		PolicyRevision: resolution.gatewayRevision}
	setCondition(&agent.Status.Conditions, api.ConditionHelmDeployed,
		metav1.ConditionTrue, "ChartInstalled", "Helm chart successfully installed")
	setCondition(&agent.Status.Conditions, api.ConditionAgentConfigReady,
		metav1.ConditionTrue, "AgentConfigResolved", "Built-in Harness configuration resolved")
	setCondition(&agent.Status.Conditions, builtInPrerequisitesReady,
		metav1.ConditionTrue, "PrerequisitesReady", "All prerequisites are ready")
	setCondition(&agent.Status.Conditions, api.ConditionPodReady,
		metav1.ConditionTrue, "PodReady", "StatefulSet pod is running and ready")
	setCondition(&agent.Status.Conditions, api.ConditionReady,
		metav1.ConditionTrue, "InstanceReady", "Instance is fully operational")
	setCondition(&agent.Status.Conditions, api.ConditionSandboxReady,
		metav1.ConditionTrue, "SandboxReady", "Sandbox enforcement is verified")
	meta.FindStatusCondition(agent.Status.Conditions, api.ConditionSandboxReady).ObservedGeneration = agent.Generation
	if err := f.kube.Status().Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
}

// A first chart download failure remains a configuration failure: no common
// governed workload may be applied from an unprepared chart.
func TestCommonGovernedChartDownloadFailureBeforeFirstApply(t *testing.T) {
	f, _ := newUnreconciledSandboxRuntimeFixture(t, true, nil, true, "openclaw", true, false)
	outage := &chartOutageHelm{HelmInstaller: f.r.HelmClient, err: chartOutageError(f.agent.Spec.Helm.Version)}
	f.r.HelmClient = outage

	_, err := f.reconcile()
	if err == nil {
		t.Fatal("first chart download failure was not returned")
	}
	agent := f.current(t)
	condition := meta.FindStatusCondition(agent.Status.Conditions, api.ConditionAgentConfigReady)
	if agent.Status.Phase != api.PhaseFailed || condition == nil || condition.Reason != "AgentConfigInvalid" ||
		!strings.Contains(condition.Message, "could not be downloaded from the registry") {
		t.Fatalf("first failure status = %+v, want a rejected configuration", agent.Status)
	}
	if outage.prepares != 1 || f.installer.stages != 0 {
		t.Fatalf("first failed download prepared %d times and applied %d common workloads", outage.prepares, f.installer.stages)
	}
}

// An outage after the common governed workload has been applied must preserve
// that release, continue observing its health, and recover without a new
// apply.  Repeated failed polling must not write status or emit more warnings.
func TestCommonGovernedChartOutagePreservesReleaseAndRecovers(t *testing.T) {
	f := newSandboxRuntimeFixture(t, true, nil, true)
	setCommonAgentReady(t, f)
	agent := f.current(t)
	resolution, err := f.r.resolvePolicy(context.Background(), agent)
	if err != nil || policyReprojectionMustHold(agent, resolution) || !policyEnforcementVerified(agent, resolution) {
		t.Fatalf("test setup did not establish an applied common policy: resolution=%+v err=%v sandbox=%+v gateway=%+v generation=%d policy=%+v conditions=%+v", resolution, err, agent.Status.Sandbox, agent.Status.AIGateway, agent.Generation, agent.Status.Policy, agent.Status.Conditions)
	}
	setCommonWorkloadReady(t, f, 1)
	recorder := record.NewFakeRecorder(20)
	f.r.EventRecorder = recorder
	outage := &chartOutageHelm{HelmInstaller: f.r.HelmClient, err: chartOutageError(f.agent.Spec.Helm.Version)}
	f.r.HelmClient = outage
	stages := f.installer.stages
	want := "Agent chart " + f.agent.Spec.Helm.Version + " could not be downloaded from the registry; the deployed release is unchanged and the operator will retry"

	result, err := f.reconcile()
	if err != nil || result.RequeueAfter != chartUnavailableRetry {
		t.Fatalf("first outage result=%+v err=%v, want retry after %s", result, err, chartUnavailableRetry)
	}
	first := f.current(t)
	condition := meta.FindStatusCondition(first.Status.Conditions, api.ConditionAgentConfigReady)
	if outage.prepares != 1 || first.Status.Phase != api.PhaseReady || condition == nil || condition.Status != metav1.ConditionFalse ||
		condition.Reason != chartUnavailableReason || condition.Message != want || f.installer.stages != stages {
		t.Fatalf("first outage changed release or readiness: status=%+v stages=%d want=%d", first.Status, f.installer.stages, stages)
	}
	if warnings := chartWarningEvents(recorder); len(warnings) != 1 || warnings[0] != "Warning "+chartUnavailableReason+" "+want {
		t.Fatalf("first outage warnings=%q", warnings)
	}

	result, err = f.reconcile()
	if err != nil || result.RequeueAfter != chartUnavailableRetry {
		t.Fatalf("repeated outage result=%+v err=%v", result, err)
	}
	second := f.current(t)
	if outage.prepares != 2 || second.ResourceVersion != first.ResourceVersion || f.installer.stages != stages || len(chartWarningEvents(recorder)) != 0 {
		t.Fatalf("repeated outage changed status, events, or release: RV %s -> %s stages=%d", first.ResourceVersion, second.ResourceVersion, f.installer.stages)
	}

	// The temporary registry error must not freeze workload health reporting.
	setCommonWorkloadReady(t, f, 0)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if current := f.current(t); current.Status.Phase != api.PhaseProvisioning || !meta.IsStatusConditionFalse(current.Status.Conditions, api.ConditionPodReady) {
		t.Fatalf("outage did not report an unready workload: %+v", current.Status)
	}
	setCommonWorkloadReady(t, f, 1)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if current := f.current(t); current.Status.Phase != api.PhaseReady || !meta.IsStatusConditionTrue(current.Status.Conditions, api.ConditionPodReady) {
		t.Fatalf("outage did not report workload recovery: %+v", current.Status)
	}

	// After the registry recovers the normal preparation pass clears the
	// temporary condition without reapplying the unchanged common release.
	outage.err = nil
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	current := f.current(t)
	condition = meta.FindStatusCondition(current.Status.Conditions, api.ConditionAgentConfigReady)
	if condition == nil || condition.Status != metav1.ConditionTrue || f.installer.stages != stages {
		t.Fatalf("recovery status=%+v stages=%d want=%d", current.Status, f.installer.stages, stages)
	}
}

func TestChartInstalledForGeneration(t *testing.T) {
	installed := metav1.Condition{Type: api.ConditionHelmDeployed, Status: metav1.ConditionTrue, Reason: "ChartInstalled"}
	notInstalled := metav1.Condition{Type: api.ConditionHelmDeployed, Status: metav1.ConditionFalse, Reason: "ChartNotInstalled"}
	for name, tc := range map[string]struct {
		conditions []metav1.Condition
		observed   int64
		want       bool
	}{
		"installed for the current generation": {[]metav1.Condition{installed}, 3, true},
		"installed for an earlier generation":  {[]metav1.Condition{installed}, 2, false},
		"chart not installed":                  {[]metav1.Condition{notInstalled}, 3, false},
		"no HelmDeployed condition":            {nil, 3, false},
	} {
		agent := &api.Agent{
			ObjectMeta: metav1.ObjectMeta{Generation: 3},
			Status:     api.AgentStatus{Conditions: tc.conditions, ObservedGeneration: tc.observed},
		}
		if got := chartInstalledForGeneration(agent); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}
