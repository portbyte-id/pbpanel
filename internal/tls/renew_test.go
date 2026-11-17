package tls

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

const day = 24 * time.Hour

// fakeIssuer behaves like *Issuer: on success it replaces the row with a fresh
// 90-day certificate, on failure it touches nothing.
type fakeIssuer struct {
	r     *rig
	mu    sync.Mutex
	calls []string
	err   map[string]error
	block chan struct{}
	panic bool
}

func (f *fakeIssuer) issue(ctx context.Context, domain, source, provider string, names []string) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, source+":"+domain+":"+provider)
	err := f.err[domain]
	f.mu.Unlock()
	if f.panic {
		panic("boom")
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	if err != nil {
		return Result{}, err
	}
	old, _ := f.r.st.GetCert(ctx, domain)
	row := Cert{
		Domain: domain, Source: source, Names: names, Provider: provider, RedirectHTTP: old.RedirectHTTP,
		NotBefore: f.r.now, NotAfter: f.r.now.Add(90 * day), Issuer: "CN=Fake", Serial: "new",
		LastRenewalAt: f.r.now, LastRenewalResult: RenewalOK,
	}
	if err := f.r.st.PutCert(ctx, row); err != nil {
		return Result{}, err
	}
	return Result{Domain: domain, NotAfter: row.NotAfter}, nil
}

func (f *fakeIssuer) IssueHTTP01(ctx context.Context, domain string, names []string, _ string) (Result, error) {
	return f.issue(ctx, domain, SourceHTTP01, "", names)
}

func (f *fakeIssuer) IssueDNS01(ctx context.Context, domain string, names []string, p, _ string) (Result, error) {
	return f.issue(ctx, domain, SourceDNS01, p, names)
}

func (f *fakeIssuer) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type audited struct{ domain, outcome, detail string }

type renewRig struct {
	*rig
	iss     *fakeIssuer
	rn      *Renewer
	applied []string
	applyEr error
	audits  []audited
	days    int
}

