// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

var credentialPolicyRevision = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var errAgentIdentityProvisioning = errors.New("Agent Identity provisioning intent was recorded")

// credentialPolicyRequest is the deliberately small, canonical control-plane
// contract. The instance identity is only in the path; accepting it in the
// body would let two sources disagree about the policy subject.
type credentialPolicyRequest struct {
	Revision string   `json:"revision"`
	Services []string `json:"services"`
}

type credentialPolicyResponse struct {
	InstanceUID string   `json:"instanceUid"`
	Revision    string   `json:"revision"`
	Services    []string `json:"services"`
}

func credentialPolicyForResolution(agent *api.Agent, resolution *policyResolution) (credentialPolicyRequest, error) {
	if agent == nil || resolution == nil || resolution.policy == nil {
		return credentialPolicyRequest{}, errors.New("credential policy resolution is unavailable")
	}
	if !credentialPolicyRevision.MatchString(resolution.credentialRevision) {
		return credentialPolicyRequest{}, errors.New("credential policy revision is invalid")
	}
	services := slices.Clone(resolution.policy.Spec.AgentIdentity.CredentialServices)
	// v1 has one typed executor. Keep this validation in the operator as well
	// as in the CRD so a stale or malformed object cannot widen a Broker grant.
	if len(services) != 1 || services[0] != "azure-resource-manager" {
		return credentialPolicyRequest{}, errors.New("credential policy services are unsupported")
	}
	return credentialPolicyRequest{Revision: resolution.credentialRevision, Services: services}, nil
}

