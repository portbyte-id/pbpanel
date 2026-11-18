package store

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// Migration 0018 rebuilds cron_jobs. Every row keeps its id and its runs, and
// the id sequence carries over, so an id a deleted job had is never reused.
func TestCronBuiltinMigrationKeepsJobsAndIDs(t *testing.T) {
	ctx := context.Background()
	db, err := openDB(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	all, err := loadMigrations(embeddedMigrations())
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	before := fstest.MapFS{}
	for _, m := range all {
		if m.version < 18 {
			before[m.name] = &fstest.MapFile{Data: []byte(m.body)}
		}
	}
	if err := migrate(ctx, db, before); err != nil {
		t.Fatalf("migrate to 0017: %v", err)
	}

	// Job 9 is deleted, so the sequence is past the largest surviving id.
	const old = `INSERT INTO cron_jobs (id, name, schedule, kind, run_user, timeout_seconds, overlap, retention, enabled, created_at, updated_at)
		VALUES (?, ?, '* * * * *', ?, 'root', 3600, 'skip', 50, ?, 't', 't')`
	for _, row := range []struct {
		id      int
		name    string
		kind    string
		enabled int
	}{{5, "nightly", "argv", 1}, {7, "cleanup", "shell", 0}, {9, "gone", "script", 1}} {
		if _, err := db.ExecContext(ctx, old, row.id, row.name, row.kind, row.enabled); err != nil {
			t.Fatalf("insert %s: %v", row.name, err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM cron_jobs WHERE id = 9`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO cron_runs (job_id, source, started_at, status) VALUES (5, 'manual', 't', 'succeeded')`); err != nil {
		t.Fatalf("insert run: %v", err)
	}

	if err := migrate(ctx, db, embeddedMigrations()); err != nil {
		t.Fatalf("migrate to 0018: %v", err)
	}

	rows, err := db.QueryContext(ctx, `SELECT id, name, kind, enabled, builtin FROM cron_jobs ORDER BY id`)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	type row struct {
		id      int
		name    string
		kind    string
		enabled int
		builtin string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.name, &r.kind, &r.enabled, &r.builtin); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	_ = rows.Close()
	want := []row{
		{id: 5, name: "nightly", kind: "argv", enabled: 1},
		{id: 7, name: "cleanup", kind: "shell", enabled: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	var runs int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM cron_runs WHERE job_id = 5`).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("runs of job 5 = %d, %v; want 1", runs, err)
	}

	// The new kind is accepted, and the id after 9 is 10, not 8 or a reused 9.
	res, err := db.ExecContext(ctx, `INSERT INTO cron_jobs (name, schedule, kind, run_user, timeout_seconds, overlap, retention, enabled, builtin, created_at, updated_at)
		VALUES ('ssl-renew', '17 3 * * *', 'builtin', 'root', 7200, 'skip', 50, 1, 'tls.renew', 't', 't')`)
	if err != nil {
		t.Fatalf("insert builtin: %v", err)
	}
	if id, _ := res.LastInsertId(); id != 10 {
		t.Fatalf("builtin job id = %d, want 10 (sequence kept past the deleted 9)", id)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO cron_jobs (name, schedule, kind, run_user, timeout_seconds, overlap, retention, created_at, updated_at)
		VALUES ('bogus', '@daily', 'bogus', 'root', 60, 'skip', 50, 't', 't')`); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
}
