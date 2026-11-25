package notify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// D6: outbound destinations. https is allowed anywhere except link-local and
// unspecified addresses. http is allowed only to loopback and RFC 1918 / ULA
// addresses. The check runs when a channel is saved and again on every dial,
// because DNS can change between the two.

type schemeKey struct{}

// withScheme records the scheme of the request being dialled, so the dialer
// can apply the http rule to it.
func withScheme(ctx context.Context, scheme string) context.Context {
	return context.WithValue(ctx, schemeKey{}, scheme)
}

func schemeOf(ctx context.Context) string {
	if s, ok := ctx.Value(schemeKey{}).(string); ok {
		return s
	}
	return "https"
}

// addrAllowed reports whether a connection to ip is permitted for scheme.
func addrAllowed(ip net.IP, scheme string) bool {
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return false
	}
	if scheme == "http" {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	return true
}

// ValidateOutboundURL is the static half of D6, run on save.
func ValidateOutboundURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("must be an absolute URL")
	}
	if u.User != nil || u.Fragment != "" {
		return errors.New("must not carry user information or a fragment")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	switch u.Scheme {
	case "https":
		if ip != nil && !addrAllowed(ip, "https") {
			return fmt.Errorf("address %s is not a valid destination", host)
		}
		return nil
	case "http":
		if host == "localhost" {
			return nil
		}
		if ip != nil && addrAllowed(ip, "http") {
			return nil
		}
		return errors.New("http is allowed only to loopback or private addresses; use https")
	}
	return errors.New("must start with https://")
}

// guardDial is the dial-time half of D6. It resolves the host, drops every
// address the scheme may not reach, and dials an IP literal, so the address
// checked is the address connected to.
func guardDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	scheme := schemeOf(ctx)
	var (
		dialer  net.Dialer
		lastErr = errors.New("no permitted address for " + host)
	)
	for _, ip := range ips {
		if !addrAllowed(ip, scheme) {
			lastErr = fmt.Errorf("destination %s is not allowed", ip)
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// newOutboundClient is the HTTP client for webhooks, Telegram and Discord. It
// never uses a proxy from the environment (that would bypass the dial guard),
// never follows redirects, and has a hard timeout.
func newOutboundClient() *http.Client {
	tr := &http.Transport{
		DialContext:         guardDial,
		Proxy:               nil,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
