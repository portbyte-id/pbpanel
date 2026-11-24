package waf

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/<name>. With -update it writes the file
// instead, so a deliberate change is one command and one reviewed diff.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	file := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s does not match.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// openAppsecConfig is a site that inspects with the second engine, with the
// engine-independent protections switched on beside it: the block must carry the
// open-appsec directive and none of ModSecurity's, and everything else unchanged.
func openAppsecConfig() Config {
	c := Default()
	c.Engine = EngineOpenAppsec
	c.Mode = ModeBlock
	c.OpenAppsec.Confidence = ConfidenceMedium
	c.RateLimit.Enabled = true
	c.PathBlocklist = []string{"/secret"}
	c.IPRules.Deny = []string{"203.0.113.9/32"}
	return c
}

// TestOpenAppsecBlockGolden pins the site block for both engines. The two files
// differ only in the engine's own lines: the rate limit, the path and address
// rules and the challenge are engine-independent, so a change there has to show
// up in both goldens or in neither.
func TestOpenAppsecBlockGolden(t *testing.T) {
	e := guardedEngine(t)

	c, err := openAppsecConfig().Normalize()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "openappsec-block.conf", e.RenderWith("a.test", c, Global{}).Block)

	m := Default()
	m.Mode, m.Paranoia = ModeBlock, 2
	m.RateLimit.Enabled = true
	m.PathBlocklist = []string{"/secret"}
	m.IPRules.Deny = []string{"203.0.113.9/32"}
	m, err = m.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "modsecurity-block.conf", e.RenderWith("a.test", m, Global{}).Block)
}

// TestOpenAppsecBlockHasNoModSecurity: the two engines are exclusive per site
// (D2). A block that carried both would make an open-appsec site depend on a
// build's ModSecurity too, and the two inspectors would run on one request.
func TestOpenAppsecBlockHasNoModSecurity(t *testing.T) {
	c, err := openAppsecConfig().Normalize()
	if err != nil {
		t.Fatal(err)
	}
	block := string(guardedEngine(t).RenderWith("a.test", c, Global{}).Block)
	for _, banned := range []string{"modsecurity", "SecRule"} {
		if strings.Contains(block, banned) {
			t.Errorf("an open-appsec block mentions %q:\n%s", banned, block)
		}
	}
	if !strings.Contains(block, OpenAppsecDirective) {
		t.Errorf("the open-appsec block lacks %q:\n%s", OpenAppsecDirective, block)
	}
}

// A site that does not use the engine must not carry its directive: the
// directive needs the module, and the module is loaded only for sites that name
// it (D4, the placement decision step 4 took).
func TestOpenAppsecDirectiveOnlyWhereUsed(t *testing.T) {
	for name, c := range map[string]Config{
		"firewall off":        Default(),
		"modsecurity block":   func() Config { x := Default(); x.Mode = ModeBlock; return x }(),
		"open-appsec but off": func() Config { x := Default(); x.Engine = EngineOpenAppsec; return x }(),
		"open-appsec detect":  func() Config { x := Default(); x.Engine = EngineOpenAppsec; x.Mode = ModeDetect; return x }(),
	} {
		got := strings.Contains(string(guardedEngine(t).RenderWith("a.test", c, Global{}).Block), "cp-nano-nginx-attachment")
		want := name == "open-appsec detect"
		if got != want {
			t.Errorf("%s: directive present = %v, want %v", name, got, want)
		}
	}
}

