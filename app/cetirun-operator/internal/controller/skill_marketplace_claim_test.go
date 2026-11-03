// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// withSkillMarketplace enables the Skill Marketplace for the reconciler and its
// Kubernetes prerequisites, as CETIRUN_SKILL_MARKETPLACE does, and Memory,
// whose composed context the Team subtree of the skills claim requires.
func withSkillMarketplace(r *AgentReconciler, _ client.Client) {
	r.SkillMarketplace = true
	r.SandboxPreparation.Prerequisites.(*KubernetesSandboxPrerequisites).SkillMarketplace = true
	r.MemoryImage = "registry.example.com/memory:1.2.3"
}

// createMarketplaceSkillsClaim creates the Agent's skills claim the way the
// Skill Marketplace does: owned by, and annotated for, the Agent.
func createMarketplaceSkillsClaim(t *testing.T, f *nativeSandboxFixture, input testfixture.Fixture) *corev1.PersistentVolumeClaim {
	t.Helper()
	agent := f.current(t)
	claim := input.Input.Skills.DeepCopy()
	claim.UID, claim.ResourceVersion = "", ""
	claim.OwnerReferences = []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: agent.Name, UID: agent.UID}}
	claim.Labels = map[string]string{api.SkillMarketplaceManagedLabel: "true"}
	claim.Annotations = map[string]string{
		w.AgentUIDAnnotation: string(agent.UID), w.AgentNameAnnotation: agent.Name,
		api.SkillMarketplaceTeamUIDAnnotation: string(input.Team.UID),
	}
	if err := f.kube.Create(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	return claim
}

// assertSkillsClaimWait checks a pass that waited for the skills claim: no
// error, no failure or hold, the wait recorded, and the key left unblocked.
func assertSkillsClaimWait(t *testing.T, f *nativeSandboxFixture, result ctrl.Result, err error) {
	t.Helper()
	if err != nil || result.RequeueAfter != skillsClaimPendingRetry {
		t.Fatalf("result %+v, %v; want a requeue after %s", result, err, skillsClaimPendingRetry)
	}
	agent := f.current(t)
	if agent.Status.Phase == api.PhaseFailed || slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) {
		t.Fatalf("waiting for the skills claim failed or held the Agent: phase %q, finalizers %v", agent.Status.Phase, agent.Finalizers)
	}
	condition := apimeta.FindStatusCondition(agent.Status.Conditions, builtInPrerequisitesReady)
	if condition == nil || condition.Status != metav1.ConditionFalse ||
		condition.Reason != skillsClaimPendingReason || condition.Message != skillsClaimPendingMessage {
		t.Fatalf("prerequisite readiness = %+v, want the Skill Marketplace wait", condition)
	}
	for _, key := range f.gateway.keys {
		if key.Blocked {
			t.Fatal("waiting for the skills claim blocked the Agent's key")
		}
	}
}

// A new native Agent of a marketplace Team waits for the skills claim that the
// Skill Marketplace creates, then starts with only its Team's subtree.
func TestSkillMarketplaceWaitsForTheTeamSkillsClaim(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			f, input := newUnreconciledSandboxRuntimeFixture(t, true, []string{"example-skill"}, true, kind, true, false, withSkillMarketplace)
			for range 2 {
				result, err := f.reconcile()
				assertSkillsClaimWait(t, f, result, err)
			}
			agent := f.current(t)
			ready := apimeta.FindStatusCondition(agent.Status.Conditions, api.ConditionReady)
			if (agent.Status.Phase != api.PhasePending && agent.Status.Phase != api.PhaseProvisioning) ||
				ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != skillsClaimPendingReason {
				t.Fatalf("new Agent phase %q, Ready %+v; want a pending wait for the skills claim", agent.Status.Phase, ready)
			}
			if f.installer.stages != 0 {
				t.Fatal("the Agent was installed before its skills claim existed")
			}
			if err := f.kube.Get(ctx, client.ObjectKey{Namespace: instanceNamespace(agent.Name), Name: instanceServiceName(agent.Name)},
				&appsv1.StatefulSet{}); !apierrors.IsNotFound(err) {
				t.Fatal("the Agent workload exists before its skills claim:", err)
			}

			claim := createMarketplaceSkillsClaim(t, f, input)
			for pass := 0; pass < 3 && f.installer.stages == 0; pass++ {
				f.reconcileOK(t)
			}
			if f.installer.stages != 1 {
				t.Fatalf("the Agent did not proceed once its skills claim existed: %d applies", f.installer.stages)
			}
			agent = f.current(t)
			for _, conditionType := range []string{builtInPrerequisitesReady, api.ConditionReady} {
				if condition := apimeta.FindStatusCondition(agent.Status.Conditions, conditionType); condition != nil && condition.Reason == skillsClaimPendingReason {
					t.Fatalf("%s still reports the finished wait: %+v", conditionType, condition)
				}
			}
			team := &api.Team{}
			if err := f.kube.Get(ctx, client.ObjectKey{Name: input.Team.Name}, team); err != nil {
				t.Fatal(err)
			}
			expected := reconciledExpectationForTest(t, f)
			if !slices.ContainsFunc(expected.Sources, func(source w.SourceReference) bool {
				return source.Kind == "PersistentVolumeClaim" && source.Name == claim.Name && source.UID != ""
			}) {
				t.Fatal("the marketplace claim is not a pinned source of the Agent")
			}
			mount := corev1.VolumeMount{Name: "shared-skills", MountPath: w.SkillMarketplaceMountPath,
				SubPath: api.SkillMarketplaceTeamPath(string(team.UID)), ReadOnly: true}
			selection := corev1.EnvVar{Name: w.SkillMarketplaceSelectionEnv, Value: api.SkillMarketplaceSelectionDigest(team.Spec.Skills)}
			index := slices.IndexFunc(expected.Workloads, func(workload w.RenderedWorkload) bool { return workload.Kind == "StatefulSet" })
			if index < 0 {
				t.Fatal("the expectation has no Agent workload")
			}
			spec := expected.Workloads[index].Template.Spec
			initializer := slices.IndexFunc(spec.InitContainers, func(container corev1.Container) bool { return container.Name == "init-home-data" })
			if initializer < 0 || !slices.ContainsFunc(spec.InitContainers[initializer].VolumeMounts, func(actual corev1.VolumeMount) bool {
				return reflect.DeepEqual(actual, mount)
			}) || !slices.Contains(spec.InitContainers[initializer].Env, selection) {
				t.Fatal("the HOME initializer does not read the Team's subtree and selection")
			}
			if slices.ContainsFunc(spec.Containers[0].VolumeMounts, func(mount corev1.VolumeMount) bool { return mount.Name == "shared-skills" }) {
				t.Fatal("the Agent container mounts the skills claim")
			}
		})
	}
}

