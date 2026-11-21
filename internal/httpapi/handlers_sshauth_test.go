package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/sshauth"
)

type fakeSSHAuth struct {
	got    sshauth.Query
	report sshauth.Report
	err    error
	avail  error
}

func (f *fakeSSHAuth) Attempts(_ context.Context, q sshauth.Query) (sshauth.Report, error) {
	f.got = q
	return f.report, f.err
}

func (f *fakeSSHAuth) Available(context.Context) error { return f.avail }

// sampleReport is a fixed answer so the shape can be asserted field by field.
func sampleReport() sshauth.Report {
	return sshauth.Report{
		From:  time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
		To:    time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
		Limit: 200,
		Total: 2,
		Summary: sshauth.Summary{
			Failed: 1, Succeeded: 1, DistinctIPs: 1, DistinctUsers: 1,
			TopIPs:   []sshauth.IPRow{{IP: "45.155.205.233", Failed: 1, Succeeded: 1, Total: 2}},
			TopUsers: []sshauth.UserRow{{User: "admin", Failed: 1, Succeeded: 1, Total: 2}},
		},
		Attempts: []sshauth.Attempt{
			{At: time.Date(2026, 11, 21, 11, 59, 0, 0, time.UTC), IP: "45.155.205.233", User: "admin", Port: 44556,
				Outcome: sshauth.OutcomeSuccess, Method: sshauth.MethodPublicKey},
			{At: time.Date(2026, 11, 21, 11, 58, 0, 0, time.UTC), IP: "45.155.205.233", User: "admin", Port: 44556,
				Outcome: sshauth.OutcomeFailed, Method: sshauth.MethodPassword},
		},
	}
}

func newSSHHarness(t *testing.T) (*harness, *fakeSSHAuth, session) {
	t.Helper()
	h := newHarness(t)
	fake := &fakeSSHAuth{report: sampleReport()}
	h.deps.SSHAuth = fake
	handler, err := New(h.deps)
	if err != nil {
		t.Fatal(err)
	}
	h.handler = handler
	return h, fake, h.login()
}

func TestSSHAuthRequiresAuthAndAnswers503WithoutAMonitor(t *testing.T) {
	h := newHarness(t)
	if rec := h.do(http.MethodGet, "/api/ssh/attempts", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated: %d", rec.Code)
	}
	s := h.login()
	if rec := h.do(http.MethodGet, "/api/ssh/attempts", s.cookie, s.csrf, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("without a monitor: %d", rec.Code)
	}
}

func TestSSHAttemptsResponseShape(t *testing.T) {
	h, _, s := newSSHHarness(t)
	rec := h.do(http.MethodGet, "/api/ssh/attempts", s.cookie, s.csrf, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		From     time.Time `json:"from"`
		To       time.Time `json:"to"`
		Total    int       `json:"total"`
		Limit    int       `json:"limit"`
		Attempts []struct {
			At      time.Time `json:"at"`
			IP      string    `json:"ip"`
			User    string    `json:"user"`
			Port    int       `json:"port"`
			Outcome string    `json:"outcome"`
			Method  string    `json:"method"`
		} `json:"attempts"`
		Summary struct {
			Failed        int `json:"failed"`
			Succeeded     int `json:"succeeded"`
			DistinctIPs   int `json:"distinct_ips"`
			DistinctUsers int `json:"distinct_users"`
			TopIPs        []struct {
				IP     string `json:"ip"`
				Failed int    `json:"failed"`
				Total  int    `json:"total"`
			} `json:"top_ips"`
			TopUsers []struct {
				User   string `json:"user"`
				Failed int    `json:"failed"`
			} `json:"top_users"`
		} `json:"summary"`
	}
	h.decode(rec, &out)
	if len(out.Attempts) != 2 || out.Total != 2 || out.Limit != 200 {
		t.Fatalf("report = %s", rec.Body.String())
	}
	a := out.Attempts[0]
	if a.IP != "45.155.205.233" || a.User != "admin" || a.Port != 44556 || a.Outcome != "success" || a.Method != "publickey" {
		t.Errorf("first attempt = %+v", a)
	}
	if a.At.IsZero() || out.Attempts[1].Outcome != "failed" || out.Attempts[1].Method != "password" {
		t.Errorf("attempts = %+v", out.Attempts)
	}
	// The summary is beside the feed, so the page can render both from one read.
	if out.Summary.Failed != 1 || out.Summary.Succeeded != 1 || out.Summary.DistinctIPs != 1 {
		t.Errorf("summary = %+v", out.Summary)
	}
	if len(out.Summary.TopIPs) != 1 || out.Summary.TopIPs[0].IP != "45.155.205.233" || len(out.Summary.TopUsers) != 1 {
		t.Errorf("top lists = %+v %+v", out.Summary.TopIPs, out.Summary.TopUsers)
	}
}

