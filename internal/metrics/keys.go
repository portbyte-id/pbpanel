package metrics

import (
	"context"
	"fmt"
	"sync"
)

// Key kinds in series_keys.
const (
	KindMount   = "mount"
	KindService = "service"
	KindSite    = "site"
)

type keyCache struct {
	mu  sync.Mutex
	ids map[seriesKey]int64
}

type seriesKey struct{ kind, name string }

func newKeyCache() *keyCache {
	return &keyCache{ids: map[seriesKey]int64{}}
}

// KeyID returns the dictionary id of a series, creating it on first use. It
// commits on its own, outside any flush transaction, so a rolled-back flush can
// never leave the cache pointing at an id that does not exist.
func (d *DB) KeyID(ctx context.Context, kind, name string) (int64, error) {
	k := seriesKey{kind, name}
	d.keys.mu.Lock()
	id, ok := d.keys.ids[k]
	d.keys.mu.Unlock()
	if ok {
		return id, nil
	}
	if _, err := d.st.DB().ExecContext(ctx, `INSERT OR IGNORE INTO series_keys (kind, name) VALUES (?, ?)`, kind, name); err != nil {
		return 0, fmt.Errorf("metrics: create series key %s/%s: %w", kind, name, err)
	}
	if err := d.st.DB().QueryRowContext(ctx, `SELECT id FROM series_keys WHERE kind = ? AND name = ?`, kind, name).Scan(&id); err != nil {
		return 0, fmt.Errorf("metrics: read series key %s/%s: %w", kind, name, err)
	}
	d.keys.mu.Lock()
	d.keys.ids[k] = id
	d.keys.mu.Unlock()
	return id, nil
}

// KeyNames returns id -> name for every series of a kind.
func (d *DB) KeyNames(ctx context.Context, kind string) (map[int64]string, error) {
	rows, err := d.st.DB().QueryContext(ctx, `SELECT id, name FROM series_keys WHERE kind = ?`, kind)
	if err != nil {
		return nil, fmt.Errorf("metrics: list series keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("metrics: list series keys: %w", err)
		}
		out[id] = name
	}
	return out, rows.Err()
}
