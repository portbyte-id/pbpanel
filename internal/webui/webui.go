// Package webui serves the embedded single-page app.
//
// Two things make this more than http.FileServer:
//
//   - cache headers. Vite writes content-hashed files under assets/, so those
//     can be cached forever; index.html names them, so it must never be cached
//     or a browser keeps asking for a bundle that no longer exists.
//   - the SPA fallback. /settings and /audit are client-side routes with no
//     file behind them, so an unknown path that is not an asset returns
//     index.html and lets the router decide. A path that looks like a missing
//     asset gets a 404 instead, because answering a missing .js with HTML turns
//     a build mistake into a syntax error in the console.
package webui

import (
	"bytes"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

const (
	indexFile = "index.html"

	// hashedPrefix is where Vite puts content-hashed build output.
	hashedPrefix = "assets/"

	noStore = "no-store"

	// longLived applies to a hashed asset: the URL changes when the bytes do.
	longLived = "public, max-age=31536000, immutable"
)

// Handler serves the app from fsys. The filesystem is a parameter rather than a
// package global so a test can serve a synthetic tree, and so this package does
// not depend on the embed directive that produced the real one.
func Handler(fsys fs.FS) (http.Handler, error) {
	if _, err := fs.Stat(fsys, indexFile); err != nil {
		return nil, errors.New("webui: the app has no " + indexFile + ": build the frontend first")
	}

	files := http.FileServer(http.FS(fsys))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" || name == "." {
			name = indexFile
		}

		if name == indexFile {
			serveIndex(w, r, fsys)
			return
		}

		if info, err := fs.Stat(fsys, name); err == nil && !info.IsDir() {
			setAssetCache(w, name)
			files.ServeHTTP(w, r)
			return
		}

		if looksLikeAsset(name) {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r, fsys)
	}), nil
}

// looksLikeAsset reports whether a path with no file behind it was a request
// for a file rather than a client-side route.
//
// An extension alone does not say so: a site's page is /sites/example.com, and
// every domain has a dot. What the build produces is files under assets/ and a
// few at the top level (favicon.ico, robots.txt), so those two shapes are the
// ones a missing file is reported for. Everything else is a route.
func looksLikeAsset(name string) bool {
	if strings.HasPrefix(name, hashedPrefix) {
		return true
	}
	return !strings.Contains(name, "/") && path.Ext(name) != ""
}

// serveIndex writes index.html with no-store. It is read and written directly
// rather than through FileServer so the header is set before any byte goes out
// and the content type cannot be guessed wrong.
func serveIndex(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	data, err := fs.ReadFile(fsys, indexFile)
	if err != nil {
		http.Error(w, "the panel interface could not be read", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", noStore)
	http.ServeContent(w, r, indexFile, time.Time{}, bytes.NewReader(data))
}

// setAssetCache gives a hashed build asset the immutable policy and everything
// else a short one. The content type is set from the extension because the
// embedded filesystem has no name to derive it from.
func setAssetCache(w http.ResponseWriter, name string) {
	h := w.Header()
	if strings.HasPrefix(name, hashedPrefix) {
		h.Set("Cache-Control", longLived)
	} else {
		// Not hashed, so the URL does not change with the content: revalidate
		// rather than risk serving a stale file forever.
		h.Set("Cache-Control", "public, max-age=300")
	}
	if ctype := mime.TypeByExtension(path.Ext(name)); ctype != "" {
		h.Set("Content-Type", ctype)
	}
}
