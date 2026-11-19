package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/docker"
	"github.com/portbyte-id/server-panel/internal/images"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/native"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/sites"
)

// PHPManager is the part of php.Manager the routes use. *php.Manager
// implements it; tests substitute a fake.
type PHPManager interface {
	Paths() php.Paths
	List(ctx context.Context) ([]php.Version, error)
	Get(ctx context.Context, ver string) (php.Version, error)
	StartPull(ctx context.Context, ver string) error
	Pull(ctx context.Context, ver string, exts []php.Extension, progress docker.Progress) error
	CurrentExtensions(ctx context.Context, ver string) ([]php.Extension, error)
	StartRebuild(ctx context.Context, ver string, names []string) ([]php.Extension, error)
	Rebuild(ctx context.Context, ver string, exts []php.Extension, progress docker.Progress) (images.Build, error)
	Builds(ctx context.Context, ver string) ([]images.Build, error)
	StartActivate(ctx context.Context, ver string) error
	Activate(ctx context.Context, ver, id string) (images.Build, error)
	Remove(ctx context.Context, ver string, inUse func(context.Context) (bool, error)) error
	Settings(ctx context.Context, ver string) (php.Settings, error)
	SetSettings(ctx context.Context, ver string, s php.Settings) (confedit.Result, error)
	ReadIni(ctx context.Context, ver string) ([]byte, error)
	WriteIni(ctx context.Context, ver string, content []byte) (confedit.Result, error)
	Service(ver string, start bool) confedit.Service
	// Status is a version's unit as systemd reports it; the bool is false when
	// the unit does not exist yet.
	Status(ctx context.Context, ver string) (native.Status, bool, error)
}

// phpRoutes registers the routes of development plan step 2.8. h is the
// wrapper of nginxRoutes: PHP runs on the same stack, so the same 503 applies.
func (s *server) phpRoutes(mux *http.ServeMux, h func(func(*NginxDeps, http.ResponseWriter, *http.Request)) http.Handler) {
	php := func(fn func(*NginxDeps, http.ResponseWriter, *http.Request)) http.Handler {
		return h(func(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
			if n.PHP == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable, "PHP is not available on this host; see the daemon log")
				return
			}
			fn(n, w, r)
		})
	}

	mux.Handle("GET /api/php", php(s.handleListPHP))
	mux.Handle("GET /api/php/extensions", php(s.handleExtensions))
	mux.Handle("POST /api/php/{ver}/pull", php(s.handlePullPHP))
	mux.Handle("DELETE /api/php/{ver}", php(s.handleRemovePHP))
	mux.Handle("POST /api/php/{ver}/build", php(s.handleBuildPHP))
	mux.Handle("GET /api/php/{ver}/builds", php(s.handleListPHPBuilds))
	mux.Handle("POST /api/php/{ver}/builds/{id}/activate", php(s.handleActivatePHPBuild))
	mux.Handle("GET /api/php/{ver}/settings", php(s.handleGetPHPSettings))
	mux.Handle("PUT /api/php/{ver}/settings", php(s.handlePutPHPSettings))
	mux.Handle("GET /api/php/{ver}/ini", php(s.handleGetPHPIni))
	mux.Handle("PUT /api/php/{ver}/ini", php(s.handlePutPHPIni))

	mux.Handle("GET /api/sites/{domain}/pool", php(s.handleGetPool))
	mux.Handle("PUT /api/sites/{domain}/pool", php(s.handlePutPool))
	mux.Handle("PUT /api/sites/{domain}/php-version", php(s.handlePutPHPVersion))
}

// phpHistoryFile is the history key of a version's php.ini, the one raw PHP
// file. The version is matched exactly, so the key cannot leave its directory.
var phpHistoryFile = regexp.MustCompile(`^php/([0-9]\.[0-9]{1,2})/php\.ini$`)

type phpVersionJSON struct {
	php.Version
	Container containerJSON `json:"container"`
	// Sites are the domains that run on this version.
	Sites []string `json:"sites"`
}

// unitWord is a unit's state in the word the container shape used, so the SPA's
// badge logic reads the same strings it always did.
func unitWord(st native.Status) string {
	switch {
	case st.Active:
		return "running"
	case st.State == "activating":
		return "restarting"
	case st.State == "failed":
		return "failed"
	}
	return "exited"
}

// unitSince is when the unit entered its current state, as the wall clock.
// systemd reports it monotonic, so it is converted: a unit that has not been up
// long enough to say anything yields no time rather than a wrong one.
func unitSince(st native.Status) (time.Time, bool) {
	usec, err := strconv.ParseUint(st.Since, 10, 64)
	if err != nil || usec == 0 {
		return time.Time{}, false
	}
	up := time.Duration(usec) * time.Microsecond
	return time.Now().Add(-up), true
}

