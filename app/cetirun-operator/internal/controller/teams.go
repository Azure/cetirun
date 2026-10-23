// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/azure"
)

// reconcileTeams orchestrates the full Teams auto-provisioning lifecycle:
//
//  1. Resolve AAD App (auto-provision with fallback chain)
//  2. Ensure Federated Identity Credential
//  3. Ensure Service Principal
//  4. Ensure Bot Service + Teams Channel
//  5. Update status → Ready
//
// When auto-provisioning fails and no spec.msteams.appId is provided,
// the status is set to PendingAppRegistration.
func (r *AgentReconciler) reconcileTeams(ctx context.Context, instance *cetirunv1alpha1.Agent) error {
	if instance.Spec.MSTeams == nil || !instance.Spec.MSTeams.Enabled {
		// Teams disabled — clean up Azure resources and clear status if previously provisioned.
		if instance.Status.MSTeams != nil {
			r.deleteTeams(ctx, instance)
			instance.Status.MSTeams = nil
			setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionTeamsReady,
				metav1.ConditionFalse, "TeamsDisabled", "Teams integration is not enabled")
		}
		return nil
	}

	logger := log.FromContext(ctx).WithName("teams")
	instanceName := instance.Name
	msteams := instance.Spec.MSTeams

	// Resolve tenant ID: spec overrides operator config.
	tenantID := msteams.TenantID
	if tenantID == "" {
		tenantID = r.AzureTenantID
	}
	if tenantID == "" {
		return r.teamsSetPending(instance, "No tenant ID configured (set spec.msteams.tenantId or operator AZURE_TENANT_ID)")
	}

	// Validate operator-level Azure config.
	if r.AzureSubscriptionID == "" || r.AzureResourceGroup == "" || r.AzureBotNamePrefix == "" {
		return r.teamsSetPending(instance, "Operator Azure config incomplete (AZURE_SUBSCRIPTION_ID, AZURE_RESOURCE_GROUP, AZURE_BOT_NAME_PREFIX required)")
	}

	botName := r.AzureBotNamePrefix + "-" + instanceName
	endpoint := fmt.Sprintf("https://%s/%s/api/messages", r.DomainSuffix, instanceName)
	ficSubject := fmt.Sprintf("system:serviceaccount:cetirun-%s:openclaw-%s-cetirun", instanceName, instanceName)

	// Short-circuit: if Teams is already fully provisioned and the desired
	// inputs haven't changed, skip all Azure API calls. We verify that the
	// resolved appID and tenantID still match the current desired values so
	// that spec changes (which bump CR generation and miss the skip-cache)
	// correctly flow through to EnsureBotService.
	if status := instance.Status.MSTeams; status != nil && status.State == cetirunv1alpha1.TeamsStateReady &&
		status.AppID != "" && status.BotResourceID != "" &&
		status.AppID == resolveDesiredAppID(instance) {
		logger.V(1).Info("Teams already provisioned, skipping Azure API calls",
			"instance", instanceName, "appId", status.AppID, "botResourceId", status.BotResourceID)
		return nil
	}

	// Initialize status if needed.
	if instance.Status.MSTeams == nil {
		instance.Status.MSTeams = &cetirunv1alpha1.MSTeamsStatus{}
	}
	status := instance.Status.MSTeams
	status.State = cetirunv1alpha1.TeamsStateProvisioning

	// Create Azure clients.
	cred, err := r.getAzureCredential()
	if err != nil {
		return fmt.Errorf("acquire Azure credential: %w", err)
	}
	graphClient := azure.NewGraphClient(cred)
	botClient, err := azure.NewBotServiceClient(cred, r.AzureSubscriptionID)
	if err != nil {
		return fmt.Errorf("create bot service client: %w", err)
	}

	// ── Step A: Resolve AAD App (auto-provision with fallback chain) ──
	previousAppObjectID := status.AppObjectID
	appID, appObjectID, err := r.resolveAADApp(ctx, graphClient, instance, botName, logger)
	if err != nil {
		return err
	}
	if appID == "" {
		// PendingAppRegistration already set by resolveAADApp.
		return nil
	}
	status.AppID = appID
	if appObjectID != "" {
		status.AppObjectID = appObjectID
	}

	// Persist status immediately so AppObjectID survives a failed reconcile.
	// This prevents duplicate app creation if later steps (FIC, Bot) fail.
	// Only call updateStatus when AppObjectID was newly set or changed to
	// avoid triggering an infinite reconcile loop.
	if appObjectID != "" && appObjectID != previousAppObjectID {
		if err := r.updateStatus(ctx, instance); err != nil {
			logger.Error(err, "Failed to persist AppObjectID after app resolution")
			return fmt.Errorf("persist Teams status after app resolution: %w", err)
		}
	}

	// ── Orphan cleanup is deferred to after step E (Ready) ──
	// This avoids deleting a previously-working app registration before the
	// new app is fully wired to Bot Service + Teams channel.

	// ── Step B: Ensure FIC (required for the managed-identity Teams auth flow). ──
	if appObjectID != "" {
		if r.AzureOIDCIssuerURL == "" {
			status.State = "Pending"
			status.Message = "Waiting for Azure OIDC issuer URL before creating Federated Identity Credential"
			setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionTeamsReady,
				metav1.ConditionFalse, "FICPending", status.Message)
			err := fmt.Errorf("azure OIDC issuer URL is not configured; cannot create Federated Identity Credential for app %s", appObjectID)
			logger.Error(err, "Missing OIDC issuer URL for FIC", "app", appObjectID)
			r.EventRecorder.Eventf(instance, corev1.EventTypeWarning, "FICPending", "%s", status.Message)
			return err
		}

		if err := r.ensureFIC(ctx, graphClient, appObjectID, tenantID, ficSubject, logger); err != nil {
			status.State = cetirunv1alpha1.TeamsStateFailed
			status.Message = fmt.Sprintf("FIC creation failed: %v", err)
			setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionTeamsReady,
				metav1.ConditionFalse, "FICFailed", status.Message)
			logger.Error(err, "Failed to ensure FIC", "app", appObjectID)
			r.EventRecorder.Eventf(instance, corev1.EventTypeWarning, "FICFailed",
				"Failed to create Federated Identity Credential: %v", err)
			return err
		}
	}

	// ── Step C: Ensure Service Principal ──
	if appObjectID != "" {
		spID, err := r.ensureServicePrincipal(ctx, graphClient, appID, logger)
		if err != nil {
			logger.Error(err, "Failed to ensure service principal", "appId", appID)
			r.EventRecorder.Eventf(instance, corev1.EventTypeWarning, "SPFailed",
				"Failed to create service principal: %v", err)
		} else if spID != "" {
			status.ServicePrincipalID = spID
		}
	}

	// ── Step D: Ensure Bot Service + Teams Channel ──
	botResourceID, err := botClient.EnsureBotService(ctx, r.AzureResourceGroup, botName, endpoint, appID, tenantID)
	if err != nil {
		status.State = cetirunv1alpha1.TeamsStateFailed
		status.Message = fmt.Sprintf("Bot Service creation failed: %v", err)
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionTeamsReady,
			metav1.ConditionFalse, "TeamsProvisioningFailed", status.Message)
		return fmt.Errorf("ensure bot service: %w", err)
	}
	status.BotResourceID = botResourceID

	if err := botClient.EnsureTeamsChannel(ctx, r.AzureResourceGroup, botName); err != nil {
		status.State = cetirunv1alpha1.TeamsStateFailed
		status.Message = fmt.Sprintf("Teams channel creation failed: %v", err)
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionTeamsReady,
			metav1.ConditionFalse, "TeamsProvisioningFailed", status.Message)
		return fmt.Errorf("ensure teams channel: %w", err)
	}

	// ── Step E: Update status → Ready ──
	status.State = cetirunv1alpha1.TeamsStateReady
	status.Message = "AAD app, Bot Service, and Teams channel provisioned"
	setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionTeamsReady,
		metav1.ConditionTrue, "TeamsProvisioned", status.Message)

	// Clean up orphaned AAD apps now that the full pipeline succeeded.
	// Safe to run here because the Bot Service and Teams channel are already
	// wired to the resolved appID.
	if appObjectID != "" {
		r.cleanupOrphanedApps(ctx, graphClient, botName, appObjectID, logger)
	}

	// Update gateway secret with resolved app ID and tenant ID.
	if err := r.updateTeamsSecretKeys(ctx, instance, appID, tenantID); err != nil {
		logger.Error(err, "Failed to update gateway secret with Teams credentials")
	}

	logger.Info("Teams provisioning complete", "instance", instanceName, "botName", botName, "appId", appID)
	r.EventRecorder.Eventf(instance, corev1.EventTypeNormal, "TeamsProvisioned",
		"Teams integration provisioned: bot=%s, appId=%s", botName, appID)
	return nil
}

