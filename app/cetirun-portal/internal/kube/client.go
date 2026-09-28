// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	cryptorand "crypto/rand"
	stderrors "errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

var ErrUnsupportedCustomUpdate = stderrors.New("Custom does not support Gateway Token, Teams or Tailscale updates")

func IsUnsupportedCustomUpdate(err error) bool {
	return stderrors.Is(err, ErrUnsupportedCustomUpdate)
}

func validateInstanceUpdate(instance *cetirunv1alpha1.Agent, opts UpdateInstanceOpts) error {
	if instance.Spec.Agent.Type == cetirunv1alpha1.AgentTypeCustom &&
		(opts.GatewayToken != nil || opts.TailscaleAuthKey != nil || opts.TeamsEnabled != nil) {
		return ErrUnsupportedCustomUpdate
	}
	return nil
}

// AnnotationSecretUpdatedAt is set on the CR when a Secret is updated,
// triggering a reconcile even when the CR spec (generation) hasn't changed.
const AnnotationSecretUpdatedAt = "cetirun.microsoft.com/secret-updated-at"

// UpdateInstanceOpts specifies which fields to update on an existing instance.
// nil pointer = no change; non-nil = apply the pointed-to value.
type UpdateInstanceOpts struct {
	DisplayName      *string // nil = no change
	Team             *string // nil = no change
	GatewayToken     *string // nil = no change, non-empty = update
	TailscaleAuthKey *string // nil = no change, "" = disable tailscale, non-empty = update/enable
	TeamsEnabled     *bool   // nil = no change
	Model            *string // nil = no change, "" = use deployment default
}

// CreateInstanceOptions contains security-sensitive platform-derived fields.
// Callers must never populate owner identifiers from request JSON.
type CreateInstanceOptions struct {
	AgentIdentityEnabled bool
	OwnerTenantID        string
	OwnerObjectID        string
}

const chartVersionTTL = 5 * time.Minute

// Client wraps controller-runtime client for Agent operations.
type Client struct {
	client          client.Client
	chartVersionMu  sync.RWMutex
	chartVersionVal string
	chartVersionErr error
	chartVersionExp time.Time
	operatorDomain  string
}

// NewClient creates a new kube Client.
func NewClient(c client.Client) *Client {
	return &Client{client: c}
}

// GetInstance retrieves an Agent by alias (cluster-scoped).
func (c *Client) GetInstance(ctx context.Context, alias string) (*cetirunv1alpha1.Agent, error) {
	instance := &cetirunv1alpha1.Agent{}
	key := client.ObjectKey{Name: alias}
	if err := c.client.Get(ctx, key, instance); err != nil {
		return nil, err
	}
	return instance, nil
}

