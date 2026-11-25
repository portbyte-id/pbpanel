package metrics

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "metrics.db"), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestOpenTwiceIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.db")
	d, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(path, nil)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer func() { _ = d.Close() }()
	var n int
	if err := d.SQL().QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("ledger rows = %d, %v; want 1", n, err)
	}
	for _, table := range []string{"host_10s", "host_1m", "host_5m", "disk_10s", "svc_30s", "svc_5m", "site_1m", "site_15m", "meta", "series_keys"} {
		var c int
		if err := d.SQL().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&c); err != nil || c != 1 {
			t.Errorf("table %s missing", table)
		}
	}
}

func TestCorruptFileIsMovedAsideAndRecreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.db")
	if err := os.WriteFile(path, []byte(strings.Repeat("this is not a sqlite database ", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	var logged []string
	d, err := Open(path, func(f string, _ ...any) { logged = append(logged, f) })
	if err != nil {
		t.Fatalf("Open over a corrupt file: %v", err)
	}
	defer func() { _ = d.Close() }()
	if len(logged) != 1 {
		t.Errorf("logged %d times, want once", len(logged))
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("corrupt copies = %v, want one", matches)
	}
	if _, err := d.KeyID(context.Background(), KindSite, "example.com"); err != nil {
		t.Fatalf("recreated database is not usable: %v", err)
	}
}

func TestKeyDictionaryRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	a, err := d.KeyID(ctx, KindSite, "a.example")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := d.KeyID(ctx, KindSite, "a.example")
	b, _ := d.KeyID(ctx, KindMount, "a.example") // same name, other kind
	if a != again || a == b {
		t.Fatalf("ids a=%d again=%d b=%d", a, again, b)
	}
	names, err := d.KeyNames(ctx, KindSite)
	if err != nil || len(names) != 1 || names[a] != "a.example" {
		t.Fatalf("names = %v, %v", names, err)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	if _, ok := d.getMeta(ctx, "x"); ok {
		t.Fatal("meta present before it was set")
	}
	for _, v := range []string{"1", "2"} {
		if err := d.Tx(ctx, func(tx *sql.Tx) error { return setMeta(ctx, tx, "x", v) }); err != nil {
			t.Fatal(err)
		}
	}
	if v, _ := d.getMeta(ctx, "x"); v != "2" {
		t.Fatalf("meta = %q, want 2", v)
	}
}
