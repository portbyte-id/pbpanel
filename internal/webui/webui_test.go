package webui_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/portbyte-id/server-panel/internal/webui"
)

// app is a synthetic build tree: the files Vite emits, with the names it gives
// them. A test asserts on policy, so the content is irrelevant and the names
// are not.
func app() fs.FS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><div id=root></div>")},
		"assets/index-a1b2c3.js":  {Data: []byte("console.log('app')")},
		"assets/index-d4e5f6.css": {Data: []byte("body{margin:0}")},
		"favicon.svg":             {Data: []byte("<svg/>")},
	}
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHandlerRequiresIndex(t *testing.T) {
	_, err := webui.Handler(fstest.MapFS{})
	if err == nil {
		t.Fatal("Handler accepted a filesystem with no index.html")
	}
}

func TestRootServesIndexWithNoStore(t *testing.T) {
	h, err := webui.Handler(app())
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), `id=root`) {
		t.Errorf("body is not index.html: %q", rec.Body.String())
	}
}

// TestHashedAssetsAreImmutable is the rule that makes a deploy cheap: the URL
// contains a content hash, so the browser never needs to ask again.
func TestHashedAssetsAreImmutable(t *testing.T) {
	h, err := webui.Handler(app())
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	for _, name := range []string{"/assets/index-a1b2c3.js", "/assets/index-d4e5f6.css"} {
		t.Run(name, func(t *testing.T) {
			rec := get(t, h, name)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			got := rec.Header().Get("Cache-Control")
			if !strings.Contains(got, "immutable") || !strings.Contains(got, "max-age=31536000") {
				t.Errorf("Cache-Control = %q, want a year and immutable", got)
			}
		})
	}
}

func TestAssetContentTypes(t *testing.T) {
	h, err := webui.Handler(app())
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	cases := map[string]string{
		"/assets/index-a1b2c3.js":  "text/javascript",
		"/assets/index-d4e5f6.css": "text/css",
		"/favicon.svg":             "image/svg+xml",
	}
	for path, want := range cases {
		t.Run(path, func(t *testing.T) {
			got := get(t, h, path).Header().Get("Content-Type")
			if !strings.HasPrefix(got, want) {
				t.Errorf("Content-Type = %q, want prefix %q", got, want)
			}
		})
	}
}

// TestClientRouteFallsBackToIndex is what makes a deep link work: /settings has
// no file, and the router needs index.html to render it.
func TestClientRouteFallsBackToIndex(t *testing.T) {
	h, err := webui.Handler(app())
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// A site's page carries its domain, and every domain has a dot: an
	// extension is not what tells a route from a file.
	for _, path := range []string{"/settings", "/audit", "/settings/deep", "/sites/a.test", "/sites/www.example.com", "/sites/app.js"} {
		t.Run(path, func(t *testing.T) {
			rec := get(t, h, path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store: the fallback must not be cached", got)
			}
			if !strings.Contains(rec.Body.String(), `id=root`) {
				t.Errorf("body is not index.html: %q", rec.Body.String())
			}
		})
	}
}

// TestMissingAssetIs404 keeps a build mistake visible: answering a missing .js
// with HTML would surface as a syntax error in the console instead.
func TestMissingAssetIs404(t *testing.T) {
	h, err := webui.Handler(app())
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	for _, path := range []string{"/assets/index-gone.js", "/assets/gone", "/favicon.ico", "/robots.txt"} {
		if rec := get(t, h, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, rec.Code)
		}
	}
}

// TestUnhashedFileIsRevalidated covers a file outside assets/: its URL does not
// change with its content, so it must not be cached forever.
func TestUnhashedFileIsRevalidated(t *testing.T) {
	h, err := webui.Handler(app())
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	got := get(t, h, "/favicon.svg").Header().Get("Cache-Control")
	if strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, an unhashed file must not be immutable", got)
	}
}

// TestTraversalDoesNotEscape is the containment check: a cleaned path cannot
// climb out of the embedded tree.
func TestTraversalDoesNotEscape(t *testing.T) {
	h, err := webui.Handler(app())
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	for _, path := range []string{"/../../etc/passwd", "/assets/../../secret.txt"} {
		t.Run(path, func(t *testing.T) {
			rec := get(t, h, path)
			if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "root:") {
				t.Fatalf("served a file outside the tree: %q", rec.Body.String())
			}
		})
	}
}
