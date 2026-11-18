package waf

import (
	"errors"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/nginx"
)

var testPaths = nginx.Paths{Root: "/www"}

func TestNormalizeDefaults(t *testing.T) {
	c, err := Config{}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != ModeOff || c.Paranoia != 1 || c.RateLimit.RequestsPerSecond != 10 {
		t.Errorf("zero config normalized to %+v", c)
	}
	if c.PathBlocklist == nil || c.IPRules.Allow == nil || c.IPRules.Deny == nil || c.Exclusions == nil {
		t.Errorf("lists must never be nil (they are JSON arrays): %+v", c)
	}
	if !c.IsDefault() {
		t.Error("a config that asks for nothing must say so")
	}
}

func TestNormalizeRejects(t *testing.T) {
	cases := map[string]func(*Config){
		"mode":                func(c *Config) { c.Mode = "maybe" },
		"paranoia 5":          func(c *Config) { c.Paranoia = 5 },
		"rps zero":            func(c *Config) { c.RateLimit = RateLimit{Enabled: true, RequestsPerSecond: 0, Burst: 5} },
		"burst negative":      func(c *Config) { c.RateLimit = RateLimit{Enabled: true, RequestsPerSecond: 5, Burst: -1} },
		"path no slash":       func(c *Config) { c.PathBlocklist = []string{"wp-admin"} },
		"path whole site":     func(c *Config) { c.PathBlocklist = []string{"/"} },
		"path semicolon":      func(c *Config) { c.PathBlocklist = []string{"/a;b"} },
		"path brace":          func(c *Config) { c.PathBlocklist = []string{"/a{b"} },
		"path space":          func(c *Config) { c.PathBlocklist = []string{"/a b"} },
		"path quote":          func(c *Config) { c.PathBlocklist = []string{`/a"b`} },
		"path star":           func(c *Config) { c.PathBlocklist = []string{"/a*"} },
		"ip junk":             func(c *Config) { c.IPRules.Deny = []string{"not-an-ip"} },
		"ip mapped":           func(c *Config) { c.IPRules.Deny = []string{"::ffff:1.2.3.4"} },
		"ip in both":          func(c *Config) { c.IPRules.Allow = []string{"10.0.0.1"}; c.IPRules.Deny = []string{"10.0.0.1/32"} },
		"exclusion id 0":      func(c *Config) { c.Exclusions = []Exclusion{{Kind: KindRule}} },
		"exclusion kind":      func(c *Config) { c.Exclusions = []Exclusion{{Kind: "all", RuleID: 1}} },
		"exclusion path":      func(c *Config) { c.Exclusions = []Exclusion{{Kind: KindRulePath, RuleID: 1, Path: "x"}} },
		"exclusion arg":       func(c *Config) { c.Exclusions = []Exclusion{{Kind: KindRuleArg, RuleID: 1, Arg: `a" "b`}} },
		"exclusion arg empty": func(c *Config) { c.Exclusions = []Exclusion{{Kind: KindRuleArg, RuleID: 1}} },
		"exclusion id huge":   func(c *Config) { c.Exclusions = []Exclusion{{Kind: KindRule, RuleID: MaxRuleID + 1}} },
		"exclusion note long": func(c *Config) {
			c.Exclusions = []Exclusion{{Kind: KindRule, RuleID: 1, Note: strings.Repeat("x", MaxNote+1)}}
		},
	}
	for name, mutate := range cases {
		c := Default()
		mutate(&c)
		_, err := c.Normalize()
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want a ValidationError", name, err)
		}
	}
}

func TestNormalizeCanonicalForm(t *testing.T) {
	c := Default()
	c.Mode = ModeBlock
	c.PathBlocklist = []string{" /a ", "/a", "/b"}
	c.IPRules.Deny = []string{"203.0.113.9", "10.1.2.3/8", "10.0.0.0/8", "2001:db8::1"}
	c.Exclusions = []Exclusion{{Kind: KindRule, RuleID: 942100, Path: "/ignored", Arg: "ignored", Note: "  a\nb  "}}
	got, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.PathBlocklist, ",") != "/a,/b" {
		t.Errorf("paths = %v", got.PathBlocklist)
	}
	if strings.Join(got.IPRules.Deny, ",") != "10.0.0.0/8,2001:db8::1/128,203.0.113.9/32" {
		t.Errorf("deny = %v", got.IPRules.Deny)
	}
	if e := got.Exclusions[0]; e.Path != "" || e.Arg != "" || e.Note != "a b" {
		t.Errorf("exclusion = %+v", e)
	}
}

func TestSiteIDNeverCollides(t *testing.T) {
	first, again := SiteID("a.test"), SiteID("a.test")
	if SiteID("a-b.com") == SiteID("a.b-com") || first != again {
		t.Error("site ids must be stable and distinct")
	}
}

func TestBlockBodyOff(t *testing.T) {
	body := string(BlockBody(testPaths, "a.test", Default()))
	if strings.Contains(body, "modsecurity") || strings.Contains(body, "limit_req") || strings.Contains(body, "if (") {
		t.Errorf("an off config rendered directives:\n%s", body)
	}
}

func TestBlockBodyEverything(t *testing.T) {
	c := Default()
	c.Mode = ModeBlock
	c.RateLimit = RateLimit{Enabled: true, RequestsPerSecond: 5, Burst: 7}
	c.PathBlocklist = []string{"/wp-admin"}
	c.IPRules.Deny = []string{"203.0.113.9"}
	c, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	body := string(BlockBody(testPaths, "a.test", c))
	id := SiteID("a.test")
	for _, want := range []string{
		"if ($pbwaf_" + id + "_ipdeny) { return 403; }",
		"if ($pbwaf_" + id + "_path) { return 403; }",
		"limit_req zone=pbwaf_" + id + " burst=7 nodelay;",
		"limit_req_status 429;",
		"modsecurity on;",
		`modsecurity_transaction_id "$request_id";`,
		"modsecurity_rules_file /www/panel/nginx/waf/rules/a.test.conf;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("block lacks %q:\n%s", want, body)
		}
	}
}

