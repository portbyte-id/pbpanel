package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/docker"
)

// DockerService is the part of *docker.Client the Docker page uses. Tests
// substitute a fake.
type DockerService interface {
	Info(ctx context.Context) (docker.EngineInfo, error)
	ContainerList(ctx context.Context, all bool) ([]docker.ContainerSummary, error)
	ContainerStart(ctx context.Context, id string) error
	ContainerStop(ctx context.Context, id string, timeout time.Duration) error
	ContainerRestart(ctx context.Context, id string, timeout time.Duration) error
	ContainerKill(ctx context.Context, id, signal string) error
	ContainerRemove(ctx context.Context, id string, force bool) error
	ContainerLogs(ctx context.Context, id string, opts docker.LogOptions, stdout, stderr io.Writer) error
	ContainerTop(ctx context.Context, id string) (docker.ProcessList, error)
	ContainerStats(ctx context.Context, id string) (docker.ContainerStats, error)
	StatsAll(ctx context.Context, ids []string) []docker.ContainerStats
	ImageList(ctx context.Context) ([]docker.ImageSummary, error)
	ImageRemove(ctx context.Context, ref string) error
	VolumeList(ctx context.Context) ([]docker.Volume, error)
	VolumeRemove(ctx context.Context, name string) error
}

const (
	errDockerDown    = "docker_unreachable"
	errPanelManaged  = "panel_managed"
	dockerStopWait   = 10 * time.Second
	dockerLogsTail   = 200
	dockerLogsMax    = 2000
	dockerLogsBytes  = 1 << 20
	dockerCallBudget = 30 * time.Second
)

