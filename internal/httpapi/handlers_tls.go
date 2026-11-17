package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/jobs"
	"github.com/portbyte-id/server-panel/internal/sites"
	tlspkg "github.com/portbyte-id/server-panel/internal/tls"
)

// TLSSites is the part of sites.Manager the TLS routes use. *sites.Manager
// implements it; tests substitute a fake.
type TLSSites interface {
	Get(ctx context.Context, domain string) (sites.Site, error)
	RefreshTLS(ctx context.Context, domain string) (confedit.Result, error)
}

// TLSIssuer is the part of tls.Issuer the routes use.
type TLSIssuer interface {
	IssueHTTP01(ctx context.Context, domain string, names []string, email string) (tlspkg.Result, error)
	IssueDNS01(ctx context.Context, domain string, names []string, provider, email string) (tlspkg.Result, error)
}

// TLSRenewer renews one certificate now. The daemon adapts *tls.Renewer to it.
type TLSRenewer interface {
	RenewNow(ctx context.Context, domain string) error
}

// panelDomainFile is the name, inside the SSL directory, of the one-line file
// that records which certificate row feeds the panel's own listener.
const panelDomainFile = "panel-cert-domain"

// TLSDeps is what the TLS routes run on. Nil Deps.TLS leaves them answering 503.
// Sites and PanelCerts may be nil on their own: the site routes then answer 503
// (no nginx on this host) while providers and the panel certificate still work.
type TLSDeps struct {
	Store   *tlspkg.Store
	Issuer  TLSIssuer
	Renewer TLSRenewer
	Sites   TLSSites
	// Root is the install root (`/www`): certificate files live under it.
	Root string
	// Providers constructs a DNS provider, which is how a PUT validates the
	// credentials it was given. Nil means tls.ProviderFactory.
	Providers func(kind string, credentials []byte) (tlspkg.DNSProvider, error)
	// PanelCerts returns the listener's certificate reloader. It is a function
	// because the listener is built after the handler.
	PanelCerts func() *CertReloader
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu   sync.Mutex
	busy map[string]bool
}

func (t *TLSDeps) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *TLSDeps) providers() func(string, []byte) (tlspkg.DNSProvider, error) {
	if t.Providers != nil {
		return t.Providers
	}
	return tlspkg.ProviderFactory
}

// begin marks a domain as having an issuance in flight; two orders for one name
// at once would race on the files and burn the CA's rate limit.
func (t *TLSDeps) begin(domain string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.busy == nil {
		t.busy = map[string]bool{}
	}
	if t.busy[domain] {
		return false
	}
	t.busy[domain] = true
	return true
}

func (t *TLSDeps) end(domain string) {
	t.mu.Lock()
	delete(t.busy, domain)
	t.mu.Unlock()
}

