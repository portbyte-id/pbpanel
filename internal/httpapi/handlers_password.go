package httpapi

import (
	"net/http"
)

// minPasswordLen is the floor for a new password. Length is the only strength
// rule enforced here: composition rules push people towards predictable
// substitutions, while a longer passphrase is both stronger and easier to
// remember. The client shows the same number, so a rejection is not a surprise.
const minPasswordLen = 12

// passwordRequest is the POST /api/password body.
type passwordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handlePassword changes the admin password after re-checking the current one.
//
// SetPassword revokes every session, including the one making this request, so
// the cookie is cleared and the client must log in again. That is the point of
// decision 5: a replaced password must not leave old cookies working, and
// silently sparing the caller's own session would break that promise.
func (s *server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, errBadRequest, "current_password and new_password are required")
		return
	}
	if len(req.NewPassword) < minPasswordLen {
		writeError(w, http.StatusBadRequest, errBadRequest,
			"the new password must be at least "+itoa(minPasswordLen)+" characters")
		return
	}

	// The current password is proved through VerifyLogin rather than a bare hash
	// check so a wrong one is throttled exactly like a failed login. VerifyLogin
	// opens a session on success; it is revoked below along with all the others,
	// and never reaches a cookie.
	session, err := s.deps.Auth.VerifyLogin(r.Context(), s.deps.Actor, req.CurrentPassword, clientIP(r), r.UserAgent())
	if err != nil {
		// A lockout here is the same condition as a login lockout, so it gets the
		// same answer: 429 with Retry-After.
		s.writeLoginError(w, r, err)
		return
	}
	// Revoke the session VerifyLogin just made before SetPassword revokes the
	// rest, so this check never leaves a live session behind on a failure path.
	if err := s.deps.Auth.Sessions().Revoke(r.Context(), session.ID); err != nil {
		s.writeInternal(w, r, err)
		return
	}

	if err := s.deps.Auth.SetPassword(r.Context(), s.deps.Actor, req.NewPassword); err != nil {
		s.writeInternal(w, r, err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}
