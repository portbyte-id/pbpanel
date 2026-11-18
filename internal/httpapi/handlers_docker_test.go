package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
)

// fakeDocker answers from its fields and records the controls it was given, so
// a test can assert both what a route sent to the Engine and what it refused to.
type fakeDocker struct {
	err        error
	containers []docker.ContainerSummary
	images     []docker.ImageSummary
	volumes    []docker.Volume
	calls      []string
	logText    string
}

func (f *fakeDocker) rec(s string) { f.calls = append(f.calls, s) }

func (f *fakeDocker) Info(context.Context) (docker.EngineInfo, error) {
	return docker.EngineInfo{ServerVersion: "29.9.0", Running: 1}, f.err
}
func (f *fakeDocker) ContainerList(_ context.Context, all bool) ([]docker.ContainerSummary, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []docker.ContainerSummary
	for _, c := range f.containers {
		if all || c.State == "running" {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *fakeDocker) ContainerStart(_ context.Context, id string) error {
	f.rec("start " + id)
	return f.err
}
func (f *fakeDocker) ContainerStop(_ context.Context, id string, _ time.Duration) error {
	f.rec("stop " + id)
	return f.err
}
func (f *fakeDocker) ContainerRestart(_ context.Context, id string, _ time.Duration) error {
	f.rec("restart " + id)
	return f.err
}
func (f *fakeDocker) ContainerKill(_ context.Context, id, sig string) error {
	f.rec("kill " + id + " " + sig)
	return f.err
}
func (f *fakeDocker) ContainerRemove(_ context.Context, id string, force bool) error {
	if force {
		f.rec("rm -f " + id)
	} else {
		f.rec("rm " + id)
	}
	return f.err
}
func (f *fakeDocker) ContainerLogs(_ context.Context, id string, _ docker.LogOptions, out, _ io.Writer) error {
	f.rec("logs " + id)
	_, _ = io.WriteString(out, f.logText)
	return f.err
}
func (f *fakeDocker) ContainerTop(context.Context, string) (docker.ProcessList, error) {
	return docker.ProcessList{Titles: []string{"PID"}, Processes: [][]string{{"1"}}}, f.err
}
func (f *fakeDocker) ContainerStats(_ context.Context, id string) (docker.ContainerStats, error) {
	return docker.ContainerStats{ID: id, CPUPercent: 12.5}, f.err
}
func (f *fakeDocker) StatsAll(_ context.Context, ids []string) []docker.ContainerStats {
	out := []docker.ContainerStats{}
	for _, id := range ids {
		out = append(out, docker.ContainerStats{ID: id})
	}
	return out
}
func (f *fakeDocker) ImageList(context.Context) ([]docker.ImageSummary, error) {
	return f.images, f.err
}
func (f *fakeDocker) ImageRemove(_ context.Context, ref string) error {
	f.rec("rmi " + ref)
	return f.err
}
func (f *fakeDocker) VolumeList(context.Context) ([]docker.Volume, error) { return f.volumes, f.err }
func (f *fakeDocker) VolumeRemove(_ context.Context, name string) error {
	f.rec("rmvol " + name)
	return f.err
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		containers: []docker.ContainerSummary{
			{ID: "aaaa1111", Name: "redis", State: "running", ImageID: "sha256:r", Volumes: []string{"redis-data"}},
			{ID: "bbbb2222", Name: "panel-nginx", State: "running", Managed: true, ImageID: "sha256:n"},
			{ID: "cccc3333", Name: "old", State: "exited", ImageID: "sha256:o"},
		},
		images: []docker.ImageSummary{
			{ID: "sha256:r", Tags: []string{"redis:7"}},
			{ID: "sha256:n", Tags: []string{"panel/nginx:1"}},
			{ID: "sha256:d", Tags: []string{}, Dangling: true},
		},
		volumes: []docker.Volume{{Name: "redis-data"}, {Name: "spare"}},
	}
}

func dockerHarness(t *testing.T, d DockerService) (*harness, session) {
	t.Helper()
	h := newHarness(t)
	if d != nil {
		h.deps.Docker = d
		var err error
		if h.handler, err = New(h.deps); err != nil {
			t.Fatal(err)
		}
	}
	return h, h.login()
}

func TestDockerRoutesUnavailableWithoutService(t *testing.T) {
	h, s := dockerHarness(t, nil)
	for _, p := range []string{"/api/docker", "/api/docker/containers", "/api/docker/images", "/api/docker/volumes", "/api/docker/stats"} {
		if rec := h.do(http.MethodGet, p, s.cookie, s.csrf, nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s = %d, want 503", p, rec.Code)
		}
	}
}

func TestDockerRoutesNeedLogin(t *testing.T) {
	h, _ := dockerHarness(t, newFakeDocker())
	if rec := h.do(http.MethodGet, "/api/docker/containers", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestDockerListingsAnnotateUsage(t *testing.T) {
	h, s := dockerHarness(t, newFakeDocker())

	var imgs struct {
		Images []struct {
			ID      string   `json:"id"`
			InUseBy []string `json:"inUseBy"`
			Managed bool     `json:"managed"`
		} `json:"images"`
	}
	rec := h.do(http.MethodGet, "/api/docker/images", s.cookie, s.csrf, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("images: %d %s", rec.Code, rec.Body.String())
	}
	h.decode(rec, &imgs)
	by := map[string]int{}
	for i, im := range imgs.Images {
		by[im.ID] = i
	}
	if u := imgs.Images[by["sha256:r"]].InUseBy; len(u) != 1 || u[0] != "redis" {
		t.Errorf("redis image users: %v", u)
	}
	if !imgs.Images[by["sha256:n"]].Managed || imgs.Images[by["sha256:r"]].Managed {
		t.Error("managed flag wrong")
	}
	if imgs.Images[by["sha256:d"]].InUseBy == nil {
		t.Error("inUseBy must be [] not null")
	}

	var vols struct {
		Volumes []struct {
			Name    string   `json:"name"`
			InUseBy []string `json:"inUseBy"`
		} `json:"volumes"`
	}
	h.decode(h.do(http.MethodGet, "/api/docker/volumes", s.cookie, s.csrf, nil), &vols)
	if len(vols.Volumes) != 2 || len(vols.Volumes[0].InUseBy) != 1 || vols.Volumes[1].InUseBy == nil || len(vols.Volumes[1].InUseBy) != 0 {
		t.Errorf("volumes: %+v", vols.Volumes)
	}
}

func TestDockerStatsAllSamplesRunningOnly(t *testing.T) {
	h, s := dockerHarness(t, newFakeDocker())
	var out struct {
		Stats []docker.ContainerStats `json:"stats"`
	}
	h.decode(h.do(http.MethodGet, "/api/docker/stats", s.cookie, s.csrf, nil), &out)
	if len(out.Stats) != 2 {
		t.Fatalf("want the 2 running containers, got %+v", out.Stats)
	}
	for _, st := range out.Stats {
		if st.Name == "" {
			t.Errorf("name must be filled from the list: %+v", st)
		}
	}
}

func TestDockerContainerControls(t *testing.T) {
	f := newFakeDocker()
	h, s := dockerHarness(t, f)

	for _, c := range []struct{ path, want string }{
		{"/api/docker/containers/redis/start", "start aaaa1111"},
		{"/api/docker/containers/redis/stop", "stop aaaa1111"},
		{"/api/docker/containers/aaaa/restart", "restart aaaa1111"}, // id prefix
		{"/api/docker/containers/redis/kill", "kill aaaa1111 SIGKILL"},
		{"/api/docker/containers/redis/kill?signal=SIGHUP", "kill aaaa1111 SIGHUP"},
	} {
		f.calls = nil
		if rec := h.do(http.MethodPost, c.path, s.cookie, s.csrf, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.path, rec.Code, rec.Body.String())
		}
		if len(f.calls) != 1 || f.calls[0] != c.want {
			t.Errorf("%s sent %v, want %q", c.path, f.calls, c.want)
		}
	}

	f.calls = nil
	if rec := h.do(http.MethodDelete, "/api/docker/containers/old?force=true", s.cookie, s.csrf, nil); rec.Code != http.StatusOK || f.calls[0] != "rm -f cccc3333" {
		t.Fatalf("remove: %d %v", rec.Code, f.calls)
	}
}

func TestDockerRefusesWhatItShould(t *testing.T) {
	f := newFakeDocker()
	h, s := dockerHarness(t, f)
	cases := []struct {
		name, method, path string
		status             int
		class              string
	}{
		{"stop panel container", "POST", "/api/docker/containers/panel-nginx/stop", 409, errPanelManaged},
		{"kill panel container", "POST", "/api/docker/containers/panel-nginx/kill", 409, errPanelManaged},
		{"remove panel container", "DELETE", "/api/docker/containers/panel-nginx", 409, errPanelManaged},
		{"remove panel image", "DELETE", "/api/docker/images/sha256:n", 409, errPanelManaged},
		{"unknown container", "POST", "/api/docker/containers/nope/start", 404, errNotFound},
		{"unknown image", "DELETE", "/api/docker/images/sha256:zzz", 404, errNotFound},
		{"bad signal", "POST", "/api/docker/containers/redis/kill?signal=rm%20-rf", 400, errBadRequest},
		{"bad id", "POST", "/api/docker/containers/..%2Fetc/start", 400, errBadRequest},
		{"bad tail", "GET", "/api/docker/containers/redis/logs?tail=99999", 400, errBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f.calls = nil
			rec := h.do(c.method, c.path, s.cookie, s.csrf, nil)
			env := h.errorEnvelopeOf(rec)
			if rec.Code != c.status || env.Error != c.class {
				t.Fatalf("got %d %+v, want %d %s", rec.Code, env, c.status, c.class)
			}
			if len(f.calls) != 0 {
				t.Errorf("a refused request still reached the Engine: %v", f.calls)
			}
		})
	}

	// Panel containers can still be started and restarted.
	for _, p := range []string{"start", "restart"} {
		if rec := h.do(http.MethodPost, "/api/docker/containers/panel-nginx/"+p, s.cookie, s.csrf, nil); rec.Code != http.StatusOK {
			t.Errorf("%s panel container: %d", p, rec.Code)
		}
	}
}

func TestDockerImageRemoveMatchesWholeID(t *testing.T) {
	f := newFakeDocker()
	h, s := dockerHarness(t, f)
	if rec := h.do(http.MethodDelete, "/api/docker/images/sha256", s.cookie, s.csrf, nil); rec.Code != http.StatusNotFound || len(f.calls) != 0 {
		t.Fatalf("a partial id must not prefix-match: %d %v", rec.Code, f.calls)
	}
	if rec := h.do(http.MethodDelete, "/api/docker/images/sha256:d", s.cookie, s.csrf, nil); rec.Code != http.StatusOK || f.calls[0] != "rmi sha256:d" {
		t.Fatalf("remove dangling: %d %v", rec.Code, f.calls)
	}
}

func TestDockerErrorMapping(t *testing.T) {
	apiErr := func(status int, msg string) error { return &docker.APIError{Op: "x", Status: status, Message: msg} }
	cases := []struct {
		name   string
		err    error
		status int
		class  string
		detail string
	}{
		{"in use", apiErr(409, "image is being used by running container abc"), 409, errConflict, "image is being used by running container abc"},
		{"missing", apiErr(404, "No such volume"), 404, errNotFound, ""},
		{"engine error", apiErr(500, "boom from engine"), 502, errInternal, "boom from engine"},
		{"daemon down", errors.New("docker: list volumes: dial unix /var/run/docker.sock: no such file"), 503, errDockerDown, "Docker is not reachable; is the Docker service running?"},
		{"timeout", context.DeadlineExceeded, 504, errInternal, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, s := dockerHarness(t, &fakeDocker{err: c.err})
			rec := h.do(http.MethodGet, "/api/docker/volumes", s.cookie, s.csrf, nil)
			env := h.errorEnvelopeOf(rec)
			if rec.Code != c.status || env.Error != c.class {
				t.Fatalf("got %d %+v, want %d %s", rec.Code, env, c.status, c.class)
			}
			if c.detail != "" && env.Detail != c.detail {
				t.Errorf("detail %q, want %q", env.Detail, c.detail)
			}
			if strings.Contains(env.Detail, "/var/run/docker.sock") {
				t.Errorf("socket path leaked: %q", env.Detail)
			}
		})
	}
}

func TestDockerLogsAreCappedAndMarkedTruncated(t *testing.T) {
	f := newFakeDocker()
	f.logText = strings.Repeat("x", dockerLogsBytes+500)
	h, s := dockerHarness(t, f)
	var out struct {
		Logs      string `json:"logs"`
		Truncated bool   `json:"truncated"`
	}
	h.decode(h.do(http.MethodGet, "/api/docker/containers/redis/logs?tail=50", s.cookie, s.csrf, nil), &out)
	if len(out.Logs) != dockerLogsBytes || !out.Truncated {
		t.Fatalf("len=%d truncated=%v", len(out.Logs), out.Truncated)
	}
}
