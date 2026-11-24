package httpapi

import (
	"context"
	"net/http"
	"net/netip"
	"sort"
	"time"

	"github.com/portbyte-id/server-panel/internal/geoip"
	"github.com/portbyte-id/server-panel/internal/wafmon"
)

// GeoSource is the country database the world map reads. Nil leaves the map
// empty and the download answering 503.
type GeoSource interface {
	Country(addr netip.Addr) (string, bool)
	Status() geoip.Status
	Fetch(ctx context.Context) (geoip.Status, error)
}

// wafSourcesTop is how many single addresses the sources view lists.
const wafSourcesTop = 20

// wafCountryRow is one country on the map.
type wafCountryRow struct {
	Code      string `json:"code"`
	Requests  int    `json:"requests"`
	Blocked   int    `json:"blocked"`
	Addresses int    `json:"addresses"`
}

// wafSourceRow is one address in the sources table.
type wafSourceRow struct {
	IP       string `json:"ip"`
	Country  string `json:"country"`
	Requests int    `json:"requests"`
	Errors   int    `json:"errors"`
	Blocked  int    `json:"blocked"`
}

// wafSourcesResponse is where the traffic of a window comes from.
type wafSourcesResponse struct {
	Range    string       `json:"range"`
	Geo      geoip.Status `json:"geo"`
	Requests int          `json:"requests"`
	Blocked  int          `json:"blocked"`
	// Unlocated is the requests from addresses no country covers, such as
	// private networks or addresses the database does not list.
	Unlocated int `json:"unlocated"`
	// Untracked is the requests the monitor kept no address for: the buckets keep
	// the busiest addresses only.
	Untracked int             `json:"untracked"`
	Countries []wafCountryRow `json:"countries"`
	TopIPs    []wafSourceRow  `json:"top_ips"`
}

// wafGeoRoutes registers the world map's routes. They answer even without a
// country database, so the page can say what to do.
func (s *server) wafGeoRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/waf/sources", s.requireAuth(http.HandlerFunc(s.handleWAFSources)))
	mux.Handle("GET /api/waf/geo", s.requireAuth(http.HandlerFunc(s.handleWAFGeoStatus)))
	mux.Handle("POST /api/waf/geo/download", s.requireAuth(http.HandlerFunc(s.handleWAFGeoDownload)))
}

func (s *server) handleWAFGeoStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.geoStatus())
}

// geoStatus is the database's status with the credit the DB-IP license asks for,
// which the page always shows, whether or not a database is installed.
func (s *server) geoStatus() geoip.Status {
	var st geoip.Status
	if s.deps.Geo != nil {
		st = s.deps.Geo.Status()
	}
	st.Attribution, st.AttributionURL, st.LicenseURL = geoip.Attribution, geoip.AttributionURL, geoip.LicenseURL
	return st
}

// handleWAFGeoDownload fetches the monthly country database. It runs in the
// request: the file is a few megabytes, and the answer says whether it worked.
func (s *server) handleWAFGeoDownload(w http.ResponseWriter, r *http.Request) {
	if s.deps.Geo == nil {
		writeError(w, http.StatusServiceUnavailable, errUnavailable, "the country database is not available on this panel")
		return
	}
	st, err := s.deps.Geo.Fetch(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, errUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleWAFSources answers where the requests of a window came from, by country
// and by address. A blocked request is counted from the WAF events the monitor
// holds, so a window older than the events it keeps shows fewer blocks.
func (s *server) handleWAFSources(w http.ResponseWriter, r *http.Request) {
	if s.deps.WAFMon == nil {
		writeError(w, http.StatusServiceUnavailable, errUnavailable,
			"traffic monitoring is not available; see the daemon log")
		return
	}
	p := newWAFParams(w, r)
	window := p.window()
	if p.done {
		return
	}
	label := p.get("range")
	if label == "" {
		label = "15m"
	}

	counts := s.deps.WAFMon.SourceCountsOver(window)

	blockedBy := map[string]int{}
	cutoff := time.Now().Add(-window)
	for _, e := range s.deps.WAFMon.Matching(wafmon.EventQuery{Action: "blocked"}) {
		if e.Time.Before(cutoff) {
			break
		}
		blockedBy[e.IP]++
	}

	out := wafSourcesResponse{Range: label, Geo: s.geoStatus(), Requests: counts.Requests, Countries: []wafCountryRow{}, TopIPs: []wafSourceRow{}}
	byCountry := map[string]*wafCountryRow{}
	tracked := 0
	for _, row := range counts.IPs {
		tracked += row.Requests
		blocked := blockedBy[row.IP]
		out.Blocked += blocked
		code := ""
		if s.deps.Geo != nil {
			if a, err := netip.ParseAddr(row.IP); err == nil {
				if cc, ok := s.deps.Geo.Country(a); ok {
					code = cc
				}
			}
		}
		if code == "" {
			out.Unlocated += row.Requests
		} else {
			c := byCountry[code]
			if c == nil {
				c = &wafCountryRow{Code: code}
				byCountry[code] = c
			}
			c.Requests += row.Requests
			c.Blocked += blocked
			c.Addresses++
		}
		if len(out.TopIPs) < wafSourcesTop {
			out.TopIPs = append(out.TopIPs, wafSourceRow{
				IP: row.IP, Country: code, Requests: row.Requests, Errors: row.Errors, Blocked: blocked,
			})
		}
	}
	out.Untracked = max(counts.Requests-tracked, 0)

	for _, c := range byCountry {
		out.Countries = append(out.Countries, *c)
	}
	sort.Slice(out.Countries, func(i, j int) bool {
		a, b := out.Countries[i], out.Countries[j]
		return a.Requests > b.Requests || (a.Requests == b.Requests && a.Code < b.Code)
	})
	writeJSON(w, http.StatusOK, out)
}
