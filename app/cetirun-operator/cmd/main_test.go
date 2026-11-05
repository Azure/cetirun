// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import "testing"

func TestRuntimeRegistryPreservesPublicationPrefix(t *testing.T) {
	for _, test := range []struct{ repo, registry string }{
		{"oci://registry.example.com/helm/cetirun", "registry.example.com"},
		{"oci://mcr.microsoft.com/aks/ai-runtime/cetirun/helm/cetirun", "mcr.microsoft.com/aks/ai-runtime/cetirun"},
		{"oci://registry.example.com:5000/project/staging/123/helm/cetirun", "registry.example.com:5000/project/staging/123"},
		{"oci://registry.example.com/other/runtime", "registry.example.com"},
	} {
		if got := runtimeRegistry(test.repo); got != test.registry {
			t.Errorf("runtimeRegistry(%q) = %q, want %q", test.repo, got, test.registry)
		}
	}
}
