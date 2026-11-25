package notify

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Caps from docs/design/notifications-plan.md §4.1.
const (
	maxSubjectBytes = 200
	maxBodyBytes    = 4 << 10
	maxSourceBytes  = 64
	maxKeyBytes     = 200
	maxLinkBytes    = 512
	maxFields       = 16
	maxFieldKey     = 32
	maxFieldValue   = 256
)

// ErrInvalidEvent wraps every reason Normalize refuses an event.
var ErrInvalidEvent = errors.New("notify: invalid event")

// Normalize returns a cleaned copy of e ready for storage, or an error naming
// the field that is wrong. It does not modify e.
//
// Rules:
//   - Kind must be well formed (ValidKind). Unknown well-formed kinds pass.
//   - Source is required and at most 64 bytes.
//   - Subject is required; whitespace is collapsed to single spaces and the
//     result is cut to 200 bytes on a rune boundary.
//   - Body is trimmed and cut to 4 KiB on a rune boundary.
//   - Severity empty becomes the kind's default; any other unknown value is refused.
//   - Key empty becomes Kind. A Key longer than 200 bytes is refused rather
//     than cut, because a cut key could collide with another one.
//   - At zero becomes now; the result is always in UTC.
//   - Link, when set, must be a panel-relative path: it starts with "/" and not "//".
//   - Fields are copied; at most 16, each key non-empty and at most 32 bytes,
//     each value cut to 256 bytes on a rune boundary.
//   - Invalid UTF-8 is replaced with U+FFFD in every text field.
func Normalize(e Event, now time.Time) (Event, error) {
	out := e

	if !ValidKind(e.Kind) {
		return Event{}, fmt.Errorf("%w: kind %q is not <area>.<event>", ErrInvalidEvent, e.Kind)
	}

	out.Source = strings.TrimSpace(cleanText(e.Source))
	if out.Source == "" {
		return Event{}, fmt.Errorf("%w: source is required", ErrInvalidEvent)
	}
	if len(out.Source) > maxSourceBytes {
		return Event{}, fmt.Errorf("%w: source is %d bytes, limit %d", ErrInvalidEvent, len(out.Source), maxSourceBytes)
	}

	out.Subject = strings.Join(strings.Fields(cleanText(e.Subject)), " ")
	if out.Subject == "" {
		return Event{}, fmt.Errorf("%w: subject is required", ErrInvalidEvent)
	}
	out.Subject = cutBytes(out.Subject, maxSubjectBytes)

	out.Body = cutBytes(strings.TrimSpace(cleanText(e.Body)), maxBodyBytes)

	switch {
	case e.Severity == "":
		out.Severity = DefaultSeverity(e.Kind)
	case e.Severity.Valid():
		out.Severity = e.Severity
	default:
		return Event{}, fmt.Errorf("%w: severity %q is not info, warning or critical", ErrInvalidEvent, e.Severity)
	}

	out.Key = strings.TrimSpace(cleanText(e.Key))
	if out.Key == "" {
		out.Key = e.Kind
	}
	if len(out.Key) > maxKeyBytes {
		return Event{}, fmt.Errorf("%w: key is %d bytes, limit %d", ErrInvalidEvent, len(out.Key), maxKeyBytes)
	}

	if e.At.IsZero() {
		out.At = now.UTC()
	} else {
		out.At = e.At.UTC()
	}

	out.Link = strings.TrimSpace(e.Link)
	if out.Link != "" {
		if !strings.HasPrefix(out.Link, "/") || strings.HasPrefix(out.Link, "//") {
			return Event{}, fmt.Errorf("%w: link %q is not a panel-relative path", ErrInvalidEvent, out.Link)
		}
		if len(out.Link) > maxLinkBytes {
			return Event{}, fmt.Errorf("%w: link is %d bytes, limit %d", ErrInvalidEvent, len(out.Link), maxLinkBytes)
		}
	}

	out.Fields = nil
	if len(e.Fields) > 0 {
		if len(e.Fields) > maxFields {
			return Event{}, fmt.Errorf("%w: %d fields, limit %d", ErrInvalidEvent, len(e.Fields), maxFields)
		}
		out.Fields = make(map[string]string, len(e.Fields))
		for k, v := range e.Fields {
			name := strings.TrimSpace(cleanText(k))
			if name == "" || len(name) > maxFieldKey {
				return Event{}, fmt.Errorf("%w: field name %q must be 1-%d bytes", ErrInvalidEvent, k, maxFieldKey)
			}
			out.Fields[name] = cutBytes(strings.TrimSpace(cleanText(v)), maxFieldValue)
		}
	}

	return out, nil
}

// cleanText replaces invalid UTF-8 so that every later length check counts
// real text.
func cleanText(s string) string {
	return strings.ToValidUTF8(s, "�")
}

// cutBytes returns s cut to at most limit bytes without splitting a rune.
// s must already be valid UTF-8 (see cleanText).
func cutBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	i := limit
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}
