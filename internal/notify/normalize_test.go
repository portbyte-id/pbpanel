package notify

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var testNow = time.Date(2026, 11, 25, 12, 0, 0, 0, time.FixedZone("X", 3600))

func TestValidKind(t *testing.T) {
	cases := []struct {
		kind string
		want bool
	}{
		{"tls.renew_failed", true},
		{"a.b", true},
		{"a.b_c", true},
		{"backup2.ok9", true},
		{"tls", false},
		{"TLS.renew", false},
		{"tls.Renew", false},
		{"1tls.renew", false},
		{"tls.1renew", false},
		{"tls..renew", false},
		{"tls.renew-failed", false},
		{"tls.renew.failed", false},
		{"", false},
		{"a." + strings.Repeat("b", 46), true},  // 48 bytes exactly
		{"a." + strings.Repeat("b", 47), false}, // 49 bytes
	}
	for _, c := range cases {
		if got := ValidKind(c.kind); got != c.want {
			t.Errorf("ValidKind(%q) = %v, want %v", c.kind, got, c.want)
		}
	}
}

func TestDefaultSeverity(t *testing.T) {
	if got := DefaultSeverity(KindTLSRenewFailed); got != Critical {
		t.Errorf("tls.renew_failed default = %q, want critical", got)
	}
	if got := DefaultSeverity(KindBanAdded); got != Info {
		t.Errorf("ban.added default = %q, want info", got)
	}
	if got := DefaultSeverity("acme.unknown_thing"); got != Warning {
		t.Errorf("unknown kind default = %q, want warning", got)
	}
}

func TestKnownKindsAreWellFormedAndSorted(t *testing.T) {
	kinds := Kinds()
	if len(kinds) != len(registry) {
		t.Fatalf("Kinds() returned %d, registry has %d", len(kinds), len(registry))
	}
	for i, info := range kinds {
		if !ValidKind(info.Kind) {
			t.Errorf("registry kind %q is not well formed", info.Kind)
		}
		if !info.Severity.Valid() {
			t.Errorf("registry kind %q has invalid severity %q", info.Kind, info.Severity)
		}
		if info.Label == "" {
			t.Errorf("registry kind %q has no label", info.Kind)
		}
		if !Known(info.Kind) {
			t.Errorf("Known(%q) = false", info.Kind)
		}
		if i > 0 && kinds[i-1].Kind >= info.Kind {
			t.Errorf("Kinds() not sorted at %q", info.Kind)
		}
	}
}

func TestNormalizeRefusesBadKind(t *testing.T) {
	_, err := Normalize(Event{Kind: "tls", Subject: "s", Source: "tls"}, testNow)
	assertInvalid(t, err)
}

