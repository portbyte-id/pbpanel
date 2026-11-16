package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	// WAL lets readers run while one writer holds the write lock, so the pool
	// needs more than one connection to be worth having; four allows a few
	// concurrent readers without leaving long-lived read snapshots that would
	// stall WAL checkpointing.
	poolConns = 4

	// Writes serialise on SQLite's single writer. Five seconds is long enough
	// for a short write transaction to drain and short enough that a wedged
	// request fails instead of hanging the panel.
	busyTimeoutMS = 5000
)

// Store is the panel's SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens the database at path and brings its schema up to date. The parent
// directory must already exist.
func Open(path string) (*Store, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	// journal_mode(WAL) is silently refused on filesystems without shared
	// memory, and the pool's concurrency assumes WAL, so read it back.
	if err := checkPragmas(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: %s: %w", path, err)
	}
	if err := migrate(context.Background(), db, embeddedMigrations()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func openDB(path string) (*sql.DB, error) {
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("store: database path %q contains '?', which the driver reads as the start of a query string", path)
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(poolConns)
	db.SetMaxIdleConns(poolConns)

	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	return db, nil
}

// Close releases the connection pool.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB is the read path: settings, audit, auth and jobs run their queries here.
// Writes go through WithTx so a partial change cannot land.
func (s *Store) DB() *sql.DB {
	return s.db
}

// WithTx runs fn in a transaction: it commits when fn returns nil, rolls back
// when fn returns an error, and rolls back and re-raises a panic.
func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin transaction: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			// Commit failures leave the transaction for the driver to discard;
			// ErrTxDone just says there is nothing left to roll back.
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				err = errors.Join(err, fmt.Errorf("store: rollback: %w", rbErr))
			}
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: commit transaction: %w", err)
	}
	return nil
}

// dsn appends the connection settings as a query string rather than using a
// file: URI: the driver strips everything from the first '?' when the DSN has
// no file: prefix, so a plain path keeps working with Windows drive letters
// and backslashes.
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	q.Add("_pragma", "journal_mode(WAL)")
	// Immediate transactions take the write lock at BEGIN, so a transaction
	// never has to upgrade a read lock halfway through and fail with
	// SQLITE_BUSY, which the busy timeout cannot rescue.
	q.Set("_txlock", "immediate")
	return path + "?" + q.Encode()
}

func checkPragmas(db *sql.DB) error {
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("read journal_mode: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("journal_mode is %q, want wal: the data directory must be on a local filesystem", mode)
	}

	var timeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		return fmt.Errorf("read busy_timeout: %w", err)
	}
	if timeout != busyTimeoutMS {
		return fmt.Errorf("busy_timeout is %d, want %d", timeout, busyTimeoutMS)
	}
	return nil
}