// resolveAADApp implements the fallback chain for resolving the AAD app:
//
//	A1. If status.msteams.appObjectId already set → verify via GetApp()
//	    - 404 with timer < 15 min → requeue (AAD eventual consistency)
//	    - 404 with timer ≥ 15 min → app genuinely gone, fall through
//	    - Non-404 error → requeue immediately
//	A2. Lookup by displayName → GetAppByDisplayName(botName)
//	    - Error → requeue (never fall through to A3 on error)
//	A3. Try CreateApp(botName, "AzureADMyOrg")
//	A4. Fallback to spec.msteams.appId (no appObjectId → can't manage FIC)
//	A5. Nothing → PendingAppRegistration
//
// Returns (appID, appObjectID, error). If PendingAppRegistration, returns ("", "", nil).
func (r *AgentReconciler) resolveAADApp(ctx context.Context, graphClient *azure.GraphClient, instance *cetirunv1alpha1.Agent, botName string, logger logr.Logger) (string, string, error) {
	status := instance.Status.MSTeams

	// A1: Check cached appObjectId in status.
	if status.AppObjectID != "" {
		app, err := graphClient.GetApp(ctx, status.AppObjectID)
		if err != nil {
			if azure.IsNotFound(err) {
				// 404 — could be AAD eventual consistency (up to ~15 min after creation).
				now := metav1.Now()
				if status.AppNotFoundSince == nil {
					status.AppNotFoundSince = &now
					logger.Info("Cached app returned 404, starting consistency timer", "appObjectId", status.AppObjectID)
					return "", "", fmt.Errorf("cached app %s not found (AAD may still be propagating), will retry", status.AppObjectID)
				}
				elapsed := time.Since(status.AppNotFoundSince.Time)
				if elapsed < 15*time.Minute {
					logger.Info("Cached app still returning 404, waiting for AAD consistency",
						"appObjectId", status.AppObjectID, "elapsed", elapsed.Round(time.Second))
					return "", "", fmt.Errorf("cached app %s not found for %s (AAD may still be propagating), will retry",
						status.AppObjectID, elapsed.Round(time.Second))
				}
				// Timer expired — app is genuinely gone.
				logger.Info("Cached app not found after 15 min, treating as deleted",
					"appObjectId", status.AppObjectID, "elapsed", elapsed.Round(time.Second))
				status.AppObjectID = ""
				status.AppNotFoundSince = nil
			} else {
				// Non-404 error (500, timeout, etc.) — requeue immediately, don't clear anything.
				return "", "", fmt.Errorf("verify cached app %s: %w", status.AppObjectID, err)
			}
		} else if app != nil {
			// Success — clear the not-found timer if it was set.
			status.AppNotFoundSince = nil
			return app.AppID, app.ID, nil
		}
	}

	// A2: Lookup by displayName.
	app, err := graphClient.GetAppByDisplayName(ctx, botName)
	if err != nil {
		// Return error to trigger requeue — never fall through to CreateApp on transient failures.
		return "", "", fmt.Errorf("lookup app by displayName %q: %w", botName, err)
	}
	if app != nil {
		logger.Info("Found existing AAD app by displayName", "displayName", botName, "appId", app.AppID)
		status.AppNotFoundSince = nil
		return app.AppID, app.ID, nil
	}

	// A3: Try auto-create.
	app, err = graphClient.CreateApp(ctx, botName, "AzureADMyOrg", r.AzureServiceTreeID)
	if err != nil {
		logger.Info("Auto-create AAD app failed (may lack permissions)", "error", err, "displayName", botName)
	} else {
		logger.Info("Created new AAD app", "displayName", botName, "appId", app.AppID, "objectId", app.ID)
		r.EventRecorder.Eventf(instance, corev1.EventTypeNormal, "AADAppCreated",
			"Auto-created AAD app %s (appId: %s)", botName, app.AppID)
		status.AppNotFoundSince = nil
		return app.AppID, app.ID, nil
	}

	// A4: Fallback to spec.msteams.appId.
	if instance.Spec.MSTeams.AppID != "" {
		logger.Info("Using fallback appId from spec", "appId", instance.Spec.MSTeams.AppID)
		status.AppNotFoundSince = nil
		return instance.Spec.MSTeams.AppID, "", nil
	}

	// A5: Nothing → PendingAppRegistration.
	return "", "", r.teamsSetPending(instance, fmt.Sprintf(
		"AAD app %q not found; auto-creation failed and no appId in spec. "+
			"Create the app manually and set spec.msteams.appId, or grant the operator Application.ReadWrite.OwnedBy.", botName))
}

