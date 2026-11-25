package waf

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/guard"
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
	if c.Engine != EngineModSecurity || c.OpenAppsec.Confidence != ConfidenceHigh {
		t.Errorf("zero config normalized to engine %q, confidence %q", c.Engine, c.OpenAppsec.Confidence)
	}
	if c.PathBlocklist == nil || c.IPRules.Allow == nil || c.IPRules.Deny == nil || c.Exclusions == nil ||
		c.OpenAppsec.Exceptions == nil {
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

func TestNormalizeEngine(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want EngineKind
	}{
		{"", EngineModSecurity},
		{"modsecurity", EngineModSecurity},
		{" ModSecurity ", EngineModSecurity},
		{"open-appsec", EngineOpenAppsec},
		{"OPEN-APPSEC", EngineOpenAppsec},
	} {
		c, err := Config{Engine: EngineKind(tc.in)}.Normalize()
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if c.Engine != tc.want {
			t.Errorf("%q normalized to %q, want %q", tc.in, c.Engine, tc.want)
		}
	}
	for _, in := range []string{"apache", "openappsec", "mod-sec", "open_appsec"} {
		_, err := Config{Engine: EngineKind(in)}.Normalize()
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != "engine" {
			t.Errorf("%q: got %v, want an engine validation error", in, err)
		}
	}
}

func TestNormalizeConfidence(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Confidence
	}{
		{"", ConfidenceHigh},
		{"high", ConfidenceHigh},
		{"Critical", ConfidenceCritical},
		{" MEDIUM ", ConfidenceMedium},
	} {
		c := Default()
		c.OpenAppsec.Confidence = Confidence(tc.in)
		got, err := c.Normalize()
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got.OpenAppsec.Confidence != tc.want {
			t.Errorf("%q normalized to %q, want %q", tc.in, got.OpenAppsec.Confidence, tc.want)
		}
	}
	for _, in := range []string{"low", "very_high", "mid"} {
		c := Default()
		c.OpenAppsec.Confidence = Confidence(in)
		_, err := c.Normalize()
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != "open_appsec.confidence" {
			t.Errorf("%q: got %v, want an open_appsec.confidence validation error", in, err)
		}
	}
}

func TestNormalizeOpenAppsecExceptions(t *testing.T) {
	c := Default()
	c.Engine = EngineOpenAppsec
	c.Mode = ModeBlock
	c.OpenAppsec.Exceptions = []OpenAppsecException{
		{Kind: ExceptionParam, Param: " id ", Path: "/ignored", SourceIP: "ignored", Note: "  login\nform "},
		{Kind: ExceptionPath, Path: " /api/v1 ", Param: "ignored", Note: "public api"},
		{Kind: ExceptionSourceIP, SourceIP: "10.1.2.3/8", Path: "ignored", Note: "office"},
		{Kind: ExceptionSourceIP, SourceIP: "2001:db8::1", Note: "v6"},
		{Kind: ExceptionParam, Param: "id", Note: "login form"},
	}
	got, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	ex := got.OpenAppsec.Exceptions
	if len(ex) != 4 {
		t.Fatalf("exceptions = %+v, want the exact duplicate dropped", ex)
	}
	// Sorted by kind then the field, the fields an entry does not use cleared,
	// and the network masked the way the IP rules are.
	want := []OpenAppsecException{
		{Kind: ExceptionParam, Param: "id", Note: "login form"},
		{Kind: ExceptionPath, Path: "/api/v1", Note: "public api"},
		{Kind: ExceptionSourceIP, SourceIP: "10.0.0.0/8", Note: "office"},
		{Kind: ExceptionSourceIP, SourceIP: "2001:db8::1/128", Note: "v6"},
	}
	for i := range want {
		if ex[i] != want[i] {
			t.Errorf("exceptions[%d] = %+v, want %+v", i, ex[i], want[i])
		}
	}
	if got.OpenAppsec.Confidence != ConfidenceHigh {
		t.Errorf("confidence = %q, want the default", got.OpenAppsec.Confidence)
	}
}