func (r *AgentReconciler) credentialPolicyURL(instanceUID string) (*url.URL, error) {
	if !canonicalUUID.MatchString(instanceUID) {
		return nil, errors.New("instance UID is invalid")
	}
	base, err := url.Parse(r.AgentIdentityBrokerURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("Agent Identity Broker URL is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/operator/v1/instances/" + url.PathEscape(instanceUID) + "/credential-policy"
	return base, nil
}

func (r *AgentReconciler) credentialPolicyHTTPClient() *http.Client {
	if r.AgentIdentityHTTPClient != nil {
		return r.AgentIdentityHTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func decodeCredentialPolicyResponse(body io.Reader) (credentialPolicyResponse, error) {
	var response credentialPolicyResponse
	decoder := json.NewDecoder(io.LimitReader(body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return credentialPolicyResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return credentialPolicyResponse{}, errors.New("credential policy response has trailing JSON")
	}
	return response, nil
}

func credentialPolicyResponseMatches(response credentialPolicyResponse, instanceUID string, expected credentialPolicyRequest) bool {
	return response.InstanceUID == instanceUID && response.Revision == expected.Revision &&
		slices.Equal(response.Services, expected.Services)
}

func (r *AgentReconciler) putCredentialPolicy(ctx context.Context, instanceUID string, policy credentialPolicyRequest) error {
	endpoint, err := r.credentialPolicyURL(instanceUID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("encode credential policy: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create credential policy apply request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := r.credentialPolicyHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("apply credential policy: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("apply credential policy returned HTTP %d", response.StatusCode)
	}
	observed, err := decodeCredentialPolicyResponse(response.Body)
	if err != nil {
		return fmt.Errorf("decode credential policy apply response: %w", err)
	}
	if !credentialPolicyResponseMatches(observed, instanceUID, policy) {
		return errors.New("credential policy apply response does not match the requested policy")
	}
	return nil
}

func (r *AgentReconciler) getCredentialPolicy(ctx context.Context, instanceUID string, expected credentialPolicyRequest) error {
	endpoint, err := r.credentialPolicyURL(instanceUID)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("create credential policy read-back request: %w", err)
	}
	response, err := r.credentialPolicyHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("read credential policy: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("read credential policy returned HTTP %d", response.StatusCode)
	}
	observed, err := decodeCredentialPolicyResponse(response.Body)
	if err != nil {
		return fmt.Errorf("decode credential policy read-back response: %w", err)
	}
	if !credentialPolicyResponseMatches(observed, instanceUID, expected) {
		return errors.New("credential policy read-back does not match the requested policy")
	}
	return nil
}

func (r *AgentReconciler) deleteCredentialPolicy(ctx context.Context, instanceUID string) error {
	endpoint, err := r.credentialPolicyURL(instanceUID)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("create credential policy cleanup request: %w", err)
	}
	response, err := r.credentialPolicyHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("delete credential policy: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("delete credential policy returned HTTP %d", response.StatusCode)
	}
	return nil
}

func clearCredentialPolicyEvidence(agent *api.Agent) bool {
	if agent == nil || agent.Status.AgentIdentity == nil || agent.Status.AgentIdentity.CredentialPolicyRevision == "" {
		return false
	}
	agent.Status.AgentIdentity.CredentialPolicyRevision = ""
	return true
}

// revokeCredentialPolicyIfProjected avoids contacting the Broker for a new or
// unsupported Agent which has never published read-back evidence. With no
// applied revision in status, the Broker issuance barrier is already closed.
func (r *AgentReconciler) revokeCredentialPolicyIfProjected(ctx context.Context, agent *api.Agent) error {
	if agent == nil || agent.Status.AgentIdentity == nil || agent.Status.AgentIdentity.CredentialPolicyRevision == "" {
		return nil
	}
	return r.revokeCredentialPolicy(ctx, agent)
}

func (r *AgentReconciler) clearCredentialPolicyEvidenceStatus(ctx context.Context, agent *api.Agent) error {
	if !clearCredentialPolicyEvidence(agent) {
		return nil
	}
	return r.updateStatus(ctx, agent)
}

// revokeCredentialPolicy first removes local evidence before contacting the
// Broker. If either the Broker or status API is unavailable, issuing a new
// credential remains blocked and the caller can stop compute independently.
func (r *AgentReconciler) revokeCredentialPolicy(ctx context.Context, agent *api.Agent) error {
	if agent == nil || agent.UID == "" {
		return errors.New("Agent UID is unavailable for credential policy cleanup")
	}
	var statusErr error
	if clearCredentialPolicyEvidence(agent) {
		statusErr = r.updateStatus(ctx, agent)
	}
	deleteErr := r.deleteCredentialPolicy(ctx, string(agent.UID))
	return errors.Join(statusErr, deleteErr)
}

// reconcileCredentialPolicy applies and immediately reads back the typed
// Broker policy. Status evidence is written only after that exact read-back;
// a previously applied revision is cleared before an apply attempt so it can
// never attest to a replacement policy after an error.
func (r *AgentReconciler) reconcileCredentialPolicy(ctx context.Context, agent *api.Agent, resolution *policyResolution) error {
	if !agentIdentityRequested(agent) {
		return r.revokeCredentialPolicy(ctx, agent)
	}
	status := agent.Status.AgentIdentity
	if status == nil || status.Phase != api.AgentIdentityPhaseReady || status.ObservedGeneration != agent.Generation {
		err := r.revokeCredentialPolicy(ctx, agent)
		return errors.Join(errors.New("Agent Identity is not ready for credential policy application"), err)
	}
	policy, err := credentialPolicyForResolution(agent, resolution)
	if err != nil {
		return errors.Join(err, r.revokeCredentialPolicy(ctx, agent))
	}
	if err := r.revokeCredentialPolicy(ctx, agent); err != nil {
		return err
	}
	if err := r.putCredentialPolicy(ctx, string(agent.UID), policy); err != nil {
		return err
	}
	if err := r.getCredentialPolicy(ctx, string(agent.UID), policy); err != nil {
		return err
	}
	status = ensureAgentIdentityStatus(agent)
	status.CredentialPolicyRevision = policy.Revision
	if err := r.updateStatus(ctx, agent); err != nil {
		return fmt.Errorf("persist credential policy read-back evidence: %w", err)
	}
	return nil
}

// reconcileAgentIdentityPolicyProjection records identity creation before
// contacting Graph, persists a current identity observation, then applies the
// typed policy. It deliberately runs before the native Sandbox hold path: a
// held workload must not create a circular dependency which prevents its own
// Agent Identity policy from ever becoming ready.
func (r *AgentReconciler) reconcileAgentIdentityPolicyProjection(ctx context.Context, agent *api.Agent, resolution *policyResolution) error {
	if agent == nil || (agent.Spec.AgentIdentity == nil && agent.Status.AgentIdentity == nil) {
		return nil
	}
	// A suspended Agent must carry no executable Broker policy. Revoke before
	// Graph verification or provisioning so suspension converges without asking
	// the Broker to accept a policy which its own issuance boundary rejects.
	if agent.Spec.Suspended {
		return r.revokeCredentialPolicyIfProjected(ctx, agent)
	}
	if agentIdentityRequested(agent) && (agent.Status.AgentIdentity == nil || agent.Status.AgentIdentity.ObservedGeneration != agent.Generation) {
		status := ensureAgentIdentityStatus(agent)
		status.Phase = api.AgentIdentityPhaseProvisioning
		status.ObservedGeneration = agent.Generation
		status.Reason = ""
		status.CredentialPolicyRevision = ""
		setCondition(&agent.Status.Conditions, api.ConditionAgentIdentityReady,
			metav1.ConditionFalse, "Provisioning", "Agent Identity provisioning intent is recorded")
		if err := r.updateStatus(ctx, agent); err != nil {
			return fmt.Errorf("persist Agent Identity provisioning intent: %w", err)
		}
		return errAgentIdentityProvisioning
	}
	if agentIdentityRequested(agent) && agent.Status.AgentIdentity != nil &&
		agent.Status.AgentIdentity.Phase == api.AgentIdentityPhaseReady &&
		agent.Status.AgentIdentity.CredentialPolicyRevision != "" && resolution != nil &&
		agent.Status.AgentIdentity.CredentialPolicyRevision == resolution.credentialRevision &&
		!agentIdentityVerificationDue(agent, time.Now()) {
		expected, err := credentialPolicyForResolution(agent, resolution)
		if err != nil {
			return errors.Join(err, r.clearCredentialPolicyEvidenceStatus(ctx, agent))
		}
		if err := r.getCredentialPolicy(ctx, string(agent.UID), expected); err != nil {
			// Do not blindly delete on a read-back failure: it might conceal a
			// transient Broker read outage. Clearing status evidence is sufficient
			// to fence issuance and force the normal apply path on the retry.
			return errors.Join(err, r.clearCredentialPolicyEvidenceStatus(ctx, agent))
		}
		return nil
	}
	if err := r.reconcileAgentIdentity(ctx, agent); err != nil {
		// Persist a cleared evidence record even when Graph verification was the
		// first operation to fail. The caller fences compute separately.
		clearErr := r.revokeCredentialPolicy(ctx, agent)
		return errors.Join(err, clearErr)
	}
	if !agentIdentityRequested(agent) {
		return nil
	}
	if agent.Status.AgentIdentity == nil || agent.Status.AgentIdentity.Phase != api.AgentIdentityPhaseReady {
		return r.revokeCredentialPolicy(ctx, agent)
	}
	now := metav1.Now()
	agent.Status.AgentIdentity.LastVerifiedTime = &now
	if err := r.updateStatus(ctx, agent); err != nil {
		return fmt.Errorf("persist Agent Identity verification: %w", err)
	}
	return r.reconcileCredentialPolicy(ctx, agent, resolution)
}