// cleanupOrphanedApps deletes any AAD app registrations with the same displayName
// that don't match the resolved appObjectID. This cleans up duplicates created by
// previous bugs where the fallback chain fell through to CreateApp unnecessarily.
// Best-effort: errors are logged but never block reconciliation.
func (r *AgentReconciler) cleanupOrphanedApps(ctx context.Context, graphClient *azure.GraphClient, botName, currentAppObjectID string, logger logr.Logger) {
	apps, err := graphClient.GetAppsByDisplayName(ctx, botName)
	if err != nil {
		logger.Error(err, "Failed to list apps for orphan cleanup (best-effort)", "displayName", botName)
		return
	}
	for _, app := range apps {
		if app.ID == currentAppObjectID {
			continue
		}
		logger.Info("Deleting orphaned AAD app", "displayName", botName, "orphanObjectId", app.ID, "orphanAppId", app.AppID)
		if err := graphClient.DeleteApp(ctx, app.ID); err != nil {
			logger.Error(err, "Failed to delete orphaned AAD app (best-effort)", "objectId", app.ID)
		}
	}
}

// teamsSetPending sets the Teams status to PendingAppRegistration.
func (r *AgentReconciler) teamsSetPending(instance *cetirunv1alpha1.Agent, message string) error {
	if instance.Status.MSTeams == nil {
		instance.Status.MSTeams = &cetirunv1alpha1.MSTeamsStatus{}
	}
	instance.Status.MSTeams.State = cetirunv1alpha1.TeamsStatePendingAppRegistration
	instance.Status.MSTeams.Message = message
	setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionTeamsReady,
		metav1.ConditionFalse, "PendingAppRegistration", message)
	return nil // not an error — caller should return nil to avoid requeue
}

