package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/portbyte-id/server-panel/internal/secretbox"
	"github.com/portbyte-id/server-panel/internal/store"
)

// Channel types (notify_channels.type).
const (
	TypeSMTP     = "smtp"
	TypeWebhook  = "webhook"
	TypeTelegram = "telegram"
	TypeDiscord  = "discord"
)

// Errors the repository and the service return.
var (
	ErrNotFound     = errors.New("notify: not found")
	ErrNameTaken    = errors.New("notify: a channel with that name exists")
	ErrNoSecretKey  = errors.New("notify: channel credentials need data/secret.key, which this host does not have")
	ErrInvalidState = errors.New("notify: the delivery is not in a state that allows this")
)

// timeLayout is fixed-width UTC, so that text comparison in SQL matches time order.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// querier is satisfied by *sql.DB and *sql.Tx, so every helper runs inside
// a caller's transaction when one is open.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Repo is the persistence layer for notify. box is nil on a host without
// data/secret.key: reads still work, and any call that would store a secret
// returns ErrNoSecretKey (D2).
type Repo struct {
	st  *store.Store
	box *secretbox.Box
	now func() time.Time
}

// NewRepo builds the repository. now is the clock; nil means time.Now.
func NewRepo(st *store.Store, box *secretbox.Box, now func() time.Time) *Repo {
	if now == nil {
		now = time.Now
	}
	return &Repo{st: st, box: box, now: now}
}

// ChannelWrite is what an operator sets on a channel. Secret holds only the
// secret fields; an empty value deletes that field on update.
type ChannelWrite struct {
	Name         string
	Type         string
	Enabled      bool
	MinSeverity  Severity
	Kinds        []string
	QuietStart   string
	QuietEnd     string
	SendResolved bool
	Config       map[string]any
	Secret       map[string]string
}

