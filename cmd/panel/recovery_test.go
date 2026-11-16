package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/auth"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/version"
)

// tempDB creates a database at the default layout under a temporary data
// directory and points the recovery commands at it, so every test below runs the
// real path-building code rather than passing a path straight in.
func tempDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(dataDirEnv, dir)

	path := filepath.Join(dir, "panel.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

// seededDB is tempDB with an admin account and one live session, which is the
// state `reset-password` has to handle.
func seededDB(t *testing.T) (path, username, password string) {
	t.Helper()
	path = tempDB(t)

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	username, password = "operator", "the original password"
	svc := newAuthService(st)
	if err := svc.SetPassword(context.Background(), username, password); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if _, err := svc.Sessions().Create(context.Background(), "203.0.113.7", "test-agent"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return path, username, password
}

func TestDatabasePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(dataDirEnv, dir)
	if got, want := databasePath(), filepath.Join(dir, "panel.db"); got != want {
		t.Fatalf("databasePath() = %q, want %q", got, want)
	}
}

// The default must match decision 6, which is what install writes and what the
// exit gate's `panel info` reads back. The panel's paths are Linux paths, so the
// comparison is on the slash form even when the tests run on Windows.
func TestDatabasePathDefault(t *testing.T) {
	t.Setenv(dataDirEnv, "")
	if got, want := filepath.ToSlash(databasePath()), "/www/panel/data/panel.db"; got != want {
		t.Fatalf("databasePath() = %q, want %q", got, want)
	}
}

func TestInfoPrintsTheExpectedFields(t *testing.T) {
	path := tempDB(t)
	ctx := context.Background()

	// A value the operator changed, so `info` is shown reading the database
	// rather than printing defaults.
	if err := setSetting(ctx, io.Discard, path, "panel.port", "9443"); err != nil {
		t.Fatalf("set panel.port: %v", err)
	}
	if err := setSetting(ctx, io.Discard, path, "panel.bind", "127.0.0.1"); err != nil {
		t.Fatalf("set panel.bind: %v", err)
	}

	var out bytes.Buffer
	if err := info(ctx, &out, databasePath()); err != nil {
		t.Fatalf("info: %v", err)
	}

	for _, want := range []string{
		"version   " + version.Version,
		"database  " + path,
		"bind      127.0.0.1",
		"port      9443",
		"url       https://127.0.0.1:9443",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("info output is missing %q:\n%s", want, out.String())
		}
	}
}

// The report is what a test or the smoke script reads, so its fields are pinned
// separately from the printed text.
func TestReadInfoReportsStoredValues(t *testing.T) {
	tempDB(t)
	ctx := context.Background()

	r, err := readInfo(ctx, databasePath())
	if err != nil {
		t.Fatalf("readInfo: %v", err)
	}
	if r.Port != 8443 || r.Bind != "0.0.0.0" {
		t.Fatalf("readInfo = %+v, want the registry defaults", r)
	}
	if r.URL != "https://0.0.0.0:8443" {
		t.Fatalf("URL = %q, want https://0.0.0.0:8443", r.URL)
	}
	if r.Database != databasePath() {
		t.Fatalf("Database = %q, want %q", r.Database, databasePath())
	}
}

// panelURL must bracket a literal IPv6 address; without it the printed URL is
// not a URL.
func TestPanelURLBracketsIPv6(t *testing.T) {
	if got, want := panelURL("::1", 8443), "https://[::1]:8443"; got != want {
		t.Fatalf("panelURL = %q, want %q", got, want)
	}
}

func TestInfoWithoutADatabaseSaysHowToFixIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(dataDirEnv, dir)

	var out bytes.Buffer
	err := info(context.Background(), &out, databasePath())
	if err == nil {
		t.Fatal("info on a missing database returned no error")
	}
	if !strings.Contains(err.Error(), "panel install") {
		t.Fatalf("error = %q, want it to point at `panel install`", err)
	}
	if out.Len() != 0 {
		t.Fatalf("info printed %q on failure, want nothing", out.String())
	}
}

