package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/portbyte-id/server-panel/internal/sites"
	tlspkg "github.com/portbyte-id/server-panel/internal/tls"
)

// Certificates for a host name, with or without a site. The store keys a
// certificate by its host name, so one can be made before any site exists; a
// site whose names it covers reuses it (see reuseForSite). Upload and DNS-01
// need no site. HTTP-01 does, because nginx answers the challenge only for a
// name that a site serves.

type certUploadRequest struct {
	Domain string `json:"domain"`
	uploadRequest
}

type certIssueRequest struct {
	Domain string `json:"domain"`
	issueRequest
}

// certDomain reads the host name a standalone certificate is for.
func certDomain(w http.ResponseWriter, raw string) (string, bool) {
	domain := strings.ToLower(strings.TrimSpace(raw))
	if err := tlspkg.CheckDomain(domain); err != nil {
		invalidField(w, "domain", "enter the host name the certificate covers, such as shop.example.com")
		return "", false
	}
	return domain, true
}

// siteHas reports whether a managed site serves the domain. A host without
// nginx has no sites.
func (t *TLSDeps) siteHas(ctx context.Context, domain string) (bool, error) {
	if t.Sites == nil {
		return false, nil
	}
	_, err := t.Sites.Get(ctx, domain)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sites.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// tlsCertUpload stores an uploaded certificate for a host name. When a site
// serves the name, its nginx configuration is refreshed as the site upload does.
func (s *server) tlsCertUpload(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
	var req certUploadRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	domain, ok := certDomain(w, req.Domain)
	if !ok {
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
	if t.PanelDomain() == domain {
		writeError(w, http.StatusConflict, errConflict,
			domain+" is the panel's own hostname; replace the panel certificate under Settings instead")
		return
	}
	parsed, warns, err := tlspkg.ValidateUpload([]byte(req.Certificate), []byte(req.Chain), []byte(req.PrivateKey), domain, t.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput, trimTLS(err))
		return
	}
	ctx := r.Context()
	hasSite, err := t.siteHas(ctx, domain)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	snap, err := t.snapshot(ctx, domain)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	row := tlspkg.Cert{
		Domain: domain, Source: tlspkg.SourceUpload, Names: parsed.Names,
		NotBefore: parsed.NotBefore, NotAfter: parsed.NotAfter, Issuer: parsed.Issuer, Serial: parsed.Serial,
	}
	if snap.row != nil {
		row.RedirectHTTP = snap.row.RedirectHTTP
	}
	if err := tlspkg.WriteSiteFiles(t.Root, domain, parsed.FullchainPEM, parsed.KeyPEM); err != nil {
		s.writeInternal(w, r, err)
		return
	}
	if err := t.Store.PutCert(ctx, row); err != nil {
		_ = t.restore(ctx, domain, snap)
		s.writeInternal(w, r, err)
		return
	}
	var applyWarns []tlsWarning
	if hasSite {
		res, err := t.Sites.RefreshTLS(ctx, domain)
		if err != nil {
			if rerr := t.restore(ctx, domain, snap); rerr != nil {
				s.logger.ErrorContext(ctx, "restoring the previous certificate failed",
					"domain", domain, "error", rerr, "request_id", requestIDFrom(ctx))
			}
			s.writeNginxError(w, r, err)
			return
		}
		for _, m := range res.Warnings {
			applyWarns = append(applyWarns, tlsWarning{Code: "apply_warning", Message: m})
		}
	}
	note(r, "source", tlspkg.SourceUpload)
	noteTarget(r, domain)

	out := make([]tlsWarning, 0, len(warns)+len(applyWarns))
	for _, wn := range warns {
		out = append(out, tlsWarning{Code: wn.Code, Message: wn.Message})
	}
	out = append(out, applyWarns...)
	stored, err := t.Store.GetCert(ctx, domain)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Cert     CertView     `json:"cert"`
		Warnings []tlsWarning `json:"warnings"`
	}{t.view(stored), out})
}

// tlsCertIssue orders a certificate for a host name as a job. HTTP-01 is
// refused unless a site serves the name, because nginx answers the challenge.
func (s *server) tlsCertIssue(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
	if t.Issuer == nil {
		writeError(w, http.StatusServiceUnavailable, errUnavailable, "certificate issuing is not available on this host; see the daemon log")
		return
	}
	var req certIssueRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	domain, ok := certDomain(w, req.Domain)
	if !ok {
		return
	}
	if t.PanelDomain() == domain {
		writeError(w, http.StatusConflict, errConflict,
			domain+" is the panel's own hostname; issue the panel certificate under Settings instead")
		return
	}
	plan, ok := t.planIssue(r.Context(), w, req.issueRequest, domain, []string{domain})
	if !ok {
		return
	}
	if plan.method == tlspkg.SourceHTTP01 {
		hasSite, err := t.siteHas(r.Context(), domain)
		if err != nil {
			s.writeInternal(w, r, err)
			return
		}
		if !hasSite {
			writeError(w, http.StatusConflict, errConflict,
				"HTTP-01 needs nginx to answer for "+domain+", and no site has that domain: create a site for it first, or use DNS-01")
			return
		}
	}
	note(r, "method", plan.method)
	noteTarget(r, domain)
	s.startIssue(t, w, r, domain, plan, issueStandalone)
}

// tlsCertDelete removes a certificate that no site serves. A site's certificate
// is removed through the site, which also rewrites its nginx configuration.
func (s *server) tlsCertDelete(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
	domain := strings.ToLower(r.PathValue("domain"))
	noteTarget(r, domain)
	if tlspkg.CheckDomain(domain) != nil {
		writeError(w, http.StatusNotFound, errNotFound, "no certificate is stored for "+domain)
		return
	}
	if t.PanelDomain() == domain {
		writeError(w, http.StatusConflict, errConflict,
			domain+" is the panel's own hostname and its certificate feeds the panel; replace the panel certificate first")
		return
	}
	ctx := r.Context()
	hasSite, err := t.siteHas(ctx, domain)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	if hasSite {
		writeError(w, http.StatusConflict, errConflict,
			"a site serves "+domain+"; remove its certificate from the site's TLS tab")
		return
	}
	snap, err := t.snapshot(ctx, domain)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	if snap.row == nil {
		writeError(w, http.StatusNotFound, errNotFound, "no certificate is stored for "+domain)
		return
	}
	if err := t.Store.DeleteCert(ctx, domain); err != nil {
		s.tlsFail(w, r, err)
		return
	}
	if err := tlspkg.RemoveSiteFiles(t.Root, domain); err != nil {
		s.logger.WarnContext(ctx, "certificate files were not removed",
			"domain", domain, "error", err, "request_id", requestIDFrom(ctx))
	}
	w.WriteHeader(http.StatusNoContent)
}
