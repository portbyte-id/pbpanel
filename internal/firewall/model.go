package firewall

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// Protocol selects which transport protocols a rule matches.
type Protocol string

// Supported protocols. ProtoBoth expands to one tcp and one udp line when rendered.
const (
	ProtoTCP  Protocol = "tcp"
	ProtoUDP  Protocol = "udp"
	ProtoBoth Protocol = "both"
)

// MaxCommentLen bounds a rule comment. iptables allows 255 bytes; the panel keeps
// them short enough to read in `iptables -S` output.
const MaxCommentLen = 64

// Rule opens one port or port range on the host.
type Rule struct {
	ID        int64    `json:"id"`
	PortStart int      `json:"portStart"`
	PortEnd   int      `json:"portEnd"` // equals PortStart for a single port
	Protocol  Protocol `json:"protocol"`
	// Source restricts the rule to one CIDR or address. Empty means any source.
	Source  string `json:"source,omitempty"`
	Comment string `json:"comment,omitempty"`
	Enabled bool   `json:"enabled"`
}

// BlockEntry drops all traffic from one address or CIDR.
type BlockEntry struct {
	ID      int64  `json:"id"`
	Addr    string `json:"addr"`
	Comment string `json:"comment,omitempty"`
}

// Validation errors. Callers match with errors.Is.
var (
	ErrPort     = errors.New("firewall: port must be between 1 and 65535")
	ErrPortOrd  = errors.New("firewall: port range start is above its end")
	ErrProto    = errors.New("firewall: protocol must be tcp, udp or both")
	ErrAddr     = errors.New("firewall: invalid address or CIDR")
	ErrComment  = errors.New("firewall: invalid comment")
	ErrEmptyNet = errors.New("firewall: address is required")
)

// Validate checks a rule and returns the first problem found.
func (r Rule) Validate() error {
	if r.PortStart < 1 || r.PortStart > 65535 || r.PortEnd < 1 || r.PortEnd > 65535 {
		return ErrPort
	}
	if r.PortStart > r.PortEnd {
		return ErrPortOrd
	}
	switch r.Protocol {
	case ProtoTCP, ProtoUDP, ProtoBoth:
	default:
		return ErrProto
	}
	if r.Source != "" {
		if _, err := ParsePrefix(r.Source); err != nil {
			return err
		}
	}
	return validateComment(r.Comment)
}

// Validate checks a blocklist entry.
func (b BlockEntry) Validate() error {
	if b.Addr == "" {
		return ErrEmptyNet
	}
	if _, err := ParsePrefix(b.Addr); err != nil {
		return err
	}
	return validateComment(b.Comment)
}

// ParsePrefix accepts a CIDR or a bare address and returns the masked prefix. A bare
// address becomes a /32 or /128. IPv4-mapped IPv6 addresses are rejected: they would
// land in the wrong table.
func ParsePrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil || p.Addr().Is4In6() || p.Addr().Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("%w: %q", ErrAddr, s)
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Is4In6() || a.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("%w: %q", ErrAddr, s)
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// validateComment keeps comments to printable ASCII without characters that would
// need escaping inside an iptables-restore line.
func validateComment(c string) error {
	if len(c) > MaxCommentLen {
		return fmt.Errorf("%w: longer than %d characters", ErrComment, MaxCommentLen)
	}
	for _, r := range c {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return fmt.Errorf("%w: only printable ASCII without quotes or backslashes", ErrComment)
		}
	}
	return nil
}
