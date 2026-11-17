package tls

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"
)

// Renewal outcomes, the values of Outcome.Result.
const (
	OutcomeRenewed = "renewed"
	OutcomeFailed  = "failed"
	OutcomeNotDue  = "not_due"
	OutcomeSkipped = "skipped"
)

const (
	defaultDaysBefore = 30
	defaultInterval   = 24 * time.Hour
	// failureBackoff is how long a certificate that failed to renew is left
	// alone by RunOnce, so a broken DNS record does not hit the CA every run.
	failureBackoff = 6 * time.Hour
	// firstRunDelay is the wait between Start and the first scan, long enough
	// for the listener, nginx and the settings to be up.
	firstRunDelay  = time.Minute
	maxErrorLength = 500
	// applyFailedPrefix marks a row whose certificate was issued and stored but
	// not installed. The next run installs it without ordering again.
	applyFailedPrefix = "issued but not applied: "
)

// ErrNotRenewable is returned by RenewNow for a certificate the panel cannot
// order again: an uploaded one.
var ErrNotRenewable = errors.New("tls: certificate was uploaded and cannot be renewed automatically")

// CertIssuer is the part of *Issuer the renewer needs.
type CertIssuer interface {
	IssueHTTP01(ctx context.Context, domain string, names []string, email string) (Result, error)
	IssueDNS01(ctx context.Context, domain string, names []string, providerName, email string) (Result, error)
}

// RenewerOptions configures a Renewer.
type RenewerOptions struct {
	Store  *Store
	Issuer CertIssuer
	// Apply installs the renewed files for a domain (an nginx reload for sites,
	// a listener replace for the panel certificate). Called after a successful
	// issue; nil means nothing to install.
	Apply func(ctx context.Context, domain string) error
	// DaysBefore reads tls.renew.days_before live; nil or a value below 1 means 30.
	DaysBefore func() int
	// Email is the ACME contact; may be nil.
	Email func() string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Interval is the time between scans; 0 means 24 h.
	Interval time.Duration
	// Audit is called for every renewed, failed or backoff-skipped attempt; may be nil.
	Audit func(ctx context.Context, domain, outcome, detail string)
	// Logger may be nil.
	Logger *slog.Logger
}

// Outcome is what happened to one certificate.
type Outcome struct {
	Domain string
	// Result is "renewed", "failed", "not_due" or "skipped".
	Result string
	Error  string
}

// Renewer re-issues certificates that are close to expiry. Runs are serialised:
// RenewNow during a scan waits for it to finish.
type Renewer struct {
	st         *Store
	issuer     CertIssuer
	apply      func(context.Context, string) error
	daysBefore func() int
	email      func() string
	now        func() time.Time
	interval   time.Duration
	audit      func(context.Context, string, string, string)
	log        *slog.Logger

	firstDelay time.Duration
	sem        chan struct{}
}

