package handler

import (
	"net/http"

	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
)

// MeHandler serves /api/v1/me — returns the current user's identity and
// admin flag, derived from OAuth2 Proxy headers and the configured admin
// allowlist.
type MeHandler struct{}

// MeResponse is the JSON shape of GET /api/v1/me.
type MeResponse struct {
	Alias       string `json:"alias"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	IsAdmin     bool   `json:"isAdmin"`
}

// GetMe handles GET /api/v1/me.
func (h *MeHandler) GetMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not_authenticated", "Missing user claims")
		return
	}
	writeJSON(w, http.StatusOK, MeResponse{
		Alias:       claims.Alias,
		DisplayName: claims.DisplayName,
		Email:       claims.Email,
		IsAdmin:     claims.IsAdmin,
	})
}