// PanelDomain is the certificate row the panel's listener is fed from, or ""
// when the panel certificate was uploaded or is the self-signed one. The marker
// is the file `<root>/panel/ssl/panel-cert-domain`, one line holding the domain:
// it is durable across restarts, tiny, and needs no schema. The renewer's Apply
// reads it to decide whether a renewed certificate must also be hot-swapped
// into the listener.
func (t *TLSDeps) PanelDomain() string {
	b, err := os.ReadFile(filepath.FromSlash(path.Join(tlspkg.SSLDir(t.Root), panelDomainFile)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (t *TLSDeps) setPanelDomain(domain string) error {
	p := filepath.FromSlash(path.Join(tlspkg.SSLDir(t.Root), panelDomainFile))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(domain+"\n"), 0o644)
}

func (t *TLSDeps) clearPanelDomain() error {
	err := os.Remove(filepath.FromSlash(path.Join(tlspkg.SSLDir(t.Root), panelDomainFile)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Apply makes the stored certificate of domain live: it re-renders the site's
// tls.conf when a managed site has that domain, and hot-swaps the panel
// listener when the domain feeds the panel. Both happen when both are true. It
// is what the renewal job and the issue jobs call after new files are on disk.
func (t *TLSDeps) Apply(ctx context.Context, domain string) error {
	var errs []error
	if t.PanelDomain() == domain {
		if err := t.replacePanel(domain); err != nil {
			errs = append(errs, fmt.Errorf("panel certificate: %w", err))
		}
	}
	if t.Sites != nil {
		_, err := t.Sites.Get(ctx, domain)
		switch {
		case err == nil:
			if _, err := t.Sites.RefreshTLS(ctx, domain); err != nil {
				errs = append(errs, err)
			}
		case errors.Is(err, sites.ErrNotFound):
		default:
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (t *TLSDeps) replacePanel(domain string) error {
	if t.PanelCerts == nil || t.PanelCerts() == nil {
		return errors.New("the panel listener is not available")
	}
	certFile, keyFile := tlspkg.SitePaths(t.Root, domain)
	certPEM, err := os.ReadFile(filepath.FromSlash(certFile))
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(filepath.FromSlash(keyFile))
	if err != nil {
		return err
	}
	return t.PanelCerts().Replace(certPEM, keyPEM, t.now)
}

// CertView is a certificate as the API shows it. It carries no key material.
type CertView struct {
	Domain            string     `json:"domain"`
	Source            string     `json:"source"`
	Names             []string   `json:"names"`
	NotBefore         time.Time  `json:"not_before"`
	NotAfter          time.Time  `json:"not_after"`
	Issuer            string     `json:"issuer"`
	Serial            string     `json:"serial"`
	DaysLeft          int        `json:"days_left"`
	LastRenewalAt     *time.Time `json:"last_renewal_at"`
	LastRenewalResult string     `json:"last_renewal_result"`
	LastRenewalError  string     `json:"last_renewal_error"`
	RedirectHTTP      bool       `json:"redirect_http"`
	Provider          string     `json:"provider"`
}

func (t *TLSDeps) view(c tlspkg.Cert) CertView {
	v := CertView{
		Domain: c.Domain, Source: c.Source, Names: c.Names,
		NotBefore: c.NotBefore, NotAfter: c.NotAfter, Issuer: c.Issuer, Serial: c.Serial,
		DaysLeft:          daysLeft(c.NotAfter.Sub(t.now())),
		LastRenewalResult: c.LastRenewalResult, LastRenewalError: c.LastRenewalError,
		RedirectHTTP: c.RedirectHTTP, Provider: c.Provider,
	}
	if v.Names == nil {
		v.Names = []string{}
	}
	if !c.LastRenewalAt.IsZero() {
		at := c.LastRenewalAt
		v.LastRenewalAt = &at
	}
	return v
}

type tlsWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// tlsRoutes registers the routes of development plan step 6.8. They are
// registered even when Deps.TLS is nil, so the UI gets a 503 that says why.
func (s *server) tlsRoutes(mux *http.ServeMux) {
	h := func(fn func(*TLSDeps, http.ResponseWriter, *http.Request)) http.Handler {
		return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.TLS == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"TLS is not available on this host; see the daemon log")
				return
			}
			fn(s.deps.TLS, w, r)
		}))
	}
	// hs is h for the routes that act on a managed site: it resolves the site
	// first, so an unknown domain is a 404 and a host without nginx is a 503.
	hs := func(fn func(*TLSDeps, sites.Site, http.ResponseWriter, *http.Request)) http.Handler {
		return h(func(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
			if t.Sites == nil {
				writeError(w, http.StatusServiceUnavailable, errUnavailable,
					"sites are not available on this host, so a site certificate cannot be applied; see the daemon log")
				return
			}
			site, err := t.Sites.Get(r.Context(), r.PathValue("domain"))
			if err != nil {
				s.writeNginxError(w, r, err)
				return
			}
			noteTarget(r, site.Domain)
			fn(t, site, w, r)
		})
	}

	mux.Handle("GET /api/sites/{domain}/tls", hs(s.tlsSiteGet))
	mux.Handle("POST /api/sites/{domain}/tls/upload", hs(s.tlsSiteUpload))
	mux.Handle("POST /api/sites/{domain}/tls/issue", hs(s.tlsSiteIssue))
	mux.Handle("POST /api/sites/{domain}/tls/renew", h(s.tlsRenew))
	mux.Handle("PUT /api/sites/{domain}/tls/redirect", hs(s.tlsSiteRedirect))
	mux.Handle("DELETE /api/sites/{domain}/tls", hs(s.tlsSiteDelete))

	mux.Handle("GET /api/tls/certs", h(s.tlsCerts))
	mux.Handle("GET /api/tls/providers", h(s.tlsProvidersList))
	mux.Handle("PUT /api/tls/providers/{name}", h(s.tlsProviderPut))
	mux.Handle("DELETE /api/tls/providers/{name}", h(s.tlsProviderDelete))

	mux.Handle("GET /api/panel/cert", h(s.panelCertGet))
	mux.Handle("POST /api/panel/cert/upload", h(s.panelCertUpload))
	mux.Handle("POST /api/panel/cert/issue", h(s.panelCertIssue))
}

// tlsFail maps an error from the tls package, then from sites and confedit.
func (s *server) tlsFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, tlspkg.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound, trimTLS(err))
	case errors.Is(err, tlspkg.ErrInvalid), errors.Is(err, tlspkg.ErrBadDomain):
		writeError(w, http.StatusBadRequest, errInvalidInput, trimTLS(err))
	default:
		s.writeNginxError(w, r, err)
	}
}

