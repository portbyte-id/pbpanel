//go:build integration && linux

package waf

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/nginx"
)

// TestOpenAppsecConfigAgainstRealNginx is the one thing step 4 can prove without
// an agent: that the rendered configuration loads the attachment module and
// passes `nginx -t` with the module present and the agent absent.
//
// The module fails open (plan §0.2), so a missing agent is not an error to
// nginx — but a *site directive without the module* is (plan §0.5), which is why
// the module line and the per-server line are always written together. This test
// puts them together in one file, as the panel does, and separately proves the
// failure the pairing avoids.
func TestOpenAppsecConfigAgainstRealNginx(t *testing.T) {
	bin := os.Getenv("PB_NGINX")
	explicit := bin != ""
	if !explicit {
		bin = "/www/panel/nginx/current/sbin/nginx"
	}
	// An explicit PB_NGINX that cannot be used is a failure, not a skip: a runner
	// that asked for this test must not be told it passed when it did not run.
	require := func(err error, format string, args ...any) {
		if explicit {
			t.Fatalf(format, args...)
		}
		t.Skipf(format, args...)
	}
	if _, err := os.Stat(bin); err != nil {
		require(err, "no nginx build at %s: %v (set PB_NGINX to one with the open-appsec module)", bin, err)
	}
	prefix, err := exec.Command(bin, "-V").CombinedOutput()
	if err != nil {
		require(err, "%s -V: %v\n%s", bin, err, prefix)
	}
	// The module lives beside the binary it was built for.
	prefixDir := filepath.Dir(filepath.Dir(bin))
	mod := filepath.Join(prefixDir, OpenAppsecModuleFile)
	if _, err := os.Stat(mod); err != nil {
		require(err, "this build has no %s; it predates the attachment", OpenAppsecModuleFile)
	}

	root := t.TempDir()
	_ = os.Chmod(root, 0o755)
	p := nginx.Paths{Root: root}
	e := New(nil, p, nil)

	c, err := openAppsecConfig().Normalize()
	if err != nil {
		t.Fatal(err)
	}
	r := e.RenderWith("a.test", c, Global{})
	if !strings.Contains(string(r.Block), OpenAppsecDirective) {
		t.Fatalf("the rendered block does not inspect with open-appsec:\n%s", r.Block)
	}
	// The module loader is a main-context file, as the panel writes it; the rest
	// of the site's files go where the panel puts them.
	files := append([]confedit.File{{Path: e.OpenAppsecModulePath(), Content: OpenAppsecMainInclude()}}, r.Files...)
	for _, f := range files {
		if f.Delete {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.Path, f.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(p.WAFGlobalHTTP()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.WAFGlobalHTTP(), GlobalHTTPInclude(Global{}), 0o644); err != nil {
		t.Fatal(err)
	}

	base := fmt.Sprintf(`worker_processes 1;
error_log /dev/stderr notice;
pid %[1]s/nginx.pid;
events {}
http {
    include %[2]s;
    access_log off;
    server {
        listen 18099;
        server_name a.test;
        location / { return 200 "ok\n"; }
%[3]s
    }
}
`, root, p.WAFHTTPFile("a.test"), indent(string(r.Block)))
	conf := filepath.Join(root, "nginx.conf")
	if err := os.WriteFile(conf, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. With the module loaded, `nginx -t` accepts the whole configuration even
	//    though no agent is running: the module fails open.
	//
	//    The loader names the module relative to the build's compiled-in prefix,
	//    which is how the panel writes it and why a candidate loads its own
	//    module. This build was extracted somewhere else, so the test rewrites
	//    that one token to the absolute path of the module it must load.
	withModule := filepath.Join(root, "with-module.conf")
	loader := strings.Replace(string(OpenAppsecMainInclude()), OpenAppsecModuleFile, mod, 1)
	confText := loader + base
	if err := os.WriteFile(withModule, []byte(confText), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "-t", "-c", withModule).CombinedOutput(); err != nil {
		t.Fatalf("nginx -t rejected the rendered open-appsec configuration: %v\n%s\n%s", err, out, confText)
	}

	// 2. Without the module, the same site file is rejected — the trap the
	//    pairing exists to avoid, pinned here so a future change that writes the
	//    directive without the loader fails this test rather than a host.
	if err := os.WriteFile(conf, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "-t", "-c", conf).CombinedOutput(); err == nil {
		t.Errorf("nginx -t accepted %q with no module loaded:\n%s", OpenAppsecDirective, out)
	} else if !strings.Contains(string(out), "unknown directive") {
		t.Errorf("the failure is not the unknown directive: %v\n%s", err, out)
	}
}
