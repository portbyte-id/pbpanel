package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageTagPostsRepoAndTag(t *testing.T) {
	var gotPath, gotQuery string
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.WriteHeader(http.StatusCreated)
	}))
	if err := c.ImageTag(context.Background(), "php:8.3-fpm", "panel/php:8.3-20261117120000-abcdef"); err != nil {
		t.Fatalf("ImageTag = %v", err)
	}
	if !strings.HasSuffix(gotPath, "/images/php:8.3-fpm/tag") {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "repo=panel%2Fphp") || !strings.Contains(gotQuery, "tag=8.3-20261117120000-abcdef") {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestImageRemoveMapsMissingToNotFound(t *testing.T) {
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s", r.Method)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"No such image: panel/php:8.3-x"}`)
	}))
	if err := c.ImageRemove(context.Background(), "panel/php:8.3-x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestImageRemoveReturnsTheEngineRefusal(t *testing.T) {
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"message":"conflict: image is being used by container"}`)
	}))
	err := c.ImageRemove(context.Background(), "panel/php:8.3-x")
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "being used") {
		t.Errorf("err = %v, want the engine's conflict", err)
	}
}

func TestTarDirPacksTheTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := tarDir(dir)
	if err != nil {
		t.Fatalf("tarDir = %v", err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	got := readTar(t, raw)
	if got["Dockerfile"] != "FROM x\n" || got["sub/f.txt"] != "hi" {
		t.Errorf("entries = %v", got)
	}
	if _, ok := got["sub/"]; !ok {
		t.Errorf("the sub directory has no entry: %v", got)
	}
}

func TestTarDirRefusesRelativeAndLinkedContexts(t *testing.T) {
	if _, err := tarDir("relative/dir"); err == nil {
		t.Error("a relative context path was accepted")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "Dockerfile"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("symbolic links are not available here: %v", err)
	}
	if _, err := tarDir(dir); err == nil {
		t.Error("a symbolic link in the context was accepted")
	}
}

func TestImageBuildSendsTheContextDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var sent []byte
	c := fakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/build") {
			sent, _ = io.ReadAll(r.Body)
			_, _ = io.WriteString(w, `{"stream":"Step 1/1\n"}`+"\n")
			return
		}
		// The inspect that follows the build finds the tag.
		_, _ = io.WriteString(w, `{"Id":"sha256:abc","RepoTags":["panel/php:8.3-x"]}`)
	}))
	if _, err := c.ImageBuild(context.Background(), BuildOptions{Tag: "panel/php:8.3-x", ContextDir: dir}, nil); err != nil {
		t.Fatalf("ImageBuild = %v", err)
	}
	if _, ok := readTar(t, sent)["Dockerfile"]; !ok {
		t.Error("the Dockerfile of the context directory was not sent")
	}
}

// readTar returns the regular entries of a tar stream by name.
func readTar(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out[h.Name] = string(body)
	}
}