// CreateInstance creates a new Agent CR.
// instanceName is the logical name for the instance (spec.instanceName and CR name);
// if empty it defaults to alias (backward-compatible single-instance behaviour).
// If gatewayToken or tailscaleAuthKey are non-empty, it pre-creates the
// corresponding Secrets in the instance namespace before creating the CR.
func (c *Client) CreateInstance(ctx context.Context, alias, displayName, team, email, instanceName, agentType, gatewayToken, tailscaleAuthKey, model string, teamsEnabled bool, optional ...CreateInstanceOptions) (*cetirunv1alpha1.Agent, error) {
	if instanceName == "" {
		instanceName = alias
	}
	if agentType == "" {
		agentType = string(cetirunv1alpha1.DefaultAgentType)
	}
	ns := fmt.Sprintf("cetirun-%s", instanceName)
	var createdNamespace *corev1.Namespace

	// Pre-create secrets if provided.
	if gatewayToken != "" || tailscaleAuthKey != "" {
		var err error
		createdNamespace, err = c.createNamespace(ctx, ns, alias)
		if err != nil {
			return nil, fmt.Errorf("ensure namespace %s: %w", ns, err)
		}
	}
	if gatewayToken != "" {
		if err := c.createOrUpdateSecret(ctx, ns, "gateway-token", map[string][]byte{
			"OPENCLAW_GATEWAY_TOKEN": []byte(gatewayToken),
		}); err != nil {
			return nil, fmt.Errorf("create gateway-token secret: %w", err)
		}
	}
	if tailscaleAuthKey != "" {
		if err := c.createOrUpdateSecret(ctx, ns, "tailscale-auth", map[string][]byte{
			"TAILSCALE_AUTH_KEY": []byte(tailscaleAuthKey),
		}); err != nil {
			return nil, fmt.Errorf("create tailscale-auth secret: %w", err)
		}
	}

	instance := &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:       instanceName,
			Finalizers: []string{cetirunv1alpha1.Finalizer},
		},
		Spec: cetirunv1alpha1.AgentSpec{
			Agent:                 cetirunv1alpha1.HarnessSpec{Type: cetirunv1alpha1.AgentType(agentType)},
			Model:                 strings.TrimSpace(model),
			GatewayTokenSecretRef: "gateway-token",
			Team:                  team,
			User: cetirunv1alpha1.UserSpec{
				Alias:       alias,
				DisplayName: displayName,
				Team:        team,
				Email:       email,
			},
		},
	}
	if len(optional) > 0 && optional[0].AgentIdentityEnabled {
		if optional[0].OwnerTenantID == "" || optional[0].OwnerObjectID == "" {
			return nil, fmt.Errorf("verified owner identity is required for Agent Identity")
		}
		instance.Annotations = map[string]string{
			cetirunv1alpha1.AnnotationOwnerTenantID: optional[0].OwnerTenantID,
			cetirunv1alpha1.AnnotationOwnerObjectID: optional[0].OwnerObjectID,
		}
		instance.Spec.AgentIdentity = &cetirunv1alpha1.AgentIdentitySpec{Enabled: true}
	}
	if tailscaleAuthKey != "" {
		instance.Spec.Tailscale = &cetirunv1alpha1.TailscaleSpec{
			AuthKeySecretRef: "tailscale-auth",
		}
	}
	if teamsEnabled {
		instance.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{Enabled: true}
	}
	if err := c.client.Create(ctx, instance, client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		return nil, err
	}
	if createdNamespace != nil {
		if err := c.bindCreatedNamespace(ctx, createdNamespace, instance); err != nil {
			return nil, fmt.Errorf("Agent created but namespace ownership could not be recorded; cleanup requires recovery: %w", err)
		}
	}
	return instance, nil
}

// DeleteInstance deletes an Agent by alias (CR object name = alias).
func (c *Client) DeleteInstance(ctx context.Context, alias string) error {
	instance := &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name: alias,
		},
	}
	return c.client.Delete(ctx, instance)
}

// DeleteInstanceByName deletes an Agent whose spec.instanceName matches instanceName.
func (c *Client) DeleteInstanceByName(ctx context.Context, instanceName string) error {
	instance, err := c.GetInstanceByName(ctx, instanceName)
	if err != nil {
		return err
	}
	return c.client.Delete(ctx, instance)
}

// IsNotFound returns true if the error is a Kubernetes not-found error.
func IsNotFound(err error) bool {
	return errors.IsNotFound(err)
}

// IsAlreadyExists returns true if the error is a Kubernetes already-exists error.
func IsAlreadyExists(err error) bool {
	return errors.IsAlreadyExists(err)
}

// IsForbidden returns true if the error is a Kubernetes forbidden error
// (typically RBAC denial).
func IsForbidden(err error) bool {
	return errors.IsForbidden(err)
}

// GetGatewayToken retrieves the gateway token from the instance's Secret.
func (c *Client) GetGatewayToken(ctx context.Context, namespace, secretName string) (string, error) {
	if secretName == "" {
		return "", fmt.Errorf("secret name is empty for namespace %q", namespace)
	}

	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: namespace, Name: secretName}
	if err := c.client.Get(ctx, key, secret); err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", namespace, secretName, err)
	}
	tokenBytes, ok := secret.Data["OPENCLAW_GATEWAY_TOKEN"]
	if !ok || len(tokenBytes) == 0 {
		return "", fmt.Errorf("missing key OPENCLAW_GATEWAY_TOKEN in secret %s/%s", namespace, secretName)
	}
	return string(tokenBytes), nil
}

