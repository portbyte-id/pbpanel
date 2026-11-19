package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestLoginHappyPath proves the login contract: a 200, the cookie flags a
// browser needs, and a CSRF token in the body that the session actually accepts.
func TestLoginHappyPath(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": testPassword})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var resp loginResponse
	h.decode(rec, &resp)
	if resp.Username != testUser {
		t.Errorf("username = %q, want %q", resp.Username, testUser)
	}
	if resp.CSRFToken == "" {
		t.Error("login returned no CSRF token")
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login set %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != sessionCookieName {
		t.Errorf("cookie name = %q, want %q", c.Name, sessionCookieName)
	}
	if !c.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
	if !c.Secure {
		t.Error("session cookie is not Secure")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("cookie path = %q, want /", c.Path)
	}
	// The password must not travel back out in any form.
	if c.Value == testPassword || resp.CSRFToken == testPassword {
		t.Error("login echoed the password back")
	}
}

// TestLoginWrongPasswordIs401 proves a wrong password is one 401 that does not
// say which half was wrong.
func TestLoginWrongPasswordIs401(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": "wrong password here"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	if env := h.errorEnvelopeOf(rec); env.Error != errInvalidLogin {
		t.Errorf("error = %q, want %q", env.Error, errInvalidLogin)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a failed login set a cookie")
	}
}

// TestLoginUnknownUserLooksLikeWrongPassword proves the unknown-username path is
// indistinguishable from the wrong-password path in status and message.
func TestLoginUnknownUserLooksLikeWrongPassword(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": "not-admin", "password": testPassword})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	if env := h.errorEnvelopeOf(rec); env.Error != errInvalidLogin {
		t.Errorf("error = %q, want %q", env.Error, errInvalidLogin)
	}
}

// TestLoginLockoutIs429 proves the throttle surfaces as 429 with Retry-After,
// which is what lets a client wait instead of hammering.
func TestLoginLockoutIs429(t *testing.T) {
	h := newHarness(t)

	// The harness configures five attempts before lockout. The first four are
	// ordinary 401s; the fifth failure is the one that opens the lockout, so it
	// already answers 429.
	for i := range 4 {
		rec := h.do(http.MethodPost, "/api/login", "", "",
			map[string]string{"username": testUser, "password": "wrong password here"})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}

	fifth := h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": "wrong password here"})
	if fifth.Code != http.StatusTooManyRequests {
		t.Fatalf("fifth attempt: status = %d, want 429", fifth.Code)
	}

	// Even the correct password is refused while the lockout is open.
	rec := h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": testPassword})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body %s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 has no Retry-After header")
	}
	if env := h.errorEnvelopeOf(rec); env.Error != errTooMany {
		t.Errorf("error = %q, want %q", env.Error, errTooMany)
	}
}

// TestLogoutRevokesSession proves logout kills the session server-side, not just
// the cookie: replaying the same cookie afterwards is a 401.
func TestLogoutRevokesSession(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	rec := h.do(http.MethodPost, "/api/logout", s.cookie, s.csrf, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 || cookies[0].Value != "" {
		t.Fatalf("logout did not clear the cookie: %+v", cookies)
	}

	replay := h.do(http.MethodGet, "/api/me", s.cookie, "", nil)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed cookie: status = %d, want 401 (body %s)", replay.Code, replay.Body.String())
	}
}

// TestMeReturnsCSRFToken proves GET /api/me hands back the token a reloaded SPA
// needs, so it can make unsafe calls without logging in again.
func TestMeReturnsCSRFToken(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	rec := h.do(http.MethodGet, "/api/me", s.cookie, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var resp meResponse
	h.decode(rec, &resp)
	if resp.Username != testUser {
		t.Errorf("username = %q, want %q", resp.Username, testUser)
	}
	if resp.CSRFToken != s.csrf {
		t.Errorf("csrf_token = %q, want the token login issued (%q)", resp.CSRFToken, s.csrf)
	}
}

// TestPasswordChangeRevokesEverything proves decision 5 through the API: the
// new password works, the old one does not, and the cookie that made the change
// is dead too.
func TestPasswordChangeRevokesEverything(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	const newPassword = "a much longer new password"

	rec := h.do(http.MethodPost, "/api/password", s.cookie, s.csrf,
		map[string]string{"current_password": testPassword, "new_password": newPassword})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}

	// The caller's own cookie is revoked with the rest.
	if after := h.do(http.MethodGet, "/api/me", s.cookie, "", nil); after.Code != http.StatusUnauthorized {
		t.Errorf("cookie still valid after a password change: status %d", after.Code)
	}

	// The old password no longer logs in.
	old := h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": testPassword})
	if old.Code != http.StatusUnauthorized {
		t.Errorf("old password: status = %d, want 401", old.Code)
	}

	// The new one does.
	fresh := h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": newPassword})
	if fresh.Code != http.StatusOK {
		t.Fatalf("new password: status = %d, want 200 (body %s)", fresh.Code, fresh.Body.String())
	}
}

