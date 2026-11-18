package httpapi

import (
	"net/http"

	"github.com/portbyte-id/server-panel/internal/sites"
	"github.com/portbyte-id/server-panel/internal/waf"
)

// wafResponse is a site's firewall settings and what the host can do with them.
type wafResponse struct {
	Config waf.Config      `json:"config"`
	Status sites.WAFStatus `json:"status"`
}

// wafPutResponse adds the warnings every applied change carries.
type wafPutResponse struct {
	wafResponse
	applyResponse
}

func (s *server) handleGetSiteWAF(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	c, st, err := n.Sites.WAFConfig(r.Context(), r.PathValue("domain"))
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wafResponse{c, st})
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
	writeJSON(w, http.StatusOK, wafPutResponse{wafResponse{res.Config, res.Status}, applyResponse{warningsOf(res.Apply)}})
}

func (s *server) handleWAFStatus(n *NginxDeps, w http.ResponseWriter, r *http.Request) {
	o, err := n.Sites.WAFOverview(r.Context())
	if err != nil {
		s.writeNginxError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}