// GetAPIServerKey retrieves the Hermes API server key from the instance's Secret.
func (c *Client) GetAPIServerKey(ctx context.Context, namespace, secretName string) (string, error) {
	if secretName == "" {
		return "", fmt.Errorf("secret name is empty for namespace %q", namespace)
	}

	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: namespace, Name: secretName}
	if err := c.client.Get(ctx, key, secret); err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", namespace, secretName, err)
	}
	keyBytes, ok := secret.Data["API_SERVER_KEY"]
	if !ok || len(keyBytes) == 0 {
		return "", fmt.Errorf("missing key API_SERVER_KEY in secret %s/%s", namespace, secretName)
	}
	return string(keyBytes), nil
}

// GetCetiRunAuthKey retrieves the in-pod cetirun-auth bearer key from the
// instance's Secret. For OpenClaw instances the secret only contains
// CETIRUN_AUTH_KEY; for older Hermes instances created before the dedicated
// key was added, fall back to API_SERVER_KEY (which the helper service still
// accepts as a legacy bearer credential).
func (c *Client) GetCetiRunAuthKey(ctx context.Context, namespace, secretName string) (string, error) {
	if secretName == "" {
		return "", fmt.Errorf("secret name is empty for namespace %q", namespace)
	}

	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: namespace, Name: secretName}
	if err := c.client.Get(ctx, key, secret); err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", namespace, secretName, err)
	}
	if keyBytes, ok := secret.Data["CETIRUN_AUTH_KEY"]; ok && len(keyBytes) > 0 {
		return string(keyBytes), nil
	}
	if keyBytes, ok := secret.Data["API_SERVER_KEY"]; ok && len(keyBytes) > 0 {
		return string(keyBytes), nil
	}
	return "", fmt.Errorf("missing key CETIRUN_AUTH_KEY (and legacy API_SERVER_KEY) in secret %s/%s", namespace, secretName)
}

// GetTailscaleAuthKey retrieves the Tailscale auth key from the instance's Secret.
func (c *Client) GetTailscaleAuthKey(ctx context.Context, namespace, secretName string) (string, error) {
	if secretName == "" {
		return "", fmt.Errorf("secret name is empty for namespace %q", namespace)
	}

	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: namespace, Name: secretName}
	if err := c.client.Get(ctx, key, secret); err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", namespace, secretName, err)
	}
	keyBytes, ok := secret.Data["TAILSCALE_AUTH_KEY"]
	if !ok || len(keyBytes) == 0 {
		return "", fmt.Errorf("missing key TAILSCALE_AUTH_KEY in secret %s/%s", namespace, secretName)
	}
	return string(keyBytes), nil
}

// ensureNamespace creates the instance namespace if it doesn't already exist.
func (c *Client) ensureNamespace(ctx context.Context, ns, alias string) error {
	_, err := c.createNamespace(ctx, ns, alias)
	return err
}

// createNamespace returns only a namespace created by this call, never an
// existing namespace that a same-named Agent might otherwise adopt.
func (c *Client) createNamespace(ctx context.Context, ns, alias string) (*corev1.Namespace, error) {
	nsObj := &corev1.Namespace{}
	err := c.client.Get(ctx, client.ObjectKey{Name: ns}, nsObj)
	if err == nil {
		return nil, nil
	}
	if !errors.IsNotFound(err) {
		return nil, fmt.Errorf("get namespace %s: %w", ns, err)
	}
	nsObj = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "cetirun",
				"app.kubernetes.io/instance":   alias,
				"app.kubernetes.io/managed-by": "cetirun-operator",
			},
		},
	}
	if err := c.client.Create(ctx, nsObj); err != nil {
		return nil, client.IgnoreAlreadyExists(err)
	}
	return nsObj, nil
}

