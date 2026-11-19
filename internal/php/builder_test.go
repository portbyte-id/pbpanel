package php

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/native"
)

// testID is a build ID: the shape images.NewID makes.
const testID = "20261119103000-a1b2c3"

// minimalExtensions is what step 2 builds: the official core set plus the three
// extensions every site needs.
func minimalExtensions(t *testing.T) []Extension {
	t.Helper()
	exts, err := ParseExtensions([]string{"opcache", "mysqli", "pdo_mysql"})
	if err != nil {
		t.Fatal(err)
	}
	return exts
}

func TestBuilderGoldenSeries(t *testing.T) {
	golden(t, "builder-8.3.Dockerfile",
		DockerfileFor("ubuntu:26.04", testID, "8.3", minimalExtensions(t), testPaths), nil)
}

func TestBuilderGoldenRelease(t *testing.T) {
	golden(t, "builder-8.3.28.Dockerfile",
		DockerfileFor("debian:12", testID, "8.3.28", minimalExtensions(t), testPaths), nil)
}

// A catalogue extension with libraries and flags, a PECL one and one the core
// already builds, so the golden file pins every shape.
func TestBuilderGoldenMixedExtensions(t *testing.T) {
	exts, err := ParseExtensions([]string{"gd", "intl", "redis", "sodium"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "builder-mixed.Dockerfile", DockerfileFor("ubuntu:26.04", testID, "8.4", exts, testPaths), nil)
}

func TestDockerfileForFillsEveryPlaceholder(t *testing.T) {
	for name, d := range map[string]string{
		"series":  string(DockerfileFor("ubuntu:26.04", testID, "8.3", minimalExtensions(t), testPaths)),
		"release": string(DockerfileFor("debian:12", testID, "8.3.28", minimalExtensions(t), testPaths)),
		"none":    string(DockerfileFor("ubuntu:26.04", testID, "8.4", nil, testPaths)),
	} {
		if i := strings.Index(d, "@"); i >= 0 {
			t.Errorf("%s: a placeholder is left: %q", name, d[i:min(i+40, len(d))])
		}
	}
	d := string(DockerfileFor("ubuntu:26.04", testID, "8.3", minimalExtensions(t), testPaths))
	for _, want := range []string{
		"FROM ubuntu:26.04 AS build",
		`P="/www/panel/php/8.3/versions/` + testID + `"`,
		`--with-fpm-user="www" --with-fpm-group="www"`,
		"--with-fpm-systemd",
		`--with-config-file-scan-dir="$P/etc/conf.d"`,
		`LDFLAGS="-Wl,-rpath,\$ORIGIN/../lib"`,
		"file \\",
		"FROM scratch AS out",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("the recipe lacks %q", want)
		}
	}
}

// The prefix is the build's own directory in every layer, not a staging path
// copied at the end: PHP compiles extension_dir and the config scan dir in, and
// those strings have to resolve on the host. A build staged at a fixed path
// passes its own checks inside the container and then loads no shared extension
// on the host, because the compiled-in directory does not exist there.
//
// The cost is that the core layers name the build ID, so the core compile is not
// shared between builds. Both properties cannot hold at once: one binary has one
// compiled-in prefix.
func TestDockerfileForCompilesIntoTheBuildsOwnPrefix(t *testing.T) {
	d := string(DockerfileFor("ubuntu:26.04", testID, "8.3", minimalExtensions(t), testPaths))
	prefix := "/www/panel/php/8.3/versions/" + testID
	if !strings.Contains(d, `P="`+prefix+`"; multi=$(gcc -print-multiarch)`) {
		t.Error("the core configure does not use the build's own prefix")
	}
	if !strings.Contains(d, `--with-config-file-scan-dir="$P/etc/conf.d"`) {
		t.Error("the compiled-in scan dir is not the build's own")
	}
	for line := range strings.SplitSeq(d, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "RUN ") && strings.Contains(line, "/build/prefix") {
			t.Errorf("a layer compiles into a staging path, which the host cannot resolve: %q", line)
		}
	}
	if !strings.Contains(d, "COPY --from=build "+prefix+" "+prefix) {
		t.Error("the out stage does not read the build's own prefix")
	}
}

