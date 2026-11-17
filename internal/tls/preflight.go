package tls

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// preflightPrefix names the probe files. A crash between write and delete leaves
// one behind; the prefix makes it recognisable and harmless (it holds random bytes).
const preflightPrefix = "pb-preflight-"

// probeTimeout bounds one probe, including the dial. A filtered port 80 would
// otherwise hang for the kernel's connect timeout.
const probeTimeout = 10 * time.Second

// PreflightError is returned when a name does not reach this server's challenge
// directory over port 80. The CA has not been contacted: failing here costs no
// Let's Encrypt rate limit.
type PreflightError struct {
	Name string
	// Reason is a sentence for the operator, not a Go error string.
	Reason string
	Err    error
}

func (e *PreflightError) Error() string {
	return fmt.Sprintf("tls: pre-flight for %s: %s", e.Name, e.Reason)
}

func (e *PreflightError) Unwrap() error { return e.Err }

// newProbeClient is the default pre-flight client. It dials the name the way a
// CA's validator would, ignores proxy variables (a proxy would answer for a
// server that is not reachable) and never follows a redirect: the challenge path
// must answer on port 80 itself.
func newProbeClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DisableKeepAlives = true
	return &http.Client{
		Transport: tr,
		Timeout:   probeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// preflight proves each name serves a file written into the ACME webroot.
func (i *Issuer) preflight(ctx context.Context, names []string) error {
	webroot := filepath.FromSlash(ACMEWebroot(i.root))
	dir := filepath.Join(webroot, ".well-known", "acme-challenge")
	if err := os.MkdirAll(dir, sharedDirMode); err != nil {
		return fmt.Errorf("tls: pre-flight: create challenge directory: %w", err)
	}
	for _, name := range names {
		if err := i.probe(ctx, dir, name); err != nil {
			return err
		}
	}
	return nil
}

func (i *Issuer) probe(ctx context.Context, dir, name string) error {
	file, err := randomHex(8)
	if err != nil {
		return fmt.Errorf("tls: pre-flight: %w", err)
	}
	file = preflightPrefix + file
	body, err := randomHex(16)
	if err != nil {
		return fmt.Errorf("tls: pre-flight: %w", err)
	}
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("tls: pre-flight: write probe file: %w", err)
	}
	defer func() { _ = os.Remove(path) }()
	// WriteFile is subject to the umask; nginx's worker must be able to read it.
	if err := os.Chmod(path, 0o644); err != nil && !errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("tls: pre-flight: chmod probe file: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+name+"/.well-known/acme-challenge/"+file, nil)
	if err != nil {
		return &PreflightError{Name: name, Reason: "the name is not a usable host name", Err: err}
	}
	resp, err := i.probeClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return classifyProbeError(name, err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return &PreflightError{Name: name, Reason: "the connection broke while reading the answer from port 80", Err: err}
	}
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return &PreflightError{Name: name, Err: ErrPreflightMismatch, Reason: fmt.Sprintf(
			"port 80 redirects the challenge path (HTTP %d) instead of serving it: something other than this server's ACME location answers",
			resp.StatusCode)}
	case resp.StatusCode != http.StatusOK:
		return &PreflightError{Name: name, Err: ErrPreflightMismatch, Reason: fmt.Sprintf(
			"port 80 answered HTTP %d for the challenge path: something other than this server's ACME location answers",
			resp.StatusCode)}
	case subtle.ConstantTimeCompare(got, []byte(body)) != 1:
		return &PreflightError{Name: name, Err: ErrPreflightMismatch,
			Reason: "port 80 answered with different content: the name points at a different server, or a proxy or CDN in front of it answers"}
	}
	return nil
}

// ErrPreflightMismatch marks a pre-flight failure where something answered on
// port 80 but it was not this server's challenge directory.
var ErrPreflightMismatch = errors.New("tls: pre-flight answer mismatch")

func classifyProbeError(name string, err error) error {
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return &PreflightError{Name: name, Err: err,
			Reason: "the name does not resolve: create an A or AAAA record that points at this server and wait for it to propagate"}
	case errors.Is(err, syscall.ECONNREFUSED):
		return &PreflightError{Name: name, Err: err,
			Reason: "connection refused on port 80: nothing listens there, or a firewall rejects it"}
	case errors.As(err, &netErr) && netErr.Timeout():
		return &PreflightError{Name: name, Err: err,
			Reason: "timed out connecting to port 80: a firewall is probably dropping the traffic, or the name points at an address that is not this server"}
	default:
		return &PreflightError{Name: name, Err: err,
			Reason: "could not reach port 80: " + err.Error()}
	}
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return hex.EncodeToString(b), nil
}
