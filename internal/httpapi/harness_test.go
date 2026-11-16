package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/audit"
	"github.com/portbyte-id/server-panel/internal/auth"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
)

const (
	testUser     = "admin"
	testPassword = "the correct horse battery"
)

// harness is a complete panel over a temp database: the real auth service, the
// real settings registry, the real audit writer, and the handler New returns.
type harness struct {
	t       *testing.T
	handler http.Handler
	deps    Deps
	store   *store.Store
	audit   *recordingAudit
}

// recordingAudit captures the entries the middleware writes, so a test can
// assert on them directly, and forwards them to a real writer so GET /api/audit
// reads what the middleware recorded rather than a second, parallel fake.
type recordingAudit struct {
	entries []audit.Entry
	err     error
	inner   *audit.Writer
}

func (r *recordingAudit) Write(ctx context.Context, e audit.Entry) error {
	if r.err != nil {
		return r.err
	}
	r.entries = append(r.entries, e)
	return r.inner.Write(ctx, e)
}

// newHarness builds a panel with a seeded admin account.
func newHarness(t *testing.T) *harness {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})

	reg, err := settings.New(st)
	if err != nil {
		t.Fatalf("settings.New: %v", err)
	}

	authSvc := auth.NewService(st, auth.SystemClock{},
		func() time.Duration { return 30 * time.Minute },
		func() int { return 5 },
		func() time.Duration { return 15 * time.Minute },
	)
	if err := authSvc.SetPassword(context.Background(), testUser, testPassword); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	rec := &recordingAudit{inner: audit.New(st.DB(),
		audit.WithMirrorPath(filepath.Join(t.TempDir(), "audit.log")),
		audit.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)}
	deps := Deps{
		Store:    st,
		Settings: reg,
		Auth:     authSvc,
		Audit:    rec,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	h, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &harness{t: t, handler: h, deps: deps, store: st, audit: rec}
}

// session is a logged-in client's credentials.
type session struct {
	cookie string
	csrf   string
}

// login authenticates and returns the cookie and CSRF token the client holds.
func (h *harness) login() session {
	h.t.Helper()

	body := map[string]string{"username": testUser, "password": testPassword}
	rec := h.do(http.MethodPost, "/api/login", "", "", body)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("login: status %d, body %s", rec.Code, rec.Body.String())
	}

	var resp loginResponse
	h.decode(rec, &resp)

	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		h.t.Fatal("login set no cookie")
	}
	return session{cookie: cookies[0].Value, csrf: resp.CSRFToken}
}

// do sends a request through the full chain. A non-empty body is marshalled as
// JSON; a nil body sends none.
func (h *harness) do(method, path, cookie, csrf string, body any) *httptest.ResponseRecorder {
	h.t.Helper()

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(buf)
	}

	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "203.0.113.9:54321"
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
	}
	if csrf != "" {
		req.Header.Set(csrfHeader, csrf)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// doRaw sends a request with a caller-supplied body, for the malformed-input
// tests.
func (h *harness) doRaw(method, path, cookie, csrf, body string) *httptest.ResponseRecorder {
	h.t.Helper()

	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.RemoteAddr = "203.0.113.9:54321"
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
	}
	if csrf != "" {
		req.Header.Set(csrfHeader, csrf)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func (h *harness) decode(rec *httptest.ResponseRecorder, v any) {
	h.t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		h.t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
}

// errorEnvelopeOf reads the body as the error envelope and checks the shape.
func (h *harness) errorEnvelopeOf(rec *httptest.ResponseRecorder) errorEnvelope {
	h.t.Helper()
	var env errorEnvelope
	h.decode(rec, &env)
	if env.Error == "" || env.Detail == "" {
		h.t.Fatalf("error envelope has an empty field: %q", rec.Body.String())
	}
	// A stack trace or a Go source path in a response is exactly what the
	// envelope exists to prevent.
	if bytes.Contains(rec.Body.Bytes(), []byte("goroutine")) ||
		bytes.Contains(rec.Body.Bytes(), []byte(".go:")) {
		h.t.Fatalf("error response leaks internals: %q", rec.Body.String())
	}
	return env
}
