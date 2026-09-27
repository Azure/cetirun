// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package runtimebindings defines receipts from platform capability owners.
// These are internal inputs, not fields accepted from an Agent or Helm values.
package runtimebindings

import (
	"fmt"
	"path"
	"strings"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/agents"
	"k8s.io/apimachinery/pkg/util/validation"
)

type ObjectReference struct {
	Name     string `json:"name"`
	UID      string `json:"uid"`
	Revision string `json:"revision"`
}

type ModelAccess struct {
	ObjectReference
	BaseURL string `json:"baseURL"`
	Key     string `json:"key"`
}

type Home struct {
	ObjectReference
	FSGroup int64 `json:"fsGroup"`
}

// Context is an immutable ConfigMap snapshot. Files maps keys to paths under
// HOME/.agents. Its owner supplies approved content, not executable init hooks.
type Context struct {
	ObjectReference
	Files map[string]string `json:"files"`
}

type Peer struct {
	Namespace string            `json:"namespace"`
	PodLabels map[string]string `json:"podLabels"`
}

type Network struct {
	Revision    string `json:"revision"`
	Callers     []Peer `json:"callers"`
	Gateway     Peer   `json:"gateway"`
	GatewayPort int32  `json:"gatewayPort"`
	DNS         Peer   `json:"dns"`
}

// Custom is returned only after policy and capability owners approve these
// exact objects for the requested Agent UID, generation, type and image.
type Custom struct {
	AgentName      string          `json:"agentName"`
	AgentUID       string          `json:"agentUID"`
	Generation     int64           `json:"generation"`
	Namespace      ObjectReference `json:"namespace"`
	ApprovedType   string          `json:"approvedType"`
	ApprovedImage  string          `json:"approvedImage"`
	PolicyRevision string          `json:"policyRevision"`
	Model          ModelAccess     `json:"model"`
	Home           Home            `json:"home"`
	Context        Context         `json:"context"`
	Network        Network         `json:"network"`
}

func (b Custom) HarnessBindings() agents.Bindings {
	return agents.Bindings{
		Model:           agents.ModelBinding{BaseURL: b.Model.BaseURL, CredentialSlot: "model", Revision: b.Model.Revision},
		ContextRevision: b.Context.Revision,
	}
}

func validReference(ref ObjectReference) bool {
	return len(validation.IsDNS1123Subdomain(ref.Name)) == 0 && ref.UID != "" && ref.Revision != ""
}

func validPeer(peer Peer) bool {
	if len(validation.IsDNS1123Label(peer.Namespace)) != 0 || len(peer.PodLabels) == 0 {
		return false
	}
	for key, value := range peer.PodLabels {
		if len(validation.IsQualifiedName(key)) != 0 || len(validation.IsValidLabelValue(value)) != 0 {
			return false
		}
	}
	return true
}

func (b Custom) Validate(agent agents.ResolvedAgent) error {
	if len(validation.IsDNS1123Subdomain(b.AgentName)) != 0 || b.AgentUID == "" || b.Generation < 1 || !validReference(b.Namespace) ||
		b.ApprovedType != "custom" || b.ApprovedImage != agent.Image || b.PolicyRevision == "" {
		return fmt.Errorf("PolicyBindingPending: exact Agent and image approval is required")
	}
	if !validReference(b.Model.ObjectReference) || len(validation.IsConfigMapKey(b.Model.Key)) != 0 {
		return fmt.Errorf("ModelBindingPending: versioned instance Secret reference is required")
	}
	if !validReference(b.Home.ObjectReference) || b.Home.FSGroup < 1 || b.Home.FSGroup > 2147483647 {
		return fmt.Errorf("HomeBindingPending: approved persistent HOME and volume permissions are required")
	}
	if !validReference(b.Context.ObjectReference) || len(b.Context.Files) > 256 {
		return fmt.Errorf("ContextBindingPending: approved immutable context is required")
	}
	seen := map[string]bool{}
	for key, file := range b.Context.Files {
		if len(validation.IsConfigMapKey(key)) != 0 || file == "" || len(file) > 512 ||
			path.Clean(file) != file || strings.ContainsAny(file, "\\\x00\r\n") ||
			(file != "AGENTS.md" && !strings.HasPrefix(file, "skills/")) ||
			seen[file] || strings.HasSuffix(file, "/") {
			return fmt.Errorf("ContextBindingInvalid: invalid or duplicate context path")
		}
		seen[file] = true
	}
	for file := range seen {
		for parent := path.Dir(file); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return fmt.Errorf("ContextBindingInvalid: file shadows a context directory")
			}
		}
	}
	if !seen["AGENTS.md"] || !seen["skills/cetirun-memory/SKILL.md"] {
		return fmt.Errorf("ContextBindingPending: canonical AGENTS.md and cetirun-memory are required")
	}
	if b.Network.Revision == "" || !validPeer(b.Network.Gateway) || !validPeer(b.Network.DNS) ||
		b.Network.GatewayPort < 1 || b.Network.GatewayPort > 65535 {
		return fmt.Errorf("NetworkBindingPending: approved Gateway and DNS peers are required")
	}
	for _, peer := range b.Network.Callers {
		if !validPeer(peer) {
			return fmt.Errorf("NetworkBindingInvalid: callers require a namespace and pod selector")
		}
	}
	return agents.ValidatePortableBindings(b.HarnessBindings())
}

// ValidateOverrides deliberately keeps the Custom override surface small.
// Bindings, identity, routing, hooks and volumes never come from Helm overrides.
func ValidateOverrides(values map[string]any) error {
	for key := range values {
		if _, supported := customPlatformKeys[key]; !supported {
			return fmt.Errorf("UnsupportedFeature: helm.values.%s is not supported for Custom", key)
		}
		switch key {
		case "resources":
			if values[key] == nil {
				continue
			}
			resources, ok := values[key].(map[string]any)
			if !ok {
				return fmt.Errorf("invalid Custom resources")
			}
			for field := range resources {
				if field != "requests" && field != "limits" {
					return fmt.Errorf("UnsupportedFeature: Custom resources accepts only requests and limits")
				}
			}
		}
	}
	return ValidateScheduling(values)
}

// The value states whether a supported setting changes the Pod's inputs.
var customPlatformKeys = map[string]bool{
	"resources": true, "runtimeClassName": true, "nodeSelector": true, "affinity": true,
	"tolerations": true, "topologySpreadConstraints": true, "desktop": true, "podAnnotations": true, "global": true,
	"networkPolicy": false, "sidecarScope": false, "execRestriction": false,
	"podDisruptionBudget": false, "autoscaling": false, "ttyd": false,
}

// PlatformValues selects supported shared settings for the Custom plan hash.
// Native configuration and credentials remain outside the Custom runtime.
func PlatformValues(values map[string]any) map[string]any {
	selected := map[string]any{}
	for key := range customPlatformKeys {
		// ttyd is not consumed by Custom until its integration is revisited.
		if key == "ttyd" {
			continue
		}
		if value, ok := values[key]; ok {
			selected[key] = value
		}
	}
	return selected
}

func WorkloadValues(values map[string]any) map[string]any {
	selected := map[string]any{}
	for key, workload := range customPlatformKeys {
		if workload {
			if value, ok := values[key]; ok {
				selected[key] = value
			}
		}
	}
	desktop, _ := values["desktop"].(map[string]any)
	if desktop["enabled"] != true {
		delete(selected, "desktop")
		delete(selected, "global")
	}
	return selected
}
