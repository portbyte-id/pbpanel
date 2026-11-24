package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/portbyte-id/server-panel/internal/guard"
)

// solveForTest finds a nonce whose answer has the difficulty's leading zero bits,
// the same work the login page does in the browser.
func solveForTest(t *testing.T, challenge string, difficulty int) string {
	t.Helper()
	for n := range 1 << 30 {
		nonce := strconv.Itoa(n)
		if guard.LeadingZeroBits(sha256.Sum256([]byte(challenge+":"+nonce))) >= difficulty {
			return nonce
		}
	}
	t.Fatal("no nonce found")
	return ""
}

// loginChallenge fetches GET /api/login/challenge for the harness client.
func (h *harness) loginChallenge() loginChallengeResponse {
	h.t.Helper()
	rec := h.do(http.MethodGet, "/api/login/challenge", "", "", nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("challenge: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp loginChallengeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("decode challenge: %v", err)
	}
	return resp
}

// postLogin signs in the way the login page does: it asks whether a check is
// needed, solves one if so, and posts the credentials with the answer.
func (h *harness) postLogin(username, password string) *httptest.ResponseRecorder {
	h.t.Helper()
	ch := h.loginChallenge()
	body := map[string]string{"username": username, "password": password}
	if ch.Required {
		body["challenge"] = ch.Challenge
		body["nonce"] = solveForTest(h.t, ch.Challenge, ch.Difficulty)
	}
	return h.do(http.MethodPost, "/api/login", "", "", body)
}

// TestLoginChallengeOffUntilAFailure proves a client with no failed login is not
// asked for a check, so the normal sign-in costs it nothing.
func TestLoginChallengeOffUntilAFailure(t *testing.T) {
	h := newHarness(t)

	if ch := h.loginChallenge(); ch.Required || ch.Challenge != "" {
		t.Fatalf("fresh client: got %+v, want no check", ch)
	}
	rec := h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": testPassword})
	if rec.Code != http.StatusOK {
		t.Fatalf("clean login: status = %d, want 200", rec.Code)
	}
}

// TestLoginChallengeOnAfterAFailure proves the first failed login turns the check
// on at once: the page learns it from the challenge endpoint and gets a puzzle.
func TestLoginChallengeOnAfterAFailure(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": "wrong password here"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want 401", rec.Code)
	}

	ch := h.loginChallenge()
	if !ch.Required || ch.Challenge == "" {
		t.Fatalf("after a failure: got %+v, want a required check with a puzzle", ch)
	}
	if ch.Difficulty != loginDifficulty {
		t.Errorf("difficulty = %d, want %d", ch.Difficulty, loginDifficulty)
	}
}

// TestLoginWithoutCheckIsRefusedAfterAFailure proves that once an address has a
// failure on record, a login without a check is turned away before the password
// is looked at, even when the password is right.
func TestLoginWithoutCheckIsRefusedAfterAFailure(t *testing.T) {
	h := newHarness(t)

	h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": "wrong password here"})

	rec := h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": testPassword})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if env := h.errorEnvelopeOf(rec); env.Error != errChallengeRequired {
		t.Errorf("error = %q, want %q", env.Error, errChallengeRequired)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a refused login set a cookie")
	}
}

// TestLoginWithCheckSucceedsAfterAFailure proves a solved check lets the next
// attempt through, so a visitor who mistyped once can still sign in, and that a
// successful login clears the requirement.
func TestLoginWithCheckSucceedsAfterAFailure(t *testing.T) {
	h := newHarness(t)

	h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": "wrong password here"})

	rec := h.postLogin(testUser, testPassword)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if ch := h.loginChallenge(); ch.Required {
		t.Errorf("after a successful login the check is still required: %+v", ch)
	}
}

// TestLoginCheckIsSingleUse proves a solved check opens one attempt only. A
// script that solves once cannot reuse the answer for a second password guess.
func TestLoginCheckIsSingleUse(t *testing.T) {
	h := newHarness(t)

	h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": "wrong password here"})

	ch := h.loginChallenge()
	answer := map[string]string{
		"username": testUser, "password": "wrong password here",
		"challenge": ch.Challenge, "nonce": solveForTest(t, ch.Challenge, ch.Difficulty),
	}
	if rec := h.do(http.MethodPost, "/api/login", "", "", answer); rec.Code != http.StatusUnauthorized {
		t.Fatalf("first use: status = %d, want 401", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/login", "", "", answer); rec.Code != http.StatusForbidden {
		t.Fatalf("replayed answer: status = %d, want 403", rec.Code)
	}
}

// TestLoginCheckRejectsWrongAnswer proves a made-up nonce does not open the door.
func TestLoginCheckRejectsWrongAnswer(t *testing.T) {
	h := newHarness(t)

	h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": "wrong password here"})

	ch := h.loginChallenge()
	rec := h.do(http.MethodPost, "/api/login", "", "", map[string]string{
		"username": testUser, "password": testPassword,
		"challenge": ch.Challenge, "nonce": "not-a-number",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// TestLoginCheckIsBoundToTheClient proves a check issued to one address does not
// work from another. The other address has no failure on record, so it is not
// asked for a check; the answer is judged only where it is needed.
func TestLoginCheckIsBoundToTheClient(t *testing.T) {
	h := newHarness(t)

	const other = "198.51.100.77:4000"

	// Both addresses fail once, so both have a check on record.
	h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": "wrong password here"})
	fromOther(t, h, other, map[string]string{"username": testUser, "password": "wrong password here"})

	// The puzzle is issued to the harness address; the answer is replayed from
	// the other one.
	ch := h.loginChallenge()
	rec := fromOther(t, h, other, map[string]string{
		"username": testUser, "password": testPassword,
		"challenge": ch.Challenge, "nonce": solveForTest(t, ch.Challenge, ch.Difficulty),
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("answer from another address: status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
}

// fromOther posts a login from remoteAddr, for the tests that need two clients.
func fromOther(t *testing.T, h *harness, remoteAddr string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(raw))
	req.RemoteAddr = remoteAddr
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// TestOperatorUnlockLiftsTheCheck proves `pbp unlock-login` lifts the check on
// the very next request, not only after a later password attempt: the unlock
// applies when the challenge is asked for.
func TestOperatorUnlockLiftsTheCheck(t *testing.T) {
	h := newHarness(t)

	h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": "wrong password here"})
	if ch := h.loginChallenge(); !ch.Required {
		t.Fatalf("after a failure: got %+v, want a required check", ch)
	}

	if err := h.deps.Auth.UnlockLogin(context.Background()); err != nil {
		t.Fatalf("UnlockLogin: %v", err)
	}
	if ch := h.loginChallenge(); ch.Required {
		t.Fatalf("after unlock-login: got %+v, want no check", ch)
	}
	if rec := h.do(http.MethodPost, "/api/login", "", "",
		map[string]string{"username": testUser, "password": testPassword}); rec.Code != http.StatusOK {
		t.Fatalf("login after unlock: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}