// A running Agent whose claim is missing is not stopped: the wait changes no
// workload, key, route or readiness that the Agent already has.
func TestSkillMarketplaceClaimWaitKeepsARunningAgent(t *testing.T) {
	ctx := context.Background()
	f, input := newUnreconciledSandboxRuntimeFixture(t, true, []string{"example-skill"}, true, "openclaw", true, false, withSkillMarketplace)
	result, err := f.reconcile()
	assertSkillsClaimWait(t, f, result, err)
	claim := createMarketplaceSkillsClaim(t, f, input)
	for pass := 0; pass < 3 && f.installer.stages == 0; pass++ {
		f.reconcileOK(t)
	}
	workload := f.workload(t)
	workload.Status.ObservedGeneration = workload.Generation
	workload.Status.Replicas, workload.Status.ReadyReplicas, workload.Status.UpdatedReplicas = 1, 1, 1
	if err := f.kube.Status().Update(ctx, workload); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 3 && f.current(t).Status.Phase != api.PhaseReady; pass++ {
		f.reconcileOK(t)
	}
	before := f.current(t)
	if before.Status.Phase != api.PhaseReady || !apimeta.IsStatusConditionTrue(before.Status.Conditions, api.ConditionReady) {
		t.Fatalf("fixture Agent is not running: phase %q, conditions %v", before.Status.Phase, before.Status.Conditions)
	}
	workload, stages := f.workload(t), f.installer.stages
	state, _, err := f.r.readReconciledSandboxExpectation(ctx, before)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.kube.Delete(ctx, claim); err != nil {
		t.Fatal(err)
	}
	result, err = f.reconcile()
	assertSkillsClaimWait(t, f, result, err)
	after := f.current(t)
	if after.Status.Phase != api.PhaseReady || !apimeta.IsStatusConditionTrue(after.Status.Conditions, api.ConditionReady) ||
		after.Status.AccessURL != before.Status.AccessURL || after.Status.AIGateway.KeyID != before.Status.AIGateway.KeyID {
		t.Fatalf("the wait changed the running Agent: phase %q, conditions %v", after.Status.Phase, after.Status.Conditions)
	}
	current := f.workload(t)
	if current.UID != workload.UID || current.ResourceVersion != workload.ResourceVersion ||
		ptr.Deref(current.Spec.Replicas, 0) != 1 || f.installer.stages != stages {
		t.Fatal("the wait scaled, replaced or re-applied the running workload")
	}
	currentState := &corev1.ConfigMap{}
	if err := f.kube.Get(ctx, client.ObjectKeyFromObject(state), currentState); err != nil ||
		currentState.ResourceVersion != state.ResourceVersion {
		t.Fatal("the wait changed the admitted expectation:", err)
	}
}

// Without the marketplace a missing skills claim stays a configuration error.
func TestMissingSkillsClaimWithoutMarketplaceFailsTheAgent(t *testing.T) {
	f, _ := newUnreconciledSandboxRuntimeFixture(t, true, []string{"example-skill"}, true, "openclaw", true, false)
	if _, err := f.reconcile(); err == nil {
		t.Fatal("the missing skills claim was not reported")
	}
	agent := f.current(t)
	condition := apimeta.FindStatusCondition(agent.Status.Conditions, api.ConditionAgentConfigReady)
	if agent.Status.Phase != api.PhaseFailed || condition == nil || condition.Reason != "AgentConfigInvalid" ||
		!strings.HasPrefix(condition.Message, "Sandbox skills require the configured skills PVC or validated publication") {
		t.Fatalf("phase %q, AgentConfigReady %+v; want the existing configuration error", agent.Status.Phase, condition)
	}
	if f.installer.stages != 0 {
		t.Fatal("an Agent without its skills claim was installed")
	}
}

