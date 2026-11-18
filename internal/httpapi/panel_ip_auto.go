package httpapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	tlspkg "github.com/portbyte-id/server-panel/internal/tls"
)

// Outcomes of AutoIssuePanelIP. Everything but AutoIPIssued is a reason nothing
// was ordered, not a failure.
const (
	AutoIPIssued   = "issued"
	AutoIPSkipped  = "skipped"
	AutoIPFailed   = "failed"
	autoIPNoIssuer = "certificate issuing is not available"
)

// AutoIPResult is what one AutoIssuePanelIP call did.
type AutoIPResult struct {
	Outcome string
	// IP is the address the certificate covers; set when a public IP was found.
	IP string
	// Reason says, in a sentence, why the call skipped or failed.
	Reason string
}

// AutoIssuePanelIP orders an IP-address certificate for the panel's own
// listener when this host has a public IPv4 address on one of its interfaces,
// and installs it. detect returns that address (tlspkg.PublicIPv4 in
// production). It never replaces a certificate the operator chose: an uploaded
// one, or one issued for a host name, is left alone. Only the self-signed
// bootstrap certificate, or an earlier automatic IP certificate for an address
// the host no longer has, is replaced.
//
// Renewal needs nothing here: the row is an ordinary HTTP-01 one, so the
// renewer re-orders it and Apply swaps the listener because PanelDomain is the
// IP.
func (t *TLSDeps) AutoIssuePanelIP(ctx context.Context, detect func() (string, bool), email string) AutoIPResult {
	skip := func(ip, why string) AutoIPResult {
		return AutoIPResult{Outcome: AutoIPSkipped, IP: ip, Reason: why}
	}
	if t.Issuer == nil {
		return skip("", autoIPNoIssuer)
	}
	if t.PanelCerts == nil || t.PanelCerts() == nil {
		return skip("", "the panel listener is not running")
	}
	ip, ok := detect()
	if !ok {
		return skip("", "no public IPv4 address on this host's interfaces (behind NAT, or IPv6 only)")
	}

	cur := t.PanelDomain()
	switch {
	case cur == ip:
		return skip(ip, "the panel already has an automatic certificate for "+ip)
	case cur != "" && !tlspkg.IsIPName(cur):
		return skip(ip, "the panel's certificate was issued for "+cur)
	case cur == "" && !t.PanelCerts().IsBootstrap():
		return skip(ip, "the panel is serving a certificate that was uploaded")
	}

	if !t.begin(ip) {
		return skip(ip, "a certificate for "+ip+" is already being issued or renewed")
	}
	defer t.end(ip)

	if _, err := t.Issuer.IssueHTTP01(ctx, ip, []string{ip}, email); err != nil {
		return AutoIPResult{Outcome: AutoIPFailed, IP: ip, Reason: trimTLS(err)}
	}
	if err := t.setPanelDomain(ip); err != nil {
		return AutoIPResult{Outcome: AutoIPFailed, IP: ip,
			Reason: fmt.Sprintf("certificate issued, but recording the panel address failed: %v", err)}
	}
	if err := t.Apply(ctx, ip); err != nil {
		// The renewer installs the stored files on its next run.
		_ = tlspkg.MarkNotApplied(ctx, t.Store, ip, err, time.Now())
		return AutoIPResult{Outcome: AutoIPFailed, IP: ip,
			Reason: "the certificate was issued but is not live: " + err.Error()}
	}
	if cur != "" {
		t.dropIPCert(ctx, cur)
	}
	return AutoIPResult{Outcome: AutoIPIssued, IP: ip}
}

// dropIPCert removes the row and files of an automatic certificate the panel no
// longer uses. A leftover is harmless, so errors are ignored.
func (t *TLSDeps) dropIPCert(ctx context.Context, ip string) {
	if err := t.Store.DeleteCert(ctx, ip); err != nil && !errors.Is(err, tlspkg.ErrNotFound) {
		return
	}
	_ = tlspkg.RemoveSiteFiles(t.Root, ip)
}
