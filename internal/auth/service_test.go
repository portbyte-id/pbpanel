package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

const (
	testUser     = "admin"
	testPassword = "a long enough password"
)

func newService(t *testing.T, st *store.Store, clock Clock) *Service {
	t.Helper()
	return NewService(st, clock,
		func() time.Duration { return testIdle },
		func() int { return testMaxAttempts },
		func() time.Duration { return testLockout },
	)
}

func seeded(t *testing.T) (*Service, *store.Store, *fakeClock) {
	t.Helper()
	st := openStore(t)
	clock := newFakeClock()
	svc := newService(t, st, clock)
	if err := svc.SetPassword(context.Background(), testUser, testPassword); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	return svc, st, clock
}

func TestSetPasswordThenVerifyLogin(t *testing.T) {
	svc, _, _ := seeded(t)
	ctx := context.Background()

	ok, err := svc.HasAdmin(ctx)
	if err != nil {
		t.Fatalf("HasAdmin: %v", err)
	}
	if !ok {
		t.Fatal("HasAdmin reported false after SetPassword")
	}

	session, err := svc.VerifyLogin(ctx, testUser, testPassword, "203.0.113.7", "test-agent")
	if err != nil {
		t.Fatalf("VerifyLogin: %v", err)
	}
	if session.ID == "" || session.CSRFToken == "" {
		t.Fatal("VerifyLogin returned a session without an ID or CSRF token")
	}

	// The returned session must be usable, which is what proves it was stored.
	if _, err := svc.Sessions().Lookup(ctx, session.ID); err != nil {
		t.Fatalf("Lookup of the session VerifyLogin returned: %v", err)
	}
}

// A lockout holds until the operator clears it from the CLI. The CLI is another
// process with its own Service on the same database, so the unlock goes through a
// second Service here: the running one must notice it without a restart.
func TestUnlockLoginClearsALockoutFromAnotherService(t *testing.T) {
	svc, st, clock := seeded(t)
	cli := newService(t, st, clock)
	ctx := context.Background()
	const ip = "203.0.113.7"

	for range testMaxAttempts {
		_, _ = svc.VerifyLogin(ctx, testUser, "wrong password", ip, "agent")
	}
	if _, err := svc.VerifyLogin(ctx, testUser, testPassword, ip, "agent"); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("VerifyLogin while locked = %v, want ErrLockedOut", err)
	}

	clock.advance(time.Second)
	if err := cli.UnlockLogin(ctx); err != nil {
		t.Fatalf("UnlockLogin: %v", err)
	}
	if _, err := svc.VerifyLogin(ctx, testUser, testPassword, ip, "agent"); err != nil {
		t.Fatalf("VerifyLogin after the unlock = %v, want success", err)
	}
}

// An unlock is one-shot: failures after it count and lock again, and the old
// marker must not clear them a second time.
func TestUnlockLoginDoesNotReapplyAnOldMarker(t *testing.T) {
	svc, st, clock := seeded(t)
	cli := newService(t, st, clock)
	ctx := context.Background()
	const ip = "203.0.113.8"

	clock.advance(time.Second)
	if err := cli.UnlockLogin(ctx); err != nil {
		t.Fatalf("UnlockLogin: %v", err)
	}
	for range testMaxAttempts {
		_, _ = svc.VerifyLogin(ctx, testUser, "wrong password", ip, "agent")
	}
	if _, err := svc.VerifyLogin(ctx, testUser, testPassword, ip, "agent"); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("VerifyLogin after fresh failures = %v, want ErrLockedOut", err)
	}
}

func TestHasAdminOnAnEmptyDatabase(t *testing.T) {
	st := openStore(t)
	svc := newService(t, st, newFakeClock())

	ok, err := svc.HasAdmin(context.Background())
	if err != nil {
		t.Fatalf("HasAdmin: %v", err)
	}
	if ok {
		t.Fatal("HasAdmin reported true on a database where install has not run")
	}
}

func TestVerifyLoginRejectsWrongPassword(t *testing.T) {
	svc, _, _ := seeded(t)

	_, err := svc.VerifyLogin(context.Background(), testUser, "the wrong password", "203.0.113.7", "test-agent")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("VerifyLogin with a wrong password = %v, want ErrInvalidCredentials", err)
	}
}

// The error must not say which half was wrong: a distinguishable "no such user"
// would confirm the admin username to anyone who asked.
func TestVerifyLoginRejectsUnknownUser(t *testing.T) {
	svc, _, _ := seeded(t)

	_, err := svc.VerifyLogin(context.Background(), "nobody", testPassword, "203.0.113.7", "test-agent")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("VerifyLogin with an unknown user = %v, want ErrInvalidCredentials", err)
	}
}

func TestVerifyLoginRejectsEmptyUsername(t *testing.T) {
	svc, _, _ := seeded(t)

	_, err := svc.VerifyLogin(context.Background(), "", testPassword, "203.0.113.7", "test-agent")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("VerifyLogin with an empty username = %v, want ErrInvalidCredentials", err)
	}
}

