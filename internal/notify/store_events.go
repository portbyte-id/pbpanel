package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// StoredEvent is one row of the event list behind the bell.
type StoredEvent struct {
	ID       int64             `json:"id"`
	At       time.Time         `json:"at"`
	Kind     string            `json:"kind"`
	Severity Severity          `json:"severity"`
	Key      string            `json:"key"`
	Subject  string            `json:"subject"`
	Body     string            `json:"body"`
	Source   string            `json:"source"`
	Link     string            `json:"link"`
	Fields   map[string]string `json:"fields"`
	Resolved bool              `json:"resolved"`
	ReadAt   *time.Time        `json:"read_at"`
}

// EventFilter pages the event list, newest first.
type EventFilter struct {
	Limit      int
	BeforeID   int64
	UnreadOnly bool
}

// Summary is the bell's badge.
type Summary struct {
	Unread       int64 `json:"unread"`
	OpenCritical int64 `json:"open_critical"`
	OpenWarning  int64 `json:"open_warning"`
}

const (
	defaultEventLimit = 50
	maxEventLimit     = 200
)

// stateRow is one condition key the engine has seen (notify_state).
type stateRow struct {
	Key         string
	Kind        string
	Source      string
	Open        bool
	Severity    Severity
	OpenedAt    time.Time
	LastEventID int64
}

// delivery is one row of notify_deliveries.
type delivery struct {
	ID        int64
	EventID   int64
	ChannelID int64
	State     string
	Reason    string
	Attempts  int
	NextAt    *time.Time
	LastError string
	SentAt    *time.Time
	CreatedAt time.Time
}

// DeliveryRow is a delivery with its event and channel names, for the log.
type DeliveryRow struct {
	ID          int64      `json:"id"`
	EventID     int64      `json:"event_id"`
	ChannelID   int64      `json:"channel_id"`
	ChannelName string     `json:"channel_name"`
	Kind        string     `json:"kind"`
	Subject     string     `json:"subject"`
	State       string     `json:"state"`
	Reason      string     `json:"reason"`
	Attempts    int        `json:"attempts"`
	NextAt      *time.Time `json:"next_at"`
	LastError   string     `json:"last_error"`
	SentAt      *time.Time `json:"sent_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// DeliveryFilter pages the delivery log, newest first.
type DeliveryFilter struct {
	ChannelID int64
	State     string
	Limit     int
	BeforeID  int64
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

func parseNullTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTime(s.String)
	return &t
}

// insertEvent stores a normalized event and returns its id.
func (r *Repo) insertEvent(ctx context.Context, q querier, e Event) (int64, error) {
	fields := e.Fields
	if fields == nil {
		fields = map[string]string{}
	}
	fb, err := json.Marshal(fields)
	if err != nil {
		return 0, fmt.Errorf("notify: encode fields: %w", err)
	}
	res, err := q.ExecContext(ctx, `INSERT INTO notify_events
		(at, kind, severity, key, subject, body, source, link, fields, resolved)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fmtTime(e.At), e.Kind, string(e.Severity), e.Key, e.Subject, e.Body, e.Source,
		e.Link, string(fb), boolInt(e.Resolved))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *Repo) getEvent(ctx context.Context, q querier, id int64) (StoredEvent, error) {
	var (
		ev       StoredEvent
		at, fb   string
		resolved int
		readAt   sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT id, at, kind, severity, key, subject, body, source, link,
		fields, resolved, read_at FROM notify_events WHERE id = ?`, id).Scan(
		&ev.ID, &at, &ev.Kind, &ev.Severity, &ev.Key, &ev.Subject, &ev.Body, &ev.Source,
		&ev.Link, &fb, &resolved, &readAt)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredEvent{}, ErrNotFound
	}
	if err != nil {
		return StoredEvent{}, err
	}
	ev.At = parseTime(at)
	ev.Resolved = resolved == 1
	ev.ReadAt = parseNullTime(readAt)
	ev.Fields = map[string]string{}
	if err := json.Unmarshal([]byte(fb), &ev.Fields); err != nil {
		return StoredEvent{}, fmt.Errorf("notify: decode fields of event %d: %w", id, err)
	}
	return ev, nil
}

// GetEvent returns one stored event.
func (r *Repo) GetEvent(ctx context.Context, id int64) (StoredEvent, error) {
	return r.getEvent(ctx, r.st.DB(), id)
}

// ListEvents returns the event list, newest first.
func (r *Repo) ListEvents(ctx context.Context, f EventFilter) ([]StoredEvent, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultEventLimit
	}
	if limit > maxEventLimit {
		limit = maxEventLimit
	}
	q := `SELECT id FROM notify_events WHERE 1 = 1`
	var args []any
	if f.BeforeID > 0 {
		q += ` AND id < ?`
		args = append(args, f.BeforeID)
	}
	if f.UnreadOnly {
		q += ` AND read_at IS NULL`
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.st.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	out := []StoredEvent{}
	for _, id := range ids {
		ev, err := r.GetEvent(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

// MarkRead sets read_at on the given events. It returns how many changed.
func (r *Repo) MarkRead(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	marks := make([]string, len(ids))
	args := []any{fmtTime(r.now())}
	for i, id := range ids {
		marks[i] = "?"
		args = append(args, id)
	}
	res, err := r.st.DB().ExecContext(ctx,
		`UPDATE notify_events SET read_at = ? WHERE read_at IS NULL AND id IN (`+strings.Join(marks, ",")+`)`,
		args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MarkAllRead sets read_at on every unread event.
func (r *Repo) MarkAllRead(ctx context.Context) (int64, error) {
	res, err := r.st.DB().ExecContext(ctx,
		`UPDATE notify_events SET read_at = ? WHERE read_at IS NULL`, fmtTime(r.now()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Summary counts unread events and open conditions by severity.
func (r *Repo) Summary(ctx context.Context) (Summary, error) {
	var s Summary
	if err := r.st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notify_events WHERE read_at IS NULL`).Scan(&s.Unread); err != nil {
		return s, err
	}
	rows, err := r.st.DB().QueryContext(ctx,
		`SELECT severity, COUNT(*) FROM notify_state WHERE open = 1 GROUP BY severity`)
	if err != nil {
		return s, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sev string
		var n int64
		if err := rows.Scan(&sev, &n); err != nil {
			return s, err
		}
		switch Severity(sev) {
		case Critical:
			s.OpenCritical = n
		case Warning:
			s.OpenWarning = n
		}
	}
	return s, rows.Err()
}

