// Package notify is the shared contract for notifications and alerts.
//
// Producers depend on the Emitter and Syncer interfaces alone. The engine,
// store and senders arrive in later steps of docs/design/notifications-plan.md;
// until then producers are wired to Nop. This file and its siblings import the
// standard library only.
package notify

import (
	"context"
	"time"
)

// Severity orders events for filtering. Empty means "the kind's default".
type Severity string

const (
	Info     Severity = "info"
	Warning  Severity = "warning"
	Critical Severity = "critical"
)

// rank orders severities: info < warning < critical. Unknown values rank 0.
func (s Severity) rank() int {
	switch s {
	case Info:
		return 1
	case Warning:
		return 2
	case Critical:
		return 3
	}
	return 0
}

// AtLeast reports whether s is at least min. An unknown s or min is never
// at least anything, so a bad filter value passes nothing.
func (s Severity) AtLeast(min Severity) bool {
	return s.Valid() && min.Valid() && s.rank() >= min.rank()
}

// Valid reports whether s is one of the three named severities.
func (s Severity) Valid() bool {
	return s.rank() > 0
}

// Event is one thing that happened, or one condition that started or cleared.
type Event struct {
	Kind     string            // "<area>.<event>", see ValidKind; required
	Severity Severity          // info | warning | critical; empty means the kind's default
	Key      string            // dedupe identity; empty means Kind. Convention: Kind + ":" + subject id
	Subject  string            // one line, <= 200 bytes after trimming; required
	Body     string            // plain text, <= 4 KiB; may be empty
	Source   string            // producing feature: "tls", "jobs", "backup", ...; required
	At       time.Time         // when it happened; zero means now
	Resolved bool              // this Key's condition has cleared
	Link     string            // panel-relative path, e.g. "/tls" or "/sites/example.com"; optional
	Fields   map[string]string // <= 16 short labels for webhooks (domain, job, metric, value); optional
}

// Emitter is what producers depend on. Emit never blocks on the network, never
// returns an error and never panics; a malformed event is logged and dropped.
type Emitter interface {
	Emit(ctx context.Context, e Event)
}

// Syncer is for producers that recompute a whole set of conditions each pass
// (the sweep, threshold checks, security checks). For every kind in kinds,
// a Key that is open for this source and absent from current gets a Resolved
// event; every event in current is emitted as by Emit.
type Syncer interface {
	Sync(ctx context.Context, source string, kinds []string, current []Event)
}

// Nop discards everything. Producers default to it, so they can land and be
// tested before notify is wired.
type Nop struct{}

// Emit discards e.
func (Nop) Emit(context.Context, Event) {}

// Sync discards current and resolves nothing.
func (Nop) Sync(context.Context, string, []string, []Event) {}

var (
	_ Emitter = Nop{}
	_ Syncer  = Nop{}
)

// Or returns e, or Nop when e is nil.
func Or(e Emitter) Emitter {
	if e == nil {
		return Nop{}
	}
	return e
}
