package sites

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/guard"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/waf"
)

// panelKeys are the Turnstile keys the panel's Settings hold in a test. The
// firewall reads them on each use, so a test changes them between calls.
type panelKeys struct{ site, secret string }

func (k *panelKeys) source(context.Context) (string, string, error) {
	return k.site, k.secret, nil
}

func guardRig(t *testing.T) (*rig, *panelKeys) {
	t.Helper()
	r := wafRig(t, true)
	keys, err := guard.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	pk := &panelKeys{}
	r.m.waf.WithGuard(keys).WithTurnstile(pk.source)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	return r, pk
}

func storedJSON(t *testing.T, r *rig) string {
	t.Helper()
	var raw string
	if err := r.st.DB().QueryRow(`SELECT json FROM site_waf WHERE domain = 'a.test'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSetWAFChallengePoW(t *testing.T) {
	r, _ := guardRig(t)
	c := waf.Default()
	c.Challenge.Enabled = true
	res, err := r.m.SetWAF(ctx, "a.test", c)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Status.ChallengeAvailable || !res.Config.Challenge.Enabled {
		t.Fatalf("result %+v", res)
	}
	body := blockOf(t, string(r.fs.files["/www/panel/nginx/sites/a.test.conf"]), nginx.BlockWAF)
	if !strings.Contains(body, "secure_link_md5") || !strings.Contains(body, "return 418;") {
		t.Fatalf("the site block has no challenge:\n%s", body)
	}
	if !strings.Contains(string(r.fs.files["/www/panel/nginx/waf/guard/a.test.html"]), `data-provider="pow"`) {
		t.Fatal("the challenge page was not written")
	}

	s, ok, err := r.m.waf.GuardSettings(ctx, "a.test")
	if err != nil || !ok || s.Provider != guard.ProviderPoW || s.Difficulty != waf.DefaultDifficulty || s.CookieName != waf.CookieName("a.test") {
		t.Fatalf("GuardSettings = %+v %v %v", s, ok, err)
	}

	c.Challenge.Enabled = false
	if _, err := r.m.SetWAF(ctx, "a.test", c); err != nil {
		t.Fatal(err)
	}
	if r.fs.has("/www/panel/nginx/waf/guard/a.test.html") {
		t.Fatal("switching the challenge off left its page")
	}
	if _, ok, _ := r.m.waf.GuardSettings(ctx, "a.test"); ok {
		t.Fatal("the handler still answers for a site whose challenge is off")
	}
}

// TestSetWAFTurnstileGlobalKeys: a site uses the panel's Turnstile keys, which
// are read live, are never stored with the site, and must both be set first.
func TestSetWAFTurnstileGlobalKeys(t *testing.T) {
	r, keys := guardRig(t)
	c := waf.Default()
	c.Challenge = waf.Challenge{Enabled: true, Provider: guard.ProviderTurnstile}

	_, err := r.m.SetWAF(ctx, "a.test", c)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "challenge.enabled" {
		t.Fatalf("Turnstile without panel keys: %v", err)
	}

	keys.site = "0x4AAAAAAAkey"
	keys.secret = "0x4AAAAAAAsecret"
	res, err := r.m.SetWAF(ctx, "a.test", c)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Status.TurnstileReady {
		t.Fatalf("the status does not report the panel keys: %+v", res.Status)
	}
	if out, _ := json.Marshal(res.Config); strings.Contains(string(out), "turnstile_") {
		t.Fatalf("the site's settings carry Turnstile key fields: %s", out)
	}
	if strings.Contains(storedJSON(t, r), "turnstile_") {
		t.Fatal("the site's stored settings carry Turnstile fields")
	}

	s, ok, err := r.m.waf.GuardSettings(ctx, "a.test")
	if err != nil || !ok || s.TurnstileSiteKey != "0x4AAAAAAAkey" || s.TurnstileSecret != "0x4AAAAAAAsecret" {
		t.Fatalf("GuardSettings = %+v %v %v", s, ok, err)
	}
	page := string(r.fs.files["/www/panel/nginx/waf/guard/a.test.html"])
	if !strings.Contains(page, `data-provider="turnstile"`) || strings.Contains(page, "0x4AAAAAAAkey") || strings.Contains(page, "0x4AAAAAAAsecret") {
		t.Fatal("the page must name its provider and carry neither Turnstile key")
	}

	// A new site key reaches the next visitor without a save or a reload.
	keys.site = "0x4AAAAAAAnew"
	if s, _, _ := r.m.waf.GuardSettings(ctx, "a.test"); s.TurnstileSiteKey != "0x4AAAAAAAnew" {
		t.Fatalf("the site key is not read live: %q", s.TurnstileSiteKey)
	}

	// Removing the secret takes the site's Turnstile off until it is set again.
	keys.secret = ""
	if _, status, err := r.m.WAFConfig(ctx, "a.test"); err != nil || status.TurnstileReady {
		t.Fatalf("status without the secret: %+v %v", status, err)
	}
}

func TestSetWAFChallengeWithoutKeys(t *testing.T) {
	r := wafRig(t, true)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	c := waf.Default()
	c.Challenge.Enabled = true
	_, err := r.m.SetWAF(ctx, "a.test", c)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "challenge.enabled" {
		t.Fatalf("got %v, want a challenge.enabled validation error", err)
	}
	_, st, err := r.m.WAFConfig(ctx, "a.test")
	if err != nil || st.ChallengeAvailable {
		t.Fatalf("status %+v %v", st, err)
	}
}
