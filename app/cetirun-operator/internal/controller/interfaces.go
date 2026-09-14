package controller

// HelmInstaller abstracts Helm install/upgrade/uninstall operations.
// The concrete helm.Client satisfies this interface.
type HelmInstaller interface {
	InstallOrUpgrade(namespace, releaseName string, values map[string]any, chartVersion string) error
	Uninstall(namespace, releaseName string) error
}