func (s *server) handleListPHP(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	versions, err := n.PHP.List(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	all, err := n.Sites.List(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	users := map[string][]string{}
	for _, site := range all {
		if site.PHPVersion != nil {
			users[*site.PHPVersion] = append(users[*site.PHPVersion], site.Domain)
		}
	}

	out := make([]phpVersionJSON, 0, len(versions))
	for _, v := range versions {
		row := phpVersionJSON{Version: v, Sites: users[v.Version]}
		if row.Sites == nil {
			row.Sites = []string{}
		}
		row.Container.Status = "missing"
		if v.State == php.StateReady {
			st, found, err := n.PHP.Status(r.Context(), v.Version)
			switch {
			case err != nil:
				s.logger.WarnContext(r.Context(), "php unit state", "version", v.Version, "error", err)
				row.Container = containerJSON{Error: "systemd did not answer; see the daemon log"}
			case found:
				row.Container = containerJSON{Exists: true, Running: st.Active, Status: unitWord(st)}
				if since, ok := unitSince(st); ok {
					row.Container.StartedAt = &since
				}
			}
		}
		out = append(out, row)
	}

	steps := []sites.PHPStep{}
	if n.PHPReconcile != nil {
		if got := n.PHPReconcile(); got != nil {
			steps = got
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Versions  []phpVersionJSON `json:"versions"`
		Reconcile []sites.PHPStep  `json:"reconcile"`
	}{out, steps})
}

// handlePullPHP installs a version. The body is optional: `{"extensions":[…]}`
// names the extensions of the build; without a body the version keeps the
// extensions of the build it serves, or has none when it was never built.
func (s *server) handlePullPHP(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	ver, ok := s.phpVersion(n, w, r)
	if !ok {
		return
	}
	names, given, err := extensionsBody(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	var exts []php.Extension
	if given {
		exts, err = php.ParseExtensions(names)
	} else {
		exts, err = n.PHP.CurrentExtensions(r.Context(), ver)
	}
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	if err := n.PHP.StartPull(r.Context(), ver); err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	// The job outlives the request that started it.
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), "php.pull", func(ctx context.Context, out *jobs.Writer) error {
		return n.PHP.Pull(ctx, ver, exts, func(line string) { _ = out.Line(line) })
	})
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Job jobs.Job `json:"job"`
	}{job})
}

// phpVersion resolves {ver} to a known version, answering 404 otherwise.
func (s *server) phpVersion(n *NginxDeps, w http.ResponseWriter, r *http.Request) (string, bool) {
	v, err := n.PHP.Get(r.Context(), r.PathValue("ver"))
	if err != nil {
		s.writeNginxError(w, r, err)
		return "", false
	}
	return v.Version, true
}

func (s *server) handleGetPHPSettings(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	ver, ok := s.phpVersion(n, w, r)
	if !ok {
		return
	}
	st, err := n.PHP.Settings(r.Context(), ver)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *server) handlePutPHPSettings(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	ver, ok := s.phpVersion(n, w, r)
	if !ok {
		return
	}
	var req php.Settings
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	if _, err := n.PHP.SetSettings(r.Context(), ver, req); err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	s.handleGetPHPSettings(n, w, r)
}

func (s *server) handleGetPHPIni(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	ver, ok := s.phpVersion(n, w, r)
	if !ok {
		return
	}
	content, err := n.PHP.ReadIni(r.Context(), ver)
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Path        string `json:"path"`
		Content     string `json:"content"`
		HistoryFile string `json:"history_file"`
	}{n.PHP.Paths().IniPath(ver), string(content), "php/" + ver + "/php.ini"})
}

func (s *server) handlePutPHPIni(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	ver, ok := s.phpVersion(n, w, r)
	if !ok {
		return
	}
	var req contentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	res, err := n.PHP.WriteIni(r.Context(), ver, []byte(req.Content))
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, applyResponse{warningsOf(res)})
}

// --- per-site ----------------------------------------------------------------

func (s *server) handleGetPool(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	p, err := n.Sites.Pool(r.Context(), r.PathValue("domain"))
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) handlePutPool(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	var req php.PoolSettings
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	p, res, err := n.Sites.SetPool(r.Context(), r.PathValue("domain"), req)
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		sites.Pool
		applyResponse
	}{p, applyResponse{warningsOf(res)}})
}

func (s *server) handlePutPHPVersion(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	res, err := n.Sites.SwitchPHP(r.Context(), r.PathValue("domain"), req.Version)
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, siteResponse{res.Site, applyResponse{warningsOf(res.Apply)}})
}