func TestNormalizeAcceptsUnknownWellFormedKind(t *testing.T) {
	out, err := Normalize(Event{Kind: "acme.unknown_thing", Subject: "s", Source: "acme"}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Kind != "acme.unknown_thing" || out.Severity != Warning {
		t.Errorf("got kind %q severity %q", out.Kind, out.Severity)
	}
}

func TestNormalizeRequiresSourceAndSubject(t *testing.T) {
	_, err := Normalize(Event{Kind: "tls.expiring", Subject: "s", Source: "   "}, testNow)
	assertInvalid(t, err)

	_, err = Normalize(Event{Kind: "tls.expiring", Subject: " \t\n ", Source: "tls"}, testNow)
	assertInvalid(t, err)
}

func TestNormalizeSourceCap(t *testing.T) {
	_, err := Normalize(Event{Kind: "tls.expiring", Subject: "s", Source: strings.Repeat("x", 65)}, testNow)
	assertInvalid(t, err)
}

func TestNormalizeDefaultsSeverityKeyAndTime(t *testing.T) {
	out, err := Normalize(Event{Kind: KindTLSRenewFailed, Subject: "example.com", Source: "tls"}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Severity != Critical {
		t.Errorf("severity = %q, want critical (kind default)", out.Severity)
	}
	if out.Key != KindTLSRenewFailed {
		t.Errorf("key = %q, want the kind when Key is empty", out.Key)
	}
	if !out.At.Equal(testNow) || out.At.Location() != time.UTC {
		t.Errorf("At = %v (%v), want %v in UTC", out.At, out.At.Location(), testNow)
	}
}

func TestNormalizeKeepsExplicitSeverityAndKey(t *testing.T) {
	out, err := Normalize(Event{
		Kind: KindTLSExpiring, Severity: Critical, Key: "tls.expiring:example.com",
		Subject: "s", Source: "tls",
	}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Severity != Critical || out.Key != "tls.expiring:example.com" {
		t.Errorf("severity %q key %q", out.Severity, out.Key)
	}
}

func TestNormalizeRefusesUnknownSeverity(t *testing.T) {
	_, err := Normalize(Event{Kind: "tls.expiring", Severity: "urgent", Subject: "s", Source: "tls"}, testNow)
	assertInvalid(t, err)
}

func TestNormalizeKeepsExplicitTimeInUTC(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("Z", -5*3600))
	out, err := Normalize(Event{Kind: "tls.expiring", Subject: "s", Source: "tls", At: at}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.At.Equal(at) || out.At.Location() != time.UTC {
		t.Errorf("At = %v, want the same instant in UTC", out.At)
	}
}

func TestNormalizeSubjectOneLineAndCapped(t *testing.T) {
	out, err := Normalize(Event{Kind: "tls.expiring", Subject: "  a\n\tb   c  ", Source: "tls"}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Subject != "a b c" {
		t.Errorf("subject = %q, want %q", out.Subject, "a b c")
	}

	long := strings.Repeat("x", 199) + "é" // 201 bytes; é starts at byte 199
	out, err = Normalize(Event{Kind: "tls.expiring", Subject: long, Source: "tls"}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Subject) > 200 {
		t.Errorf("subject is %d bytes, cap 200", len(out.Subject))
	}
	if !utf8.ValidString(out.Subject) {
		t.Errorf("subject split a rune: %q", out.Subject)
	}
	if out.Subject != strings.Repeat("x", 199) {
		t.Errorf("subject cut at the wrong place: len %d", len(out.Subject))
	}
}

func TestNormalizeBodyTrimmedAndCapped(t *testing.T) {
	out, err := Normalize(Event{Kind: "tls.expiring", Subject: "s", Source: "tls", Body: "  hi  \n"}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Body != "hi" {
		t.Errorf("body = %q, want %q", out.Body, "hi")
	}

	body := strings.Repeat("ü", 3000) // 6000 bytes
	out, err = Normalize(Event{Kind: "tls.expiring", Subject: "s", Source: "tls", Body: body}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Body) > 4096 || !utf8.ValidString(out.Body) {
		t.Errorf("body len %d valid=%v, want <= 4096 and valid UTF-8", len(out.Body), utf8.ValidString(out.Body))
	}
}

func TestNormalizeReplacesInvalidUTF8(t *testing.T) {
	out, err := Normalize(Event{Kind: "tls.expiring", Subject: "bad\xffbyte", Source: "tls"}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !utf8.ValidString(out.Subject) || !strings.Contains(out.Subject, "�") {
		t.Errorf("subject = %q, want invalid bytes replaced", out.Subject)
	}
}

func TestNormalizeKeyCapRefusedNotCut(t *testing.T) {
	_, err := Normalize(Event{
		Kind: "tls.expiring", Key: "tls.expiring:" + strings.Repeat("d", 200),
		Subject: "s", Source: "tls",
	}, testNow)
	assertInvalid(t, err)
}

func TestNormalizeLinkMustBePanelRelative(t *testing.T) {
	good := []string{"", "/tls", "/sites/example.com"}
	for _, link := range good {
		if _, err := Normalize(Event{Kind: "tls.expiring", Subject: "s", Source: "tls", Link: link}, testNow); err != nil {
			t.Errorf("link %q refused: %v", link, err)
		}
	}
	bad := []string{"tls", "https://evil.example/", "//evil.example/", "javascript:alert(1)"}
	for _, link := range bad {
		_, err := Normalize(Event{Kind: "tls.expiring", Subject: "s", Source: "tls", Link: link}, testNow)
		if err == nil {
			t.Errorf("link %q accepted, want refused", link)
		}
	}
}

func TestNormalizeFields(t *testing.T) {
	in := map[string]string{"domain": " example.com ", "value": strings.Repeat("v", 300)}
	out, err := Normalize(Event{Kind: "tls.expiring", Subject: "s", Source: "tls", Fields: in}, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Fields["domain"] != "example.com" {
		t.Errorf("domain = %q", out.Fields["domain"])
	}
	if len(out.Fields["value"]) != 256 {
		t.Errorf("value length %d, want 256", len(out.Fields["value"]))
	}

	// The copy must not alias the caller's map.
	in["domain"] = "changed"
	if out.Fields["domain"] != "example.com" {
		t.Errorf("output aliases the input map")
	}
}

func TestNormalizeFieldLimits(t *testing.T) {
	many := map[string]string{}
	for i := range 17 {
		many["k"+string(rune('a'+i))] = "v"
	}
	_, err := Normalize(Event{Kind: "tls.expiring", Subject: "s", Source: "tls", Fields: many}, testNow)
	assertInvalid(t, err)

	_, err = Normalize(Event{Kind: "tls.expiring", Subject: "s", Source: "tls",
		Fields: map[string]string{strings.Repeat("n", 33): "v"}}, testNow)
	assertInvalid(t, err)

	_, err = Normalize(Event{Kind: "tls.expiring", Subject: "s", Source: "tls",
		Fields: map[string]string{"  ": "v"}}, testNow)
	assertInvalid(t, err)
}

func TestNormalizeDoesNotModifyInput(t *testing.T) {
	in := Event{Kind: "tls.expiring", Subject: "  padded  ", Source: " tls ", Fields: map[string]string{"a": "b"}}
	_, err := Normalize(in, testNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if in.Subject != "  padded  " || in.Source != " tls " || in.Key != "" {
		t.Errorf("input was modified: %+v", in)
	}
}

func assertInvalid(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("error %v does not wrap ErrInvalidEvent", err)
	}
}
