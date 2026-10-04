// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
)

func (s SourceReference) IsAgent() bool {
	return s.APIVersion == api.GroupVersion.String() && s.Kind == "Agent" && s.Namespace == ""
}

func (s SourceReference) ValidIdentity() bool {
	if s.Name == "" || s.UID == "" {
		return false
	}
	if !s.IsAgent() {
		return s.ResourceVersion != "" && s.Generation == 0 && s.IntentDigest == ""
	}
	raw, err := hex.DecodeString(s.IntentDigest)
	return s.ResourceVersion == "" && s.Generation > 0 && err == nil && len(raw) == sha256.Size &&
		hex.EncodeToString(raw) == s.IntentDigest
}

// AgentSourceReference excludes status and unrelated metadata, but includes
// explicit reauthorization and lifecycle holds even when generation is unchanged.
func AgentSourceReference(agent *api.Agent) (SourceReference, error) {
	if _, err := sandboxbinding.AuthorizedBinding(agent); err != nil {
		return SourceReference{}, err
	}
	if agent.Generation < 1 || agent.ResourceVersion == "" {
		return SourceReference{}, fmt.Errorf("Sandbox Agent intent requires a persisted generation and resource version")
	}
	annotations := make(map[string]string, 2)
	for _, key := range []string{sandboxbinding.AuthorizationAnnotation, sandboxbinding.ReauthorizeAnnotation} {
		if value, found := agent.Annotations[key]; found {
			annotations[key] = value
		}
	}
	intent := struct {
		Spec         api.AgentSpec
		Annotations  map[string]string
		Held         bool
		Prepared     bool
		Bootstrapped bool `json:",omitempty"`
	}{
		Spec: agent.Spec, Annotations: annotations,
		Held:         slices.Contains(agent.Finalizers, sandbox.HoldFinalizer),
		Prepared:     slices.Contains(agent.Finalizers, sandbox.PreparationFinalizer),
		Bootstrapped: slices.Contains(agent.Finalizers, sandbox.BootstrapFinalizer),
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return SourceReference{}, fmt.Errorf("encode Sandbox Agent intent: %w", err)
	}
	digest := sha256.Sum256(raw)
	return SourceReference{
		APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: agent.Name, UID: agent.UID,
		Generation: agent.Generation, IntentDigest: hex.EncodeToString(digest[:]),
	}, nil
}
