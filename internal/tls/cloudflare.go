package tls

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultCloudflareAPI = "https://api.cloudflare.com/client/v4"
	// txtTTL is short on purpose: a stale challenge record does not need to
	// linger in resolver caches.
	txtTTL = 120
	// maxAPIBody bounds what is read from one Cloudflare response.
	maxAPIBody = 1 << 20
)

// CloudflareOptions tunes a Cloudflare provider. The zero value talks to the
// real API.
type CloudflareOptions struct {
	// BaseURL replaces the API root; tests point it at an httptest server.
	BaseURL string
	// HTTPClient replaces the default client (30 s timeout).
	HTTPClient *http.Client
}

type cloudflareCredentials struct {
	APIToken string `json:"api_token"`
}

// Cloudflare is a DNS-01 provider for Cloudflare zones. It is a direct client of
// the v4 API rather than lego's provider: lego's takes its token from process
// environment variables, which are global to the daemon, and a panel with two
// Cloudflare accounts would race on them.
type Cloudflare struct {
	token string
	base  string
	hc    *http.Client
}

// NewCloudflare parses credentials of the form {"api_token":"..."}. Only a
// scoped API token (Zone:DNS:Edit on the zones involved) is accepted; the
// global API key is a different, far broader credential and has no field here.
func NewCloudflare(credentials []byte, o CloudflareOptions) (*Cloudflare, error) {
	var c cloudflareCredentials
	dec := json.NewDecoder(bytes.NewReader(credentials))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		// The decoder's message can quote input; say only what is wrong.
		return nil, fmt.Errorf(`%w: cloudflare credentials must be JSON {"api_token":"..."}`, ErrInvalid)
	}
	if strings.TrimSpace(c.APIToken) == "" {
		return nil, fmt.Errorf("%w: cloudflare api_token is empty", ErrInvalid)
	}
	cf := &Cloudflare{token: strings.TrimSpace(c.APIToken), base: strings.TrimRight(o.BaseURL, "/"), hc: o.HTTPClient}
	if cf.base == "" {
		cf.base = defaultCloudflareAPI
	}
	if cf.hc == nil {
		cf.hc = &http.Client{Timeout: 30 * time.Second}
	}
	return cf, nil
}

// Name implements DNSProvider.
func (*Cloudflare) Name() string { return KindCloudflare }

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

type cfRecord struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// do sends one API call and returns the result field. Every error is passed
// through redact so the token cannot leave in a message.
func (c *Cloudflare) do(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("cloudflare: encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, errors.New(redact(fmt.Sprintf("cloudflare: build request: %v", err), c.token))
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New(redact(fmt.Sprintf("cloudflare: %s %s: %v", method, path, err), c.token))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBody))
	if err != nil {
		return nil, errors.New(redact(fmt.Sprintf("cloudflare: read response: %v", err), c.token))
	}
	var env cfEnvelope
	jsonErr := json.Unmarshal(raw, &env)
	if resp.StatusCode/100 != 2 || jsonErr != nil || !env.Success {
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if len(env.Errors) > 0 {
			parts := make([]string, 0, len(env.Errors))
			for _, e := range env.Errors {
				parts = append(parts, fmt.Sprintf("%d %s", e.Code, e.Message))
			}
			msg += ": " + strings.Join(parts, "; ")
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			msg += " (the API token is invalid or lacks Zone:DNS:Edit on this zone)"
		}
		return nil, errors.New(redact(fmt.Sprintf("cloudflare: %s %s: %s", method, path, msg), c.token))
	}
	return env.Result, nil
}

// zoneID finds the zone that contains fqdn by trying the name and then each
// parent: the longest match wins, which is right for a delegated sub-zone.
func (c *Cloudflare) zoneID(ctx context.Context, fqdn string) (string, error) {
	name := strings.TrimSuffix(strings.ToLower(fqdn), ".")
	labels := strings.Split(name, ".")
	// Stop before the bare TLD: no one hosts a zone for "com" in Cloudflare.
	for i := 0; i < len(labels)-1; i++ {
		candidate := strings.Join(labels[i:], ".")
		raw, err := c.do(ctx, http.MethodGet, "/zones", url.Values{"name": {candidate}, "per_page": {"1"}}, nil)
		if err != nil {
			return "", err
		}
		var zones []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &zones); err != nil {
			return "", fmt.Errorf("cloudflare: decode zones: %w", err)
		}
		if len(zones) > 0 {
			return zones[0].ID, nil
		}
	}
	return "", fmt.Errorf("cloudflare: no zone for %s is visible to this API token", strings.TrimSuffix(fqdn, "."))
}

// Present implements DNSProvider.
func (c *Cloudflare) Present(ctx context.Context, _, fqdn, value string) error {
	zone, err := c.zoneID(ctx, fqdn)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(zone)+"/dns_records", nil, map[string]any{
		"type":    "TXT",
		"name":    strings.TrimSuffix(fqdn, "."),
		"content": value,
		"ttl":     txtTTL,
	})
	return err
}

// CleanUp implements DNSProvider. It deletes only records whose content equals
// value: a wildcard and its base name share one record name with two values, and
// removing the other order's record would fail it.
func (c *Cloudflare) CleanUp(ctx context.Context, _, fqdn, value string) error {
	zone, err := c.zoneID(ctx, fqdn)
	if err != nil {
		return err
	}
	name := strings.TrimSuffix(fqdn, ".")
	raw, err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(zone)+"/dns_records",
		url.Values{"type": {"TXT"}, "name": {name}, "per_page": {"100"}}, nil)
	if err != nil {
		return err
	}
	var recs []cfRecord
	if err := json.Unmarshal(raw, &recs); err != nil {
		return fmt.Errorf("cloudflare: decode records: %w", err)
	}
	var errs []error
	for _, r := range recs {
		// The API returns TXT content quoted; compare without the quotes.
		if strings.Trim(r.Content, `"`) != value {
			continue
		}
		if _, err := c.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(zone)+"/dns_records/"+url.PathEscape(r.ID), nil, nil); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
