// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/copilot"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

// The GitHub Copilot sign-in runs in the Portal. The Portal has more than one
// replica, so a sign-in in progress lives in a short-lived Secret in the
// Agent's namespace rather than in memory: any replica can poll it, and the
// device code never reaches the browser.
const (
	copilotLoginSecretName = "github-copilot-login"

	loginState           = "state"
	loginDeviceCode      = "device-code"
	loginUserCode        = "user-code"
	loginVerificationURI = "verification-uri"
	loginExpiresAt       = "expires-at"
	loginInterval        = "interval"
	loginNextPollAt      = "next-poll-at"
	loginMessage         = "message"
	loginCompletingUntil = "completing-until"

	// Sign-in states reported to the browser.
	copilotLoginNone      = "none"
	copilotLoginPending   = "pending"
	copilotLoginSucceeded = "succeeded"
	copilotLoginExpired   = "expired"
	copilotLoginDenied    = "denied"
	copilotLoginFailed    = "failed"
	// copilotLoginCompleting marks a sign-in whose token is being stored. It
	// is committed before anything is written, so a cancel or a new start
	// that came first makes the commit conflict and the token is dropped;
	// once committed, it holds off both until the outcome is recorded. The
	// browser sees it as pending.
	copilotLoginCompleting = "completing"

	// copilotLoginLease bounds how long a sign-in being completed holds off
	// a new start or a cancel. Storing the token takes a few API calls; the
	// lease only matters if the replica completing it stops on the way.
	copilotLoginLease = 2 * time.Minute

	// copilotSignInAnnotation on the token Secret fences completions, since
	// a lease cannot: a replica that stalls past it may still resume. A
	// completion reads the token Secret before it commits and stores the
	// token under that version. Once a start, a cancel, or the end of a
	// completion whose lease ran out has been accepted, it gives this
	// annotation a new value, so a completion it replaced can no longer
	// store its token.
	copilotSignInAnnotation = "cetirun.microsoft.com/copilot-sign-in"
)

// errCopilotLoginCompleting reports a sign-in that is storing its token.
var errCopilotLoginCompleting = errors.New("the GitHub Copilot sign-in is being completed")

// errCopilotLoginChanged reports a sign-in Secret that kept changing while a
// start or a cancel tried to replace it.
var errCopilotLoginChanged = errors.New("the GitHub Copilot sign-in changed meanwhile")

// errCopilotLoginSuperseded reports a completion whose token a newer sign-in
// replaced before the completion recorded its models.
var errCopilotLoginSuperseded = errors.New("a newer GitHub Copilot sign-in replaced this one")

// CopilotLoginResponse reports a GitHub Copilot sign-in. It carries the code
// the user enters at GitHub, never the device code or a token.
type CopilotLoginResponse struct {
	State           string `json:"state"`
	UserCode        string `json:"userCode,omitempty"`
	VerificationURI string `json:"verificationUri,omitempty"`
	ExpiresAt       string `json:"expiresAt,omitempty"`
	Interval        int    `json:"interval,omitempty"`
	Message         string `json:"message,omitempty"`
}

var errCopilotProxyNotReady = errors.New("the agent's GitHub Copilot proxy is not set up yet")

func (h *InstanceHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *InstanceHandler) copilotClient() *copilot.Client {
	if h.Copilot != nil {
		return h.Copilot
	}
	return &copilot.Client{}
}