func TestHTTPIncludeDefinesWhatTheBlockReads(t *testing.T) {
	c := Default()
	c.RateLimit = RateLimit{Enabled: true, RequestsPerSecond: 5, Burst: 7}
	c.PathBlocklist = []string{"/.git", "/wp-admin"}
	c.IPRules.Allow = []string{"10.0.0.0/8"}
	c.IPRules.Deny = []string{"10.9.9.9"}
	c, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	inc := string(HTTPInclude("a.test", c))
	id := SiteID("a.test")
	for _, want := range []string{
		"limit_req_zone $binary_remote_addr zone=pbwaf_" + id + ":10m rate=5r/s;",
		"geo $remote_addr $pbwaf_" + id + "_ip {",
		"default 1;",
		"10.0.0.0/8 0;",
		"10.9.9.9/32 1;",
		`~^/\.well-known/acme-challenge/ 1;`,
		"map $uri $pbwaf_" + id + "_path {",
		`~*^/\.git 1;`,
		"~*^/wp-admin 1;",
	} {
		if !strings.Contains(inc, want) {
			t.Errorf("http include lacks %q:\n%s", want, inc)
		}
	}
	// Every variable the block reads is defined here.
	for _, v := range []string{"_ipdeny", "_path"} {
		if !strings.Contains(inc, "$pbwaf_"+id+v+" {") {
			t.Errorf("variable %s is read by the block but not defined", v)
		}
	}
}

func TestHTTPIncludeDenyOnlyDefaultsToAllow(t *testing.T) {
	c := Default()
	c.IPRules.Deny = []string{"203.0.113.0/24"}
	c, _ = c.Normalize()
	if inc := string(HTTPInclude("a.test", c)); !strings.Contains(inc, "default 0;") || strings.Contains(inc, "default 1;") {
		t.Errorf("a deny-only list must let everyone else in:\n%s", inc)
	}
}

func TestRulesFileModesAndParanoia(t *testing.T) {
	for _, tc := range []struct {
		mode   Mode
		engine string
	}{{ModeDetect, "SecRuleEngine DetectionOnly"}, {ModeBlock, "SecRuleEngine On"}} {
		c := Default()
		c.Mode, c.Paranoia = tc.mode, 3
		c, _ = c.Normalize()
		rules := string(RulesFile(testPaths, "a.test", c))
		for _, want := range []string{
			tc.engine,
			"tx.blocking_paranoia_level=3", "tx.detection_paranoia_level=3",
			"SecAuditLogFormat JSON",
			"SecAuditLog /www/panel/logs/sites/a.test.waf-audit.log",
			"Include " + testPaths.CRSDir() + "/rules/*.conf",
			"ctl:ruleEngine=Off", // certificate challenges are never inspected
		} {
			if !strings.Contains(rules, want) {
				t.Errorf("%s: rules lack %q", tc.mode, want)
			}
		}
	}
}

func TestRulesFileExclusionOrder(t *testing.T) {
	c := Default()
	c.Mode = ModeBlock
	c.Exclusions = []Exclusion{
		{Kind: KindRule, RuleID: 942100, Note: "login form"},
		{Kind: KindRulePath, RuleID: 920420, Path: "/api/"},
		{Kind: KindRuleArg, RuleID: 942200, Arg: "password"},
	}
	c, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	rules := string(RulesFile(testPaths, "a.test", c))
	include := strings.Index(rules, "Include "+testPaths.CRSDir())
	pathEx := strings.Index(rules, "ctl:ruleRemoveById=920420")
	removeEx := strings.Index(rules, "SecRuleRemoveById 942100")
	argEx := strings.Index(rules, `SecRuleUpdateTargetById 942200 "!ARGS:password"`)
	if pathEx < 0 || removeEx < 0 || argEx < 0 || include < 0 {
		t.Fatalf("an exclusion is missing:\n%s", rules)
	}
	if pathEx > include {
		t.Error("a ctl: exclusion has to run before the rule set sees the request")
	}
	if removeEx < include || argEx < include {
		t.Error("exclusions that edit the rule set have to come after it is loaded")
	}
	if !strings.Contains(rules, "# login form\nSecRuleRemoveById 942100") {
		t.Errorf("the note is not kept with its exclusion:\n%s", rules)
	}
}

func TestRenderConfigFiles(t *testing.T) {
	e := New(nil, testPaths, nil)
	off := e.RenderConfig("a.test", Default())
	if off.NeedsBlock {
		t.Error("an off config must not make the site file grow a block")
	}
	var deletesRules bool
	for _, f := range off.Files {
		if f.Path == testPaths.WAFRulesFile("a.test") && f.Delete {
			deletesRules = true
		}
	}
	if !deletesRules {
		t.Error("switching ModSecurity off must delete the rules file")
	}

	c := Default()
	c.Mode = ModeDetect
	on := e.RenderConfig("a.test", c)
	if !on.NeedsBlock {
		t.Error("ModSecurity on needs the block")
	}
	var writesRules bool
	for _, f := range on.Files {
		if f.Path == testPaths.WAFRulesFile("a.test") && !f.Delete && len(f.Content) > 0 {
			writesRules = true
		}
	}
	if !writesRules {
		t.Error("ModSecurity on must write the rules file")
	}
}