// The whole point of reset-password: the printed password works, the old one
// does not, and the session that existed before is signed out.
func TestResetPasswordChangesTheHashAndRevokesSessions(t *testing.T) {
	path, username, oldPassword := seededDB(t)
	ctx := context.Background()

	var out bytes.Buffer
	if err := resetPassword(ctx, &out, path); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}

	newPassword := passwordFromOutput(t, out.String())
	if newPassword == oldPassword {
		t.Fatal("the generated password equals the old one")
	}
	if !strings.Contains(out.String(), username) {
		t.Errorf("output does not name the account %q:\n%s", username, out.String())
	}

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	// Verify through the same hash the daemon would read.
	var storedHash string
	if err := st.DB().QueryRowContext(ctx, `SELECT password_hash FROM admin WHERE id = 1`).Scan(&storedHash); err != nil {
		t.Fatalf("read password_hash: %v", err)
	}
	if ok, err := auth.Verify(storedHash, newPassword); err != nil || !ok {
		t.Fatalf("Verify(stored hash, printed password) = %v, %v; want true", ok, err)
	}
	if ok, err := auth.Verify(storedHash, oldPassword); err != nil || ok {
		t.Fatalf("Verify(stored hash, old password) = %v, %v; want false", ok, err)
	}

	// Every session must be revoked: a password change that leaves a cookie
	// working is the hole decision 5 exists to close.
	var live int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM sessions WHERE revoked_at IS NULL`).Scan(&live); err != nil {
		t.Fatalf("count live sessions: %v", err)
	}
	if live != 0 {
		t.Fatalf("%d session(s) are still live after reset-password, want 0", live)
	}
}

// Each run must produce a different password; a generator that repeats is worse
// than none.
func TestResetPasswordGeneratesADifferentPasswordEachTime(t *testing.T) {
	path, _, _ := seededDB(t)
	ctx := context.Background()

	var first, second bytes.Buffer
	if err := resetPassword(ctx, &first, path); err != nil {
		t.Fatalf("first resetPassword: %v", err)
	}
	if err := resetPassword(ctx, &second, path); err != nil {
		t.Fatalf("second resetPassword: %v", err)
	}
	if passwordFromOutput(t, first.String()) == passwordFromOutput(t, second.String()) {
		t.Fatal("two runs generated the same password")
	}
}

// An empty database has no admin row, which is the state after a fresh `panel
// install` that has not been given a password yet.
func TestResetPasswordCreatesTheAdminWhenThereIsNone(t *testing.T) {
	path := tempDB(t)

	var out bytes.Buffer
	if err := resetPassword(context.Background(), &out, path); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}
	if !strings.Contains(out.String(), defaultAdminUser) {
		t.Fatalf("output does not name the default account %q:\n%s", defaultAdminUser, out.String())
	}

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	var username string
	if err := st.DB().QueryRow(`SELECT username FROM admin WHERE id = 1`).Scan(&username); err != nil {
		t.Fatalf("read admin: %v", err)
	}
	if username != defaultAdminUser {
		t.Fatalf("username = %q, want %q", username, defaultAdminUser)
	}
}

func TestGeneratedPasswordShape(t *testing.T) {
	seen := make(map[rune]bool)
	for range 50 {
		p, err := generatePassword()
		if err != nil {
			t.Fatalf("generatePassword: %v", err)
		}
		if len(p) != generatedPasswordLen {
			t.Fatalf("password %q has length %d, want %d", p, len(p), generatedPasswordLen)
		}
		for _, r := range p {
			seen[r] = true
			if !strings.ContainsRune(passwordAlphabet, r) {
				t.Fatalf("password %q contains %q, which is outside the alphabet", p, r)
			}
		}
	}
	// The confusable characters are the ones a console operator misreads, so
	// their absence is a property of the alphabet, not of one draw.
	for _, bad := range []rune{'0', 'O', '1', 'I', 'l'} {
		if strings.ContainsRune(passwordAlphabet, bad) {
			t.Errorf("the alphabet contains the confusable character %q", bad)
		}
	}
	if len(seen) < 2 {
		t.Error("50 draws used fewer than two distinct characters: the generator is not random")
	}
}

