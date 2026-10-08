// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/go-logr/logr"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/agents/builtin"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/internal/chataccess"
	"github.com/Azure/CetiRun/app/internal/registryauth"
)

// AgentReconciler reconciles Agent objects.
//
// +kubebuilder:rbac:groups=cetirun.microsoft.com,resources=agents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cetirun.microsoft.com,resources=agents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cetirun.microsoft.com,resources=agents/finalizers,verbs=update
// +kubebuilder:rbac:groups=cetirun.microsoft.com,resources=teams,verbs=get;list;watch
// +kubebuilder:rbac:groups=cetirun.microsoft.com,resources=teams/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cetirun.microsoft.com,resources=sandboxes;agentpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=cetirun.microsoft.com,resources=sandboxes/status;agentpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=node.k8s.io,resources=runtimeclasses,verbs=get
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;delete
// +kubebuilder:rbac:groups=apps,resources=controllerrevisions,verbs=get
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.istio.io,resources=virtualservices;sidecars,verbs=get;list;watch;create;update;patch;delete
type AgentReconciler struct {
	client.Client
	APIReader                   client.Reader
	Scheme                      *runtime.Scheme
	EventRecorder               record.EventRecorder
	HelmClient                  HelmInstaller
	OperatorNamespace           string
	OperatorSAName              string
	ChartVersion                string
	IstioRevision               string // explicit override; if empty, auto-detected from aks-istio-system
	IstioGateway                string // namespace/name of the Gateway used by instance VirtualServices
	DomainSuffix                string
	SkillsStorageAccount        string
	CetiRunHelmValues           map[string]interface{} // Operator-level Helm value overrides for all instances
	ChartRegistry               string                 // OCI registry host (e.g. "registry.example.com"), derived from chart repo URL
	DefaultModel                string                 // Default GitHub Copilot model when spec.model is empty
	CustomBindings              CustomBindingProvider
	ChatAccess                  chataccess.Config
	SandboxPreparation          *SandboxPreparation
	RegistryCredentialNamespace string

	// Azure config for Teams auto-provisioning.
	AzureSubscriptionID string
	AzureResourceGroup  string
	AzureBotNamePrefix  string
	AzureTenantID       string
	AzureServiceTreeID  string // optional serviceManagementReference for app creation
	AzureOIDCIssuerURL  string // auto-detected from cluster at startup

	// Microsoft Entra Agent Identity deployment configuration.
	AgentIdentityEnabled           bool
	AgentIdentityTenantID          string
	AgentIdentityBlueprintClientID string
	AgentIdentityNamePrefix        string
	AgentIdentityBrokerURL         string
	AgentIdentityHTTPClient        *http.Client
	// agentIdentityGraphClientFactory is test-only dependency injection. A nil
	// factory constructs the deployment's workload-identity-backed Graph client.
	agentIdentityGraphClientFactory func() (agentIdentityDirectoryClient, error)

	// AI Gateway deployment configuration for per-Agent virtual keys.
	AIGateway           AIGatewayConfig
	AIGatewayHTTPClient *http.Client
	// aiGatewayClientFactory is test-only dependency injection. A nil factory
	// reads the master key Secret and calls the configured Gateway.
	aiGatewayClientFactory func(context.Context) (aiGatewayKeyClient, error)
	// aiGatewayCatalog is the operator's view of the Gateway's models, kept
	// fresh by reconciles and the catalog publisher.
	aiGatewayCatalog aiGatewayCatalog
	aiGatewayOwners  aiGatewayOwnerCache
	aiGatewayPrices  aiGatewayPriceCache
	// aiGatewayEvents queues Agents after a shared-model change.
	aiGatewayEvents chan event.GenericEvent

	istioMu          sync.Mutex
	istioRevResolved string
	customPlans      sync.Map
}

// SetupWithManager registers the reconciler with the controller manager.
// instanceTeamIndex is the field index used to look up Agents by
// spec.team without scanning the full cluster on every team event.
const instanceTeamIndex = "spec.team"

const gatewaySecretResourceVersionAnnotation = "cetirun.microsoft.com/gateway-secret-resource-version"

func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &cetirunv1alpha1.Agent{}, registrySourceIndex, func(obj client.Object) []string {
		agent := obj.(*cetirunv1alpha1.Agent)
		if agent.Spec.Agent.RegistrySecretRef == nil {
			return nil
		}
		return []string{agent.Spec.Agent.RegistrySecretRef.Name}
	}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&cetirunv1alpha1.Agent{},
		instanceTeamIndex,
		func(obj client.Object) []string {
			inst, ok := obj.(*cetirunv1alpha1.Agent)
			if !ok || inst.Spec.Team == "" {
				return nil
			}
			return []string{inst.Spec.Team}
		},
	); err != nil {
		return fmt.Errorf("index instance by spec.team: %w", err)
	}
	if err := indexSandboxDependencies(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return err
	}
	builder := ctrl.NewControllerManagedBy(mgr).
		For(&cetirunv1alpha1.Agent{}).
		Owns(&appsv1.Deployment{}).
		Owns(&istiov1beta1.VirtualService{}).
		WatchesMetadata(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.enqueueRegistrySecret)).
		Watches(
			&cetirunv1alpha1.Team{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueInstancesForTeam),
		).
		Watches(
			&cetirunv1alpha1.Sandbox{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueInstancesForSandbox),
		).
		Watches(
			&cetirunv1alpha1.AgentPolicy{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueInstancesForAgentPolicy),
		)
	if r.AIGateway.Enabled {
		if r.aiGatewayEvents == nil {
			r.aiGatewayEvents = make(chan event.GenericEvent, 1024)
		}
		builder = builder.WatchesRawSource(source.Channel(r.aiGatewayEvents, &handler.EnqueueRequestForObject{}))
		if err := mgr.Add(&AIGatewayCatalogPublisher{Reconciler: r}); err != nil {
			return fmt.Errorf("add AI Gateway catalog publisher: %w", err)
		}
	}
	return builder.
		WithOptions(crcontroller.Options{MaxConcurrentReconciles: 4}).
		Complete(r)
}

