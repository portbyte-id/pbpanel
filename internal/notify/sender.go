package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// permanentError marks a failure that retrying cannot fix (a 4xx other than
// 408 and 429, a bad credential, a refused address).
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func permanent(format string, args ...any) error {
	return &permanentError{err: fmt.Errorf(format, args...)}
}

// retryAfterError is a rate limit the receiver named a wait for.
type retryAfterError struct {
	err   error
	after time.Duration
}

func (e *retryAfterError) Error() string { return e.err.Error() }
func (e *retryAfterError) Unwrap() error { return e.err }

var urlPattern = regexp.MustCompile(`https?://[^\s"']+`)

// redact removes every secret value and every URL from text before it is
// stored or logged. Go's client errors print the request URL, which for
// Telegram and Discord carries the token (§7).
func redact(text string, secrets ...string) string {
	text = urlPattern.ReplaceAllString(text, "[url]")
	for _, s := range secrets {
		if s != "" {
			text = strings.ReplaceAll(text, s, "[redacted]")
		}
	}
	return cutBytes(text, 300)
}

// payload is the message every sender renders from an event (§4.3).
type payload struct {
	ID       int64
	Kind     string
	Severity Severity
	Key      string
	Subject  string
	Body     string
	Source   string
	At       time.Time
	Resolved bool
	Link     string // absolute, or ""
	Fields   map[string]string
	Host     string
}

// webhookBody is the JSON body of a generic webhook (§4.3).
type webhookBody struct {
	Version  int               `json:"version"`
	ID       int64             `json:"id"`
	Kind     string            `json:"kind"`
	Severity Severity          `json:"severity"`
	Key      string            `json:"key"`
	Subject  string            `json:"subject"`
	Body     string            `json:"body"`
	Source   string            `json:"source"`
	At       string            `json:"at"`
	Resolved bool              `json:"resolved"`
	Link     string            `json:"link,omitempty"`
	Fields   map[string]string `json:"fields"`
	Host     string            `json:"host"`
}

// postJSON posts body and classifies the answer. A 2xx is success; a 429
// honours Retry-After or a retry_after field in the body (Telegram nests it
// under parameters, Discord does not); other 4xx are permanent; 408, 5xx and
// transport errors are retried. secrets are redacted from every error text.
func postJSON(ctx context.Context, client *http.Client, url string, body []byte, headers map[string]string, secrets ...string) error {
	req, err := http.NewRequestWithContext(withScheme(ctx, schemeFromURL(url)), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return permanent("build request: %s", redact(err.Error(), secrets...))
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send: %s", redact(err.Error(), secrets...))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	msg := fmt.Errorf("http %d: %s", resp.StatusCode, redact(strings.TrimSpace(string(raw)), secrets...))
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return &retryAfterError{err: msg, after: retryAfterOf(resp.Header.Get("Retry-After"), raw)}
	case resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode >= 500:
		return msg
	default:
		return &permanentError{err: msg}
	}
}

func schemeFromURL(u string) string {
	if strings.HasPrefix(u, "http://") {
		return "http"
	}
	return "https"
}

// retryAfterOf reads the wait a rate limit names: the Retry-After header in
// seconds, else retry_after in the body (top level, or under parameters).
func retryAfterOf(header string, body []byte) time.Duration {
	if n, err := strconv.ParseFloat(strings.TrimSpace(header), 64); err == nil && n > 0 {
		return time.Duration(n * float64(time.Second))
	}
	var probe struct {
		RetryAfter float64 `json:"retry_after"`
		Parameters struct {
			RetryAfter float64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if json.Unmarshal(body, &probe) == nil {
		n := probe.RetryAfter
		if n <= 0 {
			n = probe.Parameters.RetryAfter
		}
		if n > 0 {
			return time.Duration(n * float64(time.Second))
		}
	}
	return 0
}

// isPermanent reports whether err must not be retried.
func isPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

// retryWait returns the rate limit's wait, if err carries one.
func retryWait(err error) (time.Duration, bool) {
	var r *retryAfterError
	if errors.As(err, &r) && r.after > 0 {
		return r.after, true
	}
	return 0, false
}

// clampText cuts s to limit bytes on a rune boundary and keeps it valid UTF-8.
func clampText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

// discordColour is the embed colour per severity.
func discordColour(sev Severity) int {
	switch sev {
	case Critical:
		return 0xE74C3C
	case Warning:
		return 0xF1C40F
	}
	return 0x3498DB
}

// subjectLine is the one-line title used by email and chat senders.
func subjectLine(p payload) string {
	label := "RESOLVED"
	if !p.Resolved {
		label = strings.ToUpper(string(p.Severity))
	}
	return fmt.Sprintf("[%s] %s: %s", p.Host, label, p.Subject)
}

// webhookPayload builds the generic webhook body for p.
func webhookPayload(p payload) webhookBody {
	fields := p.Fields
	if fields == nil {
		fields = map[string]string{}
	}
	return webhookBody{
		Version:  1,
		ID:       p.ID,
		Kind:     p.Kind,
		Severity: p.Severity,
		Key:      p.Key,
		Subject:  p.Subject,
		Body:     p.Body,
		Source:   p.Source,
		At:       p.At.UTC().Format(time.RFC3339),
		Resolved: p.Resolved,
		Link:     p.Link,
		Fields:   fields,
		Host:     p.Host,
	}
}