func newRenewRig(t *testing.T) *renewRig {
	t.Helper()
	rr := &renewRig{rig: newRig(t)}
	rr.iss = &fakeIssuer{r: rr.rig, err: map[string]error{}}
	rn, err := NewRenewer(RenewerOptions{
		Store: rr.st, Issuer: rr.iss,
		Apply: func(_ context.Context, d string) error {
			rr.applied = append(rr.applied, d)
			return rr.applyEr
		},
		DaysBefore: func() int { return rr.days },
		Now:        func() time.Time { return rr.now },
		Audit: func(_ context.Context, d, o, det string) {
			rr.audits = append(rr.audits, audited{d, o, det})
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	rr.rn = rn
	return rr
}

func (rr *renewRig) put(t *testing.T, domain, source string, expiresIn time.Duration) {
	t.Helper()
	c := sampleCert(domain)
	c.Source = source
	c.NotAfter = rr.now.Add(expiresIn)
	if source == SourceDNS01 {
		c.Provider = "cf"
	}
	if err := rr.st.PutCert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func outcomeOf(t *testing.T, outs []Outcome, domain string) Outcome {
	t.Helper()
	for _, o := range outs {
		if o.Domain == domain {
			return o
		}
	}
	t.Fatalf("no outcome for %s in %+v", domain, outs)
	return Outcome{}
}

func TestRenewWindowBoundary(t *testing.T) {
	rr := newRenewRig(t)
	rr.put(t, "d29.example.com", SourceHTTP01, 29*day)
	rr.put(t, "d30.example.com", SourceHTTP01, 30*day)
	rr.put(t, "d31.example.com", SourceHTTP01, 31*day)
	outs, err := rr.rn.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for d, want := range map[string]string{
		"d29.example.com": OutcomeRenewed, "d30.example.com": OutcomeRenewed, "d31.example.com": OutcomeNotDue,
	} {
		if got := outcomeOf(t, outs, d).Result; got != want {
			t.Errorf("%s = %s, want %s", d, got, want)
		}
	}
	if len(rr.applied) != 2 {
		t.Fatalf("applied = %v", rr.applied)
	}
	c, _ := rr.st.GetCert(context.Background(), "d29.example.com")
	if !c.NotAfter.Equal(rr.now.Add(90*day)) || c.LastRenewalResult != RenewalOK {
		t.Fatalf("row not renewed: %+v", c)
	}
}

func TestRenewCustomDaysBefore(t *testing.T) {
	rr := newRenewRig(t)
	rr.days = 7
	rr.put(t, "a.example.com", SourceHTTP01, 8*day)
	rr.put(t, "b.example.com", SourceHTTP01, 7*day)
	outs, _ := rr.rn.RunOnce(context.Background())
	if outcomeOf(t, outs, "a.example.com").Result != OutcomeNotDue || outcomeOf(t, outs, "b.example.com").Result != OutcomeRenewed {
		t.Fatalf("outs = %+v", outs)
	}
	rr.days = 0 // unset falls back to 30
	rr.put(t, "c.example.com", SourceHTTP01, 20*day)
	outs, _ = rr.rn.RunOnce(context.Background())
	if outcomeOf(t, outs, "c.example.com").Result != OutcomeRenewed {
		t.Fatalf("default window not 30 days: %+v", outs)
	}
}

func TestRenewExpiredCertAndDNSProvider(t *testing.T) {
	rr := newRenewRig(t)
	rr.put(t, "old.example.com", SourceDNS01, -5*day)
	outs, _ := rr.rn.RunOnce(context.Background())
	if outcomeOf(t, outs, "old.example.com").Result != OutcomeRenewed {
		t.Fatalf("outs = %+v", outs)
	}
	if calls := rr.iss.callList(); len(calls) != 1 || calls[0] != "dns01:old.example.com:cf" {
		t.Fatalf("calls = %v", calls)
	}
	c, _ := rr.st.GetCert(context.Background(), "old.example.com")
	if c.Provider != "cf" {
		t.Fatalf("provider lost: %+v", c)
	}
}

func TestRenewDNSWithoutProviderFails(t *testing.T) {
	rr := newRenewRig(t)
	rr.put(t, "x.example.com", SourceDNS01, 5*day)
	c, _ := rr.st.GetCert(context.Background(), "x.example.com")
	c.Provider = ""
	if err := rr.st.PutCert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	outs, _ := rr.rn.RunOnce(context.Background())
	o := outcomeOf(t, outs, "x.example.com")
	if o.Result != OutcomeFailed || !strings.Contains(o.Error, "no DNS provider") || len(rr.iss.callList()) != 0 {
		t.Fatalf("o = %+v calls = %v", o, rr.iss.callList())
	}
}

func TestRenewSkipsUploadedCerts(t *testing.T) {
	rr := newRenewRig(t)
	rr.put(t, "up.example.com", SourceUpload, 1*day)
	outs, err := rr.rn.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if o := outcomeOf(t, outs, "up.example.com"); o.Result != OutcomeSkipped {
		t.Fatalf("o = %+v", o)
	}
	if len(rr.iss.callList()) != 0 || len(rr.applied) != 0 {
		t.Fatal("an uploaded certificate was ordered or applied")
	}
	c, _ := rr.st.GetCert(context.Background(), "up.example.com")
	if c.LastRenewalResult != "" {
		t.Fatalf("row touched: %+v", c)
	}
}

func TestRenewNowNotRenewable(t *testing.T) {
	rr := newRenewRig(t)
	rr.put(t, "up.example.com", SourceUpload, 1*day)
	if _, err := rr.rn.RenewNow(context.Background(), "up.example.com"); !errors.Is(err, ErrNotRenewable) {
		t.Fatalf("err = %v", err)
	}
	if _, err := rr.rn.RenewNow(context.Background(), "none.example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestRenewNowIgnoresWindowAndBackoff(t *testing.T) {
	rr := newRenewRig(t)
	rr.put(t, "fresh.example.com", SourceHTTP01, 80*day)
	o, err := rr.rn.RenewNow(context.Background(), "fresh.example.com")
	if err != nil || o.Result != OutcomeRenewed {
		t.Fatalf("o = %+v err = %v", o, err)
	}
	rr.iss.err["fresh.example.com"] = errors.New("ca down")
	if o, _ = rr.rn.RenewNow(context.Background(), "fresh.example.com"); o.Result != OutcomeFailed {
		t.Fatalf("o = %+v", o)
	}
	rr.iss.err["fresh.example.com"] = nil
	if o, _ = rr.rn.RenewNow(context.Background(), "fresh.example.com"); o.Result != OutcomeRenewed {
		t.Fatalf("RenewNow honoured the backoff: %+v", o)
	}
}

func TestRenewFailureRecordedOldCertKept(t *testing.T) {
	rr := newRenewRig(t)
	rr.put(t, "bad.example.com", SourceHTTP01, 10*day)
	rr.put(t, "good.example.com", SourceHTTP01, 10*day)
	before, _ := rr.st.GetCert(context.Background(), "bad.example.com")
	rr.iss.err["bad.example.com"] = errors.New("validation failed\n" + strings.Repeat("x", 800))

	outs, err := rr.rn.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcomeOf(t, outs, "good.example.com").Result != OutcomeRenewed {
		t.Fatalf("one failure stopped the others: %+v", outs)
	}
	o := outcomeOf(t, outs, "bad.example.com")
	if o.Result != OutcomeFailed || len(o.Error) > maxErrorLength || strings.Contains(o.Error, "\n") {
		t.Fatalf("o = %+v (len %d)", o, len(o.Error))
	}
	got, _ := rr.st.GetCert(context.Background(), "bad.example.com")
	if got.LastRenewalResult != RenewalFailed || got.LastRenewalError != o.Error || !got.LastRenewalAt.Equal(rr.now) {
		t.Fatalf("failure not recorded: %+v", got)
	}
	if !got.NotAfter.Equal(before.NotAfter) || got.Serial != before.Serial || got.Source != before.Source ||
		strings.Join(got.Names, ",") != strings.Join(before.Names, ",") {
		t.Fatalf("old certificate disturbed: %+v vs %+v", got, before)
	}
	for _, a := range rr.audits {
		if a.domain == "bad.example.com" && a.outcome != OutcomeFailed {
			t.Fatalf("audit = %+v", a)
		}
	}
	if len(rr.audits) != 2 {
		t.Fatalf("audits = %+v", rr.audits)
	}
}

func TestRenewBackoffSixHours(t *testing.T) {
	rr := newRenewRig(t)
	rr.put(t, "bad.example.com", SourceHTTP01, 10*day)
	rr.iss.err["bad.example.com"] = errors.New("nope")
	ctx := context.Background()
	rr.rn.RunOnce(ctx)
	if n := len(rr.iss.callList()); n != 1 {
		t.Fatalf("calls = %d", n)
	}

	rr.now = rr.now.Add(5*time.Hour + 59*time.Minute)
	outs, _ := rr.rn.RunOnce(ctx)
	if o := outcomeOf(t, outs, "bad.example.com"); o.Result != OutcomeSkipped {
		t.Fatalf("o = %+v", o)
	}
	if n := len(rr.iss.callList()); n != 1 {
		t.Fatalf("backoff hit the CA: %d calls", n)
	}
	last := rr.audits[len(rr.audits)-1]
	if last.outcome != OutcomeSkipped {
		t.Fatalf("skip not audited: %+v", last)
	}

	rr.now = rr.now.Add(2 * time.Minute)
	outs, _ = rr.rn.RunOnce(ctx)
	if o := outcomeOf(t, outs, "bad.example.com"); o.Result != OutcomeFailed {
		t.Fatalf("o = %+v", o)
	}
	if n := len(rr.iss.callList()); n != 2 {
		t.Fatalf("calls = %d", n)
	}
}

func TestRenewApplyFailureRetriesWithoutReissue(t *testing.T) {
	rr := newRenewRig(t)
	rr.put(t, "site.example.com", SourceHTTP01, 10*day)
	rr.applyEr = errors.New("nginx -t failed")
	ctx := context.Background()

	outs, _ := rr.rn.RunOnce(ctx)
	o := outcomeOf(t, outs, "site.example.com")
	if o.Result != OutcomeFailed || !strings.HasPrefix(o.Error, "issued but not applied: ") {
		t.Fatalf("o = %+v", o)
	}
	c, _ := rr.st.GetCert(ctx, "site.example.com")
	if c.LastRenewalResult != RenewalFailed || !c.NotAfter.Equal(rr.now.Add(90*day)) {
		t.Fatalf("row = %+v", c)
	}

	// Inside the 6 h backoff and with a fresh certificate: Apply is retried
	// anyway, and nothing is ordered again.
	rr.now = rr.now.Add(time.Hour)
	rr.applyEr = nil
	outs, _ = rr.rn.RunOnce(ctx)
	if o := outcomeOf(t, outs, "site.example.com"); o.Result != OutcomeRenewed {
		t.Fatalf("o = %+v", o)
	}
	if n := len(rr.iss.callList()); n != 1 {
		t.Fatalf("reissued: %v", rr.iss.callList())
	}
	if len(rr.applied) != 2 {
		t.Fatalf("applied = %v", rr.applied)
	}
	c, _ = rr.st.GetCert(ctx, "site.example.com")
	if c.LastRenewalResult != RenewalOK || c.LastRenewalError != "" || !c.LastRenewalAt.Equal(rr.now) {
		t.Fatalf("row = %+v", c)
	}

	// And it settles: the next run has nothing to do.
	outs, _ = rr.rn.RunOnce(ctx)
	if o := outcomeOf(t, outs, "site.example.com"); o.Result != OutcomeNotDue {
		t.Fatalf("o = %+v", o)
	}
}

func TestRenewRunsAreSerialised(t *testing.T) {
	rr := newRenewRig(t)
	rr.put(t, "a.example.com", SourceHTTP01, 10*day)
	rr.iss.block = make(chan struct{})
	ctx := context.Background()

	done := make(chan []Outcome, 1)
	go func() { o, _ := rr.rn.RunOnce(ctx); done <- o }()
	for len(rr.iss.callList()) == 0 {
		time.Sleep(time.Millisecond)
	}

	// RenewNow waits for the run; with a short deadline it gives up.
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := rr.rn.RenewNow(short, "a.example.com"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if n := len(rr.iss.callList()); n != 1 {
		t.Fatalf("a second order started during a run: %d", n)
	}

	// Without a deadline it proceeds once the run ends.
	res := make(chan error, 1)
	go func() { _, err := rr.rn.RenewNow(ctx, "a.example.com"); res <- err }()
	close(rr.iss.block)
	if outs := <-done; len(outs) != 1 || outs[0].Result != OutcomeRenewed {
		t.Fatalf("outs = %+v", outs)
	}
	if err := <-res; err != nil {
		t.Fatal(err)
	}
}

func TestStartRecoversPanicAndRunsAgain(t *testing.T) {
	rr := newRenewRig(t)
	rr.put(t, "a.example.com", SourceHTTP01, 10*day)
	rr.iss.panic = true
	rr.rn.firstDelay = time.Millisecond
	rr.rn.interval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { rr.rn.Start(ctx); close(stopped) }()
	deadline := time.After(5 * time.Second)
	for len(rr.iss.callList()) < 2 {
		select {
		case <-deadline:
			t.Fatalf("Start did not run again after a panic; calls = %d", len(rr.iss.callList()))
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
	// The run lock was released despite the panic.
	if err := rr.rn.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	rr.rn.unlock()
}

func TestStartCancelsBeforeFirstRun(t *testing.T) {
	rr := newRenewRig(t)
	rr.put(t, "a.example.com", SourceHTTP01, 10*day)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { rr.rn.Start(ctx); close(stopped) }()
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return")
	}
	if len(rr.iss.callList()) != 0 {
		t.Fatal("ran after cancel")
	}
}

func TestNewRenewerRequiresStoreAndIssuer(t *testing.T) {
	if _, err := NewRenewer(RenewerOptions{}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestTrimErrorKeepsRunes(t *testing.T) {
	got := trimError(strings.Repeat("é", 400))
	if len(got) > maxErrorLength || !strings.HasSuffix(got, "...") {
		t.Fatalf("len %d: %q", len(got), got[len(got)-10:])
	}
}

// TestMarkNotAppliedMakesTheNextRunInstallWithoutOrdering covers an issue job
// whose Apply failed: the row said ok until MarkNotApplied, and the renewal
// run then installs the stored files without calling the CA.
func TestMarkNotAppliedMakesTheNextRunInstallWithoutOrdering(t *testing.T) {
	rr := newRenewRig(t)
	rr.put(t, "site.example.com", SourceHTTP01, 60*day) // fresh, not due
	ctx := context.Background()

	if err := MarkNotApplied(ctx, rr.st, "site.example.com", errors.New("nginx -t failed"), rr.now); err != nil {
		t.Fatal(err)
	}
	c, _ := rr.st.GetCert(ctx, "site.example.com")
	if c.LastRenewalResult != RenewalFailed || !strings.HasPrefix(c.LastRenewalError, "issued but not applied: ") {
		t.Fatalf("row = %+v", c)
	}

	outs, _ := rr.rn.RunOnce(ctx)
	if o := outcomeOf(t, outs, "site.example.com"); o.Result != OutcomeRenewed {
		t.Fatalf("o = %+v", o)
	}
	if n := len(rr.iss.callList()); n != 0 {
		t.Fatalf("ordered again: %v", rr.iss.callList())
	}
	if len(rr.applied) != 1 {
		t.Fatalf("applied = %v", rr.applied)
	}

	if err := MarkNotApplied(ctx, rr.st, "missing.example.com", errors.New("x"), rr.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown domain: %v", err)
	}
}