func (c *Client) bindCreatedNamespace(ctx context.Context, created *corev1.Namespace, agent *cetirunv1alpha1.Agent) error {
	if created.UID == "" || agent.UID == "" {
		return fmt.Errorf("namespace and Agent must have persisted UIDs")
	}
	owner := metav1.OwnerReference{APIVersion: cetirunv1alpha1.GroupVersion.String(), Kind: "Agent", Name: agent.Name, UID: agent.UID}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &corev1.Namespace{}
		if err := c.client.Get(ctx, client.ObjectKeyFromObject(created), current); err != nil {
			return err
		}
		if current.UID != created.UID || !current.DeletionTimestamp.IsZero() {
			return fmt.Errorf("newly created namespace was replaced or is deleting")
		}
		if len(current.OwnerReferences) != 0 {
			if len(current.OwnerReferences) == 1 && current.OwnerReferences[0] == owner {
				return nil
			}
			return fmt.Errorf("newly created namespace already has another owner")
		}
		before := current.DeepCopy()
		current.OwnerReferences = []metav1.OwnerReference{owner}
		return c.client.Patch(ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
}

// UpdateInstance updates an existing Agent CR and its associated Secrets.
// Secrets are updated first (idempotent), then the CR is updated inside a
// RetryOnConflict loop to handle races with operator status updates.
func (c *Client) UpdateInstance(ctx context.Context, alias string, opts UpdateInstanceOpts) (*cetirunv1alpha1.Agent, error) {
	// Pre-flight: verify the instance exists.
	existing, err := c.GetInstance(ctx, alias)
	if err != nil {
		return nil, err
	}
	if err := validateInstanceUpdate(existing, opts); err != nil {
		return nil, err
	}

	ns := fmt.Sprintf("cetirun-%s", alias)
	secretsUpdated := false

	// Update Secrets outside the retry loop (idempotent operations).
	if opts.GatewayToken != nil && *opts.GatewayToken != "" {
		if err := c.ensureNamespace(ctx, ns, alias); err != nil {
			return nil, fmt.Errorf("ensure namespace %s: %w", ns, err)
		}
		if err := c.createOrUpdateSecret(ctx, ns, "gateway-token", map[string][]byte{
			"OPENCLAW_GATEWAY_TOKEN": []byte(*opts.GatewayToken),
		}); err != nil {
			return nil, fmt.Errorf("update gateway-token secret: %w", err)
		}
		secretsUpdated = true
	}
	if opts.TailscaleAuthKey != nil && *opts.TailscaleAuthKey != "" {
		if err := c.ensureNamespace(ctx, ns, alias); err != nil {
			return nil, fmt.Errorf("ensure namespace %s: %w", ns, err)
		}
		if err := c.createOrUpdateSecret(ctx, ns, "tailscale-auth", map[string][]byte{
			"TAILSCALE_AUTH_KEY": []byte(*opts.TailscaleAuthKey),
		}); err != nil {
			return nil, fmt.Errorf("update tailscale-auth secret: %w", err)
		}
		secretsUpdated = true
	}

	// Update the CR with RetryOnConflict to handle races with operator.
	var result *cetirunv1alpha1.Agent
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		instance, err := c.GetInstance(ctx, alias)
		if err != nil {
			return err
		}
		if err := validateInstanceUpdate(instance, opts); err != nil {
			return err
		}

		crChanged := false

		if opts.DisplayName != nil {
			instance.Spec.User.DisplayName = *opts.DisplayName
			crChanged = true
		}
		if opts.Team != nil {
			instance.Spec.User.Team = *opts.Team
			crChanged = true
		}
		if opts.GatewayToken != nil && *opts.GatewayToken != "" {
			touchSecretAnnotation(instance)
			crChanged = true
		}
		if opts.TailscaleAuthKey != nil {
			if *opts.TailscaleAuthKey != "" {
				if instance.Spec.Tailscale == nil {
					instance.Spec.Tailscale = &cetirunv1alpha1.TailscaleSpec{
						AuthKeySecretRef: "tailscale-auth",
					}
				}
				touchSecretAnnotation(instance)
				crChanged = true
			} else {
				// Disable tailscale.
				instance.Spec.Tailscale = nil
				crChanged = true
			}
		}
		if opts.TeamsEnabled != nil {
			if *opts.TeamsEnabled {
				if instance.Spec.MSTeams == nil {
					instance.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{}
				}
				instance.Spec.MSTeams.Enabled = true
			} else {
				instance.Spec.MSTeams = nil
			}
			crChanged = true
		}
		if opts.Model != nil {
			instance.Spec.Model = strings.TrimSpace(*opts.Model)
			crChanged = true
		}
		if secretsUpdated && !crChanged {
			touchSecretAnnotation(instance)
			crChanged = true
		}

		if crChanged {
			if err := c.client.Update(ctx, instance); err != nil {
				return fmt.Errorf("update instance: %w", err)
			}
		}

		result = instance
		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// UpdateInstanceByName updates an Agent looked up by metadata.name.
// Unlike UpdateInstance (which looks up by alias==metadata.name), this uses
// the actual CR name so it works for instances with custom names (multi-instance).
func (c *Client) UpdateInstanceByName(ctx context.Context, instanceName string, opts UpdateInstanceOpts) (*cetirunv1alpha1.Agent, error) {
	// Pre-flight: verify the instance exists and get the alias for namespace/secret ops.
	existing, err := c.GetInstanceByName(ctx, instanceName)
	if err != nil {
		return nil, err
	}
	if err := validateInstanceUpdate(existing, opts); err != nil {
		return nil, err
	}
	alias := existing.Spec.User.Alias
	ns := fmt.Sprintf("cetirun-%s", instanceName)
	secretsUpdated := false

	if opts.GatewayToken != nil && *opts.GatewayToken != "" {
		if err := c.ensureNamespace(ctx, ns, alias); err != nil {
			return nil, fmt.Errorf("ensure namespace %s: %w", ns, err)
		}
		if err := c.createOrUpdateSecret(ctx, ns, "gateway-token", map[string][]byte{
			"OPENCLAW_GATEWAY_TOKEN": []byte(*opts.GatewayToken),
		}); err != nil {
			return nil, fmt.Errorf("update gateway-token secret: %w", err)
		}
		secretsUpdated = true
	}
	if opts.TailscaleAuthKey != nil && *opts.TailscaleAuthKey != "" {
		if err := c.ensureNamespace(ctx, ns, alias); err != nil {
			return nil, fmt.Errorf("ensure namespace %s: %w", ns, err)
		}
		if err := c.createOrUpdateSecret(ctx, ns, "tailscale-auth", map[string][]byte{
			"TAILSCALE_AUTH_KEY": []byte(*opts.TailscaleAuthKey),
		}); err != nil {
			return nil, fmt.Errorf("update tailscale-auth secret: %w", err)
		}
		secretsUpdated = true
	}

	var result *cetirunv1alpha1.Agent
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		instance, err := c.GetInstanceByName(ctx, instanceName)
		if err != nil {
			return err
		}
		if err := validateInstanceUpdate(instance, opts); err != nil {
			return err
		}
		crChanged := false

		if opts.DisplayName != nil {
			instance.Spec.User.DisplayName = *opts.DisplayName
			crChanged = true
		}
		if opts.Team != nil {
			instance.Spec.User.Team = *opts.Team
			crChanged = true
		}
		if opts.GatewayToken != nil && *opts.GatewayToken != "" {
			touchSecretAnnotation(instance)
			crChanged = true
		}
		if opts.TailscaleAuthKey != nil {
			if *opts.TailscaleAuthKey != "" {
				if instance.Spec.Tailscale == nil {
					instance.Spec.Tailscale = &cetirunv1alpha1.TailscaleSpec{
						AuthKeySecretRef: "tailscale-auth",
					}
				}
				touchSecretAnnotation(instance)
				crChanged = true
			} else {
				instance.Spec.Tailscale = nil
				crChanged = true
			}
		}
		if opts.TeamsEnabled != nil {
			if *opts.TeamsEnabled {
				if instance.Spec.MSTeams == nil {
					instance.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{}
				}
				instance.Spec.MSTeams.Enabled = true
			} else {
				instance.Spec.MSTeams = nil
			}
			crChanged = true
		}
		if opts.Model != nil {
			instance.Spec.Model = strings.TrimSpace(*opts.Model)
			crChanged = true
		}
		if secretsUpdated && !crChanged {
			touchSecretAnnotation(instance)
			crChanged = true
		}

		if crChanged {
			if err := c.client.Update(ctx, instance); err != nil {
				return fmt.Errorf("update instance: %w", err)
			}
		}
		result = instance
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// UpgradeInstanceByName upgrades an Agent looked up by spec.instanceName.
func (c *Client) UpgradeInstanceByName(ctx context.Context, instanceName string) (*cetirunv1alpha1.Agent, error) {
	latestVersion, err := c.GetOperatorChartVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("get latest chart version: %w", err)
	}
	var result *cetirunv1alpha1.Agent
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		instance, err := c.GetInstanceByName(ctx, instanceName)
		if err != nil {
			return err
		}
		if instance.Spec.Helm.Version == latestVersion {
			result = instance
			return nil
		}
		instance.Spec.Helm.Version = latestVersion
		if err := c.client.Update(ctx, instance); err != nil {
			return fmt.Errorf("update instance: %w", err)
		}
		result = instance
		return nil
	})
	return result, err
}

