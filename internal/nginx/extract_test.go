package nginx

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testID = "20261118040935-fcd8dc"

type entry struct {
	name, body, link string
	typ              byte
	mode             int64
}

func archive(t *testing.T, entries ...entry) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Linkname: e.link}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

func goodBuild(t *testing.T) *bytes.Reader {
	return archive(t,
		entry{name: testID + "/", typ: tar.TypeDir, mode: 0o755},
		entry{name: testID + "/sbin/", typ: tar.TypeDir, mode: 0o755},
		entry{name: testID + "/sbin/nginx", body: "ELF", mode: 0o755},
		entry{name: testID + "/version", body: "1.30.1\n"},
		entry{name: testID + "/lib/libz.so.1", body: "lib"},
		entry{name: testID + "/conf/mime.types", body: "types {}"},
	)
}

func TestExtractBuildUnpacksUnderTheID(t *testing.T) {
	dir := t.TempDir()
	if err := ExtractBuild(goodBuild(t), dir, testID); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, testID, "sbin", "nginx"))
	if err != nil || string(got) != "ELF" {
		t.Fatalf("sbin/nginx = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, testID, "conf", "mime.types")); err != nil {
		t.Error(err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".extract-*")); len(left) != 0 {
		t.Errorf("the staging directory was left behind: %v", left)
	}
}

func TestExtractBuildRefusesWhatCouldEscapeTheBuild(t *testing.T) {
	for name, entries := range map[string][]entry{
		"climbing name": {{name: testID + "/../evil", body: "x"}},
		"absolute name": {{name: "/etc/passwd", body: "x"}},
		"foreign top":   {{name: "other/sbin/nginx", body: "x"}},
		"absolute link": {{name: testID + "/lib/l", typ: tar.TypeSymlink, link: "/etc/passwd"}},
		"climbing link": {{name: testID + "/lib/l", typ: tar.TypeSymlink, link: "../../x"}},
		"device":        {{name: testID + "/dev", typ: tar.TypeChar}},
		"hard link":     {{name: testID + "/hl", typ: tar.TypeLink, link: testID + "/sbin/nginx"}},
	} {
		dir := t.TempDir()
		err := ExtractBuild(archive(t, entries...), dir, testID)
		if err == nil {
			t.Errorf("%s: extracted", name)
			continue
		}
		if _, serr := os.Stat(filepath.Join(dir, testID)); serr == nil {
			t.Errorf("%s: a partial build was left at its final path", name)
		}
	}
}

func TestExtractBuildKeepsAnInsideLink(t *testing.T) {
	dir := t.TempDir()
	err := ExtractBuild(archive(t,
		entry{name: testID + "/sbin/nginx", body: "ELF", mode: 0o755},
		entry{name: testID + "/lib/libx.so", typ: tar.TypeSymlink, link: "libx.so.1"},
		entry{name: testID + "/lib/libx.so.1", body: "lib"},
	), dir, testID)
	if err != nil {
		t.Skipf("symbolic links are not available here: %v", err)
	}
	if got, err := os.Readlink(filepath.Join(dir, testID, "lib", "libx.so")); err != nil || got != "libx.so.1" {
		t.Errorf("link = %q, %v", got, err)
	}
}

func TestExtractBuildNeedsABinary(t *testing.T) {
	dir := t.TempDir()
	err := ExtractBuild(archive(t, entry{name: testID + "/lib/libz.so.1", body: "lib"}), dir, testID)
	if err == nil || !strings.Contains(err.Error(), "sbin/nginx") {
		t.Errorf("an archive without a binary = %v", err)
	}
}

func TestExtractBuildNeedsTheCompiledVersion(t *testing.T) {
	dir := t.TempDir()
	err := ExtractBuild(archive(t,
		entry{name: testID + "/sbin/nginx", body: "ELF", mode: 0o755},
	), dir, testID)
	if err == nil || !strings.Contains(err.Error(), "no version") {
		t.Errorf("an archive without a version = %v", err)
	}
}

func TestBuildVersionReadsWhatTheBuildCompiled(t *testing.T) {
	p := Paths{Root: filepath.ToSlash(t.TempDir())}
	if err := ExtractBuild(goodBuild(t), filepath.FromSlash(p.VersionsDir()), testID); err != nil {
		t.Fatal(err)
	}
	if got, err := BuildVersion(p, testID); err != nil || got != "1.30.1" {
		t.Errorf("BuildVersion = %q, %v; want 1.30.1", got, err)
	}
}

func TestExtractBuildNeverOverwritesABuild(t *testing.T) {
	dir := t.TempDir()
	if err := ExtractBuild(goodBuild(t), dir, testID); err != nil {
		t.Fatal(err)
	}
	if err := ExtractBuild(goodBuild(t), dir, testID); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("a second extraction of the same ID = %v", err)
	}
}
