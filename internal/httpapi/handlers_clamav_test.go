package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/clamav"
	"github.com/portbyte-id/server-panel/internal/fm"
	"github.com/portbyte-id/server-panel/internal/jobs"
)

// fakeClamav records what it was asked to do and never touches a host.
type fakeClamav struct {
	avail   clamav.Availability
	scanned []string
	scanErr error
	install int
	uninst  int
}

func (f *fakeClamav) Availability(context.Context) clamav.Availability { return f.avail }

func (f *fakeClamav) Install(_ context.Context, progress func(string)) error {
	f.install++
	progress("installing")
	return nil
}

func (f *fakeClamav) Uninstall(context.Context, func(string)) error {
	f.uninst++
	return nil
}

func (f *fakeClamav) Scan(_ context.Context, path string, progress func(string)) (clamav.ScanResult, error) {
	f.scanned = append(f.scanned, path)
	progress("scanning " + path)
	return clamav.ScanResult{Path: path}, f.scanErr
}

// clamavFiles is the file manager as the scan route uses it: it cleans a path and
// says whether it exists. Every other method would panic, which is the point: the
// scan route must not need them.
type clamavFiles struct {
	FileManager
	exists map[string]bool
}

func (c clamavFiles) Clean(p string) (string, error) {
	if !strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return "", fm.ErrInvalidPath
	}
	return p, nil
}

func (c clamavFiles) Stat(clean string) (fm.Entry, error) {
	if !c.exists[clean] {
		return fm.Entry{}, fm.ErrNotFound
	}
	return fm.Entry{Path: clean}, nil
}

func newClamavHarness(t *testing.T) (*nginxHarness, *fakeClamav) {
	t.Helper()
	h := newWAFHarness(t, true)
	fake := &fakeClamav{avail: clamav.Availability{
		Status:   clamav.Status{Installed: true, Version: "1.4.3", SignatureVersion: "27800", DaemonActive: true, UpdaterActive: true},
		Packages: clamav.Packages,
	}}
	h.deps.Clamav = fake
	h.deps.Files = clamavFiles{exists: map[string]bool{"/srv/www": true}}
	handler, err := New(h.deps)
	if err != nil {
		t.Fatal(err)
	}
	h.handler = handler
	return h, fake
}

func TestClamavStatus(t *testing.T) {
	h, _ := newClamavHarness(t)
	code, out := h.call(http.MethodGet, "/api/clamav", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/clamav = %d %v", code, out)
	}
	if out["installed"] != true || out["version"] != "1.4.3" || out["signature_version"] != "27800" {
		t.Errorf("status = %v", out)
	}
	if pk, _ := out["packages"].([]any); len(pk) != 3 {
		t.Errorf("packages = %v", out["packages"])
	}
}

func TestClamavInstallAndUninstallStartJobs(t *testing.T) {
	h, fake := newClamavHarness(t)
	for path, kind := range map[string]string{"/api/clamav/install": "clamav.install", "/api/clamav/uninstall": "clamav.uninstall"} {
		code, out := h.call(http.MethodPost, path, map[string]any{})
		if code != http.StatusAccepted {
			t.Fatalf("%s = %d %v", path, code, out)
		}
		job, ok := out["job"].(map[string]any)
		if !ok || job["kind"] != kind {
			t.Fatalf("%s job = %v", path, out["job"])
		}
		id, _ := job["id"].(string)
		if done, err := h.deps.Jobs.Wait(context.Background(), id); err != nil || done.State != jobs.StateSucceeded {
			t.Fatalf("%s job = %+v, %v", path, done, err)
		}
	}
	if fake.install != 1 || fake.uninst != 1 {
		t.Errorf("install=%d uninstall=%d", fake.install, fake.uninst)
	}
}

func TestClamavScan(t *testing.T) {
	t.Run("starts a job on the cleaned path", func(t *testing.T) {
		h, fake := newClamavHarness(t)
		code, out := h.call(http.MethodPost, "/api/clamav/scan", map[string]string{"path": "/srv/www"})
		if code != http.StatusAccepted {
			t.Fatalf("scan = %d %v", code, out)
		}
		job := out["job"].(map[string]any)
		if job["kind"] != "clamav.scan" {
			t.Errorf("job kind = %v", job["kind"])
		}
		if _, err := h.deps.Jobs.Wait(context.Background(), job["id"].(string)); err != nil {
			t.Fatal(err)
		}
		if len(fake.scanned) != 1 || fake.scanned[0] != "/srv/www" {
			t.Errorf("scanned = %v", fake.scanned)
		}
	})
	t.Run("infected files fail the job", func(t *testing.T) {
		h, fake := newClamavHarness(t)
		fake.scanErr = clamav.ErrInfected
		_, out := h.call(http.MethodPost, "/api/clamav/scan", map[string]string{"path": "/srv/www"})
		id := out["job"].(map[string]any)["id"].(string)
		done, err := h.deps.Jobs.Wait(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if done.State != jobs.StateFailed || !strings.Contains(done.Err, "infected") {
			t.Errorf("job = %s %q, want failed with the infection", done.State, done.Err)
		}
	})
	t.Run("a bad path is refused before any job", func(t *testing.T) {
		h, fake := newClamavHarness(t)
		code, _ := h.call(http.MethodPost, "/api/clamav/scan", map[string]string{"path": "/srv/../etc"})
		if code != http.StatusBadRequest {
			t.Errorf("traversal = %d, want 400", code)
		}
		code, _ = h.call(http.MethodPost, "/api/clamav/scan", map[string]string{"path": "/nope"})
		if code != http.StatusNotFound {
			t.Errorf("missing path = %d, want 404", code)
		}
		if len(fake.scanned) != 0 {
			t.Errorf("scanned = %v, want none", fake.scanned)
		}
	})
	t.Run("not installed is a conflict", func(t *testing.T) {
		h, fake := newClamavHarness(t)
		fake.avail.Installed = false
		code, _ := h.call(http.MethodPost, "/api/clamav/scan", map[string]string{"path": "/srv/www"})
		if code != http.StatusConflict {
			t.Errorf("scan without ClamAV = %d, want 409", code)
		}
	})
	t.Run("a scan already running is a conflict", func(t *testing.T) {
		h, fake := newClamavHarness(t)
		fake.avail.Scanning = true
		code, _ := h.call(http.MethodPost, "/api/clamav/scan", map[string]string{"path": "/srv/www"})
		if code != http.StatusConflict {
			t.Errorf("overlapping scan = %d, want 409", code)
		}
	})
}