// copilotAgent returns the caller's Agent when it runs a GitHub Copilot proxy
// through the AI Gateway, or writes the error response. A request that
// changes the sign-in names the Agent it was made for with expectedUID, so a
// page left open while the name was deleted and created again cannot change
// the new Agent's sign-in; reads pass nil.
func (h *InstanceHandler) copilotAgent(w http.ResponseWriter, r *http.Request, expectedUID *string) (*cetirunv1alpha1.Agent, bool) {
	if !h.Config.AIGatewayEnabled {
		writeError(w, http.StatusNotFound, "feature_disabled", "The AI Gateway is not enabled for this deployment")
		return nil, false
	}
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return nil, false
	}
	instance, err := h.Kube.GetInstanceByName(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Agent not found")
		return nil, false
	}
	// The same ownership rule as the other instance endpoints: an Agent bound
	// to a verified owner accepts only that tenant and object ID, whatever
	// alias another principal shares with it.
	if !ownsInstance(instance, claims) {
		writeError(w, http.StatusForbidden, "forbidden", "forbidden")
		return nil, false
	}
	if expectedUID != nil {
		if *expectedUID == "" {
			writeError(w, http.StatusPreconditionRequired, "version_required", "uid from the current Agent is required")
			return nil, false
		}
		if *expectedUID != string(instance.UID) {
			writeManagementError(w, kube.ErrInstanceChanged)
			return nil, false
		}
	}
	if instance.DeletionTimestamp != nil {
		writeError(w, http.StatusConflict, "agent_deleting", "The agent is being deleted")
		return nil, false
	}
	if !aiGatewayRouted(instance) || instance.Status.AIGateway.Copilot == nil {
		writeError(w, http.StatusConflict, "copilot_not_enabled", "GitHub Copilot is not enabled for this agent")
		return nil, false
	}
	return instance, true
}

// copilotLoginRequest is the JSON body that starts or cancels a sign-in.
type copilotLoginRequest struct {
	UID string `json:"uid"`
}

// copilotLoginUID reads the Agent UID a start or cancel request was made for,
// with the origin check of the other management requests, or writes the
// error response.
func (h *InstanceHandler) copilotLoginUID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !h.validManagementOrigin(w, r) {
		return "", false
	}
	var req copilotLoginRequest
	if _, ok := decodeManagementRequest(w, r, &req); !ok {
		return "", false
	}
	return req.UID, true
}

// copilotTokenSecret returns the Secret the operator created for the Agent's
// Copilot token. A Secret labelled for another Agent UID is never written.
func (h *InstanceHandler) copilotTokenSecret(ctx context.Context, instance *cetirunv1alpha1.Agent) (*corev1.Secret, error) {
	secret, err := h.Kube.GetSecret(ctx, kube.InstanceNamespace(instance.Name), cetirunv1alpha1.CopilotTokenSecretName)
	if kube.IsNotFound(err) {
		return nil, errCopilotProxyNotReady
	}
	if err != nil {
		return nil, err
	}
	if secret.Labels[kube.AgentUIDLabel] != string(instance.UID) {
		return nil, errCopilotProxyNotReady
	}
	return secret, nil
}

// fenceCopilotCompletions gives the token Secret a new sign-in marker, so a
// completion that read the Secret earlier can no longer store its token. An
// Agent without its token Secret has no completion to fence.
func (h *InstanceHandler) fenceCopilotCompletions(ctx context.Context, instance *cetirunv1alpha1.Agent) error {
	for attempt := 0; attempt < 3; attempt++ {
		secret, err := h.copilotTokenSecret(ctx, instance)
		if errors.Is(err, errCopilotProxyNotReady) {
			return nil
		}
		if err != nil {
			return err
		}
		marker, err := randomHex()
		if err != nil {
			return err
		}
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations[copilotSignInAnnotation] = marker
		if err := h.Kube.UpdateSecret(ctx, secret); !kube.IsConflict(err) {
			return err
		}
	}
	return errCopilotLoginChanged
}

// newCopilotSignInGeneration identifies a sign-in whose token and models are
// being stored.
func newCopilotSignInGeneration() (string, error) {
	return randomHex()
}