func trimTLS(err error) string {
	return strings.TrimPrefix(strings.TrimPrefix(err.Error(), "httpapi: "), "tls: ")
}

func invalidField(w http.ResponseWriter, field, detail string) {
	writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errInvalidInput, Detail: detail, Field: field})
}

// applyFailureText is a sentence for a job's error line about why a change did
// not reach nginx; the validator's own output follows on separate lines.
func applyFailureText(err error) (text string, output string) {
	var (
		ae       *confedit.ApplyError
		detached *sites.DetachedError
	)
	switch {
	case errors.As(err, &ae):
		if ae.Stage == confedit.StageValidate {
			return "nginx rejected the configuration; the previous files are back in place", ae.Output
		}
		return "the change failed at the " + string(ae.Stage) + " stage and was rolled back", ae.Output
	case errors.As(err, &detached):
		return strings.TrimPrefix(detached.Error(), "sites: "), ""
	default:
		return err.Error(), ""
	}
}

// --- per-site routes ---------------------------------------------------------

func (s *server) tlsSiteGet(t *TLSDeps, site sites.Site, w http.ResponseWriter, r *http.Request) {
	c, err := t.Store.GetCert(r.Context(), site.Domain)
	if errors.Is(err, tlspkg.ErrNotFound) {
		writeJSON(w, http.StatusOK, struct {
			Cert *CertView `json:"cert"`
		}{})
		return
	}
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	v := t.view(c)
	writeJSON(w, http.StatusOK, struct {
		Cert *CertView `json:"cert"`
	}{&v})
}

// siteSnapshot is a site's certificate as it was before a change: the row and
// both files. restore puts it back, or removes everything when there was none.
type siteSnapshot struct {
	row     *tlspkg.Cert
	certPEM []byte
	keyPEM  []byte
}

func (t *TLSDeps) snapshot(ctx context.Context, domain string) (siteSnapshot, error) {
	var snap siteSnapshot
	c, err := t.Store.GetCert(ctx, domain)
	if errors.Is(err, tlspkg.ErrNotFound) {
		return snap, nil
	}
	if err != nil {
		return snap, err
	}
	snap.row = &c
	certFile, keyFile := tlspkg.SitePaths(t.Root, domain)
	// A missing file is not fatal: the row is restored without it.
	snap.certPEM, _ = os.ReadFile(filepath.FromSlash(certFile))
	snap.keyPEM, _ = os.ReadFile(filepath.FromSlash(keyFile))
	return snap, nil
}

