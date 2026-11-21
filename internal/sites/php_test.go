package sites

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/php"
	"github.com/portbyte-id/server-panel/internal/store"
)

// fakePHP is the PHP runtime: versions are usable or not, and each version's
// base file is "missing" until an apply writes it.
type fakePHP struct {
	fs      *memFS
	usable  map[string]bool
	ensured []string
}

type namedSvc struct{ name string }

func (s namedSvc) Name() string                             { return s.name }
func (s namedSvc) Validate(context.Context) (string, error) { return "", nil }
func (s namedSvc) Reload(context.Context) error             { return nil }
func (s namedSvc) Healthy(context.Context) error            { return nil }

func (f *fakePHP) Paths() php.Paths { return php.Paths{Root: "/www"} }
func (f *fakePHP) Usable(_ context.Context, ver string) error {
	switch ok, known := f.usable[ver]; {
	case !known:
		return php.ErrUnknownVersion
	case !ok:
		return php.ErrNotReady
	}
	return nil
}
func (f *fakePHP) Service(ver string, start bool) confedit.Service {
	name := "php-" + ver
	if start {
		name += "+start"
	}
	return namedSvc{name}
}
func (f *fakePHP) BaseFiles(_ context.Context, ver string) ([]confedit.File, error) {
	conf := f.Paths().FPMConf(ver)
	if f.fs.has(conf) {
		return nil, nil
	}
	return []confedit.File{{Path: conf, Content: []byte("[global]\n")}}, nil
}
func (f *fakePHP) EnsureRunning(_ context.Context, ver string) (php.EnsureResult, error) {
	f.ensured = append(f.ensured, ver)
	return php.EnsureResult{Action: php.ActionNone, Reason: "running"}, nil
}

type phpRig struct {
	*rig
	php *fakePHP
}