// NewRenewer returns a Renewer.
func NewRenewer(o RenewerOptions) (*Renewer, error) {
	if o.Store == nil || o.Issuer == nil {
		return nil, errors.New("tls: renewer needs a Store and an Issuer")
	}
	r := &Renewer{
		st: o.Store, issuer: o.Issuer, apply: o.Apply, daysBefore: o.DaysBefore, email: o.Email,
		now: o.Now, interval: o.Interval, audit: o.Audit, log: o.Logger,
		firstDelay: firstRunDelay, sem: make(chan struct{}, 1),
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.interval <= 0 {
		r.interval = defaultInterval
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	return r, nil
}

func (r *Renewer) window() time.Duration {
	d := defaultDaysBefore
	if r.daysBefore != nil {
		if v := r.daysBefore(); v > 0 {
			d = v
		}
	}
	return time.Duration(d) * 24 * time.Hour
}

func (r *Renewer) contact() string {
	if r.email == nil {
		return ""
	}
	return r.email()
}

// lock takes the run lock, giving up when ctx ends first.
func (r *Renewer) lock(ctx context.Context) error {
	select {
	case r.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Renewer) unlock() { <-r.sem }

// RunOnce renews every certificate that is due. A failing certificate is
// recorded and does not stop the others. The error is non-nil only when the
// scan itself could not run or ctx ended; outcomes of the certificates handled
// before that are still returned.
func (r *Renewer) RunOnce(ctx context.Context) ([]Outcome, error) {
	if err := r.lock(ctx); err != nil {
		return nil, err
	}
	defer r.unlock()

	certs, err := r.st.ListCerts(ctx)
	if err != nil {
		return nil, err
	}
	var out []Outcome
	for _, c := range certs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		out = append(out, r.scan(ctx, c))
	}
	return out, ctx.Err()
}

// RenewNow renews one certificate regardless of the renewal window and of the
// failure backoff. An uploaded certificate returns ErrNotRenewable and an
// unknown one ErrNotFound. A failed order is not an error here: it is the
// returned Outcome, already recorded on the row.
func (r *Renewer) RenewNow(ctx context.Context, domain string) (Outcome, error) {
	if err := r.lock(ctx); err != nil {
		return Outcome{}, err
	}
	defer r.unlock()

	c, err := r.st.GetCert(ctx, domain)
	if err != nil {
		return Outcome{}, err
	}
	if c.Source == SourceUpload {
		return Outcome{Domain: domain, Result: OutcomeSkipped, Error: ErrNotRenewable.Error()}, ErrNotRenewable
	}
	return r.renew(ctx, c), ctx.Err()
}

// scan decides what to do with one certificate during a scheduled run.
func (r *Renewer) scan(ctx context.Context, c Cert) Outcome {
	if c.Source == SourceUpload {
		return Outcome{Domain: c.Domain, Result: OutcomeSkipped, Error: ErrNotRenewable.Error()}
	}
	// Files that were issued but never installed: install them, do not order again.
	if c.LastRenewalResult == RenewalFailed && strings.HasPrefix(c.LastRenewalError, applyFailedPrefix) {
		return r.reapply(ctx, c)
	}
	now := r.now()
	if c.NotAfter.Sub(now) > r.window() {
		return Outcome{Domain: c.Domain, Result: OutcomeNotDue}
	}
	if c.LastRenewalResult == RenewalFailed && !c.LastRenewalAt.IsZero() && now.Sub(c.LastRenewalAt) < failureBackoff {
		o := Outcome{Domain: c.Domain, Result: OutcomeSkipped,
			Error: "last renewal failed less than 6 hours ago: " + c.LastRenewalError}
		r.report(ctx, o)
		return o
	}
	return r.renew(ctx, c)
}

// renew orders the certificate again and installs it.
func (r *Renewer) renew(ctx context.Context, c Cert) Outcome {
	var err error
	switch c.Source {
	case SourceHTTP01:
		_, err = r.issuer.IssueHTTP01(ctx, c.Domain, c.Names, r.contact())
	case SourceDNS01:
		if c.Provider == "" {
			err = errors.New("no DNS provider is recorded for this certificate; issue it again from the site's TLS tab")
		} else {
			_, err = r.issuer.IssueDNS01(ctx, c.Domain, c.Names, c.Provider, r.contact())
		}
	default:
		err = fmt.Errorf("unknown certificate source %q", c.Source)
	}
	if err != nil {
		// The Issuer writes files only after the CA has answered, so the old
		// certificate is still what is on disk and in the row.
		return r.fail(ctx, c, err.Error())
	}
	// Re-read: the Issuer replaced the row, and what is recorded now is the truth.
	fresh, gerr := r.st.GetCert(ctx, c.Domain)
	if gerr != nil {
		return r.fail(ctx, c, "issued but the certificate row could not be read: "+gerr.Error())
	}
	return r.install(ctx, fresh)
}

// reapply installs an already-issued certificate.
func (r *Renewer) reapply(ctx context.Context, c Cert) Outcome {
	return r.install(ctx, c)
}

// install runs Apply for a certificate whose files are on disk and records the result.
func (r *Renewer) install(ctx context.Context, c Cert) Outcome {
	if r.apply != nil {
		if err := r.apply(ctx, c.Domain); err != nil {
			return r.fail(ctx, c, applyFailedPrefix+err.Error())
		}
	}
	// A reapplied row still says failed; a freshly issued one already says ok.
	if c.LastRenewalResult != RenewalOK || c.LastRenewalError != "" {
		c.LastRenewalResult, c.LastRenewalError = RenewalOK, ""
		c.LastRenewalAt = r.now()
		if err := r.st.PutCert(ctx, c); err != nil {
			r.log.Error("tls: record renewal", "domain", c.Domain, "err", err)
		}
	}
	o := Outcome{Domain: c.Domain, Result: OutcomeRenewed}
	r.report(ctx, o)
	return o
}

// fail records a failure on the row, keeping every other field, and reports it.
func (r *Renewer) fail(ctx context.Context, c Cert, msg string) Outcome {
	msg = trimError(msg)
	o := Outcome{Domain: c.Domain, Result: OutcomeFailed, Error: msg}
	if ctx.Err() != nil {
		// Shutdown, not a verdict on the certificate: leave the row alone.
		return o
	}
	c.LastRenewalAt = r.now()
	c.LastRenewalResult = RenewalFailed
	c.LastRenewalError = msg
	if err := r.st.PutCert(ctx, c); err != nil {
		r.log.Error("tls: record renewal failure", "domain", c.Domain, "err", err)
	}
	r.report(ctx, o)
	return o
}

// MarkNotApplied records that domain's certificate was issued and stored but
// could not be installed, so the next renewal run installs it without ordering
// again. The issue jobs call it when Apply fails after a successful order; the
// row would otherwise say "ok" and the install would wait for the renewal window.
func MarkNotApplied(ctx context.Context, st *Store, domain string, cause error, now time.Time) error {
	c, err := st.GetCert(ctx, domain)
	if err != nil {
		return fmt.Errorf("tls: mark %s not applied: %w", domain, err)
	}
	c.LastRenewalAt = now
	c.LastRenewalResult = RenewalFailed
	c.LastRenewalError = trimError(applyFailedPrefix + cause.Error())
	if err := st.PutCert(ctx, c); err != nil {
		return fmt.Errorf("tls: mark %s not applied: %w", domain, err)
	}
	return nil
}

func (r *Renewer) report(ctx context.Context, o Outcome) {
	switch o.Result {
	case OutcomeFailed, OutcomeSkipped:
		r.log.Warn("tls: certificate renewal", "domain", o.Domain, "result", o.Result, "err", o.Error)
	default:
		r.log.Info("tls: certificate renewal", "domain", o.Domain, "result", o.Result)
	}
	if r.audit != nil {
		r.audit(ctx, o.Domain, o.Result, o.Error)
	}
}

// trimError makes an error message fit one table cell: a single line of at
// most maxErrorLength bytes, cut on a rune boundary.
func trimError(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxErrorLength {
		return s
	}
	cut := maxErrorLength - len("...")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// Start scans once shortly after it is called and then every Interval, until
// ctx ends. A panic in a scan is logged and the loop carries on.
func (r *Renewer) Start(ctx context.Context) {
	timer := time.NewTimer(r.firstDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		r.runGuarded(ctx)
		timer.Reset(r.interval)
	}
}

func (r *Renewer) runGuarded(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			r.log.Error("tls: renewal run panicked", "panic", fmt.Sprint(p))
		}
	}()
	if _, err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
		r.log.Error("tls: renewal run", "err", err)
	}
}
