// Package waf is the per-site web application firewall: native nginx
// protections (rate limit, path blocklist, IP rules) that work on any nginx
// flavor, and ModSecurity v3 with the OWASP Core Rule Set, which the
// post-quantum flavor bundles.
//
// The package renders configuration and keeps each site's settings in the
// database. It never writes a file itself: the sites manager puts the rendered
// files through confedit, so every change is validated with `nginx -t` first and
// rolled back when nginx rejects it.
package waf

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/portbyte-id/server-panel/internal/guard"
)

// Mode is what ModSecurity does with a request the rules flag.
type Mode string

// Modes.
const (
	ModeOff    Mode = "off"
	ModeDetect Mode = "detect"
	ModeBlock  Mode = "block"
)

// Kind is how an exclusion narrows the rule set.
type Kind string

// Exclusion kinds.
const (
	// KindRule turns a rule off for the whole site.
	KindRule Kind = "rule"
	// KindRulePath turns a rule off for requests whose path starts with Path.
	KindRulePath Kind = "rule_path"
	// KindRuleArg stops a rule from inspecting one request argument.
	KindRuleArg Kind = "rule_arg"
)

// Limits. They bound what one site can put in a file nginx has to parse.
const (
	MaxBlocklist  = 200
	MaxIPEntries  = 500
	MaxExclusions = 200
	MaxNote       = 200
	MaxPathLen    = 255
	MaxRuleID     = 9999999

	maxRPS   = 10000
	maxBurst = 100000
)

// RateLimit limits requests per client address. Excess requests get a 429.
type RateLimit struct {
	Enabled           bool `json:"enabled"`
	RequestsPerSecond int  `json:"requests_per_second"`
	Burst             int  `json:"burst"`
}

// IPRules are addresses and networks, in CIDR or plain form. A non-empty Allow
// list admits only those clients; Deny always wins over Allow, since the more
// specific entry decides.
type IPRules struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

// Exclusion is one rule exception, so a false positive does not need the whole
// firewall switched off.
type Exclusion struct {
	Kind   Kind   `json:"kind"`
	RuleID int    `json:"rule_id"`
	Path   string `json:"path"`
	Arg    string `json:"arg"`
	Note   string `json:"note"`
}

// Challenge limits and defaults.
const (
	MinDifficulty     = 8
	MaxDifficulty     = 24
	DefaultDifficulty = 16

	MinClearanceMinutes     = 5
	MaxClearanceMinutes     = 7 * 24 * 60
	DefaultClearanceMinutes = 60

	// MaxTurnstileKey bounds a Turnstile site key or secret key.
	MaxTurnstileKey = 128
)

// Challenge is the page a visitor has to pass before the site opens: a
// proof of work solved in the browser, or Cloudflare Turnstile.
type Challenge struct {
	Enabled  bool           `json:"enabled"`
	Provider guard.Provider `json:"provider"`
	// Difficulty is the number of leading zero bits the proof of work needs.
	// Each extra bit doubles the average work: 16 takes well under a second.
	Difficulty int `json:"difficulty"`
	// ClearanceMinutes is how long a visitor who passed is let through.
	ClearanceMinutes int `json:"clearance_minutes"`
	// ExemptPaths are path prefixes that are never challenged: APIs, webhooks,
	// feeds, anything a program and not a browser requests.
	ExemptPaths []string `json:"exempt_paths"`
	// ExemptIPs are addresses and networks that are never challenged.
	ExemptIPs []string `json:"exempt_ips"`
}

// Config is one site's firewall.
type Config struct {
	Mode          Mode        `json:"mode"`
	Paranoia      int         `json:"paranoia"`
	RateLimit     RateLimit   `json:"rate_limit"`
	PathBlocklist []string    `json:"path_blocklist"`
	IPRules       IPRules     `json:"ip_rules"`
	Exclusions    []Exclusion `json:"exclusions"`
	Challenge     Challenge   `json:"challenge"`
}

// DefaultChallenge is a challenge that is off.
func DefaultChallenge() Challenge {
	return Challenge{
		Provider:         guard.ProviderPoW,
		Difficulty:       DefaultDifficulty,
		ClearanceMinutes: DefaultClearanceMinutes,
		ExemptPaths:      []string{},
		ExemptIPs:        []string{},
	}
}

// Default is a site with the firewall off.
func Default() Config {
	return Config{
		Mode:          ModeOff,
		Paranoia:      1,
		RateLimit:     RateLimit{RequestsPerSecond: 10, Burst: 20},
		PathBlocklist: []string{},
		IPRules:       IPRules{Allow: []string{}, Deny: []string{}},
		Exclusions:    []Exclusion{},
		Challenge:     DefaultChallenge(),
	}
}