func TestSSHAttemptsParameters(t *testing.T) {
	h, fake, s := newSSHHarness(t)
	get := func(q url.Values) int {
		return h.do(http.MethodGet, "/api/ssh/attempts?"+q.Encode(), s.cookie, s.csrf, nil).Code
	}

	if got := get(url.Values{}); got != http.StatusOK || fake.got.Range != 0 || fake.got.Limit != 0 || fake.got.Outcome != "" {
		t.Errorf("defaults: %d %+v", got, fake.got)
	}
	if got := get(url.Values{"range": {"90m"}, "outcome": {"FAILED"}, "limit": {"50"}}); got != http.StatusOK {
		t.Fatalf("explicit: %d", got)
	}
	if fake.got.Range != 90*time.Minute || fake.got.Outcome != sshauth.OutcomeFailed || fake.got.Limit != 50 {
		t.Errorf("query = %+v", fake.got)
	}
	if got := get(url.Values{"outcome": {"success"}}); got != http.StatusOK || fake.got.Outcome != sshauth.OutcomeSuccess {
		t.Errorf("success outcome: %d %+v", got, fake.got)
	}

	for name, q := range map[string]url.Values{
		"range unparsable": {"range": {"yesterday"}},
		"range negative":   {"range": {"-1h"}},
		"range too small":  {"range": {"1m"}},
		"range too large":  {"range": {"8760h"}},
		"outcome":          {"outcome": {"maybe"}},
		"limit zero":       {"limit": {"0"}},
		"limit negative":   {"limit": {"-5"}},
		"limit text":       {"limit": {"ten"}},
		"limit too large":  {"limit": {"100000"}},
	} {
		rec := h.do(http.MethodGet, "/api/ssh/attempts?"+q.Encode(), s.cookie, s.csrf, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
			continue
		}
		if env := h.errorEnvelopeOf(rec); env.Error != errBadRequest || env.Field == "" {
			t.Errorf("%s: envelope = %+v", name, env)
		}
	}
}

// A host with no readable journal is one unavailable source, not a request the
// page should retry as if it had been malformed.
func TestSSHAttemptsReportsAnUnreadableJournal(t *testing.T) {
	h, fake, s := newSSHHarness(t)
	fake.err = sshauth.ErrUnavailable
	rec := h.do(http.MethodGet, "/api/ssh/attempts", s.cookie, s.csrf, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if env := h.errorEnvelopeOf(rec); env.Error != errUnavailable {
		t.Errorf("envelope = %+v", env)
	}
}

// A failure that is not the journal being absent is the panel's own bug and must
// not describe itself to the client.
func TestSSHAttemptsHidesAnInternalFailure(t *testing.T) {
	h, fake, s := newSSHHarness(t)
	fake.err = errors.New("pq: connection reset by peer")
	rec := h.do(http.MethodGet, "/api/ssh/attempts", s.cookie, s.csrf, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if env := h.errorEnvelopeOf(rec); env.Error != errInternal {
		t.Errorf("envelope = %+v", env)
	}
}
