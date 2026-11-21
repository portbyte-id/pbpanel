package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/native"
)

const digest = "1111111111111111111111111111111111111111111111111111111111111111"

func manifest(t *testing.T, dir, release, id, built string) native.Package {
	t.Helper()
	p := native.Package{
		Product: "nginx", Release: release, ID: id,
		Distro: "ubuntu", VersionID: "24.04", Arch: "amd64", Root: "/www", Recipe: "abc123",
		File: "nginx-" + release + "-" + id + ".tar.gz", SHA256: digest, Size: 1, Built: built,
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, native.ManifestName(p.File)), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIndexMergesManifestsAndListsWhatToDrop(t *testing.T) {
	dir, out := t.TempDir(), t.TempDir()
	old := manifest(t, dir, "1.30.1", "20261101000000-aaaaaa", "2026-11-01T00:00:00Z")
	mid := manifest(t, dir, "1.30.2", "20261110000000-bbbbbb", "2026-11-10T00:00:00Z")
	cur := manifest(t, dir, "1.30.3", "20261120000000-cccccc", "2026-11-20T00:00:00Z")
	if err := os.WriteFile(filepath.Join(dir, "junk.manifest.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	indexFile, dropFile := filepath.Join(out, "index.json"), filepath.Join(out, "drop.txt")
	if err := run(context.Background(), []string{"index", "-manifests", dir, "-out", indexFile, "-drop", dropFile}); err != nil {
		t.Fatalf("index: %v", err)
	}

	raw, err := os.ReadFile(indexFile)
	if err != nil {
		t.Fatal(err)
	}
	var idx native.Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatalf("index.json is not an Index: %v\n%s", err, raw)
	}
	if idx.Version != native.IndexVersion || len(idx.Packages) != 2 || idx.Packages[0].ID != cur.ID || idx.Packages[1].ID != mid.ID {
		t.Errorf("index = %+v, want the two newest, newest first", idx)
	}
	dropped, err := os.ReadFile(dropFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := native.ManifestName(old.File) + "\n" + old.File + "\n"; string(dropped) != want {
		t.Errorf("drop list = %q, want %q", dropped, want)
	}
}

// An index with nothing in it would make every host believe nothing is published,
// and would replace a good one. Publishing it is refused.
func TestIndexRefusesToPublishNothing(t *testing.T) {
	dir, out := t.TempDir(), t.TempDir()
	err := run(context.Background(), []string{"index", "-manifests", dir, "-out", filepath.Join(out, "i.json"), "-drop", filepath.Join(out, "d.txt")})
	if err == nil || !strings.Contains(err.Error(), "empty index") {
		t.Fatalf("err = %v, want a refusal to publish an empty index", err)
	}
	if _, serr := os.Stat(filepath.Join(out, "i.json")); serr == nil {
		t.Error("an index file was written anyway")
	}
}

// Every one of these is refused before Docker is touched.
func TestBuildNeedsItsArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"nothing":           {},
		"unknown":           {"frobnicate"},
		"no distro":         {"build", "-out", "x"},
		"no out":            {"build", "-distro", "ubuntu", "-version-id", "24.04"},
		"php is not ready":  {"build", "-product", "php", "-distro", "ubuntu", "-version-id", "24.04", "-out", "x"},
		"relative root":     {"build", "-distro", "ubuntu", "-version-id", "24.04", "-out", "x", "-root", "www"},
		"a root with dots":  {"build", "-distro", "ubuntu", "-version-id", "24.04", "-out", "x", "-root", "/www/../etc"},
		"a hostile version": {"build", "-distro", "ubuntu", "-version-id", "24.04", "-out", "x", "-series", "1.30; rm -rf /"},
	} {
		if err := run(context.Background(), args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
