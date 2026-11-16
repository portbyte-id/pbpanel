package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The settings registry declares the Store interface it needs; these methods
// are that implementation. Values stay text here: only the registry parses and
// validates them, so this package never has to know what a value means.

// LoadSetting returns the stored text for name. ok is false when the operator
// has never changed the key, which is the signal to use the registry default.
func (s *Store) LoadSetting(ctx context.Context, name string) (value string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE name = ?`, name).Scan(&value)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("store: load setting %s: %w", name, err)
	}
	return value, true, nil
}

// LoadSettings returns every stored setting. Keys still at their default have
// no row and are therefore absent.
func (s *Store) LoadSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("store: load settings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]string)
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, fmt.Errorf("store: load settings: %w", err)
		}
		out[name] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: load settings: %w", err)
	}
	return out, nil
}

// SaveSetting stores one value, replacing any previous one. The upsert is a
// single statement, which SQLite applies atomically without an explicit
// transaction.
func (s *Store) SaveSetting(ctx context.Context, name, value string) error {
	const q = `
		INSERT INTO settings (name, value, updated_at)
		VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT (name) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at`
	if _, err := s.db.ExecContext(ctx, q, name, value); err != nil {
		return fmt.Errorf("store: save setting %s: %w", name, err)
	}
	return nil
}