// touchSecretAnnotation sets a timestamp annotation on the CR so that
// a watch event fires even when spec.generation doesn't change (secret-only updates).
func touchSecretAnnotation(instance *cetirunv1alpha1.Agent) {
	if instance.Annotations == nil {
		instance.Annotations = make(map[string]string)
	}
	instance.Annotations[AnnotationSecretUpdatedAt] = time.Now().UTC().Format(time.RFC3339Nano)
}

// createOrUpdateSecret creates an Opaque Secret or updates it if it already exists.
//
// For the "gateway-token" secret, this also ensures an CETIRUN_AUTH_KEY field
// is present (auto-generated 48-char alphanumeric, preserved across updates).
// CETIRUN_AUTH_KEY is the bearer key for the in-pod cetirun-auth helper
// service (Portal "Copilot Auth" UI). The chart cannot manage this field
// itself because Portal passes the secret in as secrets.existingSecret,
// which causes the chart's Secret template to be skipped entirely.
func (c *Client) createOrUpdateSecret(ctx context.Context, ns, name string, data map[string][]byte) error {
	labels := map[string]string{
		"app.kubernetes.io/name":       "cetirun",
		"app.kubernetes.io/managed-by": "cetirun-portal",
	}
	secret := &corev1.Secret{}
	err := c.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, secret)
	if err == nil {
		// Preserve CETIRUN_AUTH_KEY (and any other chart-managed fields the
		// caller didn't supply) by merging caller-supplied data over the
		// in-cluster Secret. Replacing secret.Data outright would rotate the
		// auth key on every update and 401 every running pod's Portal →
		// cetirun-auth call, since env-var-mounted secrets don't refresh.
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		for k, v := range data {
			secret.Data[k] = v
		}
		if err := ensureCetiRunAuthKey(secret, name); err != nil {
			return err
		}
		return c.client.Update(ctx, secret)
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("get secret %s/%s: %w", ns, name, err)
	}
	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			Labels:    labels,
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	if err := ensureCetiRunAuthKey(secret, name); err != nil {
		return err
	}
	return c.client.Create(ctx, secret)
}

