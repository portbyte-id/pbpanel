//go:build integration && linux

package sites

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/nginx"
)

// TestRealNginxEndToEnd runs the whole of steps 1.5 to 1.9 against the native
// nginx: a rendered config passes `nginx -t` and serves a site, a bad raw edit is
// rolled back with nginx still serving, a crash is restarted by systemd, and a
// stop by hand is undone by the next reconcile. It needs port 80 free on the host.
func TestRealNginxEndToEnd(t *testing.T) {
	eng := requireNativeHost(t)
	bg := context.Background()
	h := newNativeHost(t, eng, "panel-sites-it-", nativeLayout{})
	mgr := h.manager(t, nil)
	rec := h.reconciler(t, mgr)

	// A site is created before nginx has ever run: the files it renders must pass
	// `nginx -t`, so the create has to wait for boot. Boot first, so the apply
	// below has a running nginx to reload.
	if st := rec.Run(bg); !st.OK {
		t.Fatalf("first reconcile: %+v", st)
	}
	if _, err := mgr.Create(bg, CreateInput{Domain: "a.test", Aliases: []string{"www.a.test"}, Kind: KindStatic}); err != nil {
		t.Fatalf("create: %v", err)
	}
	waitFor(t, "the site to be served", func() bool {
		code, body := get(t, "a.test")
		return code == 200 && strings.Contains(body, "a.test")
	})
	if code, _ := get(t, "www.a.test"); code != 200 {
		t.Errorf("alias answered %d", code)
	}
	// A host no site claims gets the landing page of the default server, not the first site.
	if code, body := get(t, "unknown.test"); code != 200 || !strings.Contains(body, "No website configured") {
		t.Errorf("unclaimed host: HTTP %d, want the landing page", code)
	}

	// 1.8: a bad raw edit is rolled back and nginx keeps serving.
	good, err := h.global.ReadConf(bg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.global.WriteConf(bg, append([]byte("bogus_directive on;\n"), good...))
	var ae *confedit.ApplyError
	if !errors.As(err, &ae) || ae.Stage != confedit.StageValidate || !strings.Contains(ae.Output, "bogus_directive") {
		t.Fatalf("bad edit: %v (want a validate-stage rejection quoting nginx)", err)
	}
	after, _ := h.global.ReadConf(bg)
	if string(after) != string(good) {
		t.Error("nginx.conf was not restored after the rejected edit")
	}
	if code, _ := get(t, "a.test"); code != 200 {
		t.Errorf("nginx stopped serving after a rejected edit: %d", code)
	}

	// 1.8: a good settings change applies and is reflected on disk.
	s := nginx.DefaultHTTPSettings()
	s.ClientMaxBodySize = "7m"
	if _, err := h.global.SetSettings(bg, s); err != nil {
		t.Fatalf("settings: %v", err)
	}
	if b, _ := os.ReadFile(h.paths.HTTPIncludePath()); !strings.Contains(string(b), "client_max_body_size 7m;") {
		t.Errorf("panel-http.conf = %s", b)
	}

	// 1.9a: nginx crashes. systemd restarts it with no panel involved.
	oldPID := nginxMainPID(t)
	nginxCrash(t)
	waitFor(t, "systemd to restart nginx", func() bool { return nginxActive() && nginxMainPID(t) != oldPID })
	waitFor(t, "the site to answer after the crash", func() bool {
		code, _ := get(t, "a.test")
		return code == 200
	})

	// 1.9b: nginx is stopped by hand. The site is down until the next reconcile.
	nginxStop(t)
	if code, _ := get(t, "a.test"); code == 200 {
		t.Fatal("the site still answers after nginx was stopped")
	}
	if st2 := rec.Run(bg); !st2.OK {
		t.Fatalf("second reconcile: %+v", st2)
	}
	waitFor(t, "the site to come back", func() bool {
		code, _ := get(t, "a.test")
		return code == 200
	})

	// A third run with everything current writes nothing and does not restart nginx.
	pid := nginxMainPID(t)
	if st3 := rec.Run(bg); !st3.OK {
		t.Fatalf("third reconcile: %+v", st3)
	}
	if nginxMainPID(t) != pid {
		t.Error("a no-op reconcile restarted nginx")
	}
	if code, _ := get(t, "a.test"); code != 200 {
		t.Errorf("site down after a no-op reconcile: %d", code)
	}

	// Delete the site: the config goes, the web root stays.
	res, err := mgr.Delete(bg, "a.test")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(res.WebRoot); err != nil {
		t.Errorf("web root %s: %v", res.WebRoot, err)
	}
}
