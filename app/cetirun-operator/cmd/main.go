// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can use them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	sandboxadmission "github.com/Azure/CetiRun/app/cetirun-operator/internal/admission"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/buildinfo"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	helmclient "github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"github.com/Azure/CetiRun/app/internal/chataccess"
	"github.com/Azure/CetiRun/app/internal/registryauth"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cetirunv1alpha1.AddToScheme(scheme))
	// Register Istio networking types for VirtualService management.
	utilruntime.Must(istiov1beta1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var chartRepoURL string
	var chartVersion string
	var domainSuffix string
	var istioGateway string
	var defaultModel string
	var showVersion bool
	var mode string
	var admissionCertDir string
	var admissionPort int
	var admissionPodStateNamespace string
	var admissionPodTimeout time.Duration

	flag.StringVar(&mode, "mode", "operator", "Process mode: operator or sandbox-admission.")
	flag.StringVar(&admissionCertDir, "admission-cert-dir", "", "Directory containing platform-issued tls.crt and tls.key for Sandbox admission.")
	flag.IntVar(&admissionPort, "admission-port", 9443, "Sandbox admission HTTPS port.")
	flag.StringVar(&admissionPodStateNamespace, "admission-pod-state-namespace", "", "Protected controller state namespace for optional Sandbox Pod admission.")
	flag.DurationVar(&admissionPodTimeout, "admission-pod-timeout", sandboxadmission.DefaultPodRequestTimeout, "Total Sandbox admission validation budget; must be shorter than the API-server webhook timeout.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&chartRepoURL, "chart-repo-url", "oci://cetirun.azurecr.io/helm/cetirun",
		"OCI registry URL for the CetiRun Helm chart.")
	flag.StringVar(&chartVersion, "chart-version", "0.1.1",
		"Version of the CetiRun Helm chart to deploy.")
	flag.StringVar(&domainSuffix, "domain-suffix", "", "Base domain suffix for instance access URLs.")
	flag.StringVar(&istioGateway, "istio-gateway", controller.DefaultIstioGateway,
		"Istio Gateway namespace/name used by instance VirtualServices.")
	flag.StringVar(&defaultModel, "default-model", "gpt-6-astra", "Default GitHub Copilot model for instances without spec.model.")
	flag.BoolVar(&showVersion, "version", false, "Print version information and exit.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	if showVersion {
		fmt.Printf("cetirun-operator %s (%s)\n", buildinfo.Version, buildinfo.Commit)
		return
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	if mode == "sandbox-admission" {
		config, err := sandboxadmission.ParseConfig(os.Getenv("CETIRUN_SANDBOX_ADMISSION"))
		if err != nil {
			setupLog.Error(err, "invalid Sandbox admission configuration")
			os.Exit(1)
		}
		setupLog.Info("Starting Sandbox configuration admission; runtime execution remains disabled",
			"version", buildinfo.Version, "commit", buildinfo.Commit)
		if err := sandboxadmission.Run(ctrl.SetupSignalHandler(), ctrl.GetConfigOrDie(), config, sandboxadmission.ServerOptions{
			CertDirectory: admissionCertDir, Port: admissionPort, MetricsAddress: metricsAddr, ProbeAddress: probeAddr,
			PodStateNamespace: admissionPodStateNamespace, PodRequestTimeout: admissionPodTimeout,
		}); err != nil {
			setupLog.Error(err, "Sandbox admission server failed")
			os.Exit(1)
		}
		return
	}
	if mode != "operator" {
		setupLog.Error(fmt.Errorf("unsupported process mode"), "mode must be operator or sandbox-admission")
		os.Exit(1)
	}
	setupLog.Info("Starting CetiRun Operator", "version", buildinfo.Version, "commit", buildinfo.Commit)

	// Support environment variable overrides for chart configuration.
	if url := os.Getenv("CETIRUN_CHART_REPO_URL"); url != "" {
		chartRepoURL = url
	}
	if version := os.Getenv("CETIRUN_CHART_VERSION"); version != "" {
		chartVersion = version
	}
	if model := os.Getenv("CETIRUN_DEFAULT_MODEL"); model != "" {
		defaultModel = model
	}

	chartRef := helmclient.ChartReference{
		RepoURL: chartRepoURL,
		Version: chartVersion,
	}
	// Extract the registry hostname from the chart repo URL (e.g.
	// "oci://registry.example.com/helm/cetirun" → "registry.example.com").
	// This is passed to the reconciler to override the chart's default
	// image.repository so that all clusters pull images from the correct ACR.
	chartRegistry := extractRegistryHost(chartRepoURL)
	setupLog.Info("Using OCI chart repository", "url", chartRepoURL, "version", chartVersion, "registry", chartRegistry)
	if chartRegistry != "" {
		setupLog.Info("Overriding agent image repositories",
			"openclaw", chartRegistry+"/openclaw-gateway",
			"hermes", chartRegistry+"/hermes-gateway",
			"desktop", chartRegistry+"/cetirun-desktop")
	} else {
		setupLog.Info("Could not extract registry host from chart repo URL, using chart default image.repository")
	}

	if err := chartRef.Validate(); err != nil {
		setupLog.Error(err, "invalid chart reference configuration")
		os.Exit(1)
	}

	if domainSuffix == "" {
		setupLog.Info("domain-suffix must be set (e.g. --domain-suffix=example.com)")
		os.Exit(1)
	}
	if istioGateway == "" {
		setupLog.Info("istio-gateway must be set (e.g. --istio-gateway=aks-istio-ingress/cetirun-gateway)")
		os.Exit(1)
	}

	istioRevision := os.Getenv("ISTIO_REVISION")

	// Optional: override the chart's default skills storage account for non-prod environments.
	// Deprecated: use CETIRUN_HELM_VALUES with skills.storageAccount instead.
	skillsStorageAccount := os.Getenv("SKILLS_STORAGE_ACCOUNT")
	if skillsStorageAccount != "" {
		setupLog.Info("Overriding skills storage account", "storageAccount", skillsStorageAccount)
	}

	// Operator-level Helm value overrides applied to every instance.
	var cetirunHelmValues map[string]interface{}
	if raw := os.Getenv("CETIRUN_HELM_VALUES"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cetirunHelmValues); err != nil {
			setupLog.Error(err, "failed to parse CETIRUN_HELM_VALUES")
			os.Exit(1)
		}
		setupLog.Info("Loaded operator-level CetiRun Helm values", "keys", helmValueKeys(cetirunHelmValues))
	}

	sandboxSourcesConfig, err := controller.ParseSandboxSourcesConfig(os.Getenv("CETIRUN_SANDBOX_SOURCES"))
	if err != nil {
		setupLog.Error(err, "invalid Sandbox source configuration")
		os.Exit(1)
	}

	// Azure config for Teams auto-provisioning (optional — only needed when Teams is enabled).
	azureSubscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID")
	azureResourceGroup := os.Getenv("AZURE_RESOURCE_GROUP")
	azureBotNamePrefix := os.Getenv("AZURE_BOT_NAME_PREFIX")
	azureTenantID := os.Getenv("AZURE_TENANT_ID")
	azureServiceTreeID := os.Getenv("AZURE_SERVICE_TREE_ID")
	agentIdentityEnabled := strings.EqualFold(os.Getenv("AGENT_ID_ENABLED"), "true")
	agentIdentityCleanupEnabled := strings.EqualFold(os.Getenv("AGENT_ID_CLEANUP_ENABLED"), "true")
	agentIdentityTenantID := os.Getenv("AGENT_ID_TENANT_ID")
	agentIdentityBlueprintClientID := os.Getenv("AGENT_ID_BLUEPRINT_CLIENT_ID")
	agentIdentityNamePrefix := getEnvOrDefault("AGENT_ID_NAME_PREFIX", "cetirun-agent")
	agentIdentityBrokerURL := os.Getenv("AGENT_ID_BROKER_URL")
	if (agentIdentityEnabled || agentIdentityCleanupEnabled) && (agentIdentityTenantID == "" || agentIdentityBlueprintClientID == "" || agentIdentityBrokerURL == "") {
		setupLog.Info("Agent Identity requires tenant ID, Blueprint client ID, and Broker URL")
		os.Exit(1)
	}
	aiGatewayConfig, err := controller.ParseAIGatewayConfig(os.Getenv)
	if err != nil {
		setupLog.Error(err, "invalid AI Gateway configuration")
		os.Exit(1)
	}
	if aiGatewayConfig.Enabled {
		setupLog.Info("AI Gateway enabled", "url", aiGatewayConfig.URL,
			"defaultModel", aiGatewayConfig.DefaultModel, "githubCopilot", aiGatewayConfig.CopilotProxyImage != "")
	}

	// Auto-detect OIDC issuer URL from the cluster.
	azureOIDCIssuerURL := detectOIDCIssuer(setupLog)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Client: client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.Secret{}}}},
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "cetirun-operator.cetirun.microsoft.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}
	if metricsAddr != "0" {
		if err := controller.RegisterSandboxMetrics(metrics.Registry, mgr); err != nil {
			setupLog.Error(err, "unable to register Sandbox reported-status metrics")
			os.Exit(1)
		}
	}

	helmClient := helmclient.NewClient(chartRef)

	customConfig, err := controller.ParseCustomRuntimeConfig(os.Getenv("CETIRUN_CUSTOM_RUNTIME"))
	if err != nil {
		setupLog.Error(err, "invalid Custom runtime configuration")
		os.Exit(1)
	}
	agentReconciler := &controller.AgentReconciler{
		Client:                         mgr.GetClient(),
		Scheme:                         mgr.GetScheme(),
		EventRecorder:                  mgr.GetEventRecorderFor("cetirun-operator"),
		HelmClient:                     helmClient,
		OperatorNamespace:              getEnvOrDefault("OPERATOR_NAMESPACE", "cetirun-system"),
		OperatorSAName:                 getEnvOrDefault("OPERATOR_SA_NAME", "cetirun-operator-controller-manager"),
		ChartVersion:                   chartVersion,
		IstioRevision:                  istioRevision,
		IstioGateway:                   istioGateway,
		DomainSuffix:                   domainSuffix,
		SkillsStorageAccount:           skillsStorageAccount,
		CetiRunHelmValues:              cetirunHelmValues,
		ChartRegistry:                  chartRegistry,
		DefaultModel:                   defaultModel,
		RegistryCredentialNamespace:    getEnvOrDefault("REGISTRY_CREDENTIAL_NAMESPACE", registryauth.DefaultNamespace),
		AzureSubscriptionID:            azureSubscriptionID,
		AzureResourceGroup:             azureResourceGroup,
		AzureBotNamePrefix:             azureBotNamePrefix,
		AzureTenantID:                  azureTenantID,
		AzureServiceTreeID:             azureServiceTreeID,
		AzureOIDCIssuerURL:             azureOIDCIssuerURL,
		AgentIdentityEnabled:           agentIdentityEnabled,
		AgentIdentityTenantID:          agentIdentityTenantID,
		AgentIdentityBlueprintClientID: agentIdentityBlueprintClientID,
		AgentIdentityNamePrefix:        agentIdentityNamePrefix,
		AgentIdentityBrokerURL:         agentIdentityBrokerURL,
		AIGateway:                      aiGatewayConfig,
	}
	if customConfig.Enabled {
		agentReconciler.CustomBindings = controller.NewKubernetesCustomBindings(agentReconciler, customConfig)
	}
	agentReconciler.ChatAccess, err = chataccess.Parse(os.Getenv("CETIRUN_CHAT_ACCESS"))
	if err != nil {
		setupLog.Error(err, "invalid Chat access configuration")
		os.Exit(1)
	}
	agentReconciler.SandboxPreparation, err = controller.NewKubernetesSandboxPreparation(
		sandboxSourcesConfig, mgr.GetAPIReader(), helmClient, agentReconciler.OperatorNamespace, chartVersion,
	)
	if err != nil {
		setupLog.Error(err, "unable to configure Sandbox sources")
		os.Exit(1)
	}
	var sandboxSource controller.SandboxConfigurationSource
	if agentReconciler.SandboxPreparation != nil {
		sandboxSource = agentReconciler.SandboxPreparation.Configuration
		setupLog.Info("Kubernetes Sandbox sources enabled; qualified writer exclusion is unavailable and execution remains held")
	}
	if err = agentReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Agent")
		os.Exit(1)
	}
	if err := (&controller.SandboxReconciler{Source: sandboxSource}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Sandbox")
		os.Exit(1)
	}
	if err := (&controller.AgentPolicyReconciler{Source: sandboxSource}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AgentPolicy")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// extractRegistryHost extracts the registry hostname from an OCI chart URL.
// Example: "oci://registry.example.com/helm/cetirun" → "registry.example.com".
// Returns empty string if the URL cannot be parsed or the host is empty.
func extractRegistryHost(repoURL string) string {
	// Replace oci:// with https:// so net/url can parse it.
	parsed, err := url.Parse(strings.Replace(repoURL, "oci://", "https://", 1))
	if err != nil || parsed.Host == "" {
		return ""
	}
	return parsed.Host
}

// detectOIDCIssuer queries the in-cluster Kubernetes API server's OIDC discovery
// endpoint to determine the cluster's OIDC issuer URL. This is used as the issuer
// when creating Federated Identity Credentials for per-user bot apps.
// Returns empty string on failure (non-fatal — Teams provisioning will fail later
// with a clear error if the issuer is needed but not available).
func detectOIDCIssuer(logger logr.Logger) string {
	// In-cluster: the API server is at https://kubernetes.default.svc
	// Load the ServiceAccount CA bundle to verify the API server's TLS cert.
	caCert, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	tlsConfig := &tls.Config{}
	if err != nil {
		logger.Info("Could not load ServiceAccount CA (non-fatal, using system certs)", "error", err)
	} else {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(caCert) {
			tlsConfig.RootCAs = pool
		} else {
			logger.Info("Could not parse ServiceAccount CA (non-fatal, using system certs)")
		}
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	req, err := http.NewRequest(http.MethodGet, "https://kubernetes.default.svc/.well-known/openid-configuration", nil)
	if err != nil {
		logger.Info("Failed to create OIDC discovery request (non-fatal)", "error", err)
		return ""
	}

	// Some clusters require authentication for the OIDC discovery endpoint.
	saToken, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err == nil && len(saToken) > 0 {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(saToken)))
	}

	resp, err := client.Do(req)
	if err != nil {
		logger.Info("Failed to detect OIDC issuer (non-fatal)", "error", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Info("OIDC discovery returned non-200 (non-fatal)", "status", resp.StatusCode)
		return ""
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		logger.Info("Failed to read OIDC discovery response (non-fatal)", "error", err)
		return ""
	}

	var discovery struct {
		Issuer string `json:"issuer"`
	}
	if err := json.Unmarshal(body, &discovery); err != nil {
		logger.Info("Failed to parse OIDC discovery response (non-fatal)", "error", err)
		return ""
	}

	if discovery.Issuer == "" {
		logger.Info("OIDC discovery response has empty issuer (non-fatal)")
		return ""
	}

	logger.Info("Detected OIDC issuer URL", "issuer", discovery.Issuer)
	return discovery.Issuer
}

// helmValueKeys returns the top-level keys of a map for logging purposes.
func helmValueKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
