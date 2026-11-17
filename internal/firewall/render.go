package firewall

import (
	"fmt"
	"sort"
	"strings"
)

// Chains the panel owns. The renderer emits lines for these and for the single jump
// from INPUT, and for nothing else.
const (
	ChainInput = "PANEL-INPUT"
	ChainBlock = "PANEL-BLOCK"
)

// RenderInput is everything the renderer needs. It holds no state of its own.
type RenderInput struct {
	Rules     []Rule
	Blocklist []BlockEntry
	SSHPorts  []int
	PanelPort int
	// HookV4 and HookV6 add the jump from INPUT to PANEL-INPUT. The applier sets each only
	// when `iptables -C` says that family's jump is missing, so repeated applies never stack it.
	HookV4, HookV6 bool
}

// Rendered holds the two iptables-restore inputs, applied with --noflush.
type Rendered struct {
	V4 string
	V6 string
}

type family int

const (
	fam4 family = iota
	fam6
)

// Render builds the IPv4 and IPv6 restore inputs. It validates nothing; callers
// validate rules on write, and Render skips disabled rules and entries of the other
// family. A rule with no source goes to both tables.
func Render(in RenderInput) Rendered {
	return Rendered{V4: renderFamily(in, fam4), V6: renderFamily(in, fam6)}
}

func renderFamily(in RenderInput, f family) string {
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	line("*filter")
	line(":%s - [0:0]", ChainInput)
	line(":%s - [0:0]", ChainBlock)
	line("-F %s", ChainInput)
	line("-F %s", ChainBlock)

	// Blocklist chain.
	for _, e := range in.Blocklist {
		p, err := ParsePrefix(e.Addr)
		if err != nil || (p.Addr().Is4()) != (f == fam4) {
			continue
		}
		line("-A %s -s %s%s -j DROP", ChainBlock, p, commentArg(e.Comment))
	}

	// Built-in rules, generated and never stored.
	line("-A %s -i lo -j ACCEPT", ChainInput)
	line("-A %s -j %s", ChainInput, ChainBlock)
	line("-A %s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT", ChainInput)
	for _, t := range icmpTypes(f) {
		if f == fam4 {
			line("-A %s -p icmp --icmp-type %d -j ACCEPT", ChainInput, t)
		} else {
			line("-A %s -p ipv6-icmp --icmpv6-type %d -j ACCEPT", ChainInput, t)
		}
	}
	for _, p := range builtinPorts(in) {
		line("-A %s -p tcp --dport %d%s -j ACCEPT", ChainInput, p, commentArg("panel:builtin"))
	}

	// User rules, in the order given.
	for _, r := range in.Rules {
		if !r.Enabled {
			continue
		}
		src := ""
		if r.Source != "" {
			p, err := ParsePrefix(r.Source)
			if err != nil || (p.Addr().Is4()) != (f == fam4) {
				continue
			}
			src = " -s " + p.String()
		}
		dport := fmt.Sprintf("%d", r.PortStart)
		if r.PortEnd != r.PortStart {
			dport = fmt.Sprintf("%d:%d", r.PortStart, r.PortEnd)
		}
		for _, proto := range protosOf(r.Protocol) {
			line("-A %s -p %s%s --dport %s%s -j ACCEPT", ChainInput, proto, src, dport, commentArg(r.Comment))
		}
	}

	line("-A %s -j DROP", ChainInput)
	if (f == fam4 && in.HookV4) || (f == fam6 && in.HookV6) {
		line("-I INPUT 1 -j %s", ChainInput)
	}
	line("COMMIT")
	return b.String()
}

// icmpTypes lists the ICMP types that keep the host reachable and path MTU discovery
// working. IPv6 needs neighbour discovery or the host drops off its own link.
func icmpTypes(f family) []int {
	if f == fam4 {
		return []int{3, 8, 11, 12} // unreachable, echo request, time exceeded, parameter problem
	}
	return []int{1, 2, 3, 4, 128, 133, 134, 135, 136}
}

func builtinPorts(in RenderInput) []int {
	seen := map[int]bool{}
	var out []int
	for _, p := range append(append([]int{}, in.SSHPorts...), in.PanelPort) {
		if p >= 1 && p <= 65535 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Ints(out)
	return out
}

func protosOf(p Protocol) []string {
	if p == ProtoBoth {
		return []string{"tcp", "udp"}
	}
	return []string{string(p)}
}

func commentArg(c string) string {
	if c == "" {
		return ""
	}
	return fmt.Sprintf(" -m comment --comment \"%s\"", c)
}
