package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// Session is one logged-in browser. ID is the value the client holds; it is
// never stored, and is left empty on sessions read back from the database.
type Session struct {
	ID        string
	CreatedAt time.Time
	LastSeen  time.Time
	IP        string
	UserAgent string
	CSRFToken string
}

// ErrNoSession reports a session ID that is unknown, revoked or idle-expired.
// Callers must not distinguish these cases in a response: which one applies is
// information about the server's state, not about the client.
var ErrNoSession = errors.New("auth: no valid session")

const (
	sessionIDBytes = 32 // 256 bits, decision 5
	csrfTokenBytes = 32
)

// sessionTTL is the idle window. Every read updates last_seen_at, so the clock
// restarts on each request.
type sessionTTL func() time.Duration

// SessionStore owns the sessions table.
type SessionStore struct {
	st    *store.Store
	clock Clock
	idle  sessionTTL
}

// NewSessionStore returns a store reading the idle timeout through idle. The
// caller wires idle to the `session.idle_timeout` setting; passing a function
// rather than a duration keeps a settings change live without a restart.
func NewSessionStore(st *store.Store, clock Clock, idle sessionTTL) *SessionStore {
	return &SessionStore{st: st, clock: clock, idle: idle}
}

// Create opens a session for ip and userAgent and returns it with its ID and
// CSRF token filled in. The caller puts ID in the cookie and hands the token to
// the client; the database keeps only the hash of the ID.
func (s *SessionStore) Create(ctx context.Context, ip, userAgent string) (Session, error) {
	id, err := randomToken(sessionIDBytes)
	if err != nil {
		return Session{}, err
	}
	csrf, err := randomToken(csrfTokenBytes)
	if err != nil {
		return Session{}, err
	}

	now := s.clock.Now().UTC()
	_, err = s.st.DB().ExecContext(ctx,
		`INSERT INTO sessions (id_hash, created_at, last_seen_at, ip, user_agent, csrf_token)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hashSessionID(id), formatTime(now), formatTime(now), ip, userAgent, csrf,
	)
	if err != nil {
		return Session{}, fmt.Errorf("auth: create session: %w", err)
	}
	return Session{
		ID:        id,
		CreatedAt: now,
		LastSeen:  now,
		IP:        ip,
		UserAgent: userAgent,
		CSRFToken: csrf,
	}, nil
}

// Lookup resolves a session ID from a cookie, sliding its idle window forward.
// It returns ErrNoSession for an unknown, revoked or expired ID.
func (s *SessionStore) Lookup(ctx context.Context, id string) (Session, error) {
	if id == "" {
		return Session{}, ErrNoSession
	}

	row, err := s.read(ctx, id)
	if err != nil {
		return Session{}, err
	}

	now := s.clock.Now().UTC()
	if now.Sub(row.LastSeen) >= s.idle() {
		return Session{}, ErrNoSession
	}

	// The touch is conditional so a RevokeAll that lands between the read above
	// and this write still wins: without the guard, a request racing a password
	// change could slide a session that was just revoked back into use.
	res, err := s.st.DB().ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ? WHERE id_hash = ? AND revoked_at IS NULL`,
		formatTime(now), hashSessionID(id),
	)
	if err != nil {
		return Session{}, fmt.Errorf("auth: touch session: %w", err)
	}
	touched, err := res.RowsAffected()
	if err != nil {
		return Session{}, fmt.Errorf("auth: touch session: %w", err)
	}
	if touched == 0 {
		return Session{}, ErrNoSession
	}
	row.LastSeen = now
	return row, nil
}

// read loads one session, treating a revoked row as absent. A row with an
// unparseable timestamp is an error rather than a silent miss: it means the
// database was written by something other than this package.
func (s *SessionStore) read(ctx context.Context, id string) (Session, error) {
	var (
		createdAt, lastSeen string
		revokedAt           sql.NullString
		out                 Session
	)
	err := s.st.DB().QueryRowContext(ctx,
		`SELECT created_at, last_seen_at, ip, user_agent, csrf_token, revoked_at
		   FROM sessions WHERE id_hash = ?`, hashSessionID(id),
	).Scan(&createdAt, &lastSeen, &out.IP, &out.UserAgent, &out.CSRFToken, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("auth: read session: %w", err)
	}
	if revokedAt.Valid {
		return Session{}, ErrNoSession
	}

	if out.CreatedAt, err = parseTime(createdAt); err != nil {
		return Session{}, fmt.Errorf("auth: session created_at: %w", err)
	}
	if out.LastSeen, err = parseTime(lastSeen); err != nil {
		return Session{}, fmt.Errorf("auth: session last_seen_at: %w", err)
	}
	return out, nil
}

// Revoke marks one session revoked. Revoking an unknown ID is not an error:
// logout must be idempotent, and a replayed cookie must not learn anything.
func (s *SessionStore) Revoke(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	if _, err := s.st.DB().ExecContext(ctx,
		`UPDATE sessions SET revoked_at = ? WHERE id_hash = ? AND revoked_at IS NULL`,
		formatTime(s.clock.Now().UTC()), hashSessionID(id),
	); err != nil {
		return fmt.Errorf("auth: revoke session: %w", err)
	}
	return nil
}

// RevokeAll revokes every live session. It is what a password change calls
// (decision 5): a password that has been replaced must not leave old cookies
// working, including the one that made the change.
func (s *SessionStore) RevokeAll(ctx context.Context) error {
	if _, err := s.st.DB().ExecContext(ctx,
		`UPDATE sessions SET revoked_at = ? WHERE revoked_at IS NULL`,
		formatTime(s.clock.Now().UTC()),
	); err != nil {
		return fmt.Errorf("auth: revoke all sessions: %w", err)
	}
	return nil
}

// Sweep deletes sessions that are revoked or idle past twice the timeout. The
// doubled window keeps a recently expired session around long enough that a
// retry sees a clean rejection rather than a row that vanished mid-request.
func (s *SessionStore) Sweep(ctx context.Context) (int64, error) {
	cutoff := formatTime(s.clock.Now().UTC().Add(-2 * s.idle()))
	res, err := s.st.DB().ExecContext(ctx,
		`DELETE FROM sessions WHERE revoked_at IS NOT NULL OR last_seen_at < ?`, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("auth: sweep sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("auth: sweep sessions: %w", err)
	}
	return n, nil
}

// CheckCSRF compares a token from the X-CSRF-Token header against the one bound
// to the session. The comparison is constant-time, so a wrong token leaks
// nothing about the right one.
func CheckCSRF(session Session, presented string) bool {
	if session.CSRFToken == "" || presented == "" {
		return false
	}
	return constantTimeEqual(session.CSRFToken, presented)
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// hashSessionID is the at-rest form of a session ID. SHA-256 is enough here:
// the input is 256 random bits, so there is nothing to brute-force, and a keyed
// hash would only add a key to lose.
func hashSessionID(id string) []byte {
	sum := sha256.Sum256([]byte(id))
	return sum[:]
}

// storedTimeLayout is RFC 3339 with a fixed nine-digit fraction. Fixed width
// matters: the sweep compares timestamps as text, and RFC3339Nano drops trailing
// zeros, so "…:04Z" would sort after "…:04.5Z". Always UTC, so the offset is
// always "Z" and the width is constant.
const storedTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(t time.Time) string {
	return t.UTC().Format(storedTimeLayout)
}

// parseTime accepts both the stored layout and anything else RFC 3339 allows,
// so a row written by an earlier build is still readable.
func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}
