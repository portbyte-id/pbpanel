package notify

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/settings"
)

// emitBudget caps how long Emit may spend on its database write. A producer
// never waits longer than this, even when the database is busy (D1).
const emitBudget = 2 * time.Second

// subscriberBuffer is how many events a slow SSE client may fall behind before
// events are dropped for it. The bell refreshes from the list, so a drop is
// harmless.
const subscriberBuffer = 16

// Settings is the part of the settings registry the engine reads.
// *settings.Registry satisfies it.
type Settings interface {
	Bool(ctx context.Context, name string) (bool, error)
	Int(ctx context.Context, name string) (int, error)
	Duration(ctx context.Context, name string) (time.Duration, error)
}

// Options configures a Service.
type Options struct {
	Repo     *Repo
	Settings Settings     // nil means the defaults of §4.4
	Logger   *slog.Logger // nil means slog.Default()
	Now      func() time.Time
}

// Service is the notify engine: it records events and routes them, and it
// implements Emitter and Syncer for producers.
type Service struct {
	repo     *Repo
	settings Settings
	logger   *slog.Logger
	now      func() time.Time

	mu      sync.Mutex
	buckets map[int64]*bucket
	subs    map[chan StoredEvent]struct{}
	kick    chan struct{}
}

// New builds the engine. The dispatcher (step 4) listens on Kick.
func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		repo:     opts.Repo,
		settings: opts.Settings,
		logger:   logger,
		now:      now,
		buckets:  map[int64]*bucket{},
		subs:     map[chan StoredEvent]struct{}{},
		kick:     make(chan struct{}, 1),
	}
}

// Kick returns a channel that receives a value whenever deliveries were queued.
// The dispatcher selects on it; a value that is already pending is not repeated.
func (s *Service) Kick() <-chan struct{} {
	return s.kick
}

func (s *Service) wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// readPolicy reads the notify.* settings. An absent or failing registry
// falls back to the defaults; a producer never fails because of settings.
func (s *Service) readPolicy(ctx context.Context) policy {
	if s.settings == nil {
		return defaultPolicy
	}
	pol := defaultPolicy
	if v, err := s.settings.Bool(ctx, settings.KeyNotifyEnabled); err == nil {
		pol.enabled = v
	}
	if v, err := s.settings.Duration(ctx, settings.KeyNotifyDedupeWindow); err == nil {
		pol.window = v
	}
	if v, err := s.settings.Int(ctx, settings.KeyNotifyRatePerHour); err == nil {
		pol.perHour = v
	}
	return pol
}

// Emit implements Emitter. It never returns an error and never panics: a
// malformed event is logged and dropped, and a database failure is logged.
func (s *Service) Emit(ctx context.Context, e Event) {
	defer func() {
		if p := recover(); p != nil {
			s.logger.Error("notify: emit panicked; event dropped", "kind", e.Kind, "panic", p)
		}
	}()
	s.emit(ctx, e)
}

func (s *Service) emit(ctx context.Context, e Event) {
	now := s.now()
	n, err := Normalize(e, now)
	if err != nil {
		s.logger.Warn("notify: malformed event dropped", "kind", e.Kind, "err", err)
		return
	}

	// The write is detached from the caller's cancellation, so an event raised
	// while a request is ending is still recorded.
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitBudget)
	defer cancel()

	pol := s.readPolicy(dctx)
	var stored StoredEvent
	err = s.repo.st.WithTx(dctx, func(tx *sql.Tx) error {
		var err error
		stored, err = s.record(dctx, tx, n, pol, now)
		return err
	})
	if err != nil {
		s.logger.Error("notify: event not recorded", "kind", n.Kind, "key", n.Key, "err", err)
		return
	}
	s.publish(stored)
	s.wake()
}

// record stores the event, updates its condition state and routes it. It runs
// inside the caller's transaction.
func (s *Service) record(ctx context.Context, q querier, e Event, pol policy, now time.Time) (StoredEvent, error) {
	id, err := s.repo.insertEvent(ctx, q, e)
	if err != nil {
		return StoredEvent{}, fmt.Errorf("notify: insert event: %w", err)
	}
	stored, err := s.repo.getEvent(ctx, q, id)
	if err != nil {
		return StoredEvent{}, err
	}

	st, found, err := s.repo.getState(ctx, q, e.Key)
	if err != nil {
		return StoredEvent{}, err
	}
	if e.Resolved {
		// A resolved event for a key that is not open is stored in the list
		// only (§4.3). Otherwise the key closes and is routed once.
		if !found || !st.Open {
			return stored, nil
		}
		sev := st.Severity
		st.Open = false
		st.LastEventID = id
		if err := s.repo.putState(ctx, q, st); err != nil {
			return StoredEvent{}, err
		}
		return stored, s.route(ctx, q, id, e, sev, pol, now)
	}

	if !found || !st.Open {
		st = stateRow{Key: e.Key, Kind: e.Kind, Source: e.Source, Open: true, OpenedAt: now}
	}
	st.Severity = e.Severity
	st.LastEventID = id
	if err := s.repo.putState(ctx, q, st); err != nil {
		return StoredEvent{}, err
	}
	return stored, s.route(ctx, q, id, e, e.Severity, pol, now)
}

// Sync implements Syncer. Every event in current is emitted. For each open
// key of this source whose kind is in kinds and which is absent from current,
// a resolved event is emitted (§4.1).
func (s *Service) Sync(ctx context.Context, source string, kinds []string, current []Event) {
	defer func() {
		if p := recover(); p != nil {
			s.logger.Error("notify: sync panicked", "source", source, "panic", p)
		}
	}()

	present := map[string]bool{}
	for _, e := range current {
		if e.Source == "" {
			e.Source = source
		}
		if n, err := Normalize(e, s.now()); err == nil {
			present[n.Key] = true
		}
		s.Emit(ctx, e)
	}

	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitBudget)
	defer cancel()
	open, err := s.repo.openStates(dctx, s.repo.st.DB(), source, kinds)
	if err != nil {
		s.logger.Error("notify: sync could not read open conditions", "source", source, "err", err)
		return
	}
	for _, st := range open {
		if present[st.Key] {
			continue
		}
		s.Emit(ctx, Event{
			Kind:     st.Kind,
			Severity: st.Severity,
			Key:      st.Key,
			Subject:  "Cleared: " + st.Key,
			Source:   source,
			Resolved: true,
		})
	}
}

// Subscribe returns a channel of events as they are recorded, and a function
// that ends the subscription. Events are dropped for a subscriber that falls
// behind, so Emit never waits on it.
func (s *Service) Subscribe() (<-chan StoredEvent, func()) {
	ch := make(chan StoredEvent, subscriberBuffer)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if _, ok := s.subs[ch]; ok {
				delete(s.subs, ch)
				close(ch)
			}
		})
	}
}

func (s *Service) publish(ev StoredEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Prune applies the retention rule (D13) with the configured limits.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	keep, age := 1000, 720*time.Hour
	if s.settings != nil {
		if v, err := s.settings.Int(ctx, settings.KeyNotifyEventsKeep); err == nil {
			keep = v
		}
		if v, err := s.settings.Duration(ctx, settings.KeyNotifyEventsRetainAge); err == nil {
			age = v
		}
	}
	return s.repo.pruneEvents(ctx, keep, age, s.now())
}
