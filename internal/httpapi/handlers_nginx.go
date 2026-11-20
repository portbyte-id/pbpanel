package httpapi

import (
	"context"
	"errors"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/images"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/sites"
)

// ConfigHistory is the part of confedit.Editor the history routes use.
type ConfigHistory interface {
	History(ctx context.Context, path string) ([]confedit.Version, error)
	ReadVersion(ctx context.Context, path string, version int) ([]byte, error)
	Restore(ctx context.Context, path string, version int, services []confedit.Service) (confedit.Result, error)
}

// NginxService is the live nginx service: what a restore validates and reloads
// through, and where the status route reads systemd's state of the unit.
type NginxService interface {
	confedit.Service
	State(ctx context.Context) (nginx.Status, bool, error)
}

// ReconcileStatus reports the outcome of the boot reconcile.
type ReconcileStatus interface {
	Status() sites.Status
}

// NginxDeps is what the nginx and sites routes run on.
type NginxDeps struct {
	Sites      *sites.Manager
	Global     *nginx.Global
	History    ConfigHistory
	Service    NginxService
	Reconciler ReconcileStatus
	Paths      nginx.Paths

	// PHP carries the PHP versions. Nil leaves the PHP routes answering 503.
	PHP PHPManager
	// PHPReconcile reports how each PHP version came out of the boot reconcile.
	PHPReconcile func() []sites.PHPStep
	// Images carries the nginx image builds. Nil answers the image routes 503.
	Images NginxImages
	// Container starts, stops and restarts nginx. Nil answers the routes 503. The
	// name is from when nginx ran in a container; the routes keep it.
	Container NginxContainer
}

// nginxRoutes registers the routes of development plan step 1.10. They are
// registered even when Deps.Nginx is nil, so the UI gets a 503 that says why
// instead of a 404 that looks like a missing feature.
func (s *server) nginxRoutes(mux *http.ServeMux) {
	h := func(fn func(*NginxDeps, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Nginx == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"nginx is not available on this host; see the daemon log")
				return
			}
			fn(s.deps.Nginx, w, r)
		}))
	}

	mux.Handle("GET /api/sites", h(s.handleListSites))
	mux.Handle("POST /api/sites", h(s.handleCreateSite))
	mux.Handle("GET /api/sites/{domain}", h(s.handleGetSite))
	mux.Handle("PUT /api/sites/{domain}", h(s.handleUpdateSite))
	mux.Handle("DELETE /api/sites/{domain}", h(s.handleDeleteSite))
	mux.Handle("GET /api/sites/{domain}/file", h(s.handleGetSiteFile))
	mux.Handle("PUT /api/sites/{domain}/file", h(s.handlePutSiteFile))
	mux.Handle("POST /api/sites/{domain}/regenerate", h(s.handleRegenerateSite))
	mux.Handle("GET /api/sites/{domain}/waf", h(s.handleGetSiteWAF))
	mux.Handle("PUT /api/sites/{domain}/waf", h(s.handlePutSiteWAF))
	mux.Handle("GET /api/waf/status", h(s.handleWAFStatus))

	mux.Handle("GET /api/nginx/settings", h(s.handleGetNginxSettings))
	mux.Handle("PUT /api/nginx/settings", h(s.handlePutNginxSettings))
	mux.Handle("GET /api/nginx/conf", h(s.handleGetNginxConf))
	mux.Handle("PUT /api/nginx/conf", h(s.handlePutNginxConf))

	mux.Handle("GET /api/config-history/{file}", h(s.handleHistory))
	mux.Handle("GET /api/config-history/{file}/{version}", h(s.handleHistoryVersion))
	mux.Handle("POST /api/config-history/{file}/{version}/restore", h(s.handleHistoryRestore))

	mux.Handle("GET /api/services/nginx", h(s.handleNginxService))

	s.phpRoutes(mux, h)
	s.nginxImageRoutes(mux, h)
}

// applyResponse is what a successful change returns besides its own data.
// Warnings are problems that did not stop the change: it is live, but, say, its
// history entry could not be written.
type applyResponse struct {
	Warnings []string `json:"warnings"`
}

func warningsOf(res confedit.Result) []string {
	if res.Warnings == nil {
		return []string{}
	}
	return res.Warnings
}