// Channel is a channel as the API may show it. It never carries a secret
// value: SecretSet names the secret fields that are stored.
type Channel struct {
	ID               int64           `json:"id"`
	Name             string          `json:"name"`
	Type             string          `json:"type"`
	Enabled          bool            `json:"enabled"`
	MinSeverity      Severity        `json:"min_severity"`
	Kinds            []string        `json:"kinds"`
	QuietStart       string          `json:"quiet_start"`
	QuietEnd         string          `json:"quiet_end"`
	SendResolved     bool            `json:"send_resolved"`
	Config           map[string]any  `json:"config"`
	SecretSet        map[string]bool `json:"secret_set"`
	SecretUnreadable bool            `json:"secret_unreadable"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

const channelCols = `id, name, type, enabled, min_severity, kinds, quiet_start, quiet_end,
	send_resolved, config, secret, created_at, updated_at`

// validateWrite checks what the store depends on. Per-type config rules
// (URL checks, required fields) are checked by the service before this.
func validateWrite(w ChannelWrite) (ChannelWrite, error) {
	w.Name = strings.TrimSpace(w.Name)
	if w.Name == "" || len(w.Name) > 64 {
		return w, fmt.Errorf("%w: name must be 1-64 characters", ErrInvalidEvent)
	}
	switch w.Type {
	case TypeSMTP, TypeWebhook, TypeTelegram, TypeDiscord:
	default:
		return w, fmt.Errorf("%w: type %q is not smtp, webhook, telegram or discord", ErrInvalidEvent, w.Type)
	}
	if w.MinSeverity == "" {
		w.MinSeverity = Warning
	}
	if !w.MinSeverity.Valid() {
		return w, fmt.Errorf("%w: min_severity %q is not info, warning or critical", ErrInvalidEvent, w.MinSeverity)
	}
	for _, k := range w.Kinds {
		if !ValidKind(k) {
			return w, fmt.Errorf("%w: kind filter %q is not <area>.<event>", ErrInvalidEvent, k)
		}
	}
	if w.Kinds == nil {
		w.Kinds = []string{}
	}
	if err := checkClock(w.QuietStart, w.QuietEnd); err != nil {
		return w, err
	}
	if w.Config == nil {
		w.Config = map[string]any{}
	}
	return w, nil
}

// checkClock accepts both empty (no quiet hours) or two HH:MM values.
func checkClock(start, end string) error {
	if start == "" && end == "" {
		return nil
	}
	if _, err := clockMinutes(start); err != nil {
		return fmt.Errorf("%w: quiet_start %q is not HH:MM", ErrInvalidEvent, start)
	}
	if _, err := clockMinutes(end); err != nil {
		return fmt.Errorf("%w: quiet_end %q is not HH:MM", ErrInvalidEvent, end)
	}
	return nil
}

// clockMinutes parses HH:MM (00:00-23:59) into minutes after midnight.
func clockMinutes(s string) (int, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, ErrInvalidEvent
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[3:])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, ErrInvalidEvent
	}
	return h*60 + m, nil
}

func secretAAD(id int64) string {
	return "notify_channels:secret:" + strconv.FormatInt(id, 10)
}

// seal encrypts the secret fields for row id. An empty map seals to "".
func (r *Repo) seal(secret map[string]string, id int64) (string, error) {
	if len(secret) == 0 {
		return "", nil
	}
	plain, err := json.Marshal(secret)
	if err != nil {
		return "", fmt.Errorf("notify: encode secret: %w", err)
	}
	sealed, err := r.box.Seal(plain, secretAAD(id))
	if err != nil {
		return "", fmt.Errorf("notify: seal secret: %w", err)
	}
	return sealed, nil
}

// unseal opens a stored secret blob. It returns an empty map for "" and an
// error when the blob cannot be opened (a rotated key, a copied row, or no key).
func (r *Repo) unseal(blob string, id int64) (map[string]string, error) {
	if blob == "" {
		return map[string]string{}, nil
	}
	if r.box == nil {
		return nil, ErrNoSecretKey
	}
	plain, err := r.box.Open(blob, secretAAD(id))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("notify: decode secret: %w", err)
	}
	return out, nil
}

// OpenSecret returns the decrypted secret fields of a channel for the
// dispatcher. It is not exposed to the API.
func (r *Repo) OpenSecret(ctx context.Context, id int64) (map[string]string, error) {
	var blob string
	err := r.st.DB().QueryRowContext(ctx, `SELECT secret FROM notify_channels WHERE id = ?`, id).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("notify: read secret: %w", err)
	}
	return r.unseal(blob, id)
}

// scanChannel reads one channel row. An unreadable secret is reported on the
// channel, not as an error, so one bad row does not hide the others.
func (r *Repo) scanChannel(row interface{ Scan(...any) error }) (Channel, error) {
	var (
		c                  Channel
		enabled, sendRes   int
		minSev, kinds, cfg string
		blob, created, upd string
	)
	if err := row.Scan(&c.ID, &c.Name, &c.Type, &enabled, &minSev, &kinds, &c.QuietStart,
		&c.QuietEnd, &sendRes, &cfg, &blob, &created, &upd); err != nil {
		return Channel{}, err
	}
	c.Enabled = enabled == 1
	c.SendResolved = sendRes == 1
	c.MinSeverity = Severity(minSev)
	c.CreatedAt = parseTime(created)
	c.UpdatedAt = parseTime(upd)
	if err := json.Unmarshal([]byte(kinds), &c.Kinds); err != nil || c.Kinds == nil {
		c.Kinds = []string{}
	}
	c.Config = map[string]any{}
	if err := json.Unmarshal([]byte(cfg), &c.Config); err != nil {
		return Channel{}, fmt.Errorf("notify: decode config of channel %d: %w", c.ID, err)
	}
	c.SecretSet = map[string]bool{}
	fields, err := r.unseal(blob, c.ID)
	if err != nil {
		c.SecretUnreadable = blob != ""
		return c, nil
	}
	for k := range fields {
		c.SecretSet[k] = true
	}
	return c, nil
}

// CreateChannel stores a new channel. It needs the secret key: a channel is
// always created with its credentials sealed (D2).
func (r *Repo) CreateChannel(ctx context.Context, w ChannelWrite) (Channel, error) {
	if r.box == nil {
		return Channel{}, ErrNoSecretKey
	}
	w, err := validateWrite(w)
	if err != nil {
		return Channel{}, err
	}
	secret := nonEmpty(w.Secret)

	var id int64
	err = r.st.WithTx(ctx, func(tx *sql.Tx) error {
		now := fmtTime(r.now())
		res, err := tx.ExecContext(ctx, `INSERT INTO notify_channels
			(name, type, enabled, min_severity, kinds, quiet_start, quiet_end, send_resolved,
			 config, secret, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
			w.Name, w.Type, boolInt(w.Enabled), string(w.MinSeverity), mustJSON(w.Kinds),
			w.QuietStart, w.QuietEnd, boolInt(w.SendResolved), mustJSON(w.Config), now, now)
		if err != nil {
			return mapNameErr(err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		sealed, err := r.seal(secret, id)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE notify_channels SET secret = ? WHERE id = ?`, sealed, id)
		return err
	})
	if err != nil {
		return Channel{}, err
	}
	return r.GetChannel(ctx, id)
}

// UpdateChannel replaces a channel's settings. Secret fields not named in
// w.Secret keep their stored values; a name given "" is removed.
func (r *Repo) UpdateChannel(ctx context.Context, id int64, w ChannelWrite) (Channel, error) {
	w, err := validateWrite(w)
	if err != nil {
		return Channel{}, err
	}
	err = r.st.WithTx(ctx, func(tx *sql.Tx) error {
		var blob string
		if err := tx.QueryRowContext(ctx, `SELECT secret FROM notify_channels WHERE id = ?`, id).Scan(&blob); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		newBlob := blob
		if len(w.Secret) > 0 {
			if r.box == nil {
				return ErrNoSecretKey
			}
			// A stored secret this host cannot open is treated as empty, so the
			// operator re-enters it rather than the update failing.
			merged, err := r.unseal(blob, id)
			if err != nil {
				merged = map[string]string{}
			}
			for k, v := range w.Secret {
				if v == "" {
					delete(merged, k)
				} else {
					merged[k] = v
				}
			}
			if newBlob, err = r.seal(merged, id); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE notify_channels SET name = ?, type = ?, enabled = ?,
			min_severity = ?, kinds = ?, quiet_start = ?, quiet_end = ?, send_resolved = ?,
			config = ?, secret = ?, updated_at = ? WHERE id = ?`,
			w.Name, w.Type, boolInt(w.Enabled), string(w.MinSeverity), mustJSON(w.Kinds),
			w.QuietStart, w.QuietEnd, boolInt(w.SendResolved), mustJSON(w.Config), newBlob,
			fmtTime(r.now()), id)
		if err != nil {
			return mapNameErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return Channel{}, err
	}
	return r.GetChannel(ctx, id)
}

// DeleteChannel removes a channel and its delivery rows. Deliveries are
// deleted explicitly because foreign keys are not enforced on this connection.
func (r *Repo) DeleteChannel(ctx context.Context, id int64) error {
	return r.st.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM notify_deliveries WHERE channel_id = ?`, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM notify_channels WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// GetChannel returns one channel without its secret values.
func (r *Repo) GetChannel(ctx context.Context, id int64) (Channel, error) {
	return r.channelByID(ctx, r.st.DB(), id)
}

// channelByID reads one channel through q, which may be a transaction.
func (r *Repo) channelByID(ctx context.Context, q querier, id int64) (Channel, error) {
	c, err := r.scanChannel(q.QueryRowContext(ctx, `SELECT `+channelCols+` FROM notify_channels WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	return c, err
}

// ListChannels returns every channel, oldest first.
func (r *Repo) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := r.st.DB().QueryContext(ctx, `SELECT `+channelCols+` FROM notify_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Channel{}
	for rows.Next() {
		c, err := r.scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func nonEmpty(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Only maps, slices and strings reach here; they always marshal.
		panic("notify: marshal: " + err.Error())
	}
	return string(b)
}

func mapNameErr(err error) error {
	if strings.Contains(err.Error(), "UNIQUE constraint failed: notify_channels.name") {
		return ErrNameTaken
	}
	return err
}