func newPHPRig(t *testing.T) *phpRig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mfs := newMemFS()
	ap := &memApplier{fs: mfs}
	svc := &fakeSvc{}
	fp := &fakePHP{fs: mfs, usable: map[string]bool{"8.3": true, "8.4": true, "8.5": false}}
	m, err := New(Options{
		Store: st, Applier: ap, Service: svc, Paths: nginx.Paths{Root: "/www"}, FS: mfs, PHP: fp,
		Now: func() time.Time { return time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &phpRig{rig: &rig{m: m, fs: mfs, ap: ap, svc: svc, st: st}, php: fp}
}

func (r *phpRig) create(t *testing.T, domain, ver string) Site {
	t.Helper()
	res, err := r.m.Create(ctx, CreateInput{Domain: domain, Kind: KindPHP, PHPVersion: ver})
	if err != nil {
		t.Fatalf("create %s: %v", domain, err)
	}
	return res.Site
}

func serviceNames(ch confedit.Change) string {
	var names []string
	for _, s := range ch.Services {
		names = append(names, s.Name())
	}
	return strings.Join(names, " ")
}

func (r *phpRig) last() confedit.Change { return r.ap.applied[len(r.ap.applied)-1] }

const (
	poolFile83 = "/www/panel/php/8.3/pool.d/a_test.conf"
	poolFile84 = "/www/panel/php/8.4/pool.d/a_test.conf"
	phpInclude = "/www/panel/nginx/includes/a.test/php.conf"
	siteConf   = "/www/panel/nginx/sites/a.test.conf"
)

func TestCreatePHPSite(t *testing.T) {
	r := newPHPRig(t)
	site := r.create(t, "a.test", "8.3")

	if site.Kind != KindPHP || site.PHPVersion == nil || *site.PHPVersion != "8.3" || site.PHPPool != "a_test" {
		t.Fatalf("site = %+v", site)
	}
	// FPM is validated and reloaded before nginx is pointed at its socket, and
	// the version's container is started if this is its first site.
	if got := serviceNames(r.last()); got != "php-8.3+start nginx" {
		t.Errorf("services = %q", got)
	}

	siteFile, _ := r.fs.ReadFile("/www/panel/nginx/sites/a.test.conf")
	for _, want := range []string{"root /www/services/sites/a.test;", "index index.php index.html index.htm;"} {
		if !strings.Contains(string(siteFile), want) {
			t.Errorf("site file lacks %q:\n%s", want, siteFile)
		}
	}
	if strings.Contains(string(siteFile), "include "+phpInclude) || r.fs.has(phpInclude) {
		t.Error("the PHP part is a block of the site file now, not an include")
	}
	inc := blockOf(t, string(siteFile), "php")
	if !strings.Contains(inc, "fastcgi_pass unix:/www/panel/run/php-8.3-a_test.sock;") ||
		!strings.Contains(inc, "try_files $uri =404;") {
		t.Errorf("php block:\n%s", inc)
	}
	pool, _ := r.fs.ReadFile(poolFile83)
	if !strings.Contains(string(pool), "[a_test]") || !strings.Contains(string(pool), "chdir = /www/services/sites/a.test") {
		t.Errorf("pool file:\n%s", pool)
	}
	if !r.fs.has("/www/panel/php/8.3/php-fpm.conf") {
		t.Error("the version's own files were not provisioned with its first site")
	}

	// The placeholder is plain HTML: phpinfo() is only written when asked for.
	if !r.fs.has("/www/services/sites/a.test/index.html") || r.fs.has("/www/services/sites/a.test/index.php") {
		t.Error("the default placeholder must be index.html, never phpinfo()")
	}
	if r.fs.owner["/www/panel/logs/sites/a.test.php.log"] != nginx.WWWUID {
		t.Error("the PHP error log was not created for the www account")
	}

	got, err := r.m.Get(ctx, "a.test")
	if err != nil || got.PHPPool != "a_test" || got.PHPVersion == nil || *got.PHPVersion != "8.3" {
		t.Errorf("Get = %+v, %v", got, err)
	}
	d, _ := r.m.Detail(ctx, "a.test")
	if len(d.Features) != 5 || d.Features[4].Feature != FeaturePHP || !d.Features[4].Attached {
		t.Errorf("features = %+v", d.Features)
	}
}

func TestCreatePHPSiteWithPHPInfo(t *testing.T) {
	r := newPHPRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindPHP, PHPVersion: "8.3", PHPInfo: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := r.fs.ReadFile("/www/services/sites/a.test/index.php")
	if string(b) != "<?php phpinfo();\n" || r.fs.has("/www/services/sites/a.test/index.html") {
		t.Errorf("index.php = %q", b)
	}
}

// The URL-rewrite preset changes the php block's front-controller line, and the
// choice is kept across a reload.
func TestUpdateRewriteChangesTheFrontController(t *testing.T) {
	r := newPHPRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindPHP, PHPVersion: "8.3"}); err != nil {
		t.Fatal(err)
	}
	siteFile := "/www/panel/nginx/sites/a.test.conf"
	phpBlock := func() string {
		b, _ := r.fs.ReadFile(siteFile)
		return blockOf(t, string(b), "php")
	}
	if !strings.Contains(phpBlock(), "/index.php?$query_string") {
		t.Fatalf("the default front controller is wrong:\n%s", phpBlock())
	}

	wp := RewriteWordPress
	if _, err := r.m.Update(ctx, "a.test", UpdateInput{Rewrite: &wp}); err != nil {
		t.Fatalf("update to wordpress: %v", err)
	}
	if !strings.Contains(phpBlock(), "try_files $uri $uri/ /index.php?$args;") {
		t.Errorf("wordpress did not set ?$args:\n%s", phpBlock())
	}
	got, err := r.m.Get(ctx, "a.test")
	if err != nil || got.Rewrite != RewriteWordPress {
		t.Errorf("stored rewrite = %q, %v", got.Rewrite, err)
	}

	laravel := RewriteLaravel
	if _, err := r.m.Update(ctx, "a.test", UpdateInput{Rewrite: &laravel}); err != nil {
		t.Fatalf("update to laravel: %v", err)
	}
	if !strings.Contains(phpBlock(), "/index.php?$query_string") {
		t.Errorf("laravel did not set ?$query_string:\n%s", phpBlock())
	}
}