// ensureFIC ensures a Federated Identity Credential exists on the AAD app
// for the per-user Kubernetes service account.
func (r *AgentReconciler) ensureFIC(ctx context.Context, graphClient *azure.GraphClient, appObjectID, tenantID, ficSubject string, logger logr.Logger) error {
	// Check if FIC already exists.
	existing, err := graphClient.ListFederatedCredentials(ctx, appObjectID)
	if err != nil {
		return fmt.Errorf("list FICs: %w", err)
	}
	for _, fic := range existing {
		if fic.Subject == ficSubject {
			logger.V(1).Info("FIC already exists", "subject", ficSubject)
			return nil
		}
	}

	// Look up the FIC issuer ID for this tenant.
	ficIssuerBindURL := azure.FICIssuerBindURL(tenantID)
	if ficIssuerBindURL == "" {
		return fmt.Errorf("no FIC issuer ID for tenant %s (not in allowlist)", tenantID)
	}

	// Ensure OIDC issuer URL has trailing slash (required by the FIC API).
	issuer := r.AzureOIDCIssuerURL
	if !strings.HasSuffix(issuer, "/") {
		issuer += "/"
	}

	ficName := fmt.Sprintf("cetirun-%s", strings.ReplaceAll(ficSubject, ":", "-"))
	// Truncate to 120 chars max (FIC name limit).
	if len(ficName) > 120 {
		ficName = ficName[:120]
	}

	req := azure.FICCreateRequest{
		Name:                   ficName,
		TrustedIssuerODataBind: ficIssuerBindURL,
		Issuer:                 issuer,
		Subject:                ficSubject,
		Audiences:              []string{"api://AzureADTokenExchange"},
	}

	if err := graphClient.CreateFederatedCredential(ctx, appObjectID, req); err != nil {
		if azure.IsConflict(err) {
			logger.V(1).Info("FIC already exists (conflict)", "subject", ficSubject)
			return nil
		}
		return fmt.Errorf("create FIC: %w", err)
	}
	logger.Info("Created FIC", "subject", ficSubject, "app", appObjectID)
	return nil
}

