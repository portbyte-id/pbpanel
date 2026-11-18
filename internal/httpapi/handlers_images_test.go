package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/nginx"
)

// waitJob waits for the job a 202 answer carries, as pull does.
func (h *nginxHarness) waitJob(out map[string]any) jobs.Job {
	h.t.Helper()
	job := out["job"].(map[string]any)
	done, err := h.deps.Jobs.Wait(context.Background(), job["id"].(string))
	if err != nil {
		h.t.Fatal(err)
	}
	return done
}

// The extension catalogue is served as is, so the UI can offer the same list
// the build accepts.
func TestExtensionsCatalogueIsServed(t *testing.T) {
	h := newPHPHarness(t, phpEngine{})
	code, out := h.call(http.MethodGet, "/api/php/extensions", nil)
	if code != http.StatusOK {
		t.Fatalf("GET extensions = %d %v", code, out)
	}
	names := map[string]bool{}
	for _, e := range out["extensions"].([]any) {
		names[e.(map[string]any)["name"].(string)] = true
	}
	if !names["intl"] || !names["redis"] {
		t.Errorf("catalogue lacks intl or redis: %v", names)
	}
}

// A name that is not a package name never reaches the Dockerfile: the request
// is refused with the field named, and the version's state is untouched.
func TestBuildRefusesAShellLikeExtensionName(t *testing.T) {
	h := newPHPHarness(t, phpEngine{})
	if done := h.pull("8.3"); done.State != jobs.StateSucceeded {
		t.Fatalf("pull job = %+v", done)
	}
	code, out := h.call(http.MethodPost, "/api/php/8.3/build", map[string]any{"extensions": []string{"x;rm -rf /"}})
	if code != http.StatusUnprocessableEntity || out["field"] != "extensions" {
		t.Errorf("POST build = %d %v, want 422 naming extensions", code, out)
	}
}

// A rebuild is a job; once it has run, the builds list shows both builds and
// the one that serves.
func TestBuildRunsAsAJobAndIsListed(t *testing.T) {
	h := newPHPHarness(t, phpEngine{})
	if done := h.pull("8.3"); done.State != jobs.StateSucceeded {
		t.Fatalf("pull job = %+v", done)
	}
	code, out := h.call(http.MethodPost, "/api/php/8.3/build", map[string]any{"extensions": []string{"intl"}})
	if code != http.StatusAccepted {
		t.Fatalf("POST build = %d %v", code, out)
	}
	if done := h.waitJob(out); done.State != jobs.StateSucceeded {
		t.Fatalf("build job = %+v", done)
	}

	code, out = h.call(http.MethodGet, "/api/php/8.3/builds", nil)
	if code != http.StatusOK {
		t.Fatalf("GET builds = %d %v", code, out)
	}
	builds := out["builds"].([]any)
	if len(builds) != 2 {
		t.Fatalf("builds = %v, want the pull's and the rebuild's", builds)
	}
	newest := builds[0].(map[string]any)
	if newest["current"] != true || newest["state"] != string(images.StateReady) {
		t.Errorf("newest build = %v, want ready and current", newest)
	}
	if got := newest["extensions"].([]any); len(got) != 1 || got[0] != "intl" {
		t.Errorf("newest build extensions = %v", got)
	}
}

// Removing a version with nothing using it stops it and leaves the catalogue
// row absent, so the version can be installed again.
func TestRemoveAVersionNobodyUses(t *testing.T) {
	h := newPHPHarness(t, phpEngine{})
	if done := h.pull("8.3"); done.State != jobs.StateSucceeded {
		t.Fatalf("pull job = %+v", done)
	}
	code, out := h.call(http.MethodDelete, "/api/php/8.3", nil)
	if code != http.StatusOK || out["state"] != "absent" {
		t.Fatalf("DELETE = %d %v", code, out)
	}
	vs := phpVersions(h)
	if vs["8.3"]["state"] != "absent" {
		t.Errorf("after remove: %v", vs["8.3"])
	}
}

// fakeNginxImages answers the nginx image routes without Docker.
type fakeNginxImages struct {
	builds   []images.Build
	version  string
	switched chan string
}

func (f *fakeNginxImages) Version() string {
	if f.version == "" {
		return "1.29"
	}
	return f.version
}

func (f *fakeNginxImages) SetVersion(_ context.Context, v string, _ docker.Progress) (images.Build, error) {
	if f.switched != nil {
		f.switched <- v
	}
	return images.Build{}, nil
}

// fakeNginxContainer records which action ran.
type fakeNginxContainer struct {
	ran     chan string
	stopped bool
}

func (f *fakeNginxContainer) Start(context.Context) (nginx.EnsureResult, error) {
	f.ran <- "start"
	return nginx.EnsureResult{}, nil
}
func (f *fakeNginxContainer) Stop(context.Context) error { f.ran <- "stop"; return nil }
func (f *fakeNginxContainer) Restart(context.Context) (nginx.EnsureResult, error) {
	f.ran <- "restart"
	return nginx.EnsureResult{}, nil
}
func (f *fakeNginxContainer) Stopped(context.Context) bool { return f.stopped }

