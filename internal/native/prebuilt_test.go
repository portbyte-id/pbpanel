package native

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	ctx0     = context.Background()
	wantHost = Host{ID: "ubuntu", VersionID: "24.04"}
)

const zeros = "0000000000000000000000000000000000000000000000000000000000000000"

// pkgFor is a package record that fits wantHost, with the fields a test varies
// passed in.
func pkgFor(release, id, built string) Package {
	return Package{
		Product: "nginx", Release: release, ID: id,
		Distro: "ubuntu", VersionID: "24.04", Arch: "amd64", Root: "/www", Recipe: "abc123",
		File: "nginx-" + release + "-" + id + ".tar.gz", SHA256: zeros, Size: 10, Built: built,
	}
}

func want(version string) Want {
	return Want{Product: "nginx", Version: version, Host: wantHost, Arch: "amd64", Root: "/www", Recipe: "abc123"}
}

// publish serves an index and named assets, as a release folder would.
func publish(t *testing.T, idx Index, assets map[string][]byte) Source {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(idx)
	})
	for name, body := range assets {
		mux.HandleFunc("/"+name, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return Source{BaseURL: srv.URL}
}

// buildTree writes a finished build's files under a temp directory.
func buildTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"sbin/nginx": "binary", "version": "1.30.2\n", "conf/mime.types": "types",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// packed packs a build tree under id and returns the archive's bytes and its
// record.
func packed(t *testing.T, id string) ([]byte, Package) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "p.tar.gz")
	digest, size, err := PackBuild(buildTree(t), id, out)
	if err != nil {
		t.Fatalf("PackBuild: %v", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(body)) != size {
		t.Fatalf("PackBuild reported %d bytes, the file has %d", size, len(body))
	}
	p := pkgFor("1.30.2", id, "2026-11-21T12:00:00Z")
	p.SHA256, p.Size = digest, size
	return body, p
}

const idA = "20261121120000-a1b2c3"

func TestFetchUnpacksAPackedBuild(t *testing.T) {
	body, p := packed(t, idA)
	src := publish(t, Index{Version: IndexVersion, Packages: []Package{p}}, map[string][]byte{p.File: body})

	versions := filepath.Join(t.TempDir(), "versions")
	var lines []string
	if err := src.Fetch(ctx0, p, versions, []string{"sbin/nginx", "version"}, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(versions, idA, "conf", "mime.types"))
	if err != nil || string(got) != "types" {
		t.Fatalf("the unpacked build lacks conf/mime.types: %q, %v", got, err)
	}
	if rel, err := BuildVersion(filepath.Join(versions, idA, "version"), idA, "nginx"); err != nil || rel != "1.30.2" {
		t.Errorf("BuildVersion = %q, %v", rel, err)
	}
	if len(lines) == 0 {
		t.Error("Fetch reported no progress")
	}
	entries, _ := os.ReadDir(versions)
	if len(entries) != 1 || entries[0].Name() != idA {
		t.Errorf("versions holds %v, want only %s: a temporary file was left behind", entries, idA)
	}
}

func TestFetchRefusesWhatTheIndexDoesNotDescribe(t *testing.T) {
	body, p := packed(t, idA)
	wrongSum := p
	wrongSum.SHA256 = zeros
	wrongSize := p
	wrongSize.Size = p.Size + 1

	for name, rec := range map[string]Package{"digest": wrongSum, "size": wrongSize} {
		t.Run(name, func(t *testing.T) {
			src := publish(t, Index{Version: IndexVersion, Packages: []Package{rec}}, map[string][]byte{rec.File: body})
			versions := filepath.Join(t.TempDir(), "versions")
			if err := src.Fetch(ctx0, rec, versions, []string{"sbin/nginx"}, nil); err == nil {
				t.Fatal("a package that does not match its record was unpacked")
			}
			entries, _ := os.ReadDir(versions)
			if len(entries) != 0 {
				t.Errorf("a failed fetch left %v behind", entries)
			}
		})
	}
}