// ensureServicePrincipal ensures a service principal exists for the app.
// Returns the SP object ID, or empty string on failure.
func (r *AgentReconciler) ensureServicePrincipal(ctx context.Context, graphClient *azure.GraphClient, appID string, logger logr.Logger) (string, error) {
	sp, err := graphClient.CreateServicePrincipal(ctx, appID)
	if err != nil {
		if azure.IsConflict(err) {
			// SP already exists — look it up to get the ID.
			existing, lookupErr := graphClient.GetServicePrincipalByAppID(ctx, appID)
			if lookupErr != nil {
				logger.Error(lookupErr, "SP exists but failed to look up ID", "appId", appID)
				return "", nil
			}
			if existing != nil {
				logger.V(1).Info("Service principal already exists", "appId", appID, "spId", existing.ID)
				return existing.ID, nil
			}
			return "", nil
		}
		return "", fmt.Errorf("create service principal: %w", err)
	}
	logger.Info("Ensured service principal", "appId", appID, "spId", sp.ID)
	return sp.ID, nil
}

// updateTeamsSecretKeys updates the gateway secret with the resolved Teams credentials.
func (r *AgentReconciler) updateTeamsSecretKeys(ctx context.Context, instance *cetirunv1alpha1.Agent, appID, tenantID string) error {
	ns := instanceNamespace(instance.Name)
	secretName := instance.Spec.GatewayTokenSecretRef

	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: ns, Name: secretName}
	if err := r.Get(ctx, key, secret); err != nil {
		return fmt.Errorf("get gateway secret: %w", err)
	}

	needsUpdate := false
	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	if string(secret.Data["MSTEAMS_APP_ID"]) != appID {
		secret.Data["MSTEAMS_APP_ID"] = []byte(appID)
		needsUpdate = true
	}
	if string(secret.Data["MSTEAMS_TENANT_ID"]) != tenantID {
		secret.Data["MSTEAMS_TENANT_ID"] = []byte(tenantID)
		needsUpdate = true
	}
	if !needsUpdate {
		return nil
	}
	return r.Update(ctx, secret)
}

