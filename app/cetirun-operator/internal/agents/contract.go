// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package agents describes native Agent configuration without deployment
// clients, credentials, or ownership of the Agent's execution loop.
package agents

type Harness interface {
	Describe() Descriptor
	Validate(ResolvedAgent, Bindings) error
	Build(ResolvedAgent, Bindings) (Contribution, error)
}

type Descriptor struct {
	Type             string
	ConfigFile       string
	PortName         string
	DefaultPort      int32
	HealthPath       string
	ReadinessPath    string
	StartupPath      string
	DashboardPort    int32
	PreserveUIPrefix bool
	NativeSecretKey  string
	EnvironmentFile  string
}

type Request struct {
	Type     string `json:"type"`
	Image    string `json:"image,omitempty"`
	Username string `json:"username,omitempty"`
	Port     *int32 `json:"port,omitempty"`
}

type Profile struct {
	// Profiles belong to the selected chart, not to a mutable global catalog.
	ID              string
	Image           string
	Port            int32
	QualifiedImages []string
}

type ResolvedAgent struct {
	Type     string `json:"type"`
	Image    string `json:"image"`
	Username string `json:"username"`
	HomeDir  string `json:"home"`
	Port     *int32 `json:"port,omitempty"`
}

// Bindings contains only non-sensitive, platform-selected inputs. ConfigTemplate
// is an asset in the selected chart, never a user-supplied Helm value.
type Bindings struct {
	ProfileID         string
	ChartVersion      string
	NativeConfig      map[string]any
	NativeOptions     map[string]any
	Model             ModelBinding
	Skills            SkillsBinding
	Integrations      Integrations
	ConfigTemplate    string
	ReservedPorts     []PortBinding
	ContextRevision   string
	AgentManagedModel bool
}

type ModelBinding struct {
	Provider       string
	Name           string
	BaseURL        string
	AuthMode       string
	CredentialSlot string
	Revision       string
}

type SkillsBinding struct {
	Shared    bool
	MountPath string
	NPM       []string
}

// Presence flags enable native integrations without passing their credentials
// to a renderer or including those credentials in a ConfigMap.
type Integrations struct {
	ExistingSecret    bool
	Brave             bool
	Perplexity        bool
	Telegram          bool
	TelegramTokenFile string
	Slack             bool
	Discord           bool
	Feishu            bool
	TeamsApp          bool
	TeamsTenant       bool
	TeamsPassword     bool
	TeamsPort         int32
	TeamsAuthType     string
	Tailscale         bool
}

type Contribution struct {
	Agent       ResolvedAgent      `json:"agent"`
	ProfileID   string             `json:"profileID"`
	ConfigFiles map[string]string  `json:"configFiles"`
	Ports       []PortBinding      `json:"ports"`
	HTTP        *HTTPBinding       `json:"http,omitempty"`
	Environment []EnvironmentValue `json:"environment,omitempty"`
}

// CredentialSlot is resolved by the platform, never an adapter-selected Secret.
type EnvironmentValue struct {
	Name           string `json:"name"`
	Value          string `json:"value,omitempty"`
	CredentialSlot string `json:"credentialSlot,omitempty"`
}

type PortBinding struct {
	Name   string `json:"name"`
	Number int32  `json:"number"`
}

type HTTPBinding struct {
	PortName      string `json:"portName"`
	HealthPath    string `json:"healthPath"`
	ReadinessPath string `json:"readinessPath,omitempty"`
	StartupPath   string `json:"startupPath"`
	ChatPath      string `json:"chatPath,omitempty"`
	ModelsPath    string `json:"modelsPath,omitempty"`
}
