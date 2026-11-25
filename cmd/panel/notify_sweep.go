package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/httpapi"
	"github.com/portbyte-id/server-panel/internal/notify"
	"github.com/portbyte-id/server-panel/internal/sites"
	"github.com/portbyte-id/server-panel/internal/tls"
)

// Certificate expiry windows, as the dashboard uses them (§2): 14 days, or 2
// days for an IP-address certificate.
const (
	sweepWindowDays   = 14
	sweepIPWindowDays = 2
)

// sweepSource is one read-only view the sweep needs. A nil field means that
// source is not on this host; its kind is left out of the sync, so its open
// conditions are neither resolved nor raised.
type sweepSource struct {
	firewall func(ctx context.Context) (enabled bool, err error)
	sites    func(ctx context.Context) ([]sites.Detail, error)
	certs    func(ctx context.Context) ([]tls.Cert, error)
}

// notifySweep is the cron built-in notify-sweep (D8). The cron service is
// built before the sources exist, so the sources are bound afterwards.
type notifySweep struct {
	mu  sync.Mutex
	svc *notify.Service
	src sweepSource
}

// bind attaches the engine and the sources. Until it is called the built-in fails.
func (n *notifySweep) bind(svc *notify.Service, src sweepSource) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.svc, n.src = svc, src
}

// run is the built-in function: one sweep, with a line per condition.
func (n *notifySweep) run(ctx context.Context, out io.Writer) error {
	n.mu.Lock()
	svc, src := n.svc, n.src
	n.mu.Unlock()
	if svc == nil {
		return errors.New("notify sweep is not bound to the notification engine")
	}
	return sweepOnce(ctx, svc, src, out, time.Now())
}

// sweepOnce reads each available source and syncs the polled kinds. A source
// that fails aborts the sweep before any Sync, so a read error never resolves
// a condition that is still true.
func sweepOnce(ctx context.Context, sy notify.Syncer, src sweepSource, out io.Writer, now time.Time) error {
	var (
		kinds   []string
		current []notify.Event
	)

	if src.firewall != nil {
		enabled, err := src.firewall(ctx)
		if err != nil {
			return fmt.Errorf("firewall state: %w", err)
		}
		kinds = append(kinds, notify.KindFirewallDisabled)
		if !enabled {
			_, _ = fmt.Fprintln(out, "firewall: disabled")
			current = append(current, sweepEvent(notify.KindFirewallDisabled, notify.KindFirewallDisabled,
				notify.Warning, "Firewall is disabled", "", "/firewall"))
		}
	}

	if src.sites != nil {
		details, err := src.sites(ctx)
		if err != nil {
			return fmt.Errorf("site features: %w", err)
		}
		kinds = append(kinds, notify.KindSiteDetached)
		for _, d := range details {
			var detached []string
			for _, f := range d.Features {
				if !f.Attached {
					detached = append(detached, fmt.Sprint(f.Feature))
				}
			}
			if len(detached) == 0 {
				continue
			}
			_, _ = fmt.Fprintf(out, "site %s: detached %v\n", d.Domain, detached)
			current = append(current, sweepEvent(notify.KindSiteDetached, notify.KindSiteDetached+":"+d.Domain,
				notify.Warning, "Site "+d.Domain+" has detached features", fmt.Sprint(detached), "/sites/"+d.Domain))
		}
	}

	if src.certs != nil {
		certs, err := src.certs(ctx)
		if err != nil {
			return fmt.Errorf("certificates: %w", err)
		}
		kinds = append(kinds, notify.KindTLSExpiring)
		for _, c := range certs {
			window := float64(sweepWindowDays)
			if net.ParseIP(c.Domain) != nil {
				window = sweepIPWindowDays
			}
			days := c.NotAfter.Sub(now).Hours() / 24
			if days > window {
				continue
			}
			sev, subject := notify.Warning, fmt.Sprintf("Certificate for %s expires in %.0f days", c.Domain, days)
			if days < 0 {
				sev, subject = notify.Critical, "Certificate for "+c.Domain+" has expired"
			}
			_, _ = fmt.Fprintf(out, "certificate %s: %.1f days left\n", c.Domain, days)
			current = append(current, sweepEvent(notify.KindTLSExpiring, notify.KindTLSExpiring+":"+c.Domain,
				sev, subject, "", "/tls"))
		}
	}

	if len(kinds) == 0 {
		return nil
	}
	sy.Sync(ctx, "sweep", kinds, current)
	return nil
}

// sweepEvent builds one current condition. Every sweep event carries the
// source "sweep", because Sync resolves open keys of that source only.
func sweepEvent(kind, key string, sev notify.Severity, subject, body, link string) notify.Event {
	return notify.Event{Kind: kind, Key: key, Severity: sev, Subject: subject, Body: body,
		Source: "sweep", Link: link}
}

// firewallSource adapts the firewall service. A nil service (not Linux) is nil.
func firewallSource(fw httpapi.FirewallService) func(ctx context.Context) (bool, error) {
	if fw == nil {
		return nil
	}
	return func(ctx context.Context) (bool, error) {
		st, err := fw.State(ctx)
		return st.Enabled, err
	}
}

// siteSource adapts the site manager, which lists sites and reports each one's
// detached features. A lister without Detail leaves the kind out.
func siteSource(l any) func(ctx context.Context) ([]sites.Detail, error) {
	m, ok := l.(interface {
		List(ctx context.Context) ([]sites.Site, error)
		Detail(ctx context.Context, domain string) (sites.Detail, error)
	})
	if !ok {
		return nil
	}
	return func(ctx context.Context) ([]sites.Detail, error) {
		list, err := m.List(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]sites.Detail, 0, len(list))
		for _, s := range list {
			d, err := m.Detail(ctx, s.Domain)
			if err != nil {
				return nil, err
			}
			out = append(out, d)
		}
		return out, nil
	}
}

// certSource adapts the certificate store.
func certSource(t *tlsStack) func(ctx context.Context) ([]tls.Cert, error) {
	if t == nil || t.Store == nil {
		return nil
	}
	return t.Store.ListCerts
}