// deleteTeams performs best-effort cleanup of all Teams-related Azure resources.
// 404 errors are ignored; other errors are logged but never block CR deletion.
func (r *AgentReconciler) deleteTeams(ctx context.Context, instance *cetirunv1alpha1.Agent) {
	if instance.Status.MSTeams == nil {
		return
	}

	logger := log.FromContext(ctx).WithName("teams-delete")
	instanceName := instance.Name
	botName := r.AzureBotNamePrefix + "-" + instanceName
	status := instance.Status.MSTeams

	// Skip if no Azure config.
	if r.AzureSubscriptionID == "" || r.AzureResourceGroup == "" {
		logger.Info("Skipping Teams cleanup (no Azure config)")
		return
	}

	cred, err := r.getAzureCredential()
	if err != nil {
		logger.Error(err, "Failed to acquire Azure credential for Teams cleanup")
		return
	}

	// 1. Delete Teams channel.
	botClient, err := azure.NewBotServiceClient(cred, r.AzureSubscriptionID)
	if err != nil {
		logger.Error(err, "Failed to create bot service client for cleanup")
		return
	}
	if err := botClient.DeleteTeamsChannel(ctx, r.AzureResourceGroup, botName); err != nil {
		logger.Error(err, "Failed to delete Teams channel (best-effort)", "bot", botName)
	}

	// 2. Delete Bot Service.
	if err := botClient.DeleteBotService(ctx, r.AzureResourceGroup, botName); err != nil {
		logger.Error(err, "Failed to delete Bot Service (best-effort)", "bot", botName)
	}

	graphClient := azure.NewGraphClient(cred)

	// 3. Delete Service Principal.
	spID := status.ServicePrincipalID
	if spID == "" && status.AppID != "" {
		// ServicePrincipalID may not have been recorded (e.g., 409 conflict before
		// the lookup fix). Look it up by appId.
		sp, lookupErr := graphClient.GetServicePrincipalByAppID(ctx, status.AppID)
		if lookupErr != nil {
			logger.Error(lookupErr, "Failed to look up service principal by appId (best-effort)", "appId", status.AppID)
		} else if sp != nil {
			spID = sp.ID
		}
	}
	if spID != "" {
		if err := graphClient.DeleteServicePrincipal(ctx, spID); err != nil {
			logger.Error(err, "Failed to delete service principal (best-effort)", "spId", spID)
		} else {
			logger.Info("Deleted service principal", "spId", spID)
		}
	}

	// 4. Delete AAD App (if we have the object ID, meaning operator created it).
	if status.AppObjectID != "" {
		if err := graphClient.DeleteApp(ctx, status.AppObjectID); err != nil {
			logger.Error(err, "Failed to delete AAD app (best-effort)", "objectId", status.AppObjectID)
		} else {
			logger.Info("Deleted AAD app", "objectId", status.AppObjectID)
		}
	}

	// 5. Clean up any orphaned AAD apps with the same displayName.
	// Only run when we know the current app object ID; otherwise every app
	// with this displayName would be treated as an orphan.
	if r.AzureBotNamePrefix != "" && status.AppObjectID != "" {
		r.cleanupOrphanedApps(ctx, graphClient, botName, status.AppObjectID, logger)
	}

	logger.Info("Teams cleanup complete", "instance", instanceName, "botName", botName)
}

// resolveDesiredAppID returns the app ID that the operator would use for this
// instance: status.AppID (auto-provisioned) takes precedence over spec.AppID.
// Used by the short-circuit check to detect when the desired app ID has changed.
func resolveDesiredAppID(instance *cetirunv1alpha1.Agent) string {
	if instance.Status.MSTeams != nil && instance.Status.MSTeams.AppID != "" {
		return instance.Status.MSTeams.AppID
	}
	if instance.Spec.MSTeams != nil {
		return instance.Spec.MSTeams.AppID
	}
	return ""
}

// configHash returns the operator config hash including Azure Teams config.
// This ensures Teams-related operator config changes (subscription, resource
// group, bot name prefix, tenant, OIDC issuer) invalidate the skip-cache,
// which allows Teams-enabled instances to use the same skip-cache as all
// other instances. teamRV is the referenced Team's ResourceVersion
// (empty when no team is referenced) — mixing it in ensures team edits
// (defaults / memberContext / skills / skillStorage) invalidate the cache so
// the watch-driven re-enqueue actually re-runs buildHelmValues with the new
// team data instead of being short-circuited.
const agentIdentityRuntimeRevision = "native-mcp-v1"

