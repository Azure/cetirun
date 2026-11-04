// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
)

func (r *AgentReconciler) memoryOptions(agentType api.AgentType) helm.MemoryOptions {
	image := r.MemoryImage
	if agentType == api.AgentTypeHermes && r.HermesMemoryImage != "" {
		image = r.HermesMemoryImage
	}
	return helm.MemoryOptions{Enabled: r.MemoryEnabled || image != "", Image: image, Registry: r.MemoryRegistry}
}
