package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/sites"
)

// phpEngine is the Docker client the native builder would use. The harness
// replaces the build hook, so nothing here is reached: a test that gets here
// says so rather than silently passing.
type phpEngine struct{}

func (phpEngine) ImageBuild(context.Context, docker.BuildOptions, docker.Progress) (docker.Image, error) {
	return docker.Image{}, errors.New("the tests provide their own build hook")
}
func (phpEngine) ContainerCreate(context.Context, string, docker.ContainerConfig) (string, error) {
	return "", errors.New("the tests provide their own build hook")
}
func (phpEngine) ContainerArchive(context.Context, string, string) (io.ReadCloser, error) {
	return nil, errors.New("the tests provide their own build hook")
}
func (phpEngine) ContainerRemove(context.Context, string, bool) error {
	return errors.New("the tests provide their own build hook")
}
func (phpEngine) ImageRemove(context.Context, string) error {
	return errors.New("the tests provide their own build hook")
}

// phpHost is the native side of the PHP manager in these tests: a build hook that
// leaves a real prefix on disk, and a runner that answers the two validators and
// systemctl. Nothing here touches Docker.
type phpHost struct {
	p        php.Paths
	unitDir  string
	modules  string
	buildErr error
	active   bool
	calls    []string
}

func (h *phpHost) build(_ context.Context, id, ver string, _ []php.Extension, progress docker.Progress) error {
	h.calls = append(h.calls, "build "+ver+" "+id)
	if progress != nil {
		progress("building php " + ver)
	}
	if h.buildErr != nil {
		return h.buildErr
	}
	prefix := filepath.FromSlash(h.p.BuildPrefix(ver, id))
	for _, rel := range []string{"sbin/php-fpm", "bin/php"} {
		file := filepath.Join(prefix, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file, []byte("#!/bin/sh\n"), 0o755); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(prefix, "etc"), 0o755); err != nil {
		return err
	}
	for name, body := range map[string]string{
		"version":       "8.3.37\n",
		"source-sha256": "3d678a41fd5aade34760127fef5dd64fd7a77848913fc0f70dde0cf467c94427\n",
	} {
		if err := os.WriteFile(filepath.Join(prefix, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(prefix, "etc", "php.ini-production"), []byte("; php.ini-production\n"), 0o644)
}

func (h *phpHost) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return h.RunEnv(ctx, nil, name, args...)
}

func (h *phpHost) RunEnv(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	h.calls = append(h.calls, name+" "+strings.Join(args, " "))
	switch {
	case name == "systemctl" && len(args) > 0 && args[0] == "show":
		unit := args[len(args)-1]
		// systemd knows only the units whose file was written, as on a host.
		if _, err := os.Stat(filepath.Join(h.unitDir, unit)); err != nil {
			return fmt.Appendf(nil, "LoadState=not-found\nActiveState=inactive\nMainPID=0\nActiveEnterTimestampMonotonic=0\n"), nil
		}
		state, pid, since := "inactive", 0, "0"
		if h.active {
			state, pid, since = "active", 7, "100"
		}
		return fmt.Appendf(nil, "LoadState=loaded\nActiveState=%s\nMainPID=%d\nActiveEnterTimestampMonotonic=%s\n", state, pid, since), nil
	case strings.HasSuffix(name, "/sbin/php-fpm"):
		return []byte("NOTICE: configuration file test is successful\n"), nil
	case strings.HasSuffix(name, "/bin/php"):
		return []byte(h.modules), nil
	}
	return nil, nil
}

func newPHPHarness(t *testing.T, host *phpHost) *nginxHarness {
	t.Helper()
	h := newHarness(t)
	// One root for both products: a pool's web root has to be inside the PHP
	// layout's sites tree for the pool file to render, and the build writes a real
	// prefix under it.
	root := filepath.ToSlash(t.TempDir())
	paths := nginx.Paths{Root: root}
	mfs := &memFS{files: map[string][]byte{}, dirs: map[string]bool{}}
	ap := &memApplier{fs: mfs}
	svc := &fakeNginx{status: &nginx.Status{Loaded: true, Active: true, State: "active"}}
	hist := &fakeHistory{versions: map[string][]confedit.Version{}, content: map[string]string{}}

	host.p = php.Paths{Root: root}
	host.unitDir = t.TempDir()
	if host.modules == "" {
		host.modules = "[PHP Modules]\nCore\nintl\n"
	}
	phpMgr, err := php.New(php.Options{
		Store: h.store, Engine: phpEngine{}, Applier: ap, Paths: host.p, Read: mfs.ReadFile,
		Build: host.build, Run: host, UnitDir: host.unitDir,
		Supported: func(context.Context) ([]string, error) { return []string{"8.3", "8.4"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := sites.New(sites.Options{Store: h.store, Applier: ap, Service: svc, Paths: paths, FS: mfs, PHP: phpMgr})
	if err != nil {
		t.Fatal(err)
	}
	h.deps.Nginx = &NginxDeps{
		Sites:   mgr,
		Global:  nginx.NewGlobal(h.store, ap, svc, paths).ReadWith(mfs.ReadFile),
		History: hist, Service: svc, Paths: paths,
		Reconciler: fakeReconcile{sites.Status{Done: true, OK: true}},
		PHP:        phpMgr,
		PHPReconcile: func() []sites.PHPStep {
			return []sites.PHPStep{{Version: "8.3", Sites: 1, OK: true, Detail: "none"}}
		},
	}
	h.handler, err = New(h.deps)
	if err != nil {
		t.Fatal(err)
	}
	return &nginxHarness{harness: h, s: h.login(), fs: mfs, applier: ap, svc: svc, history: hist, php: host.p}
}

// pull starts the pull job over HTTP and waits for it to end.
func (h *nginxHarness) pull(ver string) jobs.Job {
	h.t.Helper()
	code, out := h.call(http.MethodPost, "/api/php/"+ver+"/pull", nil)
	if code != http.StatusAccepted {
		h.t.Fatalf("pull %s: %d %v", ver, code, out)
	}
	job := out["job"].(map[string]any)
	if job["kind"] != "php.pull" {
		h.t.Fatalf("job = %v", job)
	}
	done, err := h.deps.Jobs.Wait(context.Background(), job["id"].(string))
	if err != nil {
		h.t.Fatal(err)
	}
	return done
}

func phpVersions(h *nginxHarness) map[string]map[string]any {
	h.t.Helper()
	code, out := h.call(http.MethodGet, "/api/php", nil)
	if code != http.StatusOK {
		h.t.Fatalf("GET /api/php: %d %v", code, out)
	}
	byVer := map[string]map[string]any{}
	for _, v := range out["versions"].([]any) {
		row := v.(map[string]any)
		byVer[row["version"].(string)] = row
	}
	return byVer
}

func TestPHPRoutesAnswer503WithoutPHP(t *testing.T) {
	h := newNginxHarness(t) // nginx, but no PHP manager
	for _, path := range []string{"/api/php", "/api/php/8.3/settings", "/api/sites/a.test/pool"} {
		code, out := h.call(http.MethodGet, path, nil)
		if code != http.StatusServiceUnavailable || out["error"] != errUnavailable {
			t.Errorf("%s: %d %v", path, code, out)
		}
	}
}

func TestPHPListAndPull(t *testing.T) {
	h := newPHPHarness(t, &phpHost{})
	vs := phpVersions(h)
	if len(vs) != 2 || vs["8.3"]["state"] != "absent" || vs["8.3"]["image"] != "8.3" || vs["8.3"]["supported"] != true {
		t.Fatalf("versions = %v", vs)
	}

	if done := h.pull("8.3"); done.State != jobs.StateSucceeded {
		t.Fatalf("pull job = %+v", done)
	}
	vs = phpVersions(h)
	row := vs["8.3"]
	if row["state"] != "ready" || row["image"] != "8.3.37" || row["digest"] == "" {
		t.Errorf("after pull: %v", row)
	}
	if c := row["container"].(map[string]any); c["exists"] != false || c["status"] != "missing" {
		t.Errorf("container = %v, want none before a site uses the version", c)
	}
	if s := row["sites"].([]any); len(s) != 0 {
		t.Errorf("sites = %v", s)
	}

	for path, want := range map[string]int{
		"/api/php/7.4/pull":          http.StatusNotFound,
		"/api/php/8.3%2F..%2Fx/pull": http.StatusNotFound,
	} {
		if code, out := h.call(http.MethodPost, path, nil); code != want {
			t.Errorf("POST %s: %d %v, want %d", path, code, out, want)
		}
	}
}

func TestFailedPullIsReportedWithItsOutput(t *testing.T) {
	h := newPHPHarness(t, &phpHost{buildErr: errors.New("manifest unknown")})
	if done := h.pull("8.4"); done.State != jobs.StateFailed {
		t.Fatalf("pull job = %+v, want failed", done)
	}
	row := phpVersions(h)["8.4"]
	if row["state"] != "failed" || !strings.Contains(row["output"].(string), "manifest unknown") {
		t.Errorf("version = %v", row)
	}
}

func TestPHPSettingsRoutes(t *testing.T) {
	h := newPHPHarness(t, &phpHost{})
	code, out := h.call(http.MethodGet, "/api/php/8.3/settings", nil)
	if code != http.StatusOK || out["memory_limit"] != "256M" || out["date_timezone"] != "UTC" {
		t.Fatalf("defaults: %d %v", code, out)
	}
	body := map[string]any{"memory_limit": "512M", "upload_max_filesize": "64M", "post_max_size": "64M", "max_execution_time": 30, "date_timezone": "Europe/Rome"}

	// Not pulled yet: there is nothing to validate the change against.
	if code, out := h.call(http.MethodPut, "/api/php/8.3/settings", body); code != http.StatusConflict {
		t.Fatalf("PUT on an absent version: %d %v", code, out)
	}
	h.pull("8.3")

	code, out = h.call(http.MethodPut, "/api/php/8.3/settings", body)
	if code != http.StatusOK || out["memory_limit"] != "512M" || out["date_timezone"] != "Europe/Rome" {
		t.Fatalf("PUT: %d %v", code, out)
	}
	form, _ := h.fs.ReadFile(h.php.FormIni("8.3"))
	if !strings.Contains(string(form), "memory_limit = 512M") || !strings.Contains(string(form), "date.timezone = Europe/Rome") {
		t.Errorf("zz-panel.ini:\n%s", form)
	}

	body["memory_limit"] = "lots"
	code, out = h.call(http.MethodPut, "/api/php/8.3/settings", body)
	if code != http.StatusUnprocessableEntity || out["error"] != errInvalidInput || out["field"] != "memory_limit" {
		t.Errorf("invalid value: %d %v", code, out)
	}

	// PHP rejecting the result is a 422 with its output, verbatim.
	body["memory_limit"] = "1G"
	h.applier.err = &confedit.ApplyError{Stage: confedit.StageValidate, Service: "php-8.3", Output: "PHP:  syntax error in zz-panel.ini\n", Err: errors.New("rejected")}
	code, out = h.call(http.MethodPut, "/api/php/8.3/settings", body)
	if code != http.StatusUnprocessableEntity || out["error"] != errConfigRejected ||
		out["output"] != "PHP:  syntax error in zz-panel.ini\n" || !strings.HasPrefix(out["detail"].(string), "PHP 8.3 rejected") {
		t.Errorf("rejected: %d %v", code, out)
	}
	h.applier.err = nil
	if _, out = h.call(http.MethodGet, "/api/php/8.3/settings", nil); out["memory_limit"] != "512M" {
		t.Errorf("settings after a rejected save = %v", out)
	}

	if code, _ := h.call(http.MethodGet, "/api/php/7.4/settings", nil); code != http.StatusNotFound {
		t.Errorf("unknown version: %d", code)
	}
}

func TestPHPIniRoutes(t *testing.T) {
	h := newPHPHarness(t, &phpHost{})
	if code, _ := h.call(http.MethodGet, "/api/php/8.3/ini", nil); code != http.StatusConflict {
		t.Fatalf("GET ini of an absent version: %d", code)
	}
	h.pull("8.3")

	code, out := h.call(http.MethodGet, "/api/php/8.3/ini", nil)
	if code != http.StatusOK || out["content"] != "; php.ini-production\n" ||
		out["path"] != h.php.IniPath("8.3") || out["history_file"] != "php/8.3/php.ini" {
		t.Fatalf("GET ini: %d %v", code, out)
	}
	if code, out := h.call(http.MethodPut, "/api/php/8.3/ini", map[string]string{"content": "memory_limit = 1G\n"}); code != http.StatusOK {
		t.Fatalf("PUT ini: %d %v", code, out)
	}
	if _, out = h.call(http.MethodGet, "/api/php/8.3/ini", nil); out["content"] != "memory_limit = 1G\n" {
		t.Errorf("ini after save = %v", out["content"])
	}

	h.applier.err = &confedit.ApplyError{Stage: confedit.StageValidate, Service: "php-8.3", Output: "PHP:  syntax error, unexpected '='\n", Err: errors.New("rejected")}
	code, out = h.call(http.MethodPut, "/api/php/8.3/ini", map[string]string{"content": "= = =\n"})
	if code != http.StatusUnprocessableEntity || out["error"] != errConfigRejected || !strings.Contains(out["output"].(string), "syntax error") {
		t.Errorf("rejected ini: %d %v", code, out)
	}
	h.applier.err = nil
	if _, out = h.call(http.MethodGet, "/api/php/8.3/ini", nil); out["content"] != "memory_limit = 1G\n" {
		t.Errorf("ini after a rejected save = %v", out["content"])
	}

	// History: php.ini is restored through its own version's service.
	h.history.content[h.php.IniPath("8.3")+"#1"] = "; old\n"
	if code, out := h.call(http.MethodPost, "/api/config-history/php%2F8.3%2Fphp.ini/1/restore", nil); code != http.StatusOK {
		t.Fatalf("restore: %d %v", code, out)
	}
	if len(h.history.restored) != 1 || h.history.restored[0] != h.php.IniPath("8.3")+"#1 via php-8.3" {
		t.Errorf("restored = %v", h.history.restored)
	}
	for _, file := range []string{"php%2F8.3%2Fpool.d%2Fa.conf", "php%2F..%2Fnginx.conf", "php%2F8.3%2Fphp-fpm.conf"} {
		if code, _ := h.call(http.MethodGet, "/api/config-history/"+file, nil); code != http.StatusNotFound {
			t.Errorf("history of %s: %d, want 404", file, code)
		}
	}
}

func TestPHPSiteRoutes(t *testing.T) {
	h := newPHPHarness(t, &phpHost{})
	create := map[string]any{"domain": "a.test", "kind": "php", "php_version": "8.3", "phpinfo": true}

	code, out := h.call(http.MethodPost, "/api/sites", create)
	if code != http.StatusUnprocessableEntity || out["field"] != "php_version" {
		t.Fatalf("create on an absent version: %d %v", code, out)
	}
	h.pull("8.3")
	code, out = h.call(http.MethodPost, "/api/sites", create)
	site, _ := out["site"].(map[string]any)
	if code != http.StatusCreated || site["kind"] != "php" || site["php_version"] != "8.3" || site["php_pool"] != "a_test" {
		t.Fatalf("create: %d %v", code, out)
	}
	if b, _ := h.fs.ReadFile(h.php.SitesRoot() + "/a.test/index.php"); string(b) != "<?php phpinfo();\n" {
		t.Errorf("index.php = %q", b)
	}
	if users := phpVersions(h)["8.3"]["sites"].([]any); len(users) != 1 || users[0] != "a.test" {
		t.Errorf("sites of 8.3 = %v", users)
	}

	// The pool.
	code, out = h.call(http.MethodGet, "/api/sites/a.test/pool", nil)
	settings, _ := out["settings"].(map[string]any)
	if code != http.StatusOK || out["name"] != "a_test" || out["socket"] != h.php.Socket("8.3", "a_test") || settings["pm"] != "dynamic" {
		t.Fatalf("GET pool: %d %v", code, out)
	}
	pool := map[string]any{"pm": "static", "max_children": 7, "start_servers": 0, "min_spare_servers": 0, "max_spare_servers": 0, "max_requests": 0, "idle_timeout": 10}
	code, out = h.call(http.MethodPut, "/api/sites/a.test/pool", pool)
	if code != http.StatusOK || out["settings"].(map[string]any)["max_children"] != float64(7) {
		t.Fatalf("PUT pool: %d %v", code, out)
	}
	pool["max_children"] = 0
	code, out = h.call(http.MethodPut, "/api/sites/a.test/pool", pool)
	if code != http.StatusUnprocessableEntity || out["field"] != "max_children" {
		t.Errorf("invalid pool: %d %v", code, out)
	}

	// The version switch.
	code, out = h.call(http.MethodPut, "/api/sites/a.test/php-version", map[string]string{"version": "8.4"})
	if code != http.StatusUnprocessableEntity || out["field"] != "php_version" {
		t.Fatalf("switch to an absent version: %d %v", code, out)
	}
	h.pull("8.4")
	h.applier.err = &confedit.ApplyError{Stage: confedit.StageValidate, Service: "php-8.4", Output: "ERROR: bad pool\n", Err: errors.New("rejected")}
	code, out = h.call(http.MethodPut, "/api/sites/a.test/php-version", map[string]string{"version": "8.4"})
	if code != http.StatusUnprocessableEntity || !strings.HasPrefix(out["detail"].(string), "PHP 8.4 rejected") {
		t.Fatalf("rejected switch: %d %v", code, out)
	}
	if _, out = h.call(http.MethodGet, "/api/sites/a.test", nil); out["php_version"] != "8.3" {
		t.Errorf("site after a failed switch = %v, want still 8.3", out["php_version"])
	}
	h.applier.err = nil
	code, out = h.call(http.MethodPut, "/api/sites/a.test/php-version", map[string]string{"version": "8.4"})
	if code != http.StatusOK || out["site"].(map[string]any)["php_version"] != "8.4" {
		t.Fatalf("switch: %d %v", code, out)
	}
	if _, out = h.call(http.MethodGet, "/api/sites/a.test/pool", nil); out["socket"] != h.php.Socket("8.4", "a_test") ||
		out["settings"].(map[string]any)["max_children"] != float64(7) {
		t.Errorf("pool after the switch = %v", out)
	}

	// A static site has no pool.
	h.mustCreate(map[string]any{"domain": "s.test", "kind": "static"})
	if code, _ := h.call(http.MethodGet, "/api/sites/s.test/pool", nil); code != http.StatusUnprocessableEntity {
		t.Errorf("pool of a static site: %d", code)
	}
	if code, _ := h.call(http.MethodGet, "/api/sites/nope.test/pool", nil); code != http.StatusNotFound {
		t.Errorf("pool of a missing site: %d", code)
	}
}
