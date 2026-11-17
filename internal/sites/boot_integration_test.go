//go:build integration && linux

package sites

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
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

// chownless is the real filesystem for a test that does not run as root: the
// www account does not exist on a developer machine, and the files are world
// readable, which is all the container's workers need here.
type chownless struct{ OSFS }

func (chownless) Chown(string, int, int) error { return nil }

// get fetches the site through nginx's host-network port 80, by Host header.
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

// TestRealNginxEndToEnd runs the whole of steps 1.5 to 1.9 against a real
// Docker Engine: rendered config passes `nginx -t` and serves a site, a bad raw
// edit is rolled back with nginx still serving, and a container deleted by hand
// comes back on the next reconcile. It needs port 80 free on the host.
func TestRealNginxEndToEnd(t *testing.T) {
	eng, err := docker.New(docker.DefaultSocket)
	if err != nil {
		t.Fatal(err)
	}
	bg := context.Background()
	if _, err := eng.Ping(bg); err != nil {
		t.Skipf("no Docker Engine: %v", err)
	}
	_ = eng.ContainerRemove(bg, nginx.ContainerName, true)
	t.Cleanup(func() { _ = eng.ContainerRemove(bg, nginx.ContainerName, true) })

	root, err := os.MkdirTemp("", "panel-sites-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for _, d := range []string{"", "panel", "panel/nginx", "panel/nginx/sites", "panel/nginx/includes", "panel/logs", "panel/logs/sites", "panel/history", "services", "services/sites"} {
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
	ed, err := confedit.New(confedit.Options{Roots: []string{paths.Dir()}, HistoryDir: filepath.Join(root, "panel", "history"), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := nginx.NewImages(eng, paths, images.NewCatalog(st), nil)
	if err != nil {
		t.Fatal(err)
	}
	live := nginx.NewService(eng, paths, imgs.ImageFunc())
	mgr, err := New(Options{Store: st, Applier: ed, Service: live, Paths: paths, FS: chownless{}})
	if err != nil {
		t.Fatal(err)
	}
	global := nginx.NewGlobal(st, ed, live, paths)
	rec := &Reconciler{
		Sites: mgr, Global: global, Recover: ed.Recover,
		Boot: nginx.NewValidateOnly(eng, paths, imgs.ImageFunc()), Live: live, Runtime: nginx.NewRuntime(eng, paths, imgs.ImageFunc()),
		BuildImage: func(ctx context.Context) error {
			_, err := imgs.Build(ctx, func(l string) { t.Log(l) })
			return err
		},
	}

	// A site is created before nginx has ever run: the files it renders must pass
	// `nginx -t` in a one-shot container, so the create has to wait for boot.
	// Boot first, so the apply below has a running nginx to reload.
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
	if code, _ := get(t, "unknown.test"); code == 200 {
		t.Error("a host no site claims was answered; the default server must drop it")
	}

	// 1.8: a bad raw edit is rolled back and nginx keeps serving.
	good, err := global.ReadConf(bg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = global.WriteConf(bg, append([]byte("bogus_directive on;\n"), good...))
	var ae *confedit.ApplyError
	if !errors.As(err, &ae) || ae.Stage != confedit.StageValidate || !strings.Contains(ae.Output, "bogus_directive") {
		t.Fatalf("bad edit: %v (want a validate-stage rejection quoting nginx)", err)
	}
	after, _ := global.ReadConf(bg)
	if string(after) != string(good) {
		t.Error("nginx.conf was not restored after the rejected edit")
	}
	if code, _ := get(t, "a.test"); code != 200 {
		t.Errorf("nginx stopped serving after a rejected edit: %d", code)
	}

	// 1.8: a good settings change applies and is reflected on disk.
	s := nginx.DefaultHTTPSettings()
	s.ClientMaxBodySize = "7m"
	if _, err := global.SetSettings(bg, s); err != nil {
		t.Fatalf("settings: %v", err)
	}
	if b, _ := os.ReadFile(paths.HTTPIncludePath()); !strings.Contains(string(b), "client_max_body_size 7m;") {
		t.Errorf("panel-http.conf = %s", b)
	}

	// 1.9: delete the container by hand; the next boot restores it.
	if err := eng.ContainerRemove(bg, nginx.ContainerName, true); err != nil {
		t.Fatal(err)
	}
	if code, _ := get(t, "a.test"); code == 200 {
		t.Fatal("the site still answers after its container was removed")
	}
	st2 := rec.Run(bg)
	if !st2.OK {
		t.Fatalf("second reconcile: %+v", st2)
	}
	waitFor(t, "the site to come back", func() bool {
		code, _ := get(t, "a.test")
		return code == 200
	})

	// A third run with everything current writes nothing and reloads once.
	if st3 := rec.Run(bg); !st3.OK {
		t.Fatalf("third reconcile: %+v", st3)
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
