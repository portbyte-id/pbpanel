package nginx

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/docker"
)

func TestParseHostReadsOsRelease(t *testing.T) {
	h, err := parseHost(strings.NewReader("PRETTY_NAME=\"Ubuntu 26.04.1 LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"26.04\"\nID=ubuntu\nID_LIKE=debian\n"))
	if err != nil || h.ID != "ubuntu" || h.VersionID != "26.04" {
		t.Errorf("host = %+v, %v", h, err)
	}
	if _, err := parseHost(strings.NewReader("NAME=x\n")); err == nil {
		t.Error("a release without ID and VERSION_ID parsed")
	}
}

func TestBuilderImageMatchesTheHost(t *testing.T) {
	for host, want := range map[Host]string{
		{ID: "ubuntu", VersionID: "26.04"}: "ubuntu:26.04",
		{ID: "debian", VersionID: "12"}:    "debian:12",
	} {
		if got, err := BuilderImage(host); err != nil || got != want {
			t.Errorf("BuilderImage(%+v) = %q, %v; want %q", host, got, err, want)
		}
	}
	// The EL family waits for M9; it must say so, not build a mismatched binary.
	for _, id := range []string{"rocky", "almalinux", "rhel"} {
		if _, err := BuilderImage(Host{ID: id, VersionID: "9.8"}); !errors.Is(err, ErrUnsupportedHost) {
			t.Errorf("%s: %v, want ErrUnsupportedHost", id, err)
		}
	}
}

func TestDockerfileForFillsEveryPlaceholder(t *testing.T) {
	d := string(DockerfileFor("ubuntu:26.04", testID, "1.30", testPaths))
	if i := strings.Index(d, "@"); i >= 0 {
		end := min(i+30, len(d))
		t.Fatalf("a placeholder is left: %q", d[i:end])
	}
	for _, want := range []string{
		"FROM ubuntu:26.04",
		"nginx-1.30",
		`P="/www/panel/nginx/versions/` + testID + `"`,
		"--conf-path=\"/www/panel/nginx/nginx.conf\"",
		"openssl-3.5",
		"--user=www --group=www",
		"--without-curl",
		"fastcgi_params",
		"mime.types",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
}

func TestVersionHandling(t *testing.T) {
	for v, ok := range map[string]bool{"1.30": true, "1.9": true, "1.2.3": false, "pqc-1.30": false, "": false, "1.30; rm": false} {
		if got := CheckVersion(v) == nil; got != ok {
			t.Errorf("CheckVersion(%q) ok = %v, want %v", v, got, ok)
		}
	}
	// A version stored by the post-quantum flavor keeps working as its series.
	if got := NormalizeVersion("pqc-1.30"); got != "1.30" {
		t.Errorf("NormalizeVersion = %q", got)
	}
}

// fakeBuildEngine is the Docker side of a build: it records what was made and
// removed, and serves an archive of a build for the extraction.
type fakeBuildEngine struct {
	archive []byte
	buildOK bool
	calls   []string
	dir     string // BuildOptions.Dockerfile as sent
}

func (f *fakeBuildEngine) ImageBuild(_ context.Context, o docker.BuildOptions, _ docker.Progress) (docker.Image, error) {
	f.calls = append(f.calls, "build "+o.Tag)
	f.dir = string(o.Dockerfile)
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

func osRelease(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func buildArchive(t *testing.T, id string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range []struct {
		name string
		dir  bool
		body string
	}{{id + "/", true, ""}, {id + "/sbin/", true, ""}, {id + "/sbin/nginx", false, "ELF"}} {
		hdr := &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(e.body))}
		if e.dir {
			hdr.Typeflag, hdr.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if !e.dir {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	return buf.Bytes()
}

func TestDockerBuilderLeavesAFinishedBuildAndCleansUp(t *testing.T) {
	p := Paths{Root: filepath.ToSlash(t.TempDir())}
	eng := &fakeBuildEngine{archive: buildArchive(t, testID), buildOK: true}
	b := DockerBuilder{Eng: eng, P: p, OSRelease: osRelease(t, "ID=ubuntu\nVERSION_ID=\"26.04\"\n")}

	var lines []string
	if err := b.Build(context.Background(), testID, "1.30", func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.FromSlash(p.BinaryOf(testID))); err != nil {
		t.Errorf("the build is not at %s: %v", p.BuildDir(testID), err)
	}
	if !strings.Contains(eng.dir, "FROM ubuntu:26.04") {
		t.Errorf("the builder image was not matched to the host:\n%.200s", eng.dir)
	}
	for _, want := range []string{
		"build panel/nginx-build:" + testID,
		"archive cid " + p.BuildDir(testID),
		"remove container cid",
		"remove image panel/nginx-build:" + testID,
	} {
		if !contains(eng.calls, want) {
			t.Errorf("calls = %v, want %q", eng.calls, want)
		}
	}
	if len(lines) == 0 {
		t.Error("no progress was reported")
	}
}

func TestDockerBuilderFailureLeavesNothingBehind(t *testing.T) {
	p := Paths{Root: filepath.ToSlash(t.TempDir())}
	eng := &fakeBuildEngine{buildOK: false}
	b := DockerBuilder{Eng: eng, P: p, OSRelease: osRelease(t, "ID=ubuntu\nVERSION_ID=\"26.04\"\n")}
	if err := b.Build(context.Background(), testID, "1.30", nil); err == nil {
		t.Fatal("a failed build succeeded")
	}
	if _, err := os.Stat(filepath.FromSlash(p.BuildDir(testID))); err == nil {
		t.Error("a failed build left a directory")
	}
}

func TestDockerBuilderRefusesAnUnsupportedHostBeforeBuilding(t *testing.T) {
	eng := &fakeBuildEngine{buildOK: true}
	b := DockerBuilder{Eng: eng, P: testPaths, OSRelease: osRelease(t, "ID=rocky\nVERSION_ID=\"9.8\"\n")}
	if err := b.Build(context.Background(), testID, "1.30", nil); !errors.Is(err, ErrUnsupportedHost) {
		t.Errorf("Build on Rocky = %v, want ErrUnsupportedHost", err)
	}
	if len(eng.calls) != 0 {
		t.Errorf("Docker was used for an unsupported host: %v", eng.calls)
	}
}
