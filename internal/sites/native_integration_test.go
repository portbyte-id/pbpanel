//go:build integration && linux

package sites

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/store"
)

// These tests run the production nginx, not a container: a build compiled by the
// builder image, a systemd unit (panel-nginx.service) and the reconciler. The
// host must be root, run systemd as PID 1, have the www account the workers run
// as, and have a Docker Engine for the builder. Each test installs the unit and
// removes it again, so nothing is left on a host that ran them.

// chownless is the real filesystem for a test that does not run as root. The www
// account is not a file owner here, and the files are world readable, which is
// all the workers need.
type chownless struct{ OSFS }

func (chownless) Chown(string, int, int) error { return nil }

// requireNativeHost skips the test unless this host can run the native nginx,
// and returns the Docker client the builder uses. It skips, rather than remove
// it, when the unit is already installed: that unit is the host's own nginx.
func requireNativeHost(t *testing.T) *docker.Client {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("native nginx needs root: it installs a systemd unit and binds ports 80 and 443")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("systemd is not running on this host")
	}
	if _, err := user.Lookup("www"); err != nil {
		t.Skipf("the nginx workers run as www, which this host does not have: %v", err)
	}
	unit := filepath.Join(nginx.DefaultUnitDir, nginx.UnitName)
	if _, err := os.Stat(unit); err == nil {
		t.Skipf("%s is already installed; the tests own that unit, so remove it first", unit)
	}
	eng, err := docker.New(docker.DefaultSocket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Ping(context.Background()); err != nil {
		t.Skipf("no Docker Engine, and the nginx builder needs one: %v", err)
	}
	return eng
}

// nativeHost is one panel on this machine, wired the way cmd/panel wires it: a
// fresh root, a store, the config editor, the build family, the systemd service
// and the runtime.
type nativeHost struct {
	root    string
	paths   nginx.Paths
	st      *store.Store
	ed      *confedit.Editor
	cat     *images.Catalog
	imgs    *nginx.Images
	live    *nginx.Service
	runtime *nginx.Runtime
	global  *nginx.Global
}

// nativeLayout is what one test's root needs beyond the base directories.
type nativeLayout struct {
	// dirs are created under the root.
	dirs []string
	// roots are the config trees the editor may write, under the root, in
	// addition to the nginx tree.
	roots []string
}

// newNativeHost makes a root and the production wiring over it. The builder
// compiles nginx with the root as its prefix, so every test builds its own nginx;
// Docker's layer cache keeps the builds after the first one short.
func newNativeHost(t *testing.T, eng *docker.Client, prefix string, lay nativeLayout) *nativeHost {
	t.Helper()
	root, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	// Cleanups run last in first-out order: the unit goes before the root does.
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Cleanup(func() { stopNative(t) })

	// The base layout is what install creates before nginx first runs
	// (internal/install/layout.go), cache/nginx included.
	base := []string{"", "panel", "panel/nginx", "panel/nginx/sites", "panel/nginx/includes", "panel/cache", "panel/cache/nginx", "panel/logs", "panel/logs/sites", "panel/history", "services", "services/sites"}
	for _, d := range append(base, lay.dirs...) {
		p := filepath.Join(root, d)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	st, err := store.Open(filepath.Join(root, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	paths := nginx.Paths{Root: root}
	roots := []string{paths.Dir()}
	for _, r := range lay.roots {
		roots = append(roots, filepath.Join(root, r))
	}
	ed, err := confedit.New(confedit.Options{Roots: roots, HistoryDir: filepath.Join(root, "panel", "history"), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	cat := images.NewCatalog(st)
	run := nginx.ExecRunner{}
	imgs, err := nginx.NewImages(paths, run, nginx.DockerBuilder{Eng: eng, P: paths}, cat, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	live := nginx.NewService(paths, run)
	return &nativeHost{
		root: root, paths: paths, st: st, ed: ed, cat: cat, imgs: imgs, live: live,
		runtime: nginx.NewRuntime(paths, run).WithFlags(cat),
		global:  nginx.NewGlobal(st, ed, live, paths),
	}
}

// manager builds the site manager over h. opts may add the TLS, WAF, PHP or
// settings parts a test needs.
func (h *nativeHost) manager(t *testing.T, opts func(*Options)) *Manager {
	t.Helper()
	o := Options{Store: h.st, Applier: h.ed, Service: h.live, Paths: h.paths, FS: chownless{}}
	if opts != nil {
		opts(&o)
	}
	mgr, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

// reconciler is the boot reconcile over h. The first run builds nginx if no
// build serves; the unit then starts it on the build the current link names.
func (h *nativeHost) reconciler(t *testing.T, mgr *Manager) *Reconciler {
	return &Reconciler{
		Sites: mgr, Global: h.global, Recover: h.ed.Recover,
		Boot: nginx.NewValidateOnly(h.paths, nginx.ExecRunner{}), Live: h.live, Runtime: h.runtime,
		BuildImage: func(ctx context.Context) error {
			_, err := h.imgs.Build(ctx, func(l string) { t.Log(l) })
			return err
		},
	}
}

// stopNative stops and removes the panel-nginx unit a test installed, so the
// next test starts with no nginx at all.
func stopNative(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	run := nginx.ExecRunner{}
	_, _ = run.Run(ctx, "systemctl", "disable", "--now", nginx.UnitName)
	_ = os.Remove(filepath.Join(nginx.DefaultUnitDir, nginx.UnitName))
	_, _ = run.Run(ctx, "systemctl", "daemon-reload")
	_, _ = run.Run(ctx, "systemctl", "reset-failed", nginx.UnitName)
}

// nginxActive reports whether systemd has the nginx unit active.
func nginxActive() bool {
	out, _ := nginx.ExecRunner{}.Run(context.Background(), "systemctl", "is-active", nginx.UnitName)
	return strings.TrimSpace(string(out)) == "active"
}

// nginxMainPID is the master's process id as systemd reports it; 0 when it is not
// running. A changed PID is how a test sees that nginx was restarted.
func nginxMainPID(t *testing.T) string {
	t.Helper()
	out, _ := nginx.ExecRunner{}.Run(context.Background(), "systemctl", "show", "-p", "MainPID", "--value", nginx.UnitName)
	return strings.TrimSpace(string(out))
}

// nginxStop stops the unit the way an operator does: systemctl stop, no panel call.
func nginxStop(t *testing.T) {
	t.Helper()
	if out, err := (nginx.ExecRunner{}).Run(context.Background(), "systemctl", "stop", nginx.UnitName); err != nil {
		t.Fatalf("systemctl stop: %v: %s", err, out)
	}
}

// nginxCrash kills every process of the unit, as a crash would. The unit's
// Restart=on-failure then brings it back without the panel.
func nginxCrash(t *testing.T) {
	t.Helper()
	if out, err := (nginx.ExecRunner{}).Run(context.Background(), "systemctl", "kill", "-s", "KILL", nginx.UnitName); err != nil {
		t.Fatalf("systemctl kill: %v: %s", err, out)
	}
}

// get fetches the site through nginx's port 80, by Host header.
func get(t *testing.T, host string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	req.Host = host
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// waitFor polls ok for up to 10 seconds, the time nginx needs to restart.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for range 40 {
		if ok() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
