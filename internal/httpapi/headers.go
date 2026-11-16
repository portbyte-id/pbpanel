package httpapi

import "net/http"

// securityHeaders are the browser-facing protections the panel promises on
// every response. They are constants rather than settings: an operator who
// could turn CSP off would be turning off the main defence against an injected
// script reading the CSRF token out of the page.
const (
	// CSP locks the panel down to its own origin. There is no inline script and
	// no eval: the SPA ships as files, and a build that needed either would
	// have to change this line deliberately.
	contentSecurityPolicy = "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		"font-src 'self' data:; " +
		"connect-src 'self'; " +
		"object-src 'none'; " +
		"base-uri 'none'; " +
		"form-action 'self'; " +
		"frame-ancestors 'none'"

	// HSTS with a year and subdomains. No preload: preload is a one-way door for
	// every subdomain of whatever name the panel answers on, which for a
	// single-server tool is a decision for the operator, not this binary.
	strictTransportSecurity = "max-age=31536000; includeSubDomains"

	referrerPolicy = "no-referrer"
)

// withSecurityHeaders sets the protection headers before the handler runs, so
// they are present on a successful response and on an error alike.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("Strict-Transport-Security", strictTransportSecurity)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", referrerPolicy)
		next.ServeHTTP(w, r)
	})
}
