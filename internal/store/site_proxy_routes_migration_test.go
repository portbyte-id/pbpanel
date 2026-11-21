package store

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// Migration 0020 turns each reverse-proxy site's single target into one route
// on "/", keeps proxy_target readable for a rollback, and leaves every other
// site with no route. The old row must come out behaving exactly as it did.
func TestSiteProxyRoutesMigrationTurnsTargetIntoRootRoute(t *testing.T) {
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
		if m.version < 20 {
			before[m.name] = &fstest.MapFile{Data: []byte(m.body)}
		}
	}
	if err := migrate(ctx, db, before); err != nil {
		t.Fatalf("migrate to 0019: %v", err)
	}

	const ins = `INSERT INTO sites (domain, kind, root, proxy_target, php_version, created_at)
		VALUES (?, ?, ?, ?, NULL, '2026-11-17T12:00:00Z')`
	for _, row := range []struct {
		domain, kind, root, target string
	}{{
		"api.test", "proxy", "", "http://127.0.0.1:3000",
	}, {
		"a.test", "static", "/www/services/sites/a.test", "",
	}, {
		"php.test", "php", "/www/services/sites/php.test", "",
	}, {
		// A proxy row with no target is damaged, but it must not become a route
		// to nowhere: nothing is inserted for it.
		"broken.test", "proxy", "", "",
	}} {
		if _, err := db.ExecContext(ctx, ins, row.domain, row.kind, row.root, row.target); err != nil {
			t.Fatalf("insert %s: %v", row.domain, err)
		}
	}

	if err := migrate(ctx, db, embeddedMigrations()); err != nil {
		t.Fatalf("migrate to 0020: %v", err)
	}

	rows, err := db.QueryContext(ctx, `SELECT domain, position, path, target, strip_prefix FROM site_proxy_routes ORDER BY domain`)
	if err != nil {
		t.Fatalf("select routes: %v", err)
	}
	type route struct {
		domain, path, target string
		position, strip      int
	}
	var got []route
	for rows.Next() {
		var r route
		if err := rows.Scan(&r.domain, &r.position, &r.path, &r.target, &r.strip); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	_ = rows.Close()
	want := []route{{domain: "api.test", position: 0, path: "/", target: "http://127.0.0.1:3000", strip: 0}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("routes = %+v, want %+v", got, want)
	}

	// proxy_target survives, so a rollback to a pre-0020 daemon reads the same
	// target it wrote.
	var target string
	if err := db.QueryRowContext(ctx, `SELECT proxy_target FROM sites WHERE domain = 'api.test'`).Scan(&target); err != nil {
		t.Fatalf("read proxy_target: %v", err)
	}
	if target != "http://127.0.0.1:3000" {
		t.Fatalf("proxy_target = %q, want it kept for a rollback", target)
	}

	// The new table's constraints match the model: a prefix is a normal route, a
	// duplicate path for one site is refused, and two sites may share a path.
	if _, err := db.ExecContext(ctx, `INSERT INTO site_proxy_routes (domain, position, path, target, strip_prefix) VALUES ('php.test', 0, '/api', 'http://127.0.0.1:8080', 1)`); err != nil {
		t.Fatalf("insert a subpath route: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO site_proxy_routes (domain, position, path, target, strip_prefix) VALUES ('php.test', 1, '/api', 'http://127.0.0.1:9', 1)`); err == nil {
		t.Fatal("a duplicate (domain, path) was accepted")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO site_proxy_routes (domain, position, path, target, strip_prefix) VALUES ('a.test', 0, '/api', 'http://127.0.0.1:8080', 1)`); err != nil {
		t.Fatalf("two sites may proxy the same path: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO site_proxy_routes (domain, position, path, target, strip_prefix) VALUES ('php.test', 2, '/v2', 'http://127.0.0.1:9', 5)`); err == nil {
		t.Fatal("a strip_prefix outside 0/1 was accepted")
	}
}
