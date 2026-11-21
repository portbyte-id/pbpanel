//go:build unix

package nginx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
	"github.com/portbyte-id/server-panel/internal/native"
)

const hostRelease = "ID=ubuntu\nVERSION_ID=\"24.04\"\n"

// recordingBuilder is the source build: it makes a build as fakeBuilder does and
// remembers the IDs it was asked for.
type recordingBuilder struct {
	inner fakeBuilder
	ids   []string
}

func (r *recordingBuilder) Build(ctx context.Context, id, version string, progress docker.Progress) error {
	r.ids = append(r.ids, id)
	return r.inner.Build(ctx, id, version, progress)
}

// publishedBuild packs a finished nginx build, as CI would, and returns the
// archive with the record that describes it for p.
func publishedBuild(t *testing.T, p Paths, release, id string) ([]byte, native.Package) {
	t.Helper()
	tree := t.TempDir()
	for name, body := range map[string]string{
		"sbin/nginx": "#!/bin/sh\n", "version": release + "\n", "conf/mime.types": "types",
	} {
		f := filepath.Join(tree, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "pkg.tar.gz")
	digest, size, err := native.PackBuild(tree, id, out)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return body, native.Package{
		Product: "nginx", Release: release, ID: id,
		Distro: "ubuntu", VersionID: "24.04", Arch: "amd64", Root: p.Root, Recipe: RecipeID(),
		File: "nginx-" + release + "-" + id + ".tar.gz", SHA256: digest, Size: size,
		Built: "2026-11-21T12:00:00Z",
	}
}

// releaseFolder serves an index and assets as the project's release folder would.
func releaseFolder(t *testing.T, pkgs []native.Package, assets map[string][]byte) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(native.Index{Version: native.IndexVersion, Packages: pkgs})
	})
	for name, body := range assets {
		mux.HandleFunc("/"+name, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// emptyFolder is a source that has published nothing.
func emptyFolder(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// prebuiltAt is a Prebuilt for an amd64 Ubuntu 24.04 host.
func prebuiltAt(t *testing.T, p Paths, url string, on bool, fallback Builder) *Prebuilt {
	t.Helper()
	return &Prebuilt{
		Fallback: fallback, P: p, Arch: "amd64", OSRelease: osRelease(t, hostRelease),
		Enabled: func(context.Context) bool { return on },
		URL:     func(context.Context) string { return url },
	}
}

func never(string) bool { return false }

const ciID = "20261121120000-a1b2c3"

func TestRecipeIDIsStableAndShort(t *testing.T) {
	a, b := RecipeID(), RecipeID()
	if a != b || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(a) {
		t.Errorf("RecipeID = %q then %q, want one stable 16-digit hex string", a, b)
	}
}

// With a package that fits, a first build is the download: the family keeps it
// under the package's own ID, nothing is compiled, and the build serves.
func TestImagesBuildUnderThePackagesIDWithoutCompiling(t *testing.T) {
	p, h := nativePaths(t), newHost()
	body, pkg := publishedBuild(t, p, "1.30.2", ciID)
	url := releaseFolder(t, []native.Package{pkg}, map[string][]byte{pkg.File: body})
	src := &recordingBuilder{inner: fakeBuilder{p: p}}
	pre := prebuiltAt(t, p, url, true, src)

	imgs, err := NewImages(p, h, pre, images.NewCatalog(openStore(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	first, err := imgs.Build(context.Background(), func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if first.ID != ciID {
		t.Fatalf("the build is %s, want the package's own ID %s", first.ID, ciID)
	}
	if len(src.ids) != 0 {
		t.Errorf("the source build ran for %v although a package fit", src.ids)
	}
	if got, err := readLink(p); err != nil || got != ciID {
		t.Errorf("the link names %q (%v), want %s", got, err, ciID)
	}
	if rel, err := BuildVersion(p, ciID); err != nil || rel != "1.30.2" {
		t.Errorf("BuildVersion = %q, %v", rel, err)
	}
	if _, err := os.Stat(filepath.FromSlash(p.MimeTypes())); err != nil {
		t.Errorf("the package's files are not behind the current link: %v", err)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "using the prebuilt nginx 1.30.2") {
		t.Errorf("progress never said a package was used:\n%s", strings.Join(lines, "\n"))
	}

	// The package's build is on the host now, so the next update has nothing newer
	// to download and compiles, under an ID of its own, rather than colliding.
	lines = nil
	second, err := imgs.Build(context.Background(), func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if second.ID == ciID || len(src.ids) != 1 || src.ids[0] != second.ID {
		t.Errorf("second build %s, source builds %v: want one compile under a generated ID", second.ID, src.ids)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "already on this host") {
		t.Errorf("progress did not say why it compiled:\n%s", strings.Join(lines, "\n"))
	}
}

func TestPrebuiltIsOffUnlessTheOperatorTurnsItOn(t *testing.T) {
	p := nativePaths(t)
	body, pkg := publishedBuild(t, p, "1.30.2", ciID)
	url := releaseFolder(t, []native.Package{pkg}, map[string][]byte{pkg.File: body})
	src := &recordingBuilder{inner: fakeBuilder{p: p}}
	pre := prebuiltAt(t, p, url, false, src)

	if id, ok := pre.PickID(context.Background(), "1.30", never); ok {
		t.Fatalf("PickID offered %s while the setting is off", id)
	}
	var lines []string
	if err := pre.Build(context.Background(), "20261121130000-ffffff", "1.30", func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	if len(src.ids) != 1 || len(lines) != 0 {
		t.Errorf("source builds %v, progress %v: want a plain source build and no mention of packages", src.ids, lines)
	}
}

func TestPrebuiltCompilesWhenNothingFits(t *testing.T) {
	p := nativePaths(t)
	body, pkg := publishedBuild(t, p, "1.30.2", ciID)

	oldRecipe := pkg
	oldRecipe.Recipe = "0000000000000000" // made from an older recipe than this panel ships
	otherRelease := pkg
	otherRelease.VersionID = "22.04" // made for another release of the distro: another glibc
	otherRoot := pkg
	otherRoot.Root = "/srv/www" // its compiled-in paths are not this host's

	for name, url := range map[string]string{
		"nothing published": emptyFolder(t),
		"an older recipe":   releaseFolder(t, []native.Package{oldRecipe}, map[string][]byte{pkg.File: body}),
		"another release":   releaseFolder(t, []native.Package{otherRelease}, map[string][]byte{pkg.File: body}),
		"another root":      releaseFolder(t, []native.Package{otherRoot}, map[string][]byte{pkg.File: body}),
	} {
		t.Run(name, func(t *testing.T) {
			src := &recordingBuilder{inner: fakeBuilder{p: p}}
			pre := prebuiltAt(t, p, url, true, src)
			if id, ok := pre.PickID(context.Background(), "1.30", never); ok {
				t.Fatalf("PickID offered %s", id)
			}
			const own = "20261121140000-eeeeee"
			var lines []string
			if err := pre.Build(context.Background(), own, "1.30", func(l string) { lines = append(lines, l) }); err != nil {
				t.Fatal(err)
			}
			if len(src.ids) != 1 || src.ids[0] != own {
				t.Errorf("source builds %v, want one for %s", src.ids, own)
			}
			if !strings.Contains(strings.Join(lines, "\n"), "no prebuilt package used") {
				t.Errorf("progress did not say why it compiled:\n%s", strings.Join(lines, "\n"))
			}
		})
	}
}

// A package whose bytes are not what the index promised is never unpacked. The
// host still gets nginx: it compiles, under the ID it already chose.
func TestPrebuiltCompilesWhenThePackageIsCorrupt(t *testing.T) {
	p := nativePaths(t)
	body, pkg := publishedBuild(t, p, "1.30.2", ciID)
	body[len(body)/2] ^= 0xff
	url := releaseFolder(t, []native.Package{pkg}, map[string][]byte{pkg.File: body})
	src := &recordingBuilder{inner: fakeBuilder{p: p}}
	pre := prebuiltAt(t, p, url, true, src)

	id, ok := pre.PickID(context.Background(), "1.30", never)
	if !ok || id != ciID {
		t.Fatalf("PickID = %q, %v; the index describes a package that fits", id, ok)
	}
	var lines []string
	if err := pre.Build(context.Background(), id, "1.30", func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(src.ids) != 1 || src.ids[0] != ciID {
		t.Errorf("source builds %v, want one under %s", src.ids, ciID)
	}
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "sha256") || !strings.Contains(out, "building from source instead") {
		t.Errorf("progress should name the digest mismatch and the fallback:\n%s", out)
	}
	// What is on disk is the compiled build, not the corrupt package: the source
	// build has a binary, and the package's own files are absent.
	if _, err := os.Stat(filepath.FromSlash(p.BinaryOf(ciID))); err != nil {
		t.Errorf("no build under %s: %v", ciID, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.FromSlash(p.BuildDir(ciID)), "conf", "mime.types")); err == nil {
		t.Error("files from the corrupt package are on disk")
	}
}
