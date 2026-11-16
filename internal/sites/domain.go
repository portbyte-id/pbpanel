package sites

import (
	"fmt"
	"net/netip"
	"strings"

	"golang.org/x/net/idna"
)

// ValidationError is a rejected input, for a 422 that names the field.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// idnaProfile maps case and Unicode forms the way a resolver does, and holds the
// result to LDH hostname rules, so what is stored is what DNS will be asked for.
var idnaProfile = idna.New(
	idna.MapForLookup(),
	idna.BidiRule(),
	idna.StrictDomainName(true),
	idna.VerifyDNSLength(true),
)

// NormalizeDomain returns the punycode, lowercase form of a domain, or an error
// saying what is wrong. Wildcards are refused: they need DNS-01 certificates,
// which arrive with TLS in M6.
func NormalizeDomain(field, raw string) (string, error) {
	s := strings.TrimSpace(raw)
	switch {
	case s == "":
		return "", invalid(field, "is required")
	case strings.Contains(s, "*"):
		return "", invalid(field, "wildcards are not supported yet")
	case strings.ContainsAny(s, "/:@ \t\r\n;{}$\"'\\#"):
		return "", invalid(field, "%q is not a domain name", raw)
	}
	s = strings.TrimSuffix(s, ".")
	ascii, err := idnaProfile.ToASCII(s)
	if err != nil {
		return "", invalid(field, "%q is not a valid domain name: %v", raw, err)
	}
	ascii = strings.ToLower(ascii)
	// An address would be accepted by the label rules (digits are letters here)
	// and then match nothing as a Host header.
	if _, err := netip.ParseAddr(ascii); err == nil {
		return "", invalid(field, "%q is an IP address, not a domain name", raw)
	}
	if strings.HasPrefix(ascii, ".") || strings.Contains(ascii, "..") {
		return "", invalid(field, "%q has an empty label", raw)
	}
	for label := range strings.SplitSeq(ascii, ".") {
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", invalid(field, "%q has a label that starts or ends with a hyphen", raw)
		}
	}
	return ascii, nil
}

// normalizeAliases normalizes each alias and rejects a repeat or the primary
// domain itself. The order given is kept: it is the order in server_name.
func normalizeAliases(primary string, raw []string) ([]string, error) {
	seen := map[string]bool{primary: true}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if strings.TrimSpace(r) == "" {
			continue
		}
		a, err := NormalizeDomain("aliases", r)
		if err != nil {
			return nil, err
		}
		if seen[a] {
			return nil, invalid("aliases", "%s is listed twice or repeats the primary domain", a)
		}
		seen[a] = true
		out = append(out, a)
	}
	return out, nil
}
