package tls

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/challenge/dns01"
)

// DNSProvider publishes and removes the TXT record a DNS-01 challenge needs.
// domain is the name being validated (without "*."), fqdn is the record's full
// name with a trailing dot, value is the TXT content.
//
// The context carries the issuance's cancellation; an implementation must pass
// it to every network call it makes.
type DNSProvider interface {
	Name() string
	Present(ctx context.Context, domain, fqdn, value string) error
	CleanUp(ctx context.Context, domain, fqdn, value string) error
}

// KindCloudflare is the provider kind of Cloudflare zones.
const KindCloudflare = "cloudflare"

// ProviderFactory builds the DNS provider of a stored kind from its opened
// credentials. Credentials live in the factory's argument and the returned
// value only; they are never put in the process environment, which is shared by
// every goroutine in the daemon.
func ProviderFactory(kind string, credentials []byte) (DNSProvider, error) {
	switch kind {
	case KindCloudflare:
		return NewCloudflare(credentials, CloudflareOptions{})
	default:
		return nil, fmt.Errorf("%w: unknown DNS provider kind %q", ErrInvalid, kind)
	}
}

// DNSProviderKinds lists the kinds ProviderFactory accepts, for validation in the API.
func DNSProviderKinds() []string { return []string{KindCloudflare} }

// legoDNS adapts a DNSProvider to lego's challenge.Provider for one issuance.
// lego hands over the key authorisation; the TXT value and record name are
// derived here so providers see only the DNS facts.
type legoDNS struct {
	ctx context.Context
	p   DNSProvider
}

func (a legoDNS) Present(domain, _, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	if err := a.p.Present(a.ctx, domain, info.EffectiveFQDN, info.Value); err != nil {
		return fmt.Errorf("tls: dns-01 present %s with %s: %w", domain, a.p.Name(), err)
	}
	return nil
}

func (a legoDNS) CleanUp(domain, _, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	// A cancelled issuance still owes the zone its cleanup, so cleanup gets a
	// context that outlives the cancellation but is bounded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(a.ctx), 30*time.Second)
	defer cancel()
	if err := a.p.CleanUp(ctx, domain, info.EffectiveFQDN, info.Value); err != nil {
		return fmt.Errorf("tls: dns-01 clean up %s with %s: %w", domain, a.p.Name(), err)
	}
	return nil
}

// Timeout is how long lego waits for the record to propagate and how often it
// looks.
func (legoDNS) Timeout() (timeout, interval time.Duration) {
	return 2 * time.Minute, 4 * time.Second
}

// MemoryDNS is a DNSProvider that keeps records in memory. It is for tests of
// the issuer and of the layers above it.
type MemoryDNS struct {
	mu      sync.Mutex
	records map[string]map[string]bool
	log     []string
	// PresentErr, when set, is returned by Present after it is logged.
	PresentErr error
}

// NewMemoryDNS returns an empty provider.
func NewMemoryDNS() *MemoryDNS { return &MemoryDNS{records: map[string]map[string]bool{}} }

// Name implements DNSProvider.
func (*MemoryDNS) Name() string { return "memory" }

// Present implements DNSProvider.
func (m *MemoryDNS) Present(ctx context.Context, _, fqdn, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, "present "+fqdn)
	if m.PresentErr != nil {
		return m.PresentErr
	}
	if m.records[fqdn] == nil {
		m.records[fqdn] = map[string]bool{}
	}
	m.records[fqdn][value] = true
	return nil
}

// CleanUp implements DNSProvider.
func (m *MemoryDNS) CleanUp(_ context.Context, _, fqdn, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, "cleanup "+fqdn)
	delete(m.records[fqdn], value)
	if len(m.records[fqdn]) == 0 {
		delete(m.records, fqdn)
	}
	return nil
}

// Records returns the TXT values currently published at fqdn.
func (m *MemoryDNS) Records(fqdn string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for v := range m.records[fqdn] {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// Calls returns the ordered call log, "present <fqdn>" and "cleanup <fqdn>".
func (m *MemoryDNS) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.log...)
}

// redact removes each secret from text. It is the last line of defence: an
// error built from a response body or a URL must not carry a credential into a
// log or a job row, even if the remote echoes it.
func redact(text string, secrets ...string) string {
	for _, s := range secrets {
		if s != "" {
			text = strings.ReplaceAll(text, s, "[redacted]")
		}
	}
	return text
}