// The archive is checked by the same code a build read out of a container goes
// through, so a name that climbs out of the build is refused even when the digest
// matches what the index promised.
func TestFetchRefusesAnArchiveThatEscapes(t *testing.T) {
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = gz.Close()
	sum := sha256.Sum256(raw.Bytes())

	p := pkgFor("1.30.2", idA, "2026-11-21T12:00:00Z")
	p.SHA256, p.Size = hex.EncodeToString(sum[:]), int64(raw.Len())
	src := publish(t, Index{Version: IndexVersion, Packages: []Package{p}}, map[string][]byte{p.File: raw.Bytes()})

	root := t.TempDir()
	versions := filepath.Join(root, "versions")
	if err := src.Fetch(ctx0, p, versions, nil, nil); err == nil {
		t.Fatal("an archive naming ../evil was unpacked")
	}
	if _, err := os.Stat(filepath.Join(root, "evil")); err == nil {
		t.Fatal("a file escaped the versions directory")
	}
}

func TestFindPicksTheNewestMatchingPackage(t *testing.T) {
	older := pkgFor("1.30.1", "20261101120000-aaaaaa", "2026-11-01T12:00:00Z")
	newest := pkgFor("1.30.10", "20261120120000-bbbbbb", "2026-11-20T12:00:00Z")
	rebuilt := pkgFor("1.30.10", "20261121120000-cccccc", "2026-11-21T12:00:00Z")
	otherSeries := pkgFor("1.28.4", "20261121130000-dddddd", "2026-11-21T13:00:00Z")

	arm := rebuilt
	arm.Arch = "arm64"
	debian := rebuilt
	debian.Distro = "debian"
	otherRoot := rebuilt
	otherRoot.Root = "/srv/www"
	otherRecipe := rebuilt
	otherRecipe.Recipe = "zzz999"
	hostile := rebuilt
	hostile.File = "../../etc/passwd"
	badID := rebuilt
	badID.ID = "../x"

	src := publish(t, Index{Version: IndexVersion, Packages: []Package{
		older, newest, rebuilt, otherSeries, arm, debian, otherRoot, otherRecipe, hostile, badID,
	}}, nil)

	got, err := src.Find(ctx0, want("1.30"))
	if err != nil {
		t.Fatalf("Find 1.30: %v", err)
	}
	// 1.30.10 is higher than 1.30.1 even though "10" sorts before "2" as text, and
	// of two builds of it the later one wins. Every lookalike that does not fit the
	// host was skipped, including the ones with hostile fields.
	if got.ID != rebuilt.ID {
		t.Errorf("Find 1.30 picked %s (%s), want %s", got.ID, got.Release, rebuilt.ID)
	}

	got, err = src.Find(ctx0, want("1.30.1"))
	if err != nil || got.ID != older.ID {
		t.Errorf("Find 1.30.1 = %v, %v; a release is matched exactly", got.ID, err)
	}
	got, err = src.Find(ctx0, want("1.28"))
	if err != nil || got.ID != otherSeries.ID {
		t.Errorf("Find 1.28 = %v, %v", got.ID, err)
	}
}

func TestFindReportsWhyNothingFits(t *testing.T) {
	p := pkgFor("1.30.2", idA, "2026-11-21T12:00:00Z")
	src := publish(t, Index{Version: IndexVersion, Packages: []Package{p}}, nil)

	for name, w := range map[string]Want{
		"another series":  want("1.29"),
		"another release": want("1.30.9"),
		"another arch":    {Product: "nginx", Version: "1.30", Host: wantHost, Arch: "arm64", Root: "/www", Recipe: "abc123"},
		"another host":    {Product: "nginx", Version: "1.30", Host: Host{ID: "ubuntu", VersionID: "22.04"}, Arch: "amd64", Root: "/www", Recipe: "abc123"},
		"another recipe":  {Product: "nginx", Version: "1.30", Host: wantHost, Arch: "amd64", Root: "/www", Recipe: "other"},
	} {
		if _, err := src.Find(ctx0, w); !errors.Is(err, ErrNoPackage) {
			t.Errorf("%s: err = %v, want ErrNoPackage", name, err)
		}
	}

	// A source that has published nothing yet is "no package", not a failure.
	empty := httptest.NewServer(http.NotFoundHandler())
	defer empty.Close()
	if _, err := (Source{BaseURL: empty.URL}).Find(ctx0, want("1.30")); !errors.Is(err, ErrNoPackage) {
		t.Errorf("a missing index: err = %v, want ErrNoPackage", err)
	}

	// An index in a layout this code does not know is refused, not guessed at.
	future := publish(t, Index{Version: IndexVersion + 1, Packages: []Package{p}}, nil)
	if _, err := future.Find(ctx0, want("1.30")); err == nil || errors.Is(err, ErrNoPackage) {
		t.Errorf("a newer index layout: err = %v, want a hard error", err)
	}
}