func (f *fakeNginxImages) Idle() error { return nil }
func (f *fakeNginxImages) Builds(context.Context) ([]images.Build, error) {
	return f.builds, nil
}
func (f *fakeNginxImages) Current(context.Context) (images.Build, bool, error) {
	for _, b := range f.builds {
		if b.Current {
			return b, true, nil
		}
	}
	return images.Build{}, false, nil
}
func (f *fakeNginxImages) Present(context.Context) (bool, error) { return true, nil }
func (f *fakeNginxImages) Build(context.Context, docker.Progress) (images.Build, error) {
	return images.Build{}, nil
}
func (f *fakeNginxImages) Activate(context.Context, string) (images.Build, error) {
	return images.Build{}, nil
}

// The nginx image answers what serves and what served before, and an
// activation of the build that already serves is refused.
func TestNginxImageShowsCurrentAndPrevious(t *testing.T) {
	h := newPHPHarness(t, phpEngine{})
	h.deps.Nginx.Images = &fakeNginxImages{builds: []images.Build{
		{ID: "20261117120001-aaaaaa", State: images.StateReady, Tag: "panel/nginx:1.29-20261117120001-aaaaaa", Current: true},
		{ID: "20261117120000-bbbbbb", State: images.StateReady, Tag: "panel/nginx:1.29-20261117120000-bbbbbb", Previous: true},
	}}
	var err error
	h.handler, err = New(h.deps)
	if err != nil {
		t.Fatal(err)
	}

	code, out := h.call(http.MethodGet, "/api/nginx/image", nil)
	if code != http.StatusOK {
		t.Fatalf("GET nginx image = %d %v", code, out)
	}
	if out["base"] != nginx.BuildSource || out["repo"] != "panel/nginx:1.29" {
		t.Errorf("base/repo = %v / %v", out["base"], out["repo"])
	}
	if cur := out["current"].(map[string]any); cur["id"] != "20261117120001-aaaaaa" {
		t.Errorf("current = %v", cur)
	}
	if prev := out["previous"].(map[string]any); prev["id"] != "20261117120000-bbbbbb" {
		t.Errorf("previous = %v", prev)
	}

	code, out = h.call(http.MethodPost, "/api/nginx/image/builds/20261117120001-aaaaaa/activate", map[string]any{})
	if code != http.StatusConflict {
		t.Errorf("activating the serving build = %d %v, want 409", code, out)
	}
	code, _ = h.call(http.MethodPost, "/api/nginx/image/builds/20260101000000-cccccc/activate", map[string]any{})
	if code != http.StatusNotFound {
		t.Errorf("activating an unknown build = %d, want 404", code)
	}
}

func imagesHarness(t *testing.T, imgs *fakeNginxImages, c *fakeNginxContainer) *nginxHarness {
	t.Helper()
	h := newPHPHarness(t, phpEngine{})
	h.deps.Nginx.Images = imgs
	if c != nil {
		h.deps.Nginx.Container = c
	}
	var err error
	h.handler, err = New(h.deps)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func waitRan(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("the job never ran")
		return ""
	}
}

func TestNginxContainerActions(t *testing.T) {
	c := &fakeNginxContainer{ran: make(chan string, 1)}
	h := imagesHarness(t, &fakeNginxImages{}, c)
	for _, action := range []string{"stop", "start", "restart"} {
		code, out := h.call(http.MethodPost, "/api/nginx/container/"+action, map[string]any{})
		if code != http.StatusAccepted {
			t.Fatalf("%s = %d %v", action, code, out)
		}
		if got := waitRan(t, c.ran); got != action {
			t.Errorf("ran %q, want %q", got, action)
		}
	}
	if code, _ := h.call(http.MethodPost, "/api/nginx/container/explode", map[string]any{}); code != http.StatusNotFound {
		t.Errorf("unknown action = %d, want 404", code)
	}
}

func TestNginxContainerUnavailableWithoutRuntime(t *testing.T) {
	h := imagesHarness(t, &fakeNginxImages{}, nil)
	if code, _ := h.call(http.MethodPost, "/api/nginx/container/stop", map[string]any{}); code != http.StatusServiceUnavailable {
		t.Errorf("stop without a runtime = %d, want 503", code)
	}
}

func TestNginxVersionSwitch(t *testing.T) {
	imgs := &fakeNginxImages{switched: make(chan string, 1)}
	h := imagesHarness(t, imgs, &fakeNginxContainer{ran: make(chan string, 1), stopped: true})

	code, out := h.call(http.MethodGet, "/api/nginx/image", nil)
	if code != http.StatusOK || out["version"] != "1.29" || out["stopped"] != true {
		t.Fatalf("GET image = %d %v", code, out)
	}
	if code, _ := h.call(http.MethodPost, "/api/nginx/image/version", map[string]any{"version": "latest"}); code != http.StatusBadRequest {
		t.Errorf("bad version = %d, want 400", code)
	}
	if code, _ := h.call(http.MethodPost, "/api/nginx/image/version", map[string]any{"version": "1.29"}); code != http.StatusConflict {
		t.Errorf("same version = %d, want 409", code)
	}
	code, out = h.call(http.MethodPost, "/api/nginx/image/version", map[string]any{"version": "1.28"})
	if code != http.StatusAccepted {
		t.Fatalf("switch = %d %v", code, out)
	}
	if got := waitRan(t, imgs.switched); got != "1.28" {
		t.Errorf("switched to %q", got)
	}
}
