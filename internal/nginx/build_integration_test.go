//go:build integration && linux

package nginx

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
)

// TestRealBuildShipsBothModules compiles a build with the recipe and checks the two
// dynamic modules it ships the way a host uses them: each is in the build under the
// name a configuration loads, every library either one names resolves inside the
// build, and a configuration that loads them passes `nginx -t`.
//
// The open-appsec half is the part that is new and the part with a failure mode
// worth a test: the attachment fails open when the agent is absent
// (docs/design/open-appsec-plan.md §6), so a host that never installs the agent --
// every host today -- must still be able to run a build that ships it.
//
// It compiles nginx, OpenSSL, ModSecurity and the attachment from source, so it
// needs Docker, network access for all of those, and the www account the build
// configures nginx with. It is far longer than any default test timeout: run it
// with -timeout 45m.
func TestRealBuildShipsBothModules(t *testing.T) {
	if _, err := user.Lookup(WWWUser); err != nil {
		t.Skipf("the build configures nginx for %s, which this host does not have: %v", WWWUser, err)
	}
	eng, err := docker.New(docker.DefaultSocket)
	if err != nil {
		t.Skipf("no Docker Engine, and the build needs one: %v", err)
	}
	ctx := context.Background()
	if _, err := eng.Ping(ctx); err != nil {
		t.Skipf("no Docker Engine, and the build needs one: %v", err)
	}

	root := t.TempDir()
	p := Paths{Root: filepath.ToSlash(root)}
	// What the host creates before nginx first runs. The build's own directories
	// are made by the extraction; these are the ones the test configuration names.
	for _, d := range []string{p.VersionsDir(), p.MainLogDir(), p.RunDir(), p.CacheDir()} {
		if err := os.MkdirAll(filepath.FromSlash(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	id, err := images.NewID(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b := DockerBuilder{Eng: eng, P: p}
	if err := b.Build(ctx, id, Version, func(l string) { t.Log(l) }); err != nil {
		t.Fatalf("build %s = %v", id, err)
	}

	// The build's own self-check already refuses a build without either module;
	// this is the same assertion on the build as it landed on this host.
	build := filepath.FromSlash(p.BuildDir(id))
	for _, rel := range []string{
		filepath.Join("modules", "ngx_http_modsecurity_module.so"),
		AttachmentModuleFile,
	} {
		if _, err := os.Stat(filepath.Join(build, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("the build holds no %s: %v", rel, err)
		}
	}

	// The six libraries the vendor packages beside the attachment. The module
	// needs three of them; the other three travel with it because the vendor's own
	// package ships the same six.
	libDir := filepath.Join(build, "lib")
	for _, name := range []string{
		"libshmem_ipc_2.so", "libosrc_shmem_ipc.so", "libosrc_compression_utils.so",
		"libosrc_nginx_attachment_util.so", "libnano_attachment.so", "libnano_attachment_util.so",
	} {
		if _, err := os.Stat(filepath.Join(libDir, name)); err != nil {
			t.Errorf("the build holds no %s: %v", name, err)
		}
	}

	// ldd runs without the build's lib directory on the search path, so every entry
	// is resolved the way a host resolves it: through the bundle's own RUNPATH.
	for _, rel := range []string{
		filepath.Join("modules", "ngx_http_modsecurity_module.so"),
		AttachmentModuleFile,
	} {
		mod := filepath.Join(build, filepath.FromSlash(rel))
		out, err := exec.CommandContext(ctx, "ldd", mod).CombinedOutput()
		if err != nil {
			t.Fatalf("ldd %s: %v\n%s", rel, err, out)
		}
		if strings.Contains(string(out), "not found") {
			t.Errorf("%s does not resolve its libraries:\n%s", rel, out)
		}
	}

	// A configuration that loads both modules and uses the attachment's directive.
	// The directive exists only while the module is loaded, so `nginx -t` accepting
	// it is proof the module really loaded, not merely that the file is there. No
	// agent is installed on any host this runs on, which is the point: the module
	// must load and leave traffic alone.
	conf := "load_module " + filepath.Join("modules", "ngx_http_modsecurity_module.so") + ";\n" +
		"load_module " + AttachmentModuleFile + ";\n" +
		"worker_processes 1;\n" +
		"error_log " + p.MainErrorLog() + ";\n" +
		"pid " + filepath.Join(filepath.FromSlash(p.RunDir()), "nginx.pid") + ";\n" +
		"events {}\n" +
		"http {\n" +
		"    access_log off;\n" +
		"    server {\n" +
		"        listen 18080;\n" +
		"        server_name attachment.test;\n" +
		"        cp-nano-nginx-attachment on;\n" +
		"        location / { return 200 \"ok\\n\"; }\n" +
		"    }\n" +
		"}\n"
	confPath := filepath.Join(root, "nginx.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, filepath.FromSlash(p.BinaryOf(id)), "-t", "-c", confPath).CombinedOutput()
	if err != nil {
		t.Fatalf("nginx -t with both modules loaded = %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "test is successful") {
		t.Errorf("nginx -t said:\n%s", out)
	}
}
