package waf

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/portbyte-id/server-panel/internal/nginx"
)

// Global firewall settings apply to every site, on top of the site's own
// settings. They are the protections a panel operator wants everywhere: bot
// blocking, dotfile and TRACE blocking, an address deny list, and the page a
// blocked visitor sees.

// Scanners are the attack tools a web server should not serve. Matched
// case-insensitively against the User-Agent.
var Scanners = []string{
	"sqlmap", "nikto", "nmap", "masscan", "zgrab", "acunetix", "nuclei", "wpscan",
	"dirbuster", "gobuster", "feroxbuster", "ffuf", "wfuzz", "havij", "netsparker",
	"xsstrike", "commix", "sqlninja", "openvas", "nessus", "w3af", "arachni",
}

// AICrawlers are the crawlers that collect pages for AI training or answers.
var AICrawlers = []string{
	"GPTBot", "ChatGPT-User", "OAI-SearchBot", "ClaudeBot", "Claude-Web", "anthropic-ai",
	"CCBot", "PerplexityBot", "Bytespider", "Amazonbot", "Meta-ExternalAgent", "cohere-ai",
	"Diffbot",
}

// SEOCrawlers are the crawlers that build link and keyword indexes for sale.
var SEOCrawlers = []string{
	"AhrefsBot", "SemrushBot", "MJ12bot", "DotBot", "PetalBot", "BLEXBot", "DataForSeoBot",
}

// Limits for the global settings.
const (
	MaxCustomAgents  = 200
	MaxAgentLen      = 64
	MaxErrorPageHTML = 64 << 10
)