func TestCreatePHPSiteRejections(t *testing.T) {
	r := newPHPRig(t)
	for name, in := range map[string]CreateInput{
		"php_version":    {Domain: "a.test", Kind: KindPHP},                       // none given
		"php_version ":   {Domain: "a.test", Kind: KindPHP, PHPVersion: "8.5"},    // not pulled
		"php_version  ":  {Domain: "a.test", Kind: KindPHP, PHPVersion: "7.4"},    // unknown
		"php_version   ": {Domain: "a.test", Kind: KindStatic, PHPVersion: "8.3"}, // not a PHP site
		"proxy_target":   {Domain: "a.test", Kind: KindPHP, PHPVersion: "8.3", ProxyTarget: "http://x"},
		"root":           {Domain: "a.test", Kind: KindPHP, PHPVersion: "8.3", Root: "/etc"},
	} {
		_, err := r.m.Create(ctx, in)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != strings.TrimSpace(name) {
			t.Errorf("%+v: err = %v, want a ValidationError on %s", in, err, strings.TrimSpace(name))
		}
	}
	if list, _ := r.m.List(ctx); len(list) != 0 || len(r.ap.applied) != 0 {
		t.Errorf("a rejected create left %d site(s) and %d apply(s)", len(list), len(r.ap.applied))
	}

	// Without a PHP runtime the kind is refused outright.
	plain := newRig(t)
	_, err := plain.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindPHP, PHPVersion: "8.3"})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "kind" {
		t.Errorf("no runtime: err = %v", err)
	}
}

// Step 2.4: two domains that map to one pool name cannot both exist.
func TestPoolNameCollisionIsRejected(t *testing.T) {
	r := newPHPRig(t)
	r.create(t, "a-b.test", "8.3")
	_, err := r.m.Create(ctx, CreateInput{Domain: "a.b.test", Kind: KindPHP, PHPVersion: "8.3"})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "a_b_test") || !strings.Contains(err.Error(), "a-b.test") {
		t.Fatalf("err = %v, want a conflict naming the pool and its owner", err)
	}
	if _, err := r.m.Get(ctx, "a.b.test"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the colliding site was left in the database: %v", err)
	}
	// On another version too: the name is the site's, not the version's.
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.b.test", Kind: KindPHP, PHPVersion: "8.4"}); !errors.Is(err, ErrConflict) {
		t.Errorf("collision across versions: %v", err)
	}
}

func TestFailedPHPCreateLeavesNothing(t *testing.T) {
	r := newPHPRig(t)
	r.ap.err = &confedit.ApplyError{Stage: confedit.StageValidate, Service: "php-8.3", Output: "bad pool", Err: errors.New("rejected")}
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindPHP, PHPVersion: "8.3"}); err == nil {
		t.Fatal("create succeeded")
	}
	if _, err := r.m.Get(ctx, "a.test"); !errors.Is(err, ErrNotFound) {
		t.Errorf("row left behind: %v", err)
	}
	if r.fs.has("/www/services/sites/a.test") {
		t.Error("web root left behind")
	}
	// The pool row went with it, so the same domain can be created again.
	r.ap.err = nil
	r.create(t, "a.test", "8.3")
}

func TestSetPool(t *testing.T) {
	r := newPHPRig(t)
	r.create(t, "a.test", "8.3")

	p, err := r.m.Pool(ctx, "a.test")
	if err != nil || p.Name != "a_test" || p.Version != "8.3" || p.Settings != php.DefaultPoolSettings() ||
		p.Socket != "/www/panel/run/php-8.3-a_test.sock" || p.File != poolFile83 {
		t.Fatalf("Pool = %+v, %v", p, err)
	}

	next := php.PoolSettings{PM: "static", MaxChildren: 8, MaxRequests: 0, IdleTimeout: 10}
	if _, _, err := r.m.SetPool(ctx, "a.test", next); err != nil {
		t.Fatal(err)
	}
	if got := serviceNames(r.last()); got != "php-8.3+start" {
		t.Errorf("services = %q", got)
	}
	b, _ := r.fs.ReadFile(poolFile83)
	if !strings.Contains(string(b), "pm = static") || !strings.Contains(string(b), "pm.max_children = 8") {
		t.Errorf("pool file:\n%s", b)
	}

	// A rejected apply puts the stored settings back.
	r.ap.err = &confedit.ApplyError{Stage: confedit.StageValidate, Err: errors.New("rejected")}
	if _, _, err := r.m.SetPool(ctx, "a.test", php.DefaultPoolSettings()); err == nil {
		t.Fatal("a rejected pool was reported as saved")
	}
	if p, _ := r.m.Pool(ctx, "a.test"); p.Settings != next {
		t.Errorf("stored pool after a rejected apply = %+v", p.Settings)
	}

	var ie *php.InputError
	if _, _, err := r.m.SetPool(ctx, "a.test", php.PoolSettings{PM: "nope"}); !errors.As(err, &ie) {
		t.Errorf("invalid pool: %v", err)
	}
}