// The challenge and the ACME path are the two places a visitor or a certificate
// client has to reach without passing the engine. Both engines' switches sit in
// the same two locations, because both are nginx per-location directives.
func TestOpenAppsecExemptsTheChallenge(t *testing.T) {
	c := openAppsecConfig()
	c.Challenge = Challenge{Enabled: true}
	c, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	block := string(guardedEngine(t).RenderWith("a.test", c, Global{}).Block)
	for _, loc := range []string{"location = ", "location ^~ "} {
		at := strings.Index(block, loc)
		if at < 0 {
			t.Fatalf("a challenge location is missing:\n%s", block)
		}
		end := strings.Index(block[at:], "\n}")
		if end < 0 || !strings.Contains(block[at:at+end], "cp-nano-nginx-attachment off;") {
			t.Errorf("a challenge location is inspected by open-appsec:\n%s", block[at:at+end+2])
		}
	}
	// The engine is on for the server, so the locations have something to turn off.
	if !strings.Contains(block, OpenAppsecDirective) {
		t.Errorf("the server lost its directive:\n%s", block)
	}
	// And a site without a challenge carries no off directive at all.
	plain, err := openAppsecConfig().Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(guardedEngine(t).RenderWith("a.test", plain, Global{}).Block), "cp-nano-nginx-attachment off;") {
		t.Error("a site without a challenge switched the engine off in a location")
	}
}

// The block page is served to a visitor who has just been refused, so the same
// engine must not refuse the page itself: the location turns it off, exactly as
// it does for ModSecurity.
func TestOpenAppsecExemptsTheBlockPage(t *testing.T) {
	g := DefaultGlobal()
	g.ErrorPage.Enabled = true
	g, err := g.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	c, err := openAppsecConfig().Normalize()
	if err != nil {
		t.Fatal(err)
	}
	block := string(guardedEngine(t).RenderWith("a.test", c, g).Block)
	at := strings.Index(block, "location = "+BlockedPagePath+" {")
	if at < 0 {
		t.Fatalf("the block page location is missing:\n%s", block)
	}
	end := strings.Index(block[at:], "\n}")
	if end < 0 || !strings.Contains(block[at:at+end], "cp-nano-nginx-attachment off;") {
		t.Errorf("the block page is inspected by open-appsec:\n%s", block[at:at+end+2])
	}
	// A site with the page off does not carry the switch.
	if strings.Contains(string(guardedEngine(t).RenderWith("a.test", c, Global{}).Block), "cp-nano-nginx-attachment off;") {
		t.Error("a site without a block page switched the engine off in a location")
	}
}