func (t *TLSDeps) restore(ctx context.Context, domain string, snap siteSnapshot) error {
	ctx = context.WithoutCancel(ctx)
	if snap.row == nil {
		var errs []error
		if err := t.Store.DeleteCert(ctx, domain); err != nil && !errors.Is(err, tlspkg.ErrNotFound) {
			errs = append(errs, err)
		}
		if err := tlspkg.RemoveSiteFiles(t.Root, domain); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	if len(snap.certPEM) > 0 && len(snap.keyPEM) > 0 {
		if err := tlspkg.WriteSiteFiles(t.Root, domain, snap.certPEM, snap.keyPEM); err != nil {
			return err
		}
	}
	return t.Store.PutCert(ctx, *snap.row)
}

type uploadRequest struct {
	Certificate string `json:"certificate"`
	Chain       string `json:"chain"`
	PrivateKey  string `json:"private_key"`
}

func (s *server) tlsSiteUpload(t *TLSDeps, site sites.Site, w http.ResponseWriter, r *http.Request) {
	var req uploadRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	if strings.TrimSpace(req.Certificate) == "" {
		invalidField(w, "certificate", "paste the certificate (PEM)")
		return
	}
	if strings.TrimSpace(req.PrivateKey) == "" {
		invalidField(w, "private_key", "paste the private key (PEM)")
		return
	}
	parsed, warns, err := tlspkg.ValidateUpload([]byte(req.Certificate), []byte(req.Chain), []byte(req.PrivateKey), site.Domain, t.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput, trimTLS(err))
		return
	}
	snap, err := t.snapshot(r.Context(), site.Domain)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	row := tlspkg.Cert{
		Domain: site.Domain, Source: tlspkg.SourceUpload, Names: parsed.Names,
		NotBefore: parsed.NotBefore, NotAfter: parsed.NotAfter, Issuer: parsed.Issuer, Serial: parsed.Serial,
	}
	if snap.row != nil {
		row.RedirectHTTP = snap.row.RedirectHTTP
	}
	if err := tlspkg.WriteSiteFiles(t.Root, site.Domain, parsed.FullchainPEM, parsed.KeyPEM); err != nil {
		s.writeInternal(w, r, err)
		return
	}
	if err := t.Store.PutCert(r.Context(), row); err != nil {
		_ = t.restore(r.Context(), site.Domain, snap)
		s.writeInternal(w, r, err)
		return
	}
	res, err := t.Sites.RefreshTLS(r.Context(), site.Domain)
	if err != nil {
		if rerr := t.restore(r.Context(), site.Domain, snap); rerr != nil {
			s.logger.ErrorContext(r.Context(), "restoring the previous certificate failed",
				"domain", site.Domain, "error", rerr, "request_id", requestIDFrom(r.Context()))
		}
		s.writeNginxError(w, r, err)
		return
	}
	note(r, "source", tlspkg.SourceUpload)

	out := make([]tlsWarning, 0, len(warns))
	for _, wn := range warns {
		out = append(out, tlsWarning{Code: wn.Code, Message: wn.Message})
	}
	for _, m := range res.Warnings {
		out = append(out, tlsWarning{Code: "apply_warning", Message: m})
	}
	stored, err := t.Store.GetCert(r.Context(), site.Domain)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Cert     CertView     `json:"cert"`
		Warnings []tlsWarning `json:"warnings"`
	}{t.view(stored), out})
}

type issueRequest struct {
	Method   string   `json:"method"`
	Names    []string `json:"names"`
	Provider string   `json:"provider"`
	Email    string   `json:"email"`
}

// issuePlan is a validated issue request.
type issuePlan struct {
	method   string
	names    []string
	provider string
	email    string
}

// planIssue validates the parts of an issue request that do not need the
// network, so a typo is a 400 now and not a failed job a minute later. domain
// is the name the certificate must cover.
func (t *TLSDeps) planIssue(ctx context.Context, w http.ResponseWriter, req issueRequest, domain string, defaultNames []string) (issuePlan, bool) {
	p := issuePlan{method: req.Method, provider: strings.TrimSpace(req.Provider), email: strings.TrimSpace(req.Email)}
	switch p.method {
	case tlspkg.SourceHTTP01, tlspkg.SourceDNS01:
	default:
		invalidField(w, "method", `method must be "http01" or "dns01"`)
		return p, false
	}
	names := req.Names
	if len(names) == 0 {
		for _, n := range defaultNames {
			if !strings.HasPrefix(n, "*.") {
				names = append(names, n)
			}
		}
	}
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		host, wild := strings.CutPrefix(n, "*.")
		if wild && p.method != tlspkg.SourceDNS01 {
			invalidField(w, "names", n+" is a wildcard, and a wildcard certificate can only be issued with DNS-01: choose DNS-01 and a DNS provider, or list the names without the wildcard")
			return p, false
		}
		if err := tlspkg.CheckDomain(host); err != nil {
			invalidField(w, "names", n+" is not a valid host name")
			return p, false
		}
		if !seen[n] {
			seen[n] = true
			p.names = append(p.names, n)
		}
	}
	if len(p.names) == 0 {
		invalidField(w, "names", "name at least one host the certificate must cover")
		return p, false
	}
	if !coversName(p.names, domain) {
		invalidField(w, "names", "none of the names covers "+domain)
		return p, false
	}
	if p.email != "" && !strings.Contains(p.email, "@") {
		invalidField(w, "email", "that is not an email address")
		return p, false
	}
	if p.method == tlspkg.SourceDNS01 {
		if p.provider == "" {
			invalidField(w, "provider", "choose the DNS provider that holds the zone")
			return p, false
		}
		list, err := t.Store.ListProviders(ctx)
		if err != nil {
			writeError(w, http.StatusInternalServerError, errInternal, "the request could not be completed")
			return p, false
		}
		found := false
		for _, pi := range list {
			if pi.Name == p.provider {
				found = true
			}
		}
		if !found {
			invalidField(w, "provider", "no DNS provider is named "+p.provider+"; add it under DNS providers first")
			return p, false
		}
	}
	return p, true
}

