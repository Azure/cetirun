// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package agenttools

import "testing"

func TestAzureServiceUsesFixedARMOriginAndAudience(t *testing.T) {
	service, ok := LookupService("azure_arm")
	if !ok || service.Provider != "azure" || service.Origin != "https://management.azure.com" || service.Audience != "https://management.azure.com/" {
		t.Fatalf("invalid ARM profile: %+v", service)
	}
	for _, name := range []string{"https://attacker.example", "github", "microsoft_graph", "google_gmail"} {
		if _, ok := LookupService(name); ok {
			t.Fatalf("unavailable service exposed: %s", name)
		}
	}
	services := Services()
	services[0].Origin = "https://attacker.example"
	if service, _ := LookupService("azure_arm"); service.Origin != "https://management.azure.com" {
		t.Fatal("caller mutated trusted registry")
	}
}