// Reconcile implements the reconciliation loop.
func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("name", req.Name)

	// 1. Fetch the CR.
	instance := &cetirunv1alpha1.Agent{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		if errors.IsNotFound(err) {
			r.customPlans.Delete(req.Name)
			logger.V(1).Info("Agent not found, skipping")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get Agent: %w", err)
	}

	if ns := instanceNamespace(instance.Name); ns == r.registryNamespace() || ns == registryauth.DefaultNamespace {
		return r.rejectReservedRegistryInstance(ctx, instance)
	}

	// 2. Handle deletion.
	if !instance.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(instance, sandbox.PreparationFinalizer) ||
			controllerutil.ContainsFinalizer(instance, sandbox.BootstrapFinalizer) {
			return r.deleteSandboxBuiltin(ctx, instance)
		}
		if instance.Spec.Agent.Type == cetirunv1alpha1.AgentTypeCustom {
			return r.deleteCustom(ctx, instance)
		}
		if sandboxRequested(instance, nil) {
			return r.deleteSandboxBuiltin(ctx, instance)
		}
		if err := r.cleanupRegistryCredentials(ctx, instance, true); err != nil {
			return r.registryWaiting(ctx, instance, "RegistryCleanupPending")
		}
		logger.V(1).Info("DeletionTimestamp set, entering delete handler", "alias", instance.Spec.User.Alias)
		return r.handleDelete(ctx, instance)
	}

	// "ui" is the top-level namespace for native agent routes (/ui/<instance>/).
	// It must never remain an externally routed instance name: otherwise a legacy
	// /ui/... VirtualService can overlap native routes and route an authorized
	// request for one instance to the "ui" workload. This is intentionally a
	// breaking change; no migration path exists for the reserved legacy name.
	if instance.Name == "ui" {
		return r.rejectReservedUIInstance(ctx, instance)
	}

	if sandboxRequested(instance, nil) {
		return r.holdSandbox(ctx, instance)
	}

	// Inspect Team governance before runtime recovery can retain a Ready state.
	// Lookup errors are handled below so Custom suspension/recovery stays available.
	var team *cetirunv1alpha1.Team
	var teamErr error
	if instance.Spec.Team != "" {
		team = &cetirunv1alpha1.Team{}
		teamErr = r.Get(ctx, types.NamespacedName{Name: instance.Spec.Team}, team)
	}
	if teamErr == nil && sandboxRequested(instance, team) {
		return r.holdSandbox(ctx, instance)
	}

	if instance.Spec.Agent.Type == cetirunv1alpha1.AgentTypeCustom && instance.Spec.Suspended {
		return r.suspendCustom(ctx, instance)
	}
	if instance.Spec.Agent.Type != cetirunv1alpha1.AgentTypeCustom && instance.Spec.Suspended && registryManaged(instance) {
		return r.suspendRegistryBuiltin(ctx, instance)
	}
	if instance.Spec.Agent.Type == cetirunv1alpha1.AgentTypeCustom {
		if err := r.retryCustomCompletion(ctx, instance); err != nil {
			return r.customWaiting(ctx, instance, "CustomCompletionPending", "Waiting to acknowledge the previous apply outcome")
		}
	}
	if err := r.removeRegistryCredentials(ctx, instance); err != nil {
		return r.registryWaiting(ctx, instance, "RegistryCleanupPending")
	}

	if err := validateAgentRuntime(instance.Spec.Agent); err != nil {
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionAgentConfigReady,
			metav1.ConditionFalse, "AgentRuntimeNotImplemented", err.Error())
		return r.failWith(ctx, instance, "AgentRuntimeNotImplemented", err.Error())
	}
	if condition := meta.FindStatusCondition(instance.Status.Conditions, cetirunv1alpha1.ConditionAgentConfigReady); condition != nil &&
		condition.Reason == "AgentRuntimeNotImplemented" {
		meta.RemoveStatusCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionAgentConfigReady)
	}

	// 3. Ensure finalizer.
	if !controllerutil.ContainsFinalizer(instance, cetirunv1alpha1.Finalizer) {
		logger.V(1).Info("Adding finalizer", "alias", instance.Spec.User.Alias)
		before := instance.DeepCopy()
		controllerutil.AddFinalizer(instance, cetirunv1alpha1.Finalizer)
		if err := r.Patch(ctx, instance, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 3a. Backfill spec.helm.version if empty.
	// This pins the chart version on the CR so that future operator upgrades
	// do not automatically roll out a new chart (avoiding pod restarts).
	// Governed Agents stop at holdSandbox above. Before enabling that path,
	// authorize version/image/port pinning narrowly; admission rejects controller
	// spec writes and must not gain a blanket controller exemption.
	if instance.Spec.Helm.Version == "" {
		before := instance.DeepCopy()
		instance.Spec.Helm.Version = r.ChartVersion
		if err := r.Patch(ctx, instance, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, fmt.Errorf("backfill helm version: %w", err)
		}
		// A spec patch bumps Generation; re-queue to pick up the new value.
		return ctrl.Result{Requeue: true}, nil
	}

	// 3b. Require the referenced Team before workload preparation.
	if teamErr != nil {
		if errors.IsNotFound(teamErr) {
			return r.failWith(ctx, instance,
				"TeamNotFound",
				fmt.Sprintf("referenced Team %q not found", instance.Spec.Team))
		}
		return ctrl.Result{}, fmt.Errorf("get Team %s: %w", instance.Spec.Team, teamErr)
	}

	if instance.Spec.Agent.Type == cetirunv1alpha1.AgentTypeCustom {
		return r.reconcileCustom(ctx, instance, team)
	}

	// Persist the Agent Identity provisioning intent before any workload
	// preparation. This preserves a canonical binding and create checkpoint
	// across generation changes and transient downstream failures.
	if agentIdentityRequested(instance) && (instance.Status.AgentIdentity == nil || instance.Status.AgentIdentity.ObservedGeneration != instance.Generation) {
		status := ensureAgentIdentityStatus(instance)
		status.Phase = cetirunv1alpha1.AgentIdentityPhaseProvisioning
		status.ObservedGeneration = instance.Generation
		status.Reason = ""
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionAgentIdentityReady,
			metav1.ConditionFalse, "Provisioning", "Agent Identity provisioning intent is recorded")
		if err := r.updateStatus(ctx, instance); err != nil {
			return ctrl.Result{}, fmt.Errorf("persist Agent Identity provisioning intent: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if instance.Spec.Agent.RegistrySecretRef != nil && instance.Spec.Agent.Image != nil && !instance.Spec.Suspended {
		// Credential rotation must not pull the runtime chart or enter Helm.
		values, err := buildHelmValues(instance, team, r.DomainSuffix, r.SkillsStorageAccount, r.CetiRunHelmValues, r.ChartRegistry, r.DefaultModel)
		if err != nil {
			return r.registryWaiting(ctx, instance, "RegistryRuntimeConfigRejected")
		}
		sa, err := registryServiceAccount(values, instanceServiceName(instance.Name))
		if err != nil {
			return r.registryWaiting(ctx, instance, "RegistryIsolationUnverified")
		}
		if _, err := r.syncRegistryCredentials(ctx, instance, sa); err != nil {
			return r.registryWaiting(ctx, instance, err.Error())
		}
	}

	var prepared *helm.PreparedRelease
	if instance.Spec.Agent.Image == nil || instance.Spec.Agent.Port == nil {
		var err error
		prepared, err = r.prepareAgent(instance, team)
		if err != nil {
			return r.rejectAgentConfig(ctx, instance, err)
		}
		before := instance.DeepCopy()
		if pinAgentDefaults(&instance.Spec.Agent, prepared.Agent) {
			if err := r.Patch(ctx, instance, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
				return ctrl.Result{}, fmt.Errorf("pin agent defaults: %w", err)
			}
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// 4. If already Ready and nothing has changed, skip the full reconcile.
	// This prevents a status-update loop: LastReconcileTime always differs,
	// which triggers a new watch event, which triggers another reconcile.
	// We read the gateway Secret content hash here so that external Secret
	// modifications (token rotation, tailscale key update) invalidate the
	// skip cache and trigger a full reconcile with Helm upgrade.
	gwSecretHash := ""
	gatewaySecretResourceVersion := ""
	if instance.Status.Phase == cetirunv1alpha1.PhaseReady || instance.Status.Phase == cetirunv1alpha1.PhaseSuspended {
		gwSecret := &corev1.Secret{}
		gwSecretKey := types.NamespacedName{
			Namespace: instanceNamespace(instance.Name),
			Name:      instance.Spec.GatewayTokenSecretRef,
		}
		if err := r.Get(ctx, gwSecretKey, gwSecret); err != nil {
			if !errors.IsNotFound(err) {
				// Transient or RBAC error — requeue instead of silently falling back
				// to an empty hash which would spuriously invalidate the skip cache.
				logger.Error(err, "failed to read gateway Secret for skip-cache hash", "alias", instance.Spec.User.Alias)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			// NotFound: Secret doesn't exist yet, leave gwSecretHash empty.
		} else {
			// Do this before evaluating the skip-cache so existing instances created
			// before CETIRUN_AUTH_KEY was introduced are upgraded even when their
			// previously observed Secret hash still matches.
			if err := r.ensureCetiRunAuthKey(ctx, logger, gwSecret); err != nil {
				logger.Error(err, "failed to backfill CetiRun auth key before skip-cache", "alias", instance.Spec.User.Alias)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			gwSecretHash = secretContentHash(gwSecret.Data)
		}

		identityVerificationDue := agentIdentityVerificationDue(instance, time.Now())
		aiGatewayDue := r.aiGatewayReconcileDue(instance, time.Now())
		if instance.Status.ObservedGeneration == instance.Generation &&
			!identityVerificationDue && !aiGatewayDue &&
			instance.Status.ObservedOperatorHash == r.configHash(instance, team, gwSecretHash, teamResourceVersion(team)) {
			// Lightweight: ensure VirtualService is up to date (may change after operator upgrade).
			// Update ConditionIngressReady so CR status reflects the actual VS state.
			// When suspended, ensure VirtualService is absent (retries deletion if
			// a previous reconcile partially failed). When not suspended, ensure
			// VirtualService is up to date (may change after operator upgrade).
			bypassSkipCache := false
			statusChanged := false
			// The canonical native UI route is controller-owned state. Keep it
			// current even when the instance otherwise qualifies for the lightweight
			// reconcile path after an operator upgrade.
			if instance.Status.Phase == cetirunv1alpha1.PhaseReady {
				canonicalAccessURL := accessURLPathBased(instance.Name, r.DomainSuffix)
				if instance.Status.AccessURL != canonicalAccessURL {
					instance.Status.AccessURL = canonicalAccessURL
					statusChanged = true
				}
			}
			if instance.Status.Phase == cetirunv1alpha1.PhaseSuspended {
				vsName := virtualServiceName(instance.Name)
				nsCheck := instanceNamespace(instance.Name)
				vs := &istiov1beta1.VirtualService{}
				vsKey := types.NamespacedName{Name: vsName, Namespace: nsCheck}
				if err := r.Get(ctx, vsKey, vs); err == nil {
					// VS still exists — bypass skip-cache so the full suspend path re-runs.
					logger.Info("VirtualService still exists for suspended instance, forcing full reconcile", "alias", instance.Spec.User.Alias, "vs", vsName)
					bypassSkipCache = true
				} else if !errors.IsNotFound(err) {
					logger.Error(err, "failed to check VirtualService for suspended instance", "alias", instance.Spec.User.Alias)
				}
			} else {
				prevIngressReady := isConditionTrue(instance.Status.Conditions, cetirunv1alpha1.ConditionIngressReady)
				if err := r.ensureVirtualService(ctx, instance); err != nil {
					logger.Error(err, "VirtualService update failed (lightweight reconcile)", "alias", instance.Spec.User.Alias)
					setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionIngressReady,
						metav1.ConditionFalse, "VirtualServiceFailed", err.Error())
					statusChanged = statusChanged || prevIngressReady
				} else {
					setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionIngressReady,
						metav1.ConditionTrue, "VirtualServiceCreated", "Istio ingress configured")
					statusChanged = statusChanged || !prevIngressReady
				}
			}
			if !bypassSkipCache {
				if statusChanged {
					if err := r.updateStatus(ctx, instance); err != nil {
						return ctrl.Result{}, fmt.Errorf("update steady-state status: %w", err)
					}
				}
				logger.V(1).Info("Instance already at steady state with no changes, skipping reconcile", "alias", instance.Spec.User.Alias, "phase", instance.Status.Phase)
				return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
			}
		}
	}

	if prepared == nil {
		var err error
		prepared, err = r.prepareAgent(instance, team)
		if err != nil {
			return r.rejectAgentConfig(ctx, instance, err)
		}
	}

	// 5. Set phase to Provisioning if still Pending (or empty).
	if instance.Status.Phase == "" || instance.Status.Phase == cetirunv1alpha1.PhasePending {
		logger.V(1).Info("Setting phase to Provisioning", "alias", instance.Spec.User.Alias, "currentPhase", instance.Status.Phase)
		if err := r.setPhase(ctx, instance, cetirunv1alpha1.PhaseProvisioning); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 5a. Transition Ready/Suspended → Provisioning when a full reconcile is needed.
	// If we reach this point while Ready or Suspended, the skip-cache (step 4)
	// determined that generation or operator config has changed, so an upgrade
	// will happen. We only set the phase in-memory here; it will be persisted
	// by the next updateStatus call (after Helm install or on error), avoiding
	// an extra status write that would trigger a redundant reconcile.
	if instance.Status.Phase == cetirunv1alpha1.PhaseReady || instance.Status.Phase == cetirunv1alpha1.PhaseSuspended {
		instance.Status.Phase = cetirunv1alpha1.PhaseProvisioning
	}

	alias := instance.Spec.User.Alias
	ns := instanceNamespace(instance.Name)
	releaseName := helmReleaseName(instance.Name)

	logger.Info("Reconciling Agent", "alias", alias, "generation", instance.Generation)
	r.EventRecorder.Eventf(instance, corev1.EventTypeNormal, "InstanceCreating",
		"Creating agent for user %s", alias)

	// 6. Ensure namespace.
	logger.V(1).Info("Ensuring namespace", "namespace", ns)
	if err := r.ensureNamespace(ctx, ns, alias, instance); err != nil {
		return r.failWith(ctx, instance, "ProvisioningFailed", fmt.Sprintf("ensure namespace: %v", err))
	}

	// 6a. Ensure namespaced Role + RoleBinding so the operator's access to Secrets,
	// ConfigMaps, Services, StatefulSets, PVCs, and ServiceAccounts is scoped to
	// this namespace only, rather than cluster-wide.
	logger.V(1).Info("Ensuring namespaced RBAC", "namespace", ns)
	if err := r.ensureNamespacedRBAC(ctx, ns); err != nil {
		return r.failWith(ctx, instance, "ProvisioningFailed", fmt.Sprintf("ensure namespaced RBAC: %v", err))
	}

	// 7. Ensure gateway token secret.
	logger.V(1).Info("Ensuring gateway token secret", "alias", alias)
	if err := r.ensureGatewayToken(ctx, instance); err != nil {
		logger.Error(err, "failed to ensure gateway token", "alias", alias)
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionReady,
			metav1.ConditionFalse, "GatewayTokenError", err.Error())
		_ = r.updateStatus(ctx, instance)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// 8. Ensure Tailscale auth key is in the gateway token Secret (if configured).
	// The operator uses secrets.existingSecret, so the chart's Secret template is skipped.
	// We must patch the auth key directly into the gateway token Secret for the StatefulSet
	// to find TAILSCALE_AUTH_KEY via its secretKeyRef.
	if err := r.ensureTailscaleAuthKey(ctx, instance); err != nil {
		logger.Error(err, "failed to ensure Tailscale auth key", "alias", alias)
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionTailscaleReady,
			metav1.ConditionFalse, "TailscaleAuthKeyFailed", err.Error())
		_ = r.updateStatus(ctx, instance)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if instance.Spec.Tailscale != nil {
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionTailscaleReady,
			metav1.ConditionTrue, "TailscaleAuthKeyReady", "Tailscale auth key written to gateway Secret")
	} else {
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionTailscaleReady,
			metav1.ConditionFalse, "TailscaleDisabled", "Tailscale is not configured")
	}

	// 8a. Reconcile Teams integration (Azure AD app, FIC, Bot Service, Teams channel).
	// This must run before Helm install so that the gateway secret has MSTEAMS_APP_ID
	// and MSTEAMS_TENANT_ID populated for the chart to pick up.
	// Run reconcileTeams when Teams is enabled OR when it was previously provisioned
	// (status.MSTeams != nil) so the disable cleanup path can execute.
	var teamsRequeueAfter time.Duration
	if (instance.Spec.MSTeams != nil && instance.Spec.MSTeams.Enabled) || instance.Status.MSTeams != nil {
		logger.Info("Reconciling Teams integration", "alias", alias)
		if err := r.reconcileTeams(ctx, instance); err != nil {
			logger.Error(err, "Teams reconciliation failed", "alias", alias)
			// Teams failure is non-blocking for the rest of the reconcile.
			// The TeamsReady condition is already set by reconcileTeams.
			if statusErr := r.updateStatus(ctx, instance); statusErr != nil {
				logger.Error(statusErr, "Failed to persist Teams status after reconciliation error", "alias", alias)
			}
			// Request a faster requeue so the 404 timer and other retryable
			// Teams errors don't wait for the next full reconcile cycle.
			teamsRequeueAfter = 30 * time.Second
		}
	}

	// Reconcile the per-instance Agent Identity before rendering the workload.
	// The Broker reads only the canonical binding persisted in CR status.
	var agentIdentityRequeueAfter time.Duration
	if instance.Spec.AgentIdentity != nil || instance.Status.AgentIdentity != nil {
		logger.Info("Reconciling Agent Identity", "instance", instance.Name)
		if err := r.reconcileAgentIdentity(ctx, instance); err != nil {
			logger.Error(err, "Agent Identity reconciliation failed", "instance", instance.Name)
			reason := "ReconcileFailed"
			if instance.Status.AgentIdentity != nil && instance.Status.AgentIdentity.Reason != "" {
				reason = instance.Status.AgentIdentity.Reason
			}
			setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionAgentIdentityReady,
				metav1.ConditionFalse, reason, "Agent Identity is not ready")
			_ = r.updateStatus(ctx, instance)
			if !instance.Spec.Suspended {
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			// Suspension is a safety boundary and must not depend on directory
			// availability. Continue to scale the workload to zero and remove
			// ingress, while retaining the identity error for a separate retry.
			agentIdentityRequeueAfter = 30 * time.Second
		}
		if agentIdentityRequested(instance) && instance.Status.AgentIdentity != nil && instance.Status.AgentIdentity.Phase == cetirunv1alpha1.AgentIdentityPhaseReady {
			now := metav1.Now()
			instance.Status.AgentIdentity.LastVerifiedTime = &now
		}
	}

	// Reconcile the Agent's AI Gateway virtual key before rendering the
	// workload: an Agent routed through the Gateway needs its key at startup.
	var aiGatewayRequeueAfter time.Duration
	if r.AIGateway.Enabled {
		chartSupported := aiGatewayChartSupports(prepared, instance.Spec.Agent.Type)
		if err := r.reconcileAIGateway(ctx, instance, chartSupported); err != nil {
			logger.Error(err, "AI Gateway virtual key reconciliation failed", "instance", instance.Name)
			_ = r.updateStatus(ctx, instance)
			if r.aiGatewayRequired(instance) && chartSupported && !r.aiGatewayRouted(instance) && !instance.Spec.Suspended {
				// Never provisioned: do not start an Agent that would be
				// routed through the Gateway without its key.
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			// A previously provisioned key keeps working through a Gateway
			// outage, and unrouted Agents do not depend on the key yet.
			aiGatewayRequeueAfter = 30 * time.Second
		} else if copilot := instance.Status.AIGateway.Copilot; copilot != nil &&
			(copilot.Phase == cetirunv1alpha1.AIGatewayCopilotPhaseDegraded || copilot.Phase == cetirunv1alpha1.AIGatewayCopilotPhaseStarting) {
			aiGatewayRequeueAfter = 15 * time.Second
		}
	} else if instance.Status.AIGateway != nil && instance.Status.AIGateway.Routed {
		// The deployment turned the feature off: stop routing. Existing keys
		// stay in the Gateway until the Agent is deleted.
		instance.Status.AIGateway.Routed = false
	}

	// 9. Install / upgrade Helm chart (skip if already deployed at this generation).
	// We check HelmDeployed condition + generation + operator hash rather than Phase,
	// because Phase may be Provisioning while waiting for the StatefulSet pod to become
	// ready (step 11). In that case Helm has already succeeded and we must not re-run it.
	// Re-read the gateway Secret hash now that ensureGatewayToken / ensureTailscaleAuthKey
	// have guaranteed the Secret exists and may have modified its content.
	{
		gwSecret := &corev1.Secret{}
		gwSecretKey := types.NamespacedName{Namespace: ns, Name: instance.Spec.GatewayTokenSecretRef}
		if err := r.Get(ctx, gwSecretKey, gwSecret); err != nil {
			if !errors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("re-read gateway Secret for operator hash: %w", err)
			}
		} else {
			gwSecretHash = secretContentHash(gwSecret.Data)
			gatewaySecretResourceVersion = gwSecret.ResourceVersion
		}
	}
	alreadyDeployed := isConditionTrue(instance.Status.Conditions, cetirunv1alpha1.ConditionHelmDeployed) &&
		instance.Status.ObservedGeneration == instance.Generation &&
		instance.Status.ObservedOperatorHash == r.configHash(instance, team, gwSecretHash, teamResourceVersion(team))
	// Teams may have resolved its application ID and Tailscale its auth key.
	// Rebuild from current bindings without reloading the versioned chart.
	helmValues, err := buildHelmValuesWithAgentIdentity(instance, team, r.DomainSuffix, r.SkillsStorageAccount, r.CetiRunHelmValues, r.ChartRegistry, r.DefaultModel, AgentIdentityHelmConfig{
		Enabled:   r.AgentIdentityEnabled,
		BrokerURL: r.AgentIdentityBrokerURL,
	})
	if err != nil {
		return r.rejectAgentConfig(ctx, instance, err)
	}
	if err := r.applyAIGatewayValues(helmValues, instance); err != nil {
		return r.rejectAgentConfig(ctx, instance, err)
	}
	if prepared.SupportsValuePath(aiGatewayChartValue, aiGatewayChartCopilotValue, aiGatewayChartResponsesValue) {
		r.applyCopilotResponsesModels(helmValues, instance)
	}
	if prepared.SupportsValuePath(aiGatewayChartValue, aiGatewayChartCopilotValue, aiGatewayChartProxyKeyValue) {
		applyCopilotProxyKeyID(helmValues, instance)
	}
	if prepared.SupportsValuePath(aiGatewayChartValue, aiGatewayChartCopilotValue, aiGatewayChartSignInValue) {
		applyCopilotSignInGeneration(helmValues, instance)
	}
	{
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: instance.Spec.GatewayTokenSecretRef}, secret); err != nil {
			return ctrl.Result{}, fmt.Errorf("read native integration bindings: %w", err)
		}
		helmValues["agentRequest"].(map[string]any)["tailscaleEnabled"] = len(secret.Data["TAILSCALE_AUTH_KEY"]) > 0
	}
	prepared, err = r.HelmClient.Refresh(prepared, helmValues)
	if err != nil {
		return r.rejectAgentConfig(ctx, instance, err)
	}
	r.recordSignInHelper(instance, prepared.ServesSignInHelper())
	setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionAgentConfigReady,
		metav1.ConditionTrue, "AgentConfigResolved", "Built-in Harness configuration resolved")
	helmValues = prepared.Values
	if !alreadyDeployed && instance.Spec.Agent.RegistrySecretRef != nil {
		sa, err := registryServiceAccount(helmValues, instanceServiceName(instance.Name))
		if err != nil {
			return r.registryWaiting(ctx, instance, "RegistryIsolationUnverified")
		}
		if err := registryauth.CheckIsolation(ctx, r.registryClient(), r.registryNamespace(), ns, sa, instance.Spec.Agent.RegistrySecretRef.Name); err != nil {
			return r.registryWaiting(ctx, instance, "RegistryIsolationUnverified")
		}
	}
	// existingSecret is not rendered by the chart and therefore cannot
	// contribute to checksum/secret. Its resourceVersion is non-sensitive and
	// changes on every Secret update, so include it in the Pod template to roll
	// the workload after any environment-backed credential changes.
	if gatewaySecretResourceVersion != "" {
		helmValues["podAnnotations"] = deepMerge(helmValues["podAnnotations"], map[string]interface{}{
			gatewaySecretResourceVersionAnnotation: gatewaySecretResourceVersion,
		})
	}
	if !alreadyDeployed {
		if err := r.checkRegistryIntent(ctx, instance); err != nil {
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Info("Installing/upgrading Helm chart", "release", releaseName, "namespace", ns)
		if err := r.HelmClient.InstallOrUpgrade(ns, releaseName, prepared); err != nil {
			logger.Error(err, "Helm install/upgrade failed", "release", releaseName)
			setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionHelmDeployed,
				metav1.ConditionFalse, "HelmInstallFailed", err.Error())
			_ = r.updateStatus(ctx, instance)
			return ctrl.Result{RequeueAfter: 60 * time.Second}, err
		}
		logger.Info("Helm chart installed/upgraded successfully", "release", releaseName)
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionHelmDeployed,
			metav1.ConditionTrue, "ChartInstalled", "Helm chart successfully installed")
		// Record generation and operator hash immediately so that subsequent reconciles
		// (e.g. while polling for pod readiness) can skip the Helm install.
		instance.Status.ObservedGeneration = instance.Generation
		instance.Status.ObservedOperatorHash = r.configHash(instance, team, gwSecretHash, teamResourceVersion(team))
		r.EventRecorder.Eventf(instance, corev1.EventTypeNormal, "HelmInstalled",
			"Helm chart openclaw installed in namespace %s", ns)
	} else {
		logger.V(1).Info("Skipping Helm install, already deployed at current generation", "release", releaseName)
	}
	if registryManaged(instance) {
		if err := r.cleanupRegistryCredentials(ctx, instance, false); err != nil {
			return r.registryWaiting(ctx, instance, "RegistryCleanupPending")
		}
	}
	if err := r.recoverRegistryPull(ctx, instance); err != nil {
		return ctrl.Result{}, err
	}

	// 10. Handle suspended instances.
	// When suspended, delete the VirtualService (remove ingress routing) and
	// skip StatefulSet readiness checks — the pod was scaled to zero by the
	// Helm upgrade above (replicaCount=0). PVCs are preserved by the
	// StatefulSet's whenScaled: Retain retention policy.
	if instance.Spec.Suspended {
		logger.Info("Instance is suspended, removing ingress and marking Suspended", "alias", alias)
		// Delete VirtualService so users see connection refused instead of 503.
		vsName := virtualServiceName(instance.Name)
		vs := &istiov1beta1.VirtualService{}
		vsKey := types.NamespacedName{Name: vsName, Namespace: ns}
		var vsDeleteErr error
		if err := r.Get(ctx, vsKey, vs); err != nil {
			if !errors.IsNotFound(err) {
				vsDeleteErr = err
				logger.Error(err, "failed to get VirtualService for suspend deletion", "name", vsName)
			}
		} else {
			if err := r.Delete(ctx, vs); err != nil && !errors.IsNotFound(err) {
				vsDeleteErr = err
				logger.Error(err, "failed to delete VirtualService on suspend", "name", vsName)
			} else {
				logger.Info("VirtualService deleted (instance suspended)", "name", vsName)
			}
		}
		if vsDeleteErr != nil {
			setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionIngressReady,
				metav1.ConditionFalse, "SuspendIngressRemovalFailed",
				fmt.Sprintf("Failed to remove VirtualService %q: %v", vsName, vsDeleteErr))
			setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionReady,
				metav1.ConditionFalse, "SuspendIngressRemovalFailed",
				"Instance suspend waiting for ingress removal to succeed")
			now := metav1.Now()
			instance.Status.Namespace = ns
			instance.Status.HelmReleaseName = releaseName
			instance.Status.LastReconcileTime = &now
			if err := r.updateStatus(ctx, instance); err != nil {
				logger.Error(err, "Failed to update status after VirtualService suspend deletion failure", "alias", alias)
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, vsDeleteErr
		}
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionIngressReady,
			metav1.ConditionFalse, "Suspended", "Ingress removed — instance is suspended")
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionPodReady,
			metav1.ConditionFalse, "Suspended", "Pod scaled to zero — instance is suspended")
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionReady,
			metav1.ConditionFalse, "InstanceSuspended", "Instance is suspended")
		now := metav1.Now()
		instance.Status.Phase = cetirunv1alpha1.PhaseSuspended
		instance.Status.Namespace = ns
		instance.Status.HelmReleaseName = releaseName
		instance.Status.LastReconcileTime = &now
		if err := r.updateStatus(ctx, instance); err != nil {
			logger.Error(err, "Failed to update status to Suspended", "alias", alias)
			return ctrl.Result{}, err
		}
		r.EventRecorder.Event(instance, corev1.EventTypeNormal, "InstanceSuspended", "Instance suspended — scaled to zero and ingress removed")
		requeueAfter := 10 * time.Minute
		if teamsRequeueAfter > 0 && teamsRequeueAfter < requeueAfter {
			requeueAfter = teamsRequeueAfter
		}
		if agentIdentityRequeueAfter > 0 && agentIdentityRequeueAfter < requeueAfter {
			requeueAfter = agentIdentityRequeueAfter
		}
		if aiGatewayRequeueAfter > 0 && aiGatewayRequeueAfter < requeueAfter {
			requeueAfter = aiGatewayRequeueAfter
		}
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	// 10a. Ensure VirtualService (non-suspended path).
	// TODO: VirtualService failure should block Ready (return error, set phase=Failed).
	// Currently best-effort because Istio is not installed on all test clusters.
	// When Istio is guaranteed to be present, change this block to:
	//   if err != nil { return r.failWith(ctx, instance, "VirtualServiceFailed", err.Error()) }
	logger.V(1).Info("Ensuring VirtualService", "alias", alias)
	if err := r.ensureVirtualService(ctx, instance); err != nil {
		logger.Error(err, "VirtualService creation/update failed (best-effort)", "alias", alias)
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionIngressReady,
			metav1.ConditionFalse, "VirtualServiceFailed", err.Error())
	} else {
		logger.V(1).Info("VirtualService ensured", "alias", alias)
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionIngressReady,
			metav1.ConditionTrue, "VirtualServiceCreated", "Istio ingress configured")
		r.EventRecorder.Eventf(instance, corev1.EventTypeNormal, "IngressConfigured",
			"Istio VirtualService created, instance accessible at %s", accessURLPathBased(instance.Name, r.DomainSuffix))
	}

	// 11. Verify StatefulSet pod readiness before marking Ready.
	stsName := instanceServiceName(instance.Name)
	sts := &appsv1.StatefulSet{}
	stsKey := types.NamespacedName{Namespace: ns, Name: stsName}
	if err := r.Get(ctx, stsKey, sts); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("StatefulSet not found yet, requeueing", "name", stsName, "namespace", ns)
			setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionPodReady,
				metav1.ConditionFalse, "StatefulSetNotFound", fmt.Sprintf("StatefulSet %s not found", stsName))
			setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionReady,
				metav1.ConditionFalse, "PodNotReady", "StatefulSet not found")
			_ = r.updateStatus(ctx, instance)
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
		logger.Error(err, "Failed to get StatefulSet", "name", stsName, "namespace", ns)
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionPodReady,
			metav1.ConditionFalse, "StatefulSetGetFailed", fmt.Sprintf("Failed to get StatefulSet %s: %v", stsName, err))
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionReady,
			metav1.ConditionFalse, "PodNotReady", "Failed to get StatefulSet")
		_ = r.updateStatus(ctx, instance)
		return ctrl.Result{}, err
	}

	desiredReplicas := int32(1)
	if sts.Spec.Replicas != nil {
		desiredReplicas = *sts.Spec.Replicas
	}
	rolloutComplete := (sts.Status.UpdatedReplicas >= desiredReplicas || registryOnlyRollout(sts)) &&
		sts.Status.ReadyReplicas >= desiredReplicas &&
		(sts.Status.ObservedGeneration >= sts.Generation || registryOnlyRollout(sts))
	if !rolloutComplete {
		logger.Info("StatefulSet rollout not complete, requeueing",
			"statefulset", stsName,
			"readyReplicas", sts.Status.ReadyReplicas,
			"updatedReplicas", sts.Status.UpdatedReplicas,
			"desiredReplicas", desiredReplicas,
			"stsGeneration", sts.Generation,
			"stsObservedGeneration", sts.Status.ObservedGeneration)
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionPodReady,
			metav1.ConditionFalse, "PodNotReady",
			fmt.Sprintf("Waiting for rollout (ready: %d, updated: %d, desired: %d)",
				sts.Status.ReadyReplicas, sts.Status.UpdatedReplicas, desiredReplicas))
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionReady,
			metav1.ConditionFalse, "PodNotReady", "StatefulSet rollout not complete")
		instance.Status.Phase = cetirunv1alpha1.PhaseProvisioning
		_ = r.updateStatus(ctx, instance)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionPodReady,
		metav1.ConditionTrue, "PodReady", "StatefulSet pod is running and ready")

	// 12. Mark Ready.
	now := metav1.Now()
	instance.Status.Phase = cetirunv1alpha1.PhaseReady
	instance.Status.AccessURL = accessURLPathBased(instance.Name, r.DomainSuffix)
	instance.Status.Namespace = ns
	instance.Status.HelmReleaseName = releaseName
	instance.Status.LastReconcileTime = &now
	setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionReady,
		metav1.ConditionTrue, "InstanceReady", "Instance is fully operational")
	if err := r.updateStatus(ctx, instance); err != nil {
		logger.Error(err, "Failed to update status to Ready", "alias", alias)
		return ctrl.Result{}, err
	}

	r.EventRecorder.Event(instance, corev1.EventTypeNormal, "InstanceReady", "Agent fully operational")
	logger.Info("Reconciliation complete", "alias", alias, "accessURL", accessURLPathBased(instance.Name, r.DomainSuffix))
	requeueAfter := 10 * time.Minute
	if teamsRequeueAfter > 0 && teamsRequeueAfter < requeueAfter {
		requeueAfter = teamsRequeueAfter
	}
	if aiGatewayRequeueAfter > 0 && aiGatewayRequeueAfter < requeueAfter {
		requeueAfter = aiGatewayRequeueAfter
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// handleDelete performs cleanup and removes the finalizer.
func (r *AgentReconciler) handleDelete(ctx context.Context, instance *cetirunv1alpha1.Agent) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	alias := instance.Spec.User.Alias

	if !controllerutil.ContainsFinalizer(instance, cetirunv1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}

	logger.Info("Deleting Agent", "alias", alias)
	r.EventRecorder.Eventf(instance, corev1.EventTypeNormal, "InstanceDeleting",
		"Starting cleanup for instance %s", alias)

	if err := r.setPhase(ctx, instance, cetirunv1alpha1.PhaseDeleting); err != nil {
		logger.Error(err, "failed to set phase Deleting")
	}

	ns := instanceNamespace(instance.Name)
	releaseName := helmReleaseName(instance.Name)

	// Delete Teams Azure resources (best-effort, never blocks deletion).
	r.deleteTeams(ctx, instance)

	// Agent Identity cleanup is blocking. Removing the finalizer before the
	// authorization record and directory principal are gone would orphan them.
	if agentIdentityRequested(instance) || instance.Status.AgentIdentity != nil {
		if err := r.deleteAgentIdentity(ctx, instance); err != nil {
			logger.Error(err, "Agent Identity cleanup failed; retaining finalizer", "instance", instance.Name)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}

	// AI Gateway key revocation is blocking for the same reason, and runs
	// before the workload is removed so the key cannot outlive the Agent.
	if err := r.deleteAIGatewayKey(ctx, instance); err != nil {
		logger.Error(err, "AI Gateway key revocation failed; retaining finalizer", "instance", instance.Name)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Helm uninstall.
	logger.Info("Uninstalling Helm release", "release", releaseName, "namespace", ns)
	if err := r.HelmClient.Uninstall(ns, releaseName); err != nil {
		logger.Error(err, "helm uninstall failed, continuing cleanup", "release", releaseName)
	} else {
		logger.V(1).Info("Helm release uninstalled", "release", releaseName)
	}

	// Delete VirtualService.
	vsName := virtualServiceName(instance.Name)
	vs := &istiov1beta1.VirtualService{}
	vsKey := types.NamespacedName{Name: vsName, Namespace: instanceNamespace(instance.Name)}
	logger.V(1).Info("Deleting VirtualService", "name", vsName, "namespace", vsKey.Namespace)
	if err := r.Get(ctx, vsKey, vs); err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("VirtualService not found, skipping delete", "name", vsName, "namespace", vsKey.Namespace)
		} else {
			logger.Error(err, "failed to get VirtualService for deletion", "name", vsName, "namespace", vsKey.Namespace)
			return ctrl.Result{}, fmt.Errorf("get VirtualService %s/%s for deletion: %w", vsKey.Namespace, vsName, err)
		}
	} else {
		if err := r.Delete(ctx, vs); err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "delete VirtualService failed", "name", vsName, "namespace", vsKey.Namespace)
		} else {
			logger.V(1).Info("VirtualService deleted", "name", vsName, "namespace", vsKey.Namespace)
		}
	}

	// Delete namespace (cascades all resources).
	logger.Info("Deleting namespace", "namespace", ns)
	nsObj := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: ns}, nsObj); err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("Namespace not found, skipping delete", "namespace", ns)
		} else {
			logger.Error(err, "failed to get namespace for deletion", "namespace", ns)
			return ctrl.Result{}, fmt.Errorf("get namespace %s for deletion: %w", ns, err)
		}
	} else {
		if err := r.Delete(ctx, nsObj); err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "delete namespace failed", "namespace", ns)
		} else {
			logger.V(1).Info("Namespace deletion initiated", "namespace", ns)
		}
	}

	// Remove finalizer.
	before := instance.DeepCopy()
	controllerutil.RemoveFinalizer(instance, cetirunv1alpha1.Finalizer)
	logger.V(1).Info("Removing finalizer", "alias", alias)
	if err := r.Patch(ctx, instance, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("CR already deleted, finalizer removal unnecessary", "alias", alias)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to remove finalizer", "alias", alias)
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	logger.Info("Finalizer removed", "alias", alias)

	r.EventRecorder.Eventf(instance, corev1.EventTypeNormal, "InstanceDeleted",
		"Instance cleanup completed for %s", alias)
	return ctrl.Result{}, nil
}