// coversName reports whether any name is domain itself or a wildcard that
// matches it one label deep.
func coversName(names []string, domain string) bool {
	for _, n := range names {
		if n == domain {
			return true
		}
		if suffix, ok := strings.CutPrefix(n, "*"); ok {
			if rest, ok := strings.CutSuffix(domain, suffix); ok && rest != "" && !strings.Contains(rest, ".") {
				return true
			}
		}
	}
	return false
}

func (s *server) tlsSiteIssue(t *TLSDeps, site sites.Site, w http.ResponseWriter, r *http.Request) {
	if t.Issuer == nil {
		writeError(w, http.StatusServiceUnavailable, errUnavailable, "certificate issuing is not available on this host; see the daemon log")
		return
	}
	var req issueRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	plan, ok := t.planIssue(r.Context(), w, req, site.Domain, append([]string{site.Domain}, site.Aliases...))
	if !ok {
		return
	}
	note(r, "method", plan.method)
	s.startIssue(t, w, r, site.Domain, plan, false)
}

// startIssue runs an issuance as a job: order, then Apply. panel marks the
// panel's own certificate, whose domain is recorded once the order succeeds.
func (s *server) startIssue(t *TLSDeps, w http.ResponseWriter, r *http.Request, domain string, plan issuePlan, panel bool) {
	if !t.begin(domain) {
		writeError(w, http.StatusConflict, errConflict, "a certificate for "+domain+" is already being issued or renewed")
		return
	}
	kind := "tls.issue"
	if panel {
		kind = "tls.panel.issue"
	}
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), kind,
		func(ctx context.Context, out *jobs.Writer) error {
			defer t.end(domain)
			label := "HTTP-01"
			if plan.method == tlspkg.SourceDNS01 {
				label = "DNS-01 with " + plan.provider
			}
			_ = out.Printf("Requesting a certificate for %s by %s", strings.Join(plan.names, ", "), label)
			var (
				res tlspkg.Result
				err error
			)
			if plan.method == tlspkg.SourceHTTP01 {
				_ = out.Line("Checking that the CA can reach the names over HTTP, then ordering")
				res, err = t.Issuer.IssueHTTP01(ctx, domain, plan.names, plan.email)
			} else {
				_ = out.Line("Publishing the DNS challenge records, then ordering")
				res, err = t.Issuer.IssueDNS01(ctx, domain, plan.names, plan.provider, plan.email)
			}
			if err != nil {
				return errors.New(trimTLS(err))
			}
			_ = out.Printf("Issued by %s: serial %s, valid until %s", res.Issuer, res.Serial, res.NotAfter.UTC().Format(time.DateOnly))
			if panel {
				if err := t.setPanelDomain(domain); err != nil {
					return fmt.Errorf("certificate issued, but recording the panel hostname failed: %w", err)
				}
				_ = out.Line("Loading the certificate into the panel listener")
			} else {
				_ = out.Line("Updating the site's nginx configuration")
			}
			if err := t.Apply(ctx, domain); err != nil {
				text, output := applyFailureText(err)
				for _, l := range strings.Split(strings.TrimSpace(output), "\n") {
					if l != "" {
						_ = out.Line(l)
					}
				}
				// Let the renewal job install the stored files on its next run.
				if merr := tlspkg.MarkNotApplied(ctx, t.Store, domain, err, time.Now()); merr != nil {
					_ = out.Printf("Could not record the failed install: %v", merr)
				}
				return fmt.Errorf("the certificate was issued but is not live: %s", text)
			}
			_ = out.Line("Done: the new certificate is live")
			return nil
		})
	if err != nil {
		t.end(domain)
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		JobID string   `json:"job_id"`
		Job   jobs.Job `json:"job"`
	}{job.ID, job})
}

