package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// ErrInvalidCredentials is the single failure returned by VerifyLogin. It does
// not say whether the username was wrong or the password was: distinguishing
// them would confirm the admin username to anyone who asked.
var ErrInvalidCredentials = errors.New("auth: invalid credentials")

// ErrLockedOut is returned while the throttle's lockout window is open. It is
// deliberately distinct from ErrInvalidCredentials so the HTTP layer can answer
// 429 with Retry-After instead of 401; the lockout state is already visible to
// the client through its own requests.
var ErrLockedOut = errors.New("auth: too many failed logins")

// LockoutError carries the moment a lockout ends, which the HTTP layer turns
// into Retry-After. It unwraps to ErrLockedOut, so callers that only care about
// the class of failure use errors.Is.
type LockoutError struct {
	RetryAt time.Time
}

func (e *LockoutError) Error() string { return ErrLockedOut.Error() }

// Unwrap lets errors.Is(err, ErrLockedOut) match.
func (e *LockoutError) Unwrap() error { return ErrLockedOut }

func lockoutError(retryAt time.Time) error { return &LockoutError{RetryAt: retryAt} }

// dummyHash is verified against when the username does not match, so an unknown
// user costs the same argon2 work as a known one.
//
// It is built lazily rather than in an init function: an init panic would take
// down commands that never authenticate (`panel --version`), and paying for one
// argon2 derivation at every CLI start to answer a version flag is waste. The
// password is random and thrown away, so nothing can log in as it.
var dummyHash = sync.OnceValues(func() (string, error) {
	secret, err := randomToken(32)
	if err != nil {
		return "", fmt.Errorf("auth: generate dummy hash: %w", err)
	}
	return Hash(secret)
})

// Service is the authentication entry point: the admin password, login, and the
// sessions a login creates.
type Service struct {
	st       *store.Store
	clock    Clock
	sessions *SessionStore
	throttle *Throttle

	// verify is the password check, held as a field so a test can prove the
	// unknown-user path still runs a verification without timing the wall
	// clock. Production always uses Verify.
	verify func(encoded, password string) (bool, error)

	// loginMu makes check -> verify -> record-failure one step. Without it
	// the throttle is checked before any of N concurrent guesses has been
	// counted, so all N pass and all N are verified: the lockout would bound
	// sequential guesses only, and every parallel request would also hold a
	// 64 MiB argon2 derivation. One admin logs in rarely, so serialising
	// logins costs nothing real.
	loginMu sync.Mutex

	// unlockSeen is the newest unlock marker already applied; guarded by loginMu.
	unlockSeen time.Time
}

// NewService wires the package together. idle, maxAttempts and lockout are
// functions because they read live settings (`session.idle_timeout`,
// `login.max_attempts`, `login.lockout`); a value captured here would freeze at
// the first read.
func NewService(st *store.Store, clock Clock, idle func() time.Duration, maxAttempts func() int, lockout func() time.Duration) *Service {
	return &Service{
		st:       st,
		clock:    clock,
		sessions: NewSessionStore(st, clock, idle),
		throttle: NewThrottle(clock, maxAttempts, lockout),
		verify:   Verify,
	}
}

// OfflineService builds the service the CLI uses: `panel install` creating the
// first account and `panel reset-password` replacing it. Only SetPassword,
// HasAdmin and Username are called on it, and all three work straight off the
// store.
//
// The three login policy functions belong to login, which the CLI never
// performs, so they return zero: a login attempted through this service fails
// closed (a session created here would expire at once) rather than running with
// limits nobody configured.
func OfflineService(st *store.Store) *Service {
	return NewService(st, SystemClock{},
		func() time.Duration { return 0 },
		func() int { return 0 },
		func() time.Duration { return 0 },
	)
}

// Sessions exposes the session store for the HTTP layer.
func (s *Service) Sessions() *SessionStore { return s.sessions }

// Throttle exposes the login throttle for the HTTP layer.
func (s *Service) Throttle() *Throttle { return s.throttle }

// SetPassword stores the admin account: username and a fresh argon2id hash of
// password, revoking every existing session in the same transaction. It is the
// install and `panel reset-password` path.
//
// Revoking here rather than leaving it to the caller is what makes the
// invariant hold by construction: a new password and a live old session must
// never both exist, and a caller that forgot the second half would silently
// leave that hole open (decision 5).
func (s *Service) SetPassword(ctx context.Context, username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("auth: username is empty")
	}
	if password == "" {
		return errors.New("auth: password is empty")
	}

	hash, err := Hash(password)
	if err != nil {
		return err
	}
	now := formatTime(s.clock.Now())

	return s.st.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO admin (id, username, password_hash, updated_at) VALUES (1, ?, ?, ?)
			 ON CONFLICT (id) DO UPDATE SET username = excluded.username,
			                                password_hash = excluded.password_hash,
			                                updated_at = excluded.updated_at`,
			username, hash, now,
		); err != nil {
			return fmt.Errorf("auth: set password: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ? WHERE revoked_at IS NULL`, now,
		); err != nil {
			return fmt.Errorf("auth: revoke sessions: %w", err)
		}
		return nil
	})
}