func TestPoolOfANonPHPSite(t *testing.T) {
	r := newPHPRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "s.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	var ve *ValidationError
	if _, err := r.m.Pool(ctx, "s.test"); !errors.As(err, &ve) {
		t.Errorf("Pool of a static site = %v", err)
	}
	if _, err := r.m.SwitchPHP(ctx, "s.test", "8.3"); !errors.As(err, &ve) {
		t.Errorf("SwitchPHP of a static site = %v", err)
	}
	if _, err := r.m.Pool(ctx, "nope.test"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Pool of a missing site = %v", err)
	}
}

// Step 2.6: one apply, three files, new FPM then nginx then old FPM.
func TestSwitchPHP(t *testing.T) {
	r := newPHPRig(t)
	r.create(t, "a.test", "8.3")
	custom := php.PoolSettings{PM: "static", MaxChildren: 9, IdleTimeout: 10}
	if _, _, err := r.m.SetPool(ctx, "a.test", custom); err != nil {
		t.Fatal(err)
	}

	res, err := r.m.SwitchPHP(ctx, "a.test", "8.4")
	if err != nil {
		t.Fatal(err)
	}
	if *res.Site.PHPVersion != "8.4" {
		t.Errorf("site = %+v", res.Site)
	}
	ch := r.last()
	if got := serviceNames(ch); got != "php-8.4+start nginx php-8.3" {
		t.Errorf("reload order = %q, want new FPM, nginx, old FPM", got)
	}
	var deleted, written []string
	for _, f := range ch.Files {
		if f.Delete {
			deleted = append(deleted, f.Path)
		} else {
			written = append(written, f.Path)
		}
	}
	if strings.Join(deleted, " ") != poolFile83 {
		t.Errorf("deleted = %v", deleted)
	}
	for _, want := range []string{poolFile84, siteConf} {
		if !strings.Contains(strings.Join(written, " "), want) {
			t.Errorf("written = %v, lacks %s", written, want)
		}
	}
	if r.fs.has(poolFile83) || !r.fs.has(poolFile84) {
		t.Error("the pool did not move to the new version")
	}
	if strings.Contains(strings.Join(written, " "), phpInclude) {
		t.Errorf("written = %v: a block site must not get a php.conf", written)
	}
	site, _ := r.fs.ReadFile(siteConf)
	if inc := blockOf(t, string(site), "php"); !strings.Contains(inc, "unix:/www/panel/run/php-8.4-a_test.sock;") || strings.Contains(inc, "php-8.3") {
		t.Errorf("php block:\n%s", inc)
	}
	// The pool's settings travel with the site.
	if b, _ := r.fs.ReadFile(poolFile84); !strings.Contains(string(b), "pm.max_children = 9") {
		t.Errorf("new pool file:\n%s", b)
	}
	if got, _ := r.m.Get(ctx, "a.test"); *got.PHPVersion != "8.4" {
		t.Errorf("stored version = %s", *got.PHPVersion)
	}
}

// A failure in the new FPM leaves the site on the old version and serving.
func TestFailedSwitchKeepsTheOldVersion(t *testing.T) {
	r := newPHPRig(t)
	r.create(t, "a.test", "8.3")
	before, _ := r.fs.ReadFile(siteConf)

	r.ap.err = &confedit.ApplyError{Stage: confedit.StageValidate, Service: "php-8.4", Output: "bad", Err: errors.New("rejected")}
	_, err := r.m.SwitchPHP(ctx, "a.test", "8.4")
	var ae *confedit.ApplyError
	if !errors.As(err, &ae) || ae.Service != "php-8.4" {
		t.Fatalf("err = %v", err)
	}
	if got, _ := r.m.Get(ctx, "a.test"); *got.PHPVersion != "8.3" {
		t.Errorf("stored version = %s after a failed switch, want 8.3", *got.PHPVersion)
	}
	after, _ := r.fs.ReadFile(siteConf)
	if string(after) != string(before) || !r.fs.has(poolFile83) || r.fs.has(poolFile84) {
		t.Error("files changed although the switch failed")
	}
}