// The extension list is text, not a build argument: BuildKit invalidates every
// later layer when an ARG changes, which is the whole reason step 0 corrected D2.
func TestDockerfileForUsesNoBuildArguments(t *testing.T) {
	d := string(DockerfileFor("ubuntu:26.04", testID, "8.3", minimalExtensions(t), testPaths))
	for line := range strings.SplitSeq(d, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ARG ") {
			t.Errorf("the recipe has a build argument: %q", line)
		}
	}
	more, err := ParseExtensions([]string{"opcache", "mysqli", "pdo_mysql", "intl", "redis"})
	if err != nil {
		t.Fatal(err)
	}
	// Everything before the extension layers: the account, the packages and the
	// download. Those must not depend on the extension list, or an extension
	// change re-runs them.
	marker := "# Extensions as shared .so files"
	core := func(s string) string {
		i := strings.Index(s, marker)
		if i < 0 {
			t.Fatalf("the recipe has no extension section:\n%s", s)
		}
		return s[:i]
	}
	base := core(string(DockerfileFor("ubuntu:26.04", testID, "8.3", minimalExtensions(t), testPaths)))
	with := core(string(DockerfileFor("ubuntu:26.04", testID, "8.3", more, testPaths)))
	if base != with {
		t.Error("adding an extension changed the core layers, so they would rebuild")
	}
}

func TestExtensionRecipeShapes(t *testing.T) {
	d := string(DockerfileFor("ubuntu:26.04", testID, "8.3", minimalExtensions(t), testPaths))
	for _, want := range []string{
		`( cd ext/opcache && phpize && ./configure --enable-opcache && make -j"$(nproc)" && make install );`,
		`echo "zend_extension=opcache" > "$P/etc/conf.d/10-opcache.ini"`,
		`( cd ext/mysqli && phpize && ./configure --with-mysqli=mysqlnd && make -j"$(nproc)" && make install );`,
		`echo "extension=mysqli" > "$P/etc/conf.d/30-mysqli.ini"`,
		`( cd ext/pdo_mysql && phpize && ./configure --with-pdo-mysql=mysqlnd && make -j"$(nproc)" && make install );`,
		`echo "extension=pdo_mysql" > "$P/etc/conf.d/30-pdo_mysql.ini"`,
	} {
		if !strings.Contains(d, want) {
			t.Errorf("the recipe lacks %q", want)
		}
	}

	pecl, err := ParseExtensions([]string{"redis"})
	if err != nil {
		t.Fatal(err)
	}
	p := string(DockerfileFor("ubuntu:26.04", testID, "8.3", pecl, testPaths))
	if !strings.Contains(p, `printf '\n' | pecl install redis;`) {
		t.Errorf("a PECL extension is not installed with pecl:\n%s", p)
	}
	if !strings.Contains(p, `echo "extension=redis" > "$P/etc/conf.d/30-redis.ini"`) {
		t.Errorf("a PECL extension enables no conf.d file:\n%s", p)
	}

	gd, err := ParseExtensions([]string{"gd"})
	if err != nil {
		t.Fatal(err)
	}
	g := string(DockerfileFor("ubuntu:26.04", testID, "8.3", gd, testPaths))
	if !strings.Contains(g, "./configure --with-freetype --with-jpeg --with-webp") {
		t.Errorf("gd's catalogue Configure is not used:\n%s", g)
	}
	if !strings.Contains(g, " libfreetype-dev libjpeg-dev libpng-dev libwebp-dev") {
		t.Errorf("gd's packages are not installed:\n%s", g)
	}
}

