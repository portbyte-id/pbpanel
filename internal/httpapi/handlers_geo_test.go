package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"testing"

	"github.com/portbyte-id/server-panel/internal/geoip"
)

// fakeGeo answers for one address and records downloads.
type fakeGeo struct {
	st      geoip.Status
	fetches int
	err     error
}

func (g *fakeGeo) Country(a netip.Addr) (string, bool) {
	if a.String() == "198.51.100.4" {
		return "US", true
	}
	return "", false
}

func (g *fakeGeo) Status() geoip.Status { return g.st }

func (g *fakeGeo) Fetch(_ context.Context) (geoip.Status, error) {
	g.fetches++
	if g.err != nil {
		return g.st, g.err
	}
	g.st.Installed, g.st.Month, g.st.Entries = true, "2026-11", 4
	return g.st, nil
}

func newGeoHarness(t *testing.T, geo GeoSource) (*harness, session) {
	t.Helper()
	h := newHarness(t)
	h.deps.WAFMon = &fakeWAFMon{}
	h.deps.Geo = geo
	handler, err := New(h.deps)
	if err != nil {
		t.Fatal(err)
	}
	h.handler = handler
	return h, h.login()
}

func TestWAFSourcesGroupsByCountry(t *testing.T) {
	h, s := newGeoHarness(t, &fakeGeo{})
	rec := h.do(http.MethodGet, "/api/waf/sources?range=15m", s.cookie, s.csrf, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out wafSourcesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Requests != 12 || out.Unlocated != 3 || out.Untracked != 0 {
		t.Fatalf("totals = requests %d unlocated %d untracked %d, want 12, 3, 0", out.Requests, out.Unlocated, out.Untracked)
	}
	if len(out.Countries) != 1 || out.Countries[0].Code != "US" || out.Countries[0].Requests != 9 || out.Countries[0].Addresses != 1 {
		t.Fatalf("countries = %+v, want one US row with 9 requests", out.Countries)
	}
	if len(out.TopIPs) != 2 || out.TopIPs[0].IP != "198.51.100.4" || out.TopIPs[0].Country != "US" {
		t.Fatalf("top ips = %+v", out.TopIPs)
	}
	if out.Range != "15m" {
		t.Fatalf("range = %q", out.Range)
	}
}

func TestWAFSourcesRejectsBadRange(t *testing.T) {
	h, s := newGeoHarness(t, &fakeGeo{})
	if rec := h.do(http.MethodGet, "/api/waf/sources?range=2d", s.cookie, s.csrf, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("range=2d answered %d, want 400", rec.Code)
	}
}

func TestWAFGeoStatusAndDownload(t *testing.T) {
	geo := &fakeGeo{}
	h, s := newGeoHarness(t, geo)

	rec := h.do(http.MethodGet, "/api/waf/geo", s.cookie, s.csrf, nil)
	var st geoip.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d %v", rec.Code, err)
	}
	if st.Installed || st.Attribution != geoip.Attribution || st.AttributionURL == "" || st.LicenseURL == "" {
		t.Fatalf("status = %+v, want not installed with the DB-IP credit", st)
	}

	rec = h.do(http.MethodPost, "/api/waf/geo/download", s.cookie, s.csrf, nil)
	if rec.Code != http.StatusOK || geo.fetches != 1 {
		t.Fatalf("download answered %d after %d fetches: %s", rec.Code, geo.fetches, rec.Body.String())
	}
}

func TestWAFGeoDownloadFailureIsBadGateway(t *testing.T) {
	h, s := newGeoHarness(t, &fakeGeo{err: errors.New("no route to host")})
	if rec := h.do(http.MethodPost, "/api/waf/geo/download", s.cookie, s.csrf, nil); rec.Code != http.StatusBadGateway {
		t.Fatalf("failed download answered %d, want 502", rec.Code)
	}
}

func TestWAFGeoWithoutDatabaseAnswers503ForDownload(t *testing.T) {
	h, s := newGeoHarness(t, nil)
	if rec := h.do(http.MethodPost, "/api/waf/geo/download", s.cookie, s.csrf, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("download without a database answered %d, want 503", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/api/waf/geo", s.cookie, s.csrf, nil); rec.Code != http.StatusOK {
		t.Fatalf("status without a database answered %d", rec.Code)
	}
}