func TestSetAcceptsAValidValue(t *testing.T) {
	tempDB(t)
	ctx := context.Background()

	var out bytes.Buffer
	if err := setSetting(ctx, &out, databasePath(), "panel.port", "9443"); err != nil {
		t.Fatalf("setSetting: %v", err)
	}
	if !strings.Contains(out.String(), "panel.port = 9443") {
		t.Fatalf("output = %q, want it to confirm the new value", out.String())
	}
	// panel.port is restart-required, so the notice the brief asks for must show.
	if !strings.Contains(out.String(), "restart") {
		t.Fatalf("output = %q, want the restart-required notice", out.String())
	}

	r, err := readInfo(ctx, databasePath())
	if err != nil {
		t.Fatalf("readInfo: %v", err)
	}
	if r.Port != 9443 {
		t.Fatalf("stored port = %d, want 9443", r.Port)
	}
}

// A key that is not restart-required must not print the notice: the phrasing has
// to come from the registry, not from a blanket warning.
func TestSetDoesNotWarnAboutRestartForALiveKey(t *testing.T) {
	tempDB(t)

	var out bytes.Buffer
	if err := setSetting(context.Background(), &out, databasePath(), "session.idle_timeout", "1h"); err != nil {
		t.Fatalf("setSetting: %v", err)
	}
	if strings.Contains(out.String(), "restart") {
		t.Fatalf("output = %q, want no restart notice for a live key", out.String())
	}
}

func TestSetRejectsAnInvalidValueAndChangesNothing(t *testing.T) {
	tempDB(t)
	ctx := context.Background()

	cases := []struct {
		name  string
		key   string
		value string
		want  string // the validator's own message
	}{
		{name: "port out of range", key: "panel.port", value: "70000", want: "between 1 and 65535"},
		{name: "port not a number", key: "panel.port", value: "https", want: "port number"},
		{name: "bind not an address", key: "panel.bind", value: "localhost", want: "IP address"},
		{name: "unknown key", key: "panel.nope", value: "1", want: "unknown key"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := setSetting(ctx, io.Discard, databasePath(), tc.key, tc.value)
			if err == nil {
				t.Fatalf("setSetting(%s, %s) returned no error", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.want)
			}

			// Nothing written: no row exists for a key the write was refused.
			st, err := store.Open(databasePath())
			if err != nil {
				t.Fatalf("store.Open: %v", err)
			}
			defer func() { _ = st.Close() }()

			var rows int
			if err := st.DB().QueryRowContext(ctx,
				`SELECT count(*) FROM settings WHERE name = ?`, tc.key).Scan(&rows); err != nil {
				t.Fatalf("count settings: %v", err)
			}
			if rows != 0 {
				t.Fatalf("%s has %d stored row(s) after a rejected write, want 0", tc.key, rows)
			}
		})
	}
}

// The exit code is the contract a script depends on, so the invalid value is run
// through the real dispatcher rather than only through setSetting. The root
// check is stubbed out: it is covered by cli_test.go and root_unix_test.go, and
// this test is about the exit code the validator produces.
func TestSetInvalidValueExitsNonZero(t *testing.T) {
	tempDB(t)
	asRoot(t)

	var stdout, stderr bytes.Buffer
	code := runWith(context.Background(),
		[]string{"set", "panel.port", "0"},
		commandSet(&stdout), &stdout, &stderr)
	if code != exitError {
		t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitError, stderr.String())
	}
	if !strings.Contains(stderr.String(), "between 1 and 65535") {
		t.Fatalf("stderr = %q, want the validator's message", stderr.String())
	}
}

func TestSetValidValueExitsZeroThroughTheDispatcher(t *testing.T) {
	tempDB(t)
	asRoot(t)

	var stdout, stderr bytes.Buffer
	code := runWith(context.Background(),
		[]string{"set", "login.max_attempts", "10"},
		commandSet(&stdout), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "login.max_attempts = 10") {
		t.Fatalf("stdout = %q, want the confirmation", stdout.String())
	}
}

// asRoot makes the dispatcher's root check pass for the rest of the test.
func asRoot(t *testing.T) {
	t.Helper()
	restore := euid
	euid = func() int { return 0 }
	t.Cleanup(func() { euid = restore })
}

