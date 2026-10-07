// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package auth

import (
	"net/http"
	"strings"
)

// AdminSet is the set of aliases authorized to access /api/v1/admin/* and
// the /admin SPA section. Aliases are stored lowercased; lookups are
// case-insensitive.
type AdminSet map[string]struct{}

// NewVerifiedAdminSet creates an administrator set from immutable, verified
// Entra tenant/object ID pairs.  Alias based authorization is intentionally
// not used by Governance endpoints.
func NewVerifiedAdminSet(subjects [][2]string) AdminSet {
	s := AdminSet{}
	for _, subject := range subjects {
		tenant, object := strings.TrimSpace(subject[0]), strings.TrimSpace(subject[1])
		if tenant == "" || object == "" {
			continue
		}
		s[subjectKey(tenant, object)] = struct{}{}
	}
	return s
}

func subjectKey(tenantID, objectID string) string {
	return strings.ToLower(strings.TrimSpace(tenantID)) + "/" + strings.ToLower(strings.TrimSpace(objectID))
}

// NewAdminSet constructs an AdminSet from a slice of aliases. Empty entries
// and surrounding whitespace are ignored; entries are lowercased; duplicates
// are deduplicated.
func NewAdminSet(aliases []string) AdminSet {
	s := AdminSet{}
	for _, a := range aliases {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		s[a] = struct{}{}
	}
	return s
}

// Contains reports whether alias is in the admin set.
func (s AdminSet) Contains(alias string) bool {
	if s == nil || alias == "" {
		return false
	}
	_, ok := s[strings.ToLower(alias)]
	return ok
}

// ContainsSubject reports whether a cryptographically verified Entra subject
// is a platform administrator.  Both fields must be present: an unverified
// proxy header can never grant administration access.
func (s AdminSet) ContainsSubject(tenantID, objectID string) bool {
	if s == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(objectID) == "" {
		return false
	}
	_, ok := s[subjectKey(tenantID, objectID)]
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