// ensureCetiRunAuthKey injects an CETIRUN_AUTH_KEY into the gateway-token
// secret if one isn't already present. Preserves any existing value so that
// subsequent CR/secret updates don't invalidate active Copilot Auth sessions.
func ensureCetiRunAuthKey(secret *corev1.Secret, name string) error {
	if name != "gateway-token" {
		return nil
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	// Preserve existing key set on the in-cluster object.
	if existing, ok := secret.Data["CETIRUN_AUTH_KEY"]; ok && len(existing) > 0 {
		return nil
	}
	key, err := randAlphaNum(48)
	if err != nil {
		return fmt.Errorf("generate CETIRUN_AUTH_KEY: %w", err)
	}
	secret.Data["CETIRUN_AUTH_KEY"] = []byte(key)
	return nil
}

// randAlphaNum returns a cryptographically random alphanumeric string of
// length n, matching the chart-side randAlphaNum helper used elsewhere.
// Returns an error when the system CSPRNG fails — callers must surface that
// rather than fall back to a predictable value, since this string ends up as
// the CETIRUN_AUTH_KEY bearer credential.
func randAlphaNum(n int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, n)
	if _, err := cryptorand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		buf[i] = charset[int(b)%len(charset)]
	}
	return string(buf), nil
}

// GetInstanceByName returns the Agent with the given metadata.name.
// Since Agent is cluster-scoped and metadata.name is globally unique,
// this is a direct Get rather than a list scan.
func (c *Client) GetInstanceByName(ctx context.Context, instanceName string) (*cetirunv1alpha1.Agent, error) {
	instance := &cetirunv1alpha1.Agent{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: instanceName}, instance); err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("instance %q not found", instanceName)
		}
		return nil, err
	}
	return instance, nil
}

