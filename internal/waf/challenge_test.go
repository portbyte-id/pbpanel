package waf

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/guard"
	"github.com/portbyte-id/server-panel/internal/secretbox"
)

func guardedEngine(t *testing.T) *Engine {
	t.Helper()
	master := []byte("0123456789abcdef0123456789abcdef")
	keys, err := guard.NewKeyring(master)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(master)
	if err != nil {
		t.Fatal(err)
	}
	return New(nil, testPaths, nil).WithGuard(keys, box)
}

func challengeConfig(mutate func(*Challenge)) Config {
	c := Default()
	c.Challenge.Enabled = true
	if mutate != nil {
		mutate(&c.Challenge)
	}
	return c
}

func TestChallengeNormalize(t *testing.T) {
	c, err := Config{Challenge: Challenge{Enabled: true}}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	ch := c.Challenge
	if ch.Provider != guard.ProviderPoW || ch.Difficulty != DefaultDifficulty || ch.ClearanceMinutes != DefaultClearanceMinutes {
		t.Fatalf("defaults: %+v", ch)
	}
	if ch.ExemptPaths == nil || ch.ExemptIPs == nil {
		t.Fatal("lists must never be nil")
	}
	if c.IsDefault() {
		t.Fatal("a challenge that is on is not the default")
	}

	bad := map[string]func(*Challenge){
		"provider":          func(ch *Challenge) { ch.Provider = "recaptcha" },
		"difficulty low":    func(ch *Challenge) { ch.Difficulty = 4 },
		"difficulty high":   func(ch *Challenge) { ch.Difficulty = 30 },
		"clearance":         func(ch *Challenge) { ch.ClearanceMinutes = 1 },
		"turnstile no key":  func(ch *Challenge) { ch.Provider = guard.ProviderTurnstile },
		"site key quote":    func(ch *Challenge) { ch.Provider, ch.TurnstileSiteKey = guard.ProviderTurnstile, `0x"; return 200;` },
		"exempt whole site": func(ch *Challenge) { ch.ExemptPaths = []string{"/"} },
		"exempt brace":      func(ch *Challenge) { ch.ExemptPaths = []string{"/a{b"} },
		"exempt ip":         func(ch *Challenge) { ch.ExemptIPs = []string{"nope"} },
	}
	for name, mutate := range bad {
		_, err := challengeConfig(mutate).Normalize()
		var ve *ValidationError
		if !errors.As(err, &ve) || !strings.HasPrefix(ve.Field, "challenge.") {
			t.Errorf("%s: got %v, want a challenge.* validation error", name, err)
		}
	}
}

func TestChallengeRender(t *testing.T) {
	e := guardedEngine(t)
	c, err := challengeConfig(func(ch *Challenge) {
		ch.ExemptPaths = []string{"/api/", "/feed.xml"}
		ch.ExemptIPs = []string{"203.0.113.0/24"}
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	r := e.RenderConfig("a.test", c)
	if !r.NeedsBlock {
		t.Fatal("the challenge needs the block")
	}
	block := string(r.Block)
	id := SiteID("a.test")
	secret := e.keys.CookieSecret("a.test")
	for _, want := range []string{
		"secure_link $cookie_pbg_" + id + ";",
		`secure_link_md5 "$secure_link_expires|$remote_addr|$http_user_agent|` + secret + `";`,
		"if ($pbwaf_" + id + "_challenge) { return 418; }",
		"error_page 418 =403 /.pb-guard.html;",
		"location = /.pb-guard.html {\n    internal;\n    alias /www/panel/nginx/waf/guard/a.test.html;",
		"location ^~ /.pb-challenge/ {\n    proxy_pass http://unix:/www/panel/run/guard.sock;",
		`proxy_set_header X-PB-Site "a.test";`,
		"proxy_set_header X-PB-Client $remote_addr;",
		"Content-Security-Policy",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("block lacks %q:\n%s", want, block)
		}
	}

	inc := string(HTTPInclude("a.test", c))
	for _, want := range []string{
		"geo $remote_addr $pbwaf_" + id + "_cip {\n    default 0;\n    203.0.113.0/24 1;\n}",
		"~^/\\.well-known/acme-challenge/ 1;",
		"~^/\\.pb-challenge/ 1;",
		"    /.pb-guard.html 1;",
		"~*^/api/ 1;",
		"~*^/feed\\.xml 1;",
		"map $uri $pbwaf_" + id + "_cpage {\n    volatile;\n    default 0;\n    /.pb-guard.html 1;\n}",
		"map \"$secure_link:$pbwaf_" + id + "_cpath$pbwaf_" + id + "_cip$pbwaf_" + id + "_cpage\" $pbwaf_" + id + "_challenge {\n    volatile;\n    default 0;\n    \":000\" 1;\n    \"0:000\" 1;\n}",
	} {
		if !strings.Contains(inc, want) {
			t.Errorf("http include lacks %q:\n%s", want, inc)
		}
	}

	var page string
	for _, f := range r.Files {
		if f.Path == testPaths.GuardPage("a.test") && !f.Delete {
			page = string(f.Content)
		}
	}
	if !strings.Contains(page, `data-provider="pow"`) {
		t.Fatalf("the page is not written:\n%s", page)
	}
}

func TestChallengeOffDeletesPage(t *testing.T) {
	e := guardedEngine(t)
	r := e.RenderConfig("a.test", Default())
	if strings.Contains(string(r.Block), "secure_link") || strings.Contains(string(HTTPInclude("a.test", Default())), "_challenge") {
		t.Fatal("an off challenge rendered something")
	}
	var deletes bool
	for _, f := range r.Files {
		if f.Path == testPaths.GuardPage("a.test") && f.Delete {
			deletes = true
		}
	}
	if !deletes {
		t.Fatal("switching the challenge off must delete its page")
	}
	removed := false
	for _, f := range e.RemovedFiles("a.test") {
		if f.Path == testPaths.GuardPage("a.test") && f.Delete {
			removed = true
		}
	}
	if !removed {
		t.Fatal("deleting the site must delete its page")
	}
}

// TestChallengeWithoutKeys: a panel that lost secret.key must not render a
// challenge nobody can pass, and must refuse to switch one on.
func TestChallengeWithoutKeys(t *testing.T) {
	e := New(nil, testPaths, nil)
	c := challengeConfig(nil)
	if err := e.Check(context.Background(), c); !errors.Is(err, ErrNoChallenge) {
		t.Fatalf("Check: %v", err)
	}
	r := e.RenderConfig("a.test", c)
	if strings.Contains(string(r.Block), "return 418") {
		t.Fatal("a challenge was rendered without keys")
	}
}

func TestTurnstileSecretSealing(t *testing.T) {
	e := guardedEngine(t)
	sealed, err := e.SealTurnstileSecret("a.test", "0xSECRET")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "0xSECRET") {
		t.Fatal("the secret is stored in the clear")
	}
	if _, err := e.box.Open(sealed, turnstileAAD("b.test")); err == nil {
		t.Fatal("a secret sealed for one site opened for another")
	}
	plain, err := e.box.Open(sealed, turnstileAAD("a.test"))
	if err != nil || string(plain) != "0xSECRET" {
		t.Fatalf("open: %q %v", plain, err)
	}
}
