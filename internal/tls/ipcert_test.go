package tls

import (
	"net"
	"testing"
	"time"
)

func TestIsPublicIPv4(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":         true,
		"1.1.1.1":         true,
		"45.76.10.4":      true,
		"10.0.0.5":        false, // RFC 1918
		"172.16.4.1":      false,
		"192.168.1.20":    false,
		"100.64.0.9":      false, // carrier-grade NAT
		"127.0.0.1":       false,
		"169.254.1.1":     false, // link-local
		"203.0.113.7":     false, // documentation
		"198.18.0.1":      false, // benchmarking
		"240.0.0.1":       false, // reserved
		"0.0.0.0":         false,
		"255.255.255.255": false,
		"2606:4700::1111": false, // IPv6 is not handled
	}
	for in, want := range cases {
		if got := isPublicIPv4(net.ParseIP(in)); got != want {
			t.Errorf("isPublicIPv4(%s) = %v, want %v", in, got, want)
		}
	}
	if isPublicIPv4(nil) {
		t.Error("isPublicIPv4(nil) = true")
	}
}

func TestPublicIPv4FromPicksTheFirstPublicAddress(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("10.0.0.4"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("45.76.10.4"), Mask: net.CIDRMask(24, 32)},
		&net.IPAddr{IP: net.ParseIP("8.8.4.4")},
	}
	ip, ok := publicIPv4From(addrs)
	if !ok || ip != "45.76.10.4" {
		t.Fatalf("got %q, %v", ip, ok)
	}
}

func TestPublicIPv4FromBehindNATFindsNothing(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("192.168.0.12"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("100.100.1.1"), Mask: net.CIDRMask(10, 32)},
	}
	if ip, ok := publicIPv4From(addrs); ok {
		t.Fatalf("found %q behind NAT", ip)
	}
}

func TestProfileFor(t *testing.T) {
	if got := profileFor([]string{"example.com"}); got != "" {
		t.Errorf("hostname order asked for profile %q", got)
	}
	if got := profileFor([]string{"example.com", "45.76.10.4"}); got != ProfileShortLived {
		t.Errorf("IP order asked for profile %q", got)
	}
}

func TestCheckNamesAcceptsAnIPv4Name(t *testing.T) {
	names, err := checkNames("45.76.10.4", []string{"45.76.10.4"}, false)
	if err != nil || len(names) != 1 || names[0] != "45.76.10.4" {
		t.Fatalf("got %v, %v", names, err)
	}
}

func TestIssueDNS01RefusesAnIP(t *testing.T) {
	i := &Issuer{}
	_, err := i.IssueDNS01(t.Context(), "45.76.10.4", []string{"45.76.10.4"}, "cf", "")
	if err == nil {
		t.Fatal("DNS-01 accepted an IP name")
	}
}

func TestRenewerUsesAThirdOfAShortLivedLifetime(t *testing.T) {
	r := &Renewer{daysBefore: func() int { return 30 }}
	nb := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)

	short := Cert{NotBefore: nb, NotAfter: nb.Add(6 * 24 * time.Hour)}
	if got, want := r.windowFor(short), 2*24*time.Hour; got != want {
		t.Errorf("short-lived window = %v, want %v", got, want)
	}
	long := Cert{NotBefore: nb, NotAfter: nb.Add(90 * 24 * time.Hour)}
	if got, want := r.windowFor(long), 30*24*time.Hour; got != want {
		t.Errorf("90-day window = %v, want %v", got, want)
	}
}