// writeNginxError maps an error from the sites, nginx or confedit packages to
// the envelope. A rejected config is a 422 carrying the validator's output
// verbatim; nothing else from outside this package reaches the client.
func (s *server) writeNginxError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		invalid  *sites.ValidationError
		setting  *nginx.SettingsError
		detached *sites.DetachedError
		apply    *confedit.ApplyError
		phpInput *php.InputError
	)
	switch {
	case errors.As(err, &phpInput):
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope{Error: errInvalidInput, Detail: phpInput.Error(), Field: phpInput.Field})
	case errors.Is(err, php.ErrUnknownVersion):
		writeError(w, http.StatusNotFound, errNotFound, "no such PHP version")
	case errors.Is(err, php.ErrNotReady):
		writeError(w, http.StatusConflict, errConflict, "that PHP version is not installed; install it first")
	case errors.Is(err, php.ErrBusy):
		writeError(w, http.StatusConflict, errConflict, "that PHP version is already being installed or built")
	case errors.Is(err, php.ErrInUse):
		writeError(w, http.StatusConflict, errConflict, err.Error())
	case errors.Is(err, images.ErrBusy):
		writeError(w, http.StatusConflict, errConflict, "a build or a swap is already running for this image")
	case errors.Is(err, images.ErrNotReady):
		writeError(w, http.StatusConflict, errConflict, "that build is not ready to serve")
	case errors.Is(err, images.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound, "no such build")
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope{Error: errInvalidInput, Detail: invalid.Error(), Field: invalid.Field})
	case errors.As(err, &setting):
		writeJSON(w, http.StatusUnprocessableEntity, errorEnvelope{Error: errInvalidInput, Detail: setting.Field + ": " + setting.Msg, Field: setting.Field})
	case errors.Is(err, sites.ErrNoWAF):
		writeError(w, http.StatusServiceUnavailable, errUnavailable, "the firewall is not available on this host; see the daemon log")
	case errors.Is(err, sites.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound, "no such site")
	case errors.Is(err, confedit.ErrNoVersion):
		writeError(w, http.StatusNotFound, errNotFound, "no such version")
	case errors.Is(err, sites.ErrConflict):
		writeError(w, http.StatusConflict, errConflict, strings.TrimPrefix(err.Error(), "sites: "))
	case errors.As(err, &detached):
		writeError(w, http.StatusConflict, errDetached, strings.TrimPrefix(detached.Error(), "sites: "))
	case errors.As(err, &apply):
		s.writeApplyError(w, r, apply)
	default:
		s.writeInternal(w, r, err)
	}
}

func (s *server) writeApplyError(w http.ResponseWriter, r *http.Request, ae *confedit.ApplyError) {
	s.logger.WarnContext(r.Context(), "config change not applied",
		"stage", ae.Stage, "service", ae.Service, "error", ae.Err, "rollback_error", ae.RollbackErr,
		"request_id", requestIDFrom(r.Context()))

	env := errorEnvelope{Output: ae.Output}
	status := http.StatusBadGateway
	switch ae.Stage {
	case confedit.StageValidate:
		status = http.StatusUnprocessableEntity
		env.Error = errConfigRejected
		who := "nginx"
		if strings.HasPrefix(ae.Service, "php-") {
			who = "PHP " + strings.TrimPrefix(ae.Service, "php-")
		}
		env.Detail = who + " rejected the configuration; the previous files are back in place"
	case confedit.StagePrepare:
		status = http.StatusBadRequest
		env.Error = errBadRequest
		env.Detail = "the change could not be prepared: " + ae.Err.Error()
	default:
		env.Error = errApplyFailed
		env.Detail = "the change failed at the " + string(ae.Stage) + " stage and was rolled back"
	}
	if ae.RollbackErr != nil {
		status = http.StatusInternalServerError
		env.Error = errApplyFailed
		env.Detail = "the change failed at the " + string(ae.Stage) +
			" stage and restoring the previous files failed too; they will be restored at the next daemon start"
	}
	writeJSON(w, status, env)
}

// confirmed checks the `confirm` query parameter of a destructive action: it
// must repeat the domain, so a mistyped URL or a replayed request for another
// site cannot delete or overwrite this one.
func confirmed(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Query().Get("confirm") != r.PathValue("domain") {
		writeError(w, http.StatusBadRequest, errBadRequest, "confirm must repeat the site's domain")
		return false
	}
	return true
}

// --- sites -------------------------------------------------------------------

