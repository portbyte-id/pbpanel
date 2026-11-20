//go:build integration && linux

package sites

import (
	"context"
	"errors"
	"net"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/php"
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

// requirePHPHost skips the test unless this host can install the native PHP
// units the test owns: root, systemd, and neither version's unit already
// installed (that unit would be the host's own, and the test stops and starts
// it).
func requirePHPHost(t *testing.T, versions ...string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("native PHP installs systemd units and nginx binds port 80: it needs root")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("systemd is not running on this host")
	}
	for _, ver := range versions {
		unit := php.DefaultUnitDir + "/" + php.UnitName(ver)
		if _, err := os.Stat(unit); err == nil {
			t.Skipf("%s is already installed; the test owns that unit, so remove it first", unit)
		}
	}
}

// unitStopped stops the version's unit if anything started it and removes its
// unit file, so the next test starts with no PHP at all.
func unitStopped(t *testing.T, ver string) {
	t.Helper()
	run := php.ExecRunner{}
	ctx := context.Background()
	_, _ = run.Run(ctx, "systemctl", "disable", "--now", php.UnitName(ver))
	_ = os.Remove(php.DefaultUnitDir + "/" + php.UnitName(ver))
	_, _ = run.Run(ctx, "systemctl", "daemon-reload")
	_, _ = run.Run(ctx, "systemctl", "reset-failed", php.UnitName(ver))
}

// unitActive reports whether systemd has the version's unit running.
func unitActive(ver string) bool {
	out, _ := (php.ExecRunner{}).Run(context.Background(), "systemctl", "is-active", php.UnitName(ver))
	return strings.TrimSpace(string(out)) == "active"
}

// TestRealPHPEndToEnd is the M2 exit gate against the native runtime: a PHP site
// serves phpinfo() through nginx, a form change shows up in it, a version switch
// changes PHP_VERSION, and an invalid php.ini is rejected with the file restored
// and the site still serving. It installs both versions' units itself and needs
// root, systemd, the www account, a Docker Engine for the builder and port 80
// free; a build compiles PHP from source, minutes per version.
func TestRealPHPEndToEnd(t *testing.T) {
	const verA, verB = "8.3", "8.4"
	requirePHPHost(t, verA, verB)

	eng := requireNativeHost(t)
	bg := context.Background()
	clean := func() {
		for _, ver := range []string{verA, verB} {
			unitStopped(t, ver)
		}
	}
	clean()
	t.Cleanup(clean)

	h := newNativeHost(t, eng, "panel-php-it-", nativeLayout{
		dirs:  []string{"panel/php", "panel/run"},
		roots: []string{"panel/php"},
	})
	phpPaths := php.Paths{Root: h.root}
	phpMgr, err := php.New(php.Options{
		Store: h.st, Engine: eng, Applier: h.ed, Paths: phpPaths,
		Supported: func(context.Context) ([]string, error) { return []string{verA, verB}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr := h.manager(t, func(o *Options) { o.PHP = phpMgr })
	rec := h.reconciler(t, mgr)
	if st := rec.Run(bg); !st.OK {
		t.Fatalf("nginx reconcile: %+v", st)
	}

	// 2.1: a site cannot use a version that was not installed; an install makes it ready.
	_, err = mgr.Create(bg, CreateInput{Domain: "a.test", Kind: KindPHP, PHPVersion: verA, PHPInfo: true})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("create on an absent version: %v", err)
	}
	for _, ver := range []string{verA, verB} {
		if err := phpMgr.StartPull(bg, ver); err != nil {
			t.Fatal(err)
		}
		if err := phpMgr.Pull(bg, ver, nil, nil); err != nil {
			t.Fatalf("install %s: %v", ver, err)
		}
		v, _ := phpMgr.Get(bg, ver)
		if v.State != php.StateReady || v.Digest == "" {
			t.Fatalf("after the install: %+v", v)
		}
	}

	// 2.7: no unit runs until a site uses the version.
	if st, found, _ := phpMgr.Status(bg, verA); found && st.Active {
		t.Fatal("the PHP unit runs before any site uses it")
	}

	// Exit gate: a PHP site on version A serves phpinfo() through nginx.
	if _, err := mgr.Create(bg, CreateInput{Domain: "a.test", Kind: KindPHP, PHPVersion: verA, PHPInfo: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	waitFor(t, "phpinfo() on "+verA, func() bool { return servedVersion(t, "a.test") == verA })
	if !unitActive(verA) {
		t.Fatal("the site serves but panel-php-" + verA + ".service is not active")
	}

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
	if !unitActive(verA) {
		t.Error("the old version's unit was stopped when its last site left")
	}

	// 2.7: a unit stopped on the host comes back on the boot reconcile. It is
	// systemctl that stops it, not the panel, so the flag that keeps a version
	// down is not set and the reconcile restores it.
	if out, err := (php.ExecRunner{}).Run(bg, "systemctl", "stop", php.UnitName(verB)); err != nil {
		t.Fatalf("systemctl stop: %v: %s", err, out)
	}
	if servedVersion(t, "a.test") != "" {
		t.Fatal("the site still answers after its PHP unit was stopped")
	}
	for _, step := range mgr.ReconcilePHP(bg) {
		if !step.OK {
			t.Fatalf("php reconcile: %+v", step)
		}
	}
	waitFor(t, "the site to come back", func() bool { return servedVersion(t, "a.test") == verB })

	// Deleting the site leaves the unit up and frees nginx of it.
	if _, err := mgr.Delete(bg, "a.test"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !unitActive(verB) {
		t.Error("the unit was stopped when its last site was deleted")
	}
	if code, _ := get(t, "a.test"); code == 200 {
		t.Error("a deleted site still answers")
	}
}