// GetInstanceByAlias returns the first Agent whose spec.user.alias matches.
// Used in multi-instance mode where metadata.name != alias.
func (c *Client) GetInstanceByAlias(ctx context.Context, alias string) (*cetirunv1alpha1.Agent, error) {
	list := &cetirunv1alpha1.AgentList{}
	if err := c.client.List(ctx, list); err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].Spec.User.Alias == alias {
			return &list.Items[i], nil
		}
	}
	return nil, errors.NewNotFound(cetirunv1alpha1.GroupVersion.WithResource("agents").GroupResource(), alias)
}

// ListAllInstances returns all Agent CRs in the cluster.
func (c *Client) ListAllInstances(ctx context.Context) ([]cetirunv1alpha1.Agent, error) {
	list := &cetirunv1alpha1.AgentList{}
	if err := c.client.List(ctx, list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// GetOperatorChartVersion returns the --chart-version from the operator Deployment.
// The result (including errors) is cached for 5 minutes to avoid repeated API calls.
func (c *Client) GetOperatorChartVersion(ctx context.Context) (string, error) {
	c.chartVersionMu.RLock()
	if time.Now().Before(c.chartVersionExp) {
		v, err := c.chartVersionVal, c.chartVersionErr
		c.chartVersionMu.RUnlock()
		return v, err
	}
	c.chartVersionMu.RUnlock()

	c.chartVersionMu.Lock()
	defer c.chartVersionMu.Unlock()
	// double-check after acquiring write lock
	if time.Now().Before(c.chartVersionExp) {
		return c.chartVersionVal, c.chartVersionErr
	}

	v, err := c.fetchOperatorChartVersion(ctx)
	c.chartVersionVal = v
	c.chartVersionErr = err
	c.chartVersionExp = time.Now().Add(chartVersionTTL)
	return v, err
}

func (c *Client) fetchOperatorChartVersion(ctx context.Context) (string, error) {
	c.operatorDomain = ""
	deploy := &appsv1.Deployment{}
	key := client.ObjectKey{Namespace: "cetirun-system", Name: "cetirun-operator"}
	if err := c.client.Get(ctx, key, deploy); err != nil {
		return "", fmt.Errorf("get operator deployment: %w", err)
	}
	version := ""
	for _, container := range deploy.Spec.Template.Spec.Containers {
		if container.Name != "manager" {
			continue
		}
		for _, arg := range container.Args {
			if strings.HasPrefix(arg, "--chart-version=") {
				version = strings.TrimPrefix(arg, "--chart-version=")
			}
			if strings.HasPrefix(arg, "--domain-suffix=") {
				c.operatorDomain = strings.TrimPrefix(arg, "--domain-suffix=")
			}
		}
	}
	if version != "" {
		return version, nil
	}
	return "", fmt.Errorf("--chart-version not found in operator deployment args")
}

// Reuse the same cached deployment observation as chart-version discovery.
// The gateway may have a different origin from the Portal itself.
func (c *Client) GetOperatorDomainSuffix(ctx context.Context) (string, error) {
	if _, err := c.GetOperatorChartVersion(ctx); err != nil {
		return "", err
	}
	c.chartVersionMu.RLock()
	domain := c.operatorDomain
	c.chartVersionMu.RUnlock()
	parsed, err := url.Parse("https://" + domain)
	if err != nil || domain == "" || parsed.Host != domain || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("operator domain-suffix is unavailable or invalid")
	}
	return domain, nil
}

// GetEvents returns K8s Events related to an Agent CR.
// Agent is cluster-scoped, so we search all namespaces using a
// raw field selector that the API server natively supports for Events.
func (c *Client) GetEvents(ctx context.Context, alias string) ([]corev1.Event, error) {
	eventList := &corev1.EventList{}
	if err := c.client.List(ctx, eventList, client.MatchingFields{
		"involvedObject.name": alias,
		"involvedObject.kind": "Agent",
	}); err != nil {
		return nil, fmt.Errorf("list events for CR %s: %w", alias, err)
	}
	return eventList.Items, nil
}

// GetNamespaceEvents returns all Events in the given namespace.
func (c *Client) GetNamespaceEvents(ctx context.Context, namespace string) ([]corev1.Event, error) {
	eventList := &corev1.EventList{}
	if err := c.client.List(ctx, eventList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list events in namespace %s: %w", namespace, err)
	}
	return eventList.Items, nil
}

// GetPods returns all Pods in the given namespace.
func (c *Client) GetPods(ctx context.Context, namespace string) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := c.client.List(ctx, podList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list pods in namespace %s: %w", namespace, err)
	}
	return podList.Items, nil
}

