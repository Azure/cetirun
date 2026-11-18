// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package auth

import (
	"net/http"
	"strings"
)

// AdminSet contains object IDs from the configured Entra tenant, or aliases
// for deployments without a token verifier. The two lists are never combined.
type AdminSet map[string]struct{}

// NewAdminSet constructs an AdminSet from a slice of identifiers. Empty entries
// and surrounding whitespace are ignored; entries are lowercased; duplicates
// are deduplicated.
func NewAdminSet(identifiers []string) AdminSet {
	s := AdminSet{}
	for _, a := range identifiers {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		s[a] = struct{}{}
	}
	return s
}

// Contains reports whether the identifier is in the admin set.
func (s AdminSet) Contains(identifier string) bool {
	if s == nil || identifier == "" {
		return false
	}
	_, ok := s[strings.ToLower(identifier)]
	return ok
}

// RequireAdmin returns middleware that rejects non-admin requests with 403.
// It assumes Middleware() ran first and populated Claims in context.
func RequireAdmin() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok || !claims.IsAdmin {
				writeError(w, http.StatusForbidden, "forbidden_admin",
					"Admin access required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