func TestSkillsClaimPendingOnlyForMarketplaceBuiltinClaims(t *testing.T) {
	missing := fmt.Errorf("read Sandbox prerequisite cetirun-example/skills-example: %w",
		apierrors.NewNotFound(corev1.Resource("persistentvolumeclaims"), "skills-example"))
	builtin := &api.Agent{Spec: api.AgentSpec{Agent: api.HarnessSpec{Type: api.AgentTypeHermes}}}
	custom := &api.Agent{Spec: api.AgentSpec{Agent: api.HarnessSpec{Type: api.AgentTypeCustom}}}
	enabled := &KubernetesSandboxPrerequisites{SkillMarketplace: true}
	if !enabled.skillsClaimPending(missing, builtin, true) {
		t.Fatal("a missing marketplace claim is not a wait")
	}
	for name, test := range map[string]struct {
		source     *KubernetesSandboxPrerequisites
		err        error
		agent      *api.Agent
		reconciled bool
	}{
		"marketplace disabled": {&KubernetesSandboxPrerequisites{}, missing, builtin, true},
		"held native lifetime": {enabled, missing, builtin, false},
		"Custom":               {enabled, missing, custom, true},
		"read failure": {enabled, fmt.Errorf("read Sandbox prerequisite cetirun-example/skills-example: %w",
			apierrors.NewServiceUnavailable("synthetic outage")), builtin, true},
		"terminating claim": {enabled, fmt.Errorf("%w: Sandbox prerequisite cetirun-example/skills-example is not the persisted, non-terminating object",
			errSandboxSourceChanged), builtin, true},
	} {
		t.Run(name, func(t *testing.T) {
			if test.source.skillsClaimPending(test.err, test.agent, test.reconciled) {
				t.Fatal("the existing skills claim error became a wait")
			}
		})
	}
	wrapped := fmt.Errorf("%w: %w", errSkillsClaimPending, missing)
	if !errors.Is(wrapped, errSkillsClaimPending) || !apierrors.IsNotFound(wrapped) {
		t.Fatal("the wait lost its sentinel or its API cause")
	}
}

// With the marketplace, Custom drops marketplace skills, so they do not select
// the skills claim, which holds the subtree of every Team.
func TestSkillMarketplaceSkillsDoNotSelectAClaimForCustom(t *testing.T) {
	team := &api.Team{Spec: api.TeamSpec{Skills: []string{"example-skill"}}}
	agent := func(kind api.AgentType) *api.Agent {
		return &api.Agent{Spec: api.AgentSpec{Agent: api.HarnessSpec{Type: kind}}}
	}
	for _, test := range []struct {
		name        string
		kind        api.AgentType
		marketplace bool
		skills      []string
		want        w.SkillsMode
	}{
		{"Custom", api.AgentTypeCustom, true, []string{"example-skill"}, w.SkillsModeEmpty},
		{"Custom without the marketplace", api.AgentTypeCustom, false, []string{"example-skill"}, ""},
		{"Custom npm package", api.AgentTypeCustom, true, []string{"example-skill", "npm:@example/package"}, ""},
		{"built-in", api.AgentTypeOpenClaw, true, []string{"example-skill"}, ""},
		{"no selection", api.AgentTypeHermes, true, nil, w.SkillsModeEmpty},
	} {
		t.Run(test.name, func(t *testing.T) {
			team.Spec.Skills = test.skills
			if got := sandboxSkillsMode(true, "", test.marketplace, agent(test.kind), team); got != test.want {
				t.Fatalf("skills mode = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSkillMarketplaceReachesKubernetesPrerequisites(t *testing.T) {
	config, err := ParseSandboxSourcesConfig(nativeSandboxSourcesJSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`"SkillMarketplace":true`, `"skillMarketplace":true`} {
		if _, err := ParseSandboxSourcesConfig(strings.Replace(nativeSandboxSourcesJSON, `"enabled":true`, `"enabled":true,`+raw, 1)); err == nil {
			t.Fatalf("CETIRUN_SANDBOX_SOURCES enabled the Skill Marketplace with %s", raw)
		}
	}
	r, kube, _ := aiGatewayTestReconciler(t)
	for _, enabled := range []bool{false, true} {
		config.SkillMarketplace = enabled
		preparation, err := NewKubernetesSandboxPreparation(config, kube, &nativeSandboxInstaller{}, r.OperatorNamespace, "0.1.1")
		if err != nil {
			t.Fatal(err)
		}
		if preparation.Prerequisites.(*KubernetesSandboxPrerequisites).SkillMarketplace != enabled {
			t.Fatalf("Skill Marketplace %t did not reach the Kubernetes prerequisites", enabled)
		}
	}
}
