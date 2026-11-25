package notify

import (
	"context"
	"testing"
)

func TestSeverityAtLeast(t *testing.T) {
	cases := []struct {
		s, min Severity
		want   bool
	}{
		{Critical, Critical, true},
		{Critical, Warning, true},
		{Critical, Info, true},
		{Warning, Critical, false},
		{Warning, Warning, true},
		{Warning, Info, true},
		{Info, Warning, false},
		{Info, Info, true},
		{"", Info, false},
		{"urgent", Info, false},
		{Info, "", false},
	}
	for _, c := range cases {
		if got := c.s.AtLeast(c.min); got != c.want {
			t.Errorf("%q.AtLeast(%q) = %v, want %v", c.s, c.min, got, c.want)
		}
	}
}

func TestSeverityValid(t *testing.T) {
	for _, s := range []Severity{Info, Warning, Critical} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []Severity{"", "Warning", "error"} {
		if s.Valid() {
			t.Errorf("%q should not be valid", s)
		}
	}
}

// Nop must satisfy both producer-facing interfaces, so sibling plans can
// compile and run against it before notify is wired.
func TestNopSatisfiesContract(t *testing.T) {
	var _ Emitter = Nop{}
	var _ Syncer = Nop{}

	ctx := context.Background()
	Nop{}.Emit(ctx, Event{Kind: "tls.renew_failed", Subject: "x", Source: "tls"})
	Nop{}.Sync(ctx, "sweep", []string{"tls.expiring"}, []Event{{Kind: "tls.expiring"}})
}

func TestOrReturnsNopForNil(t *testing.T) {
	got := Or(nil)
	if _, ok := got.(Nop); !ok {
		t.Fatalf("Or(nil) = %T, want Nop", got)
	}
}

func TestOrKeepsNonNil(t *testing.T) {
	rec := &recorder{}
	if got := Or(rec); got != Emitter(rec) {
		t.Fatalf("Or(rec) did not return rec")
	}
}

type recorder struct{ n int }

func (r *recorder) Emit(context.Context, Event) { r.n++ }
