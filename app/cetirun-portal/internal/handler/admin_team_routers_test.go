// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

func testModelRouterDTO(name string) TeamModelRouterDTO {
	return TeamModelRouterDTO{Name: name, Simple: "small-model", Medium: "medium-model", Complex: "copilot/large-model"}
}

func TestAdminTeamKeepsModelRoutersAndReportsTheirStatus(t *testing.T) {
	team := &cetirunv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: "research"},
		Spec: cetirunv1alpha1.TeamSpec{AgentPolicyRef: "standard-policy", SandboxRefs: []string{"standard"}, DefaultSandboxRef: "standard",
			ModelRouters: []cetirunv1alpha1.TeamModelRouter{{Name: "auto", Classifier: cetirunv1alpha1.TeamModelRouterClassifier{Type: "heuristic"},
				Tiers: cetirunv1alpha1.TeamModelRouterTiers{Simple: "small-model", Medium: "medium-model", Complex: "large-model"}}}},
		Status: cetirunv1alpha1.TeamStatus{ModelRouters: []cetirunv1alpha1.TeamModelRouterStatus{
			{Name: "auto", GatewayModel: "cetirun-router-0123456789abcdef", Ready: false, Message: "router auto: tier medium names medium-model, which is not a shared model of the AI Gateway"},
		}},
	}
	h := newAdminTeamHandler(team)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/teams/research", nil)
	req.SetPathValue("name", "research")
	w := httptest.NewRecorder()
	h.GetTeam(w, req)
	var got TeamDTO
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil || w.Code != http.StatusOK {
		t.Fatalf("status %d: %v", w.Code, err)
	}
	if len(got.ModelRouters) != 1 || got.ModelRouters[0].Name != "auto" || got.ModelRouters[0].Classifier != "heuristic" ||
		got.ModelRouters[0].Complex != "large-model" || len(got.ModelRouterStatus) != 1 || got.ModelRouterStatus[0].Ready ||
		!strings.Contains(got.ModelRouterStatus[0].Message, "not a shared model") {
		t.Fatalf("unexpected DTO: %+v", got)
	}
	if strings.Contains(w.Body.String(), "cetirun-router-") {
		t.Fatal("the internal Gateway name stays out of the Portal")
	}

	// An edit that keeps the routers in the body keeps them; one that changes
	// them stores the change.
	current, _ := h.Kube.GetTeam(context.Background(), "research")
	routers := append(got.ModelRouters, TeamModelRouterDTO{Name: "auto-llm", Classifier: "llm", ClassifierModel: "small-model",
		Simple: "small-model", Medium: "medium-model", Complex: "large-model", Reasoning: "copilot/reasoning-model"})
	body, _ := json.Marshal(TeamUpsertRequest{Role: "new", UID: string(current.UID), ResourceVersion: current.ResourceVersion,
		AgentPolicyRef: "standard-policy", SandboxRefs: []string{"standard"}, DefaultSandboxRef: "standard", ModelRouters: routers})
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/teams/research", bytes.NewReader(body))
	req.SetPathValue("name", "research")
	w = httptest.NewRecorder()
	h.UpdateTeam(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
	}
	stored, _ := h.Kube.GetTeam(context.Background(), "research")
	if len(stored.Spec.ModelRouters) != 2 || stored.Spec.ModelRouters[1].Classifier.Type != "llm" ||
		stored.Spec.ModelRouters[1].Classifier.Model != "small-model" || stored.Spec.ModelRouters[1].Tiers.Reasoning != "copilot/reasoning-model" {
		t.Fatalf("stored routers = %+v", stored.Spec.ModelRouters)
	}
}

func TestAdminTeamRejectsInvalidModelRouters(t *testing.T) {
	llmWithoutModel := testModelRouterDTO("auto")
	llmWithoutModel.Classifier = "llm"
	copilotClassifier := testModelRouterDTO("auto")
	copilotClassifier.Classifier, copilotClassifier.ClassifierModel = "llm", "copilot/small-model"
	missingTier := testModelRouterDTO("auto")
	missingTier.Medium = ""
	spacedTier := testModelRouterDTO("auto")
	spacedTier.Simple = "small model"
	licensed := testModelRouterDTO("auto")
	licensed.Classifier = "heuristic_v2"
	tooMany := make([]TeamModelRouterDTO, 0, 11)
	for i := 0; i < 11; i++ {
		tooMany = append(tooMany, testModelRouterDTO("auto-"+string(rune('a'+i))))
	}
	for _, tc := range []struct {
		routers []TeamModelRouterDTO
		want    string
	}{
		{[]TeamModelRouterDTO{testModelRouterDTO("cetirun-auto")}, "reserved"},
		{[]TeamModelRouterDTO{testModelRouterDTO("copilot/auto")}, "without spaces or slashes"},
		{[]TeamModelRouterDTO{testModelRouterDTO("auto"), testModelRouterDTO("auto")}, "defined twice"},
		{[]TeamModelRouterDTO{llmWithoutModel}, "needs a shared model"},
		{[]TeamModelRouterDTO{copilotClassifier}, "needs a shared model"},
		{[]TeamModelRouterDTO{missingTier}, "the medium tier"},
		{[]TeamModelRouterDTO{spacedTier}, "the simple tier"},
		{[]TeamModelRouterDTO{licensed}, "heuristic or llm"},
		{tooMany, "at most 10"},
	} {
		h := newAdminTeamHandler()
		body, _ := json.Marshal(TeamUpsertRequest{Name: "research", AgentPolicyRef: "standard-policy", SandboxRefs: []string{"standard"},
			DefaultSandboxRef: "standard", ModelRouters: tc.routers})
		w := httptest.NewRecorder()
		h.CreateTeam(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/teams", bytes.NewReader(body)))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("routers %+v: status %d body %s, want %q", tc.routers, w.Code, w.Body.String(), tc.want)
		}
	}
}

