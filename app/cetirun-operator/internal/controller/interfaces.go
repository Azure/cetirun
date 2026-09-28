// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import "github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"

// HelmInstaller abstracts Helm install/upgrade/uninstall operations.
// The concrete helm.Client satisfies this interface.
type HelmInstaller interface {
	Prepare(namespace string, values map[string]any, chartVersion string) (*helm.PreparedRelease, error)
	Refresh(prepared *helm.PreparedRelease, values map[string]any) (*helm.PreparedRelease, error)
	InstallOrUpgrade(namespace, releaseName string, prepared *helm.PreparedRelease) error
	CustomApplied(namespace, releaseName string, prepared *helm.PreparedRelease) (bool, error)
	Uninstall(namespace, releaseName string) error
}