// The timing requirement, tested by observation rather than by stopwatch: an
// unknown user must still run a full verification, against the dummy hash. A
// wall-clock assertion would be flaky on a shared machine and would not say
// which code path ran.
func TestUnknownUserStillRunsAVerification(t *testing.T) {
	svc, _, _ := seeded(t)

	type attempt struct {
		user     string
		password string
	}
	cases := []attempt{
		{testUser, "wrong password"},
		{"nobody", testPassword},
		{"nobody", "wrong password"},
	}

	for i, tc := range cases {
		t.Run(tc.user+"/"+tc.password, func(t *testing.T) {
			var (
				calls    int
				verified string
			)
			svc.verify = func(encoded, password string) (bool, error) {
				calls++
				verified = encoded
				return Verify(encoded, password)
			}

			// A fresh IP per case: the throttle is not what this test is about,
			// and sharing one would lock the third attempt out.
			ip := fmt.Sprintf("203.0.113.%d", i+1)
			_, err := svc.VerifyLogin(context.Background(), tc.user, tc.password, ip, "test-agent")
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("VerifyLogin = %v, want ErrInvalidCredentials", err)
			}
			if calls != 1 {
				t.Fatalf("verification ran %d times, want exactly 1", calls)
			}
			if verified == "" {
				t.Fatal("verification ran with an empty hash: the unknown-user path skipped the dummy hash")
			}

			// The known-user failure must verify the stored hash and the
			// unknown-user failure the dummy one; both are real argon2 work.
			dummy, err := dummyHash()
			if err != nil {
				t.Fatalf("dummyHash: %v", err)
			}
			if tc.user == testUser && verified == dummy {
				t.Fatal("a known user was verified against the dummy hash")
			}
			if tc.user != testUser && verified != dummy {
				t.Fatal("an unknown user was not verified against the dummy hash")
			}
		})
	}
}

func TestVerifyLoginLocksOutAfterMaxAttempts(t *testing.T) {
	svc, _, clock := seeded(t)
	ctx := context.Background()
	const ip = "203.0.113.7"

	for i := 1; i < testMaxAttempts; i++ {
		_, err := svc.VerifyLogin(ctx, testUser, "wrong", ip, "test-agent")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d = %v, want ErrInvalidCredentials", i, err)
		}
	}

	_, err := svc.VerifyLogin(ctx, testUser, "wrong", ip, "test-agent")
	if !errors.Is(err, ErrLockedOut) {
		t.Fatalf("attempt %d = %v, want ErrLockedOut", testMaxAttempts, err)
	}

	// Even the correct password is refused while locked: otherwise the lockout
	// would only slow down guessing, not stop it.
	if _, err := svc.VerifyLogin(ctx, testUser, testPassword, ip, "test-agent"); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("correct password during lockout = %v, want ErrLockedOut", err)
	}

	clock.advance(testLockout)
	session, err := svc.VerifyLogin(ctx, testUser, testPassword, ip, "test-agent")
	if err != nil {
		t.Fatalf("correct password after the lockout expired: %v", err)
	}
	if session.ID == "" {
		t.Fatal("no session after recovering from the lockout")
	}
}

func TestVerifyLoginSuccessResetsTheCounter(t *testing.T) {
	svc, _, _ := seeded(t)
	ctx := context.Background()
	const ip = "203.0.113.7"

	// One failure short of the limit, then a success, then the same number of
	// failures again: the second batch must not lock, so the success reset it.
	for range testMaxAttempts - 1 {
		if _, err := svc.VerifyLogin(ctx, testUser, "wrong", ip, "test-agent"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("VerifyLogin = %v, want ErrInvalidCredentials", err)
		}
	}
	if _, err := svc.VerifyLogin(ctx, testUser, testPassword, ip, "test-agent"); err != nil {
		t.Fatalf("VerifyLogin: %v", err)
	}
	for i := range testMaxAttempts - 1 {
		if _, err := svc.VerifyLogin(ctx, testUser, "wrong", ip, "test-agent"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failure %d after a success = %v, want ErrInvalidCredentials", i+1, err)
		}
	}
}

// Decision 5, end to end: setting a new password invalidates every session that
// existed before it, including the one making the request.
func TestChangePasswordRevokesEverySession(t *testing.T) {
	svc, _, _ := seeded(t)
	ctx := context.Background()

	first, err := svc.VerifyLogin(ctx, testUser, testPassword, "203.0.113.7", "a")
	if err != nil {
		t.Fatalf("VerifyLogin: %v", err)
	}
	second, err := svc.VerifyLogin(ctx, testUser, testPassword, "203.0.113.8", "b")
	if err != nil {
		t.Fatalf("VerifyLogin: %v", err)
	}

	const newPassword = "a different long password"
	if err := svc.SetPassword(ctx, testUser, newPassword); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	for name, id := range map[string]string{"first": first.ID, "second": second.ID} {
		if _, err := svc.Sessions().Lookup(ctx, id); !errors.Is(err, ErrNoSession) {
			t.Fatalf("the %s session survived the password change: %v", name, err)
		}
	}

	if _, err := svc.VerifyLogin(ctx, testUser, testPassword, "203.0.113.7", "c"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("the old password still works: %v", err)
	}
	if _, err := svc.VerifyLogin(ctx, testUser, newPassword, "203.0.113.7", "c"); err != nil {
		t.Fatalf("the new password does not work: %v", err)
	}
}