// ValidationError is a rejected setting, for a 422 that names the field.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// UsesModSecurity reports whether the config needs the ModSecurity module.
func (c Config) UsesModSecurity() bool { return c.Mode == ModeDetect || c.Mode == ModeBlock }

// IsDefault reports whether the config asks nginx for nothing at all.
func (c Config) IsDefault() bool {
	return !c.UsesModSecurity() && !c.RateLimit.Enabled && !c.Challenge.Enabled &&
		len(c.PathBlocklist) == 0 && len(c.IPRules.Allow) == 0 && len(c.IPRules.Deny) == 0
}

// normalizeChallenge validates the challenge. The Turnstile keys are global
// panel settings, so a site's challenge has nothing to check about them.
func normalizeChallenge(ch Challenge) (Challenge, error) {
	def := DefaultChallenge()
	switch ch.Provider {
	case "":
		ch.Provider = def.Provider
	case guard.ProviderPoW, guard.ProviderTurnstile:
	default:
		return Challenge{}, invalid("challenge.provider", "must be pow or turnstile")
	}
	if ch.Difficulty == 0 {
		ch.Difficulty = def.Difficulty
	}
	if ch.Difficulty < MinDifficulty || ch.Difficulty > MaxDifficulty {
		return Challenge{}, invalid("challenge.difficulty", "must be between %d and %d", MinDifficulty, MaxDifficulty)
	}
	if ch.ClearanceMinutes == 0 {
		ch.ClearanceMinutes = def.ClearanceMinutes
	}
	if ch.ClearanceMinutes < MinClearanceMinutes || ch.ClearanceMinutes > MaxClearanceMinutes {
		return Challenge{}, invalid("challenge.clearance_minutes", "must be between %d and %d", MinClearanceMinutes, MaxClearanceMinutes)
	}
	paths := make([]string, 0, len(ch.ExemptPaths))
	seen := map[string]bool{}
	for _, p := range ch.ExemptPaths {
		p = strings.TrimSpace(p)
		if err := checkPath("challenge.exempt_paths", p); err != nil {
			return Challenge{}, err
		}
		if p == "/" {
			return Challenge{}, invalid("challenge.exempt_paths", "/ would exempt the whole site; turn the challenge off instead")
		}
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	if len(paths) > MaxBlocklist {
		return Challenge{}, invalid("challenge.exempt_paths", "at most %d paths", MaxBlocklist)
	}
	ch.ExemptPaths = paths
	ips, err := normalizeAddrs("challenge.exempt_ips", ch.ExemptIPs)
	if err != nil {
		return Challenge{}, err
	}
	ch.ExemptIPs = ips
	return ch, nil
}

// validKey is a Turnstile key: Cloudflare's are 0x followed by base64url-ish text.
func validKey(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '_', b == '-':
		default:
			return false
		}
	}
	return true
}

// ValidTurnstileSiteKey reports whether s is shaped like a Turnstile site key.
func ValidTurnstileSiteKey(s string) bool { return validKey(s, MaxTurnstileKey) }

// ValidTurnstileSecret reports whether s is shaped like a Turnstile secret key.
func ValidTurnstileSecret(s string) bool { return validKey(s, MaxTurnstileKey) }

// Normalize validates c and returns its canonical form: lists are never nil,
// entries are trimmed and de-duplicated, addresses are written as masked
// prefixes. Everything that reaches a template went through here, so a template
// never has to escape what it writes.
func (c Config) Normalize() (Config, error) {
	switch c.Mode {
	case ModeOff, ModeDetect, ModeBlock:
	case "":
		c.Mode = ModeOff
	default:
		return Config{}, invalid("mode", "must be off, detect or block")
	}
	if c.Paranoia == 0 {
		c.Paranoia = 1
	}
	if c.Paranoia < 1 || c.Paranoia > 4 {
		return Config{}, invalid("paranoia", "must be between 1 and 4")
	}

	rl := c.RateLimit
	if rl.RequestsPerSecond == 0 && rl.Burst == 0 && !rl.Enabled {
		rl = Default().RateLimit
	}
	if rl.RequestsPerSecond < 1 || rl.RequestsPerSecond > maxRPS {
		return Config{}, invalid("rate_limit.requests_per_second", "must be between 1 and %d", maxRPS)
	}
	if rl.Burst < 0 || rl.Burst > maxBurst {
		return Config{}, invalid("rate_limit.burst", "must be between 0 and %d", maxBurst)
	}
	c.RateLimit = rl

	paths, err := normalizePaths(c.PathBlocklist)
	if err != nil {
		return Config{}, err
	}
	c.PathBlocklist = paths

	allow, err := normalizeAddrs("ip_rules.allow", c.IPRules.Allow)
	if err != nil {
		return Config{}, err
	}
	deny, err := normalizeAddrs("ip_rules.deny", c.IPRules.Deny)
	if err != nil {
		return Config{}, err
	}
	in := make(map[string]bool, len(allow))
	for _, a := range allow {
		in[a] = true
	}
	for _, d := range deny {
		if in[d] {
			return Config{}, invalid("ip_rules", "%s is in both the allow and the deny list", d)
		}
	}
	c.IPRules = IPRules{Allow: allow, Deny: deny}

	ex, err := normalizeExclusions(c.Exclusions)
	if err != nil {
		return Config{}, err
	}
	c.Exclusions = ex

	ch, err := normalizeChallenge(c.Challenge)
	if err != nil {
		return Config{}, err
	}
	c.Challenge = ch
	return c, nil
}