func (r *AgentReconciler) operatorNamespaceRBAC(ns string) (*rbacv1.Role, *rbacv1.RoleBinding) {
	const roleName = "cetirun-operator-ns-role"
	const bindingName = "cetirun-operator-ns-rolebinding"

	desiredRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "cetirun-operator",
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"secrets", "configmaps", "services", "persistentvolumeclaims", "serviceaccounts"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"statefulsets", "deployments"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"networking.k8s.io"},
				Resources: []string{"networkpolicies"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"rbac.authorization.k8s.io"},
				Resources: []string{"roles", "rolebindings"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"batch"},
				Resources: []string{"jobs"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"networking.istio.io"},
				Resources: []string{"virtualservices", "sidecars"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"security.istio.io"},
				Resources: []string{"authorizationpolicies", "peerauthentications"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
		},
	}

	// The operator's own ServiceAccount is in the namespace where the operator pod runs.
	// We bind the Role to the operator's ServiceAccount so that it can act within ns.
	desiredBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bindingName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "cetirun-operator",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      r.OperatorSAName,
				Namespace: r.OperatorNamespace,
			},
		},
	}
	return desiredRole, desiredBinding
}

func (r *AgentReconciler) ensureNamespacedRBAC(ctx context.Context, ns string) error {
	logger := log.FromContext(ctx)
	desiredRole, desiredBinding := r.operatorNamespaceRBAC(ns)
	roleName, bindingName := desiredRole.Name, desiredBinding.Name
	existingRole := &rbacv1.Role{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: roleName}, existingRole)
	if errors.IsNotFound(err) {
		logger.V(1).Info("Creating namespaced Role", "role", roleName, "namespace", ns)
		if err := r.Create(ctx, desiredRole); err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("create namespaced Role in %s: %w", ns, err)
		}
	} else if err != nil {
		return fmt.Errorf("get namespaced Role in %s: %w", ns, err)
	} else {
		logger.V(1).Info("Updating namespaced Role", "role", roleName, "namespace", ns)
		existingRole.Rules = desiredRole.Rules
		if err := r.Update(ctx, existingRole); err != nil {
			return fmt.Errorf("update namespaced Role in %s: %w", ns, err)
		}
	}
	existingBinding := &rbacv1.RoleBinding{}
	err = r.Get(ctx, types.NamespacedName{Namespace: ns, Name: bindingName}, existingBinding)
	if errors.IsNotFound(err) {
		logger.V(1).Info("Creating namespaced RoleBinding", "binding", bindingName, "namespace", ns)
		if err := r.Create(ctx, desiredBinding); err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("create namespaced RoleBinding in %s: %w", ns, err)
		}
	} else if err != nil {
		return fmt.Errorf("get namespaced RoleBinding in %s: %w", ns, err)
	}
	// RoleBinding.RoleRef is immutable after creation; nothing to update.
	return nil
}

