//go:build integration && linux

package sites

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/store"
)

var phpInfoVersion = regexp.MustCompile(`PHP Version (\d+\.\d+)`)

// servedVersion is the PHP version phpinfo() reports through nginx, or "".
func servedVersion(t *testing.T, host string) string {
	t.Helper()
	code, body := get(t, host)
	if code != 200 {
		return ""
	}
	if m := phpInfoVersion.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// TestRealPHPEndToEnd is the M2 exit gate against a real Docker Engine: a PHP
// site serves phpinfo() through nginx, a form change shows up in it, a version
// switch changes PHP_VERSION, and an invalid php.ini is rejected with the file
// restored and the site still serving. It pulls two official images and needs
// port 80 free on the host.
func TestRealPHPEndToEnd(t *testing.T) {
	const verA, verB = "8.3", "8.4"

	eng, err := docker.New(docker.DefaultSocket)
	if err != nil {
		t.Fatal(err)
	}
	bg := context.Background()
	if _, err := eng.Ping(bg); err != nil {
		t.Skipf("no Docker Engine: %v", err)
	}
	clean := func() {
		for _, name := range []string{nginx.ContainerName, php.ContainerName(verA), php.ContainerName(verB)} {
			_ = eng.ContainerRemove(bg, name, true)
		}
	}
	clean()
	t.Cleanup(clean)

	root, err := os.MkdirTemp("", "panel-php-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for _, d := range []string{"", "panel", "panel/nginx", "panel/nginx/sites", "panel/nginx/includes", "panel/php", "panel/run", "panel/logs", "panel/logs/sites", "panel/history", "services", "services/sites"} {
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
	phpPaths := php.Paths{Root: root}
	ed, err := confedit.New(confedit.Options{
		Roots: []string{paths.Dir(), phpPaths.Base()}, HistoryDir: filepath.Join(root, "panel", "history"), Store: st,
	})
	if err != nil {
		t.Fatal(err)
	}
	phpMgr, err := php.New(php.Options{
		Store: st, Engine: eng, Applier: ed, Paths: phpPaths,
		Supported: func(context.Context) ([]string, error) { return []string{verA, verB}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	live := nginx.NewService(eng, paths)
	mgr, err := New(Options{Store: st, Applier: ed, Service: live, Paths: paths, FS: chownless{}, PHP: phpMgr})
	if err != nil {
		t.Fatal(err)
	}
	rec := &Reconciler{
		Sites: mgr, Global: nginx.NewGlobal(st, ed, live, paths), Recover: ed.Recover,
		Boot: nginx.NewValidateOnly(eng, paths), Live: live, Runtime: nginx.NewRuntime(eng, paths),
		BuildImage: func(ctx context.Context) error {
			return nginx.BuildImage(ctx, eng, func(l string) { t.Log(l) })
		},
	}
	if st := rec.Run(bg); !st.OK {
		t.Fatalf("nginx reconcile: %+v", st)
	}

	// 2.1: a site cannot use a version that was not pulled; a pull makes it ready.
	_, err = mgr.Create(bg, CreateInput{Domain: "a.test", Kind: KindPHP, PHPVersion: verA, PHPInfo: true})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("create on an absent version: %v", err)
	}
	for _, ver := range []string{verA, verB} {
		if err := phpMgr.StartPull(bg, ver); err != nil {
			t.Fatal(err)
		}
		if err := phpMgr.Pull(bg, ver, nil); err != nil {
			t.Fatalf("pull %s: %v", ver, err)
		}
		v, _ := phpMgr.Get(bg, ver)
		if v.State != php.StateReady || v.Digest == "" {
			t.Fatalf("after pull: %+v", v)
		}
	}

	// 2.7: no container until a site uses the version.
	if _, found, _ := phpMgr.ContainerState(bg, verA); found {
		t.Fatal("the PHP container exists before any site uses it")
	}

	// Exit gate: a PHP site on version A serves phpinfo() through nginx.
	if _, err := mgr.Create(bg, CreateInput{Domain: "a.test", Kind: KindPHP, PHPVersion: verA, PHPInfo: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	waitFor(t, "phpinfo() on "+verA, func() bool { return servedVersion(t, "a.test") == verA })

	// 2.2: nothing listens on TCP 9000; the pool is a socket owned by www.
	if c, err := net.DialTimeout("tcp", "127.0.0.1:9000", time.Second); err == nil {
		_ = c.Close()
		t.Error("something listens on TCP 9000")
	}
	if fi, err := os.Stat(phpPaths.Socket(verA, "a_test")); err != nil || fi.Mode().Perm() != 0o660 {
		t.Errorf("pool socket: %v, %v", fi, err)
	}

	// Exit gate: memory_limit changed in the form is what phpinfo() shows.
	s := php.DefaultSettings()
	s.MemoryLimit = "333M"
	if _, err := phpMgr.SetSettings(bg, verA, s); err != nil {
		t.Fatalf("settings: %v", err)
	}
	waitFor(t, "memory_limit 333M in phpinfo()", func() bool {
		_, body := get(t, "a.test")
		return strings.Contains(body, "333M")
	})

	// 2.4: a pool change is accepted by php-fpm -t and the site keeps serving.
	if _, _, err := mgr.SetPool(bg, "a.test", php.PoolSettings{PM: "ondemand", MaxChildren: 3, MaxRequests: 100, IdleTimeout: 5}); err != nil {
		t.Fatalf("pool: %v", err)
	}
	waitFor(t, "the site after a pool change", func() bool { return servedVersion(t, "a.test") == verA })

	// Exit gate: an invalid raw ini is rejected, the file restored, the site up.
	good, err := phpMgr.ReadIni(bg, verA)
	if err != nil {
		t.Fatal(err)
	}
	_, err = phpMgr.WriteIni(bg, verA, append([]byte("memory_limit = = =\n"), good...))
	var ae *confedit.ApplyError
	if !errors.As(err, &ae) || ae.Stage != confedit.StageValidate || !strings.Contains(ae.Output, "syntax error") {
		t.Fatalf("bad ini: %v (want a validate-stage rejection quoting PHP)", err)
	}
	_, err = phpMgr.WriteIni(bg, verA, append([]byte("extension=no_such_extension.so\n"), good...))
	if !errors.As(err, &ae) || ae.Stage != confedit.StageValidate {
		t.Fatalf("unloadable extension: %v (want a validate-stage rejection)", err)
	}
	if after, _ := phpMgr.ReadIni(bg, verA); string(after) != string(good) {
		t.Error("php.ini was not restored after the rejected edits")
	}
	if servedVersion(t, "a.test") != verA {
		t.Error("the site stopped serving after a rejected php.ini")
	}
	// A valid raw edit goes live.
	if _, err := phpMgr.WriteIni(bg, verA, append([]byte("expose_php = Off\n"), good...)); err != nil {
		t.Fatalf("good ini: %v", err)
	}

	// Exit gate: switching to version B changes PHP_VERSION.
	if _, err := mgr.SwitchPHP(bg, "a.test", verB); err != nil {
		t.Fatalf("switch: %v", err)
	}
	waitFor(t, "phpinfo() on "+verB, func() bool { return servedVersion(t, "a.test") == verB })
	if _, err := os.Stat(phpPaths.PoolFile(verA, "a_test")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the old pool file is still there: %v", err)
	}
	// 2.7: the old version lost its last site and keeps running.
	if c, found, _ := phpMgr.ContainerState(bg, verA); !found || !c.State.Running {
		t.Error("the old version's container was stopped when its last site left")
	}

	// 2.7: a container removed by hand comes back on the boot reconcile.
	if err := eng.ContainerRemove(bg, php.ContainerName(verB), true); err != nil {
		t.Fatal(err)
	}
	if servedVersion(t, "a.test") != "" {
		t.Fatal("the site still answers after its PHP container was removed")
	}
	if err := phpMgr.Reconcile(bg); err != nil {
		t.Fatal(err)
	}
	for _, step := range mgr.ReconcilePHP(bg) {
		if !step.OK {
			t.Fatalf("php reconcile: %+v", step)
		}
	}
	waitFor(t, "the site to come back", func() bool { return servedVersion(t, "a.test") == verB })

	// Deleting the site leaves the container up and frees nginx of it.
	if _, err := mgr.Delete(bg, "a.test"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if c, found, _ := phpMgr.ContainerState(bg, verB); !found || !c.State.Running {
		t.Error("the container was stopped when its last site was deleted")
	}
	if code, _ := get(t, "a.test"); code == 200 {
		t.Error("a deleted site still answers")
	}
}
