package install

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// releaseServer serves one release with a binary and a checksum file.
func releaseServer(t *testing.T, binary []byte, sum string, withSums bool) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		assets := fmt.Sprintf(`{"name":"pbp-linux-amd64","url":"%s/a/bin"}`, srv.URL)
		if withSums {
			assets += fmt.Sprintf(`,{"name":"SHA256SUMS","url":"%s/a/sums"}`, srv.URL)
		}
		fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[%s]}`, assets)
	})
	mux.HandleFunc("/a/bin", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(binary) })
	mux.HandleFunc("/a/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  pbp-linux-amd64\n", sum)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestReleaseFetchVerifies(t *testing.T) {
	bin := []byte("new-binary")
	srv := releaseServer(t, bin, digest(bin), true)
	c := &ReleaseClient{Repo: "o/r", Token: "tok", BaseURL: srv.URL, Arch: "amd64"}

	rel, err := c.Fetch(t.Context())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer rel.Cleanup()
	if rel.Tag != "v9.9.9" {
		t.Errorf("tag = %q", rel.Tag)
	}
	got, _ := os.ReadFile(rel.Path)
	if string(got) != string(bin) {
		t.Errorf("binary content = %q", got)
	}
}

func TestReleaseFetchChecksumMismatch(t *testing.T) {
	srv := releaseServer(t, []byte("tampered"), digest([]byte("other")), true)
	c := &ReleaseClient{Repo: "o/r", Token: "tok", BaseURL: srv.URL, Arch: "amd64"}
	if _, err := c.Fetch(t.Context()); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
}

func TestReleaseFetchRefusesMissingSums(t *testing.T) {
	srv := releaseServer(t, []byte("x"), "", false)
	c := &ReleaseClient{Repo: "o/r", Token: "tok", BaseURL: srv.URL, Arch: "amd64"}
	if _, err := c.Fetch(t.Context()); err == nil || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Fatalf("err = %v, want missing SHA256SUMS", err)
	}
}

func TestReleaseNoRelease(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := &ReleaseClient{Repo: "o/r", BaseURL: srv.URL}
	if _, err := c.LatestTag(t.Context()); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("err = %v, want ErrNoRelease", err)
	}
}
