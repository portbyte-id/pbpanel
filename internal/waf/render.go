package waf

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/portbyte-id/server-panel/internal/guard"
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

// challengeNames are the variables the challenge reads: the exempt paths, the
// exempt addresses, whether the request is the page itself, and the verdict.
func challengeNames(domain string) (cpath, cip, page, verdict string) {
	id := SiteID(domain)
	return "$pbwaf_" + id + "_cpath", "$pbwaf_" + id + "_cip", "$pbwaf_" + id + "_cpage", "$pbwaf_" + id + "_challenge"
}

// CookieName is the clearance cookie of a site.
func CookieName(domain string) string { return "pbg_" + SiteID(domain) }

// BlockBody is what goes in the site file's waf block: the server-context
// directives. A config that asks for nothing renders a comment only. It leaves
// the challenge out, which needs the site's secret: see Engine.RenderConfig.
func BlockBody(p nginx.Paths, domain string, c Config) []byte {
	return blockBody(p, domain, c, "")
}

func blockBody(p nginx.Paths, domain string, c Config, secret string) []byte {
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
	if c.Challenge.Enabled && secret != "" {
		writeChallengeBlock(&b, p, domain, c.Challenge, secret, c.UsesModSecurity())
	}
	return []byte(b.String())
}

// writeChallengeBlock renders the challenge. nginx checks the clearance cookie
// itself (secure_link), so a visitor who passed never reaches the panel. One who
// has not gets the static page with 403: error_page with a URI turns any method
// into GET, which the static file can answer. The page's own requests go to the
// panel over its socket.
//
// The locations are exact and ^~ prefix matches, so a site's own regex
// locations, a `location ~ /\.` that denies dotfiles for one, cannot take them.
//
// With ModSecurity on, the same locations turn the engine off for themselves:
// the puzzle fetch and the answer are the requests a visitor has to make to get
// through, and a rule that flags either one holds them at a check they cannot
// pass. An exemption in the rules file is not enough for the guard page, which
// nginx reaches through an internal redirect carrying the original URI.
func writeChallengeBlock(b *strings.Builder, p nginx.Paths, domain string, ch Challenge, secret string, modsec bool) {
	_, _, _, verdict := challengeNames(domain)
	b.WriteString("# Challenge page: a visitor without a valid clearance cookie is checked first.\n")
	fmt.Fprintf(b, "secure_link $cookie_%s;\n", CookieName(domain))
	fmt.Fprintf(b, "secure_link_md5 \"%s\";\n", guard.SecureLinkMD5(secret))
	fmt.Fprintf(b, "if (%s) { return 418; }\n", verdict)
	fmt.Fprintf(b, "error_page 418 =403 %s;\n", guard.PagePath)
	fmt.Fprintf(b, "location = %s {\n", guard.PagePath)
	b.WriteString("    internal;\n")
	fmt.Fprintf(b, "    alias %s;\n", p.GuardPage(domain))
	b.WriteString("    default_type text/html;\n")
	b.WriteString("    add_header Cache-Control \"no-store\" always;\n")
	fmt.Fprintf(b, "    add_header Content-Security-Policy \"%s\" always;\n", guard.CSP(ch.Provider))
	b.WriteString("    add_header X-Frame-Options \"DENY\" always;\n")
	b.WriteString("    add_header Referrer-Policy \"no-referrer\" always;\n")
	b.WriteString("    add_header X-Content-Type-Options \"nosniff\" always;\n")
	if modsec {
		b.WriteString("    modsecurity off;\n")
	}
	b.WriteString("}\n")
	fmt.Fprintf(b, "location ^~ %s {\n", guard.Prefix)
	fmt.Fprintf(b, "    proxy_pass http://unix:%s;\n", p.GuardSocket())
	b.WriteString("    proxy_http_version 1.1;\n")
	b.WriteString("    proxy_set_header Connection \"\";\n")
	b.WriteString("    proxy_set_header Host $host;\n")
	fmt.Fprintf(b, "    proxy_set_header %s \"%s\";\n", guard.HeaderSite, domain)
	fmt.Fprintf(b, "    proxy_set_header %s $remote_addr;\n", guard.HeaderClient)
	fmt.Fprintf(b, "    proxy_set_header %s $scheme;\n", guard.HeaderScheme)
	b.WriteString("    proxy_set_header User-Agent $http_user_agent;\n")
	b.WriteString("    proxy_set_header Cookie \"\";\n")
	b.WriteString("    client_max_body_size 16k;\n")
	b.WriteString("    add_header Cache-Control \"no-store\" always;\n")
	if modsec {
		b.WriteString("    modsecurity off;\n")
	}
	b.WriteString("}\n")
}

// writeChallengeHTTP renders the challenge's http-context variables. The
// verdict is 1 only for a request that is neither exempt nor carrying a valid
// cookie: $secure_link is empty for a missing or forged cookie, "0" for an
// expired one and "1" for a valid one.
//
// nginx caches a map's value for the whole request, internal redirects
// included, and the server-level `if` runs again after each one. That is what
// the exemption wants: /api/x that a site rewrites to /index.php stays exempt.
// The verdict cannot be cached, though: the redirect to the page would find the
// cached 1, challenge the page itself, and nginx would give up with a bare 418.
// So the verdict is volatile and reads a volatile "this is the page" flag.
func writeChallengeHTTP(b *strings.Builder, domain string, ch Challenge) {
	cpath, cip, page, verdict := challengeNames(domain)
	fmt.Fprintf(b, "geo $remote_addr %s {\n    default 0;\n", cip)
	for _, a := range ch.ExemptIPs {
		fmt.Fprintf(b, "    %s 1;\n", a)
	}
	b.WriteString("}\n")
	fmt.Fprintf(b, "map $uri %s {\n    default 0;\n", cpath)
	b.WriteString("    ~^/\\.well-known/acme-challenge/ 1;\n")
	fmt.Fprintf(b, "    ~^%s 1;\n", strings.ReplaceAll(guard.Prefix, ".", `\.`))
	fmt.Fprintf(b, "    %s 1;\n", guard.PagePath)
	for _, p := range ch.ExemptPaths {
		fmt.Fprintf(b, "    ~*^%s 1;\n", strings.ReplaceAll(p, ".", `\.`))
	}
	b.WriteString("}\n")
	fmt.Fprintf(b, "map $uri %s {\n    volatile;\n    default 0;\n    %s 1;\n}\n", page, guard.PagePath)
	fmt.Fprintf(b, "map \"$secure_link:%s%s%s\" %s {\n    volatile;\n    default 0;\n    \":000\" 1;\n    \"0:000\" 1;\n}\n", cpath, cip, page, verdict)
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

	if c.Challenge.Enabled {
		writeChallengeHTTP(&b, domain, c.Challenge)
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
