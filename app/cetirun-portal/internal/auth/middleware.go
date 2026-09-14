// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package auth

import (
	"encoding/json"
	"net/http"
)

// Middleware returns an HTTP middleware that extracts user info from OAuth2 Proxy headers.
// Group-based authorization is enforced by OAuth2 Proxy at the gateway level
// (via --allowed-group), so no group check is performed here.
//
// admins is the set of aliases granted admin privileges. If nil/empty, no
// user is admin.
func Middleware(admins AdminSet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// OAuth2 Proxy sets these headers after successful authentication.
			preferredUsername := r.Header.Get("X-Auth-Request-Preferred-Username")
			email := r.Header.Get("X-Auth-Request-Email")

			// At least one identity header must be present.
			if preferredUsername == "" && email == "" {
				writeError(w, http.StatusUnauthorized, "not_authenticated",
					"Missing authentication headers from OAuth2 Proxy")
				return
			}

			// Extract alias from preferred_username (UPN), falling back to email.
			// In the Microsoft tenant, preferred_username is the UPN/alias
			// (e.g. user@example.com), while email is the display-name
			// format (e.g. User.Name@example.com).
			identity := preferredUsername
			if identity == "" {
				identity = email
			}
			alias := aliasFromUPN(identity)
			if alias == "" {
				writeError(w, http.StatusForbidden, "invalid_identity",
					"Invalid user identity: must be a lowercase DNS-1123 label (start with a letter, contain only letters, digits, or '-', end with a letter or digit, max 63 characters)")
				return
			}

			if email == "" {
				email = identity
			}

			// X-Auth-Request-User is a hashed subject ID with the entra-id
			// provider, not a human-readable name. Use preferred_username
			// (UPN) as a readable fallback for DisplayName.
			displayName := preferredUsername
			if displayName == "" {
				displayName = email
			}

			claims := Claims{
				Alias:       alias,
				DisplayName: displayName,
				Email:       email,
				IsAdmin:     admins.Contains(alias),
			}

			ctx := NewContext(r.Context(), claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