// resolveIstioRevision returns the Istio revision to use for sidecar injection.
// If IstioRevision is explicitly set, it is returned directly. Otherwise, the
// revision is auto-detected once from the AKS managed Istio control plane by
// listing ConfigMaps in aks-istio-system with the istio.io/rev label.
func (r *AgentReconciler) resolveIstioRevision(ctx context.Context) string {
	if r.IstioRevision != "" {
		return r.IstioRevision
	}

	r.istioMu.Lock()
	defer r.istioMu.Unlock()

	// Fast path: if we've already successfully resolved a revision, reuse it.
	if r.istioRevResolved != "" {
		return r.istioRevResolved
	}

	logger := log.FromContext(ctx).WithName("istio-detection")
	cmList := &corev1.ConfigMapList{}
	labelReq, err := labels.Parse("istio.io/rev")
	if err != nil {
		logger.Info("Failed to parse istio.io/rev label selector", "error", err)
		return ""
	}
	err = r.List(ctx, cmList,
		client.InNamespace("aks-istio-system"),
		client.MatchingLabelsSelector{Selector: labelReq},
	)
	if err != nil {
		logger.Info("Could not list configmaps in aks-istio-system (not AKS Istio?)", "error", err)
		return ""
	}
	if len(cmList.Items) == 0 {
		logger.Info("No configmaps with istio.io/rev label found in aks-istio-system")
		return ""
	}

	// Collect distinct non-empty istio.io/rev values from all matching ConfigMaps.
	revisions := make(map[string]struct{})
	for _, cm := range cmList.Items {
		if cm.Labels == nil {
			continue
		}
		if rev, ok := cm.Labels["istio.io/rev"]; ok && rev != "" {
			revisions[rev] = struct{}{}
		}
	}

	if len(revisions) == 0 {
		logger.Info("Configmaps with istio.io/rev label found but no non-empty istio.io/rev values detected in aks-istio-system")
		return ""
	}

	if len(revisions) > 1 {
		distinct := make([]string, 0, len(revisions))
		for rev := range revisions {
			distinct = append(distinct, rev)
		}
		logger.Info("Multiple distinct istio.io/rev values detected in aks-istio-system; auto-detection is ambiguous", "revisions", distinct)
		return ""
	}

	var rev string
	for rVal := range revisions {
		rev = rVal
		break
	}
	r.istioRevResolved = rev
	logger.Info("Auto-detected AKS Istio revision", "revision", rev)
	return r.istioRevResolved
}