// pathByte is what a blocklist or exclusion path may contain. The set has no
// character that matters to nginx, a regular expression or a shell, apart from
// the dot, which the renderers escape.
func pathByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("/_.~%-", b) >= 0
}

func checkPath(field, p string) error {
	switch {
	case p == "" || p[0] != '/':
		return invalid(field, "%q must start with /", p)
	case len(p) > MaxPathLen:
		return invalid(field, "%q is longer than %d characters", p, MaxPathLen)
	}
	for i := 0; i < len(p); i++ {
		if !pathByte(p[i]) {
			return invalid(field, "%q may contain only letters, digits and / _ . ~ %% -", p)
		}
	}
	return nil
}

func normalizePaths(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if err := checkPath("path_blocklist", p); err != nil {
			return nil, err
		}
		if p == "/" {
			return nil, invalid("path_blocklist", "/ would block the whole site")
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) > MaxBlocklist {
		return nil, invalid("path_blocklist", "at most %d paths", MaxBlocklist)
	}
	return out, nil
}

func normalizeAddrs(field string, in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		raw = strings.TrimSpace(raw)
		var p netip.Prefix
		if a, err := netip.ParseAddr(raw); err == nil {
			p = netip.PrefixFrom(a, a.BitLen())
		} else if p, err = netip.ParsePrefix(raw); err != nil {
			return nil, invalid(field, "%q is not an IP address or a CIDR network", raw)
		}
		if p.Addr().Is4In6() {
			return nil, invalid(field, "%q is an IPv4-mapped IPv6 address; write the IPv4 address", raw)
		}
		s := p.Masked().String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) > MaxIPEntries {
		return nil, invalid(field, "at most %d entries", MaxIPEntries)
	}
	sort.Strings(out)
	return out, nil
}

func normalizeExclusions(in []Exclusion) ([]Exclusion, error) {
	if len(in) > MaxExclusions {
		return nil, invalid("exclusions", "at most %d exclusions", MaxExclusions)
	}
	out := make([]Exclusion, 0, len(in))
	for i, e := range in {
		f := fmt.Sprintf("exclusions[%d]", i)
		if e.RuleID < 1 || e.RuleID > MaxRuleID {
			return nil, invalid(f+".rule_id", "must be between 1 and %d", MaxRuleID)
		}
		e.Note = strings.Join(strings.Fields(e.Note), " ")
		if len([]rune(e.Note)) > MaxNote {
			return nil, invalid(f+".note", "is longer than %d characters", MaxNote)
		}
		switch e.Kind {
		case KindRule:
			e.Path, e.Arg = "", ""
		case KindRulePath:
			e.Path, e.Arg = strings.TrimSpace(e.Path), ""
			if err := checkPath(f+".path", e.Path); err != nil {
				return nil, err
			}
		case KindRuleArg:
			e.Path, e.Arg = "", strings.TrimSpace(e.Arg)
			if !validArg(e.Arg) {
				return nil, invalid(f+".arg", "must be 1-64 letters, digits or _ . - [ ]")
			}
		default:
			return nil, invalid(f+".kind", "must be rule, rule_path or rule_arg")
		}
		out = append(out, e)
	}
	return out, nil
}

func validArg(a string) bool {
	if a == "" || len(a) > 64 {
		return false
	}
	for i := 0; i < len(a); i++ {
		b := a[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		case strings.IndexByte("_.-[]", b) >= 0:
		default:
			return false
		}
	}
	return true
}
