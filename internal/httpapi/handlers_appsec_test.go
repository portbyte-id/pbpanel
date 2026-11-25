package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/appsec"
	"github.com/portbyte-id/server-panel/internal/jobs"
)

// fakeAppsec is the agent's lifecycle as a test drives it: it records what it was
// asked to do and never touches a host.
type fakeAppsec struct {
	status  appsec.Status
	install int
	ack     string
	uninst  int
	refuse  error
}

func (f *fakeAppsec) Status(context.Context) (appsec.Status, error) { return f.status, nil }

func (f *fakeAppsec) Availability(context.Context) appsec.Availability {
	return appsec.Availability{
		Available: f.status.Ready(), Installed: f.status.Installed, AgentActive: f.status.Active,
		AgentVersion: f.status.Version, PolicyLoad: f.status.PolicyLoad, PinnedVersion: "1.1.37",
		Warning: appsec.Warning, Acknowledgement: appsec.Acknowledgement,
	}
}

func (f *fakeAppsec) Install(_ context.Context, opts appsec.InstallOptions) error {
	f.install++
	f.ack = opts.Acknowledge
	return f.refuse
}

func (f *fakeAppsec) Uninstall(context.Context, func(context.Context) (bool, error)) error {
	f.uninst++
	return f.refuse
}

// newAppsecHarness is the firewall harness with the agent wired in, rebuilt so the
// new dependencies take effect.
func newAppsecHarness(t *testing.T) (*nginxHarness, *fakeAppsec) {
	t.Helper()
	h := newWAFHarness(t, true)
	fake := &fakeAppsec{status: appsec.Status{
		Installed: true, Active: true, State: "active", Version: "1.1.37-open-source", PolicyLoad: "Success",
	}}
	h.deps.Appsec = fake
	h.deps.OpenAppsec = fake.Availability
	handler, err := New(h.deps)
	if err != nil {
		t.Fatal(err)
	}
	h.handler = handler
	return h, fake
}

// TestAppsecInstallNeedsTheAcknowledgement is the D5 requirement as a test: an
// install without the exact acknowledgement is refused before a job starts, and the
// refusal carries the warning text.
func TestAppsecInstallNeedsTheAcknowledgement(t *testing.T) {
	h, fake := newAppsecHarness(t)
	for _, body := range []map[string]any{
		{},
		{"acknowledge": ""},
		{"acknowledge": "yes"},
		{"acknowledge": "true"},
	} {
		rec := h.do(http.MethodPost, "/api/appsec/install", h.s.cookie, h.s.csrf, body)
		env := h.errorEnvelopeOf(rec)
		if rec.Code != http.StatusUnprocessableEntity || env.Error != errInvalidInput || env.Field != "acknowledge" {
			t.Errorf("body %v: %d %+v, want 422 on acknowledge", body, rec.Code, env)
		}
		if !strings.Contains(env.Detail, "Check Point") || !strings.Contains(env.Detail, "tracking") {
			t.Errorf("body %v: the refusal must carry the warning, got %q", body, env.Detail)
		}
	}
	if fake.install != 0 {
		t.Errorf("an unacknowledged install started %d jobs", fake.install)
	}
}

func TestAppsecInstallStartsAJob(t *testing.T) {
	h, fake := newAppsecHarness(t)
	code, out := h.call(http.MethodPost, "/api/appsec/install", map[string]string{"acknowledge": appsec.Acknowledgement})
	if code != http.StatusAccepted {
		t.Fatalf("install = %d %v", code, out)
	}
	job, ok := out["job"].(map[string]any)
	if !ok || job["kind"] != "appsec.install" {
		t.Fatalf("job = %v", out["job"])
	}
	if fake.ack != appsec.Acknowledgement {
		t.Errorf("the manager saw acknowledge %q", fake.ack)
	}
}

// TestAppsecStatusCarriesTheWarning proves the Store card reads exactly the one text
// the operator acknowledged, rather than a copy the interface invented.
func TestAppsecStatusCarriesTheWarning(t *testing.T) {
	h, _ := newAppsecHarness(t)
	code, out := h.call(http.MethodGet, "/api/appsec", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/appsec = %d %v", code, out)
	}
	if out["warning"] != appsec.Warning {
		t.Errorf("warning = %q, want the constant", out["warning"])
	}
	if out["acknowledgement"] != appsec.Acknowledgement {
		t.Errorf("acknowledgement = %q", out["acknowledgement"])
	}
	if out["agent_version"] != "1.1.37-open-source" || out["pinned_version"] != "1.1.37" {
		t.Errorf("versions = %v / %v", out["agent_version"], out["pinned_version"])
	}
	if out["available"] != true || out["installed"] != true || out["agent_active"] != true {
		t.Errorf("state = %v", out)
	}
}

// TestAppsecUninstallReportsTheRefusalInTheJob proves the refusal reaches the job
// rather than being swallowed: the job row carries the error and the API answers 202
// with the job to watch.
func TestAppsecUninstallReportsTheRefusalInTheJob(t *testing.T) {
	h, fake := newAppsecHarness(t)
	fake.refuse = appsec.ErrInUse
	h.deps.WAFEngineInUse = func(context.Context) (bool, error) { return true, nil }
	handler, err := New(h.deps)
	if err != nil {
		t.Fatal(err)
	}
	h.handler = handler

	code, out := h.call(http.MethodPost, "/api/appsec/uninstall", map[string]any{})
	if code != http.StatusAccepted {
		t.Fatalf("uninstall = %d %v", code, out)
	}
	job, ok := out["job"].(map[string]any)
	if !ok {
		t.Fatalf("no job in %v", out)
	}
	id, _ := job["id"].(string)
	done, err := h.deps.Jobs.Wait(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != jobs.StateFailed {
		t.Fatalf("job state = %s, want failed", done.State)
	}
	if !strings.Contains(done.Err, "still uses the open-appsec engine") {
		t.Errorf("job error = %q", done.Err)
	}
}

// TestWAFStatusCarriesOpenAppsec proves the firewall overview and a site's firewall
// view both carry the engine's availability, the warning included, so the firewall
// page and the Store agree on the text.
func TestWAFStatusCarriesOpenAppsec(t *testing.T) {
	h, _ := newAppsecHarness(t)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})

	code, out := h.call(http.MethodGet, "/api/waf/status", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d %v", code, out)
	}
	oa, ok := out["open_appsec"].(map[string]any)
	if !ok {
		t.Fatalf("no open_appsec in the overview: %v", out)
	}
	if oa["warning"] != appsec.Warning {
		t.Errorf("overview warning = %q", oa["warning"])
	}

	code, out = h.call(http.MethodGet, "/api/sites/a.test/waf", nil)
	if code != http.StatusOK {
		t.Fatalf("site waf = %d %v", code, out)
	}
	oa, ok = out["open_appsec"].(map[string]any)
	if !ok || oa["warning"] != appsec.Warning {
		t.Errorf("site firewall view has no open_appsec warning: %v", out["open_appsec"])
	}
}

// TestOpenAppsecIsAbsentWithoutThePackage proves the field is omitted, not null, on a
// panel with no appsec package: a client can then tell "no engine" from "engine,
// unavailable".
func TestOpenAppsecIsAbsentWithoutThePackage(t *testing.T) {
	h := newWAFHarness(t, true)
	h.mustCreate(map[string]any{"domain": "a.test", "kind": "static"})
	_, out := h.call(http.MethodGet, "/api/waf/status", nil)
	if _, ok := out["open_appsec"]; ok {
		t.Errorf("open_appsec must be omitted without the package: %v", out["open_appsec"])
	}
}