func randomHex() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func loginResponse(data map[string][]byte) CopilotLoginResponse {
	state := string(data[loginState])
	if state == "" {
		return CopilotLoginResponse{State: copilotLoginNone}
	}
	// The browser keeps waiting while the token is being stored.
	if state == copilotLoginCompleting {
		state = copilotLoginPending
	}
	resp := CopilotLoginResponse{State: state, Message: string(data[loginMessage])}
	if state == copilotLoginPending {
		resp.UserCode = string(data[loginUserCode])
		resp.VerificationURI = string(data[loginVerificationURI])
		resp.ExpiresAt = string(data[loginExpiresAt])
		resp.Interval, _ = strconv.Atoi(string(data[loginInterval]))
	}
	return resp
}

// copilotLoginBeingCompleted reports a sign-in of this Agent that committed
// its outcome and is storing its token, within its lease.
func copilotLoginBeingCompleted(secret *corev1.Secret, instance *cetirunv1alpha1.Agent, now time.Time) bool {
	if secret == nil || secret.Labels[kube.AgentUIDLabel] != string(instance.UID) ||
		string(secret.Data[loginState]) != copilotLoginCompleting {
		return false
	}
	until, err := time.Parse(time.RFC3339, string(secret.Data[loginCompletingUntil]))
	return err == nil && now.Before(until)
}

func writeCopilotLoginCompleting(w http.ResponseWriter) {
	writeError(w, http.StatusConflict, "copilot_login_completing", "The GitHub Copilot sign-in is finishing. Try again in a moment.")
}

// writeCopilotFenceError reports a failure to fence earlier completions.
func writeCopilotFenceError(w http.ResponseWriter, instance *cetirunv1alpha1.Agent, err error, message string) {
	if errors.Is(err, errCopilotLoginChanged) {
		writeError(w, http.StatusConflict, "copilot_login_changed", "The sign-in changed meanwhile. Try again.")
		return
	}
	slog.Error("Failed to fence earlier GitHub Copilot sign-ins", "instance", instance.Name, "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", message)
}

// StartCopilotLogin handles POST /api/v1/instances/{name}/copilot/login with
// the body {"uid": "<Agent UID>"}. It starts a GitHub device-code sign-in,
// replacing one in progress, but not one that is storing its token.
func (h *InstanceHandler) StartCopilotLogin(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.copilotLoginUID(w, r)
	if !ok {
		return
	}
	instance, ok := h.copilotAgent(w, r, &uid)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, err := h.copilotTokenSecret(ctx, instance); err != nil {
		if !errors.Is(err, errCopilotProxyNotReady) {
			slog.Error("Failed to read the Copilot token Secret", "instance", instance.Name, "error", err)
		}
		writeError(w, http.StatusConflict, "copilot_not_ready", "The agent's GitHub Copilot proxy is not set up yet. Try again in a minute.")
		return
	}
	// Refuse before asking GitHub for a code that could not be saved.
	if current, err := h.Kube.GetSecret(ctx, kube.InstanceNamespace(instance.Name), copilotLoginSecretName); err == nil &&
		copilotLoginBeingCompleted(current, instance, h.now()) {
		writeCopilotLoginCompleting(w)
		return
	}
	code, err := h.copilotClient().StartDeviceFlow(ctx)
	if err == nil {
		if parsed, parseErr := url.Parse(code.VerificationURI); parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" {
			err = fmt.Errorf("GitHub returned an unexpected verification address")
		}
	}
	if err != nil {
		slog.Error("Failed to start the GitHub Copilot sign-in", "instance", instance.Name, "error", err)
		writeError(w, http.StatusBadGateway, "github_unavailable", "GitHub did not start the sign-in. Try again.")
		return
	}
	now := h.now().UTC()
	data := map[string][]byte{
		loginState:           []byte(copilotLoginPending),
		loginDeviceCode:      []byte(code.DeviceCode),
		loginUserCode:        []byte(code.UserCode),
		loginVerificationURI: []byte(code.VerificationURI),
		loginExpiresAt:       []byte(now.Add(time.Duration(code.ExpiresIn) * time.Second).Format(time.RFC3339)),
		loginInterval:        []byte(strconv.Itoa(code.Interval)),
		loginNextPollAt:      []byte(now.Add(time.Duration(code.Interval) * time.Second).Format(time.RFC3339)),
	}
	if err := h.saveCopilotLogin(ctx, instance, data); err != nil {
		switch {
		case errors.Is(err, errCopilotLoginCompleting):
			writeCopilotLoginCompleting(w)
		case errors.Is(err, errCopilotLoginChanged):
			writeError(w, http.StatusConflict, "copilot_login_changed", "The sign-in changed meanwhile. Try again.")
		case errors.Is(err, kube.ErrInstanceChanged):
			writeManagementError(w, err)
		default:
			slog.Error("Failed to save the GitHub Copilot sign-in", "instance", instance.Name, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to start the sign-in")
		}
		return
	}
	// Only once this sign-in has replaced the earlier one: a completion of
	// an earlier sign-in that outlived its lease must not store its token
	// now. Fencing before the replacement was accepted would fence a
	// completion that holds its lease, and the start is then refused too.
	if err := h.fenceCopilotCompletions(ctx, instance); err != nil {
		writeCopilotFenceError(w, instance, err, "Failed to start the sign-in")
		return
	}
	slog.Info("GitHub Copilot sign-in started", "instance", instance.Name)
	writeJSON(w, http.StatusOK, loginResponse(data))
}

