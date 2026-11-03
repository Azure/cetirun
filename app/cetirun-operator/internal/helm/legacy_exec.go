// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"fmt"
	"maps"
)

func (p *PreparedRelease) validateCustomManifest(namespace, release string, manifest []byte) error {
	return p.validateCustomManifestMode(namespace, release, manifest, false)
}

// Only source renders can retire the fixed legacy exception. Sealed manifests
// and stored application proofs must already satisfy the current contract.
func (p *PreparedRelease) validateCustomRenderedManifest(namespace, release string, manifest []byte) error {
	return p.validateCustomManifestMode(namespace, release, manifest, true)
}

func (p *PreparedRelease) retireLegacyExecException(obj map[string]any, namespace string) error {
	if obj["kind"] != "ValidatingAdmissionPolicy" {
		return nil
	}
	spec := object(obj["spec"])
	if _, exists := spec["matchConditions"]; !exists {
		return nil
	}
	expected, exists := p.customPlatformObjects(namespace)["ValidatingAdmissionPolicy"]
	if !exists || p.sandboxBundle != nil || p.sandboxReconcile != nil || p.Values["sandboxWorkload"] != nil {
		return fmt.Errorf("legacy exec conversion requires the ordinary Custom policy")
	}
	legacy := maps.Clone(expected.spec)
	legacy["matchConditions"] = []any{map[string]any{
		"name":       "exclude-adminclaw",
		"expression": `request.userInfo.username != "system:serviceaccount:cetirun-admin:openclaw-admin-cetirun"`,
	}}
	metadata := object(obj["metadata"])
	if obj["apiVersion"] != expected.apiVersion || metadata["name"] != expected.name || text(metadata["namespace"]) != "" || !sameJSON(spec, legacy) {
		return fmt.Errorf("legacy exec policy does not match the fixed platform contract")
	}
	delete(spec, "matchConditions")
	// The caller still validates the complete object, ownership, binding,
	// workload and remaining manifest before hashing or sealing any output.
	return nil
}
