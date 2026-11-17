package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Redacted replaces the value of a sensitive detail key.
const Redacted = "[redacted]"

// secretKeyFragments are matched case-insensitively as substrings of the detail
// key, so new_password, csrf_token and X-Api-Key are covered without a list that
// has to grow every time a caller names a field. Over-redacting is the safe
// direction: a leaked secret cannot be undone, a hidden field can be renamed.
var secretKeyFragments = []string{
	"password",
	"secret",
	"token",
	"csrf",
	"authorization",
	"cookie",
	"key",
	"credential",
	"private",
}

func isSecretKey(key string) bool {
	k := strings.ToLower(key)
	for _, fragment := range secretKeyFragments {
		if strings.Contains(k, fragment) {
			return true
		}
	}
	return false
}

// redactedDetail returns detail as JSON with every sensitive value replaced.
// Going through JSON handles maps, structs, nested objects and arrays in one
// walk, and leaves the caller's value untouched.
func redactedDetail(detail any) ([]byte, error) {
	if detail == nil {
		return []byte("{}"), nil
	}

	raw, err := json.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("audit: marshal detail: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("audit: decode detail: %w", err)
	}

	out, err := json.Marshal(redactValue(decoded))
	if err != nil {
		return nil, fmt.Errorf("audit: marshal redacted detail: %w", err)
	}
	return out, nil
}

func redactValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		for key, value := range typed {
			if isSecretKey(key) {
				typed[key] = Redacted
				continue
			}
			typed[key] = redactValue(value)
		}
		return typed
	case []any:
		for i, value := range typed {
			typed[i] = redactValue(value)
		}
		return typed
	default:
		return v
	}
}
