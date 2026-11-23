package httpapi

import (
	"net/http"
	"strings"

	tlspkg "github.com/portbyte-id/server-panel/internal/tls"
)

// panelCertIPGet reports the public IPv4 address found on this host's own
// interfaces, so the form can offer it. It sends no packet: a server behind a
// cloud 1:1 NAT has no public address on an interface, and the operator types the
// one the provider assigned.
func (s *server) panelCertIPGet(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
	out := struct {
		Detected string `json:"detected"`
		Reason   string `json:"reason,omitempty"`
		Current  string `json:"current"`
	}{Current: t.PanelDomain()}
	if ip, ok := tlspkg.PublicIPv4(); ok {
		out.Detected = ip
	} else {
		out.Reason = "No public IPv4 address is assigned to this server's network interfaces (a cloud server behind NAT looks like this). Enter the public IP your provider gave you."
	}
	writeJSON(w, http.StatusOK, out)
}

// panelCertIPIssue orders an IP-address certificate for the panel's own
// listener over HTTP-01, as a job. An empty ip means the detected one.
func (s *server) panelCertIPIssue(t *TLSDeps, w http.ResponseWriter, r *http.Request) {
	if s.panelCerts(t, w) == nil {
		return
	}
	if t.Issuer == nil {
		writeError(w, http.StatusServiceUnavailable, errUnavailable, "certificate issuing is not available on this host; see the daemon log")
		return
	}
	var req struct {
		IP    string `json:"ip"`
		Email string `json:"email"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest, decodeErrorDetail(err))
		return
	}
	ip := strings.TrimSpace(req.IP)
	if ip == "" {
		detected, ok := tlspkg.PublicIPv4()
		if !ok {
			invalidField(w, "ip", "no public IP address was found on this server: enter the public IP your provider gave you")
			return
		}
		ip = detected
	}
	if !tlspkg.IsPublicIP(ip) {
		invalidField(w, "ip", ip+" is not a public IP address: a certificate can only name an address reachable from the internet")
		return
	}
	ip = tlspkg.CanonicalIP(ip)
	email := strings.TrimSpace(req.Email)
	if email != "" && !strings.Contains(email, "@") {
		invalidField(w, "email", "that is not an email address")
		return
	}
	note(r, "method", tlspkg.SourceHTTP01)
	noteTarget(r, ip)
	s.startIssue(t, w, r, ip, issuePlan{method: tlspkg.SourceHTTP01, names: []string{ip}, email: email}, issuePanel)
}
