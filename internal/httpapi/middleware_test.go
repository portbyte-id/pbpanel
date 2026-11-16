package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSecurityHeadersOnNormalResponse is the header requirement: every response,
// success or failure, carries the protections the panel promises.
func TestSecurityHeadersOnNormalResponse(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
		code   int
	}{
		{name: "200", method: http.MethodGet, path: "/api/me", code: http.StatusOK},
		{name: "204", method: http.MethodPost, path: "/api/logout", code: http.StatusNoContent},
		{name: "400", method: http.MethodPut, path: "/api/settings", body: map[string]any{"settings": []map[string]string{}}, code: http.StatusBadRequest},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A fresh session per case: the logout case revokes its own, and a
			// shared cookie would make the next case a 401.
			s := h.login()
			rec := h.do(c.method, c.path, s.cookie, s.csrf, c.body)
			if rec.Code != c.code {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.code, rec.Body.String())
			}
			assertSecurityHeaders(t, rec)
		})
	}
}

// TestSecurityHeadersOnUnauthenticatedResponse proves the headers are set before
// the auth guard runs, so a 401 carries them too.
func TestSecurityHeadersOnUnauthenticatedResponse(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodGet, "/api/me", "", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	assertSecurityHeaders(t, rec)
}

func assertSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("no Content-Security-Policy header")
	}
	// The two directives that matter most: no inline script, and no framing.
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP is missing %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP allows unsafe-eval: %s", csp)
	}

	if got := rec.Header().Get("Strict-Transport-Security"); !strings.Contains(got, "max-age=") {
		t.Errorf("Strict-Transport-Security = %q, want a max-age", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

// TestRecoverTurnsPanicIntoJSON500 proves a panic in a handler becomes the
// envelope, not a dropped connection, and that the stack stays in the log.
func TestRecoverTurnsPanicIntoJSON500(t *testing.T) {
	var logs strings.Builder
	s := &server{
		deps:   Deps{Actor: testUser},
		logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}

	panicky := s.withRecover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	panicky = withRequestID(panicky)

	rec := httptest.NewRecorder()
	panicky.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	env := decodeEnvelope(t, rec)
	if env.Error != errInternal {
		t.Errorf("error = %q, want %q", env.Error, errInternal)
	}
	if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "goroutine") {
		t.Errorf("response leaks the panic: %s", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "boom") {
		t.Errorf("panic was not logged: %s", logs.String())
	}
}

// TestRecoverRepanicsAbortHandler proves http.ErrAbortHandler still aborts: a
// handler that uses it is asking for a silent teardown, not a 500.
func TestRecoverRepanicsAbortHandler(t *testing.T) {
	s := &server{deps: Deps{Actor: testUser}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	panicky := s.withRecover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		rec := recover()
		if rec != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler to propagate", rec)
		}
	}()
	panicky.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/me", nil))
}

// TestRequestIDIsGeneratedAndEchoed proves every response carries an ID the log
// line can be joined on, even when the client sent none.
func TestRequestIDIsGeneratedAndEchoed(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodGet, "/api/me", "", "", nil)
	id := rec.Header().Get(requestIDHeader)
	if id == "" {
		t.Fatal("no request ID header")
	}
	if len(id) != requestIDBytes*2 {
		t.Errorf("generated ID = %q, want %d hex characters", id, requestIDBytes*2)
	}
}

// TestRequestIDIsHonoured proves a client-supplied ID is kept, so the smoke
// script can follow one operation across its own log.
func TestRequestIDIsHonoured(t *testing.T) {
	h := newHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set(requestIDHeader, "smoke-run-42")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if got := rec.Header().Get(requestIDHeader); got != "smoke-run-42" {
		t.Errorf("request ID = %q, want the client's", got)
	}
}

// TestRequestIDRejectsHostileValue proves a header cannot smuggle a newline or
// other characters into the log, which is the log-injection case.
func TestRequestIDRejectsHostileValue(t *testing.T) {
	h := newHarness(t)

	for _, hostile := range []string{"a\nb", "with space", strings.Repeat("x", maxRequestIDLen+1), "semi;colon"} {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set(requestIDHeader, hostile)
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)

		got := rec.Header().Get(requestIDHeader)
		if got == hostile {
			t.Errorf("hostile request ID %q was accepted", hostile)
		}
		if got == "" {
			t.Errorf("request ID %q left the response without one", hostile)
		}
	}
}

