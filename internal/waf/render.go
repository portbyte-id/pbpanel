package waf

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/portbyte-id/server-panel/internal/nginx"
)

// ModuleFile is the ModSecurity module every nginx build ships, as `load_module`
// takes it: relative to the build's own prefix, so a candidate build loads its own
// module and not the one of the build that serves. The Core Rule Set and the other
// ModSecurity files live in the build too: see nginx.Paths.ModSecDir and CRSDir.
const (
	ModuleFile = "modules/ngx_http_modsecurity_module.so"

	// crsSetupVersion only has to be set: rule 901001 stops the engine when it
	// is missing, which is how CRS notices a setup file that was never loaded.
	crsSetupVersion = 4000

	// firstRuleID is where the rules this package writes start. They are in the
	// range CRS reserves for local rules (above 9000000 is CRS's; 10000-99999 is
	// free) and unique inside one site's rule set.
	firstRuleID = 10100
)

// SiteID is a short stable name for a site, used in the names of the zones and
// variables its files define. A domain cannot be used directly: `a-b.com` and
// `a.b-com` would collapse to the same identifier.
func SiteID(domain string) string {
	h := sha256.Sum256([]byte(domain))
	return "w" + hex.EncodeToString(h[:5])
}

// zone and variable names for one site.
func names(domain string) (zone, ip, acme, ipdeny, path string) {
	id := SiteID(domain)
	return "pbwaf_" + id, "$pbwaf_" + id + "_ip", "$pbwaf_" + id + "_acme",
		"$pbwaf_" + id + "_ipdeny", "$pbwaf_" + id + "_path"
}

// BlockBody is what goes in the site file's waf block: the server-context
// directives. A config that asks for nothing renders a comment only.
func BlockBody(p nginx.Paths, domain string, c Config) []byte {
	zone, _, _, ipdeny, path := names(domain)
	var b strings.Builder
	b.WriteString("# Managed by PB panel (firewall).\n")
	if c.IsDefault() {
		b.WriteString("# The firewall is off for this site.\n")
		return []byte(b.String())
	}
	if len(c.IPRules.Allow)+len(c.IPRules.Deny) > 0 {
		fmt.Fprintf(&b, "if (%s) { return 403; }\n", ipdeny)
	}
	if len(c.PathBlocklist) > 0 {
		fmt.Fprintf(&b, "if (%s) { return 403; }\n", path)
	}
	if c.RateLimit.Enabled {
		fmt.Fprintf(&b, "limit_req zone=%s burst=%d nodelay;\n", zone, c.RateLimit.Burst)
		b.WriteString("limit_req_status 429;\n")
	}
	if c.UsesModSecurity() {
		b.WriteString("modsecurity on;\n")
		// The request id ties an audit event to the access log line.
		b.WriteString("modsecurity_transaction_id \"$request_id\";\n")
		fmt.Fprintf(&b, "modsecurity_rules_file %s;\n", p.WAFRulesFile(domain))
	}
	return []byte(b.String())
}

// HTTPInclude is the site's http-context file: the rate-limit zone and the
// variables the block's `if` lines read. It exists for every site, and is only
// a comment when the site uses none of it, so removing a protection never
// leaves a variable the site file still reads undefined.
func HTTPInclude(domain string, c Config) []byte {
	zone, ip, acme, ipdeny, path := names(domain)
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by PB panel (firewall, %s).\n", domain)

	if c.RateLimit.Enabled {
		fmt.Fprintf(&b, "limit_req_zone $binary_remote_addr zone=%s:10m rate=%dr/s;\n", zone, c.RateLimit.RequestsPerSecond)
	}

	if len(c.IPRules.Allow)+len(c.IPRules.Deny) > 0 {
		// 1 means refused. Entries are matched by the longest prefix, so a deny
		// entry inside an allowed network still wins.
		def := 0
		if len(c.IPRules.Allow) > 0 {
			def = 1
		}
		fmt.Fprintf(&b, "geo $remote_addr %s {\n    default %d;\n", ip, def)
		for _, a := range c.IPRules.Allow {
			fmt.Fprintf(&b, "    %s 0;\n", a)
		}
		for _, d := range c.IPRules.Deny {
			fmt.Fprintf(&b, "    %s 1;\n", d)
		}
		b.WriteString("}\n")
		// Certificate challenges must reach the site from anywhere, or an allow
		// list would stop its certificate from renewing.
		fmt.Fprintf(&b, "map $uri %s {\n    default 0;\n    ~^/\\.well-known/acme-challenge/ 1;\n}\n", acme)
		fmt.Fprintf(&b, "map \"%s%s\" %s {\n    default 0;\n    \"10\" 1;\n}\n", ip, acme, ipdeny)
	}

	if len(c.PathBlocklist) > 0 {
		// $uri is decoded and normalized, so %2e%2e and // tricks do not slip past.
		fmt.Fprintf(&b, "map $uri %s {\n    default 0;\n", path)
		for _, p := range c.PathBlocklist {
			fmt.Fprintf(&b, "    ~*^%s 1;\n", strings.ReplaceAll(p, ".", `\.`))
		}
		b.WriteString("}\n")
	}
	return []byte(b.String())
}

