package auth

import (
	"context"
	"regexp"
	"strings"
)

// Claims represents the authenticated user extracted from OAuth2 Proxy headers.
type Claims struct {
	// Alias is the user's short name, extracted from the X-Auth-Request-Preferred-Username
	// header by stripping the @domain.com suffix. Used as the OpenClawInstance name.
	Alias string

	// DisplayName is a human-readable identifier for the user, derived from
	// the preferred_username (UPN) header. X-Auth-Request-User is NOT used
	// because the entra-id provider populates it with a hashed subject ID.
	DisplayName string

	// Email is the user's email from the X-Auth-Request-Email header.
	Email string

	// IsAdmin is true when the user's alias matches the configured admin
	// allowlist. Used by the RequireAdmin middleware and the /api/v1/me
	// endpoint to gate the admin portal.
	IsAdmin bool
}

type claimsKey struct{}

// NewContext returns a new context with the given Claims.
func NewContext(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

// ClaimsFromContext extracts Claims from the request context.
func ClaimsFromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(Claims)
	return c, ok
}

// DNS-1123 label pattern: start with letter, end with letter or digit,
// middle can contain letters, digits, or hyphens. Max 63 chars.
var aliasPattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// aliasFromUPN extracts the alias from a UPN like "alice@example.com"
// and converts it to lowercase. Returns empty string if the result doesn't
// match the CRD pattern ^[a-z][a-z0-9-]{0,62}$.
func aliasFromUPN(upn string) string {
	local := upn
	if idx := strings.Index(upn, "@"); idx > 0 {
		local = upn[:idx]
	}
	alias := strings.ToLower(local)

	// Validate against CRD pattern
	if !aliasPattern.MatchString(alias) {
		return ""
	}

	return alias
}
