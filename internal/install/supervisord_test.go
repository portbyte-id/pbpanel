package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestPinnedReleasesAreWellFormed(t *testing.T) {
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for arch, rel := range supervisordReleases {
		if !hex64.MatchString(rel.SHA256) {
			t.Errorf("%s: checksum %q is not a SHA-256", arch, rel.SHA256)
		}
		if !strings.HasPrefix(rel.URL, "https://github.com/ochinchina/supervisord/releases/download/"+rel.Version+"/") ||
			!strings.HasSuffix(rel.URL, "linux_"+arch) {
			t.Errorf("%s: URL %q does not name the pinned release for that architecture", arch, rel.URL)
		}
	}
	if len(supervisordReleases) != 2 {
		t.Errorf("amd64 and arm64 are the supported architectures, got %d pins", len(supervisordReleases))
	}
}

func TestFetchVerifiedInstallsOnAMatch(t *testing.T) {
	body := []byte("supervisord-binary")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "bin", "supervisord")
	if err := fetchVerified(context.Background(), srv.Client(), srv.URL, sum(body), dest, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(body) {
		t.Fatalf("installed file: %q %v", got, err)
	}
	if _, err := os.Stat(dest + downloadSuffix); !os.IsNotExist(err) {
		t.Fatal("the temporary download was left behind")
	}
}

func TestFetchVerifiedAbortsOnAChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("tampered")) }))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "supervisord")
	if err := os.WriteFile(dest, []byte("previous good binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := fetchVerified(context.Background(), srv.Client(), srv.URL, sum([]byte("the pinned binary")), dest, 0o755)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") || !strings.Contains(err.Error(), "nothing was installed") {
		t.Fatalf("got %v, want a checksum mismatch", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "previous good binary" {
		t.Fatalf("a refused download replaced the installed binary: %q", got)
	}
	if _, err := os.Stat(dest + downloadSuffix); !os.IsNotExist(err) {
		t.Fatal("the rejected download was left behind")
	}
}

func TestFetchVerifiedRefusesAnErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "supervisord")
	if err := fetchVerified(context.Background(), srv.Client(), srv.URL, sum(nil), dest, 0o755); err == nil {
		t.Fatal("a 404 was accepted")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("a file exists after a failed download")
	}
}

func TestDefaultFetchRefusesPlainHTTP(t *testing.T) {
	err := defaultFetch(context.Background(), SupervisordRelease{URL: "http://example.test/supervisord", SHA256: sum(nil)}, filepath.Join(t.TempDir(), "x"))
	if err == nil || !strings.Contains(err.Error(), "only https") {
		t.Fatalf("got %v", err)
	}
}

func TestSupervisordConfIsRootOnlyAndHasNoTCPListener(t *testing.T) {
	l := Layout{Root: "/www"}
	conf := SupervisordConf(l)
	for _, want := range []string{
		"[unix_http_server]\nfile=/www/panel/supervisor/supervisord.sock\nchmod=0700\n",
		"[include]\nfiles=/www/panel/supervisor/conf.d/*.conf\n",
		"pidfile=/www/panel/supervisor/supervisord.pid\n",
		"logfile=/www/panel/logs/supervisord.log\n",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("missing %q in:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "inet_http_server") {
		t.Error("supervisord must not open a TCP listener")
	}
}

func TestSupervisordUnitIsIndependentOfThePanel(t *testing.T) {
	unit := SupervisordUnitFile(Layout{Root: "/www"})
	if !strings.Contains(unit, "ExecStart=/www/panel/bin/supervisord -c /www/panel/supervisor/supervisord.conf\n") ||
		!strings.Contains(unit, "Restart=always\n") {
		t.Fatalf("unit:\n%s", unit)
	}
	for _, forbidden := range []string{"panel.service", "BindsTo", "PartOf", "Requires"} {
		if strings.Contains(unit, forbidden) {
			t.Errorf("the unit mentions %q; apps must survive panel restarts", forbidden)
		}
	}
}
