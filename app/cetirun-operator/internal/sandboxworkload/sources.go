// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (s SourceReference) IsAgent() bool {
	return s.APIVersion == api.GroupVersion.String() && s.Kind == "Agent" && s.Namespace == ""
}

func (s SourceReference) IsClusterRole() bool {
	return s.APIVersion == "rbac.authorization.k8s.io/v1" && s.Kind == "ClusterRole" && s.Namespace == ""
}

func (s SourceReference) ValidIdentity() bool {
	if s.Name == "" || s.UID == "" {
		return false
	}
	if s.ImmutableSecret {
		return s.APIVersion == "v1" && s.Kind == "Secret" && s.Namespace != "" &&
			s.ResourceVersion == "" && s.Generation == 0 && s.IntentDigest == ""
	}
	if !s.IsAgent() {
		return s.ResourceVersion != "" && s.Generation == 0 && s.IntentDigest == ""
	}
	raw, err := hex.DecodeString(s.IntentDigest)
	return s.ResourceVersion == "" && s.Generation > 0 && err == nil && len(raw) == sha256.Size &&
		hex.EncodeToString(raw) == s.IntentDigest
}

// ImmutableSecretReference pins a persisted immutable Secret by UID only.
// Metadata changes then keep the pin; replacing the Secret changes its UID.
// The caller reads the Secret only to check immutability, never its data.
func ImmutableSecretReference(secret *corev1.Secret) (SourceReference, error) {
	if !ImmutableSecretCurrent(secret) {
		return SourceReference{}, fmt.Errorf("a UID-pinned Secret must be persisted, not terminating and immutable")
	}
	return SourceReference{APIVersion: "v1", Kind: "Secret", Namespace: secret.Namespace, Name: secret.Name,
		UID: secret.UID, ImmutableSecret: true}, nil
}

// ImmutableSecretCurrent reports whether a live Secret can satisfy a UID pin.
func ImmutableSecretCurrent(secret *corev1.Secret) bool {
	return secret != nil && secret.UID != "" && secret.ResourceVersion != "" && secret.DeletionTimestamp.IsZero() &&
		secret.Immutable != nil && *secret.Immutable
}

// ImmutableSecretMatches reports whether a live Secret satisfies a UID-pinned source.
func ImmutableSecretMatches(source SourceReference, secret *corev1.Secret) bool {
	return source.ImmutableSecret && source.ValidIdentity() && ImmutableSecretCurrent(secret) &&
		secret.Namespace == source.Namespace && secret.Name == source.Name && secret.UID == source.UID
}

// MatchesApproval reports whether a recorded source satisfies an approved
// object identity. An approval without a version requires a UID-pinned
// immutable Secret source, or any version of another kind.
func (s SourceReference) MatchesApproval(namespace, name string, uid types.UID, resourceVersion string) bool {
	if s.Namespace != namespace || s.Name != name || s.UID != uid || uid == "" {
		return false
	}
	if resourceVersion != "" {
		return !s.ImmutableSecret && s.ResourceVersion == resourceVersion
	}
	return s.Kind != "Secret" || s.ImmutableSecret
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
	digest, err := agent.SandboxExecutionIntentDigest()
	if err != nil {
		return SourceReference{}, err
	}
	return SourceReference{
		APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: agent.Name, UID: agent.UID,
		Generation: agent.Generation, IntentDigest: digest,
	}, nil
}
