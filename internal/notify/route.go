package notify

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"
)

// Delivery states and suppression reasons (notify_deliveries).
const (
	stateQueued     = "queued"
	stateSent       = "sent"
	stateSuppressed = "suppressed"

	reasonDisabled = "disabled"
	reasonDedupe   = "dedupe"
	reasonQuiet    = "quiet"
	reasonRate     = "rate"
)

// policy is the settings a routing decision reads, captured once per event.
type policy struct {
	enabled bool
	window  time.Duration
	perHour int
}

// defaultPolicy is what the engine uses when the settings registry is absent
// or unreadable: the defaults of §4.4.
var defaultPolicy = policy{enabled: true, window: 6 * time.Hour, perHour: 30}

// bucket is a per-channel token bucket: perHour tokens, refilled evenly.
type bucket struct {
	tokens float64
	last   time.Time
}

// take spends one token if one is available. A bucket that was never used
// starts full.
func (b *bucket) take(now time.Time, perHour int) bool {
	capacity := float64(perHour)
	if b.last.IsZero() {
		b.tokens = capacity
	} else if elapsed := now.Sub(b.last).Hours(); elapsed > 0 {
		// A clock that moves back refills nothing; it never takes tokens away.
		b.tokens = min(capacity, b.tokens+elapsed*capacity)
	}
	if now.After(b.last) {
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// inQuiet reports whether now falls inside the channel's quiet hours (D4).
// Times are HH:MM in the server's local time zone. The window may cross
// midnight: 22:00-07:00 covers 23:00 and 06:59, not 07:00.
func inQuiet(start, end string, now time.Time) bool {
	if start == "" || end == "" {
		return false
	}
	s, err1 := clockMinutes(start)
	e, err2 := clockMinutes(end)
	if err1 != nil || err2 != nil || s == e {
		return false
	}
	local := now.Local()
	cur := local.Hour()*60 + local.Minute()
	if s < e {
		return cur >= s && cur < e
	}
	return cur >= s || cur < e
}

// matches reports whether a channel's filter takes an event of this kind and
// severity. Resolved events use the severity of the condition they close.
func matches(ch Channel, kind string, sev Severity) bool {
	if !ch.Enabled || !sev.AtLeast(ch.MinSeverity) {
		return false
	}
	return len(ch.Kinds) == 0 || slices.Contains(ch.Kinds, kind)
}

// enabledChannels returns the enabled channels, read through q so the read
// sees the same transaction as the event insert.
func (r *Repo) enabledChannels(ctx context.Context, q querier) ([]Channel, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+channelCols+` FROM notify_channels WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Channel
	for rows.Next() {
		c, err := r.scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// lastProblem finds the most recent problem delivery this channel has
// queued or sent for key. It returns that delivery's severity too, which
// the escalation rule needs.
func (r *Repo) lastProblem(ctx context.Context, q querier, channelID int64, key string) (delivery, Severity, bool, error) {
	var (
		d       delivery
		sev     string
		created string
		nextAt  sql.NullString
		sentAt  sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT d.id, d.event_id, d.channel_id, d.state, d.reason,
		d.attempts, d.next_at, d.last_error, d.sent_at, d.created_at, e.severity
		FROM notify_deliveries d JOIN notify_events e ON e.id = d.event_id
		WHERE d.channel_id = ? AND e.key = ? AND e.resolved = 0 AND d.state IN ('queued', 'sent')
		ORDER BY d.id DESC LIMIT 1`, channelID, key).Scan(&d.ID, &d.EventID, &d.ChannelID, &d.State,
		&d.Reason, &d.Attempts, &nextAt, &d.LastError, &sentAt, &created, &sev)
	if err == sql.ErrNoRows {
		return delivery{}, "", false, nil
	}
	if err != nil {
		return delivery{}, "", false, err
	}
	d.NextAt = parseNullTime(nextAt)
	d.SentAt = parseNullTime(sentAt)
	d.CreatedAt = parseTime(created)
	return d, Severity(sev), true, nil
}

// verdict decides what happens to one event on one channel: queued, or
// suppressed with the reason. Order: disabled, dedupe, quiet hours, rate.
// A resolved event skips dedupe, and it is not escalated.
func (s *Service) verdict(ctx context.Context, q querier, ch Channel, e Event, sev Severity, pol policy, now time.Time) (string, string, error) {
	if !pol.enabled {
		return stateSuppressed, reasonDisabled, nil
	}
	if !e.Resolved {
		prev, prevSev, found, err := s.repo.lastProblem(ctx, q, ch.ID, e.Key)
		if err != nil {
			return "", "", err
		}
		escalated := found && sev.AtLeast(prevSev) && sev != prevSev
		if found && !escalated && now.Sub(prev.CreatedAt) < pol.window {
			return stateSuppressed, reasonDedupe, nil
		}
	}
	if sev != Critical && inQuiet(ch.QuietStart, ch.QuietEnd, now) {
		return stateSuppressed, reasonQuiet, nil
	}
	if !s.take(ch.ID, now, pol.perHour) {
		return stateSuppressed, reasonRate, nil
	}
	return stateQueued, "", nil
}

// take is the per-channel rate limit (D5). Buckets live in memory: a restart
// starts every channel with a full budget.
func (s *Service) take(channelID int64, now time.Time, perHour int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.buckets[channelID]
	if !ok {
		b = &bucket{}
		s.buckets[channelID] = b
	}
	return b.take(now, perHour)
}

// route records one delivery row per matching channel for event e, which
// has already been stored as eventID. A resolved event goes only to channels
// that were sent a problem for the same key (send_resolved on the channel).
func (s *Service) route(ctx context.Context, q querier, eventID int64, e Event, sev Severity, pol policy, now time.Time) error {
	channels, err := s.repo.enabledChannels(ctx, q)
	if err != nil {
		return fmt.Errorf("notify: list channels: %w", err)
	}
	for _, ch := range channels {
		if !matches(ch, e.Kind, sev) {
			continue
		}
		if e.Resolved {
			if !ch.SendResolved {
				continue
			}
			_, _, found, err := s.repo.lastProblem(ctx, q, ch.ID, e.Key)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
		}
		state, reason, err := s.verdict(ctx, q, ch, e, sev, pol, now)
		if err != nil {
			return err
		}
		d := delivery{EventID: eventID, ChannelID: ch.ID, State: state, Reason: reason, CreatedAt: now}
		if state == stateQueued {
			d.NextAt = &now
		}
		if _, err := s.repo.insertDelivery(ctx, q, d); err != nil {
			return err
		}
	}
	return nil
}
