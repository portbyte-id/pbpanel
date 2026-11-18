package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/firewall"
)

// fakeFirewall answers every call from its fields, so a test chooses the error a
// route has to map.
type fakeFirewall struct {
	err     error
	pending firewall.Pending
	gotRule firewall.Rule
	gotID   int64
	token   string
	calls   []string
}

func (f *fakeFirewall) rec(n string) { f.calls = append(f.calls, n) }

func (f *fakeFirewall) State(context.Context) (firewall.State, error) {
	f.rec("state")
	return firewall.State{Enabled: true, SSHPorts: []int{22}, PanelPort: 8443, DockerPorts: []firewall.PublishedPort{}}, f.err
}
func (f *fakeFirewall) Rules(context.Context) ([]firewall.Rule, error) {
	return []firewall.Rule{{ID: 1, PortStart: 80, PortEnd: 80, Protocol: firewall.ProtoTCP, Enabled: true}}, f.err
}
func (f *fakeFirewall) AddRule(_ context.Context, r firewall.Rule) (firewall.Rule, error) {
	f.gotRule = r
	r.ID = 7
	return r, f.err
}
func (f *fakeFirewall) UpdateRule(_ context.Context, id int64, r firewall.Rule) (firewall.Rule, error) {
	f.gotID, f.gotRule = id, r
	r.ID = id
	return r, f.err
}
func (f *fakeFirewall) DeleteRule(_ context.Context, id int64) error { f.gotID = id; return f.err }
func (f *fakeFirewall) Blocklist(context.Context) ([]firewall.BlockEntry, error) {
	return []firewall.BlockEntry{{ID: 1, Addr: "203.0.113.0/24"}}, f.err
}
func (f *fakeFirewall) AddBlock(_ context.Context, e firewall.BlockEntry) (firewall.BlockEntry, error) {
	e.ID = 3
	return e, f.err
}
func (f *fakeFirewall) DeleteBlock(_ context.Context, id int64) error { f.gotID = id; return f.err }
func (f *fakeFirewall) Allowlist(context.Context) ([]firewall.AllowEntry, error) {
	return []firewall.AllowEntry{{ID: 1, Addr: "198.51.100.9/32"}}, f.err
}
func (f *fakeFirewall) AddAllow(_ context.Context, e firewall.AllowEntry) (firewall.AllowEntry, error) {
	e.ID = 4
	return e, f.err
}
func (f *fakeFirewall) DeleteAllow(_ context.Context, id int64) error { f.gotID = id; return f.err }
func (f *fakeFirewall) Enable(context.Context) (firewall.Pending, error) {
	f.rec("enable")
	return f.pending, f.err
}
func (f *fakeFirewall) Apply(context.Context) (firewall.Pending, error) {
	f.rec("apply")
	return f.pending, f.err
}
func (f *fakeFirewall) Confirm(_ context.Context, token string) error {
	f.rec("confirm")
	f.token = token
	return f.err
}
func (f *fakeFirewall) Revert(context.Context) error  { f.rec("revert"); return f.err }
func (f *fakeFirewall) Disable(context.Context) error { f.rec("disable"); return f.err }

func firewallHarness(t *testing.T, f FirewallService) (*harness, session) {
	t.Helper()
	h := newHarness(t)
	if f != nil {
		h.deps.Firewall = f
		var err error
		if h.handler, err = New(h.deps); err != nil {
			t.Fatal(err)
		}
	}
	return h, h.login()
}

func TestFirewallRoutesUnavailableWithoutService(t *testing.T) {
	h, s := firewallHarness(t, nil)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/firewall"},
		{http.MethodGet, "/api/firewall/rules"},
		{http.MethodPost, "/api/firewall/enable"},
	} {
		rec := h.do(c.method, c.path, s.cookie, s.csrf, nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", c.method, c.path, rec.Code)
		}
	}
}