// TestPasswordChangeNeedsCurrentPassword proves a live session alone cannot
// change the password: an attacker holding a stolen cookie must still know the
// current password.
func TestPasswordChangeNeedsCurrentPassword(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	rec := h.do(http.MethodPost, "/api/password", s.cookie, s.csrf,
		map[string]string{"current_password": "not the current password", "new_password": "a much longer new password"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	if env := h.errorEnvelopeOf(rec); env.Error != errInvalidLogin {
		t.Errorf("error = %q, want %q", env.Error, errInvalidLogin)
	}
}

// TestPasswordChangeRejectsShortPassword proves the length floor is enforced on
// the server, not only in the form.
func TestPasswordChangeRejectsShortPassword(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	rec := h.do(http.MethodPost, "/api/password", s.cookie, s.csrf,
		map[string]string{"current_password": testPassword, "new_password": "short"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	h.errorEnvelopeOf(rec)
}

// TestUsernameChangeRenamesAndRevokes proves the rename does all three things a
// credential change must: the new name logs in, the old one does not, and the
// cookie that asked for the change is dead.
func TestUsernameChangeRenamesAndRevokes(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	const newUser = "operator"
	rec := h.do(http.MethodPost, "/api/username", s.cookie, s.csrf,
		map[string]string{"current_password": testPassword, "new_username": newUser})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatalf("the change did not clear the cookie: %+v", cookies)
	}

	if after := h.do(http.MethodGet, "/api/me", s.cookie, "", nil); after.Code != http.StatusUnauthorized {
		t.Errorf("cookie still valid after a rename: status %d", after.Code)
	}

	old := h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": testPassword})
	if old.Code != http.StatusUnauthorized {
		t.Errorf("old username: status = %d, want 401", old.Code)
	}

	fresh := h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": newUser, "password": testPassword})
	if fresh.Code != http.StatusOK {
		t.Fatalf("new username: status = %d, want 200 (body %s)", fresh.Code, fresh.Body.String())
	}
}

// TestMeReportsTheRenamedAccount proves /api/me reads the stored name rather
// than the one the daemon started with, so the header follows a rename without
// a restart.
func TestMeReportsTheRenamedAccount(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	const newUser = "operator"
	if rec := h.do(http.MethodPost, "/api/username", s.cookie, s.csrf,
		map[string]string{"current_password": testPassword, "new_username": newUser}); rec.Code != http.StatusNoContent {
		t.Fatalf("rename: status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}

	again := h.loginAs(newUser, testPassword)
	rec := h.do(http.MethodGet, "/api/me", again.cookie, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp meResponse
	h.decode(rec, &resp)
	if resp.Username != newUser {
		t.Errorf("username = %q, want %q", resp.Username, newUser)
	}
}

// TestUsernameChangeNeedsCurrentPassword proves a live session alone cannot
// rename the account, the same rule a password change follows.
func TestUsernameChangeNeedsCurrentPassword(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	rec := h.do(http.MethodPost, "/api/username", s.cookie, s.csrf,
		map[string]string{"current_password": "not the current password", "new_username": "operator"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	if env := h.errorEnvelopeOf(rec); env.Error != errInvalidLogin {
		t.Errorf("error = %q, want %q", env.Error, errInvalidLogin)
	}
}

// TestUsernameChangeRejectsBadInput proves the empty and over-long cases are
// refused before the password is checked.
func TestUsernameChangeRejectsBadInput(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	cases := map[string]map[string]string{
		"empty":       {"current_password": testPassword, "new_username": "   "},
		"missing":     {"current_password": testPassword},
		"too long":    {"current_password": testPassword, "new_username": strings.Repeat("a", maxUsernameLen+1)},
		"no password": {"new_username": "operator"},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := h.do(http.MethodPost, "/api/username", s.cookie, s.csrf, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			h.errorEnvelopeOf(rec)
		})
	}

	// A refused request must not have renamed anything.
	if _, err := h.deps.Auth.VerifyLogin(context.Background(), testUser, testPassword, "203.0.113.9", "t"); err != nil {
		t.Errorf("the account was renamed by a rejected request: %v", err)
	}
}

// TestGetSettingsReturnsEveryKeyGrouped proves the Settings page can render from
// one call: every registered key appears, under the group the registry gave it,
// with a value in effect.
func TestGetSettingsReturnsEveryKeyGrouped(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	rec := h.do(http.MethodGet, "/api/settings", s.cookie, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var resp settingsResponse
	h.decode(rec, &resp)
	if len(resp.Groups) == 0 {
		t.Fatal("no groups returned")
	}

	seen := make(map[string]bool)
	for _, group := range resp.Groups {
		if group.Name == "" {
			t.Error("a group has no name")
		}
		for _, key := range group.Keys {
			seen[key.Name] = true
			if key.Group != group.Name {
				t.Errorf("key %s is in group %q but says %q", key.Name, group.Name, key.Group)
			}
			if (key.Value == "" && !key.Optional) || key.Description == "" || key.Type == "" {
				t.Errorf("key %s is missing value, type or description: %+v", key.Name, key)
			}
		}
	}
	for _, name := range []string{"panel.port", "panel.bind", "session.idle_timeout", "login.max_attempts", "login.lockout"} {
		if !seen[name] {
			t.Errorf("key %s is missing from the response", name)
		}
	}
}

// TestPutSettingsStoresAndReports proves a write through the API is stored and
// echoed back in the settings in effect.
func TestPutSettingsStoresAndReports(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	rec := h.do(http.MethodPut, "/api/settings", s.cookie, s.csrf,
		map[string]any{"settings": []map[string]string{{"name": "login.max_attempts", "value": "9"}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var resp settingsResponse
	h.decode(rec, &resp)
	if got := valueOf(resp, "login.max_attempts"); got != "9" {
		t.Errorf("login.max_attempts = %q, want 9", got)
	}

	// A second read must agree with the write, so the value is really stored.
	reread := h.do(http.MethodGet, "/api/settings", s.cookie, "", nil)
	h.decode(reread, &resp)
	if got := valueOf(resp, "login.max_attempts"); got != "9" {
		t.Errorf("after re-read login.max_attempts = %q, want 9", got)
	}
}

// TestPutSettingsRejectsInvalidValue proves the registry's validator is the one
// deciding, and its sentence reaches the client.
func TestPutSettingsRejectsInvalidValue(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	rec := h.do(http.MethodPut, "/api/settings", s.cookie, s.csrf,
		map[string]any{"settings": []map[string]string{{"name": "panel.port", "value": "99999"}}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if env := h.errorEnvelopeOf(rec); env.Error != errInvalidSetting {
		t.Errorf("error = %q, want %q", env.Error, errInvalidSetting)
	}

	// The rejected write changed nothing.
	resp := h.getSettings(s)
	if got := valueOf(resp, "panel.port"); got != "8443" {
		t.Errorf("panel.port = %q, want the default 8443", got)
	}
}

// TestPutSettingsRejectsUnknownKey proves a typo cannot create a dead setting.
func TestPutSettingsRejectsUnknownKey(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	rec := h.do(http.MethodPut, "/api/settings", s.cookie, s.csrf,
		map[string]any{"settings": []map[string]string{{"name": "panel.nonsense", "value": "x"}}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if env := h.errorEnvelopeOf(rec); env.Error != errNotFound {
		t.Errorf("error = %q, want %q", env.Error, errNotFound)
	}
}

// TestAuditListsNewestFirst proves the audit route returns the entries the
// middleware recorded, newest first, with their redacted detail.
func TestAuditListsNewestFirst(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	// One audited write: the settings PUT.
	if rec := h.do(http.MethodPut, "/api/settings", s.cookie, s.csrf,
		map[string]any{"settings": []map[string]string{{"name": "login.max_attempts", "value": "7"}}}); rec.Code != http.StatusOK {
		t.Fatalf("settings put: status = %d", rec.Code)
	}

	rec := h.do(http.MethodGet, "/api/audit", s.cookie, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var resp auditResponse
	h.decode(rec, &resp)
	if len(resp.Entries) == 0 {
		t.Fatal("audit returned no entries after an audited write")
	}
	if resp.Limit != auditDefaultLimit {
		t.Errorf("limit = %d, want the default %d", resp.Limit, auditDefaultLimit)
	}
	for i := 1; i < len(resp.Entries); i++ {
		if resp.Entries[i-1].ID <= resp.Entries[i].ID {
			t.Fatalf("entries are not newest-first: id %d before %d", resp.Entries[i-1].ID, resp.Entries[i].ID)
		}
	}
	first := resp.Entries[0]
	if first.Action != "PUT /api/settings" {
		t.Errorf("newest action = %q, want %q", first.Action, "PUT /api/settings")
	}
	if first.Actor != testUser {
		t.Errorf("actor = %q, want %q", first.Actor, testUser)
	}
	if first.Result != "success" {
		t.Errorf("result = %q, want success", first.Result)
	}
}

// TestAuditLimitIsValidated proves ?limit= is honoured and a bad value is a 400
// rather than a silent fallback.
func TestAuditLimitIsValidated(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	rec := h.do(http.MethodGet, "/api/audit?limit=5", s.cookie, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp auditResponse
	h.decode(rec, &resp)
	if resp.Limit != 5 {
		t.Errorf("limit = %d, want 5", resp.Limit)
	}

	for _, bad := range []string{"0", "-1", "100000", "abc"} {
		rec := h.do(http.MethodGet, "/api/audit?limit="+bad, s.cookie, "", nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("limit=%s: status = %d, want 400", bad, rec.Code)
		}
	}
}

// getSettings is the read half of the settings route, for tests that need the
// value in effect without repeating the decode.
func (h *harness) getSettings(s session) settingsResponse {
	h.t.Helper()
	rec := h.do(http.MethodGet, "/api/settings", s.cookie, "", nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("GET /api/settings: status %d", rec.Code)
	}
	var resp settingsResponse
	h.decode(rec, &resp)
	return resp
}

// valueOf finds one key's value in a settings response.
func valueOf(resp settingsResponse, name string) string {
	for _, group := range resp.Groups {
		for _, key := range group.Keys {
			if key.Name == name {
				return key.Value
			}
		}
	}
	return ""
}