// ensureNamespace creates the instance namespace if it does not already exist.
func (r *AgentReconciler) ensureNamespace(ctx context.Context, ns, alias string, owner ...*cetirunv1alpha1.Agent) error {
	logger := log.FromContext(ctx)
	desiredLabels := map[string]string{
		"app.kubernetes.io/name":       "cetirun",
		"app.kubernetes.io/instance":   alias,
		"app.kubernetes.io/managed-by": "cetirun-operator",
	}
	if rev := r.resolveIstioRevision(ctx); rev != "" {
		desiredLabels["istio.io/rev"] = rev
	}

	nsObj := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: ns}, nsObj)
	if err == nil {
		// Namespace exists — ensure labels are up to date.
		needUpdate := false
		if nsObj.Labels == nil {
			nsObj.Labels = make(map[string]string)
		}
		for k, v := range desiredLabels {
			if nsObj.Labels[k] != v {
				nsObj.Labels[k] = v
				needUpdate = true
			}
		}
		if needUpdate {
			if err := r.Update(ctx, nsObj); err != nil {
				if errors.IsForbidden(err) {
					logger.Info("Missing RBAC permissions to update namespace labels", "namespace", ns, "error", err.Error())
					return fmt.Errorf("forbidden updating namespace labels %s (check RBAC permissions): %w", ns, err)
				}
				return fmt.Errorf("update namespace labels %s: %w", ns, err)
			}
		}
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("get namespace %s: %w", ns, err)
	}

	logger.Info("Creating namespace", "namespace", ns, "alias", alias)
	nsObj = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns,
			Labels: desiredLabels,
		},
	}
	if len(owner) > 0 && owner[0] != nil && owner[0].UID != "" {
		nsObj.OwnerReferences = []metav1.OwnerReference{registryauth.Owner(owner[0])}
	}
	if err := r.Create(ctx, nsObj); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", ns, err)
	}
	return nil
}