// SetUsername renames the admin account and revokes every existing session in
// the same transaction. A rename changes the half of the credential the login
// page asks for, so it carries the rule SetPassword already enforces: no session
// that predates a credential change stays live.
func (s *Service) SetUsername(ctx context.Context, username string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("auth: username is empty")
	}
	now := formatTime(s.clock.Now())

	return s.st.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE admin SET username = ?, updated_at = ? WHERE id = 1`,
			username, now,
		)
		if err != nil {
			return fmt.Errorf("auth: set username: %w", err)
		}
		renamed, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("auth: set username: %w", err)
		}
		if renamed == 0 {
			return errors.New("auth: no admin account to rename")
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ? WHERE revoked_at IS NULL`, now,
		); err != nil {
			return fmt.Errorf("auth: revoke sessions: %w", err)
		}
		return nil
	})
}

// VerifyLogin checks username and password. On success it opens a session for
// ip and userAgent and returns it; the caller sets the cookie and hands the CSRF
// token to the client.
//
// ip must be the TCP peer address as observed by the listener. It is never read
// from X-Forwarded-For or any other request header: those are attacker-supplied,
// and trusting one would let a client pick a fresh counter for every attempt and
// bypass the throttle entirely.
func (s *Service) VerifyLogin(ctx context.Context, username, password, ip, userAgent string) (Session, error) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()

	if err := s.applyUnlock(ctx); err != nil {
		return Session{}, err
	}
	retryAt, locked := s.throttle.Check(ip)
	if locked {
		return Session{}, lockoutError(retryAt)
	}

	storedUser, storedHash, err := s.admin(ctx)
	if err != nil {
		return Session{}, err
	}

	userMatches := storedUser != "" && constantTimeEqual(storedUser, strings.TrimSpace(username))

	// Always verify something. When the user does not exist, the dummy hash
	// makes this call do the same argon2 work, so response time does not reveal
	// whether the username was right.
	dummy, err := dummyHash()
	if err != nil {
		return Session{}, err
	}
	encoded := dummy
	if userMatches {
		encoded = storedHash
	}
	passwordMatches, err := s.verify(encoded, password)
	if err != nil {
		// A stored hash this package cannot read is a broken row, not a reason
		// to hand the caller a 500: it is still a failed login, and counting it
		// as one keeps the throttle and the error shape consistent.
		if userMatches {
			passwordMatches = false
		} else {
			return Session{}, fmt.Errorf("auth: verify password: %w", err)
		}
	}

	if !userMatches || !passwordMatches {
		if until, locked := s.throttle.Fail(ip); locked {
			return Session{}, lockoutError(until)
		}
		return Session{}, ErrInvalidCredentials
	}

	session, err := s.sessions.Create(ctx, ip, userAgent)
	if err != nil {
		return Session{}, err
	}
	s.throttle.Reset(ip)
	return session, nil
}

// unlockKey is the settings-table row `pbp unlock-login` writes. It is not a
// registered setting, so the Settings page never lists it and `pbp set` cannot
// touch it.
const unlockKey = "login.unlocked_at"

// UnlockLogin asks the running daemon to forget every failed-login counter and
// lockout. The counters live in the daemon's memory, so the CLI, a separate
// process, leaves a timestamp in the database and the daemon clears them at its
// next login attempt. Only someone who can open the database, which is root on
// the host, can write it.
func (s *Service) UnlockLogin(ctx context.Context) error {
	return s.st.SaveSetting(ctx, unlockKey, formatTime(s.clock.Now()))
}

// LoginChallengeRequired reports whether ip must solve the browser check before
// its next login attempt. It applies a pending unlock first, as VerifyLogin does,
// so `pbp unlock-login` lifts the check on the very next request as well.
func (s *Service) LoginChallengeRequired(ctx context.Context, ip string) (bool, error) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()

	if err := s.applyUnlock(ctx); err != nil {
		return false, err
	}
	return s.throttle.Challenged(ip), nil
}

// applyUnlock clears the throttle when UnlockLogin has run since the last time
// this service looked. The caller holds loginMu.
func (s *Service) applyUnlock(ctx context.Context) error {
	raw, ok, err := s.st.LoadSetting(ctx, unlockKey)
	if err != nil {
		return fmt.Errorf("auth: read unlock marker: %w", err)
	}
	if !ok {
		return nil
	}
	at, err := parseTime(raw)
	if err != nil {
		// A marker this package did not write cannot be trusted to mean anything;
		// ignoring it fails closed, the lockout stays.
		return nil
	}
	if at.After(s.unlockSeen) {
		s.throttle.Clear()
		s.unlockSeen = at
	}
	return nil
}

// admin reads the single admin row. An empty database means install has not run
// yet, which is a valid state to report as "no admin" rather than an error: the
// caller decides whether that is a 401 or a startup failure.
func (s *Service) admin(ctx context.Context) (username, hash string, err error) {
	err = s.st.DB().QueryRowContext(ctx,
		`SELECT username, password_hash FROM admin WHERE id = 1`,
	).Scan(&username, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("auth: read admin: %w", err)
	}
	return username, hash, nil
}

// HasAdmin reports whether install has created the admin account.
func (s *Service) HasAdmin(ctx context.Context) (bool, error) {
	username, _, err := s.admin(ctx)
	if err != nil {
		return false, err
	}
	return username != "", nil
}

// Username returns the admin account's username, or "" before install has
// created one. `panel reset-password` keeps the existing username and needs the
// name to print it.
func (s *Service) Username(ctx context.Context) (string, error) {
	username, _, err := s.admin(ctx)
	if err != nil {
		return "", err
	}
	return username, nil
}