// TestNormalizeOpenAppsecRejects pins the field name of every rejection, since
// that is what the 422 shows the operator.
func TestNormalizeOpenAppsecRejects(t *testing.T) {
	cases := map[string]struct {
		field  string
		mutate func(*Config)
	}{
		"engine": {field: "engine", mutate: func(c *Config) { c.Engine = "apache" }},
		"confidence": {field: "open_appsec.confidence", mutate: func(c *Config) {
			c.OpenAppsec.Confidence = "low"
		}},
		"exception kind": {field: "open_appsec.exceptions[0].kind", mutate: func(c *Config) {
			c.OpenAppsec.Exceptions = []OpenAppsecException{{Kind: "country"}}
		}},
		"exception path no slash": {field: "open_appsec.exceptions[0].path", mutate: func(c *Config) {
			c.OpenAppsec.Exceptions = []OpenAppsecException{{Kind: ExceptionPath, Path: "api"}}
		}},
		"exception path whole site": {field: "open_appsec.exceptions[0].path", mutate: func(c *Config) {
			c.OpenAppsec.Exceptions = []OpenAppsecException{{Kind: ExceptionPath, Path: "/"}}
		}},
		"exception path quote": {field: "open_appsec.exceptions[0].path", mutate: func(c *Config) {
			c.OpenAppsec.Exceptions = []OpenAppsecException{{Kind: ExceptionPath, Path: `/a"b`}}
		}},
		"exception ip junk": {field: "open_appsec.exceptions[0].source_ip", mutate: func(c *Config) {
			c.OpenAppsec.Exceptions = []OpenAppsecException{{Kind: ExceptionSourceIP, SourceIP: "nope"}}
		}},
		"exception ip mapped": {field: "open_appsec.exceptions[0].source_ip", mutate: func(c *Config) {
			c.OpenAppsec.Exceptions = []OpenAppsecException{{Kind: ExceptionSourceIP, SourceIP: "::ffff:1.2.3.4"}}
		}},
		"exception param empty": {field: "open_appsec.exceptions[0].param", mutate: func(c *Config) {
			c.OpenAppsec.Exceptions = []OpenAppsecException{{Kind: ExceptionParam}}
		}},
		"exception param quote": {field: "open_appsec.exceptions[0].param", mutate: func(c *Config) {
			c.OpenAppsec.Exceptions = []OpenAppsecException{{Kind: ExceptionParam, Param: `a" "b`}}
		}},
		"exception note long": {field: "open_appsec.exceptions[0].note", mutate: func(c *Config) {
			c.OpenAppsec.Exceptions = []OpenAppsecException{{Kind: ExceptionParam, Param: "id", Note: strings.Repeat("x", MaxNote+1)}}
		}},
		"too many exceptions": {field: "open_appsec.exceptions", mutate: func(c *Config) {
			c.OpenAppsec.Exceptions = make([]OpenAppsecException, MaxExclusions+1)
			for i := range c.OpenAppsec.Exceptions {
				c.OpenAppsec.Exceptions[i] = OpenAppsecException{Kind: ExceptionParam, Param: "id"}
			}
		}},
	}
	for name, tc := range cases {
		c := Default()
		c.Engine = EngineOpenAppsec
		tc.mutate(&c)
		_, err := c.Normalize()
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != tc.field {
			t.Errorf("%s: err = %v, want a %s validation error", name, err, tc.field)
		}
	}
}