// ensureGatewayToken creates the gateway token Secret if it doesn't exist,
// and ensures MSTEAMS credential keys are populated when spec.msteams is configured.
func (r *AgentReconciler) ensureGatewayToken(ctx context.Context, instance *cetirunv1alpha1.Agent) error {
	logger := log.FromContext(ctx)
	ns := instanceNamespace(instance.Name)
	secretName := instance.Spec.GatewayTokenSecretRef

	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: secretName}, secret)
	if err == nil {
		logger.V(1).Info("Gateway token secret already exists", "secret", secretName, "namespace", ns)
		return r.ensureExistingGatewayToken(ctx, logger, instance, secret)
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("get gateway token secret: %w", err)
	}

	logger.Info("Creating gateway token secret", "secret", secretName, "namespace", ns)

	secret, err = r.newGatewayTokenSecret(instance)
	if err != nil {
		return err
	}
	if err := r.Create(ctx, secret); err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("create gateway token secret: %w", err)
		}
		// A concurrent reconcile may have created a legacy-form Secret after our
		// initial Get. Re-read and enrich it rather than starting Helm with a
		// Secret that is missing a required key.
		if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: secretName}, secret); err != nil {
			return fmt.Errorf("re-read concurrently created gateway token secret: %w", err)
		}
		return r.ensureExistingGatewayToken(ctx, logger, instance, secret)
	}
	return nil
}

