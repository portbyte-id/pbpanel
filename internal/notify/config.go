package notify

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// headerName is an RFC 7230 token: the characters a header name may use.
var headerName = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+$`)

// reservedHeaders may not be set by a channel's custom headers: the panel's
// own headers and the ones the HTTP client manages.
func reservedHeader(name string) bool {
	l := strings.ToLower(name)
	return strings.HasPrefix(l, "x-pbpanel-") || l == "content-type" || l == "content-length" || l == "host"
}

// ValidateChannel checks a channel's per-type configuration before it is
// stored (§4.5, step 5). secret returns the value a secret field would have
// after the write, "" when it has none, so the check covers the merged result.
func ValidateChannel(w ChannelWrite, secret func(field string) string) error {
	switch w.Type {
	case TypeWebhook:
		raw, _ := w.Config["url"].(string)
		if raw == "" {
			return fmt.Errorf("%w: webhook url is required", ErrInvalidEvent)
		}
		if err := ValidateOutboundURL(raw); err != nil {
			return fmt.Errorf("%w: webhook url: %v", ErrInvalidEvent, err)
		}
		if v, ok := w.Config["sign"]; ok {
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%w: sign must be true or false", ErrInvalidEvent)
			}
		}
		if hv, ok := w.Config["headers"]; ok {
			m, ok := hv.(map[string]any)
			if !ok {
				return fmt.Errorf("%w: headers must be an object of strings", ErrInvalidEvent)
			}
			for name, val := range m {
				if !headerName.MatchString(name) || reservedHeader(name) {
					return fmt.Errorf("%w: header %q is not allowed", ErrInvalidEvent, name)
				}
				if _, ok := val.(string); !ok {
					return fmt.Errorf("%w: header %q must be a string", ErrInvalidEvent, name)
				}
			}
		}
	case TypeTelegram:
		if id, _ := w.Config["chat_id"].(string); id == "" {
			return fmt.Errorf("%w: telegram chat_id is required", ErrInvalidEvent)
		}
		if secret("bot_token") == "" {
			return fmt.Errorf("%w: telegram bot_token is required", ErrInvalidEvent)
		}
	case TypeDiscord:
		raw := secret("webhook_url")
		if raw == "" {
			return fmt.Errorf("%w: discord webhook_url is required", ErrInvalidEvent)
		}
		if err := ValidateOutboundURL(raw); err != nil {
			return fmt.Errorf("%w: discord webhook_url: %v", ErrInvalidEvent, err)
		}
	case TypeSMTP:
		if _, err := readSMTP(Channel{Config: w.Config}); err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidEvent, errors.Unwrap(err))
		}
		from, _ := w.Config["from"].(string)
		for _, addr := range append([]string{from}, anyStrings(w.Config["to"])...) {
			if !validEmail(addr) {
				return fmt.Errorf("%w: %q is not an email address", ErrInvalidEvent, addr)
			}
		}
	}
	return nil
}

func anyStrings(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