// TestOpenAppsecKeepsModSecuritySettings: the two engines share one stored row,
// so switching the engine must not lose what the other one had.
func TestOpenAppsecKeepsModSecuritySettings(t *testing.T) {
	c := Default()
	c.Engine = EngineOpenAppsec
	c.Mode = ModeBlock
	c.Paranoia = 3
	c.Exclusions = []Exclusion{{Kind: KindRule, RuleID: 942100, Note: "login form"}}
	got, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got.Paranoia != 3 || len(got.Exclusions) != 1 || got.Exclusions[0].RuleID != 942100 {
		t.Errorf("the ModSecurity settings were not kept: paranoia %d, exclusions %+v", got.Paranoia, got.Exclusions)
	}
	if !got.UsesOpenAppsec() || got.UsesModSecurity() {
		t.Errorf("an open-appsec config still claims ModSecurity: %+v", got)
	}
	// And the open-appsec settings survive a switch back.
	back := got
	back.Engine = EngineModSecurity
	back, err = back.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if back.OpenAppsec.Confidence != ConfidenceHigh {
		t.Errorf("the open-appsec settings were not kept: %+v", back.OpenAppsec)
	}
}

// TestOldRowsReadAsModSecurity: every row stored before the engine field existed
// has no engine key and no open_appsec object, and has to read as it did then.
func TestOldRowsReadAsModSecurity(t *testing.T) {
	const old = `{"mode":"block","paranoia":2,"rate_limit":{"enabled":true,"requests_per_second":5,"burst":7},
		"path_blocklist":["/.git"],"ip_rules":{"allow":[],"deny":["203.0.113.9"]},
		"exclusions":[{"kind":"rule","rule_id":942100,"path":"","arg":"","note":"login"}],
		"challenge":{"enabled":false,"provider":"pow","difficulty":16,"clearance_minutes":60,
		"exempt_paths":[],"exempt_ips":[]}}`

	c, err := func() (Config, error) {
		c := Default()
		if err := json.Unmarshal([]byte(old), &c); err != nil {
			return Config{}, err
		}
		return c.Normalize()
	}()
	if err != nil {
		t.Fatal(err)
	}
	if c.Engine != EngineModSecurity || !c.UsesModSecurity() || c.UsesOpenAppsec() {
		t.Errorf("an old row read as %q (modsec %v, open-appsec %v)", c.Engine, c.UsesModSecurity(), c.UsesOpenAppsec())
	}
	if c.Paranoia != 2 || c.Mode != ModeBlock || len(c.Exclusions) != 1 || c.Exclusions[0].Note != "login" {
		t.Errorf("an old row lost a setting: %+v", c)
	}
	if c.OpenAppsec.Confidence != ConfidenceHigh || c.OpenAppsec.Exceptions == nil {
		t.Errorf("an old row did not get the open-appsec defaults: %+v", c.OpenAppsec)
	}

	// Round-trip: what the panel writes back still reads the same.
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	again := Default()
	if err := json.Unmarshal(raw, &again); err != nil {
		t.Fatal(err)
	}
	again, err = again.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if again.Engine != EngineModSecurity || !again.UsesModSecurity() || again.Paranoia != 2 {
		t.Errorf("the row did not round-trip: %+v", again)
	}
}

func TestEngineAwareUses(t *testing.T) {
	for _, tc := range []struct {
		engine EngineKind
		mode   Mode
		modsec bool
		appsec bool
	}{
		{EngineModSecurity, ModeOff, false, false},
		{EngineModSecurity, ModeDetect, true, false},
		{EngineModSecurity, ModeBlock, true, false},
		{EngineOpenAppsec, ModeOff, false, false},
		{EngineOpenAppsec, ModeDetect, false, true},
		{EngineOpenAppsec, ModeBlock, false, true},
		{"", ModeBlock, true, false},
	} {
		c := Default()
		c.Engine, c.Mode = tc.engine, tc.mode
		if got := c.UsesModSecurity(); got != tc.modsec {
			t.Errorf("%q/%q: UsesModSecurity = %v, want %v", c.Engine, tc.mode, got, tc.modsec)
		}
		if got := c.UsesOpenAppsec(); got != tc.appsec {
			t.Errorf("%q/%q: UsesOpenAppsec = %v, want %v", c.Engine, tc.mode, got, tc.appsec)
		}
		if want := !tc.modsec && !tc.appsec; c.IsDefault() != want {
			t.Errorf("%q/%q: IsDefault = %v, want %v", c.Engine, tc.mode, c.IsDefault(), want)
		}
	}
	// A chosen engine alone asks nginx for nothing.
	c := Default()
	c.Engine = EngineOpenAppsec
	if !c.IsDefault() {
		t.Fatal("picking an engine in mode off still renders directives")
	}
}