func (s *server) tlsRenew(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
	if t.Renewer == nil {
		writeError(w, http.StatusServiceUnavailable, errUnavailable, "certificate renewal is not available on this host; see the daemon log")
		return
	}
	domain := strings.ToLower(r.PathValue("domain"))
	c, err := t.Store.GetCert(r.Context(), domain)
	if err != nil {
		s.tlsFail(w, r, err)
		return
	}
	noteTarget(r, domain)
	if c.Source == tlspkg.SourceUpload {
		writeError(w, http.StatusConflict, errConflict,
			"this certificate was uploaded, so it cannot be renewed here; upload a new one or issue one with HTTP-01 or DNS-01")
		return
	}
	if !t.begin(domain) {
		writeError(w, http.StatusConflict, errConflict, "a certificate for "+domain+" is already being issued or renewed")
		return
	}
	job, err := s.deps.Jobs.Start(context.WithoutCancel(r.Context()), "tls.renew",
		func(ctx context.Context, out *jobs.Writer) error {
			defer t.end(domain)
			_ = out.Printf("Renewing the certificate for %s (%s)", domain, c.Source)
			if err := t.Renewer.RenewNow(ctx, domain); err != nil {
				if errors.Is(err, tlspkg.ErrNotRenewable) {
					return errors.New("this certificate cannot be renewed automatically")
				}
				text, output := applyFailureText(err)
				for _, l := range strings.Split(strings.TrimSpace(output), "\n") {
					if l != "" {
						_ = out.Line(l)
					}
				}
				return errors.New(strings.TrimPrefix(text, "tls: "))
			}
			_ = out.Line("Done: the renewed certificate is live")
			return nil
		})
	if err != nil {
		t.end(domain)
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		JobID string   `json:"job_id"`
		Job   jobs.Job `json:"job"`
	}{job.ID, job})
}

