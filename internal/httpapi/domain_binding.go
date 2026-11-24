package httpapi

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/portbyte-id/server-panel/internal/settings"
)

// bindingProbePath is the one route answered for every host name. The upgrade
// probe calls it on the loopback address to see whether a new binary came up,
// and it reads nothing, so letting it through leaks nothing.
const bindingProbePath = "/api/health"

// withDomainBinding refuses every request whose Host header does not name the
// bound domain, when panel.domain is set. The refusal is a bare 421 with one
// line of text: a bound panel has no route at all for another name, and the
// refusal is the only thing a client on that name learns.
//
// The setting is read on every request, not cached. `pbp domain-binding remove`
// runs in a second process and writes the same database, so a cache would keep
// the lock-out in place until a restart. A read failure fails closed.
func (s *server) withDomainBinding(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == bindingProbePath {
			next.ServeHTTP(w, r)
			return
		}
		bound, err := s.deps.Settings.Str(r.Context(), settings.KeyPanelDomain)
		if err != nil {
			s.logger.Error("domain binding: read setting", "error", err)
			writeError(w, http.StatusServiceUnavailable, errUnavailable,
				"the domain binding setting could not be read; see the daemon log")
			return
		}
		if bound == "" || bindingMatches(bound, r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMisdirectedRequest)
		_, _ = io.WriteString(w, fmt.Sprintf("This panel answers only at %s.\n", bound))
	})
}

// checkBindingHost decides whether a request may set panel.domain to value. The
// operator must already be using that name: binding from an IP address or from
// another name would lock them out the moment the setting takes effect. Clearing
// the binding is always allowed, because a request that can reach the Settings
// page at all has already passed the binding check. An invalid value returns nil
// so the registry reports the validator's own message.
func (s *server) checkBindingHost(r *http.Request, value string) error {
	if value == "" || s.deps.Settings.Validate(settings.KeyPanelDomain, value) != nil {
		return nil
	}
	if bindingMatches(value, r.Host) {
		return nil
	}
	return fmt.Errorf("open the panel at %s first, then bind it to that name: this request came in on %q",
		value, requestHostName(r.Host))
}

// bindingMatches reports whether a Host header value names the bound domain. The
// comparison ignores case, any port and a trailing dot.
func bindingMatches(bound, hostHeader string) bool {
	return strings.EqualFold(strings.TrimSuffix(bound, "."), requestHostName(hostHeader))
}

// requestHostName returns the host name in a Host header value, lower-cased,
// without a port, brackets or a trailing dot.
func requestHostName(hostHeader string) string {
	h := hostHeader
	if host, _, err := net.SplitHostPort(hostHeader); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.ToLower(strings.TrimSuffix(h, "."))
}