// sodium is built shared by the core line and enabled by the recipe's own file;
// mbstring is compiled in. Asking for either must not build a second .so.
func TestExtensionRecipeSkipsWhatTheCoreBuilds(t *testing.T) {
	exts, err := ParseExtensions([]string{"mbstring", "sodium"})
	if err != nil {
		t.Fatal(err)
	}
	d := string(DockerfileFor("ubuntu:26.04", testID, "8.3", exts, testPaths))
	if strings.Contains(d, "ext/mbstring") || strings.Contains(d, "ext/sodium") {
		t.Errorf("an extension the core builds was built again:\n%s", d)
	}
	if !strings.Contains(d, `echo "extension=sodium" > "$P/etc/conf.d/20-sodium.ini"`) {
		t.Error("the core's sodium is not enabled by a conf.d file")
	}
	// The module list the build checks still names them: `php -m` must show both.
	if got := moduleNames(exts); !strings.Contains(got, "'sodium'") || !strings.Contains(got, "'mbstring'") {
		t.Errorf("moduleNames = %q", got)
	}
}

func TestBuildVersionHandling(t *testing.T) {
	for v, ok := range map[string]bool{
		"8.3": true, "8.10": true, "8.3.28": true, "7.4": true,
		"8": false, "8.3.28.1": false, "latest": false, "8.3-fpm": false, "../8.3": false, "": false,
	} {
		if got := CheckBuildVersion(v) == nil; got != ok {
			t.Errorf("CheckBuildVersion(%q) ok = %v, want %v", v, got, ok)
		}
	}
	for v, want := range map[string]string{"8.3": "8.3", "8.3.28": "8.3", "8.10.1": "8.10"} {
		if got := Series(v); got != want {
			t.Errorf("Series(%q) = %q, want %q", v, got, want)
		}
	}
	if exactRelease("8.3") != "" || exactRelease("8.3.28") != "8.3.28" {
		t.Errorf("exactRelease: %q, %q", exactRelease("8.3"), exactRelease("8.3.28"))
	}
}

// --- the build ---------------------------------------------------------------

// fakeBuildEngine is the Docker side of a build: it records what was made and
// removed, and serves an archive of a finished build.
type fakeBuildEngine struct {
	archive []byte
	buildOK bool
	calls   []string
	recipe  string
}

func (f *fakeBuildEngine) ImageBuild(_ context.Context, o docker.BuildOptions, _ docker.Progress) (docker.Image, error) {
	f.calls = append(f.calls, "build "+o.Tag)
	f.recipe = string(o.Dockerfile)
	if !f.buildOK {
		return docker.Image{}, errors.New("the command returned a non-zero code: 2")
	}
	return docker.Image{ID: "sha256:x"}, nil
}
func (f *fakeBuildEngine) ContainerCreate(_ context.Context, name string, _ docker.ContainerConfig) (string, error) {
	f.calls = append(f.calls, "create "+name)
	return "cid", nil
}
func (f *fakeBuildEngine) ContainerArchive(_ context.Context, id, path string) (io.ReadCloser, error) {
	f.calls = append(f.calls, "archive "+id+" "+path)
	return io.NopCloser(bytes.NewReader(f.archive)), nil
}
func (f *fakeBuildEngine) ContainerRemove(_ context.Context, id string, _ bool) error {
	f.calls = append(f.calls, "remove container "+id)
	return nil
}
func (f *fakeBuildEngine) ImageRemove(_ context.Context, ref string) error {
	f.calls = append(f.calls, "remove image "+ref)
	return nil
}

func contains(calls []string, want string) bool { return slices.Contains(calls, want) }