func (r *AgentReconciler) newGatewayTokenSecret(instance *cetirunv1alpha1.Agent) (*corev1.Secret, error) {
	token, err := generateRandomToken(32)
	if err != nil {
		return nil, err
	}
	authKey, err := generateRandomToken(32)
	if err != nil {
		return nil, err
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Spec.GatewayTokenSecretRef,
			Namespace: instanceNamespace(instance.Name),
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "cetirun-operator",
				"app.kubernetes.io/instance":   instance.Spec.User.Alias,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"OPENCLAW_GATEWAY_TOKEN": []byte(token),
			"CETIRUN_AUTH_KEY":       []byte(authKey),
		},
	}
	h, err := builtin.NewRegistry().Lookup(string(instance.Spec.Agent.Type))
	if err != nil {
		return nil, err
	}
	descriptor := h.Describe()
	if descriptor.EnvironmentFile != "" {
		apiKey, err := generateRandomToken(32)
		if err != nil {
			return nil, err
		}
		secret.Data[descriptor.NativeSecretKey] = []byte(apiKey)
		secret.Data[descriptor.EnvironmentFile] = []byte(renderHermesEnv(secret.Data, apiKey, hermesCORSOrigins(r.DomainSuffix)))
	}
	// Seed MSTEAMS keys on initial creation (only when Teams is enabled and values are known).
	if instance.Spec.MSTeams != nil && instance.Spec.MSTeams.Enabled {
		if instance.Spec.MSTeams.AppID != "" {
			secret.Data["MSTEAMS_APP_ID"] = []byte(instance.Spec.MSTeams.AppID)
		}
		if instance.Spec.MSTeams.TenantID != "" {
			secret.Data["MSTEAMS_TENANT_ID"] = []byte(instance.Spec.MSTeams.TenantID)
		}
	}
	return secret, nil
}

func (r *AgentReconciler) ensureExistingGatewayToken(ctx context.Context, logger logr.Logger, instance *cetirunv1alpha1.Agent, secret *corev1.Secret) error {
	if err := r.ensureCetiRunAuthKey(ctx, logger, secret); err != nil {
		return err
	}
	if err := r.ensureMSTeamsSecretKeys(ctx, logger, secret, instance); err != nil {
		return err
	}
	return r.ensureHermesSecretKeys(ctx, logger, secret, instance)
}

// ensureCetiRunAuthKey adds the bearer key used by the in-pod cetirun-auth
// service to operator-managed Secrets created before that key was introduced.
func (r *AgentReconciler) ensureCetiRunAuthKey(ctx context.Context, logger logr.Logger, secret *corev1.Secret) error {
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	if len(secret.Data["CETIRUN_AUTH_KEY"]) != 0 {
		return nil
	}
	key, err := generateRandomToken(32)
	if err != nil {
		return err
	}
	secret.Data["CETIRUN_AUTH_KEY"] = []byte(key)
	logger.Info("Adding CetiRun auth key to gateway token secret", "secret", secret.Name)
	if err := r.Update(ctx, secret); err != nil {
		return fmt.Errorf("update gateway token secret with CetiRun auth key: %w", err)
	}
	return nil
}

// ensureHermesSecretKeys patches the gateway-token Secret with API_SERVER_KEY and hermes.env
// when a Hermes instance uses an operator-managed existingSecret.
func (r *AgentReconciler) ensureHermesSecretKeys(ctx context.Context, logger logr.Logger, secret *corev1.Secret, instance *cetirunv1alpha1.Agent) error {
	h, err := builtin.NewRegistry().Lookup(string(instance.Spec.Agent.Type))
	if err != nil {
		return err
	}
	descriptor := h.Describe()
	if descriptor.EnvironmentFile == "" {
		return nil
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	needsUpdate := false
	apiKey := string(secret.Data[descriptor.NativeSecretKey])
	if apiKey == "" {
		var err error
		apiKey, err = generateRandomToken(32)
		if err != nil {
			return err
		}
		secret.Data[descriptor.NativeSecretKey] = []byte(apiKey)
		needsUpdate = true
	}
	hermesEnv := renderHermesEnv(secret.Data, apiKey, hermesCORSOrigins(r.DomainSuffix))
	if string(secret.Data[descriptor.EnvironmentFile]) != hermesEnv {
		secret.Data[descriptor.EnvironmentFile] = []byte(hermesEnv)
		needsUpdate = true
	}
	if !needsUpdate {
		return nil
	}
	logger.Info("Updating gateway token secret with Hermes environment", "secret", secret.Name)
	if err := r.Update(ctx, secret); err != nil {
		return fmt.Errorf("update gateway token secret with hermes environment: %w", err)
	}
	return nil
}

func renderHermesEnv(data map[string][]byte, apiKey string, corsOrigins string) string {
	modelAPIKey := secretEnvValue(data, "MODEL_API_KEY")
	openaiKey := secretEnvValue(data, "OPENAI_API_KEY")
	if openaiKey == "" {
		openaiKey = modelAPIKey
	}
	anthropicKey := secretEnvValue(data, "ANTHROPIC_API_KEY")
	if anthropicKey == "" {
		anthropicKey = modelAPIKey
	}
	var buf strings.Builder
	buf.WriteString("API_SERVER_KEY=" + apiKey + "\n")
	if corsOrigins != "" {
		buf.WriteString("API_SERVER_CORS_ORIGINS=" + corsOrigins + "\n")
	}
	if v := secretEnvValue(data, "COPILOT_GITHUB_TOKEN"); v != "" {
		buf.WriteString("COPILOT_GITHUB_TOKEN=" + v + "\n")
	}
	if openaiKey != "" {
		buf.WriteString("OPENAI_API_KEY=" + openaiKey + "\n")
	}
	if anthropicKey != "" {
		buf.WriteString("ANTHROPIC_API_KEY=" + anthropicKey + "\n")
	}
	if v := secretEnvValue(data, "BRAVE_API_KEY"); v != "" {
		buf.WriteString("BRAVE_API_KEY=" + v + "\n")
	}
	if v := secretEnvValue(data, "PERPLEXITY_API_KEY"); v != "" {
		buf.WriteString("PERPLEXITY_API_KEY=" + v + "\n")
	}
	return buf.String()
}

// hermesCORSOrigins returns the Origin allow-list for the Hermes API server,
// derived from the operator's configured domain suffix. Without this, the
// portal chat UI is rejected with 403 by Hermes' CORS middleware because the
// API server denies unknown browser origins.
func hermesCORSOrigins(domainSuffix string) string {
	if domainSuffix == "" {
		return ""
	}
	return "https://" + domainSuffix
}

func secretEnvValue(data map[string][]byte, key string) string {
	if len(data[key]) > 0 {
		return string(data[key])
	}
	for _, line := range strings.Split(string(data["hermes.env"]), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	return ""
}

// ensureMSTeamsSecretKeys patches the gateway-token Secret with MSTEAMS_APP_ID and
// MSTEAMS_TENANT_ID when Teams is enabled and the values differ or are missing.
func (r *AgentReconciler) ensureMSTeamsSecretKeys(ctx context.Context, logger logr.Logger, secret *corev1.Secret, instance *cetirunv1alpha1.Agent) error {
	if instance.Spec.MSTeams == nil || !instance.Spec.MSTeams.Enabled {
		return nil
	}
	msteams := instance.Spec.MSTeams
	needsUpdate := false
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	if msteams.AppID != "" && string(secret.Data["MSTEAMS_APP_ID"]) != msteams.AppID {
		secret.Data["MSTEAMS_APP_ID"] = []byte(msteams.AppID)
		needsUpdate = true
	}
	if msteams.TenantID != "" && string(secret.Data["MSTEAMS_TENANT_ID"]) != msteams.TenantID {
		secret.Data["MSTEAMS_TENANT_ID"] = []byte(msteams.TenantID)
		needsUpdate = true
	}
	if !needsUpdate {
		return nil
	}
	logger.Info("Updating gateway token secret with MSTEAMS credentials", "secret", secret.Name)
	if err := r.Update(ctx, secret); err != nil {
		return fmt.Errorf("update gateway token secret with msteams keys: %w", err)
	}
	return nil
}

// ensureVirtualService creates or updates the Istio VirtualService for this instance.
func (r *AgentReconciler) ensureVirtualService(ctx context.Context, instance *cetirunv1alpha1.Agent, access ...customAccess) error {
	logger := log.FromContext(ctx)
	desired := buildVirtualService(instance, r.DomainSuffix, r.IstioGateway, access...)
	if desired == nil {
		return fmt.Errorf("unsupported Agent native access")
	}
	vsName := virtualServiceName(instance.Name)
	vsNS := instanceNamespace(instance.Name)

	existing := &istiov1beta1.VirtualService{}
	err := r.Get(ctx, types.NamespacedName{Namespace: vsNS, Name: vsName}, existing)
	if errors.IsNotFound(err) {
		logger.Info("Creating VirtualService", "name", vsName, "namespace", vsNS)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("create VirtualService: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get VirtualService: %w", err)
	}

	// Update existing VirtualService by patching it with the desired spec fields.
	// We avoid direct struct assignment because the proto message contains a sync.Mutex.
	if instance.Spec.Agent.Type == cetirunv1alpha1.AgentTypeCustom && !hasAgentOwner(existing, instance) {
		return fmt.Errorf("Custom platform route belongs to another owner")
	}
	ownedAnnotationsMatch := instance.Spec.Agent.Type != cetirunv1alpha1.AgentTypeCustom ||
		existing.Annotations[customAgentUID] == string(instance.UID)
	if proto.Equal(&existing.Spec, &desired.Spec) && maps.Equal(existing.Labels, desired.Labels) && ownedAnnotationsMatch {
		return nil
	}
	logger.V(1).Info("Updating VirtualService", "name", vsName, "namespace", vsNS)
	existing.Spec.Hosts = desired.Spec.Hosts
	existing.Spec.Gateways = desired.Spec.Gateways
	existing.Spec.Http = desired.Spec.Http
	existing.Labels = desired.Labels
	if instance.Spec.Agent.Type == cetirunv1alpha1.AgentTypeCustom {
		if existing.Annotations == nil {
			existing.Annotations = map[string]string{}
		}
		existing.Annotations[customAgentUID] = string(instance.UID)
		existing.Spec.ExportTo = desired.Spec.ExportTo
	}
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("update VirtualService: %w", err)
	}
	return nil
}

// rejectReservedUIInstance removes the externally routable VirtualService for
// an instance whose name conflicts with the native /ui/<instance>/ namespace.
func (r *AgentReconciler) rejectReservedUIInstance(ctx context.Context, instance *cetirunv1alpha1.Agent) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	vsName := virtualServiceName(instance.Name)
	vs := &istiov1beta1.VirtualService{}
	vsKey := types.NamespacedName{Name: vsName, Namespace: instanceNamespace(instance.Name)}

	if err := r.Get(ctx, vsKey, vs); err != nil {
		if !errors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("get VirtualService for reserved instance %s/%s: %w", vsKey.Namespace, vsName, err)
		}
	} else if err := r.Delete(ctx, vs); err != nil && !errors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("delete VirtualService for reserved instance %s/%s: %w", vsKey.Namespace, vsName, err)
	} else {
		logger.Info("Removed VirtualService for reserved instance name", "name", instance.Name, "virtualService", vsName)
	}

	return r.failWith(ctx, instance, "ReservedInstanceName", "instance name 'ui' is reserved for native agent routes")
}

