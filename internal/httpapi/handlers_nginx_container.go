package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/nginx"
)

// NginxContainer is what the start, stop and restart routes drive.
// *nginx.Runtime implements it. The name is from when nginx ran in a container.
type NginxContainer interface {
	Start(ctx context.Context) (nginx.EnsureResult, error)
	Stop(ctx context.Context) error
	Restart(ctx context.Context) (nginx.EnsureResult, error)
	// Stopped says the operator stopped nginx from the panel.
	Stopped(ctx context.Context) bool
}

func (s *server) nginxContainerRoutes(mux *http.ServeMux, h func(func(*NginxDeps, http.ResponseWriter, *http.Request)) http.Handler) {
	mux.Handle("POST /api/nginx/container/{action}", h(s.handleNginxContainerAction))
	mux.Handle("POST /api/nginx/image/version", h(s.handleSetNginxVersion))
}

// handleNginxContainerAction starts, stops or restarts nginx as a job, so a graceful
// stop that takes a while is watched, not waited on. The route keeps its name from
// when nginx ran in a container; the service is a systemd unit now.
func (s *server) handleNginxContainerAction(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	if n.Container == nil {
		writeError(w, http.StatusServiceUnavailable, errUnavailable, "nginx cannot be controlled on this host; see the daemon log")
		return
	}
	action := r.PathValue("action")
	var run func(ctx context.Context) error
	switch action {
	case "start":
		run = func(ctx context.Context) error { _, err := n.Container.Start(ctx); return err }
	case "stop":
		run = n.Container.Stop
	case "restart":
		run = func(ctx context.Context) error { _, err := n.Container.Restart(ctx); return err }
	default:
		writeError(w, http.StatusNotFound, errNotFound, "the action must be start, stop or restart")
		return
	}
	if n.Images != nil {
		// A build or version switch owns the container while it runs.
		if err := n.Images.Idle(); err != nil {
			s.writeNginxError(w, r, err)
			return
		}
	}
	note(r, "action", action)
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), "nginx."+action, func(ctx context.Context, out *jobs.Writer) error {
		_ = out.Line(strings.ToUpper(action[:1]) + action[1:] + "ing nginx")
		if err := run(ctx); err != nil {
			return err
		}
		_ = out.Line("Done")
		return nil
	})
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Job jobs.Job `json:"job"`
	}{job})
}

// handleSetNginxVersion switches the container to another nginx version as a job:
// build or reuse the image, check the live configuration against it, swap.
func (s *server) handleSetNginxVersion(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	imgs, ok := nginxImages(n, w)
	if !ok {
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	version := strings.TrimSpace(req.Version)
	if err := nginx.CheckVersion(version); err != nil {
		invalidField(w, "version", "choose a version such as 1.29, or type a release such as 1.29.0")
		return
	}
	if version == imgs.Version() {
		writeError(w, http.StatusConflict, errConflict, "nginx "+version+" already serves")
		return
	}
	if err := imgs.Idle(); err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	note(r, "version", version)
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), "nginx.version", func(ctx context.Context, out *jobs.Writer) error {
		_ = out.Line("Switching nginx to " + version)
		_, err := imgs.SetVersion(ctx, version, func(line string) { _ = out.Line(line) })
		if errors.Is(err, nginx.ErrSameVersion) {
			return nil
		}
		return err
	})
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Job jobs.Job `json:"job"`
	}{job})
}