func TestCheckBaseURL(t *testing.T) {
	for url, ok := range map[string]bool{
		"https://github.com/o/r/releases/download/runtimes": true,
		"http://127.0.0.1:8080/x":                           true,
		"http://localhost/x":                                true,
		"http://[::1]:9/x":                                  true,
		"http://example.com/x":                              false,
		"ftp://example.com/x":                               false,
		"https://user:pw@example.com/x":                     false,
		"https://example.com/x?a=b":                         false,
		"https://example.com/x#frag":                        false,
		"":                                                  false,
		"https://":                                          false,
	} {
		if got := CheckBaseURL(url) == nil; got != ok {
			t.Errorf("CheckBaseURL(%q) ok = %v, want %v", url, got, ok)
		}
	}
	// A source never contacts a URL it would refuse to be configured with.
	if _, err := (Source{BaseURL: "http://example.com"}).Find(ctx0, want("1.30")); err == nil {
		t.Error("Find contacted a plain-http, non-loopback source")
	}
}

func TestReleaseMatches(t *testing.T) {
	for _, c := range []struct {
		version, release string
		ok               bool
	}{
		{"1.30", "1.30.2", true},
		{"1.30", "1.30.10", true},
		{"1.30", "1.300.1", false}, // a prefix of the digits is not the series
		{"1.3", "1.30.2", false},
		{"1.30.2", "1.30.2", true},
		{"1.30.2", "1.30.20", false},
		{"1.30.2", "1.30.1", false},
	} {
		if got := ReleaseMatches(c.version, c.release); got != c.ok {
			t.Errorf("ReleaseMatches(%q, %q) = %v, want %v", c.version, c.release, got, c.ok)
		}
	}
}

func TestBuildIndexKeepsTheNewestOfEachGroup(t *testing.T) {
	a := pkgFor("1.30.1", "20261101000000-aaaaaa", "2026-11-01T00:00:00Z")
	b := pkgFor("1.30.2", "20261110000000-bbbbbb", "2026-11-10T00:00:00Z")
	c := pkgFor("1.30.3", "20261120000000-cccccc", "2026-11-20T00:00:00Z")
	arm := pkgFor("1.30.3", "20261120000001-dddddd", "2026-11-20T00:00:01Z")
	arm.Arch = "arm64"
	other := pkgFor("1.28.4", "20261001000000-eeeeee", "2026-10-01T00:00:00Z")
	broken := pkgFor("1.30.9", "20261121000000-ffffff", "2026-11-21T00:00:00Z")
	broken.SHA256 = "not a digest"

	idx, drop := BuildIndex([]Package{a, b, c, arm, other, broken}, 2)

	if idx.Version != IndexVersion {
		t.Errorf("index version = %d", idx.Version)
	}
	var got []string
	for _, p := range idx.Packages {
		got = append(got, p.ID)
	}
	// Newest first overall; the oldest of the 1.30 amd64 group is gone, the other
	// groups (another architecture, another series) are untouched, and the record
	// with a bad digest is left out rather than published.
	want := []string{arm.ID, c.ID, b.ID, other.ID}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("published %v, want %v", got, want)
	}
	wantDrop := []string{ManifestName(a.File), a.File}
	if strings.Join(drop, ",") != strings.Join(wantDrop, ",") {
		t.Errorf("drop = %v, want %v: a package and its manifest, and never the malformed one", drop, wantDrop)
	}

	// An empty input is a valid, empty index, never null.
	empty, none := BuildIndex(nil, 2)
	if empty.Packages == nil || len(none) != 0 {
		t.Errorf("empty input gave %+v, %v", empty, none)
	}
}

func TestPackBuildRefusesToOverwrite(t *testing.T) {
	out := filepath.Join(t.TempDir(), "p.tar.gz")
	if err := os.WriteFile(out, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PackBuild(buildTree(t), idA, out); err == nil {
		t.Fatal("PackBuild overwrote an existing file")
	}
	if b, _ := os.ReadFile(out); !strings.HasPrefix(string(b), "keep") {
		t.Errorf("the existing file was changed: %q", b)
	}
}
