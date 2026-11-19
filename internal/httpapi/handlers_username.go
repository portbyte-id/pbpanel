package httpapi

import (
	"context"
	"net/http"
	"strings"
)

// currentUsername is the admin account's name as stored now, falling back to
// Deps.Actor when the row cannot be read. Deps.Actor is fixed at startup, so a
// rename from the account dialog would otherwise leave /api/me, the header and
// the audit trail naming the old account until the daemon restarted.
//
// The fallback is the startup name rather than an error: a read failure here
// must not turn a page load into a 500, and the session that reached this point
// was already authenticated.
func (s *server) currentUsername(ctx context.Context) string {
	name, err := s.deps.Auth.Username(ctx)
	if err != nil || name == "" {
		return s.deps.Actor
	}
	return name
}

// handleUsername renames the admin account after re-checking the current
// password, then revokes every session. A rename is a credential change, so it
// ends in the same place as a password change: the client must sign in again
// with the new name.
func (s *server) handleUsername(w http.ResponseWriter, r *http.Request) {
	var req usernameRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	name := strings.TrimSpace(req.NewUsername)
	if req.CurrentPassword == "" || name == "" {
		writeError(w, http.StatusBadRequest, errBadRequest, "current_password and new_username are required")
		return
	}
	if len(name) > maxUsernameLen {
		writeError(w, http.StatusBadRequest, errBadRequest,
			"the new username must be at most "+itoa(maxUsernameLen)+" characters")
		return
	}

	if !s.proveCurrentPassword(w, r, req.CurrentPassword) {
		return
	}

	if err := s.deps.Auth.SetUsername(r.Context(), name); err != nil {
		s.writeInternal(w, r, err)
		return
	}

	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
