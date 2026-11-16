package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationsEmbedded embed.FS

// Migration files are forward-only: once a numbered file has landed it is
// never edited, a schema change is a new file with the next number.
type migration struct {
	version int
	name    string
	body    string
}

func embeddedMigrations() fs.FS {
	sub, err := fs.Sub(migrationsEmbedded, "migrations")
	if err != nil {
		panic("store: embedded migrations directory is missing: " + err.Error())
	}
	return sub
}

func migrate(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	if err := createLedger(ctx, db); err != nil {
		return err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}
	known, err := loadMigrations(fsys)
	if err != nil {
		return err
	}

	if err := rejectNewerSchema(applied, known); err != nil {
		return err
	}

	for _, m := range known {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

// querier is the read/write surface both *sql.DB and *sql.Tx provide. The
// ledger helpers take it so CheckMigrations can run the same code inside one
// transaction that is rolled back.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Pending reports the migration versions this binary would apply to the
// database at path. It reads the ledger and changes nothing, so `panel install`
// can decide whether the database needs opening for migration and `panel
// upgrade` can report what an upgrade would do.
func Pending(path string) ([]int, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}
	known, err := loadMigrations(embeddedMigrations())
	if err != nil {
		return nil, err
	}
	if err := rejectNewerSchema(applied, known); err != nil {
		return nil, err
	}

	var out []int
	for _, m := range known {
		if !applied[m.version] {
			out = append(out, m.version)
		}
	}
	return out, nil
}

// CheckMigrations reports whether this binary can migrate the database at path,
// without leaving a single change behind: every pending migration runs inside one
// transaction that is rolled back.
//
// This is what `panel upgrade` runs before it swaps the binary. A migration that
// fails here would fail after the rename, with the old binary already replaced;
// finding out first is the difference between a refused upgrade and a broken
// panel.
func CheckMigrations(path string) error {
	db, err := openDB(path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: migration check: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := createLedger(ctx, tx); err != nil {
		return fmt.Errorf("store: migration check: %w", err)
	}
	applied, err := appliedVersions(ctx, tx)
	if err != nil {
		return fmt.Errorf("store: migration check: %w", err)
	}
	known, err := loadMigrations(embeddedMigrations())
	if err != nil {
		return fmt.Errorf("store: migration check: %w", err)
	}
	if err := rejectNewerSchema(applied, known); err != nil {
		return fmt.Errorf("store: migration check: %w", err)
	}

	for _, m := range known {
		if applied[m.version] {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.body); err != nil {
			return fmt.Errorf("store: migration check: %s: %w", m.name, err)
		}
	}
	return nil
}

// rejectNewerSchema refuses a database written by a newer binary. Applying an
// older set of migrations to it would leave the schema in a state neither binary
// describes.
func rejectNewerSchema(applied map[int]bool, known []migration) error {
	latest := known[len(known)-1].version
	for version := range applied {
		if version > latest {
			return fmt.Errorf("database is at schema version %d but this binary knows only up to %d: upgrade the panel", version, latest)
		}
	}
	return nil
}

func createLedger(ctx context.Context, db querier) error {
	const q = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL
)`
	if _, err := db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	return nil
}

func appliedVersions(ctx context.Context, db querier) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("store: read schema_migrations: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	return applied, nil
}

// applyMigration runs one file and records its version in the same transaction,
// so a failure leaves behind neither the statements nor the version. SQLite
// makes DDL transactional, which is what allows this. The version is re-checked
// inside the transaction because `panel set` migrates while the daemon runs:
// the immediate transaction serialises the two, and the loser skips the file
// instead of applying it twice.
func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: migration %s: begin: %w", m.name, err)
	}
	defer func() { _ = tx.Rollback() }()

	var already int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&already); err != nil {
		return fmt.Errorf("store: migration %s: check version: %w", m.name, err)
	}
	if already > 0 {
		return nil
	}

	if _, err := tx.ExecContext(ctx, m.body); err != nil {
		return fmt.Errorf("store: migration %s: %w", m.name, err)
	}
	const record = `INSERT INTO schema_migrations (version, applied_at) VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`
	if _, err := tx.ExecContext(ctx, record, m.version); err != nil {
		return fmt.Errorf("store: migration %s: record version: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migration %s: commit: %w", m.name, err)
	}
	return nil
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}

	seen := make(map[int]string, len(entries))
	out := make([]migration, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		version, err := parseVersion(name)
		if err != nil {
			return nil, err
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("store: version %d is claimed by both %s and %s", version, other, name)
		}
		seen[version] = name

		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("store: read %s: %w", name, err)
		}
		out = append(out, migration{version: version, name: name, body: string(body)})
	}
	if len(out) == 0 {
		return nil, errors.New("store: no migrations are embedded")
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// parseVersion reads the NNNN_ prefix. The fixed width is what makes lexical
// and numeric order agree, so a misnamed file is an error rather than a
// migration that runs in the wrong place.
func parseVersion(name string) (int, error) {
	base := strings.TrimSuffix(name, ".sql")
	prefix, rest, ok := strings.Cut(base, "_")
	if !ok || len(prefix) != 4 || rest == "" {
		return 0, fmt.Errorf("store: migration %q is not named NNNN_name.sql", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("store: migration %q has no positive numeric prefix", name)
	}
	return version, nil
}
