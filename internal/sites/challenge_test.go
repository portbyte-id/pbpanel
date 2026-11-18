package sites

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/guard"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/secretbox"
	"github.com/portbyte-id/server-panel/internal/waf"
)

func guardRig(t *testing.T) *rig {
	t.Helper()
	r := wafRig(t, true)
	master := []byte("0123456789abcdef0123456789abcdef")
	keys, err := guard.NewKeyring(master)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(master)
	if err != nil {
		t.Fatal(err)
	}
	r.m.waf.WithGuard(keys, box)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	return r
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
	r := guardRig(t)
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

// TestSetWAFTurnstileSecret: the secret is sealed in the database, never sent
// back, kept when a later save leaves it empty, and required to switch on.
func TestSetWAFTurnstileSecret(t *testing.T) {
	r := guardRig(t)
	c := waf.Default()
	c.Challenge = waf.Challenge{Enabled: true, Provider: guard.ProviderTurnstile, TurnstileSiteKey: "0x4AAAAAAAkey"}

	_, err := r.m.SetWAF(ctx, "a.test", c)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "challenge.turnstile_secret" {
		t.Fatalf("Turnstile without a secret: %v", err)
	}

	c.Challenge.TurnstileSecret = "bad secret!"
	if _, err := r.m.SetWAF(ctx, "a.test", c); !errors.As(err, &ve) || ve.Field != "challenge.turnstile_secret" {
		t.Fatalf("malformed secret: %v", err)
	}

	c.Challenge.TurnstileSecret = "0x4AAAAAAAsecret"
	res, err := r.m.SetWAF(ctx, "a.test", c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Challenge.TurnstileSecret != "" || !res.Config.Challenge.TurnstileSecretSet {
		t.Fatalf("the response carries the secret or hides that one is set: %+v", res.Config.Challenge)
	}
	if strings.Contains(storedJSON(t, r), "0x4AAAAAAAsecret") {
		t.Fatal("the secret is stored in the clear")
	}
	out, _ := json.Marshal(res.Config)
	if strings.Contains(string(out), `"turnstile_secret"`) {
		t.Fatalf("the JSON response has the secret field: %s", out)
	}

	// A save from the form, which never has the secret, keeps it.
	got, _, err := r.m.WAFConfig(ctx, "a.test")
	if err != nil {
		t.Fatal(err)
	}
	got.Challenge.ClearanceMinutes = 30
	if _, err := r.m.SetWAF(ctx, "a.test", got); err != nil {
		t.Fatalf("a save without the secret: %v", err)
	}
	s, ok, err := r.m.waf.GuardSettings(ctx, "a.test")
	if err != nil || !ok || s.TurnstileSecret != "0x4AAAAAAAsecret" || s.Clearance.Minutes() != 30 {
		t.Fatalf("GuardSettings = %+v %v %v", s, ok, err)
	}
	page := string(r.fs.files["/www/panel/nginx/waf/guard/a.test.html"])
	if !strings.Contains(page, `data-sitekey="0x4AAAAAAAkey"`) || strings.Contains(page, "0x4AAAAAAAsecret") {
		t.Fatal("the page must carry the site key and never the secret")
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
