package native

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testID = "20261118040935-fcd8dc"

// fakeRunner records the host commands a helper runs and answers them from fn.
type fakeRunner struct {
	calls []string
	fn    func(name string, args []string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if f.fn != nil {
		return f.fn(name, args)
	}
	return nil, nil
}

func show(load, active string, pid int, since string) []byte {
	return fmt.Appendf(nil, "LoadState=%s\nActiveState=%s\nMainPID=%d\nActiveEnterTimestampMonotonic=%s\n", load, active, pid, since)
}

// The unit name and the product label are the caller's: two products sharing
// these helpers must not name each other in an error.
func TestStatusNamesTheUnitItWasGiven(t *testing.T) {
	run := &fakeRunner{fn: func(string, []string) ([]byte, error) { return show("loaded", "active", 7, "100"), nil }}
	u := Unit{Product: "php", Name: "panel-php-8.3.service"}
	st, err := StatusOf(context.Background(), run, u)
	if err != nil || !st.Active || st.PID != 7 || st.Since != "100" {
		t.Fatalf("StatusOf = %+v, %v", st, err)
	}
	want := "systemctl show -p LoadState -p ActiveState -p MainPID -p ActiveEnterTimestampMonotonic panel-php-8.3.service"
	if !slices.Contains(run.calls, want) {
		t.Errorf("calls = %v, want %q", run.calls, want)
	}

	failing := &fakeRunner{fn: func(string, []string) ([]byte, error) { return []byte("nope"), errors.New("boom") }}
	if _, err := StatusOf(context.Background(), failing, u); err == nil || !strings.HasPrefix(err.Error(), "php: ") {
		t.Errorf("error = %v, want it to name php", err)
	}
}

func TestRunningAndStableReportTheProduct(t *testing.T) {
	u := Unit{Product: "php", Name: "panel-php-8.3.service"}
	down := &fakeRunner{fn: func(string, []string) ([]byte, error) { return show("loaded", "inactive", 0, "0"), nil }}
	if _, err := Running(context.Background(), down, u); err == nil || !strings.Contains(err.Error(), "php health") {
		t.Errorf("Running on a stopped unit = %v", err)
	}

	seq := func(answers ...[]byte) *fakeRunner {
		return &fakeRunner{fn: func(string, []string) ([]byte, error) {
			a := answers[0]
			if len(answers) > 1 {
				answers = answers[1:]
			}
			return a, nil
		}}
	}
	same := show("loaded", "active", 7, "100")
	restarted := show("loaded", "active", 9, "200")
	if err := Stable(context.Background(), seq(same, same), u, 0); err != nil {
		t.Errorf("a steady unit = %v", err)
	}
	if err := Stable(context.Background(), seq(same, restarted), u, 0); err == nil || !strings.Contains(err.Error(), "php restarted") {
		t.Errorf("a restarted unit = %v, want it to name php", err)
	}
}

func TestBuilderImageNamesTheProduct(t *testing.T) {
	if got, err := BuilderImage(Host{ID: "ubuntu", VersionID: "26.04"}, "php"); err != nil || got != "ubuntu:26.04" {
		t.Errorf("BuilderImage = %q, %v", got, err)
	}
	_, err := BuilderImage(Host{ID: "rocky", VersionID: "9.8"}, "php")
	if !errors.Is(err, ErrUnsupportedHost) || !strings.HasPrefix(err.Error(), "php: ") {
		t.Errorf("an unsupported host = %v, want a php-prefixed ErrUnsupportedHost", err)
	}
}

func TestParseHostNeedsAnIDAndAVersion(t *testing.T) {
	h, err := ParseHost(strings.NewReader("VERSION_ID=\"26.04\"\nID=ubuntu\n"), "php")
	if err != nil || h != (Host{ID: "ubuntu", VersionID: "26.04"}) {
		t.Errorf("ParseHost = %+v, %v", h, err)
	}
	if _, err := ParseHost(strings.NewReader("NAME=x\n"), "php"); err == nil || !strings.HasPrefix(err.Error(), "php: ") {
		t.Errorf("a release without ID = %v, want a php-prefixed error", err)
	}
}

func archive(t *testing.T, entries ...tar.Header) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := e
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

// The markers a build must hold are the caller's: PHP needs bin/php and version,
// nginx needs sbin/nginx and version, and neither accepts the other's archive.
func TestExtractBuildChecksTheCallersMarkers(t *testing.T) {
	phpArchive := func() *bytes.Reader {
		return archive(t,
			tar.Header{Name: testID + "/", Typeflag: tar.TypeDir, Mode: 0o755},
			tar.Header{Name: testID + "/bin/php", Typeflag: tar.TypeReg, Mode: 0o755, Size: 1},
			tar.Header{Name: testID + "/version", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1},
		)
	}
	if err := ExtractBuild(phpArchive(), t.TempDir(), testID, "php", []string{"bin/php", "version"}); err != nil {
		t.Fatalf("a PHP build was refused: %v", err)
	}

	err := ExtractBuild(phpArchive(), t.TempDir(), testID, "nginx", []string{"sbin/nginx", "version"})
	if err == nil || !strings.Contains(err.Error(), "no sbin/nginx") || !strings.HasPrefix(err.Error(), "nginx: ") {
		t.Errorf("nginx markers on a PHP build = %v, want a nginx-prefixed missing-marker error", err)
	}
}

func TestBuildVersionNamesTheProduct(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "version")
	if err := os.WriteFile(file, []byte("8.3.28\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := BuildVersion(file, testID, "php"); err != nil || got != "8.3.28" {
		t.Errorf("BuildVersion = %q, %v", got, err)
	}
	_, err := BuildVersion(filepath.Join(dir, "gone"), testID, "php")
	if err == nil || !strings.HasPrefix(err.Error(), "php: ") {
		t.Errorf("a missing version file = %v, want a php-prefixed error", err)
	}
}

func TestStoreIsBoundToTheCallersPaths(t *testing.T) {
	root := t.TempDir()
	versions := filepath.Join(root, "versions")
	link := filepath.Join(root, "current")
	bin := filepath.Join(versions, testID, "bin", "php")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := Store{
		VersionsDir: versions,
		BinaryOf:    func(id string) string { return filepath.Join(versions, id, "bin", "php") },
		Link:        link,
		Product:     "php",
	}
	ref := "panel/php:8.3-" + testID
	if _, err := s.ImageInspect(context.Background(), ref); err != nil {
		t.Fatalf("ImageInspect = %v", err)
	}
	if err := s.ImageRemove(context.Background(), ref); err != nil {
		t.Fatalf("ImageRemove = %v", err)
	}

	// The build the link names is never removed.
	if err := SetLink(link, testID, "php"); err != nil {
		t.Fatal(err)
	}
	if err := s.ImageRemove(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "serves") {
		t.Errorf("the serving build was removed: %v", err)
	}
	if got, err := ReadLink(link); err != nil || got != testID {
		t.Errorf("ReadLink = %q, %v", got, err)
	}
}
