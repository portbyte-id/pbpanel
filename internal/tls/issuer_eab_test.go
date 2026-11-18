package tls

import "testing"

func TestRequiresEAB(t *testing.T) {
	cases := map[string]bool{
		"https://dv.acme-v02.api.pki.goog/directory":       true,
		"https://dv.acme-v02.test-api.pki.goog/directory":  true,
		"https://acme-v02.api.letsencrypt.org/directory":   false,
		"https://acme-staging-v02.api.letsencrypt.org/dir": false,
		"https://evil-pki.goog.example.com/directory":      false,
		"https://notpki.goog/directory":                    false,
		"::not a url":                                      false,
	}
	for dir, want := range cases {
		if got := requiresEAB(dir); got != want {
			t.Errorf("requiresEAB(%q) = %v, want %v", dir, got, want)
		}
	}
}
