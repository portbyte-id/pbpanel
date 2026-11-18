package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/sites"
	"github.com/portbyte-id/server-panel/internal/waf"
)

// newWAFHarness is the nginx harness with the firewall wired into the sites
// manager. available says whether the nginx build in use has ModSecurity.
func newWAFHarness(t *testing.T, available bool) *nginxHarness {
	t.Helper()
	h := newHarness(t)
	paths := nginx.Paths{Root: "/www"}
	mfs := &memFS{files: map[string][]byte{}, dirs: map[string]bool{}}
	ap := &memApplier{fs: mfs}
	svc := &fakeNginx{status: &nginx.Status{Loaded: true, Active: true, State: "active"}}
	hist := &fakeHistory{versions: map[string][]confedit.Version{}, content: map[string]string{}}

	engine := waf.New(h.store, paths, func(context.Context) (waf.Availability, error) {
		if available {
			return waf.Availability{Available: true, Flavor: "pqc", CRSVersion: "4.30.0"}, nil
		}
		return waf.Availability{Flavor: "standard", Reason: "this build has no ModSecurity"}, nil
	})
	mgr, err := sites.New(sites.Options{Store: h.store, Applier: ap, Service: svc, Paths: paths, FS: mfs, WAF: engine})
	if err != nil {
		t.Fatal(err)
	}
	h.deps.Nginx = &NginxDeps{
		Sites:   mgr,
		Global:  nginx.NewGlobal(h.store, ap, svc, paths).ReadWith(mfs.ReadFile),
		History: hist, Service: svc, Paths: paths,
		Reconciler: fakeReconcile{sites.Status{Done: true, OK: true}},
	}
	h.handler, err = New(h.deps)
	if err != nil {
		t.Fatal(err)
	}
	return &nginxHarness{harness: h, s: h.login(), fs: mfs, applier: ap, svc: svc, history: hist}
}

func TestSiteWAFRoundTrip(t *testing.T) {
	h := newWAFHarness(t, true)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})

	code, out := h.call(http.MethodGet, "/api/sites/a.test/waf", nil)
	if code != 200 {
		t.Fatalf("GET = %d %v", code, out)
	}
	cfg := out["config"].(map[string]any)
	st := out["status"].(map[string]any)
	if cfg["mode"] != "off" || cfg["paranoia"] != float64(1) {
		t.Errorf("a site with no settings = %v, want the defaults", cfg)
	}
	for _, k := range []string{"path_blocklist", "exclusions"} {
		if _, ok := cfg[k].([]any); !ok {
			t.Errorf("config.%s must be a JSON array, got %v", k, cfg[k])
		}
	}
	if st["available"] != true || st["flavor"] != "pqc" || st["attached"] != true || st["crs_version"] != "4.30.0" {
		t.Errorf("status = %v", st)
	}

	body := map[string]any{
		"mode": "block", "paranoia": 3,
		"rate_limit":     map[string]any{"enabled": true, "requests_per_second": 5, "burst": 10},
		"path_blocklist": []string{"/wp-admin"},
		"ip_rules":       map[string]any{"allow": []string{}, "deny": []string{"203.0.113.9"}},
		"exclusions":     []map[string]any{{"kind": "rule_arg", "rule_id": 942200, "arg": "password", "note": "login"}},
	}
	code, out = h.call(http.MethodPut, "/api/sites/a.test/waf", body)
	if code != 200 {
		t.Fatalf("PUT = %d %v", code, out)
	}
	if _, ok := out["warnings"].([]any); !ok {
		t.Errorf("warnings must be an array: %v", out)
	}
	if got := out["config"].(map[string]any)["ip_rules"].(map[string]any)["deny"].([]any); got[0] != "203.0.113.9/32" {
		t.Errorf("addresses are stored as masked prefixes, got %v", got)
	}
	if _, ok := h.fs.files["/www/panel/nginx/waf/rules/a.test.conf"]; !ok {
		t.Error("the rules file was not written")
	}

	code, out = h.call(http.MethodGet, "/api/sites/a.test/waf", nil)
	if code != 200 || out["config"].(map[string]any)["mode"] != "block" {
		t.Errorf("GET after PUT = %d %v", code, out)
	}

	code, out = h.call(http.MethodGet, "/api/waf/status", nil)
	if code != 200 || out["available"] != true {
		t.Fatalf("status = %d %v", code, out)
	}
	rows := out["sites"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["mode"] != "block" || rows[0].(map[string]any)["exclusions"] != float64(1) {
		t.Errorf("overview rows = %v", rows)
	}
}

func TestSiteWAFErrors(t *testing.T) {
	h := newWAFHarness(t, false)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})

	// ModSecurity on a build without it: a field error that says why.
	rec := h.do(http.MethodPut, "/api/sites/a.test/waf", h.s.cookie, h.s.csrf, map[string]any{"mode": "block"})
	env := h.errorEnvelopeOf(rec)
	if rec.Code != http.StatusUnprocessableEntity || env.Error != errInvalidInput || env.Field != "mode" {
		t.Errorf("unavailable module: %d %+v", rec.Code, env)
	}

	// A bad value names its field.
	rec = h.do(http.MethodPut, "/api/sites/a.test/waf", h.s.cookie, h.s.csrf, map[string]any{"path_blocklist": []string{"/a;b"}})
	if env = h.errorEnvelopeOf(rec); rec.Code != http.StatusUnprocessableEntity || env.Field != "path_blocklist" {
		t.Errorf("bad path: %d %+v", rec.Code, env)
	}

	// Unknown fields and unknown sites.
	if rec = h.do(http.MethodPut, "/api/sites/a.test/waf", h.s.cookie, h.s.csrf, map[string]any{"bogus": 1}); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", rec.Code)
	}
	if code, _ := h.call(http.MethodGet, "/api/sites/none.test/waf", nil); code != http.StatusNotFound {
		t.Errorf("unknown site: %d", code)
	}
}

func TestSiteWAFRejectedByNginxIsRolledBack(t *testing.T) {
	h := newWAFHarness(t, true)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})

	const out = "nginx: [emerg] \"modsecurity_rules_file\" directive Rules error. File: /www/panel/nginx/waf/rules/a.test.conf\nnginx: configuration file /www/panel/nginx/nginx.conf test failed\n"
	h.applier.err = rejected(out)
	rec := h.do(http.MethodPut, "/api/sites/a.test/waf", h.s.cookie, h.s.csrf, map[string]any{"mode": "block"})
	env := h.errorEnvelopeOf(rec)
	if rec.Code != http.StatusUnprocessableEntity || env.Error != errConfigRejected || env.Output != out {
		t.Fatalf("rejected change: %d %+v, want 422 config_rejected with nginx's text", rec.Code, env)
	}

	h.applier.err = nil
	_, got := h.call(http.MethodGet, "/api/sites/a.test/waf", nil)
	if got["config"].(map[string]any)["mode"] != "off" {
		t.Errorf("the rejected settings were kept: %v", got["config"])
	}
}

func TestSiteWAFAnswers503WithoutTheFirewall(t *testing.T) {
	h := newNginxHarness(t) // sites manager with no firewall engine
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	for _, p := range []string{"/api/sites/a.test/waf", "/api/waf/status"} {
		rec := h.do(http.MethodGet, p, h.s.cookie, h.s.csrf, nil)
		if rec.Code != http.StatusServiceUnavailable || h.errorEnvelopeOf(rec).Error != errUnavailable {
			t.Errorf("GET %s = %d", p, rec.Code)
		}
	}
}