func (s *server) handleListSites(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	list, err := n.Sites.List(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	if list == nil {
		list = []sites.Site{}
	}
	writeJSON(w, http.StatusOK, struct {
		Sites []sites.Site `json:"sites"`
	}{list})
}

type createSiteRequest struct {
	Domain      string   `json:"domain"`
	Aliases     []string `json:"aliases"`
	Kind        string   `json:"kind"`
	Root        string   `json:"root"`
	ProxyTarget string   `json:"proxy_target"`
	PHPVersion  string   `json:"php_version"`
	PHPInfo     bool     `json:"phpinfo"`
}

type siteResponse struct {
	Site sites.Site `json:"site"`
	applyResponse
}

func (s *server) handleCreateSite(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	var req createSiteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	res, err := n.Sites.Create(r.Context(), sites.CreateInput{
		Domain: req.Domain, Aliases: req.Aliases, Kind: sites.Kind(req.Kind),
		Root: req.Root, ProxyTarget: req.ProxyTarget,
		PHPVersion: req.PHPVersion, PHPInfo: req.PHPInfo,
	})
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, siteResponse{res.Site, applyResponse{warningsOf(res.Apply)}})
}

func (s *server) handleGetSite(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	d, err := n.Sites.Detail(r.Context(), r.PathValue("domain"))
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// updateSiteRequest uses pointers so an absent field is left alone and an empty
// list clears the aliases.
type updateSiteRequest struct {
	Aliases     *[]string `json:"aliases"`
	ProxyTarget *string   `json:"proxy_target"`
}

func (s *server) handleUpdateSite(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	var req updateSiteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	res, err := n.Sites.Update(r.Context(), r.PathValue("domain"), sites.UpdateInput{Aliases: req.Aliases, ProxyTarget: req.ProxyTarget})
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, siteResponse{res.Site, applyResponse{warningsOf(res.Apply)}})
}

func (s *server) handleDeleteSite(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	if !confirmed(w, r) {
		return
	}
	res, err := n.Sites.Delete(r.Context(), r.PathValue("domain"))
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	warn := warningsOf(res.Apply)
	if s.deps.TLS != nil {
		// The certificate store's rows and files belong to the site: leaving
		// them would keep the renewer ordering for a domain nothing serves.
		if msg := s.deps.TLS.removeSiteCert(r.Context(), r.PathValue("domain")); msg != "" {
			s.logger.WarnContext(r.Context(), msg, "request_id", requestIDFrom(r.Context()))
			warn = append(warn, msg)
		}
	}
	writeJSON(w, http.StatusOK, struct {
		// WebRoot is where the site's content still is: it is kept until the
		// trash arrives with the file manager.
		WebRoot string `json:"web_root"`
		applyResponse
	}{res.WebRoot, applyResponse{warn}})
}

type siteFileResponse struct {
	sites.SiteFile
	// HistoryFile is the key of this file under /api/config-history.
	HistoryFile string `json:"history_file"`
}

func (s *server) handleGetSiteFile(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	f, err := n.Sites.ReadFile(r.Context(), r.PathValue("domain"))
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, siteFileResponse{f, strings.TrimPrefix(f.Path, n.Paths.Dir()+"/")})
}

type contentRequest struct {
	Content string `json:"content"`
}

func (s *server) handlePutSiteFile(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	var req contentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	res, err := n.Sites.WriteFile(r.Context(), r.PathValue("domain"), []byte(req.Content))
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, applyResponse{warningsOf(res)})
}

func (s *server) handleRegenerateSite(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	if !confirmed(w, r) {
		return
	}
	res, err := n.Sites.Regenerate(r.Context(), r.PathValue("domain"))
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, applyResponse{warningsOf(res)})
}

// --- global nginx config -----------------------------------------------------