// pruneEvents keeps the newest keep events and drops anything older than
// maxAge (D13). Deliveries of a dropped event go with it, in the same
// transaction. A keep below 1 disables the count rule.
func (r *Repo) pruneEvents(ctx context.Context, keep int, maxAge time.Duration, now time.Time) (int64, error) {
	var removed int64
	err := r.st.WithTx(ctx, func(tx *sql.Tx) error {
		var boundary int64
		if keep >= 1 {
			err := tx.QueryRowContext(ctx,
				`SELECT id FROM notify_events ORDER BY id DESC LIMIT 1 OFFSET ?`, keep).Scan(&boundary)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		cutoff := fmtTime(now.Add(-maxAge))
		const where = `id <= ? OR at < ?`
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM notify_deliveries WHERE event_id IN (SELECT id FROM notify_events WHERE `+where+`)`,
			boundary, cutoff); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM notify_events WHERE `+where, boundary, cutoff)
		if err != nil {
			return err
		}
		removed, err = res.RowsAffected()
		return err
	})
	return removed, err
}

func (r *Repo) getState(ctx context.Context, q querier, key string) (stateRow, bool, error) {
	var (
		s      stateRow
		open   int
		opened string
	)
	err := q.QueryRowContext(ctx, `SELECT key, kind, source, open, severity, opened_at, last_event_id
		FROM notify_state WHERE key = ?`, key).Scan(&s.Key, &s.Kind, &s.Source, &open,
		&s.Severity, &opened, &s.LastEventID)
	if errors.Is(err, sql.ErrNoRows) {
		return stateRow{}, false, nil
	}
	if err != nil {
		return stateRow{}, false, err
	}
	s.Open = open == 1
	s.OpenedAt = parseTime(opened)
	return s, true, nil
}

func (r *Repo) putState(ctx context.Context, q querier, s stateRow) error {
	_, err := q.ExecContext(ctx, `INSERT INTO notify_state
		(key, kind, source, open, severity, opened_at, last_event_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET kind = excluded.kind, source = excluded.source,
			open = excluded.open, severity = excluded.severity, opened_at = excluded.opened_at,
			last_event_id = excluded.last_event_id`,
		s.Key, s.Kind, s.Source, boolInt(s.Open), string(s.Severity), fmtTime(s.OpenedAt), s.LastEventID)
	return err
}

