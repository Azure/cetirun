// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

var credentialPolicyRevisionPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func canonicalCredentialPolicy(policy CredentialPolicy) ([]byte, error) {
	if !credentialPolicyRevisionPattern.MatchString(policy.Revision) || len(policy.Services) != 1 || policy.Services[0] != "azure-resource-manager" {
		return nil, errors.New("credential policy is invalid")
	}
	return json.Marshal(policy)
}

// parseCredentialPolicy deliberately accepts only the one canonical JSON form.
// This makes the value which the Operator reads back byte-for-byte identical to
// the value persisted in Kubernetes and rejects duplicate keys, aliases, and
// unrecognised future fields until they have an explicit policy contract.
func parseCredentialPolicy(raw []byte) (CredentialPolicy, error) {
	var policy CredentialPolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return CredentialPolicy{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return CredentialPolicy{}, errors.New("credential policy has trailing JSON")
	}
	canonical, err := canonicalCredentialPolicy(policy)
	if err != nil || !bytes.Equal(raw, canonical) {
		return CredentialPolicy{}, errors.New("credential policy is not canonical")
	}
	return policy, nil
}

func credentialPolicyControlAllows(agent Agent, policy CredentialPolicy) bool {
	return !agent.Deleting && !agent.Suspended && agent.AgentIdentityEnabled && agent.AgentIdentityBound &&
		agent.AgentIdentityObservedGeneration == agent.Generation &&
		agent.PolicyObservedGeneration == agent.Generation &&
		(agent.PolicyPhase == "Pending" || agent.PolicyPhase == "Applied") &&
		agent.DesiredCredentialPolicyRevision != "" &&
		policy.Revision == agent.DesiredCredentialPolicyRevision
}

func (s *Server) credentialPolicyBarrier(ctx context.Context, agent Agent, service string) (Agent, error) {
	current, err := s.store.getAgent(ctx, agent.Name)
	if err != nil || current.UID != agent.UID || !current.AgentIdentityReady || current.Deleting || current.Suspended ||
		!current.AgentIdentityEnabled || !current.AgentIdentityBound ||
		current.AgentIdentityObservedGeneration != current.Generation ||
		current.PolicyObservedGeneration != current.Generation || current.PolicyPhase != "Applied" ||
		!sameIdentityID(current.AgentIdentityClientID, agent.AgentIdentityClientID) ||
		!credentialPolicyRevisionPattern.MatchString(current.DesiredCredentialPolicyRevision) ||
		current.CredentialPolicyRevision != current.DesiredCredentialPolicyRevision {
		return Agent{}, errors.New("credential policy is not applied")
	}
	policy, _, err := s.store.getCredentialPolicy(ctx, current)
	if err != nil || policy.Revision != current.DesiredCredentialPolicyRevision || !credentialPolicyAllows(policy, service) {
		return Agent{}, errors.New("credential policy is not applied")
	}
	return current, nil
}

func credentialPolicyAllows(policy CredentialPolicy, service string) bool {
	return len(policy.Services) == 1 && policy.Services[0] == service && service == "azure-resource-manager"
}