// DefaultErrorPageHTML is the page a blocked visitor sees until the operator
// writes one. `<!--#echo var="request_id" -->` is filled in by nginx per
// request; the id is the one the access log records, so support can match it.
const DefaultErrorPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Request blocked</title>
<style>
  body { font-family: system-ui, sans-serif; margin: 0; min-height: 100vh; display: grid; place-items: center; background: #f6f6f7; color: #1d1d1f; }
  main { max-width: 32rem; padding: 2rem; }
  h1 { font-size: 1.5rem; margin: 0 0 .75rem; }
  p { line-height: 1.5; margin: .5rem 0; }
  code { font-size: .85rem; color: #555; }
</style>
</head>
<body>
<main>
  <h1>Request blocked</h1>
  <p>This request was stopped by the web application firewall.</p>
  <p>If you believe this is a mistake, contact the site owner and quote the reference below.</p>
  <p><code>Reference: <!--#echo var="request_id" --></code></p>
</main>
</body>
</html>
`

// BotBlock blocks clients by their User-Agent. The lists are presets the
// operator switches on; Custom holds the rest.
type BotBlock struct {
	Enabled     bool     `json:"enabled"`
	EmptyAgent  bool     `json:"empty_agent"`
	Scanners    bool     `json:"scanners"`
	AICrawlers  bool     `json:"ai_crawlers"`
	SEOCrawlers bool     `json:"seo_crawlers"`
	Custom      []string `json:"custom"`
}

// ErrorPage is the page a blocked request gets, in place of nginx's bare 403.
type ErrorPage struct {
	Enabled bool   `json:"enabled"`
	HTML    string `json:"html"`
}

// Global is the firewall that applies to every site.
type Global struct {
	BotBlock BotBlock `json:"bot_block"`
	// SensitiveFiles refuses requests for dotfiles and dot-directories (/.git,
	// /.env, /.svn). /.well-known/ stays open for certificate and standards paths.
	SensitiveFiles bool `json:"sensitive_files"`
	// BlockTrace refuses the TRACE and TRACK methods, which echo request headers
	// back and help cookie-stealing attacks.
	BlockTrace bool `json:"block_trace"`
	// IPDeny are addresses and networks refused on every site.
	IPDeny    []string  `json:"ip_deny"`
	ErrorPage ErrorPage `json:"error_page"`
}

// DefaultGlobal is the global firewall with nothing switched on. The scanner
// preset is ticked, so switching bot blocking on blocks attack tools first.
func DefaultGlobal() Global {
	return Global{
		BotBlock:  BotBlock{Scanners: true, Custom: []string{}},
		IPDeny:    []string{},
		ErrorPage: ErrorPage{HTML: DefaultErrorPageHTML},
	}
}

// Agents is the User-Agent list the bot switch blocks, presets first.
func (b BotBlock) Agents() []string {
	var out []string
	if b.Scanners {
		out = append(out, Scanners...)
	}
	if b.AICrawlers {
		out = append(out, AICrawlers...)
	}
	if b.SEOCrawlers {
		out = append(out, SEOCrawlers...)
	}
	return append(out, b.Custom...)
}

// BotActive reports whether bot blocking refuses anything.
func (g Global) BotActive() bool {
	return g.BotBlock.Enabled && (g.BotBlock.EmptyAgent || len(g.BotBlock.Agents()) > 0)
}

// Active reports whether the global firewall asks any site for a rule.
func (g Global) Active() bool {
	return g.BotActive() || g.SensitiveFiles || g.BlockTrace || len(g.IPDeny) > 0
}

// Normalize checks and tidies the global settings.
func (g Global) Normalize() (Global, error) {
	if g.BotBlock.Custom == nil {
		g.BotBlock.Custom = []string{}
	}
	custom, err := normalizeAgents("bot_block.custom", g.BotBlock.Custom)
	if err != nil {
		return Global{}, err
	}
	g.BotBlock.Custom = custom

	g.IPDeny, err = normalizeAddrs("ip_deny", g.IPDeny)
	if err != nil {
		return Global{}, err
	}

	if strings.TrimSpace(g.ErrorPage.HTML) == "" {
		g.ErrorPage.HTML = DefaultErrorPageHTML
	}
	if len(g.ErrorPage.HTML) > MaxErrorPageHTML {
		return Global{}, invalid("error_page.html", "at most %d bytes", MaxErrorPageHTML)
	}
	if err := checkErrorPageHTML(g.ErrorPage.HTML); err != nil {
		return Global{}, err
	}
	return g, nil
}

var agentPattern = regexp.MustCompile(`^[A-Za-z0-9._/+-]{2,64}$`)

// normalizeAgents validates the custom User-Agent substrings. The charset is
// narrow on purpose: an entry goes into an nginx map pattern, so quotes,
// backslashes and spaces are not allowed.
func normalizeAgents(field string, in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		a := strings.TrimSpace(raw)
		if a == "" {
			continue
		}
		if len(a) > MaxAgentLen || !agentPattern.MatchString(a) {
			return nil, invalid(field, "%q must be 2 to %d letters, digits or . _ / + -", a, MaxAgentLen)
		}
		key := strings.ToLower(a)
		if !seen[key] {
			seen[key] = true
			out = append(out, a)
		}
	}
	if len(out) > MaxCustomAgents {
		return nil, invalid(field, "at most %d entries", MaxCustomAgents)
	}
	sort.Strings(out)
	return out, nil
}

var ssiDirective = regexp.MustCompile(`<!--#\s*([A-Za-z]+)`)

// checkErrorPageHTML allows nginx's echo directive, which the page needs for the
// request id, and nothing else: include and exec would read files or run
// commands on behalf of every blocked visitor.
func checkErrorPageHTML(html string) error {
	for _, m := range ssiDirective.FindAllStringSubmatch(html, -1) {
		if !strings.EqualFold(m[1], "echo") {
			return invalid("error_page.html", "the server-side directive %q is not allowed; only echo is", m[1])
		}
	}
	return nil
}

// botPattern is the case-insensitive User-Agent alternation for the agents. A dot
// or a plus is written as a character class, so the pattern has no backslash and
// can sit inside a quoted nginx string.
func botPattern(agents []string) string {
	parts := make([]string, len(agents))
	for i, a := range agents {
		parts[i] = strings.NewReplacer(".", "[.]", "+", "[+]").Replace(a)
	}
	return strings.Join(parts, "|")
}

// writeGlobalBlock writes the global rules a site's block applies. Each refers to
// a variable GlobalHTTPInclude defines, and only the ones that are on are written,
// so nginx never reads an undefined variable.
func writeGlobalBlock(b *strings.Builder, g Global) {
	b.WriteString("# Global protections (every site).\n")
	if len(g.IPDeny) > 0 {
		fmt.Fprintf(b, "if (%s) { return 403; }\n", globalIP)
	}
	if g.BotActive() {
		fmt.Fprintf(b, "if (%s) { return 403; }\n", globalBot)
	}
	if g.SensitiveFiles {
		fmt.Fprintf(b, "if (%s) { return 403; }\n", globalDot)
	}
	if g.BlockTrace {
		fmt.Fprintf(b, "if (%s) { return 403; }\n", globalTrace)
	}
}

// writeErrorPage sends a blocked request to the panel's page instead of nginx's
// bare 403. The status stays 403, and the page location is internal and skips
// the site's engine, as the challenge page does: a page served to a visitor who
// has just been refused must not be refused again by the same engine. A 403 the
// site's own application returns is replaced too.
func writeErrorPage(b *strings.Builder, p nginx.Paths, modsec, appsec bool) {
	b.WriteString("# Blocked requests get the panel's page (see Settings > WAF > Block page).\n")
	// Each status keeps its own code: a rate limit stays a 429.
	fmt.Fprintf(b, "error_page 403 =403 %s;\n", BlockedPagePath)
	fmt.Fprintf(b, "error_page 429 =429 %s;\n", BlockedPagePath)
	fmt.Fprintf(b, "location = %s {\n", BlockedPagePath)
	b.WriteString("    internal;\n")
	fmt.Fprintf(b, "    alias %s;\n", p.WAFErrorPage())
	b.WriteString("    default_type text/html;\n")
	b.WriteString("    ssi on;\n")
	b.WriteString("    add_header Cache-Control \"no-store\" always;\n")
	b.WriteString("    add_header X-Content-Type-Options \"nosniff\" always;\n")
	if modsec {
		b.WriteString("    modsecurity off;\n")
	}
	if appsec {
		b.WriteString("    cp-nano-nginx-attachment off;\n")
	}
	b.WriteString("}\n")
}

// GlobalHTTPInclude is the http-context file of the global firewall. It defines
// the variables the site blocks read: each is a map or geo keyed on the skip
// flag, so the block page and certificate challenges are never refused. Maps are
// volatile, since nginx would otherwise keep a value across the internal redirect
// to the page.
func GlobalHTTPInclude(g Global) []byte {
	var b strings.Builder
	b.WriteString("# Managed by PB panel (global firewall).\n")
	if !g.Active() && !g.ErrorPage.Enabled {
		b.WriteString("# No global protection is on.\n")
		return []byte(b.String())
	}
	// 1 is the block page, 2 the certificate challenge: both are exempt.
	b.WriteString("map $uri " + globalSkip + " {\n    volatile;\n    default 0;\n")
	fmt.Fprintf(&b, "    %s 1;\n", BlockedPagePath)
	b.WriteString("    ~^/\\.well-known/acme-challenge/ 2;\n}\n")

	if len(g.IPDeny) > 0 {
		b.WriteString("geo $remote_addr $pbwaf_g_ipraw {\n    default 0;\n")
		for _, a := range g.IPDeny {
			fmt.Fprintf(&b, "    %s 1;\n", a)
		}
		b.WriteString("}\n")
		fmt.Fprintf(&b, "map \"%s:$pbwaf_g_ipraw\" %s {\n    volatile;\n    default 0;\n    \"~^0:1$\" 1;\n}\n", globalSkip, globalIP)
	}
	if g.BotActive() {
		fmt.Fprintf(&b, "map \"%s:$http_user_agent\" %s {\n    volatile;\n    default 0;\n", globalSkip, globalBot)
		if g.BotBlock.EmptyAgent {
			b.WriteString("    \"~^0:$\" 1;\n")
		}
		if agents := g.BotBlock.Agents(); len(agents) > 0 {
			fmt.Fprintf(&b, "    \"~*^0:.*(%s)\" 1;\n", botPattern(agents))
		}
		b.WriteString("}\n")
	}
	if g.SensitiveFiles {
		fmt.Fprintf(&b, "map \"%s:$uri\" %s {\n    volatile;\n    default 0;\n", globalSkip, globalDot)
		b.WriteString("    \"~^0:.*/\\.(?!well-known(/|$))\" 1;\n}\n")
	}
	if g.BlockTrace {
		fmt.Fprintf(&b, "map \"%s:$request_method\" %s {\n    volatile;\n    default 0;\n", globalSkip, globalTrace)
		b.WriteString("    \"~^0:(TRACE|TRACK)$\" 1;\n}\n")
	}
	return []byte(b.String())
}