func TestCheckRefusesOpenAppsec(t *testing.T) {
	c := Default()
	c.Engine = EngineOpenAppsec
	c.Mode = ModeDetect

	e := New(nil, testPaths, nil)
	err := e.Check(context.Background(), c)
	var ve *ValidationError
	if errors.As(err, &ve) {
		t.Fatalf("Check returned a sites-level error from the waf package: %v", err)
	}
	if !errors.Is(err, ErrOpenAppsecMissing) {
		t.Fatalf("Check = %v, want ErrOpenAppsecMissing", err)
	}
	if !strings.Contains(err.Error(), "not available on this host") {
		t.Errorf("the error does not say why: %v", err)
	}

	// With a source that says yes, a configured engine is accepted.
	ok := New(nil, testPaths, nil).WithOpenAppsec(func(context.Context) (bool, string) { return true, "" })
	if err := ok.Check(context.Background(), c); err != nil {
		t.Errorf("Check refused an available engine: %v", err)
	}
	// and one that says no carries its reason.
	no := New(nil, testPaths, nil).WithOpenAppsec(func(context.Context) (bool, string) {
		return false, "the agent is not installed"
	})
	err = no.Check(context.Background(), c)
	if !errors.Is(err, ErrOpenAppsecMissing) || !strings.Contains(err.Error(), "the agent is not installed") {
		t.Errorf("Check = %v, want the source's reason", err)
	}
	// A ModSecurity config is not affected by the open-appsec state.
	modsec := Default()
	modsec.Mode = ModeBlock
	if err := no.Check(context.Background(), modsec); !errors.Is(err, ErrModuleMissing) {
		t.Errorf("a ModSecurity config with no build checks against the wrong engine: %v", err)
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

// TestChallengeExemptFromModSecurity: the challenge's own endpoints are the
// requests a visitor has to make to get through, so the engine never inspects
// them. A rule that flags the puzzle or the answer, or the 403 page itself,
// leaves a visitor at a check they cannot pass.
//
// The switch is in the two locations rather than in the rules file: nginx runs
// the request phases again after an internal redirect and takes the configuration
// of the location it lands in, so the page is exempt even though the rules file
// sees it under the URI the visitor asked for.
func TestChallengeExemptFromModSecurity(t *testing.T) {
	on := Default()
	on.Mode = ModeBlock
	on.Challenge = Challenge{Enabled: true}
	on, err := on.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	block := string(guardedEngine(t).RenderConfig("a.test", on).Block)
	page := strings.Index(block, "location = "+guard.PagePath+" {")
	sock := strings.Index(block, "location ^~ "+guard.Prefix+" {")
	if page < 0 || sock < 0 {
		t.Fatalf("the challenge locations are missing:\n%s", block)
	}
	for _, at := range []int{page, sock} {
		end := strings.Index(block[at:], "\n}")
		if end < 0 || !strings.Contains(block[at:at+end], "modsecurity off;") {
			t.Errorf("a challenge location is still inspected by ModSecurity:\n%s", block[at:at+end+2])
		}
	}

	// A site with the firewall off asks nginx for no engine at all, and a site
	// without a challenge must not carry the switch either: the directive is only
	// valid where the module is loaded for the site.
	engine := guardedEngine(t)
	for name, c := range map[string]Config{"firewall off": Default(), "no challenge": func() Config {
		x := Default()
		x.Mode = ModeBlock
		return x
	}()} {
		if block := string(engine.RenderConfig("a.test", c).Block); strings.Contains(block, "modsecurity off;") {
			t.Errorf("%s: switched the engine off in a location:\n%s", name, block)
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