func osReleaseFile(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// phpBuildArchive is a finished build's prefix as the Engine reads it out: the
// top entry is the build ID, then the two programs and the version file.
func phpBuildArchive(t *testing.T, id string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range []struct {
		name string
		dir  bool
		body string
	}{
		{id + "/", true, ""},
		{id + "/sbin/", true, ""},
		{id + "/bin/", true, ""},
		{id + "/sbin/php-fpm", false, "ELF"},
		{id + "/bin/php", false, "ELF"},
		{id + "/version", false, "8.3.28\n"},
	} {
		hdr := &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(e.body))}
		if e.dir {
			hdr.Typeflag, hdr.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.dir {
			continue
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBuilderLeavesAFinishedPrefixAndCleansUp(t *testing.T) {
	p := Paths{Root: filepath.ToSlash(t.TempDir())}
	eng := &fakeBuildEngine{archive: phpBuildArchive(t, testID), buildOK: true}
	b := &Builder{Eng: eng, P: p, OSRelease: osReleaseFile(t, "ID=ubuntu\nVERSION_ID=\"26.04\"\n")}

	var lines []string
	if err := b.Build(context.Background(), testID, "8.3", minimalExtensions(t), func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{p.BinaryOf("8.3", testID), p.CLIBinaryOf("8.3", testID), p.VersionFile("8.3", testID)} {
		if _, err := os.Stat(filepath.FromSlash(want)); err != nil {
			t.Errorf("the build holds no %s: %v", want, err)
		}
	}
	if got, err := native.BuildVersion(filepath.FromSlash(p.VersionFile("8.3", testID)), testID, product); err != nil || got != "8.3.28" {
		t.Errorf("the build's version file = %q, %v", got, err)
	}
	if !strings.Contains(eng.recipe, "FROM ubuntu:26.04") {
		t.Errorf("the builder image was not matched to the host:\n%.200s", eng.recipe)
	}
	for _, want := range []string{
		"build panel/php-build:" + testID,
		"archive cid " + p.BuildPrefix("8.3", testID),
		"remove container cid",
		"remove image panel/php-build:" + testID,
	} {
		if !contains(eng.calls, want) {
			t.Errorf("calls = %v, want %q", eng.calls, want)
		}
	}
	if len(lines) == 0 {
		t.Error("no progress was reported")
	}
}

func TestBuilderFailureLeavesNothingBehind(t *testing.T) {
	p := Paths{Root: filepath.ToSlash(t.TempDir())}
	eng := &fakeBuildEngine{buildOK: false}
	b := &Builder{Eng: eng, P: p, OSRelease: osReleaseFile(t, "ID=ubuntu\nVERSION_ID=\"26.04\"\n")}
	if err := b.Build(context.Background(), testID, "8.3", minimalExtensions(t), nil); err == nil {
		t.Fatal("a failed build succeeded")
	}
	if _, err := os.Stat(filepath.FromSlash(p.BuildPrefix("8.3", testID))); err == nil {
		t.Error("a failed build left a directory")
	}
}

func TestBuilderRefusesAnUnsupportedHostBeforeDocker(t *testing.T) {
	eng := &fakeBuildEngine{buildOK: true}
	b := &Builder{Eng: eng, P: testPaths, OSRelease: osReleaseFile(t, "ID=rocky\nVERSION_ID=\"9.8\"\n")}
	if err := b.Build(context.Background(), testID, "8.3", nil, nil); !errors.Is(err, native.ErrUnsupportedHost) {
		t.Errorf("Build on Rocky = %v, want ErrUnsupportedHost", err)
	}
	if len(eng.calls) != 0 {
		t.Errorf("Docker was used for an unsupported host: %v", eng.calls)
	}
}

func TestBuilderRefusesAVersionThatIsNotOne(t *testing.T) {
	eng := &fakeBuildEngine{buildOK: true}
	b := &Builder{Eng: eng, P: testPaths}
	for _, bad := range []string{"8.3-fpm", "../8.3", "latest", ""} {
		if err := b.Build(context.Background(), testID, bad, nil, nil); err == nil {
			t.Errorf("Build(%q) was accepted", bad)
		}
	}
	if len(eng.calls) != 0 {
		t.Errorf("Docker was used for an invalid version: %v", eng.calls)
	}
}
