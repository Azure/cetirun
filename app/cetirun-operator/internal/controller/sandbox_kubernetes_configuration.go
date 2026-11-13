// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"k8s.io/apimachinery/pkg/types"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxplatform"
)

const (
	SandboxPlatformConfigurationKey      = sandboxplatform.ConfigurationKey
	MaxSandboxPlatformConfigurationBytes = sandboxplatform.MaxConfigurationBytes
)

type SandboxPlatformConfiguration = sandboxplatform.Configuration

type KubernetesSandboxConfigurationSource = sandboxplatform.KubernetesSource

// SandboxPlatformRegistration returns the ConfigMap of the platform
// registration that source reads. It returns the zero key when source does not
// read a Kubernetes ConfigMap.
func SandboxPlatformRegistration(source SandboxConfigurationSource) types.NamespacedName {
	kubernetes, ok := source.(*KubernetesSandboxConfigurationSource)
	if !ok || kubernetes == nil {
		return types.NamespacedName{}
	}
	return types.NamespacedName{Namespace: kubernetes.Namespace, Name: kubernetes.Name}
}