// The rendered policy is byte-for-byte the same for the same sites: an unrelated
// change must not rewrite it, because a rewrite would reload the agent.
func TestOpenAppsecPolicyDeterministic(t *testing.T) {
	sites := []OpenAppsecPolicy{
		{Hosts: []string{"b.test", "b-alias.test"}, Mode: ModeBlock, Confidence: ConfidenceHigh},
		{Hosts: []string{"a.test"}, Mode: ModeDetect, Confidence: ConfidenceCritical},
		{Hosts: []string{"c.test"}, Mode: ModeBlock, Confidence: ConfidenceMedium},
	}
	first := RenderOpenAppsecPolicy(sites)

	// The order the rows arrive in is the database's, not the policy's.
	shuffled := []OpenAppsecPolicy{sites[2], sites[0], sites[1]}
	if second := RenderOpenAppsecPolicy(shuffled); !bytes.Equal(first, second) {
		t.Errorf("the same sites rendered differently:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	// An alias entered in another order is the same set of names.
	reordered := []OpenAppsecPolicy{
		{Hosts: []string{"b-alias.test", "b.test"}, Mode: ModeBlock, Confidence: ConfidenceHigh},
		sites[1], sites[2],
	}
	if third := RenderOpenAppsecPolicy(reordered); !bytes.Equal(first, third) {
		t.Errorf("a reordered alias list rendered differently:\n--- first ---\n%s\n--- third ---\n%s", first, third)
	}
	if again := RenderOpenAppsecPolicy(sites); !bytes.Equal(first, again) {
		t.Error("rendering twice gave two different documents")
	}
}

func TestOpenAppsecPolicyGolden(t *testing.T) {
	got := RenderOpenAppsecPolicy([]OpenAppsecPolicy{
		{Hosts: []string{"a.test", "www.a.test"}, Mode: ModeBlock, Confidence: ConfidenceHigh},
		{Hosts: []string{"b.test"}, Mode: ModeDetect, Confidence: ConfidenceCritical},
	})
	golden(t, "openappsec-policy.yaml", got)
}

// TestOpenAppsecPolicyShape pins the facts the agent measured, because getting
// any of them wrong makes the agent discard the policy while reporting success
// (docs/design/open-appsec-step4-findings.md §A).
func TestOpenAppsecPolicyShape(t *testing.T) {
	got := string(RenderOpenAppsecPolicy([]OpenAppsecPolicy{
		{Hosts: []string{"a.test"}, Mode: ModeBlock, Confidence: ConfidenceMedium},
		{Hosts: []string{"b.test"}, Mode: ModeDetect, Confidence: ConfidenceHigh},
	}))
	for _, want := range []string{
		"apiVersion: v1beta2",
		// Only hosts in specificRules are acted on (D4).
		"    mode: inactive",
		// The modes the panel's two names map to, and never the panel's own.
		"      mode: prevent-learn",
		"      mode: detect-learn",
		// A practice without all four blocks is silently discarded.
		"    webAttacks:\n",
		"    intrusionPrevention:\n",
		"    fileSecurity:\n",
		"    snortSignatures:\n",
		// Confidence is a practice field, not a rule field.
		"      minimumConfidence: medium",
		"      minimumConfidence: high",
		// The panel reads the agent's own log.
		"      logToAgent: true",
		"      cloud: false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the policy lacks %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"mode: block", "mode: detect\n", "confidence:"} {
		if strings.Contains(got, banned) {
			t.Errorf("the policy carries the panel's own spelling %q:\n%s", banned, got)
		}
	}
	// Every specificRules practice is defined in the same document. A rule that
	// names a practice the document lacks is discarded wholesale.
	practices := map[string]bool{}
	for _, line := range strings.Split(got, "\n") {
		if rest, ok := strings.CutPrefix(line, "  - name: "); ok {
			practices[rest] = true
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if rest, ok := strings.CutPrefix(line, "      threatPreventionPractices: ["); ok {
			name := strings.TrimSuffix(rest, "]")
			if !practices[name] {
				t.Errorf("a rule names the undefined practice %q", name)
			}
		}
	}
}

// A site with no host names must not make the renderer emit a rule with an empty
// host, which the agent would compile into a match on nothing.
func TestOpenAppsecPolicySkipsHostlessSites(t *testing.T) {
	got := string(RenderOpenAppsecPolicy([]OpenAppsecPolicy{{Mode: ModeBlock, Confidence: ConfidenceHigh}}))
	if strings.Contains(got, "specificRules:\n    -") {
		t.Errorf("a hostless site produced a rule:\n%s", got)
	}
	if !strings.Contains(got, "  specificRules: []") {
		t.Errorf("an empty set must render an empty list:\n%s", got)
	}
}

func TestOpenAppsecMainInclude(t *testing.T) {
	got := string(OpenAppsecMainInclude())
	if !strings.Contains(got, "load_module "+OpenAppsecModuleFile+";") {
		t.Errorf("the loader does not load the attachment module:\n%s", got)
	}
	if !strings.HasPrefix(got, "# Managed by PB panel") {
		t.Errorf("the loader is not marked as panel-written:\n%s", got)
	}
}

// The policy path is under the firewall directory, inside the tree confedit may
// write, and outside the nginx build the agent must not touch.
func TestOpenAppsecPaths(t *testing.T) {
	e := New(nil, testPaths, nil)
	want := "/www/panel/nginx/waf/open-appsec/local_policy.yaml"
	if got := e.OpenAppsecPolicyFile(); got != want {
		t.Errorf("policy path = %q, want %q", got, want)
	}
	if got := e.OpenAppsecModulePath(); got != "/www/panel/nginx/waf/main/open-appsec.conf" {
		t.Errorf("module path = %q, want it beside the ModSecurity loader", got)
	}
}