// dockerNameRe is the grammar of a container ID, name, image ID or volume name.
// The client checks it again; checking here turns a bad path into a 400 instead
// of an error from deep inside.
var dockerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]*$`)

// dockerSignals are the signals the Kill route accepts. Anything else is a 400,
// so the route cannot be used to send an arbitrary string to the Engine.
var dockerSignals = map[string]bool{
	"SIGHUP": true, "SIGINT": true, "SIGQUIT": true, "SIGTERM": true, "SIGKILL": true,
	"SIGUSR1": true, "SIGUSR2": true,
}

// dockerRoutes registers the Docker page's routes. They are registered even when
// Deps.Docker is nil, so the UI gets a 503 that says why.
func (s *server) dockerRoutes(mux *http.ServeMux) {
	h := func(fn func(DockerService, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Docker == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"Docker management is not available; see the daemon log")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), dockerCallBudget)
			defer cancel()
			fn(s.deps.Docker, w, r.WithContext(ctx))
		}))
	}
	mux.Handle("GET /api/docker", h(s.dockerInfo))
	mux.Handle("GET /api/docker/containers", h(s.dockerContainers))
	mux.Handle("GET /api/docker/stats", h(s.dockerStatsAll))
	mux.Handle("GET /api/docker/containers/{id}/stats", h(s.dockerStats))
	mux.Handle("GET /api/docker/containers/{id}/top", h(s.dockerTop))
	mux.Handle("GET /api/docker/containers/{id}/logs", h(s.dockerLogs))
	mux.Handle("POST /api/docker/containers/{id}/start", h(s.dockerAction("start")))
	mux.Handle("POST /api/docker/containers/{id}/stop", h(s.dockerAction("stop")))
	mux.Handle("POST /api/docker/containers/{id}/restart", h(s.dockerAction("restart")))
	mux.Handle("POST /api/docker/containers/{id}/kill", h(s.dockerAction("kill")))
	mux.Handle("DELETE /api/docker/containers/{id}", h(s.dockerRemoveContainer))
	mux.Handle("GET /api/docker/images", h(s.dockerImages))
	mux.Handle("DELETE /api/docker/images/{id}", h(s.dockerRemoveImage))
	mux.Handle("GET /api/docker/volumes", h(s.dockerVolumes))
	mux.Handle("DELETE /api/docker/volumes/{name}", h(s.dockerRemoveVolume))
}

// dockerFail maps an Engine error to a response. The Engine's own message is
// passed through for 409: it names the container or image the operator asked
// about ("image is being used by running container ...") and is what they need
// to act on.
func (s *server) dockerFail(w http.ResponseWriter, r *http.Request, err error) {
	var api *docker.APIError
	switch {
	case errors.Is(err, docker.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound, "Docker has no such item")
	case errors.Is(err, docker.ErrConflict) && errors.As(err, &api):
		writeError(w, http.StatusConflict, errConflict, api.Message)
	case errors.As(err, &api):
		writeError(w, http.StatusBadGateway, errInternal, api.Message)
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, errInternal, "Docker did not answer in time")
	default:
		s.logger.WarnContext(r.Context(), "docker unreachable", "error", err, "path", r.URL.Path)
		writeError(w, http.StatusServiceUnavailable, errDockerDown, "Docker is not reachable; is the Docker service running?")
	}
}

func (s *server) dockerInfo(d DockerService, w http.ResponseWriter, r *http.Request) {
	info, err := d.Info(r.Context())
	if err != nil {
		s.dockerFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *server) dockerContainers(d DockerService, w http.ResponseWriter, r *http.Request) {
	list, err := d.ContainerList(r.Context(), true)
	if err != nil {
		s.dockerFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Containers []docker.ContainerSummary `json:"containers"`
	}{list})
}

// dockerStatsAll samples every running container. It takes about as long as one
// sample, because they run concurrently.
func (s *server) dockerStatsAll(d DockerService, w http.ResponseWriter, r *http.Request) {
	list, err := d.ContainerList(r.Context(), false)
	if err != nil {
		s.dockerFail(w, r, err)
		return
	}
	ids := make([]string, 0, len(list))
	names := make(map[string]string, len(list))
	for _, c := range list {
		names[c.ID] = c.Name
		if c.State == "running" {
			ids = append(ids, c.ID)
		}
	}
	stats := d.StatsAll(r.Context(), ids)
	for i := range stats {
		if n, ok := names[stats[i].ID]; ok {
			stats[i].Name = n
		}
	}
	if stats == nil {
		stats = []docker.ContainerStats{}
	}
	writeJSON(w, http.StatusOK, struct {
		Stats []docker.ContainerStats `json:"stats"`
	}{stats})
}

// dockerTarget validates the {id} path value and finds the container it names.
// Resolving it through the list is what makes the managed check possible, and
// turns an unknown id into a 404 before any control reaches the Engine.
func (s *server) dockerTarget(d DockerService, w http.ResponseWriter, r *http.Request) (docker.ContainerSummary, bool) {
	id := r.PathValue("id")
	if !dockerNameRe.MatchString(id) {
		writeError(w, http.StatusBadRequest, errBadRequest, "that is not a container id or name")
		return docker.ContainerSummary{}, false
	}
	list, err := d.ContainerList(r.Context(), true)
	if err != nil {
		s.dockerFail(w, r, err)
		return docker.ContainerSummary{}, false
	}
	for _, c := range list {
		if c.ID == id || c.Name == id || (len(id) >= 4 && strings.HasPrefix(c.ID, id)) {
			return c, true
		}
	}
	writeError(w, http.StatusNotFound, errNotFound, "Docker has no such container")
	return docker.ContainerSummary{}, false
}

func (s *server) dockerStats(d DockerService, w http.ResponseWriter, r *http.Request) {
	c, ok := s.dockerTarget(d, w, r)
	if !ok {
		return
	}
	st, err := d.ContainerStats(r.Context(), c.ID)
	if err != nil {
		s.dockerFail(w, r, err)
		return
	}
	st.Name = c.Name
	writeJSON(w, http.StatusOK, st)
}

func (s *server) dockerTop(d DockerService, w http.ResponseWriter, r *http.Request) {
	c, ok := s.dockerTarget(d, w, r)
	if !ok {
		return
	}
	top, err := d.ContainerTop(r.Context(), c.ID)
	if err != nil {
		s.dockerFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, top)
}

// capWriter keeps the first limit bytes and drops the rest, so a container that
// logs without end cannot grow the response without bound.
type capWriter struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	room := c.limit - c.buf.Len()
	if room <= 0 {
		c.truncated = true
		return len(p), nil
	}
	if len(p) > room {
		c.buf.Write(p[:room])
		c.truncated = true
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

func (s *server) dockerLogs(d DockerService, w http.ResponseWriter, r *http.Request) {
	c, ok := s.dockerTarget(d, w, r)
	if !ok {
		return
	}
	tail := dockerLogsTail
	if v := r.URL.Query().Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > dockerLogsMax {
			writeError(w, http.StatusBadRequest, errBadRequest, "tail must be between 1 and "+strconv.Itoa(dockerLogsMax))
			return
		}
		tail = n
	}
	out := &capWriter{limit: dockerLogsBytes}
	if err := d.ContainerLogs(r.Context(), c.ID, docker.LogOptions{Tail: tail}, out, out); err != nil {
		s.dockerFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Logs      string `json:"logs"`
		Truncated bool   `json:"truncated"`
	}{out.buf.String(), out.truncated})
}

// dockerAction returns the handler for one lifecycle control. Containers the
// panel owns can be started and restarted but not stopped or killed from here:
// the panel's reconciler would only put them back, and while they are down every
// site is too.
func (s *server) dockerAction(action string) func(DockerService, http.ResponseWriter, *http.Request) {
	return func(d DockerService, w http.ResponseWriter, r *http.Request) {
		c, ok := s.dockerTarget(d, w, r)
		if !ok {
			return
		}
		if c.Managed && (action == "stop" || action == "kill") {
			writeError(w, http.StatusConflict, errPanelManaged,
				"this container is run by the panel; stop its service from Nginx or PHP instead")
			return
		}
		ctx := r.Context()
		var err error
		switch action {
		case "start":
			err = d.ContainerStart(ctx, c.ID)
		case "stop":
			err = d.ContainerStop(ctx, c.ID, dockerStopWait)
		case "restart":
			err = d.ContainerRestart(ctx, c.ID, dockerStopWait)
		case "kill":
			sig := "SIGKILL"
			if v := r.URL.Query().Get("signal"); v != "" {
				sig = v
			}
			if !dockerSignals[sig] {
				writeError(w, http.StatusBadRequest, errBadRequest, "that signal is not allowed")
				return
			}
			err = d.ContainerKill(ctx, c.ID, sig)
		}
		if err != nil {
			s.dockerFail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			ID     string `json:"id"`
			Action string `json:"action"`
		}{c.ID, action})
	}
}

func (s *server) dockerRemoveContainer(d DockerService, w http.ResponseWriter, r *http.Request) {
	c, ok := s.dockerTarget(d, w, r)
	if !ok {
		return
	}
	if c.Managed {
		writeError(w, http.StatusConflict, errPanelManaged,
			"this container is run by the panel and would be recreated; it cannot be removed here")
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if err := d.ContainerRemove(r.Context(), c.ID, force); err != nil {
		s.dockerFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		ID string `json:"id"`
	}{c.ID})
}

// dockerImageRow is an image with the containers that use it, so the page can
// say why a removal will be refused before the operator tries.
type dockerImageRow struct {
	docker.ImageSummary
	InUseBy []string `json:"inUseBy"`
	// Managed is true for images the panel built or pulled for its own services.
	Managed bool `json:"managed"`
}

func (s *server) dockerImages(d DockerService, w http.ResponseWriter, r *http.Request) {
	imgs, err := d.ImageList(r.Context())
	if err != nil {
		s.dockerFail(w, r, err)
		return
	}
	cons, err := d.ContainerList(r.Context(), true)
	if err != nil {
		s.dockerFail(w, r, err)
		return
	}
	users := map[string][]string{}
	for _, c := range cons {
		users[c.ImageID] = append(users[c.ImageID], c.Name)
	}
	rows := make([]dockerImageRow, 0, len(imgs))
	for _, im := range imgs {
		use := users[im.ID]
		if use == nil {
			use = []string{}
		}
		rows = append(rows, dockerImageRow{ImageSummary: im, InUseBy: use, Managed: panelImage(im)})
	}
	writeJSON(w, http.StatusOK, struct {
		Images []dockerImageRow `json:"images"`
	}{rows})
}

// panelImage reports whether any tag of the image is under the panel's own
// repository prefix.
func panelImage(im docker.ImageSummary) bool {
	for _, t := range im.Tags {
		if strings.HasPrefix(t, "panel/") {
			return true
		}
	}
	return false
}

func (s *server) dockerRemoveImage(d DockerService, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !dockerNameRe.MatchString(id) {
		writeError(w, http.StatusBadRequest, errBadRequest, "that is not an image id")
		return
	}
	imgs, err := d.ImageList(r.Context())
	if err != nil {
		s.dockerFail(w, r, err)
		return
	}
	for _, im := range imgs {
		if im.ID != id && im.ID != "sha256:"+id {
			continue
		}
		if panelImage(im) {
			// Removing one would break rollback to an earlier build.
			writeError(w, http.StatusConflict, errPanelManaged,
				"this image belongs to a panel service; remove old builds from Nginx or PHP instead")
			return
		}
		if err := d.ImageRemove(r.Context(), im.ID); err != nil {
			s.dockerFail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			ID string `json:"id"`
		}{im.ID})
		return
	}
	writeError(w, http.StatusNotFound, errNotFound, "Docker has no such image")
}

// dockerVolumeRow is a volume with the containers that mount it.
type dockerVolumeRow struct {
	docker.Volume
	InUseBy []string `json:"inUseBy"`
}

func (s *server) dockerVolumes(d DockerService, w http.ResponseWriter, r *http.Request) {
	vols, err := d.VolumeList(r.Context())
	if err != nil {
		s.dockerFail(w, r, err)
		return
	}
	cons, err := d.ContainerList(r.Context(), true)
	if err != nil {
		s.dockerFail(w, r, err)
		return
	}
	users := map[string][]string{}
	for _, c := range cons {
		for _, v := range c.Volumes {
			users[v] = append(users[v], c.Name)
		}
	}
	rows := make([]dockerVolumeRow, 0, len(vols))
	for _, v := range vols {
		use := users[v.Name]
		if use == nil {
			use = []string{}
		}
		rows = append(rows, dockerVolumeRow{Volume: v, InUseBy: use})
	}
	writeJSON(w, http.StatusOK, struct {
		Volumes []dockerVolumeRow `json:"volumes"`
	}{rows})
}

func (s *server) dockerRemoveVolume(d DockerService, w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !dockerNameRe.MatchString(name) {
		writeError(w, http.StatusBadRequest, errBadRequest, "that is not a volume name")
		return
	}
	if err := d.VolumeRemove(r.Context(), name); err != nil {
		s.dockerFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Name string `json:"name"`
	}{name})
}
