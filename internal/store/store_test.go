package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func schemaVersion(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.DB().QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return v
}

func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n int
	err := s.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	if err != nil {
		t.Fatalf("count table %s: %v", name, err)
	}
	return n == 1
}

func TestOpenMigratesEmptyDatabase(t *testing.T) {
	s := openTemp(t)

	if !tableExists(t, s, "schema_migrations") {
		t.Fatal("schema_migrations was not created")
	}
	if got := schemaVersion(t, s); got != 1 {
		t.Fatalf("schema version = %d, want 1", got)
	}
}

func TestOpenEnablesWALAndBusyTimeout(t *testing.T) {
	s := openTemp(t)

	var mode string
	if err := s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	var timeout int
	if err := s.DB().QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if timeout != busyTimeoutMS {
		t.Errorf("busy_timeout = %d, want %d", timeout, busyTimeoutMS)
	}
}

func TestMigrateRerunIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = second.Close() }()

	if got := schemaVersion(t, second); got != 1 {
		t.Fatalf("schema version after re-open = %d, want 1", got)
	}

	var rows int
	if err := second.DB().QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&rows); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if rows != 1 {
		t.Fatalf("schema_migrations has %d rows, want 1: a migration was applied twice", rows)
	}
}

// A migration that fails must leave the database exactly as it was: no version
// recorded, and none of its statements visible.
func TestFailingMigrationLeavesVersionAndSchemaUnchanged(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_init.sql": {Data: []byte(`CREATE TABLE alpha (id INTEGER PRIMARY KEY);`)},
		"0002_broken.sql": {Data: []byte(`
			CREATE TABLE beta (id INTEGER PRIMARY KEY);
			INSERT INTO does_not_exist (id) VALUES (1);
		`)},
	}

	path := filepath.Join(t.TempDir(), "panel.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()
	s := &Store{db: db}

	if err := migrate(context.Background(), db, fsys); err == nil {
		t.Fatal("migrate succeeded, want the broken migration to fail")
	}

	if got := schemaVersion(t, s); got != 1 {
		t.Fatalf("schema version = %d, want 1: the failed migration must not be recorded", got)
	}
	if !tableExists(t, s, "alpha") {
		t.Error("alpha is missing: the migration that succeeded was rolled back")
	}
	if tableExists(t, s, "beta") {
		t.Error("beta exists: the failed migration's statements were not rolled back")
	}
}

func TestMigrationsApplyInNumericOrder(t *testing.T) {
	fsys := fstest.MapFS{
		"0002_second.sql": {Data: []byte(`INSERT INTO order_log (step) VALUES (2);`)},
		"0001_first.sql": {Data: []byte(`
			CREATE TABLE order_log (step INTEGER NOT NULL);
			INSERT INTO order_log (step) VALUES (1);
		`)},
	}

	db, err := openDB(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := migrate(context.Background(), db, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rows, err := db.Query(`SELECT step FROM order_log ORDER BY rowid`)
	if err != nil {
		t.Fatalf("query order_log: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var got []int
	for rows.Next() {
		var step int
		if err := rows.Scan(&step); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, step)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("applied order = %v, want [1 2]", got)
	}
}

func TestLoadMigrationsRejectsBadNames(t *testing.T) {
	for _, name := range []string{"init.sql", "1_init.sql", "0000_init.sql", "abcd_init.sql", "0001_.sql"} {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{name: {Data: []byte("SELECT 1;")}}
			if _, err := loadMigrations(fsys); err == nil {
				t.Fatalf("loadMigrations accepted %q", name)
			}
		})
	}
}

func TestLoadMigrationsRejectsDuplicateVersions(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_one.sql": {Data: []byte("SELECT 1;")},
		"0001_two.sql": {Data: []byte("SELECT 2;")},
	}
	if _, err := loadMigrations(fsys); err == nil {
		t.Fatal("loadMigrations accepted two files claiming version 1")
	}
}

// A database written by a newer build must be refused, not migrated backwards.
func TestMigrateRefusesNewerSchema(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create ledger: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (99, '2026-11-16T00:00:00Z')`); err != nil {
		t.Fatalf("insert version: %v", err)
	}

	fsys := fstest.MapFS{"0001_init.sql": {Data: []byte("SELECT 1;")}}
	if err := migrate(context.Background(), db, fsys); err == nil {
		t.Fatal("migrate accepted a database at a newer schema version")
	}
}

func TestWithTxCommitsOnNil(t *testing.T) {
	s := openTemp(t)
	if _, err := s.DB().Exec(`CREATE TABLE t (n INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	err := s.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO t (n) VALUES (1)`)
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	if got := countRows(t, s); got != 1 {
		t.Fatalf("rows = %d, want 1: the transaction did not commit", got)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	s := openTemp(t)
	if _, err := s.DB().Exec(`CREATE TABLE t (n INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	sentinel := errors.New("caller gave up")
	err := s.WithTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO t (n) VALUES (1)`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx error = %v, want %v", err, sentinel)
	}
	if got := countRows(t, s); got != 0 {
		t.Fatalf("rows = %d, want 0: the transaction was not rolled back", got)
	}
}

func TestWithTxRollsBackAndRepanics(t *testing.T) {
	s := openTemp(t)
	if _, err := s.DB().Exec(`CREATE TABLE t (n INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	func() {
		defer func() {
			if p := recover(); p == nil {
				t.Error("WithTx swallowed the panic")
			}
		}()
		_ = s.WithTx(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO t (n) VALUES (1)`); err != nil {
				return err
			}
			panic("boom")
		})
	}()

	if got := countRows(t, s); got != 0 {
		t.Fatalf("rows = %d, want 0: the panicking transaction was not rolled back", got)
	}
}

func TestOpenRejectsQueryStringInPath(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "panel.db?mode=ro")); err == nil {
		t.Fatal("Open accepted a path containing '?', which the driver parses as a query")
	}
}

// `panel set` migrates while the daemon holds the database open, so two
// processes can race to apply the same file. The immediate transaction
// serialises them and the loser must skip, not apply twice.
func TestConcurrentMigrateAppliesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.db")

	const procs = 4
	errs := make(chan error, procs)
	for range procs {
		go func() {
			s, err := Open(path)
			if err != nil {
				errs <- err
				return
			}
			errs <- s.Close()
		}()
	}
	for range procs {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Open: %v", err)
		}
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	var rows int
	if err := s.DB().QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&rows); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if rows != 1 {
		t.Fatalf("schema_migrations has %d rows, want 1: a migration was applied twice", rows)
	}
}

func countRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}
