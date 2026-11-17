package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/sites"
)

// phpEngine is a Docker Engine where every pull and build succeeds and no
// container exists. The routes are tested over the real php.Manager on it.
type phpEngine struct{ pullErr error }

func (phpEngine) ImageInspect(context.Context, string) (docker.Image, error) {
	return docker.Image{ID: "sha256:x"}, nil
}
func (phpEngine) ContainerInspect(context.Context, string) (docker.Container, error) {
	return docker.Container{}, docker.ErrNotFound
}
func (phpEngine) ContainerCreate(context.Context, string, docker.ContainerConfig) (string, error) {
	return "id", nil
}
func (phpEngine) ContainerStart(context.Context, string) error               { return nil }
func (phpEngine) ContainerStop(context.Context, string, time.Duration) error { return nil }
func (phpEngine) ContainerRemove(context.Context, string, bool) error        { return nil }
func (phpEngine) ContainerKill(context.Context, string, string) error        { return nil }
func (phpEngine) Exec(context.Context, string, docker.ExecOptions) (docker.ExecResult, error) {
	return docker.ExecResult{}, nil
}
func (phpEngine) RunOnce(_ context.Context, _ string, cfg docker.ContainerConfig) (docker.ExecResult, error) {
	if len(cfg.Entrypoint) > 0 && cfg.Entrypoint[0] == "cat" {
		return docker.ExecResult{Stdout: []byte("; php.ini-production\n")}, nil
	}
	// `php -m` of a build: the candidate check requires the built extensions to
	// be listed, so the fake lists intl, the one the build tests ask for.
	if len(cfg.Entrypoint) == 1 && cfg.Entrypoint[0] == "php" {
		return docker.ExecResult{Stdout: []byte("[PHP Modules]\nCore\nintl\n")}, nil
	}
	return docker.ExecResult{}, nil
}
func (e phpEngine) ImagePull(_ context.Context, _ string, p docker.Progress) (docker.Image, error) {
	p("pulling")
	if e.pullErr != nil {
		return docker.Image{}, e.pullErr
	}
	return docker.Image{ID: "sha256:base", RepoDigests: []string{"php@sha256:abc"}}, nil
}
func (phpEngine) ImageBuild(context.Context, docker.BuildOptions, docker.Progress) (docker.Image, error) {
	return docker.Image{ID: "sha256:derived"}, nil
}
func (phpEngine) ImageTag(context.Context, string, string) error { return nil }
func (phpEngine) ImageRemove(context.Context, string) error      { return nil }

func newPHPHarness(t *testing.T, eng phpEngine) *nginxHarness {
	t.Helper()
	h := newHarness(t)
	paths := nginx.Paths{Root: "/www"}
	mfs := &memFS{files: map[string][]byte{}, dirs: map[string]bool{}}
	ap := &memApplier{fs: mfs}
	svc := &fakeNginx{container: &docker.Container{State: docker.ContainerState{Running: true, Status: "running"}}}
	hist := &fakeHistory{versions: map[string][]confedit.Version{}, content: map[string]string{}}

	phpMgr, err := php.New(php.Options{
		Store: h.store, Engine: eng, Applier: ap, Paths: php.Paths{Root: "/www"}, Read: mfs.ReadFile,
		Scratch:   t.TempDir(),
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
	return &nginxHarness{harness: h, s: h.login(), fs: mfs, applier: ap, svc: svc, history: hist}
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
	h := newPHPHarness(t, phpEngine{})
	vs := phpVersions(h)
	if len(vs) != 2 || vs["8.3"]["state"] != "absent" || vs["8.3"]["image"] != "php:8.3-fpm" || vs["8.3"]["supported"] != true {
		t.Fatalf("versions = %v", vs)
	}

	if done := h.pull("8.3"); done.State != jobs.StateSucceeded {
		t.Fatalf("pull job = %+v", done)
	}
	vs = phpVersions(h)
	row := vs["8.3"]
	if row["state"] != "ready" || row["digest"] != "php@sha256:abc" {
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
	h := newPHPHarness(t, phpEngine{pullErr: errors.New("manifest unknown")})
	if done := h.pull("8.4"); done.State != jobs.StateFailed {
		t.Fatalf("pull job = %+v, want failed", done)
	}
	row := phpVersions(h)["8.4"]
	if row["state"] != "failed" || !strings.Contains(row["output"].(string), "manifest unknown") {
		t.Errorf("version = %v", row)
	}
}

func TestPHPSettingsRoutes(t *testing.T) {
	h := newPHPHarness(t, phpEngine{})
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
	form, _ := h.fs.ReadFile("/www/panel/php/8.3/conf.d/zz-panel.ini")
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
	h := newPHPHarness(t, phpEngine{})
	if code, _ := h.call(http.MethodGet, "/api/php/8.3/ini", nil); code != http.StatusConflict {
		t.Fatalf("GET ini of an absent version: %d", code)
	}
	h.pull("8.3")

	code, out := h.call(http.MethodGet, "/api/php/8.3/ini", nil)
	if code != http.StatusOK || out["content"] != "; php.ini-production\n" ||
		out["path"] != "/www/panel/php/8.3/php.ini" || out["history_file"] != "php/8.3/php.ini" {
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
	h.history.content["/www/panel/php/8.3/php.ini#1"] = "; old\n"
	if code, out := h.call(http.MethodPost, "/api/config-history/php%2F8.3%2Fphp.ini/1/restore", nil); code != http.StatusOK {
		t.Fatalf("restore: %d %v", code, out)
	}
	if len(h.history.restored) != 1 || h.history.restored[0] != "/www/panel/php/8.3/php.ini#1 via php-8.3" {
		t.Errorf("restored = %v", h.history.restored)
	}
	for _, file := range []string{"php%2F8.3%2Fpool.d%2Fa.conf", "php%2F..%2Fnginx.conf", "php%2F8.3%2Fphp-fpm.conf"} {
		if code, _ := h.call(http.MethodGet, "/api/config-history/"+file, nil); code != http.StatusNotFound {
			t.Errorf("history of %s: %d, want 404", file, code)
		}
	}
}

func TestPHPSiteRoutes(t *testing.T) {
	h := newPHPHarness(t, phpEngine{})
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
	if b, _ := h.fs.ReadFile("/www/services/sites/a.test/index.php"); string(b) != "<?php phpinfo();\n" {
		t.Errorf("index.php = %q", b)
	}
	if users := phpVersions(h)["8.3"]["sites"].([]any); len(users) != 1 || users[0] != "a.test" {
		t.Errorf("sites of 8.3 = %v", users)
	}

	// The pool.
	code, out = h.call(http.MethodGet, "/api/sites/a.test/pool", nil)
	settings, _ := out["settings"].(map[string]any)
	if code != http.StatusOK || out["name"] != "a_test" || out["socket"] != "/www/panel/run/php-8.3-a_test.sock" || settings["pm"] != "dynamic" {
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
	if _, out = h.call(http.MethodGet, "/api/sites/a.test/pool", nil); out["socket"] != "/www/panel/run/php-8.4-a_test.sock" ||
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