// openStates returns the open keys of one source for the given kinds.
func (r *Repo) openStates(ctx context.Context, q querier, source string, kinds []string) ([]stateRow, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	marks := make([]string, len(kinds))
	args := []any{source}
	for i, k := range kinds {
		marks[i] = "?"
		args = append(args, k)
	}
	rows, err := q.QueryContext(ctx, `SELECT key, kind, source, open, severity, opened_at, last_event_id
		FROM notify_state WHERE open = 1 AND source = ? AND kind IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []stateRow
	for rows.Next() {
		var (
			s      stateRow
			opened string
		)
		if err := rows.Scan(&s.Key, &s.Kind, &s.Source, new(int), &s.Severity, &opened, &s.LastEventID); err != nil {
			return nil, err
		}
		s.Open = true
		s.OpenedAt = parseTime(opened)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Repo) insertDelivery(ctx context.Context, q querier, d delivery) (int64, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO notify_deliveries
		(event_id, channel_id, state, reason, attempts, next_at, last_error, sent_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.EventID, d.ChannelID, d.State, d.Reason, d.Attempts, nullTime(d.NextAt),
		d.LastError, nullTime(d.SentAt), fmtTime(d.CreatedAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// dueDeliveries returns queued deliveries whose time has come, oldest first.
func (r *Repo) dueDeliveries(ctx context.Context, now time.Time, limit int) ([]delivery, error) {
	rows, err := r.st.DB().QueryContext(ctx, `SELECT id, event_id, channel_id, state, reason, attempts,
		next_at, last_error, sent_at, created_at FROM notify_deliveries
		WHERE state = 'queued' AND (next_at IS NULL OR next_at <= ?) ORDER BY id LIMIT ?`,
		fmtTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repo) updateDelivery(ctx context.Context, q querier, d delivery) error {
	_, err := q.ExecContext(ctx, `UPDATE notify_deliveries SET state = ?, reason = ?, attempts = ?,
		next_at = ?, last_error = ?, sent_at = ? WHERE id = ?`,
		d.State, d.Reason, d.Attempts, nullTime(d.NextAt), d.LastError, nullTime(d.SentAt), d.ID)
	return err
}

func scanDelivery(row interface{ Scan(...any) error }) (delivery, error) {
	var (
		d              delivery
		nextAt, sentAt sql.NullString
		created        string
	)
	if err := row.Scan(&d.ID, &d.EventID, &d.ChannelID, &d.State, &d.Reason, &d.Attempts,
		&nextAt, &d.LastError, &sentAt, &created); err != nil {
		return delivery{}, err
	}
	d.NextAt = parseNullTime(nextAt)
	d.SentAt = parseNullTime(sentAt)
	d.CreatedAt = parseTime(created)
	return d, nil
}

// ListDeliveries returns the delivery log, newest first, with names joined in.
func (r *Repo) ListDeliveries(ctx context.Context, f DeliveryFilter) ([]DeliveryRow, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultEventLimit
	}
	if limit > maxEventLimit {
		limit = maxEventLimit
	}
	q := `SELECT d.id, d.event_id, d.channel_id, c.name, e.kind, e.subject, d.state, d.reason,
		d.attempts, d.next_at, d.last_error, d.sent_at, d.created_at
		FROM notify_deliveries d
		JOIN notify_channels c ON c.id = d.channel_id
		JOIN notify_events e ON e.id = d.event_id WHERE 1 = 1`
	var args []any
	if f.ChannelID > 0 {
		q += ` AND d.channel_id = ?`
		args = append(args, f.ChannelID)
	}
	if f.State != "" {
		q += ` AND d.state = ?`
		args = append(args, f.State)
	}
	if f.BeforeID > 0 {
		q += ` AND d.id < ?`
		args = append(args, f.BeforeID)
	}
	q += ` ORDER BY d.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.st.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []DeliveryRow{}
	for rows.Next() {
		var (
			row                     DeliveryRow
			nextAt, sentAt, created sql.NullString
		)
		if err := rows.Scan(&row.ID, &row.EventID, &row.ChannelID, &row.ChannelName, &row.Kind,
			&row.Subject, &row.State, &row.Reason, &row.Attempts, &nextAt, &row.LastError,
			&sentAt, &created); err != nil {
			return nil, err
		}
		row.NextAt = parseNullTime(nextAt)
		row.SentAt = parseNullTime(sentAt)
		row.CreatedAt = parseTime(created.String)
		out = append(out, row)
	}
	return out, rows.Err()
}

// RetryDelivery puts a failed delivery back in the queue with its attempts reset.
func (r *Repo) RetryDelivery(ctx context.Context, id int64) error {
	res, err := r.st.DB().ExecContext(ctx, `UPDATE notify_deliveries SET state = 'queued',
		attempts = 0, next_at = NULL, last_error = '' WHERE id = ? AND state = 'failed'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var state string
	err = r.st.DB().QueryRowContext(ctx, `SELECT state FROM notify_deliveries WHERE id = ?`, id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return ErrInvalidState
}
