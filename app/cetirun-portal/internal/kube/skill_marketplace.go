// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	stderrors "errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	strictjson "sigs.k8s.io/json"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

// maxSkillMarketplaceCatalogBytes bounds the catalog document the Portal
// decodes. A ConfigMap cannot hold more anyway.
const maxSkillMarketplaceCatalogBytes = 1 << 20

// GetSkillMarketplaceCatalog reads the catalog the Skill Marketplace
// publishes in the operator namespace, uncached. It reports false, without an
// error, while the marketplace has not published one.
func (c *Client) GetSkillMarketplaceCatalog(ctx context.Context, namespace string) (cetirunv1alpha1.SkillMarketplaceCatalog, bool, error) {
	configMap := &corev1.ConfigMap{}
	err := c.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: cetirunv1alpha1.SkillMarketplaceCatalogConfigMap}, configMap)
	if errors.IsNotFound(err) {
		return cetirunv1alpha1.SkillMarketplaceCatalog{}, false, nil
	}
	if err != nil {
		return cetirunv1alpha1.SkillMarketplaceCatalog{}, false, err
	}
	raw, ok := configMap.Data[cetirunv1alpha1.SkillMarketplaceCatalogKey]
	if !ok {
		return cetirunv1alpha1.SkillMarketplaceCatalog{}, false, nil
	}
	catalog, err := decodeSkillMarketplaceCatalog(raw)
	if err != nil {
		return cetirunv1alpha1.SkillMarketplaceCatalog{}, false, fmt.Errorf("decode the Skill Marketplace catalog: %w", err)
	}
	return catalog, true, nil
}

// decodeSkillMarketplaceCatalog accepts one JSON object of the supported
// catalog version, with case-sensitive field names, no duplicate fields and
// at most maxSkillMarketplaceCatalogBytes. Fields this Portal does not know
// are ignored, so a newer marketplace can add them within the same version.
func decodeSkillMarketplaceCatalog(raw string) (cetirunv1alpha1.SkillMarketplaceCatalog, error) {
	var catalog cetirunv1alpha1.SkillMarketplaceCatalog
	if len(raw) > maxSkillMarketplaceCatalogBytes {
		return catalog, fmt.Errorf("it is longer than %d bytes", maxSkillMarketplaceCatalogBytes)
	}
	strict, err := strictjson.UnmarshalStrict([]byte(raw), &catalog, strictjson.DisallowDuplicateFields)
	if err == nil {
		err = stderrors.Join(strict...)
	}
	if err != nil {
		return cetirunv1alpha1.SkillMarketplaceCatalog{}, err
	}
	if catalog.Version != cetirunv1alpha1.SkillMarketplaceCatalogVersion {
		return cetirunv1alpha1.SkillMarketplaceCatalog{}, fmt.Errorf("version %q is not the supported version %q",
			catalog.Version, cetirunv1alpha1.SkillMarketplaceCatalogVersion)
	}
	return catalog, nil
}
