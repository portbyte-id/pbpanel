package waf

import (
	"strings"
	"testing"
)

// Tests for the global firewall: its validation, the http include it defines,
// and the rules a site's block gets from it. Pure rendering, no database.

func TestGlobalDefaultIsInactive(t *testing.T) {
	g := DefaultGlobal()
	if g.Active() || g.BotActive() {
		t.Fatalf("the default global firewall asks for a rule: %+v", g)
	}
	if got := string(GlobalHTTPInclude(g)); strings.Contains(got, "map ") {
		t.Fatalf("an inactive global firewall defines variables:\n%s", got)
	}
}

func TestGlobalNormalizeAgentsAndAddrs(t *testing.T) {
	g := DefaultGlobal()
	g.BotBlock.Enabled = true
	g.BotBlock.Custom = []string{" MyBot/1.0 ", "mybot/1.0", "curl-test"}
	g.IPDeny = []string{"10.0.0.1", "10.0.0.0/8"}

	n, err := g.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(n.BotBlock.Custom) != 2 {
		t.Fatalf("custom agents = %v, want the case-insensitive duplicate removed", n.BotBlock.Custom)
	}
	if want := "10.0.0.0/8,10.0.0.1/32"; strings.Join(n.IPDeny, ",") != want {
		t.Fatalf("ip_deny = %v, want %s", n.IPDeny, want)
	}
	if n.ErrorPage.HTML != DefaultErrorPageHTML {
		t.Fatal("an empty page should fall back to the default")
	}
}

func TestGlobalNormalizeRejects(t *testing.T) {
	cases := map[string]func(*Global){
		"quote in agent": func(g *Global) { g.BotBlock.Custom = []string{`bad"agent`} },
		"space in agent": func(g *Global) { g.BotBlock.Custom = []string{"bad agent"} },
		"bad address":    func(g *Global) { g.IPDeny = []string{"not-an-ip"} },
		"include in page": func(g *Global) {
			g.ErrorPage.HTML = `<!--#include virtual="/etc/passwd" -->`
		},
		"exec in page": func(g *Global) { g.ErrorPage.HTML = `<!--#exec cmd="id" -->` },
		"page too big": func(g *Global) { g.ErrorPage.HTML = strings.Repeat("x", MaxErrorPageHTML+1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			g := DefaultGlobal()
			mutate(&g)
			if _, err := g.Normalize(); err == nil {
				t.Fatal("Normalize accepted an invalid setting")
			}
		})
	}
}

func TestGlobalEchoIsAllowedInPage(t *testing.T) {
	g := DefaultGlobal()
	g.ErrorPage.HTML = `<p>Ref <!--#echo var="request_id" --></p>`
	if _, err := g.Normalize(); err != nil {
		t.Fatalf("echo directive refused: %v", err)
	}
}

func TestGlobalHTTPIncludeOnlyWhatIsOn(t *testing.T) {
	g := DefaultGlobal()
	g.BotBlock.Enabled = true
	g.BotBlock.Custom = []string{"evil.bot+x"}
	g.BotBlock.EmptyAgent = true

	inc := string(GlobalHTTPInclude(g))
	for _, want := range []string{
		"map $uri $pbwaf_g_skip",
		"/pb-waf-blocked 1;",
		`map "$pbwaf_g_skip:$http_user_agent" $pbwaf_g_bot`,
		`"~^0:$" 1;`,
		"sqlmap",
		"evil[.]bot[+]x",
	} {
		if !strings.Contains(inc, want) {
			t.Errorf("global include lacks %q:\n%s", want, inc)
		}
	}
	for _, unwanted := range []string{"pbwaf_g_ip", "pbwaf_g_dot", "pbwaf_g_trace"} {
		if strings.Contains(inc, unwanted) {
			t.Errorf("global include defines %s though it is off:\n%s", unwanted, inc)
		}
	}
	// The bot map is a quoted nginx string: a backslash there would be read raw.
	for _, line := range strings.Split(inc, "\n") {
		if strings.Contains(line, "pbwaf_g_bot") && strings.Contains(line, `\`) {
			t.Errorf("bot map line has a backslash: %s", line)
		}
	}
}

func TestGlobalBlockRules(t *testing.T) {
	g := DefaultGlobal()
	g.BotBlock.Enabled = true
	g.SensitiveFiles = true
	g.BlockTrace = true
	g.IPDeny = []string{"192.0.2.0/24"}
	g.ErrorPage.Enabled = true

	block := string(blockBody(testPaths, "a.test", Default(), "", g))
	for _, want := range []string{
		"if ($pbwaf_g_ip) { return 403; }",
		"if ($pbwaf_g_bot) { return 403; }",
		"if ($pbwaf_g_dot) { return 403; }",
		"if ($pbwaf_g_trace) { return 403; }",
		"error_page 403 =403 /pb-waf-blocked;",
		"error_page 429 =429 /pb-waf-blocked;",
		"location = /pb-waf-blocked {",
		"alias " + testPaths.WAFErrorPage() + ";",
		"ssi on;",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("block lacks %q:\n%s", want, block)
		}
	}
}

func TestGlobalBlockOffForDefaultSite(t *testing.T) {
	block := string(blockBody(testPaths, "a.test", Default(), "", DefaultGlobal()))
	if !strings.Contains(block, "firewall is off") || strings.Contains(block, "error_page") {
		t.Fatalf("a site with nothing on should render a comment only:\n%s", block)
	}
}

func TestGlobalBlockPageModSecurity(t *testing.T) {
	g := DefaultGlobal()
	g.BotBlock.Enabled = true
	g.ErrorPage.Enabled = true

	plain := string(blockBody(testPaths, "a.test", Default(), "", g))
	if strings.Contains(plain, "modsecurity off") {
		t.Fatal("the page disables ModSecurity for a site that does not use it")
	}
	withModSec := Config{Mode: ModeBlock, Paranoia: 1, Challenge: DefaultChallenge()}
	modsec := string(blockBody(testPaths, "a.test", withModSec, "", g))
	if !strings.Contains(modsec, "modsecurity off;") {
		t.Fatalf("the page must not run ModSecurity on a site that uses it:\n%s", modsec)
	}
}

func TestRenderWithAttachesBlockForGlobalOnly(t *testing.T) {
	e := &Engine{p: testPaths}
	g := DefaultGlobal()
	g.BlockTrace = true

	if r := e.RenderWith("a.test", Default(), g); !r.NeedsBlock {
		t.Fatal("a global protection should attach a block to a site with no firewall of its own")
	}
	if r := e.RenderWith("a.test", Default(), DefaultGlobal()); r.NeedsBlock {
		t.Fatal("an inactive global firewall must not attach a block")
	}
	g2 := DefaultGlobal()
	g2.ErrorPage.Enabled = true
	if r := e.RenderWith("a.test", Default(), g2); r.NeedsBlock {
		t.Fatal("the block page alone must not attach a block to every site")
	}
}

func TestBotPatternEscapesWithoutBackslash(t *testing.T) {
	got := botPattern([]string{"a.b", "c+d", "ef-g/h"})
	if got != "a[.]b|c[+]d|ef-g/h" {
		t.Fatalf("botPattern = %q", got)
	}
}
