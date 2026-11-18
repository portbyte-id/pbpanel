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
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/waf"
)

// wafRig is a rig whose manager has the firewall wired in.
func wafRig(t *testing.T, available bool) *rig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	paths := nginx.Paths{Root: "/www"}
	mfs := newMemFS()
	ap := &memApplier{fs: mfs}
	svc := &fakeSvc{}
	engine := waf.New(st, paths, func(context.Context) (waf.Availability, error) {
		if available {
			return waf.Availability{Available: true, Flavor: "pqc"}, nil
		}
		return waf.Availability{Flavor: "standard", Reason: "no module in this build"}, nil
	})
	m, err := New(Options{
		Store: st, Applier: ap, Service: svc, Paths: paths, FS: mfs, WAF: engine,
		Now: func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) },
		Settings: func(context.Context) ([]confedit.File, error) {
			main, err := paths.RenderMainInclude(nginx.DefaultHTTPSettings())
			if err != nil {
				return nil, err
			}
			return []confedit.File{{Path: paths.MainIncludePath(), Content: main}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{m: m, fs: mfs, ap: ap, svc: svc, st: st}
}

func blockConfig() waf.Config {
	c := waf.Default()
	c.Mode, c.Paranoia = waf.ModeBlock, 2
	return c
}

func TestSetWAFWritesBlockAndFiles(t *testing.T) {
	r := wafRig(t, true)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	res, err := r.m.SetWAF(ctx, "a.test", blockConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Status.Attached || res.Config.Mode != waf.ModeBlock {
		t.Errorf("result = %+v", res)
	}
	site := string(r.fs.files["/www/panel/nginx/sites/a.test.conf"])
	body := blockOf(t, site, nginx.BlockWAF)
	for _, want := range []string{"modsecurity on;", "modsecurity_rules_file /www/panel/nginx/waf/rules/a.test.conf;"} {
		if !strings.Contains(body, want) {
			t.Errorf("waf block lacks %q:\n%s", want, body)
		}
	}
	rules := string(r.fs.files["/www/panel/nginx/waf/rules/a.test.conf"])
	if !strings.Contains(rules, "SecRuleEngine On") || !strings.Contains(rules, "blocking_paranoia_level=2") {
		t.Errorf("rules file:\n%s", rules)
	}
	if !strings.Contains(string(r.fs.files["/www/panel/nginx/waf/main/modsecurity.conf"]), "load_module") {
		t.Error("the module loader was not written")
	}
	if !r.fs.has("/www/panel/logs/sites/a.test.waf-audit.log") || r.fs.owner["/www/panel/logs/sites/a.test.waf-audit.log"] != nginx.WWWUID {
		t.Error("the audit log must exist and belong to www before nginx writes it")
	}
	// A host that predates the firewall has settings files without the includes.
	if !strings.Contains(string(r.fs.files["/www/panel/nginx/panel-main.conf"]), "waf/main/*.conf") {
		t.Error("the settings file with the firewall's include line was not brought current")
	}

	got, st, err := r.m.WAFConfig(ctx, "a.test")
	if err != nil || got.Mode != waf.ModeBlock || !st.Attached || !st.Available {
		t.Errorf("WAFConfig = %+v %+v %v", got, st, err)
	}
}

func TestSetWAFRefusesModSecurityWithoutTheModule(t *testing.T) {
	r := wafRig(t, false)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	before := len(r.ap.applied)
	_, err := r.m.SetWAF(ctx, "a.test", blockConfig())
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "mode" || !strings.Contains(ve.Msg, "no module in this build") {
		t.Fatalf("err = %v, want a ValidationError on mode that says why", err)
	}
	if len(r.ap.applied) != before {
		t.Error("a refused change reached the applier")
	}
	// The native protections do not need the module.
	c := waf.Default()
	c.PathBlocklist = []string{"/secret"}
	if _, err := r.m.SetWAF(ctx, "a.test", c); err != nil {
		t.Errorf("native protections refused on a build without ModSecurity: %v", err)
	}
}

func TestSetWAFRejectsBadSettings(t *testing.T) {
	r := wafRig(t, true)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	c := waf.Default()
	c.PathBlocklist = []string{"/a;b"}
	_, err := r.m.SetWAF(ctx, "a.test", c)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "path_blocklist" {
		t.Errorf("err = %v, want a ValidationError on path_blocklist", err)
	}
	if _, err := r.m.SetWAF(ctx, "nope.test", blockConfig()); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown site: err = %v", err)
	}
}

func TestSetWAFFailureRestoresTheStoredSettings(t *testing.T) {
	r := wafRig(t, true)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.SetWAF(ctx, "a.test", blockConfig()); err != nil {
		t.Fatal(err)
	}
	r.ap.err = errors.New("nginx -t rejected the configuration")
	next := waf.Default()
	next.PathBlocklist = []string{"/x"}
	if _, err := r.m.SetWAF(ctx, "a.test", next); err == nil {
		t.Fatal("a rejected change succeeded")
	}
	got, _, _ := r.m.WAFConfig(ctx, "a.test")
	if got.Mode != waf.ModeBlock || len(got.PathBlocklist) != 0 {
		t.Errorf("after a rejected change the settings are %+v, want the previous ones", got)
	}
}

// A site file from before the firewall block gets the block when something is
// switched on, and is left alone when the settings ask for nothing.
func TestSetWAFAttachesTheBlockToAnOlderSiteFile(t *testing.T) {
	r := wafRig(t, true)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	file := "/www/panel/nginx/sites/a.test.conf"
	var kept []string
	skip := false
	for _, l := range strings.Split(string(r.fs.files[file]), "\n") {
		switch strings.TrimSpace(l) {
		case "# >>> pb:waf":
			skip = true
		case "# <<< pb:waf":
			skip = false
		default:
			if !skip {
				kept = append(kept, l)
			}
		}
	}
	older := strings.Join(kept, "\n")
	r.fs.files[file] = []byte(older)

	// Nothing asked for: the file is not part of the change.
	r.ap.applied = nil
	if _, err := r.m.SetWAF(ctx, "a.test", waf.Default()); err != nil {
		t.Fatal(err)
	}
	for _, p := range r.ap.paths() {
		if p == file {
			t.Error("an off firewall rewrote the operator's site file")
		}
	}
	if string(r.fs.files[file]) != older {
		t.Error("the site file changed")
	}

	if _, err := r.m.SetWAF(ctx, "a.test", blockConfig()); err != nil {
		t.Fatal(err)
	}
	site := string(r.fs.files[file])
	if !nginx.HasBlock([]byte(site), nginx.BlockWAF) || !strings.Contains(blockOf(t, site, nginx.BlockWAF), "modsecurity on;") {
		t.Errorf("the block was not attached:\n%s", site)
	}
}

func TestRegenerateAndBootReconcileKeepTheFirewall(t *testing.T) {
	r := wafRig(t, true)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.SetWAF(ctx, "a.test", blockConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Regenerate(ctx, "a.test"); err != nil {
		t.Fatal(err)
	}
	site := string(r.fs.files["/www/panel/nginx/sites/a.test.conf"])
	if !strings.Contains(blockOf(t, site, nginx.BlockWAF), "modsecurity on;") {
		t.Errorf("regenerating lost the firewall block:\n%s", site)
	}
	if _, ok := r.fs.files["/www/panel/nginx/waf/rules/a.test.conf"]; !ok {
		t.Error("regenerating lost the rules file")
	}

	// The boot reconcile rewrites what is missing from the stored settings.
	delete(r.fs.files, "/www/panel/nginx/waf/rules/a.test.conf")
	delete(r.fs.files, "/www/panel/nginx/waf/main/modsecurity.conf")
	rec, _ := newReconciler(t, r, &fakeRuntime{present: true})
	want, err := rec.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, f := range want {
		if !f.Delete {
			have[f.Path] = true
		}
	}
	for _, p := range []string{"/www/panel/nginx/waf/rules/a.test.conf", "/www/panel/nginx/waf/main/modsecurity.conf", "/www/panel/nginx/waf/http/a.test.conf"} {
		if !have[p] {
			t.Errorf("the reconcile does not render %s", p)
		}
	}
}

func TestDeleteRemovesTheFirewall(t *testing.T) {
	r := wafRig(t, true)
	for _, d := range []string{"a.test", "b.test"} {
		if _, err := r.m.Create(ctx, CreateInput{Domain: d, Kind: KindStatic}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.m.SetWAF(ctx, d, blockConfig()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.m.Delete(ctx, "a.test"); err != nil {
		t.Fatal(err)
	}
	if r.fs.has("/www/panel/nginx/waf/rules/a.test.conf") || r.fs.has("/www/panel/nginx/waf/http/a.test.conf") {
		t.Error("a deleted site's firewall files survived")
	}
	if !r.fs.has("/www/panel/nginx/waf/main/modsecurity.conf") {
		t.Error("the module loader went while another site still uses ModSecurity")
	}
	if _, err := r.m.Delete(ctx, "b.test"); err != nil {
		t.Fatal(err)
	}
	if r.fs.has("/www/panel/nginx/waf/main/modsecurity.conf") {
		t.Error("the module loader stayed after the last ModSecurity site was deleted")
	}
	o, err := r.m.WAFOverview(ctx)
	if err != nil || len(o.Sites) != 0 {
		t.Errorf("overview = %+v, %v", o, err)
	}
}

func TestWAFOverview(t *testing.T) {
	r := wafRig(t, true)
	for _, d := range []string{"a.test", "b.test"} {
		if _, err := r.m.Create(ctx, CreateInput{Domain: d, Kind: KindStatic}); err != nil {
			t.Fatal(err)
		}
	}
	c := blockConfig()
	c.PathBlocklist = []string{"/x", "/y"}
	if _, err := r.m.SetWAF(ctx, "b.test", c); err != nil {
		t.Fatal(err)
	}
	o, err := r.m.WAFOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Available || len(o.Sites) != 2 {
		t.Fatalf("overview = %+v", o)
	}
	if a, b := o.Sites[0], o.Sites[1]; a.Domain != "a.test" || a.Mode != waf.ModeOff || b.Mode != waf.ModeBlock || b.PathBlocklist != 2 || b.Paranoia != 2 {
		t.Errorf("rows = %+v %+v", a, b)
	}
}

func TestWAFWithoutAnEngine(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.SetWAF(ctx, "a.test", waf.Default()); !errors.Is(err, ErrNoWAF) {
		t.Errorf("err = %v, want ErrNoWAF", err)
	}
	if _, _, err := r.m.WAFConfig(ctx, "a.test"); !errors.Is(err, ErrNoWAF) {
		t.Errorf("err = %v, want ErrNoWAF", err)
	}
}