func TestSwitchPHPRejections(t *testing.T) {
	r := newPHPRig(t)
	r.create(t, "a.test", "8.3")
	applies := len(r.ap.applied)
	for _, ver := range []string{"8.3", "8.5", "7.4", "", "8.4; rm"} {
		var ve *ValidationError
		if _, err := r.m.SwitchPHP(ctx, "a.test", ver); !errors.As(err, &ve) || ve.Field != "php_version" {
			t.Errorf("SwitchPHP(%q) = %v, want a ValidationError on php_version", ver, err)
		}
	}

	// With the block's markers gone the control is detached, not silently written.
	file, _ := r.m.ReadFile(ctx, "a.test")
	edited := strings.Replace(file.Content, "# >>> pb:php", "", 1)
	edited = strings.Replace(edited, "# <<< pb:php", "", 1)
	if _, err := r.m.WriteFile(ctx, "a.test", []byte(edited)); err != nil {
		t.Fatal(err)
	}
	applies++
	if _, err := r.m.SwitchPHP(ctx, "a.test", "8.4"); !errors.Is(err, ErrDetached) {
		t.Errorf("detached switch = %v", err)
	}
	if len(r.ap.applied) != applies {
		t.Error("a rejected switch reached the applier")
	}
}

// Step 2.7: deleting a site removes its pool and reloads FPM without stopping it.
func TestDeletePHPSite(t *testing.T) {
	r := newPHPRig(t)
	r.create(t, "a.test", "8.3")
	res, err := r.m.Delete(ctx, "a.test")
	if err != nil {
		t.Fatal(err)
	}
	if got := serviceNames(r.last()); got != "nginx php-8.3" {
		t.Errorf("services = %q, want nginx first and a plain FPM reload", got)
	}
	if r.fs.has(poolFile83) || r.fs.has(phpInclude) {
		t.Error("the pool file or php.conf was left behind")
	}
	if res.WebRoot != "/www/services/sites/a.test" || !r.fs.has(res.WebRoot) {
		t.Errorf("web root = %q, want it kept", res.WebRoot)
	}
	// The pool name is free again.
	r.create(t, "a.test", "8.4")
}

func TestReconcilePHP(t *testing.T) {
	r := newPHPRig(t)
	r.create(t, "a.test", "8.3")
	r.create(t, "b.test", "8.3")
	r.create(t, "c.test", "8.4")
	applies := len(r.ap.applied)

	// Everything current: nothing is written, the containers are ensured.
	steps := r.m.ReconcilePHP(ctx)
	if len(steps) != 2 || !steps[0].OK || steps[0].Version != "8.3" || steps[0].Sites != 2 || !steps[1].OK {
		t.Fatalf("steps = %+v", steps)
	}
	if len(r.ap.applied) != applies || strings.Join(r.php.ensured, " ") != "8.3 8.4" {
		t.Errorf("applies = %d (want %d), ensured = %v", len(r.ap.applied), applies, r.php.ensured)
	}

	// A pool file lost while the panel was down is written back, validated.
	_ = r.fs.Remove(poolFile83)
	steps = r.m.ReconcilePHP(ctx)
	if !steps[0].OK || !r.fs.has(poolFile83) || serviceNames(r.last()) != "php-8.3+start" {
		t.Errorf("steps = %+v, services = %q", steps, serviceNames(r.last()))
	}

	// A version whose image is gone is reported, and the others still run.
	r.php.usable["8.3"] = false
	steps = r.m.ReconcilePHP(ctx)
	if steps[0].OK || !strings.Contains(steps[0].Detail, "not installed") || !steps[1].OK {
		t.Errorf("steps = %+v", steps)
	}
}
