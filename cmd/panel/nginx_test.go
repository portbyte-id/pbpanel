package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/portbyte-id/server-panel/internal/guard"
	"github.com/portbyte-id/server-panel/internal/install"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/settings"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/waf"
)

// TestTurnstileKeysComeFromSettings: the firewall's Turnstile keys are panel
// settings, read on every use, so the Settings page is enough to switch Turnstile
// on. Without the source wired in, a site could never use it however the keys
// are set.
func TestTurnstileKeysComeFromSettings(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg, err := settings.New(st)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	src := turnstileKeys(reg)
	if siteKey, secret, err := src(ctx); err != nil || siteKey != "" || secret != "" {
		t.Fatalf("unset keys: %q %q %v", siteKey, secret, err)
	}

	if err := reg.Set(ctx, settings.KeyWAFTurnstileSiteKey, "0x4AAAAAAAkey"); err != nil {
		t.Fatal(err)
	}
	// A site key without its secret is not enough to turn Turnstile on.
	if siteKey, secret, err := src(ctx); err != nil || siteKey != "0x4AAAAAAAkey" || secret != "" {
		t.Fatalf("site key only: %q %q %v", siteKey, secret, err)
	}

	if err := reg.Set(ctx, settings.KeyWAFTurnstileSecret, "0x4AAAAAAAsecret"); err != nil {
		t.Fatal(err)
	}
	if siteKey, secret, err := src(ctx); err != nil || siteKey != "0x4AAAAAAAkey" || secret != "0x4AAAAAAAsecret" {
		t.Fatalf("both keys: %q %q %v", siteKey, secret, err)
	}
	// Read on every use: a key changed in Settings reaches the next check.
	if err := reg.Set(ctx, settings.KeyWAFTurnstileSiteKey, "0x4AAAAAAAnew"); err != nil {
		t.Fatal(err)
	}
	if siteKey, _, _ := src(ctx); siteKey != "0x4AAAAAAAnew" {
		t.Fatalf("the source cached the site key: %q", siteKey)
	}

	// The engine accepts Turnstile once the source is wired in, which is what the
	// daemon does; the settings source alone must satisfy its check.
	keys, err := guard.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	e := waf.New(st, nginx.Paths{Root: t.TempDir()}, nil).WithGuard(keys).WithTurnstile(src)
	c := waf.Default()
	c.Challenge = waf.Challenge{Enabled: true, Provider: "turnstile"}
	if ready, err := e.TurnstileReady(ctx); err != nil || !ready {
		t.Fatalf("TurnstileReady = %v %v", ready, err)
	}
	if err := e.Check(ctx, c); err != nil {
		t.Fatalf("Check with both keys set: %v", err)
	}
}

// The same holds for php: pools run as the account that owns the web roots.
func TestPHPAndInstallAgreeOnTheWWWAccount(t *testing.T) {
	if php.WWWUser != install.WWWUser || php.WWWUID != install.WWWUID || php.WWWGID != install.WWWGID {
		t.Fatalf("php has %s %d:%d, install has %s %d:%d",
			php.WWWUser, php.WWWUID, php.WWWGID, install.WWWUser, install.WWWUID, install.WWWGID)
	}
}

// The nginx package cannot import install (install imports the HTTP layer,
// which imports nginx), so it repeats the www account. The two must agree: the
// image's user and the owner of every web root are the same account.
func TestNginxAndInstallAgreeOnTheWWWAccount(t *testing.T) {
	if nginx.WWWUser != install.WWWUser || nginx.WWWUID != install.WWWUID || nginx.WWWGID != install.WWWGID {
		t.Fatalf("nginx has %s %d:%d, install has %s %d:%d",
			nginx.WWWUser, nginx.WWWUID, nginx.WWWGID, install.WWWUser, install.WWWUID, install.WWWGID)
	}
}