func (r *AgentReconciler) configHash(instance *cetirunv1alpha1.Agent, team *cetirunv1alpha1.Team, gwSecretHash, teamRV string) string {
	config := []string{
		r.AzureSubscriptionID, r.AzureResourceGroup, r.AzureBotNamePrefix,
		r.AzureTenantID, r.AzureOIDCIssuerURL,
	}
	if enabled, err := r.toolsMCPEnabledFor(instance); err != nil {
		// Reconcile rejects invalid allowlists before this hash is used. Retaining
		// their raw value here still prevents an invalid configuration from sharing
		// a skip-cache entry with a valid one.
		config = append(config, "invalid-tools-mcp-runtime-types", strings.Join(r.ToolsMCPRuntimeTypes, ","))
	} else if enabled {
		runtimeTypes, _ := normalizeToolsMCPRuntimeTypes(r.ToolsMCPRuntimeTypes)
		config = append(config, r.ToolsMCPURL, r.ToolsMCPAudience, r.ToolsMCPCASecretName,
			string(instance.Spec.Agent.Type), strings.Join(runtimeTypes, ","), agentIdentityRuntimeRevision)
	}
	// Preserve the existing hash for deployments and Agents that do not use
	// Agent Identity. This prevents an unrelated operator upgrade from rolling
	// every workload when the feature remains disabled.
	if r.AgentIdentityEnabled || agentIdentityRequested(instance) || instance.Status.AgentIdentity != nil {
		config = append(config, r.AgentIdentityTenantID, r.AgentIdentityBlueprintClientID,
			r.AgentIdentityNamePrefix, r.AgentIdentityBrokerURL)
	}
	if agentIdentityRequested(instance) || instance.Status.AgentIdentity != nil {
		config = append(config, agentIdentityRuntimeRevision)
	}
	// A configured default affects only GitHub Copilot instances without a
	// per-instance model. Include it only for those instances so changing the
	// default does not restart explicitly configured or non-Copilot instances.
	if defaultModel := strings.TrimSpace(r.DefaultModel); defaultModel != "" && r.usesDeploymentDefaultModel(instance, team) {
		config = append(config, defaultModel)
	}
	if revision := r.aiGatewayRevision(instance); revision != "" {
		config = append(config, revision)
	}
	config = append(config, teamRV)
	if r.MemoryEnabled || r.MemoryImage != "" {
		config = append(config, "memory-context/v2", r.MemoryImage, r.MemoryRegistry)
	}
	// Older controllers can acknowledge a generation without applying its restart.
	// Bind both the steady-state and Helm-apply caches to the requested intent.
	if instance.Spec.RestartRequestID != "" {
		config = append(config, "restart-request/v1", instance.Spec.RestartRequestID)
	}
	if request := instance.Spec.PlannedMigration; request != nil {
		config = append(config, "planned-migration/v1", request.RequestID, request.Target.Name, string(request.Target.UID))
	}
	return operatorConfigHash(
		r.DomainSuffix, gwSecretHash, r.SkillsStorageAccount,
		hashCetiRunHelmValues(r.CetiRunHelmValues), r.ChartRegistry,
		config...,
	)
}

// usesDeploymentDefaultModel reports whether buildHelmValues will render the
// operator's default model. Keep the provider precedence in sync with that
// function: operator Helm values, then the team, then instance Helm values.
func (r *AgentReconciler) usesDeploymentDefaultModel(instance *cetirunv1alpha1.Agent, team *cetirunv1alpha1.Team) bool {
	if strings.TrimSpace(instance.Spec.Model) != "" {
		return false
	}
	provider := "github_copilot"
	applyProvider := func(value interface{}) {
		secrets, ok := value.(map[string]interface{})
		if !ok {
			return
		}
		if configured, ok := secrets["provider"].(string); ok && strings.TrimSpace(configured) != "" {
			provider = strings.TrimSpace(configured)
		}
	}
	applyProvider(r.CetiRunHelmValues["secrets"])
	if team != nil && strings.TrimSpace(team.Spec.Defaults.Provider) != "" {
		provider = strings.TrimSpace(team.Spec.Defaults.Provider)
	}
	if raw, ok := instance.Spec.Helm.Values["secrets"]; ok && raw.Raw != nil {
		var values map[string]interface{}
		if err := json.Unmarshal(raw.Raw, &values); err == nil {
			applyProvider(values)
		}
	}
	return provider == "github_copilot"
}

// getAzureCredential returns a DefaultAzureCredential for Azure API calls.
func (r *AgentReconciler) getAzureCredential() (azcore.TokenCredential, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("create DefaultAzureCredential: %w", err)
	}
	return cred, nil
}
