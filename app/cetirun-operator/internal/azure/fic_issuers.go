package azure

import (
	"encoding/json"
	"os"
	"strings"
)

// FICIssuerIDs maps Azure AD tenant IDs to their pre-approved FIC Issuer IDs.
// These are required for the Graph beta API trustedIssuer@odata.bind property
// when creating Federated Identity Credentials in Microsoft productivity tenants.
//
// Populated from the FIC_ISSUER_IDS environment variable, which should be a
// comma-separated list of tenant-id=issuer-id pairs, or a JSON object.
//
// Example (comma-separated):
//
//	FIC_ISSUER_IDS="tenant1=issuer1,tenant2=issuer2"
//
// Example (JSON):
//
//	FIC_ISSUER_IDS='{"tenant1":"issuer1","tenant2":"issuer2"}'
var FICIssuerIDs = loadFICIssuerIDs()

func loadFICIssuerIDs() map[string]string {
	raw := os.Getenv("FIC_ISSUER_IDS")
	if raw == "" {
		return map[string]string{}
	}

	// Try JSON first.
	m := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &m); err == nil {
		return m
	}

	// Fall back to comma-separated key=value pairs.
	result := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(parts) == 2 {
			result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return result
}

// FICIssuerBindURL returns the trustedIssuer@odata.bind URL for a given tenant ID.
// Returns empty string if the tenant is not in the lookup table.
func FICIssuerBindURL(tenantID string) string {
	issuerID, ok := FICIssuerIDs[tenantID]
	if !ok {
		return ""
	}
	return "https://graph.microsoft.com/beta/identity/federatedIdentityCredentialIssuers/" + issuerID
}