func TestTeamValidationMessageNamesModelRouterFields(t *testing.T) {
	if !teamValidationFieldPattern.MatchString("spec.modelRouters[2].tiers.medium") ||
		!teamValidationFieldPattern.MatchString("spec.modelRouters[0].classifier.model") ||
		teamValidationFieldPattern.MatchString("spec.modelRouters[0].tiers.other") {
		t.Fatal("the model router field paths must be reported, and only those")
	}
}

func TestTeamModelRouterNamesFollowThePolicy(t *testing.T) {
	team := &cetirunv1alpha1.Team{Spec: cetirunv1alpha1.TeamSpec{ModelRouters: []cetirunv1alpha1.TeamModelRouter{{Name: "auto"}, {Name: "auto-claude"}}}}
	if got := teamModelRouterNames(team, nil); !slices.Equal(got, []string{"auto", "auto-claude"}) {
		t.Fatalf("without a policy: %v", got)
	}
	policy := &cetirunv1alpha1.AgentPolicy{Spec: cetirunv1alpha1.AgentPolicySpec{ModelAccess: cetirunv1alpha1.AgentPolicyModelAccess{
		SharedModels: cetirunv1alpha1.AgentPolicySharedModelsAccess{Allowed: []string{"small-model", "auto-claude"}}}}}
	if got := teamModelRouterNames(team, policy); !slices.Equal(got, []string{"auto-claude"}) {
		t.Fatalf("with a policy: %v", got)
	}
	if got := teamModelRouterNames(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("without a Team: %v", got)
	}
}

func TestAIGatewayResponseNamesRoutersByTheirOwnNames(t *testing.T) {
	agent := &cetirunv1alpha1.Agent{Status: cetirunv1alpha1.AgentStatus{AIGateway: &cetirunv1alpha1.AIGatewayStatus{
		Routed: true, Models: []string{"small-model", "cetirun-router-0123456789abcdef"}, DefaultModel: "cetirun-router-0123456789abcdef",
		ModelRouters: []cetirunv1alpha1.AIGatewayModelRouterStatus{
			{Name: "auto", Model: "cetirun-router-0123456789abcdef", Granted: true},
			{Name: "auto-copilot", Reason: "tier simple uses the GitHub Copilot model fast-model: sign in to GitHub Copilot with an account that offers it"},
		},
	}}}
	resp := aiGatewayResponse(agent)
	if !slices.Equal(resp.Models, []string{"small-model", "auto"}) || resp.DefaultModel != "auto" || len(resp.ModelRouters) != 2 ||
		!resp.ModelRouters[0].Granted || resp.ModelRouters[1].Granted || !strings.Contains(resp.ModelRouters[1].Reason, "sign in") {
		t.Fatalf("response = %+v", resp)
	}
	raw, _ := json.Marshal(resp)
	if strings.Contains(string(raw), "cetirun-router-") {
		t.Fatalf("the internal Gateway name stays out of the response: %s", raw)
	}
	if agent.Status.AIGateway.Models[1] != "cetirun-router-0123456789abcdef" {
		t.Fatal("the Agent's status must not be changed")
	}
}

func TestCreationOptionsOfferThePolicysModelRouters(t *testing.T) {
	team, policy := creationPolicy()
	team.Spec.ModelRouters = []cetirunv1alpha1.TeamModelRouter{{Name: "auto"}, {Name: "auto-unlisted"}}
	policy.Spec.ModelAccess.SharedModels.Allowed = append(policy.Spec.ModelAccess.SharedModels.Allowed, "auto")
	h, fc := creationPolicyHandler(team, policy)
	options := readCreateOptions(t, h, team.Name)
	if !slices.Equal(options.ModelRouters, []string{"auto"}) {
		t.Fatalf("model routers = %v", options.ModelRouters)
	}
	// A router the policy does not allow is refused as a default model before
	// any write; an allowed one passes the model check.
	w := httptest.NewRecorder()
	h.CreateInstance(w, managementRequest(http.MethodPost, `{"agent":{"type":"hermes"},"team":"policy-team","model":"auto-unlisted","gatewayToken":"synthetic-token"}`))
	if w.Code != http.StatusForbidden || errorCode(t, w) != "model_not_allowed" {
		t.Fatalf("unlisted router: %d %s", w.Code, w.Body.String())
	}
	assertNoCreationWrites(t, fc)
	w = httptest.NewRecorder()
	h.CreateInstance(w, managementRequest(http.MethodPost, `{"agent":{"type":"hermes"},"team":"policy-team","model":"auto","gatewayToken":"synthetic-token"}`))
	if w.Code == http.StatusForbidden && errorCode(t, w) == "model_not_allowed" {
		t.Fatalf("an allowed router must pass the model check: %s", w.Body.String())
	}
}
