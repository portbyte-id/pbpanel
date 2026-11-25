// Package metrics keeps what the panel measures: host, disk, service and site
// history in a SQLite file of its own, rolled up into coarser tiers, plus the
// threshold alerts evaluated against it. See docs/design/metrics-history-plan.md.
package metrics

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/portbyte-id/server-panel/internal/store"
)

//go:embed migrations/*.sql
var migrationsEmbedded embed.FS

func embeddedMigrations() fs.FS {
	sub, err := fs.Sub(migrationsEmbedded, "migrations")
	if err != nil {
		panic("metrics: embedded migrations directory is missing: " + err.Error())
	}
	return sub
}

// DB is metrics.db. It is a separate file from panel.db so that sampling never
// touches the configuration database and deleting it is a valid reset.
type DB struct {
	st   *store.Store
	path string
	keys *keyCache
}

// Open opens or creates the database at path. A file that cannot be opened or
// fails quick_check is moved aside as <path>.corrupt-<unix> and recreated, and
// logf (may be nil) is told once. History is disposable; the panel must start.
func Open(path string, logf func(format string, args ...any)) (*DB, error) {
	db, err := openChecked(path)
	if err == nil {
		return db, nil
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return nil, err // nothing to move aside: the failure is not corruption
	}
	aside := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
	if renameErr := os.Rename(path, aside); renameErr != nil {
		return nil, fmt.Errorf("metrics: %w (and could not move it aside: %v)", err, renameErr)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	logf("metrics: %s was unusable (%v); moved to %s and starting an empty history", path, err, aside)
	return openChecked(path)
}

func openChecked(path string) (*DB, error) {
	st, err := store.OpenFS(path, embeddedMigrations(), "synchronous(NORMAL)", "cache_size(-2048)")
	if err != nil {
		return nil, err
	}
	var verdict string
	if err := st.DB().QueryRow(`PRAGMA quick_check`).Scan(&verdict); err != nil || verdict != "ok" {
		_ = st.Close()
		if err == nil {
			err = fmt.Errorf("quick_check: %s", verdict)
		}
		return nil, fmt.Errorf("metrics: %w", err)
	}
	return &DB{st: st, path: path, keys: newKeyCache()}, nil
}

// Close releases the database.
func (d *DB) Close() error { return d.st.Close() }

// SQL is the read path.
func (d *DB) SQL() *sql.DB { return d.st.DB() }

// Tx runs fn in one write transaction.
func (d *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	return d.st.WithTx(ctx, fn)
}

// Path is the file on disk.
func (d *DB) Path() string { return d.path }

func (d *DB) getMeta(ctx context.Context, k string) (string, bool) {
	var v string
	if err := d.st.DB().QueryRowContext(ctx, `SELECT v FROM meta WHERE k = ?`, k).Scan(&v); err != nil {
		return "", false
	}
	return v, true
}

func setMeta(ctx context.Context, tx *sql.Tx, k, v string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO meta (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
	return err
}