// TestRecoveryWorksAgainstALiveDaemon is the WAL proof: a second connection
// holding the database open (as the daemon does) must not stop `info` or `set`.
// The daemon is simulated by a store that stays open with a read transaction in
// flight, which is the reader a writer would collide with.
func TestRecoveryWorksAgainstALiveDaemon(t *testing.T) {
	path := tempDB(t)
	ctx := context.Background()

	daemon, err := store.Open(path)
	if err != nil {
		t.Fatalf("open the simulated daemon: %v", err)
	}
	t.Cleanup(func() { _ = daemon.Close() })

	rows, err := daemon.DB().QueryContext(ctx, `SELECT name, value FROM settings`)
	if err != nil {
		t.Fatalf("daemon read: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("daemon read: %v", err)
	}

	// info must answer while the daemon holds its connection.
	var infoOut bytes.Buffer
	if err := info(ctx, &infoOut, path); err != nil {
		t.Fatalf("info against a live daemon: %v", err)
	}
	if !strings.Contains(infoOut.String(), "port") {
		t.Fatalf("info output = %q", infoOut.String())
	}

	// set must write while the daemon holds its connection.
	var setOut bytes.Buffer
	if err := setSetting(ctx, &setOut, path, "panel.port", "9444"); err != nil {
		t.Fatalf("set against a live daemon: %v", err)
	}

	// The daemon sees the write, which is what "picks it up per its own rules"
	// depends on.
	var stored string
	if err := daemon.DB().QueryRowContext(ctx,
		`SELECT value FROM settings WHERE name = 'panel.port'`).Scan(&stored); err != nil {
		t.Fatalf("daemon read of the new value: %v", err)
	}
	if stored != "9444" {
		t.Fatalf("daemon sees panel.port = %q, want 9444", stored)
	}
}

// Two concurrent writers must not fail with SQLITE_BUSY: each is a one-shot CLI
// process that takes the write lock only for its own statement.
func TestConcurrentWritesBothSucceed(t *testing.T) {
	path := tempDB(t)

	const writers = 4
	errs := make(chan error, writers)
	start := make(chan struct{})
	for i := range writers {
		go func() {
			<-start
			errs <- setSetting(context.Background(), io.Discard, path,
				"login.max_attempts", strconv.Itoa(5+i))
		}()
	}
	close(start)

	for range writers {
		if err := <-errs; err != nil {
			t.Fatalf("a concurrent set failed: %v", err)
		}
	}
}

// reset-password must also work against a live daemon: it writes the admin row
// and revokes sessions, both short transactions.
func TestResetPasswordWorksAgainstALiveDaemon(t *testing.T) {
	path, _, _ := seededDB(t)

	daemon, err := store.Open(path)
	if err != nil {
		t.Fatalf("open the simulated daemon: %v", err)
	}
	defer func() { _ = daemon.Close() }()

	var out bytes.Buffer
	if err := resetPassword(context.Background(), &out, path); err != nil {
		t.Fatalf("resetPassword against a live daemon: %v", err)
	}
	if passwordFromOutput(t, out.String()) == "" {
		t.Fatalf("no password in the output:\n%s", out.String())
	}
}

// The key list in `set -h` comes from the registry, so it must name every key
// and mark the restart-required ones.
func TestSetHelpListsRegistryKeys(t *testing.T) {
	help := setHelp()

	for _, want := range []string{
		"panel.port", "panel.bind", "login.max_attempts", "login.lockout",
		"session.idle_timeout", "restart-required", "systemctl restart panel",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("setHelp is missing %q:\n%s", want, help)
		}
	}
}

// `panel set -h` must print the usage line and then the key list, and must not
// touch the database to do it.
func TestSetHelpRunsWithoutADatabase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(dataDirEnv, dir)
	asRoot(t)

	var stdout, stderr bytes.Buffer
	code := runWith(context.Background(), []string{"set", "-h"},
		commandSet(&stdout), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "usage: panel set <key> <value>") {
		t.Fatalf("stdout = %q, want the usage line", stdout.String())
	}
	if !strings.Contains(stdout.String(), "panel.port") {
		t.Fatalf("stdout = %q, want the key list", stdout.String())
	}
}

// passwordFromOutput pulls the generated password out of the block
// resetPassword prints, so a test reads the same value the operator would.
func passwordFromOutput(t *testing.T, output string) string {
	t.Helper()
	for line := range strings.SplitSeq(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) != generatedPasswordLen {
			continue
		}
		all := true
		for _, r := range trimmed {
			if !strings.ContainsRune(passwordAlphabet, r) {
				all = false
				break
			}
		}
		if all {
			return trimmed
		}
	}
	t.Fatalf("no generated password in the output:\n%s", output)
	return ""
}