// SetPassword is the install and reset-password path, and it revokes sessions
// too, so a reset from the CLI cannot leave a live cookie behind.
func TestSetPasswordReplacesTheSingleRow(t *testing.T) {
	svc, st, _ := seeded(t)
	ctx := context.Background()

	if err := svc.SetPassword(ctx, "someone-else", "another password"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	var rows int
	if err := st.DB().QueryRow(`SELECT count(*) FROM admin`).Scan(&rows); err != nil {
		t.Fatalf("count admin: %v", err)
	}
	if rows != 1 {
		t.Fatalf("admin has %d rows, want 1", rows)
	}

	if _, err := svc.VerifyLogin(ctx, "someone-else", "another password", "203.0.113.7", "a"); err != nil {
		t.Fatalf("VerifyLogin with the new account: %v", err)
	}
	if _, err := svc.VerifyLogin(ctx, testUser, testPassword, "203.0.113.8", "b"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("the old account still logs in: %v", err)
	}
}

// A stored hash this package cannot read is a broken row. It must read as a
// failed login, not a 500: the caller's error handling should not depend on
// whether the database happens to be intact.
func TestVerifyLoginTreatsACorruptStoredHashAsAFailure(t *testing.T) {
	svc, st, _ := seeded(t)
	ctx := context.Background()

	if _, err := st.DB().Exec(`UPDATE admin SET password_hash = 'not-a-phc-string' WHERE id = 1`); err != nil {
		t.Fatalf("corrupt the stored hash: %v", err)
	}

	_, err := svc.VerifyLogin(ctx, testUser, testPassword, "203.0.113.7", "test-agent")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("VerifyLogin with a corrupt hash = %v, want ErrInvalidCredentials", err)
	}
}

func TestSetPasswordRejectsEmptyValues(t *testing.T) {
	st := openStore(t)
	svc := newService(t, st, newFakeClock())
	ctx := context.Background()

	if err := svc.SetPassword(ctx, "", testPassword); err == nil {
		t.Fatal("SetPassword accepted an empty username")
	}
	if err := svc.SetPassword(ctx, testUser, ""); err == nil {
		t.Fatal("SetPassword accepted an empty password")
	}
	if err := svc.SetPassword(ctx, testUser, ""); err == nil {
		t.Fatal("SetPassword accepted an empty password")
	}
}

// A rename is a credential change, so it revokes sessions like a password
// change does: a name that no longer exists must not leave a cookie working.
func TestSetUsernameRenamesAndRevokesEverySession(t *testing.T) {
	svc, _, _ := seeded(t)
	ctx := context.Background()

	first, err := svc.VerifyLogin(ctx, testUser, testPassword, "203.0.113.7", "a")
	if err != nil {
		t.Fatalf("VerifyLogin: %v", err)
	}

	const newUser = "operator"
	if err := svc.SetUsername(ctx, newUser); err != nil {
		t.Fatalf("SetUsername: %v", err)
	}

	if _, err := svc.Sessions().Lookup(ctx, first.ID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("the session survived the rename: %v", err)
	}

	name, err := svc.Username(ctx)
	if err != nil {
		t.Fatalf("Username: %v", err)
	}
	if name != newUser {
		t.Fatalf("Username = %q, want %q", name, newUser)
	}

	if _, err := svc.VerifyLogin(ctx, testUser, testPassword, "203.0.113.8", "b"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("the old username still logs in: %v", err)
	}
	if _, err := svc.VerifyLogin(ctx, newUser, testPassword, "203.0.113.9", "c"); err != nil {
		t.Fatalf("the new username does not log in: %v", err)
	}
}

// The name is trimmed the same way SetPassword trims it, so a stray space from
// the form does not become part of the credential.
func TestSetUsernameTrimsWhitespace(t *testing.T) {
	svc, _, _ := seeded(t)

	if err := svc.SetUsername(context.Background(), "  operator  "); err != nil {
		t.Fatalf("SetUsername: %v", err)
	}
	name, err := svc.Username(context.Background())
	if err != nil {
		t.Fatalf("Username: %v", err)
	}
	if name != "operator" {
		t.Fatalf("Username = %q, want %q", name, "operator")
	}
}

func TestSetUsernameRejectsEmptyAndMissingAccount(t *testing.T) {
	ctx := context.Background()

	st := openStore(t)
	svc := newService(t, st, newFakeClock())
	if err := svc.SetUsername(ctx, "   "); err == nil {
		t.Fatal("SetUsername accepted an empty username")
	}
	// No admin row at all: install has not run, so there is nothing to rename.
	if err := svc.SetUsername(ctx, "operator"); err == nil {
		t.Fatal("SetUsername renamed an account that does not exist")
	}
}
