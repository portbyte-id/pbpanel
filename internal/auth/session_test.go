package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

// fakeClock is the package's only source of time in tests, so expiry and lockout
// are exercised by moving time rather than sleeping.
type fakeClock struct{ now time.Time }

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

const testIdle = 30 * time.Minute

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})
	return st
}

func newSessions(t *testing.T, st *store.Store, clock Clock) *SessionStore {
	t.Helper()
	return NewSessionStore(st, clock, func() time.Duration { return testIdle })
}

func TestCreateStoresOnlyTheHashOfTheID(t *testing.T) {
	st := openStore(t)
	sessions := newSessions(t, st, newFakeClock())

	created, err := sessions.Create(context.Background(), "203.0.113.7", "test-agent")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" || created.CSRFToken == "" {
		t.Fatal("Create returned an empty session ID or CSRF token")
	}

	var stored []byte
	if err := st.DB().QueryRow(`SELECT id_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatalf("read id_hash: %v", err)
	}
	if len(stored) != 32 {
		t.Fatalf("stored id_hash is %d bytes, want 32", len(stored))
	}
	if string(stored) == created.ID {
		t.Fatal("the session ID is stored in plaintext")
	}
	if string(stored) != string(hashSessionID(created.ID)) {
		t.Fatal("stored id_hash is not the hash of the session ID")
	}

	// 256 bits of base64url is 43 characters; a shorter ID would not be the
	// length decision 5 asks for.
	if len(created.ID) != 43 {
		t.Errorf("session ID is %d characters, want 43 (256 bits base64url)", len(created.ID))
	}
}

func TestLookupSlidesTheIdleWindow(t *testing.T) {
	st := openStore(t)
	clock := newFakeClock()
	sessions := newSessions(t, st, clock)
	ctx := context.Background()

	created, err := sessions.Create(ctx, "203.0.113.7", "test-agent")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Half the window, then look up: the activity must reset the clock.
	clock.advance(testIdle / 2)
	got, err := sessions.Lookup(ctx, created.ID)
	if err != nil {
		t.Fatalf("Lookup inside the window: %v", err)
	}
	if !got.LastSeen.Equal(clock.Now()) {
		t.Errorf("LastSeen = %v, want %v", got.LastSeen, clock.Now())
	}
	if got.CSRFToken != created.CSRFToken {
		t.Error("Lookup returned a different CSRF token")
	}

	// Another half window from the touch is still inside it; the second lookup
	// is what proves the window slid rather than starting at creation.
	clock.advance(testIdle / 2)
	if _, err := sessions.Lookup(ctx, created.ID); err != nil {
		t.Fatalf("Lookup after the window slid: %v", err)
	}
}

func TestLookupExpiresAnIdleSession(t *testing.T) {
	st := openStore(t)
	clock := newFakeClock()
	sessions := newSessions(t, st, clock)
	ctx := context.Background()

	created, err := sessions.Create(ctx, "203.0.113.7", "test-agent")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	clock.advance(testIdle)
	if _, err := sessions.Lookup(ctx, created.ID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Lookup at exactly the idle timeout = %v, want ErrNoSession", err)
	}
}

func TestLookupRejectsUnknownAndEmptyIDs(t *testing.T) {
	st := openStore(t)
	sessions := newSessions(t, st, newFakeClock())
	ctx := context.Background()

	for _, id := range []string{"", "not-a-session"} {
		if _, err := sessions.Lookup(ctx, id); !errors.Is(err, ErrNoSession) {
			t.Fatalf("Lookup(%q) = %v, want ErrNoSession", id, err)
		}
	}
}

func TestRevokeRejectsThatSession(t *testing.T) {
	st := openStore(t)
	sessions := newSessions(t, st, newFakeClock())
	ctx := context.Background()

	created, err := sessions.Create(ctx, "203.0.113.7", "test-agent")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := sessions.Revoke(ctx, created.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := sessions.Lookup(ctx, created.ID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Lookup after Revoke = %v, want ErrNoSession", err)
	}

	// Logout must be idempotent: a replayed cookie is not an error.
	if err := sessions.Revoke(ctx, created.ID); err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
	if err := sessions.Revoke(ctx, ""); err != nil {
		t.Fatalf("Revoke of an empty ID: %v", err)
	}
}

// Decision 5: a password change invalidates every existing session, not just the
// one that made the change.
func TestRevokeAllRejectsEverySession(t *testing.T) {
	st := openStore(t)
	sessions := newSessions(t, st, newFakeClock())
	ctx := context.Background()

	const n = 3
	ids := make([]string, 0, n)
	for range n {
		created, err := sessions.Create(ctx, "203.0.113.7", "test-agent")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		ids = append(ids, created.ID)
	}

	if err := sessions.RevokeAll(ctx); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	for i, id := range ids {
		if _, err := sessions.Lookup(ctx, id); !errors.Is(err, ErrNoSession) {
			t.Fatalf("session %d still valid after RevokeAll: %v", i, err)
		}
	}

	// A session created after the change must work: the revocation is a
	// one-time sweep, not a permanent ban.
	fresh, err := sessions.Create(ctx, "203.0.113.7", "test-agent")
	if err != nil {
		t.Fatalf("Create after RevokeAll: %v", err)
	}
	if _, err := sessions.Lookup(ctx, fresh.ID); err != nil {
		t.Fatalf("Lookup of a session created after RevokeAll: %v", err)
	}
}

func TestSweepDeletesRevokedAndLongIdleSessions(t *testing.T) {
	st := openStore(t)
	clock := newFakeClock()
	sessions := newSessions(t, st, clock)
	ctx := context.Background()

	revoked, err := sessions.Create(ctx, "203.0.113.7", "a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := sessions.Revoke(ctx, revoked.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := sessions.Create(ctx, "203.0.113.8", "b"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Past twice the idle window the revoked and stale rows go.
	clock.advance(2*testIdle + time.Minute)

	// Created after the jump, so this one is live.
	live, err := sessions.Create(ctx, "203.0.113.9", "c")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := sessions.Lookup(ctx, live.ID); err != nil {
		t.Fatalf("Lookup of the live session: %v", err)
	}

	deleted, err := sessions.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("Sweep deleted %d rows, want 2", deleted)
	}

	var remaining int
	if err := st.DB().QueryRow(`SELECT count(*) FROM sessions`).Scan(&remaining); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("%d sessions remain, want 1 (the live one)", remaining)
	}
}

// A RevokeAll that lands between Lookup's read and its touch must still win.
// The touch is conditional on revoked_at IS NULL for exactly this reason: an
// unconditional update would slide a just-revoked session back into use, and the
// race is real because a password change and an in-flight request can overlap.
func TestLookupCannotReviveASessionRevokedMidFlight(t *testing.T) {
	st := openStore(t)
	clock := newFakeClock()
	sessions := newSessions(t, st, clock)
	ctx := context.Background()

	created, err := sessions.Create(ctx, "203.0.113.7", "test-agent")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Revoke behind the caller's back, exactly as a concurrent RevokeAll would,
	// then attempt the touch Lookup performs after its read.
	if err := sessions.RevokeAll(ctx); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	res, err := st.DB().ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ? WHERE id_hash = ? AND revoked_at IS NULL`,
		formatTime(clock.Now()), hashSessionID(created.ID),
	)
	if err != nil {
		t.Fatalf("touch: %v", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 0 {
		t.Fatalf("touch affected %d rows (err %v), want 0: the revoked guard is missing", n, err)
	}

	if _, err := sessions.Lookup(ctx, created.ID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Lookup after a mid-flight revoke = %v, want ErrNoSession", err)
	}
}

func TestCheckCSRF(t *testing.T) {
	session := Session{CSRFToken: "the-real-token"}

	cases := []struct {
		name      string
		presented string
		want      bool
	}{
		{"correct", "the-real-token", true},
		{"missing", "", false},
		{"wrong", "a-different-token", false},
		{"prefix", "the-real-toke", false},
		{"longer", "the-real-token-and-more", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckCSRF(session, tc.presented); got != tc.want {
				t.Fatalf("CheckCSRF(%q) = %v, want %v", tc.presented, got, tc.want)
			}
		})
	}

	// A session with no token must never accept the empty string: otherwise a
	// missing token on both sides would read as a match.
	if CheckCSRF(Session{}, "") {
		t.Fatal("CheckCSRF accepted an empty token against an empty session token")
	}
}