// MainInclude is the main-context file that loads the module. It exists while
// at least one site uses ModSecurity.
func MainInclude() []byte {
	return []byte("# Managed by PB panel (firewall).\nload_module " + ModuleFile + ";\n")
}

// RulesFile is the site's ModSecurity configuration: the engine mode, the audit
// log, the Core Rule Set at the site's paranoia level, and the exclusions.
//
// Order matters. Exclusions that act on a request (ctl:) run before the rule
// set sees it; those that edit the rule set (remove, update target) have to run
// after the rule set is loaded.
func RulesFile(p nginx.Paths, domain string, c Config) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by PB panel (ModSecurity, %s).\n", domain)

	engine := "DetectionOnly"
	if c.Mode == ModeBlock {
		engine = "On"
	}
	fmt.Fprintf(&b, "SecRuleEngine %s\n", engine)
	b.WriteString("SecRequestBodyAccess On\n")
	b.WriteString("SecRequestBodyLimit 13107200\n")
	b.WriteString("SecRequestBodyNoFilesLimit 131072\n")
	b.WriteString("SecRequestBodyLimitAction Reject\n")
	b.WriteString("SecResponseBodyAccess Off\n")
	b.WriteString("SecTmpDir /tmp/\nSecDataDir /tmp/\n")
	fmt.Fprintf(&b, "SecUnicodeMapFile %s/unicode.mapping 20127\n", p.ModSecDir())

	// JSON, one event per line: the panel's audit viewer reads it back.
	b.WriteString("SecAuditEngine RelevantOnly\n")
	b.WriteString("SecAuditLogRelevantStatus \"^(?:5|4(?!04))\"\n")
	b.WriteString("SecAuditLogParts ABFHZ\n")
	b.WriteString("SecAuditLogType Serial\n")
	b.WriteString("SecAuditLogFormat JSON\n")
	fmt.Fprintf(&b, "SecAuditLog %s\n", p.WAFAuditLog(domain))

	// Certificate challenges are never inspected.
	id := firstRuleID
	fmt.Fprintf(&b, "SecRule REQUEST_URI \"@beginsWith /.well-known/acme-challenge/\" \"id:%d,phase:1,pass,nolog,ctl:ruleEngine=Off\"\n", id)
	id++

	b.WriteString("\n# Core Rule Set setup.\n")
	b.WriteString("SecDefaultAction \"phase:1,log,auditlog,pass\"\n")
	b.WriteString("SecDefaultAction \"phase:2,log,auditlog,pass\"\n")
	fmt.Fprintf(&b, "SecAction \"id:900000,phase:1,pass,t:none,nolog,setvar:tx.blocking_paranoia_level=%d\"\n", c.Paranoia)
	fmt.Fprintf(&b, "SecAction \"id:900001,phase:1,pass,t:none,nolog,setvar:tx.detection_paranoia_level=%d\"\n", c.Paranoia)
	fmt.Fprintf(&b, "SecAction \"id:900990,phase:1,pass,t:none,nolog,setvar:tx.crs_setup_version=%d\"\n", crsSetupVersion)

	var before, after strings.Builder
	for _, e := range c.Exclusions {
		note := ""
		if e.Note != "" {
			note = "# " + e.Note + "\n"
		}
		switch e.Kind {
		case KindRule:
			fmt.Fprintf(&after, "%sSecRuleRemoveById %d\n", note, e.RuleID)
		case KindRulePath:
			fmt.Fprintf(&before, "%sSecRule REQUEST_URI \"@beginsWith %s\" \"id:%d,phase:1,pass,nolog,ctl:ruleRemoveById=%d\"\n",
				note, e.Path, id, e.RuleID)
			id++
		case KindRuleArg:
			fmt.Fprintf(&after, "%sSecRuleUpdateTargetById %d \"!ARGS:%s\"\n", note, e.RuleID, e.Arg)
		}
	}
	if before.Len() > 0 {
		b.WriteString("\n# Exclusions that apply before the rules run.\n")
		b.WriteString(before.String())
	}

	fmt.Fprintf(&b, "\nInclude %s/rules/*.conf\n", p.CRSDir())

	if after.Len() > 0 {
		b.WriteString("\n# Exclusions that edit the loaded rules.\n")
		b.WriteString(after.String())
	}
	return []byte(b.String())
}
