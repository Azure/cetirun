// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import "testing"

func TestMemoryRegistryPreservesPublicationPrefix(t *testing.T) {
	for _, test := range []struct{ repo, registry string }{
		{"oci://registry.example.com/helm/cetirun", "registry.example.com"},
		{"oci://registry.example.com:5000/project/staging/123/helm/cetirun", "registry.example.com:5000/project/staging/123"},
		{"oci://registry.example.com/other/runtime", "registry.example.com"},
	} {
		if got := memoryRegistry(test.repo); got != test.registry {
			t.Errorf("memoryRegistry(%q) = %q, want %q", test.repo, got, test.registry)
		}
	}
}
