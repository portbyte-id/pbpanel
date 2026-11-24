package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// TestWAFEventsAbuseIPDBExportsTheFilteredSet proves the export route is behind
// login, takes the same filters as the events list, and answers a CSV attachment
// that names the matched address.
func TestWAFEventsAbuseIPDBExportsTheFilteredSet(t *testing.T) {
	h, fake, s := newWAFMonHarness(t)

	if rec := h.do(http.MethodGet, "/api/waf/events/abuseipdb", "", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated: status = %d, want 401", rec.Code)
	}

	rec := h.do(http.MethodGet, "/api/waf/events/abuseipdb?site=a.test&action=blocked&rule=942100", s.cookie, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("content type = %q, want text/csv", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "waf-events-abuseipdb-") {
		t.Errorf("content disposition = %q, want a named attachment", cd)
	}
	if body := rec.Body.String(); !strings.HasPrefix(body, "IP,Categories,ReportDate,Comment\n") ||
		!strings.Contains(body, "198.51.100.4,21,") {
		t.Errorf("body = %q", body)
	}
	if fake.evQ.Site != "a.test" || fake.evQ.Action != "blocked" || fake.evQ.Rule != "942100" {
		t.Errorf("filters reached the monitor as %+v", fake.evQ)
	}

	for _, bad := range []string{"?action=bogus", "?site=not%20a%20domain", "?rule=abc"} {
		if rec := h.do(http.MethodGet, "/api/waf/events/abuseipdb"+bad, s.cookie, "", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", bad, rec.Code)
		}
	}
}