func TestFirewallRoutesNeedLogin(t *testing.T) {
	h, _ := firewallHarness(t, &fakeFirewall{})
	if rec := h.do(http.MethodGet, "/api/firewall", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestFirewallRulesRoundTrip(t *testing.T) {
	f := &fakeFirewall{}
	h, s := firewallHarness(t, f)

	rec := h.do(http.MethodPost, "/api/firewall/rules", s.cookie, s.csrf,
		map[string]any{"portStart": 443, "portEnd": 443, "protocol": "both", "enabled": true, "comment": "https"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if f.gotRule.PortStart != 443 || f.gotRule.Protocol != firewall.ProtoBoth || f.gotRule.Comment != "https" {
		t.Fatalf("decoded wrong: %+v", f.gotRule)
	}
	var got firewall.Rule
	h.decode(rec, &got)
	if got.ID != 7 {
		t.Fatalf("response: %+v", got)
	}

	if rec := h.do(http.MethodPut, "/api/firewall/rules/5", s.cookie, s.csrf,
		map[string]any{"portStart": 1, "portEnd": 2, "protocol": "tcp", "enabled": false}); rec.Code != http.StatusOK || f.gotID != 5 {
		t.Fatalf("update: %d id=%d", rec.Code, f.gotID)
	}
	if rec := h.do(http.MethodDelete, "/api/firewall/rules/5", s.cookie, s.csrf, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/api/firewall/rules/abc", s.cookie, s.csrf, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/api/firewall/rules", s.cookie, s.csrf, nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if rec := h.doRaw(http.MethodPost, "/api/firewall/rules", s.cookie, s.csrf, `{"portStart":1,"nope":2}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
}

func TestFirewallErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		class  string
		field  string
		method string
		path   string
	}{
		{"port", firewall.ErrPort, 400, errInvalidRule, "portStart", "POST", "/api/firewall/rules"},
		{"range order", firewall.ErrPortOrd, 400, errInvalidRule, "portStart", "POST", "/api/firewall/rules"},
		{"protocol", firewall.ErrProto, 400, errInvalidRule, "protocol", "POST", "/api/firewall/rules"},
		{"rule source", firewall.ErrAddr, 400, errInvalidRule, "source", "POST", "/api/firewall/rules"},
		{"block address", firewall.ErrAddr, 400, errInvalidRule, "addr", "POST", "/api/firewall/blocklist"},
		{"allow address", firewall.ErrAddr, 400, errInvalidRule, "addr", "POST", "/api/firewall/allowlist"},
		{"allow duplicate", firewall.ErrDuplicate, 409, errConflict, "", "POST", "/api/firewall/allowlist"},
		{"comment", firewall.ErrComment, 400, errInvalidRule, "comment", "POST", "/api/firewall/rules"},
		{"missing", firewall.ErrNotFound, 404, errNotFound, "", "DELETE", "/api/firewall/rules/9"},
		{"duplicate", firewall.ErrDuplicate, 409, errConflict, "", "POST", "/api/firewall/blocklist"},
		{"already pending", firewall.ErrPending, 409, errApplyPending, "", "POST", "/api/firewall/apply"},
		{"not enabled", firewall.ErrNotEnabled, 409, errNotEnabled, "", "POST", "/api/firewall/apply"},
		{"nothing pending", firewall.ErrNoPending, 409, errNoPending, "", "POST", "/api/firewall/revert"},
		{"wrong token", firewall.ErrToken, 409, errTokenMatch, "", "POST", "/api/firewall/confirm"},
		{"late confirm", firewall.ErrExpired, 410, errExpired, "", "POST", "/api/firewall/confirm"},
		{"iptables output is not leaked", errors.New("iptables-restore: secret detail"), 500, errInternal, "", "POST", "/api/firewall/enable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, s := firewallHarness(t, &fakeFirewall{err: c.err})
			var body any
			switch c.path {
			case "/api/firewall/rules":
				body = map[string]any{"portStart": 1, "portEnd": 1, "protocol": "tcp", "enabled": true}
			case "/api/firewall/blocklist", "/api/firewall/allowlist":
				body = map[string]any{"addr": "x"}
			case "/api/firewall/confirm":
				body = map[string]any{"token": "t"}
			case "/api/firewall/enable", "/api/firewall/apply", "/api/firewall/revert":
				body = map[string]any{}
			}
			rec := h.do(c.method, c.path, s.cookie, s.csrf, body)
			env := h.errorEnvelopeOf(rec)
			if rec.Code != c.status || env.Error != c.class || env.Field != c.field {
				t.Fatalf("got %d %+v, want %d %s field=%q", rec.Code, env, c.status, c.class, c.field)
			}
			if c.status == 500 && env.Detail != "the request could not be completed" {
				t.Fatalf("internal detail leaked: %q", env.Detail)
			}
		})
	}
}

func TestFirewallApplyEnableConfirmRevertDisable(t *testing.T) {
	dl := time.Date(2026, 11, 17, 10, 1, 0, 0, time.UTC)
	f := &fakeFirewall{pending: firewall.Pending{Token: "abc", StartedAt: dl.Add(-time.Minute), Deadline: dl}}
	h, s := firewallHarness(t, f)

	for _, p := range []string{"/api/firewall/enable", "/api/firewall/apply"} {
		rec := h.do(http.MethodPost, p, s.cookie, s.csrf, map[string]any{})
		var got firewall.Pending
		h.decode(rec, &got)
		if rec.Code != http.StatusOK || got.Token != "abc" || !got.Deadline.Equal(dl) {
			t.Fatalf("%s: %d %+v", p, rec.Code, got)
		}
	}
	if rec := h.do(http.MethodPost, "/api/firewall/confirm", s.cookie, s.csrf, map[string]any{"token": "abc"}); rec.Code != http.StatusNoContent || f.token != "abc" {
		t.Fatalf("confirm: %d token=%q", rec.Code, f.token)
	}
	if rec := h.do(http.MethodPost, "/api/firewall/revert", s.cookie, s.csrf, map[string]any{}); rec.Code != http.StatusNoContent {
		t.Fatalf("revert: %d", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/firewall/disable", s.cookie, s.csrf, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("disable with no body: %d", rec.Code)
	}
	if rec := h.doRaw(http.MethodPost, "/api/firewall/apply", s.cookie, s.csrf, `{"force":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("apply must reject unknown fields: %d", rec.Code)
	}
}

func TestFirewallStateShape(t *testing.T) {
	h, s := firewallHarness(t, &fakeFirewall{})
	rec := h.do(http.MethodGet, "/api/firewall", s.cookie, s.csrf, nil)
	var raw map[string]any
	h.decode(rec, &raw)
	for _, k := range []string{"enabled", "ipv6", "dirty", "sshPorts", "panelPort", "pending", "dockerPorts", "builtins"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("state is missing %q: %v", k, raw)
		}
	}
	if raw["pending"] != nil {
		t.Errorf("pending must be null when nothing is waiting, got %v", raw["pending"])
	}
}

func TestFirewallAllowlistRoundTrip(t *testing.T) {
	f := &fakeFirewall{}
	h, s := firewallHarness(t, f)

	rec := h.do(http.MethodPost, "/api/firewall/allowlist", s.cookie, s.csrf,
		map[string]any{"addr": "198.51.100.9", "comment": "office"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var got firewall.AllowEntry
	h.decode(rec, &got)
	if got.ID != 4 || got.Addr != "198.51.100.9" || got.Comment != "office" {
		t.Fatalf("response: %+v", got)
	}
	if rec := h.do(http.MethodGet, "/api/firewall/allowlist", s.cookie, s.csrf, nil); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/api/firewall/allowlist/5", s.cookie, s.csrf, nil); rec.Code != http.StatusNoContent || f.gotID != 5 {
		t.Fatalf("delete: %d id=%d", rec.Code, f.gotID)
	}
	if rec := h.do(http.MethodDelete, "/api/firewall/allowlist/abc", s.cookie, s.csrf, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := h.doRaw(http.MethodPost, "/api/firewall/allowlist", s.cookie, s.csrf, `{"addr":"1.2.3.4","nope":2}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
}