// copilotAgentCurrent rereads the Agent and reports kube.ErrInstanceChanged
// when it is no longer the incarnation instance was read as: an Agent deleted
// and created again under the same name has another UID, and its sign-in is
// not a request for the earlier one's to change.
func (h *InstanceHandler) copilotAgentCurrent(ctx context.Context, instance *cetirunv1alpha1.Agent) error {
	current, err := h.Kube.GetInstanceByName(ctx, instance.Name)
	if kube.IsNotFound(err) {
		return kube.ErrInstanceChanged
	}
	if err != nil {
		return err
	}
	if current.UID != instance.UID || current.DeletionTimestamp != nil {
		return kube.ErrInstanceChanged
	}
	return nil
}

// saveCopilotLogin creates or replaces the sign-in Secret, with the version
// it read: it never replaces a sign-in that is storing its token, and it
// retries when a poll updated the Secret meanwhile. Between reading the
// Secret and writing it, it checks that the Agent is still the one the
// request was made for, so a start that waited on GitHub while the Agent was
// replaced changes nothing. A Secret labelled for another UID is then left
// over from an Agent that no longer exists, and is replaced.
func (h *InstanceHandler) saveCopilotLogin(ctx context.Context, instance *cetirunv1alpha1.Agent, data map[string][]byte) error {
	namespace := kube.InstanceNamespace(instance.Name)
	labels := map[string]string{
		"app.kubernetes.io/name":       "cetirun",
		"app.kubernetes.io/managed-by": "cetirun-portal",
		kube.AgentUIDLabel:             string(instance.UID),
	}
	for attempt := 0; attempt < 3; attempt++ {
		secret, err := h.Kube.GetSecret(ctx, namespace, copilotLoginSecretName)
		if err != nil && !kube.IsNotFound(err) {
			return err
		}
		missing := err != nil
		if err := h.copilotAgentCurrent(ctx, instance); err != nil {
			return err
		}
		if missing {
			err = h.Kube.CreateSecret(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: copilotLoginSecretName, Namespace: namespace, Labels: labels},
				Type:       corev1.SecretTypeOpaque,
				Data:       data,
			})
			if kube.IsAlreadyExists(err) {
				continue
			}
			return err
		}
		if copilotLoginBeingCompleted(secret, instance, h.now()) {
			return errCopilotLoginCompleting
		}
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		for k, v := range labels {
			secret.Labels[k] = v
		}
		secret.Data = data
		err = h.Kube.UpdateSecret(ctx, secret)
		if kube.IsConflict(err) {
			continue
		}
		return err
	}
	return errCopilotLoginChanged
}