// TestAuditRecordsUnsafeMethodsOnly proves the trail answers "who changed this"
// without logging every poll.
func TestAuditRecordsUnsafeMethodsOnly(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	before := len(h.audit.entries)

	// A read must not be audited.
	h.do(http.MethodGet, "/api/me", s.cookie, "", nil)
	if got := len(h.audit.entries); got != before {
		t.Errorf("a GET was audited: %d entries before, %d after", before, got)
	}

	// A write must be, with its outcome.
	h.do(http.MethodPut, "/api/settings", s.cookie, s.csrf,
		map[string]any{"settings": []map[string]string{{"name": "login.max_attempts", "value": "8"}}})
	if got := len(h.audit.entries); got != before+1 {
		t.Fatalf("a PUT added %d entries, want 1", got-before)
	}

	e := h.audit.entries[len(h.audit.entries)-1]
	if e.Action != "PUT /api/settings" {
		t.Errorf("action = %q", e.Action)
	}
	if e.Result != "success" {
		t.Errorf("result = %q, want success", e.Result)
	}
	if e.Actor != testUser {
		t.Errorf("actor = %q, want %q", e.Actor, testUser)
	}
}

// TestAuditRecordsFailures proves a rejected write is recorded as a failure, so
// the trail shows the attempt and not only the successes.
func TestAuditRecordsFailures(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	h.do(http.MethodPut, "/api/settings", s.cookie, s.csrf,
		map[string]any{"settings": []map[string]string{{"name": "panel.port", "value": "nope"}}})

	if len(h.audit.entries) == 0 {
		t.Fatal("a failed write was not audited")
	}
	e := h.audit.entries[len(h.audit.entries)-1]
	if e.Result != "failure" {
		t.Errorf("result = %q, want failure", e.Result)
	}
}

// TestAuditFailureDoesNotBreakTheRequest proves a broken audit writer does not
// fail an action that already happened: the response still reflects the handler.
func TestAuditFailureDoesNotBreakTheRequest(t *testing.T) {
	h := newHarness(t)
	s := h.login()
	h.audit.err = errAuditBroken{}

	rec := h.do(http.MethodPut, "/api/settings", s.cookie, s.csrf,
		map[string]any{"settings": []map[string]string{{"name": "login.max_attempts", "value": "3"}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 despite the audit failure (body %s)", rec.Code, rec.Body.String())
	}
}

type errAuditBroken struct{}

func (errAuditBroken) Error() string { return "audit: disk on fire" }

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", rec.Body.String(), err)
	}
	return env
}

// TestClientIPIgnoresForwardedFor is the throttle-bypass guard from the plan's
// security review: a client that sets X-Forwarded-For must not be able to pick a
// fresh counter for every attempt.
func TestClientIPIgnoresForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	req.RemoteAddr = "198.51.100.4:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Real-IP", "5.6.7.8")

	if got := clientIP(req); got != "198.51.100.4" {
		t.Errorf("clientIP = %q, want the TCP peer 198.51.100.4", got)
	}
}

// TestSessionLookupRestoresID proves the ID from the cookie is put back on the
// session, which is what makes logout able to revoke it.
func TestSessionLookupRestoresID(t *testing.T) {
	h := newHarness(t)
	s := h.login()

	var seen string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := sessionFrom(r.Context())
		if !ok {
			t.Error("no session in context")
			return
		}
		seen = session.ID
		w.WriteHeader(http.StatusOK)
	})

	srv := &server{deps: h.deps, logger: h.deps.Logger}
	chain := srv.withSession(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: s.cookie})
	chain.ServeHTTP(httptest.NewRecorder(), req)

	if seen != s.cookie {
		t.Errorf("session ID = %q, want the cookie value %q", seen, s.cookie)
	}
}
