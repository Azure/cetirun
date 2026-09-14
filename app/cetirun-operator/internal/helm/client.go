// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ChartReference describes how to locate and load a Helm chart from an OCI registry.
type ChartReference struct {
	// RepoURL is the OCI registry URL (e.g., "oci://cetirun.azurecr.io/helm/cetirun").
	RepoURL string

	// Version is the semantic version of the chart (e.g., "0.1.0").
	Version string
}

// Validate checks whether the ChartReference configuration is valid.
func (ref *ChartReference) Validate() error {
	if ref.RepoURL == "" {
		return fmt.Errorf("RepoURL must be specified")
	}
	if !strings.HasPrefix(ref.RepoURL, "oci://") {
		return fmt.Errorf("OCI registry URL must start with oci://")
	}
	if ref.Version == "" {
		return fmt.Errorf("Version must be specified")
	}
	return nil
}

// Client wraps the Helm action configuration and provides high-level install/upgrade/uninstall operations.
type Client struct {
	chartRef ChartReference
}

// NewClient creates a new Helm client that loads the chart based on the provided ChartReference.
func NewClient(chartRef ChartReference) *Client {
	return &Client{chartRef: chartRef}
}

// loadChart pulls the chart from the OCI registry.
// cfg must have RegistryClient set (done by actionConfig) so that LocateChart
// can authenticate with the OCI registry.
// version overrides the default chart version; if empty, c.chartRef.Version is used.
func (c *Client) loadChart(cfg *action.Configuration, namespace, version string) (*chart.Chart, error) {
	if version == "" {
		version = c.chartRef.Version
	}
	logger := log.Log.WithName("helm").WithValues("namespace", namespace)
	chartURL := fmt.Sprintf("%s:%s", c.chartRef.RepoURL, version)
	logger.Info("Pulling chart from OCI registry", "url", chartURL)

	// action.NewInstall copies cfg.RegistryClient into ChartPathOptions.registryClient,
	// which is the private field that LocateChart checks for OCI references.
	install := action.NewInstall(cfg)
	install.Version = version

	chartPath, err := install.LocateChart(chartURL, cli.New())
	if err != nil {
		return nil, fmt.Errorf("locate OCI chart %s: %w", chartURL, err)
	}

	chrt, err := loader.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("load chart from %s: %w", chartPath, err)
	}

	logger.Info("Successfully loaded chart from OCI registry",
		"chartName", chrt.Name(),
		"chartVersion", chrt.Metadata.Version)

	return chrt, nil
}

// actionConfig returns a Helm action.Configuration scoped to the given namespace.
// It initialises a registry client so that OCI chart references can be resolved.
func actionConfig(namespace string) (*action.Configuration, error) {
	cfg := new(action.Configuration)
	// Use the in-cluster REST config.
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("get in-cluster config: %w", err)
	}

	logger := log.Log.WithName("helm")
	logFn := func(format string, v ...interface{}) {
		logger.V(1).Info(fmt.Sprintf(format, v...))
	}

	if err := cfg.Init(restClientGetter(restConfig, namespace), namespace, "secret", logFn); err != nil {
		return nil, fmt.Errorf("init helm action config: %w", err)
	}

	// Initialise the OCI registry client.
	// Prefer the docker credentials file so that ACR access granted via the
	// node's managed identity (written to /root/.docker/config.json by kubelet)
	// is picked up automatically.
	registryClient, err := newRegistryClient()
	if err != nil {
		return nil, fmt.Errorf("init registry client: %w", err)
	}
	cfg.RegistryClient = registryClient

	return cfg, nil
}

// newRegistryClient creates a Helm registry client that reads credentials from
// the docker config file.  It checks the standard locations in order:
//  1. $DOCKER_CONFIG/config.json
//  2. /root/.docker/config.json (AKS kubelet default)
//  3. Helm's own registry config (~/.config/helm/registry/config.json)
func newRegistryClient() (*registry.Client, error) {
	credentialsFile := dockerConfigFile()

	opts := []registry.ClientOption{
		registry.ClientOptEnableCache(true),
	}
	if credentialsFile != "" {
		opts = append(opts, registry.ClientOptCredentialsFile(credentialsFile))
	}

	return registry.NewClient(opts...)
}

