package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/portbyte-id/server-panel/internal/audit"
	"github.com/portbyte-id/server-panel/internal/auth"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/webui"
)

// uiDeps is a panel with the SPA mounted, which the JSON suites deliberately
// omit. It exists because the route table has two shapes — API-only and
// API+SPA — and only the second one can produce a mux pattern conflict.
func uiDeps(t *testing.T) Deps {
	t.Helper()

	st, err := store.Open(t.TempDir() + "/panel.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg, err := settings.New(st)
	if err != nil {
		t.Fatalf("settings.New: %v", err)
	}

	authSvc := auth.NewService(st, auth.SystemClock{},
		func() time.Duration { return 30 * time.Minute },
		func() int { return 5 },
		func() time.Duration { return 15 * time.Minute },
	)

	jobsMgr, err := jobs.New(st)
	if err != nil {
		t.Fatalf("jobs.New: %v", err)
	}

	ui, err := webui.Handler(fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><div id=root></div>")},
	})
	if err != nil {
		t.Fatalf("webui.Handler: %v", err)
	}

	return Deps{
		Store:    st,
		Settings: reg,
		Auth:     authSvc,
		Audit:    audit.New(st.DB()),
		Jobs:     jobsMgr,
		UI:       ui,
	}
}

// TestRoutesBuildWithUI proves New does not panic when the SPA is mounted. Go's
// mux panics on conflicting patterns at registration time, which no API-only
// test can catch: the conflict only exists once "/" is registered beside
// "/api/".
func TestRoutesBuildWithUI(t *testing.T) {
	if _, err := New(uiDeps(t)); err != nil {
		t.Fatalf("New with UI: %v", err)
	}
}

func TestUIAndAPICoexist(t *testing.T) {
	h, err := New(uiDeps(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	// A client-side route is served the app shell.
	for _, path := range []string{"/", "/login", "/settings"} {
		rec := get(path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `id=root`) {
			t.Errorf("%s: not the app shell: %q", path, rec.Body.String())
		}
	}

	// An unknown API path stays JSON, so a client parses an error rather than
	// a page of HTML.
	rec := get("/api/nope")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown API path: status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("unknown API path: Content-Type = %q, want JSON", ct)
	}

	// A known API route is untouched by the SPA mount.
	rec = get("/api/me")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/api/me: status = %d, want 401", rec.Code)
	}

	// The SPA is served over GET only.
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/settings", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /settings: status = %d, want 405", post.Code)
	}
}

// TestUIServesAssetsWithoutSession checks the embed's own assets are reachable
// without a session: the login page is one of them.
func TestUIServesAssetsWithoutSession(t *testing.T) {
	deps := uiDeps(t)
	ui, err := webui.Handler(fstest.MapFS{
		"index.html":             {Data: []byte("<!doctype html>")},
		"assets/index-abc123.js": {Data: []byte("console.log(1)")},
	})
	if err != nil {
		t.Fatalf("webui.Handler: %v", err)
	}
	deps.UI = ui

	h, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/index-abc123.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable", got)
	}
}