// PollCopilotLogin handles GET /api/v1/instances/{name}/copilot/login. While
// a sign-in is pending it asks GitHub at most once per polling interval,
// across replicas and browser tabs; once the user has authorized, it stores
// the token for the Agent's proxy and records the account's models. It needs
// no Agent UID: it only advances a sign-in that a start for the current Agent
// created, which the Secret's Agent UID label proves.
func (h *InstanceHandler) PollCopilotLogin(w http.ResponseWriter, r *http.Request) {
	instance, ok := h.copilotAgent(w, r, nil)
	if !ok {
		return
	}
	ctx := r.Context()
	secret, err := h.Kube.GetSecret(ctx, kube.InstanceNamespace(instance.Name), copilotLoginSecretName)
	if kube.IsNotFound(err) {
		writeJSON(w, http.StatusOK, CopilotLoginResponse{State: copilotLoginNone})
		return
	}
	if err != nil {
		slog.Error("Failed to read the GitHub Copilot sign-in", "instance", instance.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read the sign-in")
		return
	}
	if secret.Labels[kube.AgentUIDLabel] != string(instance.UID) {
		writeJSON(w, http.StatusOK, CopilotLoginResponse{State: copilotLoginNone})
		return
	}
	if string(secret.Data[loginState]) == copilotLoginCompleting {
		if copilotLoginBeingCompleted(secret, instance, h.now()) {
			writeJSON(w, http.StatusOK, loginResponse(secret.Data))
			return
		}
		// The replica storing the token stopped before recording it. Record
		// the sign-in as failed and only then, once that is accepted, fence
		// it: should it only have stalled, it can no longer store the token
		// of a sign-in reported as failed. Fencing first would fence a newer
		// sign-in's completion if this outcome were then refused.
		secret.Data = map[string][]byte{
			loginState:   []byte(copilotLoginFailed),
			loginMessage: []byte("The sign-in did not finish. Start it again."),
		}
		if err := h.Kube.UpdateSecret(ctx, secret); err != nil {
			if !kube.IsConflict(err) && !kube.IsNotFound(err) {
				slog.Error("Failed to record the GitHub Copilot sign-in outcome", "instance", instance.Name, "error", err)
			}
			h.writeCurrentCopilotLogin(ctx, w, instance)
			return
		}
		if err := h.fenceCopilotCompletions(ctx, instance); err != nil {
			slog.Error("Failed to fence an abandoned GitHub Copilot sign-in", "instance", instance.Name, "error", err)
		}
		writeJSON(w, http.StatusOK, loginResponse(secret.Data))
		return
	}
	if string(secret.Data[loginState]) != copilotLoginPending {
		writeJSON(w, http.StatusOK, loginResponse(secret.Data))
		return
	}
	now := h.now().UTC()
	expiresAt, err := time.Parse(time.RFC3339, string(secret.Data[loginExpiresAt]))
	if err != nil || !now.Before(expiresAt) {
		h.endCopilotLogin(ctx, w, instance, secret, copilotLoginExpired, "The code expired. Start the sign-in again.")
		return
	}
	if nextPoll, err := time.Parse(time.RFC3339, string(secret.Data[loginNextPollAt])); err == nil && now.Before(nextPoll) {
		writeJSON(w, http.StatusOK, loginResponse(secret.Data))
		return
	}
	// Claim this poll before calling GitHub; a replica or tab that loses the
	// race waits for the next interval instead of polling too.
	interval, _ := strconv.Atoi(string(secret.Data[loginInterval]))
	interval = max(interval, 5)
	secret.Data[loginNextPollAt] = []byte(now.Add(time.Duration(interval) * time.Second).Format(time.RFC3339))
	if err := h.Kube.UpdateSecret(ctx, secret); err != nil {
		if !kube.IsConflict(err) {
			slog.Error("Failed to update the GitHub Copilot sign-in", "instance", instance.Name, "error", err)
		}
		writeJSON(w, http.StatusOK, loginResponse(secret.Data))
		return
	}
	result, err := h.copilotClient().PollAccessToken(ctx, string(secret.Data[loginDeviceCode]))
	if err != nil {
		slog.Warn("GitHub Copilot sign-in poll failed", "instance", instance.Name, "error", err)
		writeJSON(w, http.StatusOK, loginResponse(secret.Data))
		return
	}
	switch {
	case result.AccessToken != "":
		h.finishCopilotLogin(ctx, w, instance, secret, result.AccessToken)
	case result.Error == "authorization_pending":
		writeJSON(w, http.StatusOK, loginResponse(secret.Data))
	case result.Error == "slow_down":
		if result.Interval > interval {
			interval = result.Interval
		} else {
			interval += 5
		}
		secret.Data[loginInterval] = []byte(strconv.Itoa(interval))
		secret.Data[loginNextPollAt] = []byte(now.Add(time.Duration(interval) * time.Second).Format(time.RFC3339))
		if err := h.Kube.UpdateSecret(ctx, secret); err != nil && !kube.IsConflict(err) {
			slog.Error("Failed to update the GitHub Copilot sign-in", "instance", instance.Name, "error", err)
		}
		writeJSON(w, http.StatusOK, loginResponse(secret.Data))
	case result.Error == "expired_token":
		h.endCopilotLogin(ctx, w, instance, secret, copilotLoginExpired, "The code expired. Start the sign-in again.")
	case result.Error == "access_denied":
		h.endCopilotLogin(ctx, w, instance, secret, copilotLoginDenied, "The sign-in was cancelled at GitHub.")
	default:
		slog.Warn("GitHub Copilot sign-in failed", "instance", instance.Name, "error", result.Error)
		h.endCopilotLogin(ctx, w, instance, secret, copilotLoginFailed, "GitHub refused the sign-in. Start it again.")
	}
}

// finishCopilotLogin checks that the account can use GitHub Copilot, commits
// the sign-in, stores the token for the proxy, then records the account's
// models on the Agent. The commit comes first: a cancel or a new start since
// this poll claimed the sign-in makes it conflict, and the token is then
// dropped instead of taking effect. The token Secret is read before the
// commit and the token stored under that version, so a start, a cancel or
// the end of this completion after its lease ran out, each of which fences
// the Secret once accepted, drops the token too. The token and the models
// carry the same new sign-in generation, and the models are recorded only
// while the stored token is still this sign-in's; the operator uses models
// only with the token of their generation, so the two never mix even when a
// newer sign-in stores its token in between. The token is written before the
// models: the operator registers models once the proxy has picked it up, and
// the annotation is what triggers the operator.
func (h *InstanceHandler) finishCopilotLogin(ctx context.Context, w http.ResponseWriter, instance *cetirunv1alpha1.Agent, login *corev1.Secret, token string) {
	models, err := h.copilotClient().ListModels(ctx, token)
	switch {
	case errors.Is(err, copilot.ErrNoCopilot):
		h.endCopilotLogin(ctx, w, instance, login, copilotLoginFailed, "This GitHub account cannot use GitHub Copilot.")
		return
	case err != nil:
		slog.Error("Failed to list the GitHub Copilot models", "instance", instance.Name, "error", err)
		h.endCopilotLogin(ctx, w, instance, login, copilotLoginFailed, "Signed in to GitHub, but GitHub Copilot's model list could not be read. Start the sign-in again.")
		return
	case len(models) == 0:
		h.endCopilotLogin(ctx, w, instance, login, copilotLoginFailed, "This GitHub Copilot account offers no chat models.")
		return
	}
	generation, err := newCopilotSignInGeneration()
	if err != nil {
		slog.Error("Failed to create a GitHub Copilot sign-in generation", "instance", instance.Name, "error", err)
		h.endCopilotLogin(ctx, w, instance, login, copilotLoginFailed, "Signed in to GitHub, but the token could not be stored for the agent. Start the sign-in again.")
		return
	}
	secret, err := h.copilotTokenSecret(ctx, instance)
	if err != nil {
		slog.Error("Failed to read the GitHub Copilot token Secret", "instance", instance.Name, "error", err)
		h.endCopilotLogin(ctx, w, instance, login, copilotLoginFailed, "Signed in to GitHub, but the token could not be stored for the agent. Start the sign-in again.")
		return
	}
	if !h.commitCopilotLogin(ctx, instance, login) {
		slog.Info("GitHub Copilot sign-in was cancelled or replaced while it was polled; its token is dropped", "instance", instance.Name)
		h.writeCurrentCopilotLogin(ctx, w, instance)
		return
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data[cetirunv1alpha1.CopilotTokenSecretKey] = []byte(token)
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Annotations[cetirunv1alpha1.AnnotationCopilotSignInGeneration] = generation
	secret.Annotations[cetirunv1alpha1.AnnotationCopilotTokenStoredAt] = h.now().UTC().Format(time.RFC3339)
	if err := h.Kube.UpdateSecret(ctx, secret); err != nil {
		if kube.IsConflict(err) {
			slog.Info("GitHub Copilot sign-in was replaced before its token was stored; the token is dropped", "instance", instance.Name)
			h.endCopilotLogin(ctx, w, instance, login, copilotLoginFailed, "The sign-in changed meanwhile. Start it again.")
			return
		}
		slog.Error("Failed to store the GitHub Copilot token", "instance", instance.Name, "error", err)
		h.endCopilotLogin(ctx, w, instance, login, copilotLoginFailed, "Signed in to GitHub, but the token could not be stored for the agent. Start the sign-in again.")
		return
	}
	stillStored := func(ctx context.Context) error {
		current, err := h.copilotTokenSecret(ctx, instance)
		if err != nil {
			return err
		}
		if current.Annotations[cetirunv1alpha1.AnnotationCopilotSignInGeneration] != generation {
			return errCopilotLoginSuperseded
		}
		return nil
	}
	if err := h.Kube.SetCopilotModels(ctx, instance.Name, instance.UID, models, generation, stillStored); err != nil {
		if errors.Is(err, errCopilotLoginSuperseded) {
			slog.Info("GitHub Copilot sign-in was replaced before its models were recorded; they are dropped", "instance", instance.Name)
			h.endCopilotLogin(ctx, w, instance, login, copilotLoginFailed, "The sign-in changed meanwhile. Start it again.")
			return
		}
		slog.Error("Failed to record the GitHub Copilot models", "instance", instance.Name, "error", err)
		h.endCopilotLogin(ctx, w, instance, login, copilotLoginFailed, "Signed in, but the model list could not be saved. Start the sign-in again.")
		return
	}
	slog.Info("GitHub Copilot sign-in completed", "instance", instance.Name, "models", len(models))
	h.endCopilotLogin(ctx, w, instance, login, copilotLoginSucceeded,
		fmt.Sprintf("Signed in to GitHub Copilot: %d models. They appear on the agent once its proxy has started.", len(models)))
}

// commitCopilotLogin marks the sign-in as being completed, with the version
// the poll claimed. It reports false when the sign-in changed meanwhile.
func (h *InstanceHandler) commitCopilotLogin(ctx context.Context, instance *cetirunv1alpha1.Agent, login *corev1.Secret) bool {
	data := map[string][]byte{
		loginState:           []byte(copilotLoginCompleting),
		loginCompletingUntil: []byte(h.now().UTC().Add(copilotLoginLease).Format(time.RFC3339)),
	}
	// The browser keeps showing the code until the outcome is recorded; the
	// device code is no longer needed.
	for _, key := range []string{loginUserCode, loginVerificationURI, loginExpiresAt, loginInterval} {
		if value, ok := login.Data[key]; ok {
			data[key] = value
		}
	}
	login.Data = data
	if err := h.Kube.UpdateSecret(ctx, login); err != nil {
		if !kube.IsConflict(err) && !kube.IsNotFound(err) {
			slog.Error("Failed to commit the GitHub Copilot sign-in", "instance", instance.Name, "error", err)
		}
		return false
	}
	return true
}

// writeCurrentCopilotLogin reports the sign-in as it is now stored.
func (h *InstanceHandler) writeCurrentCopilotLogin(ctx context.Context, w http.ResponseWriter, instance *cetirunv1alpha1.Agent) {
	secret, err := h.Kube.GetSecret(ctx, kube.InstanceNamespace(instance.Name), copilotLoginSecretName)
	if err != nil || secret.Labels[kube.AgentUIDLabel] != string(instance.UID) {
		writeJSON(w, http.StatusOK, CopilotLoginResponse{State: copilotLoginNone})
		return
	}
	writeJSON(w, http.StatusOK, loginResponse(secret.Data))
}

// endCopilotLogin records the outcome in place of the sign-in it was polled
// for and reports it. A sign-in cancelled or started meanwhile is left alone
// and reported as it is.
func (h *InstanceHandler) endCopilotLogin(ctx context.Context, w http.ResponseWriter, instance *cetirunv1alpha1.Agent, login *corev1.Secret, state, message string) {
	login.Data = map[string][]byte{loginState: []byte(state), loginMessage: []byte(message)}
	if err := h.Kube.UpdateSecret(ctx, login); err != nil {
		if kube.IsConflict(err) || kube.IsNotFound(err) {
			h.writeCurrentCopilotLogin(ctx, w, instance)
			return
		}
		slog.Error("Failed to record the GitHub Copilot sign-in outcome", "instance", instance.Name, "error", err)
	}
	writeJSON(w, http.StatusOK, loginResponse(login.Data))
}

// CancelCopilotLogin handles DELETE /api/v1/instances/{name}/copilot/login
// with the body {"uid": "<Agent UID>"}. A sign-in that is storing its token
// is not cancelled. Between reading the sign-in Secret and clearing it, the
// cancel checks that the Agent is still the one it was made for; a Secret
// labelled for another UID, left over from an Agent that no longer exists, is
// not this Agent's sign-in and is left alone.
func (h *InstanceHandler) CancelCopilotLogin(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.copilotLoginUID(w, r)
	if !ok {
		return
	}
	instance, ok := h.copilotAgent(w, r, &uid)
	if !ok {
		return
	}
	ctx := r.Context()
	namespace := kube.InstanceNamespace(instance.Name)
	for attempt := 0; attempt < 3; attempt++ {
		secret, err := h.Kube.GetSecret(ctx, namespace, copilotLoginSecretName)
		if kube.IsNotFound(err) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			slog.Error("Failed to read the GitHub Copilot sign-in", "instance", instance.Name, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to cancel the sign-in")
			return
		}
		if err := h.copilotAgentCurrent(ctx, instance); err != nil {
			writeManagementError(w, err)
			return
		}
		if secret.Labels[kube.AgentUIDLabel] != string(instance.UID) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if copilotLoginBeingCompleted(secret, instance, h.now()) {
			writeCopilotLoginCompleting(w)
			return
		}
		// With the version just read: a poll that claimed the sign-in
		// meanwhile makes this conflict, and the cancel is retried.
		secret.Data = map[string][]byte{}
		err = h.Kube.UpdateSecret(ctx, secret)
		if kube.IsConflict(err) {
			continue
		}
		if kube.IsNotFound(err) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			slog.Error("Failed to cancel the GitHub Copilot sign-in", "instance", instance.Name, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to cancel the sign-in")
			return
		}
		// Only once the cancel is accepted: a completion that outlived its
		// lease must not store its token after it. Fencing before the cancel
		// was accepted would fence a completion that holds its lease, and the
		// cancel is then refused too.
		if err := h.fenceCopilotCompletions(ctx, instance); err != nil {
			writeCopilotFenceError(w, instance, err, "Failed to cancel the sign-in")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeError(w, http.StatusConflict, "copilot_login_changed", "The sign-in changed meanwhile. Try again.")
}