// ensureTailscaleAuthKey reads the Tailscale auth key from the user-supplied Secret
// and patches it into the gateway token Secret so the StatefulSet can read it.
// The chart's Secret template is skipped when secrets.existingSecret is set, so
// we must write TAILSCALE_AUTH_KEY directly into the gateway token Secret.
// No-op if spec.Tailscale is nil.
func (r *AgentReconciler) ensureTailscaleAuthKey(ctx context.Context, instance *cetirunv1alpha1.Agent) error {
	ns := instanceNamespace(instance.Name)
	gwSecretName := instance.Spec.GatewayTokenSecretRef

	if instance.Spec.Tailscale == nil {
		// Tailscale disabled — remove the key from the gateway Secret if present.
		gwSecret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: gwSecretName}, gwSecret); err != nil {
			if errors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("get gateway secret %s/%s: %w", ns, gwSecretName, err)
		}
		if _, exists := gwSecret.Data[cetirunv1alpha1.TailscaleAuthKeyField]; exists {
			delete(gwSecret.Data, cetirunv1alpha1.TailscaleAuthKeyField)
			if err := r.Update(ctx, gwSecret); err != nil {
				return fmt.Errorf("remove %s from gateway secret: %w", cetirunv1alpha1.TailscaleAuthKeyField, err)
			}
		}
		return nil
	}

	// Read auth key from the user-supplied Secret.
	tsSecretName := instance.Spec.Tailscale.AuthKeySecretRef
	tsSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: tsSecretName}, tsSecret); err != nil {
		return fmt.Errorf("get Tailscale auth key secret %s/%s: %w", ns, tsSecretName, err)
	}
	authKey, ok := tsSecret.Data[cetirunv1alpha1.TailscaleAuthKeyField]
	if !ok || len(authKey) == 0 {
		return fmt.Errorf("secret %s/%s missing or empty key %q", ns, tsSecretName, cetirunv1alpha1.TailscaleAuthKeyField)
	}

	// Patch the auth key into the gateway token Secret.
	gwSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: gwSecretName}, gwSecret); err != nil {
		return fmt.Errorf("get gateway secret %s/%s: %w", ns, gwSecretName, err)
	}
	if gwSecret.Data == nil {
		gwSecret.Data = make(map[string][]byte)
	}
	if string(gwSecret.Data[cetirunv1alpha1.TailscaleAuthKeyField]) != string(authKey) {
		gwSecret.Data[cetirunv1alpha1.TailscaleAuthKeyField] = authKey
		if err := r.Update(ctx, gwSecret); err != nil {
			return fmt.Errorf("patch %s into gateway secret: %w", cetirunv1alpha1.TailscaleAuthKeyField, err)
		}
	}
	return nil
}

// setPhase updates only the Phase field in the CR status.
func (r *AgentReconciler) setPhase(ctx context.Context, instance *cetirunv1alpha1.Agent, phase string) error {
	instance.Status.Phase = phase
	return r.updateStatus(ctx, instance)
}

// updateStatus patches the CR status sub-resource.
func (r *AgentReconciler) updateStatus(ctx context.Context, instance *cetirunv1alpha1.Agent) error {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	current := &cetirunv1alpha1.Agent{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(instance), current); err != nil {
		return fmt.Errorf("read Agent before status patch: %w", err)
	}
	// Callers already changed their typed status. Read the same revision to
	// construct a delta without deleting fields introduced by a newer binary.
	if instance.ResourceVersion == "" || current.ResourceVersion != instance.ResourceVersion || current.UID != instance.UID {
		return errors.NewConflict(cetirunv1alpha1.GroupVersion.WithResource("agents").GroupResource(), instance.Name,
			fmt.Errorf("Agent changed before its status could be patched"))
	}
	before := current.DeepCopy()
	current.Status = *instance.Status.DeepCopy()
	if err := r.Status().Patch(ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	instance.Status = current.Status
	instance.ResourceVersion = current.ResourceVersion
	emitSandboxAgentTransition(ctx, before, current)
	return nil
}

// failWith sets status to Failed with the given reason/message and returns a requeue result.
// TODO: failWith returns a non-nil error, which causes controller-runtime to ignore RequeueAfter
// and instead use exponential back-off. For now this is acceptable (back-off is still retried).
// If precise retry intervals are needed, change the return to (ctrl.Result{RequeueAfter: ...}, nil)
// and log the error explicitly.
func (r *AgentReconciler) failWith(ctx context.Context, instance *cetirunv1alpha1.Agent, reason, message string) (ctrl.Result, error) {
	instance.Status.Phase = cetirunv1alpha1.PhaseFailed
	setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionReady,
		metav1.ConditionFalse, reason, message)
	_ = r.updateStatus(ctx, instance)
	r.EventRecorder.Event(instance, corev1.EventTypeWarning, reason, message)
	return ctrl.Result{RequeueAfter: 60 * time.Second}, fmt.Errorf("%s: %s", reason, message)
}

// enqueueInstancesForTeam maps an Team event to reconcile requests for
// all Agents that reference the team via spec.team.
func (r *AgentReconciler) enqueueInstancesForTeam(ctx context.Context, obj client.Object) []reconcile.Request {
	team, ok := obj.(*cetirunv1alpha1.Team)
	if !ok {
		return nil
	}
	list := &cetirunv1alpha1.AgentList{}
	if err := r.List(ctx, list, client.MatchingFields{instanceTeamIndex: team.Name}); err != nil {
		log.FromContext(ctx).Error(err, "failed to list instances for team watch", "team", team.Name)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, inst := range list.Items {
		reqs = append(reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: inst.Name},
		})
	}
	return reqs
}
