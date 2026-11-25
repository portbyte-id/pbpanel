package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/portbyte-id/server-panel/internal/metrics"
)

// fakeAlerts is an in-memory rule list that runs the real validation, so the 422
// path is the one production uses.
type fakeAlerts struct {
	rules  []metrics.Rule
	nextID int64
}

func newFakeAlerts() *fakeAlerts {
	return &fakeAlerts{nextID: 2, rules: []metrics.Rule{{
		ID: 1, Kind: metrics.RuleDiskPct, Target: "*", Threshold: 85, ClearThreshold: 80,
		ForSeconds: 300, ClearForSeconds: 300, Severity: "warning", Enabled: true, Builtin: true,
	}}}
}

func (f *fakeAlerts) List(context.Context) ([]metrics.RuleView, error) {
	out := []metrics.RuleView{}
	for _, r := range f.rules {
		out = append(out, metrics.RuleView{Rule: r, States: []metrics.State{}})
	}
	return out, nil
}

func (f *fakeAlerts) find(id int64) int {
	for i, r := range f.rules {
		if r.ID == id {
			return i
		}
	}
	return -1
}

func (f *fakeAlerts) Get(_ context.Context, id int64) (metrics.Rule, error) {
	if i := f.find(id); i >= 0 {
		return f.rules[i], nil
	}
	return metrics.Rule{}, metrics.ErrRuleNotFound
}

func (f *fakeAlerts) Create(_ context.Context, r metrics.Rule) (metrics.Rule, error) {
	if err := metrics.ValidateRule(r); err != nil {
		return metrics.Rule{}, err
	}
	r.ID, f.nextID = f.nextID, f.nextID+1
	f.rules = append(f.rules, r)
	return r, nil
}

func (f *fakeAlerts) Update(_ context.Context, id int64, r metrics.Rule) (metrics.Rule, error) {
	i := f.find(id)
	if i < 0 {
		return metrics.Rule{}, metrics.ErrRuleNotFound
	}
	if f.rules[i].Builtin {
		r.Kind, r.Target = f.rules[i].Kind, f.rules[i].Target
	}
	if err := metrics.ValidateRule(r); err != nil {
		return metrics.Rule{}, err
	}
	r.ID, r.Builtin = id, f.rules[i].Builtin
	f.rules[i] = r
	return r, nil
}

func (f *fakeAlerts) Delete(_ context.Context, id int64) error {
	i := f.find(id)
	switch {
	case i < 0:
		return metrics.ErrRuleNotFound
	case f.rules[i].Builtin:
		return metrics.ErrBuiltinRule
	}
	f.rules = append(f.rules[:i], f.rules[i+1:]...)
	return nil
}

var alertPaths = []struct{ method, path string }{
	{http.MethodGet, "/api/alerts"},
	{http.MethodPost, "/api/alerts/rules"},
	{http.MethodPut, "/api/alerts/rules/1"},
	{http.MethodDelete, "/api/alerts/rules/1"},
}

func TestAlertRoutesRequireSessionAnd503WithoutService(t *testing.T) {
	h := newHarness(t)
	for _, c := range alertPaths {
		if rec := h.do(c.method, c.path, "", "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want 401", c.method, c.path, rec.Code)
		}
	}
	s := h.login()
	for _, c := range alertPaths {
		if rec := h.do(c.method, c.path, s.cookie, s.csrf, nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", c.method, c.path, rec.Code)
		}
	}
}

