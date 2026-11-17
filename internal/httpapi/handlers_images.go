package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
)

// NginxImages is the part of nginx.Images the routes use. *nginx.Images
// implements it; tests substitute a fake.
type NginxImages interface {
	Idle() error
	Builds(ctx context.Context) ([]images.Build, error)
	Current(ctx context.Context) (images.Build, bool, error)
	Present(ctx context.Context) (bool, error)
	Build(ctx context.Context, progress docker.Progress) (images.Build, error)
	Activate(ctx context.Context, id string) (images.Build, error)
}

// extensionsRequest is the body of a PHP build: the extensions to compile in.
type extensionsRequest struct {
	Extensions []string `json:"extensions"`
}

// extensionsBody reads the optional body of a pull. An empty body is not an
// error: it reports that no list was given, and the caller keeps the current
// build's extensions.
func extensionsBody(w http.ResponseWriter, r *http.Request) ([]string, bool, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		return nil, false, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var req extensionsRequest
	if err := dec.Decode(&req); err != nil {
		return nil, false, err
	}
	return req.Extensions, true, nil
}

// findBuild returns the build with the given ID from a family's list.
func findBuild(builds []images.Build, id string) (images.Build, bool) {
	for _, b := range builds {
		if b.ID == id {
			return b, true
		}
	}
	return images.Build{}, false
}

// handleExtensions answers the curated catalogue: what a PHP build can add.
func (s *server) handleExtensions(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Extensions []php.Extension `json:"extensions"`
	}{php.Catalogue()})
}

// handleBuildPHP builds a version again with the extensions the body names and
// serves the result if it passes. The version keeps serving until then.
func (s *server) handleBuildPHP(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	ver, ok := s.phpVersion(n, w, r)
	if !ok {
		return
	}
	var req extensionsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	exts, err := n.PHP.StartRebuild(r.Context(), ver, req.Extensions)
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), "php.build", func(ctx context.Context, out *jobs.Writer) error {
		_, err := n.PHP.Rebuild(ctx, ver, exts, func(line string) { _ = out.Line(line) })
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

// handleRemovePHP uninstalls a version. A version a site uses is refused (409).
func (s *server) handleRemovePHP(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	ver, ok := s.phpVersion(n, w, r)
	if !ok {
		return
	}
	inUse := func(ctx context.Context) (bool, error) {
		all, err := n.Sites.List(ctx)
		if err != nil {
			return false, err
		}
		for _, site := range all {
			if site.PHPVersion != nil && *site.PHPVersion == ver {
				return true, nil
			}
		}
		return false, nil
	}
	if err := n.PHP.Remove(r.Context(), ver, inUse); err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Version string `json:"version"`
		State   string `json:"state"`
	}{ver, string(php.StateAbsent)})
}

// handleListPHPBuilds lists a version's builds, newest first.
func (s *server) handleListPHPBuilds(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	ver, ok := s.phpVersion(n, w, r)
	if !ok {
		return
	}
	builds, err := n.PHP.Builds(r.Context(), ver)
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	if builds == nil {
		builds = []images.Build{}
	}
	writeJSON(w, http.StatusOK, struct {
		Builds []images.Build `json:"builds"`
	}{builds})
}

// handleActivatePHPBuild makes an earlier ready build the one a version serves.
func (s *server) handleActivatePHPBuild(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	ver, ok := s.phpVersion(n, w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	builds, err := n.PHP.Builds(r.Context(), ver)
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	b, found := findBuild(builds, id)
	if !found {
		writeError(w, http.StatusNotFound, errNotFound, "no such build")
		return
	}
	if b.Current {
		writeError(w, http.StatusConflict, errConflict, "that build already serves")
		return
	}
	if b.State != images.StateReady {
		writeError(w, http.StatusConflict, errConflict, "that build is "+string(b.State)+" and cannot serve")
		return
	}
	if err := n.PHP.StartActivate(r.Context(), ver); err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), "php.activate", func(ctx context.Context, out *jobs.Writer) error {
		_, err := n.PHP.Activate(ctx, ver, id)
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

// --- nginx image ---------------------------------------------------------------

// nginxImageRoutes registers the nginx image routes of development plan step 8.6.
func (s *server) nginxImageRoutes(mux *http.ServeMux, h func(func(*NginxDeps, http.ResponseWriter, *http.Request)) http.Handler) {
	mux.Handle("GET /api/nginx/image", h(s.handleGetNginxImage))
	mux.Handle("POST /api/nginx/image/update", h(s.handleUpdateNginxImage))
	mux.Handle("POST /api/nginx/image/builds/{id}/activate", h(s.handleActivateNginxBuild))
}

// nginxImages resolves the nginx image manager, answering 503 when it is absent.
func nginxImages(n *NginxDeps, w http.ResponseWriter) (NginxImages, bool) {
	if n.Images == nil {
		writeError(w, http.StatusServiceUnavailable, errUnavailable, "the nginx image manager is not available on this host; see the daemon log")
		return nil, false
	}
	return n.Images, true
}

// handleGetNginxImage answers the nginx image: what serves, what served before,
// every build, and whether an operation is running.
func (s *server) handleGetNginxImage(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	imgs, ok := nginxImages(n, w)
	if !ok {
		return
	}
	ctx := r.Context()
	builds, err := imgs.Builds(ctx)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	cur, has, err := imgs.Current(ctx)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	present, err := imgs.Present(ctx)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	var current, previous *images.Build
	if has {
		current = &cur
	}
	for i := range builds {
		if builds[i].Previous {
			previous = &builds[i]
		}
	}
	if builds == nil {
		builds = []images.Build{}
	}
	writeJSON(w, http.StatusOK, struct {
		Base     string         `json:"base"`
		Repo     string         `json:"repo"`
		Present  bool           `json:"present"`
		Busy     bool           `json:"busy"`
		Current  *images.Build  `json:"current"`
		Previous *images.Build  `json:"previous"`
		Builds   []images.Build `json:"builds"`
	}{
		Base: nginx.BaseImage, Repo: nginx.Repo + ":" + nginx.Version, Present: present,
		Busy: imgs.Idle() != nil, Current: current, Previous: previous, Builds: builds,
	})
}

// handleUpdateNginxImage builds the image again from the newest base image. The
// build is checked with `nginx -t` and swapped in; the one before it is kept.
func (s *server) handleUpdateNginxImage(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	imgs, ok := nginxImages(n, w)
	if !ok {
		return
	}
	if err := imgs.Idle(); err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), "nginx.update", func(ctx context.Context, out *jobs.Writer) error {
		_, err := imgs.Build(ctx, func(line string) { _ = out.Line(line) })
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

// handleActivateNginxBuild puts an earlier nginx build back: the rollback.
func (s *server) handleActivateNginxBuild(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	imgs, ok := nginxImages(n, w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	builds, err := imgs.Builds(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	b, found := findBuild(builds, id)
	if !found {
		writeError(w, http.StatusNotFound, errNotFound, "no such build")
		return
	}
	if b.Current {
		writeError(w, http.StatusConflict, errConflict, "that build already serves")
		return
	}
	if b.State != images.StateReady {
		writeError(w, http.StatusConflict, errConflict, "that build is "+string(b.State)+" and cannot serve")
		return
	}
	if err := imgs.Idle(); err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), "nginx.activate", func(ctx context.Context, out *jobs.Writer) error {
		_, err := imgs.Activate(ctx, id)
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