// UpgradeInstance sets the instance's spec.helm.version to the operator's latest chart version.
func (c *Client) UpgradeInstance(ctx context.Context, alias string) (*cetirunv1alpha1.Agent, error) {
	latestVersion, err := c.GetOperatorChartVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("get latest chart version: %w", err)
	}
	var result *cetirunv1alpha1.Agent
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		instance, err := c.GetInstance(ctx, alias)
		if err != nil {
			return err
		}
		if instance.Spec.Helm.Version == latestVersion {
			result = instance
			return nil
		}
		instance.Spec.Helm.Version = latestVersion
		if err := c.client.Update(ctx, instance); err != nil {
			return fmt.Errorf("update instance: %w", err)
		}
		result = instance
		return nil
	})
	return result, err
}

// ListTeams returns all Team CRs (cluster-scoped).
func (c *Client) ListTeams(ctx context.Context) ([]cetirunv1alpha1.Team, error) {
	list := &cetirunv1alpha1.TeamList{}
	if err := c.client.List(ctx, list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// GetTeam retrieves a single Team by name (cluster-scoped).
func (c *Client) GetTeam(ctx context.Context, name string) (*cetirunv1alpha1.Team, error) {
	team := &cetirunv1alpha1.Team{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: name}, team); err != nil {
		return nil, err
	}
	return team, nil
}

// CreateTeam creates a new Team.
func (c *Client) CreateTeam(ctx context.Context, team *cetirunv1alpha1.Team) (*cetirunv1alpha1.Team, error) {
	if err := c.client.Create(ctx, team); err != nil {
		return nil, err
	}
	return team, nil
}

// UpdateTeamSpec replaces the Portal-editable fields, preserving governance
// fields from the latest stored Team on every conflict retry.
func (c *Client) UpdateTeamSpec(ctx context.Context, name string, newSpec cetirunv1alpha1.TeamSpec) (*cetirunv1alpha1.Team, error) {
	var result *cetirunv1alpha1.Team
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		team, err := c.GetTeam(ctx, name)
		if err != nil {
			return err
		}
		editable := newSpec.DeepCopy()
		team.Spec.MemberContext = editable.MemberContext
		team.Spec.Defaults = editable.Defaults
		team.Spec.Skills = editable.Skills
		team.Spec.SkillStorage = editable.SkillStorage
		if err := c.client.Update(ctx, team); err != nil {
			return fmt.Errorf("update team: %w", err)
		}
		result = team
		return nil
	})
	return result, err
}

// DeleteTeam deletes a Team by name.
func (c *Client) DeleteTeam(ctx context.Context, name string) error {
	team := &cetirunv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	return c.client.Delete(ctx, team)
}