// dockerConfigFile returns the path to a docker config.json that should be
// used for registry credentials, or empty string to rely on Helm's default.
func dockerConfigFile() string {
	// Honour the standard DOCKER_CONFIG env override.
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		cfgPath := filepath.Join(dir, "config.json")
		if fi, err := os.Stat(cfgPath); err == nil && !fi.IsDir() {
			return cfgPath
		}
	}
	// AKS writes the ACR token here when the node's managed identity has
	// AcrPull permissions.
	const aksDockerConfig = "/root/.docker/config.json"
	if fi, err := os.Stat(aksDockerConfig); err == nil && !fi.IsDir() {
		return aksDockerConfig
	}
	// Fall through: registry.NewClient will use Helm's own credentials file.
	return ""
}

// InstallOrUpgrade installs a new Helm release or upgrades an existing one.
// Wait is intentionally disabled: the operator does not block on pod readiness —
// it only ensures the chart resources are submitted to the API server.
func (c *Client) InstallOrUpgrade(namespace, releaseName string, values map[string]interface{}, chartVersion string) error {
	cfg, err := actionConfig(namespace)
	if err != nil {
		return err
	}

	chrt, err := c.loadChart(cfg, namespace, chartVersion)
	if err != nil {
		return err
	}

	// TODO: ReleaseExists calls actionConfig (and thus InClusterConfig) a second time.
	// Refactor InstallOrUpgrade to pass cfg into ReleaseExists to avoid the duplicate
	// in-cluster config lookup and Helm driver initialisation.
	exists, err := c.ReleaseExists(namespace, releaseName)
	if err != nil {
		return err
	}

	if exists {
		upgrade := action.NewUpgrade(cfg)
		upgrade.Namespace = namespace
		upgrade.ReuseValues = false
		upgrade.Wait = false
		upgrade.MaxHistory = 10
		if _, err := upgrade.Run(releaseName, chrt, values); err != nil {
			return fmt.Errorf("helm upgrade %s: %w", releaseName, err)
		}
	} else {
		install := action.NewInstall(cfg)
		install.Namespace = namespace
		install.ReleaseName = releaseName
		install.CreateNamespace = false
		install.Wait = false
		_, err := install.Run(chrt, values)
		if err != nil {
			// Another reconcile loop may have raced us to the install.
			// Treat "already exists" as success — the release is there.
			if strings.Contains(err.Error(), "already exists") {
				return nil
			}
			return fmt.Errorf("helm install %s: %w", releaseName, err)
		}
	}
	return nil
}

// Uninstall removes a Helm release.
func (c *Client) Uninstall(namespace, releaseName string) error {
	cfg, err := actionConfig(namespace)
	if err != nil {
		return err
	}

	exists, err := c.ReleaseExists(namespace, releaseName)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	uninstall := action.NewUninstall(cfg)
	if _, err := uninstall.Run(releaseName); err != nil {
		return fmt.Errorf("helm uninstall %s: %w", releaseName, err)
	}
	return nil
}

// ReleaseExists reports whether a Helm release with releaseName exists in namespace.
// A failed release is considered to exist (it will be upgraded, not re-installed).
func (c *Client) ReleaseExists(namespace, releaseName string) (bool, error) {
	cfg, err := actionConfig(namespace)
	if err != nil {
		return false, err
	}

	histClient := action.NewHistory(cfg)
	histClient.Max = 1
	releases, err := histClient.Run(releaseName)
	if err != nil {
		if err == driver.ErrReleaseNotFound {
			return false, nil
		}
		return false, fmt.Errorf("helm history %s: %w", releaseName, err)
	}
	if len(releases) == 0 {
		return false, nil
	}
	status := releases[len(releases)-1].Info.Status
	// Treat uninstalled as not existing; everything else (deployed, failed, pending) as existing.
	return status != release.StatusUninstalled, nil
}
