// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package v1alpha1

// ConditionTeamModelRoutersReady reports whether every smart-routing model a
// Team defines is registered in the AI Gateway. It is False with a message
// naming each router that is not, and why.
const ConditionTeamModelRoutersReady = "ModelRoutersReady"

// Classifier types a Team model router offers. LiteLLM's other classifier
// types, custom tier sets and custom classifier prompts are limited to one
// router per Gateway without a LiteLLM licence, so a Team cannot use them.
const (
	ModelRouterClassifierHeuristic = "heuristic"
	ModelRouterClassifierLLM       = "llm"
)

// TeamModelRouter is one smart-routing model of a Team: a model name Agents
// request, behind which each request goes to the model of the complexity tier
// its classifier picks. Administrators define routers; an Agent receives a
// router only when everything the router uses is available to it.
type TeamModelRouter struct {
	// Name is the model name Agents request, such as auto or auto-claude.
	// It has no whitespace or slash and must not start with the reserved
	// prefix cetirun- (in any letter case).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[^\s/]+$`
	// +kubebuilder:validation:XValidation:rule="!self.lowerAscii().startsWith('cetirun-')",message="a model router name must not start with the reserved prefix cetirun-"
	Name string `json:"name"`

	// Classifier decides the complexity tier of each request.
	// +kubebuilder:default={type: heuristic}
	// +optional
	Classifier TeamModelRouterClassifier `json:"classifier,omitempty"`

	// Tiers names the model each complexity tier sends requests to.
	Tiers TeamModelRouterTiers `json:"tiers"`
}

// TeamModelRouterClassifier selects how a router classifies requests.
// +kubebuilder:validation:XValidation:rule="(has(self.type) && self.type == 'llm') == has(self.model)",message="classifier.model is required for the llm classifier and not allowed for the heuristic classifier"
type TeamModelRouterClassifier struct {
	// Type is heuristic, which scores each request locally without a model
	// call, or llm, which asks a shared model to classify each request.
	// +kubebuilder:validation:Enum=heuristic;llm
	// +kubebuilder:default=heuristic
	// +optional
	Type string `json:"type,omitempty"`

	// Model is the shared model that classifies requests for the llm
	// classifier. It must be a shared model of the AI Gateway, not a GitHub
	// Copilot model or another router. Its calls are charged to the Agent.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^[^\s]+$`
	// +optional
	Model string `json:"model,omitempty"`
}

// TeamModelRouterTiers names the model of each complexity tier: a shared
// model name, or copilot/<model> for the GitHub Copilot model of that ID,
// which each Agent reaches through its owner's own Copilot sign-in.
type TeamModelRouterTiers struct {
	// Simple serves the simplest requests.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^[^\s]+$`
	Simple string `json:"simple"`

	// Medium serves ordinary requests. It is also the model the router uses
	// when a request cannot be classified.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^[^\s]+$`
	Medium string `json:"medium"`

	// Complex serves complex requests.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^[^\s]+$`
	Complex string `json:"complex"`

	// Reasoning serves requests that ask for step-by-step reasoning. Empty
	// uses the complex tier's model.
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^[^\s]+$`
	// +optional
	Reasoning string `json:"reasoning,omitempty"`
}

// ReasoningModel is the model of the reasoning tier, which defaults to the
// complex tier's model.
func (t TeamModelRouterTiers) ReasoningModel() string {
	if t.Reasoning != "" {
		return t.Reasoning
	}
	return t.Complex
}

// Models lists the models of the four tiers in tier order, duplicates
// included.
func (t TeamModelRouterTiers) Models() []string {
	return []string{t.Simple, t.Medium, t.Complex, t.ReasoningModel()}
}

// TeamModelRouterStatus is the AI Gateway registration of one router.
type TeamModelRouterStatus struct {
	// Name is the router's name in spec.modelRouters.
	Name string `json:"name"`

	// GatewayModel is the internal Gateway model name of the router, which
	// Agents granted the router send their requests to.
	// +optional
	GatewayModel string `json:"gatewayModel,omitempty"`

	// Ready reports whether the router is registered in the Gateway with its
	// current configuration.
	Ready bool `json:"ready"`

	// Message explains why the router is not ready.
	// +optional
	Message string `json:"message,omitempty"`
}

// AIGatewayModelRouterStatus reports one smart-routing model of the Agent's
// Team for this Agent: whether the Agent's key was granted it, and if not why.
type AIGatewayModelRouterStatus struct {
	// Name is the router's name, the model name the owner chooses.
	Name string `json:"name"`

	// Model is the Gateway model name the Agent sends requests for the
	// router to. Set only when the router is granted.
	// +optional
	Model string `json:"model,omitempty"`

	// Granted reports whether the Agent's key may use the router.
	Granted bool `json:"granted"`

	// Reason explains why the router is not granted.
	// +optional
	Reason string `json:"reason,omitempty"`

	// ContextWindow and MaxTokens are the smallest limits among the router's
	// tier models that the Gateway reports, which the Agent is configured with.
	// +optional
	ContextWindow int64 `json:"contextWindow,omitempty"`
	// +optional
	MaxTokens int64 `json:"maxTokens,omitempty"`
}
