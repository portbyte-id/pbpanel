package httpapi

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/portbyte-id/server-panel/internal/auth"
)

// loginRequest is the POST /api/login body.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// Challenge and Nonce are a solved browser check. An address with a failed
	// login on record must send them; see handleLoginChallenge.
	Challenge string `json:"challenge"`
	Nonce     string `json:"nonce"`
}

// loginChallengeResponse tells the login page whether the next attempt needs a
// browser check and, if it does, hands over a puzzle to solve.
type loginChallengeResponse struct {
	Required   bool   `json:"required"`
	Challenge  string `json:"challenge,omitempty"`
	Difficulty int    `json:"difficulty,omitempty"`
}

// loginResponse is what a successful login returns. The session itself travels
// in the cookie; the CSRF token is in the body because the client has to send it
// back in a header on every unsafe method, and a page cannot read a cookie.
type loginResponse struct {
	Username  string `json:"username"`
	CSRFToken string `json:"csrf_token"`
}

// meResponse is the GET /api/me body.
type meResponse struct {
	Username  string `json:"username"`
	CSRFToken string `json:"csrf_token"`
}

// handleLogin verifies the credentials, opens a session and sets the cookie.
func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, errBadRequest, "username and password are required")
		return
	}

	ip, ua := clientIP(r), r.UserAgent()
	// After a failed login the next attempt must carry a fresh, solved check. It
	// is spent before the password is verified, so a script that skips the page
	// pays the proof of work and gets no argon2 work for it. A locked-out address
	// is not asked for one: VerifyLogin answers 429 for it.
	required, err := s.deps.Auth.LoginChallengeRequired(r.Context(), ip)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	if required {
		if _, locked := s.deps.Auth.Throttle().LockedUntil(ip); !locked {
			if err := s.checks.redeem(ip, ua, req.Challenge, req.Nonce); err != nil {
				writeError(w, http.StatusForbidden, errChallengeRequired, checkDetail)
				return
			}
		}
	}

	session, err := s.deps.Auth.VerifyLogin(r.Context(), req.Username, req.Password, ip, ua)
	if err != nil {
		s.writeLoginError(w, r, err)
		return
	}

	// The cookie lives as long as the browser session and is re-issued with the
	// same attributes on every login, so a stale one from an earlier session
	// cannot keep weaker flags.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, loginResponse{Username: req.Username, CSRFToken: session.CSRFToken})
}

// handleLoginChallenge says whether the next login from this address needs a
// browser check. It is public, because the login page asks before anyone has
// signed in. When a check is needed it also issues a puzzle; the page fetches a
// new one each time the visitor ticks the box, so a puzzle does not go stale
// while the page sits open.
func (s *server) handleLoginChallenge(w http.ResponseWriter, r *http.Request) {
	ip, ua := clientIP(r), r.UserAgent()
	required, err := s.deps.Auth.LoginChallengeRequired(r.Context(), ip)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	if !required {
		writeJSON(w, http.StatusOK, loginChallengeResponse{})
		return
	}
	token, err := s.checks.issue(ip, ua)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, loginChallengeResponse{Required: true, Challenge: token, Difficulty: loginDifficulty})
}

// writeLoginError turns the auth package's failures into the responses the API
// promises. A lockout is a 429 with Retry-After, so a client can wait instead of
// hammering the throttle; every other failure is one 401 that does not say
// whether the username or the password was wrong.
func (s *server) writeLoginError(w http.ResponseWriter, r *http.Request, err error) {
	var lockout *auth.LockoutError
	if errors.As(err, &lockout) {
		secs := int64(math.Ceil(time.Until(lockout.RetryAt).Seconds()))
		if secs > 0 {
			w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
		}
		writeError(w, http.StatusTooManyRequests, errTooMany, "too many failed logins, try again later")
		return
	}
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, errInvalidLogin, "username or password is incorrect")
		return
	}
	s.writeInternal(w, r, err)
}

// handleLogout revokes the session making the request and clears the cookie.
// It is idempotent: a request whose session vanished between the middleware and
// here still succeeds, because the client's intent ("end this session") is met
// either way.
func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	session, ok := sessionFrom(r.Context())
	if ok {
		if err := s.deps.Auth.Sessions().Revoke(r.Context(), session.ID); err != nil {
			s.writeInternal(w, r, err)
			return
		}
	}

	// Deleting the cookie needs the same Path as the one that set it, or the
	// browser keeps the original alongside a second, empty cookie.
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

// handleMe returns who is logged in and the CSRF token the SPA needs after a
// reload, when it has a cookie but no in-memory token.
func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	session, _ := sessionFrom(r.Context())
	writeJSON(w, http.StatusOK, meResponse{Username: s.currentUsername(r.Context()), CSRFToken: session.CSRFToken})
}
