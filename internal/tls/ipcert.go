package tls

import (
	"net"
	"slices"
	"time"
)

// ProfileShortLived is the ACME profile Let's Encrypt requires for a certificate
// that names an IP address. Such certificates live about six days.
const ProfileShortLived = "shortlived"

// shortLivedMax is the longest lifetime the renewer treats as short-lived. A
// certificate this brief cannot use a renewal window counted in weeks.
const shortLivedMax = 14 * 24 * time.Hour

// IsIPName reports whether name is an IP address literal.
func IsIPName(name string) bool { return net.ParseIP(name) != nil }

// anyIP reports whether any of names is an IP address literal.
func anyIP(names []string) bool {
	return slices.ContainsFunc(names, IsIPName)
}

// profileFor is the ACME profile an order for names must request: the
// short-lived one when any name is an IP address, otherwise the CA's default.
func profileFor(names []string) string {
	if anyIP(names) {
		return ProfileShortLived
	}
	return ""
}

// PublicIPv4 returns a globally routable IPv4 address assigned to one of this
// host's own interfaces. It looks only at the local interfaces and sends no
// packet: a VPS behind NAT (a cloud 1:1 mapping included) has only private
// addresses on its interfaces and reports false, which is the signal not to try
// an IP certificate. The result says the address is public, not that port 80 is
// reachable from outside; the order's own validation settles that.
func PublicIPv4() (string, bool) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", false
	}
	return publicIPv4From(addrs)
}

func publicIPv4From(addrs []net.Addr) (string, bool) {
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if isPublicIPv4(ip) {
			return ip.String(), true
		}
	}
	return "", false
}

// notPublic lists the IPv4 ranges net.IP's own predicates do not cover but that
// are not reachable from the internet: carrier-grade NAT, benchmarking,
// documentation, IETF protocol assignments and the reserved class E.
var notPublic = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

func isPublicIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil || !ip4.IsGlobalUnicast() || ip4.IsPrivate() {
		return false
	}
	for _, n := range notPublic {
		if n.Contains(ip4) {
			return false
		}
	}
	return true
}

// port80Free reports whether this process could bind port 80 right now. It is a
// variable so tests can pin the answer.
var port80Free = func() bool {
	l, err := net.Listen("tcp", ":80")
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// IsPublicIP reports whether s is an IP address literal an IP certificate can
// name: a globally routable IPv4 or IPv6 address. Private, loopback, link-local
// and documentation ranges are refused, since the CA could never reach them.
func IsPublicIP(s string) bool {
	ip := net.ParseIP(s)
	switch {
	case ip == nil:
		return false
	case ip.To4() != nil:
		return isPublicIPv4(ip)
	default:
		return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast()
	}
}

// CanonicalIP is the one spelling of an IP literal used as a certificate name.
func CanonicalIP(s string) string {
	if ip := net.ParseIP(s); ip != nil {
		return ip.String()
	}
	return s
}