func (s *server) handleGetNginxSettings(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	st, err := n.Global.Settings(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *server) handlePutNginxSettings(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	var req nginx.HTTPSettings
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	if _, err := n.Global.SetSettings(r.Context(), req); err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	s.handleGetNginxSettings(n, w, r)
}

const nginxConfHistoryFile = "nginx.conf"

func (s *server) handleGetNginxConf(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	content, err := n.Global.ReadConf(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	missing := n.Global.ConfLinks(content)
	if missing == nil {
		missing = []string{}
	}
	writeJSON(w, http.StatusOK, struct {
		Path        string `json:"path"`
		Content     string `json:"content"`
		HistoryFile string `json:"history_file"`
		// MissingLinks names what stopped working because nginx.conf no longer
		// includes the file that carries it.
		MissingLinks []string `json:"missing_links"`
	}{n.Paths.ConfPath(), string(content), nginxConfHistoryFile, missing})
}

func (s *server) handlePutNginxConf(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	var req contentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	res, err := n.Global.WriteConf(r.Context(), []byte(req.Content))
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, applyResponse{warningsOf(res)})
}

// --- history -----------------------------------------------------------------

// siteHistoryFile is `sites/<name>.conf` with exactly one path segment for the
// name, so the key can never walk out of the sites directory.
var siteHistoryFile = regexp.MustCompile(`^sites/[a-z0-9][a-z0-9.-]*\.conf$`)

// historyPath turns the {file} of a history route into the managed path. Only
// the raw files have history routes: nginx.conf and the site files. The
// panel-owned files are rendered from the database, so restoring one from
// history would make the file and the database disagree.
func historyPath(n *NginxDeps, file string) (string, bool) {
	if m := phpHistoryFile.FindStringSubmatch(file); m != nil && n.PHP != nil {
		return n.PHP.Paths().IniPath(m[1]), true
	}
	if file != nginxConfHistoryFile && !siteHistoryFile.MatchString(file) {
		return "", false
	}
	return path.Join(n.Paths.Dir(), file), true
}

// historyServices is what a restore of the file validates and reloads through:
// the PHP version for its php.ini, nginx for everything else.
func historyServices(n *NginxDeps, file string) []confedit.Service {
	if m := phpHistoryFile.FindStringSubmatch(file); m != nil && n.PHP != nil {
		return []confedit.Service{n.PHP.Service(m[1], false)}
	}
	return []confedit.Service{n.Service}
}

func (s *server) historyTarget(n *NginxDeps, w http.ResponseWriter, r *http.Request) (string, bool) {
	p, ok := historyPath(n, r.PathValue("file"))
	if !ok {
		writeError(w, http.StatusNotFound, errNotFound, "no history is kept for that file")
	}
	return p, ok
}

func historyVersion(w http.ResponseWriter, r *http.Request) (int, bool) {
	v, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || v < 1 {
		writeError(w, http.StatusBadRequest, errBadRequest, "version must be a positive number")
		return 0, false
	}
	return v, true
}

func (s *server) handleHistory(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	p, ok := s.historyTarget(n, w, r)
	if !ok {
		return
	}
	versions, err := n.History.History(r.Context(), p)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	if versions == nil {
		versions = []confedit.Version{}
	}
	writeJSON(w, http.StatusOK, struct {
		File     string             `json:"file"`
		Versions []confedit.Version `json:"versions"`
	}{r.PathValue("file"), versions})
}

func (s *server) handleHistoryVersion(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	p, ok := s.historyTarget(n, w, r)
	if !ok {
		return
	}
	v, ok := historyVersion(w, r)
	if !ok {
		return
	}
	content, err := n.History.ReadVersion(r.Context(), p, v)
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Version int    `json:"version"`
		Content string `json:"content"`
	}{v, string(content)})
}

func (s *server) handleHistoryRestore(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	p, ok := s.historyTarget(n, w, r)
	if !ok {
		return
	}
	v, ok := historyVersion(w, r)
	if !ok {
		return
	}
	res, err := n.History.Restore(r.Context(), p, v, historyServices(n, r.PathValue("file")))
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, applyResponse{warningsOf(res)})
}

// --- service status ----------------------------------------------------------

// containerJSON is a service's state as the API reports it. The name is from when
// nginx ran in a container, and PHP-FPM still does; for nginx, Exists means systemd
// knows the unit and Status is its state, "running" while it is active.
type containerJSON struct {
	Exists    bool       `json:"exists"`
	Running   bool       `json:"running"`
	Status    string     `json:"status"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	// Error is set when the service manager could not be asked. It is a fixed
	// sentence: the cause is in the daemon log.
	Error string `json:"error,omitempty"`
}

func (s *server) handleNginxService(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	out := struct {
		Container containerJSON `json:"container"`
		Reconcile sites.Status  `json:"reconcile"`
	}{Reconcile: n.Reconciler.Status()}
	if out.Reconcile.Steps == nil {
		out.Reconcile.Steps = []sites.Step{}
	}

	st, found, err := n.Service.State(r.Context())
	switch {
	case err != nil:
		s.logger.WarnContext(r.Context(), "nginx service state", "error", err)
		out.Container.Error = "systemd did not answer; see the daemon log"
	case found:
		out.Container = containerJSON{Exists: true, Running: st.Active, Status: st.State}
		if st.Active {
			out.Container.Status = "running"
		}
	default:
		out.Container.Status = "missing"
	}
	writeJSON(w, http.StatusOK, out)
}
