package httpapi

import (
	"context"
	"net/http"

	"github.com/portbyte-id/server-panel/internal/appsec"
	"github.com/portbyte-id/server-panel/internal/sites"
	"github.com/portbyte-id/server-panel/internal/waf"
)

// wafResponse is a site's firewall settings and what the host can do with them.
type wafResponse struct {
	Config waf.Config      `json:"config"`
	Status sites.WAFStatus `json:"status"`
	// OpenAppsec is the second engine's state: whether a site may choose it, the
	// agent's version, and the warning text the interface renders verbatim. It is
	// omitted on a panel with no appsec package wired in.
	OpenAppsec *appsec.Availability `json:"open_appsec,omitempty"`
}

// wafPutResponse adds the warnings every applied change carries.
type wafPutResponse struct {
	wafResponse
	applyResponse
}

// appsecAvailability is the open-appsec state the firewall JSON carries, or nil
// when the package is not wired in. A read that fails is left out rather than
// failing the whole firewall view: the engine's state is secondary to it.
func (s *server) appsecAvailability(ctx context.Context) *appsec.Availability {
	if s.deps.OpenAppsec == nil {
		return nil
	}
	a := s.deps.OpenAppsec(ctx)
	return &a
}

func (s *server) handleGetSiteWAF(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	c, st, err := n.Sites.WAFConfig(r.Context(), r.PathValue("domain"))
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wafResponse{Config: c, Status: st, OpenAppsec: s.appsecAvailability(r.Context())})
}

// handlePutSiteWAF replaces a site's firewall settings. The change goes through
// confedit, so `nginx -t` runs before nginx reloads and a rejection comes back
// with the validator's output and leaves the old files and settings in place.
func (s *server) handlePutSiteWAF(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	var req waf.Config
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	res, err := n.Sites.SetWAF(r.Context(), r.PathValue("domain"), req)
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wafPutResponse{
		wafResponse{Config: res.Config, Status: res.Status, OpenAppsec: s.appsecAvailability(r.Context())},
		applyResponse{warningsOf(res.Apply)},
	})
}

// wafOverviewResponse is every site's firewall, whether ModSecurity can be used,
// and the second engine's state.
type wafOverviewResponse struct {
	sites.WAFOverview
	// OpenAppsec carries the engine's availability and its warning text, the same
	// value a site's firewall view and the Store card carry.
	OpenAppsec *appsec.Availability `json:"open_appsec,omitempty"`
}

func (s *server) handleWAFStatus(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	o, err := n.Sites.WAFOverview(r.Context())
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wafOverviewResponse{WAFOverview: o, OpenAppsec: s.appsecAvailability(r.Context())})
}

// wafGlobalResponse is the global firewall, the presets the bot switches stand
// for, and the page a blocked visitor gets until the operator writes one.
type wafGlobalResponse struct {
	Global      waf.Global          `json:"global"`
	DefaultHTML string              `json:"default_html"`
	Presets     map[string][]string `json:"presets"`
}

// wafGlobalPutResponse adds the warnings every applied change carries.
type wafGlobalPutResponse struct {
	wafGlobalResponse
	applyResponse
}

func wafGlobalView(g waf.Global) wafGlobalResponse {
	return wafGlobalResponse{
		Global:      g,
		DefaultHTML: waf.DefaultErrorPageHTML,
		Presets: map[string][]string{
			"scanners":     waf.Scanners,
			"ai_crawlers":  waf.AICrawlers,
			"seo_crawlers": waf.SEOCrawlers,
		},
	}
}

func (s *server) handleGetWAFGlobal(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	g, err := n.Sites.WAFGlobal(r.Context())
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wafGlobalView(g))
}

// handlePutWAFGlobal replaces the global firewall. Every site's block is
// re-rendered in the same confedit apply, so a rejection leaves all of them as
// they were.
func (s *server) handlePutWAFGlobal(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	var req waf.Global
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	res, err := n.Sites.SetWAFGlobal(r.Context(), req)
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wafGlobalPutResponse{wafGlobalView(res.Global), applyResponse{warningsOf(res.Apply)}})
}