func (s *server) tlsSiteRedirect(t *TLSDeps, site sites.Site, w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectHTTP *bool `json:"redirect_http"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	if req.RedirectHTTP == nil {
		invalidField(w, "redirect_http", "redirect_http must be true or false")
		return
	}
	snap, err := t.snapshot(r.Context(), site.Domain)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	if snap.row == nil {
		writeError(w, http.StatusNotFound, errNotFound, "this site has no certificate; issue or upload one before turning on the redirect")
		return
	}
	row := *snap.row
	row.RedirectHTTP = *req.RedirectHTTP
	if err := t.Store.PutCert(r.Context(), row); err != nil {
		s.writeInternal(w, r, err)
		return
	}
	if _, err := t.Sites.RefreshTLS(r.Context(), site.Domain); err != nil {
		if rerr := t.Store.PutCert(context.WithoutCancel(r.Context()), *snap.row); rerr != nil {
			s.logger.ErrorContext(r.Context(), "restoring the redirect setting failed",
				"domain", site.Domain, "error", rerr, "request_id", requestIDFrom(r.Context()))
		}
		s.writeNginxError(w, r, err)
		return
	}
	note(r, "redirect_http", *req.RedirectHTTP)
	stored, err := t.Store.GetCert(r.Context(), site.Domain)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t.view(stored))
}

func (s *server) tlsSiteDelete(t *TLSDeps, site sites.Site, w http.ResponseWriter, r *http.Request) {
	if t.PanelDomain() == site.Domain {
		writeError(w, http.StatusConflict, errConflict,
			site.Domain+" is also the panel's own hostname and its certificate feeds the panel; replace the panel certificate first")
		return
	}
	snap, err := t.snapshot(r.Context(), site.Domain)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	if snap.row == nil {
		writeError(w, http.StatusNotFound, errNotFound, "this site has no certificate")
		return
	}
	// The row goes first: tls.conf is rendered from it, so the refresh below
	// writes the plain-HTTP configuration. The files stay until nginx has let go.
	if err := t.Store.DeleteCert(r.Context(), site.Domain); err != nil {
		s.tlsFail(w, r, err)
		return
	}
	if _, err := t.Sites.RefreshTLS(r.Context(), site.Domain); err != nil {
		if rerr := t.Store.PutCert(context.WithoutCancel(r.Context()), *snap.row); rerr != nil {
			s.logger.ErrorContext(r.Context(), "restoring the certificate row failed",
				"domain", site.Domain, "error", rerr, "request_id", requestIDFrom(r.Context()))
		}
		s.writeNginxError(w, r, err)
		return
	}
	if err := tlspkg.RemoveSiteFiles(t.Root, site.Domain); err != nil {
		s.logger.WarnContext(r.Context(), "certificate files were not removed",
			"domain", site.Domain, "error", err, "request_id", requestIDFrom(r.Context()))
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- listings and providers --------------------------------------------------

func (s *server) tlsCerts(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
	list, err := t.Store.ListCerts(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	views := make([]CertView, 0, len(list))
	for _, c := range list {
		views = append(views, t.view(c))
	}
	writeJSON(w, http.StatusOK, struct {
		Certs []CertView `json:"certs"`
	}{views})
}

type providerView struct {
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *server) tlsProvidersList(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
	list, err := t.Store.ListProviders(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	views := make([]providerView, 0, len(list))
	for _, p := range list {
		views = append(views, providerView{p.Name, p.Kind, p.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, struct {
		Providers []providerView `json:"providers"`
		Kinds     []string       `json:"kinds"`
	}{views, tlspkg.DNSProviderKinds()})
}

func (s *server) tlsProviderPut(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Kind        string            `json:"kind"`
		Credentials map[string]string `json:"credentials"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	noteTarget(r, "dns-provider:"+name)
	note(r, "kind", req.Kind)
	if len(req.Credentials) == 0 {
		invalidField(w, "credentials", "credentials are required")
		return
	}
	creds, err := json.Marshal(req.Credentials)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	// Constructing the provider is the validation: the kind must exist and the
	// credentials must have the shape that kind wants. Nothing is sent to the
	// DNS host here; a wrong token surfaces when an order uses it.
	if _, err := t.providers()(req.Kind, creds); err != nil {
		msg := trimTLS(err)
		for _, v := range req.Credentials {
			if v != "" {
				msg = strings.ReplaceAll(msg, v, "[redacted]")
			}
		}
		invalidField(w, "credentials", msg)
		return
	}
	if err := t.Store.PutProvider(r.Context(), tlspkg.Provider{Name: name, Kind: req.Kind, Credentials: creds}); err != nil {
		if errors.Is(err, tlspkg.ErrInvalid) {
			invalidField(w, "name", "a provider name is lowercase letters, digits, - and _, starting with a letter or digit")
			return
		}
		s.writeInternal(w, r, err)
		return
	}
	list, err := t.Store.ListProviders(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	for _, p := range list {
		if p.Name == name {
			writeJSON(w, http.StatusOK, providerView{p.Name, p.Kind, p.UpdatedAt})
			return
		}
	}
	s.writeInternal(w, r, errors.New("provider vanished after it was stored"))
}

func (s *server) tlsProviderDelete(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	noteTarget(r, "dns-provider:"+name)
	certs, err := t.Store.ListCerts(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	var using []string
	for _, c := range certs {
		if c.Provider == name {
			using = append(using, c.Domain)
		}
	}
	if len(using) > 0 {
		writeError(w, http.StatusConflict, errConflict,
			"certificates still renew through this provider: "+strings.Join(using, ", "))
		return
	}
	if err := t.Store.DeleteProvider(r.Context(), name); err != nil {
		s.tlsFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- the panel's own certificate ---------------------------------------------

func (s *server) panelCerts(t *TLSDeps, w http.ResponseWriter) *CertReloader {
	if t.PanelCerts != nil {
		if c := t.PanelCerts(); c != nil {
			return c
		}
	}
	writeError(w, http.StatusServiceUnavailable, errUnavailable, "the panel listener is not running, so its certificate cannot be changed")
	return nil
}

func (s *server) panelCertGet(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
	c := s.panelCerts(t, w)
	if c == nil {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Cert              CertInfo `json:"cert"`
		PreviousAvailable bool     `json:"previous_available"`
		// ManagedDomain is the hostname whose issued certificate the panel
		// serves and keeps renewed; empty for an uploaded or self-signed one.
		ManagedDomain string `json:"managed_domain"`
	}{c.Current(), c.HasPrevious(), t.PanelDomain()})
}

func (s *server) panelCertUpload(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
	c := s.panelCerts(t, w)
	if c == nil {
		return
	}
	var req uploadRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	if strings.TrimSpace(req.Certificate) == "" {
		invalidField(w, "certificate", "paste the certificate (PEM)")
		return
	}
	if strings.TrimSpace(req.PrivateKey) == "" {
		invalidField(w, "private_key", "paste the private key (PEM)")
		return
	}
	pemChain := strings.TrimSpace(req.Certificate) + "\n"
	if strings.TrimSpace(req.Chain) != "" {
		pemChain += strings.TrimSpace(req.Chain) + "\n"
	}
	if err := c.Replace([]byte(pemChain), []byte(req.PrivateKey), t.now); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput, trimTLS(err))
		return
	}
	// An uploaded certificate ends auto-management of any issued one, or the
	// renewer would overwrite the upload at the next renewal.
	if warn := t.releasePanelDomain(r.Context()); warn != "" {
		s.logger.WarnContext(r.Context(), warn, "request_id", requestIDFrom(r.Context()))
	}
	writeJSON(w, http.StatusOK, c.Current())
}

// releasePanelDomain stops feeding the panel from an issued certificate. The
// row and files are deleted too unless a managed site shares the domain.
func (t *TLSDeps) releasePanelDomain(ctx context.Context) string {
	domain := t.PanelDomain()
	if domain == "" {
		return ""
	}
	if err := t.clearPanelDomain(); err != nil {
		return "clearing the panel hostname marker failed: " + err.Error()
	}
	if t.Sites != nil {
		if _, err := t.Sites.Get(ctx, domain); err == nil {
			return ""
		}
	}
	var msgs []string
	if err := t.Store.DeleteCert(ctx, domain); err != nil && !errors.Is(err, tlspkg.ErrNotFound) {
		msgs = append(msgs, err.Error())
	}
	if err := tlspkg.RemoveSiteFiles(t.Root, domain); err != nil {
		msgs = append(msgs, err.Error())
	}
	if len(msgs) > 0 {
		return "removing the replaced panel certificate failed: " + strings.Join(msgs, "; ")
	}
	return ""
}

func (s *server) panelCertIssue(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
	if s.panelCerts(t, w) == nil {
		return
	}
	if t.Issuer == nil {
		writeError(w, http.StatusServiceUnavailable, errUnavailable, "certificate issuing is not available on this host; see the daemon log")
		return
	}
	var req struct {
		Hostname string `json:"hostname"`
		issueRequest
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	host := strings.ToLower(strings.TrimSpace(req.Hostname))
	if err := tlspkg.CheckDomain(host); err != nil {
		invalidField(w, "hostname", "enter the panel's host name, such as panel.example.com")
		return
	}
	req.Names = nil
	plan, ok := t.planIssue(r.Context(), w, req.issueRequest, host, []string{host})
	if !ok {
		return
	}
	if plan.method == tlspkg.SourceHTTP01 {
		// The challenge file is served by nginx from the ACME webroot, and only
		// a managed site has a server block for the name.
		has := false
		if t.Sites != nil {
			_, err := t.Sites.Get(r.Context(), host)
			has = err == nil
		}
		if !has {
			writeError(w, http.StatusConflict, errConflict,
				"HTTP-01 needs nginx to answer for "+host+", and no site has that domain: create a site for the panel hostname first, or use DNS-01")
			return
		}
	}
	note(r, "method", plan.method)
	noteTarget(r, host)
	s.startIssue(t, w, r, host, plan, true)
}

// removeSiteCert deletes the certificate row and files of a site that was just
// deleted. The panel's own certificate is kept when the site shared its domain.
// The return value is a warning for the response, "" when all went well: the
// site is already gone, so a leftover here must not fail the request.
func (t *TLSDeps) removeSiteCert(ctx context.Context, domain string) string {
	domain = strings.ToLower(domain)
	if tlspkg.CheckDomain(domain) != nil || t.PanelDomain() == domain {
		return ""
	}
	var msgs []string
	if err := t.Store.DeleteCert(ctx, domain); err != nil && !errors.Is(err, tlspkg.ErrNotFound) {
		msgs = append(msgs, err.Error())
	}
	if err := tlspkg.RemoveSiteFiles(t.Root, domain); err != nil {
		msgs = append(msgs, err.Error())
	}
	if len(msgs) > 0 {
		return "the site was deleted but its certificate was not fully removed: " + strings.Join(msgs, "; ")
	}
	return ""
}

// daysLeft is d in whole days, rounded down, so a certificate that expired an
// hour ago is -1 and one with an hour left is 0.
func daysLeft(d time.Duration) int {
	days := int(d / (24 * time.Hour))
	if d < 0 && d%(24*time.Hour) != 0 {
		days--
	}
	return days
}
