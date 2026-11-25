package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/portbyte-id/server-panel/internal/audit"
	"github.com/portbyte-id/server-panel/internal/cron"
	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/notify"
	"github.com/portbyte-id/server-panel/internal/secretbox"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/sites"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/tls"
)

// tlsStack is the certificate store, the ACME issuer and the renewal job, and
// the HTTP layer's view of them.
type tlsStack struct {
	Store   *tls.Store
	Issuer  *tls.Issuer
	Renewer *tls.Renewer
	Deps    *httpapi.TLSDeps
	root    string
}

// newTLS builds the stack. Nothing here talks to a CA or to nginx. A host
// whose data/secret.key is missing gets no stack: its TLS routes answer 503 and
// its sites render without certificates, which is what they did before M6.
//
// The Deps' Sites and PanelCerts are filled in by the caller once the nginx
// stack and the listener exist; the renewer's Apply reads them lazily.
func newTLS(st *store.Store, reg *settings.Registry, layout install.Layout, auditW *audit.Writer, logger *slog.Logger, notifySvc *notify.Service) (*tlsStack, error) {
	key, err := os.ReadFile(layout.SecretKeyPath())
	if err != nil {
		return nil, fmt.Errorf("reading the secret key: %w", err)
	}
	box, err := secretbox.New(key)
	if err != nil {
		return nil, err
	}
	certStore, err := tls.NewStore(tls.Options{DB: st, Box: box})
	if err != nil {
		return nil, err
	}
	root := filepath.ToSlash(layout.Root)
	if err := tls.EnsureACMEWebroot(root); err != nil {
		// Not fatal: the first HTTP-01 order creates it again and reports why not.
		logger.Warn("creating the ACME webroot failed", "error", err)
	}
	issuer, err := tls.NewIssuer(tls.IssuerOptions{
		Store: certStore, Root: root,
		// Read on every order, so changing the directory on the Settings page
		// (staging to production) applies to the next order without a restart.
		Directory: func() string {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			v, err := reg.Str(ctx, settings.KeyTLSACMEDirectory)
			if err != nil {
				return ""
			}
			return v
		},
		// Read when an account is first registered at a CA that needs
		// External Account Binding (Google Public CA).
		Email: func() string {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			v, _ := reg.Str(ctx, settings.KeyTLSACMEEmail)
			return v
		},
		EAB: func() (string, string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			kid, _ := reg.Str(ctx, settings.KeyTLSACMEEABKid)
			hmacKey, _ := reg.Str(ctx, settings.KeyTLSACMEEABHMACKey)
			return kid, hmacKey
		},
	})
	if err != nil {
		return nil, err
	}
	deps := &httpapi.TLSDeps{Store: certStore, Issuer: issuer, Root: root}

	renewer, err := tls.NewRenewer(tls.RenewerOptions{
		Store: certStore, Issuer: issuer, Apply: deps.Apply,
		DaysBefore: func() int { return intSetting(context.Background(), reg, settings.KeyTLSRenewDaysBefore, 30) },
		Audit: func(ctx context.Context, domain, outcome, detail string) {
			switch outcome {
			case tls.OutcomeFailed:
				notifySvc.Emit(ctx, notify.Event{Kind: notify.KindTLSRenewFailed, Key: notify.KindTLSRenewFailed + ":" + domain, Subject: "Certificate renewal failed for " + domain, Body: detail, Source: "tls"})
			case tls.OutcomeRenewed:
				notifySvc.Clear(ctx, notify.Event{Kind: notify.KindTLSRenewFailed, Key: notify.KindTLSRenewFailed + ":" + domain, Subject: "Certificate renewed for " + domain, Source: "tls"})
			}
			result := audit.Failure
			if outcome == tls.OutcomeRenewed {
				result = audit.Success
			}
			err := auditW.Write(ctx, audit.Entry{
				Actor: "system", Action: "tls.renew", Target: domain, Result: result,
				Detail: map[string]any{"outcome": outcome, "error": detail},
			})
			if err != nil {
				logger.Error("audit write failed", "error", err, "action", "tls.renew")
			}
		},
		Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	deps.Renewer = renewAdapter{renewer}
	return &tlsStack{Store: certStore, Issuer: issuer, Renewer: renewer, Deps: deps, root: root}, nil
}

// state is the sites model's view of the certificate store.
func (t *tlsStack) state() sites.TLSState { return tlsState{st: t.Store, root: t.root} }

// cronBuiltins are the functions the cron service's built-in jobs run. The renewal
// is registered even on a host without a certificate store, so its job still
// shows on the Cron page and each run says why it failed.
func cronBuiltins(t *tlsStack) map[string]cron.BuiltinFunc {
	return map[string]cron.BuiltinFunc{cron.BuiltinSSLRenew: t.renewBuiltin}
}

// renewBuiltin is the cron built-in `tls.renew`: one renewal scan, with a line per
// certificate in the run's output. A certificate that failed to renew fails the
// run; the others were still renewed before it did.
func (t *tlsStack) renewBuiltin(ctx context.Context, out io.Writer) error {
	if t == nil || t.Renewer == nil {
		return errors.New("the certificate store is not available; see the daemon log")
	}
	outcomes, err := t.Renewer.RunOnce(ctx)
	counts := make(map[string]int)
	for _, o := range outcomes {
		counts[o.Result]++
		line := o.Domain + ": " + o.Result
		if o.Error != "" {
			line += ": " + o.Error
		}
		_, _ = fmt.Fprintln(out, line)
	}
	_, _ = fmt.Fprintf(out, "%d checked: %d renewed, %d failed, %d skipped, %d not due\n",
		len(outcomes), counts[tls.OutcomeRenewed], counts[tls.OutcomeFailed],
		counts[tls.OutcomeSkipped], counts[tls.OutcomeNotDue])
	if err != nil {
		return err
	}
	if n := counts[tls.OutcomeFailed]; n > 0 {
		return fmt.Errorf("%d certificate(s) failed to renew", n)
	}
	return nil
}

const (
	// autoIPFirstDelay lets the listener and nginx come up before the first
	// order, and the daemon settle after a boot.
	autoIPFirstDelay = 2 * time.Minute
	// autoIPInterval is both the retry pace after a failed order (a CA allows a
	// handful of failed validations an hour) and how soon a newly assigned
	// public address is noticed. Detection is local and free.
	autoIPInterval = 6 * time.Hour
)

// startAutoIP gives the panel an IP-address certificate on a host with a public
// IPv4 address, unless tls.panel.auto_ip is off. The setting is read on every
// pass, so turning it on or off applies without a restart.
func (t *tlsStack) startAutoIP(ctx context.Context, reg *settings.Registry, auditW *audit.Writer, logger *slog.Logger) {
	go func() {
		timer := time.NewTimer(autoIPFirstDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			t.autoIPOnce(ctx, reg, auditW, logger)
			timer.Reset(autoIPInterval)
		}
	}()
}

func (t *tlsStack) autoIPOnce(ctx context.Context, reg *settings.Registry, auditW *audit.Writer, logger *slog.Logger) {
	defer func() {
		if p := recover(); p != nil {
			logger.Error("tls: automatic IP certificate panicked", "panic", fmt.Sprint(p))
		}
	}()
	on, err := reg.Bool(ctx, settings.KeyTLSPanelAutoIP)
	if err != nil || !on {
		return
	}
	res := t.Deps.AutoIssuePanelIP(ctx, tls.PublicIPv4, "")
	switch res.Outcome {
	case httpapi.AutoIPIssued:
		logger.Info("tls: panel IP certificate issued", "ip", res.IP)
	case httpapi.AutoIPFailed:
		logger.Warn("tls: panel IP certificate failed", "ip", res.IP, "reason", res.Reason)
	default:
		logger.Debug("tls: panel IP certificate skipped", "ip", res.IP, "reason", res.Reason)
		return
	}
	result := audit.Success
	if res.Outcome == httpapi.AutoIPFailed {
		result = audit.Failure
	}
	if err := auditW.Write(ctx, audit.Entry{
		Actor: "system", Action: "tls.panel.auto_ip", Target: res.IP, Result: result,
		Detail: map[string]any{"outcome": res.Outcome, "error": res.Reason},
	}); err != nil {
		logger.Error("audit write failed", "error", err, "action", "tls.panel.auto_ip")
	}
}

// renewAdapter turns the renewer's recorded failure into the error the "renew
// now" job reports: RenewNow returns a failed order as an Outcome, not an error.
type renewAdapter struct{ r *tls.Renewer }

func (a renewAdapter) RenewNow(ctx context.Context, domain string) error {
	o, err := a.r.RenewNow(ctx, domain)
	if err != nil {
		return err
	}
	if o.Result == tls.OutcomeFailed {
		return errors.New(o.Error)
	}
	return nil
}

// tlsState implements sites.TLSState on the certificate store. A domain has a
// certificate exactly when it has a row; the files are at the paths the store
// writes them to.
type tlsState struct {
	st   *tls.Store
	root string
}

func (s tlsState) Lookup(ctx context.Context, domain string) (sites.TLSInfo, bool, error) {
	c, err := s.st.GetCert(ctx, domain)
	if errors.Is(err, tls.ErrNotFound) {
		return sites.TLSInfo{}, false, nil
	}
	if err != nil {
		return sites.TLSInfo{}, false, err
	}
	certFile, keyFile := tls.SitePaths(s.root, domain)
	return sites.TLSInfo{CertFile: certFile, KeyFile: keyFile, RedirectHTTP: c.RedirectHTTP}, true, nil
}