func TestAlertsList(t *testing.T) {
	h, s := m7Harness(t, func(d *Deps) { d.Alerts = newFakeAlerts() })
	rec := h.do(http.MethodGet, "/api/alerts", s.cookie, "", nil)
	var got struct {
		Rules []struct {
			ID      int64   `json:"id"`
			Kind    string  `json:"kind"`
			Builtin bool    `json:"builtin"`
			States  []any   `json:"states"`
			Thr     float64 `json:"threshold"`
		} `json:"rules"`
		Kinds []string `json:"kinds"`
	}
	h.decode(rec, &got)
	if rec.Code != http.StatusOK || len(got.Rules) != 1 || got.Rules[0].Kind != "disk_pct" || !got.Rules[0].Builtin ||
		got.Rules[0].States == nil || got.Rules[0].Thr != 85 || len(got.Kinds) != 8 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestAlertCreateValidates(t *testing.T) {
	f := newFakeAlerts()
	h, s := m7Harness(t, func(d *Deps) { d.Alerts = f })

	rec := h.do(http.MethodPost, "/api/alerts/rules", s.cookie, s.csrf, map[string]any{
		"kind": "mem_pct", "threshold": 92, "clear_threshold": 85, "severity": "critical",
	})
	if rec.Code != http.StatusCreated || len(f.rules) != 2 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	created := f.rules[1]
	if !created.Enabled || created.ForSeconds != 300 || created.Target != "*" || created.Builtin {
		t.Errorf("defaults not applied: %+v", created)
	}

	bad := []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"unknown kind", map[string]any{"kind": "nope", "threshold": 1, "clear_threshold": 1, "severity": "info"}, "kind"},
		{"threshold over the maximum", map[string]any{"kind": "disk_pct", "threshold": 150, "clear_threshold": 80, "severity": "info"}, "threshold"},
		{"clear above threshold", map[string]any{"kind": "disk_pct", "threshold": 80, "clear_threshold": 90, "severity": "info"}, "clear_threshold"},
		{"bad severity", map[string]any{"kind": "disk_pct", "threshold": 90, "clear_threshold": 80, "severity": "dire"}, "severity"},
	}
	for _, c := range bad {
		rec := h.do(http.MethodPost, "/api/alerts/rules", s.cookie, s.csrf, c.body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d, want 422", c.name, rec.Code)
			continue
		}
		if env := h.errorEnvelopeOf(rec); env.Error != "invalid_input" || env.Field != c.field {
			t.Errorf("%s: envelope %+v, want field %s", c.name, env, c.field)
		}
	}
	if len(f.rules) != 2 {
		t.Errorf("a rejected rule was stored: %d rules", len(f.rules))
	}
	if rec := h.doRaw(http.MethodPost, "/api/alerts/rules", s.cookie, s.csrf, `{"kind":`); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed JSON = %d, want 400", rec.Code)
	}
	if rec := h.doRaw(http.MethodPost, "/api/alerts/rules", s.cookie, s.csrf, `{"kind":"disk_pct","bogus":1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400", rec.Code)
	}
}

func TestAlertUpdateMergesOntoTheStoredRule(t *testing.T) {
	f := newFakeAlerts()
	h, s := m7Harness(t, func(d *Deps) { d.Alerts = f })

	// Only the switch is sent; every number stays.
	rec := h.do(http.MethodPut, "/api/alerts/rules/1", s.cookie, s.csrf, map[string]any{"enabled": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
	}
	if r := f.rules[0]; r.Enabled || r.Threshold != 85 || r.ClearThreshold != 80 || r.ForSeconds != 300 || !r.Builtin {
		t.Errorf("rule after a partial update = %+v", r)
	}
	// A built-in rule keeps its kind whatever the body says.
	h.do(http.MethodPut, "/api/alerts/rules/1", s.cookie, s.csrf, map[string]any{"kind": "mem_pct", "threshold": 88})
	if r := f.rules[0]; r.Kind != "disk_pct" || r.Threshold != 88 {
		t.Errorf("builtin after changing its kind = %+v", r)
	}
	// Breaking the hysteresis is a 422 and changes nothing.
	rec = h.do(http.MethodPut, "/api/alerts/rules/1", s.cookie, s.csrf, map[string]any{"clear_threshold": 99})
	if rec.Code != http.StatusUnprocessableEntity || f.rules[0].ClearThreshold != 80 {
		t.Errorf("bad update = %d, clear %v", rec.Code, f.rules[0].ClearThreshold)
	}
	for _, id := range []string{"99", "0", "abc", "-3"} {
		if rec := h.do(http.MethodPut, "/api/alerts/rules/"+id, s.cookie, s.csrf, map[string]any{"enabled": true}); rec.Code != http.StatusNotFound {
			t.Errorf("PUT rule %s = %d, want 404", id, rec.Code)
		}
	}
}

func TestAlertDelete(t *testing.T) {
	f := newFakeAlerts()
	h, s := m7Harness(t, func(d *Deps) { d.Alerts = f })
	h.do(http.MethodPost, "/api/alerts/rules", s.cookie, s.csrf, map[string]any{
		"kind": "cpu_pct", "threshold": 95, "clear_threshold": 80, "severity": "warning",
	})
	if rec := h.do(http.MethodDelete, "/api/alerts/rules/1", s.cookie, s.csrf, nil); rec.Code != http.StatusConflict {
		t.Errorf("deleting a built-in rule = %d, want 409", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/api/alerts/rules/2", s.cookie, s.csrf, nil); rec.Code != http.StatusNoContent || len(f.rules) != 1 {
		t.Errorf("delete = %d, %d rules left", rec.Code, len(f.rules))
	}
	if rec := h.do(http.MethodDelete, "/api/alerts/rules/2", s.cookie, s.csrf, nil); rec.Code != http.StatusNotFound {
		t.Errorf("deleting twice = %d, want 404", rec.Code)
	}
}
