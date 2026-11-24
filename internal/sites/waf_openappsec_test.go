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

// appsecSvc is the open-appsec agent as the sites manager sees it: a service
// whose apply either succeeds or is rejected.
type appsecSvc struct {
	validateErr error
	applyErr    error
	applies     int
}

func (s *appsecSvc) Name() string { return "open-appsec" }
func (s *appsecSvc) Validate(context.Context) (string, error) {
	return "", s.validateErr
}
func (s *appsecSvc) Reload(context.Context) error {
	s.applies++
	return s.applyErr
}
func (s *appsecSvc) Healthy(context.Context) error { return nil }

// svcApplier is an applier that runs the change's services the way confedit
// does — validate, then apply — and rolls the files back when one refuses. The
// rig's memApplier records the change and always succeeds, which cannot exercise
// a rejected policy.
type svcApplier struct {
	fs      *memFS
	applied []confedit.Change
	taken   map[string][]byte
}

func (a *svcApplier) Apply(ctx context.Context, ch confedit.Change) (confedit.Result, error) {
	a.applied = append(a.applied, ch)
	if a.taken == nil {
		a.taken = map[string][]byte{}
	}
	for _, f := range ch.Files {
		if prev, ok := a.fs.files[f.Path]; ok {
			a.taken[f.Path] = prev
		}
		if f.Delete {
			_ = a.fs.Remove(f.Path)
			continue
		}
		_ = a.fs.WriteFile(f.Path, f.Content, 0o644)
	}
	fail := func(stage string, svc confedit.Service, err error) (confedit.Result, error) {
		for _, f := range ch.Files {
			if prev, ok := a.taken[f.Path]; ok {
				_ = a.fs.WriteFile(f.Path, prev, 0o644)
			} else {
				_ = a.fs.Remove(f.Path)
			}
		}
		a.taken = nil
		return confedit.Result{}, errors.New(stage + " " + svc.Name() + ": " + err.Error())
	}
	for _, svc := range ch.Services {
		if _, err := svc.Validate(ctx); err != nil {
			return fail("validate", svc, err)
		}
	}
	for _, svc := range ch.Services {
		if err := svc.Reload(ctx); err != nil {
			return fail("reload", svc, err)
		}
	}
	a.taken = nil
	return confedit.Result{}, nil
}

// appsecRig is a manager whose firewall and agent are both wired in, with an
// applier that runs the services.
type appsecRig struct {
	m   *Manager
	fs  *memFS
	ap  *svcApplier
	svc *fakeSvc
	st  *store.Store
}

func newAppsecRig(t *testing.T) (*appsecRig, *appsecSvc) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	paths := nginx.Paths{Root: "/www"}
	mfs := newMemFS()
	ap := &svcApplier{fs: mfs}
	svc := &fakeSvc{}
	agent := &appsecSvc{}
	engine := waf.New(st, paths, func(context.Context) (waf.Availability, error) {
		return waf.Availability{Available: true, Flavor: "pqc"}, nil
	})
	engine.WithOpenAppsec(func(context.Context) (bool, string) { return true, "" })
	m, err := New(Options{
		Store: st, Applier: ap, Service: svc, Paths: paths, FS: mfs, WAF: engine, Appsec: agent,
		Now: func() time.Time { return time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC) },
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
	return &appsecRig{m: m, fs: mfs, ap: ap, svc: svc, st: st}, agent
}

func openAppsecSiteConfig() waf.Config {
	c := waf.Default()
	c.Engine, c.Mode = waf.EngineOpenAppsec, waf.ModeBlock
	c.OpenAppsec.Confidence = waf.ConfidenceMedium
	return c
}

const (
	appsecModulePath = "/www/panel/nginx/waf/main/open-appsec.conf"
	appsecPolicyPath = "/www/panel/nginx/waf/open-appsec/local_policy.yaml"
)

// TestSetWAFWritesTheEngineAndThePolicy: switching a site to the second engine
// writes the module loader and the policy in the same apply, and the policy
// names the site.
func TestSetWAFWritesTheEngineAndThePolicy(t *testing.T) {
	r, agent := newAppsecRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Aliases: []string{"www.a.test"}, Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.SetWAF(ctx, "a.test", openAppsecSiteConfig()); err != nil {
		t.Fatal(err)
	}
	block := blockOf(t, string(r.fs.files["/www/panel/nginx/sites/a.test.conf"]), nginx.BlockWAF)
	if !strings.Contains(block, "cp-nano-nginx-attachment on;") {
		t.Errorf("the site block does not inspect with open-appsec:\n%s", block)
	}
	if strings.Contains(block, "modsecurity") {
		t.Errorf("the site block also carries ModSecurity:\n%s", block)
	}
	if !strings.Contains(string(r.fs.files[appsecModulePath]), "load_module") {
		t.Error("the module loader was not written")
	}
	policy := string(r.fs.files[appsecPolicyPath])
	for _, want := range []string{"host: a.test", "host: www.a.test", "mode: prevent-learn", "minimumConfidence: medium"} {
		if !strings.Contains(policy, want) {
			t.Errorf("the policy lacks %q:\n%s", want, policy)
		}
	}
	// The engine-independent files are untouched by the engine choice.
	if !r.fs.has("/www/panel/nginx/waf/http/a.test.conf") {
		t.Error("the http-context file was not written")
	}
	// The agent is told after nginx: it is the last service of the change.
	if agent.applies != 1 {
		t.Errorf("the agent was applied %d times, want once", agent.applies)
	}
}

// The load order step 0 measured: a lone agent restart silently stops
// enforcement until nginx reloads, so nginx is always reloaded first.
func TestOpenAppsecAppliesAfterNginx(t *testing.T) {
	r, agent := newAppsecRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	before := len(r.ap.applied)
	if _, err := r.m.SetWAF(ctx, "a.test", openAppsecSiteConfig()); err != nil {
		t.Fatal(err)
	}
	if len(r.ap.applied)-before != 1 {
		t.Fatalf("%d applies, want one", len(r.ap.applied)-before)
	}
	services := r.ap.applied[len(r.ap.applied)-1].Services
	if len(services) != 2 || services[0] != r.svc || services[1] != agent {
		t.Errorf("services = %v, want nginx then the agent", services)
	}
}

// A rejected policy rolls the whole change back, and the stored config with it:
// the same contract an `nginx -t` failure has today.
func TestRejectedOpenAppsecPolicyRestoresEverything(t *testing.T) {
	r, agent := newAppsecRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	// A first site on ModSecurity, so the previous state has something to keep.
	keep := waf.Default()
	keep.Mode = waf.ModeBlock
	if _, err := r.m.SetWAF(ctx, "a.test", keep); err != nil {
		t.Fatal(err)
	}
	beforeSite := string(r.fs.files["/www/panel/nginx/sites/a.test.conf"])

	agent.applyErr = errors.New("the agent rejected the policy")
	if _, err := r.m.SetWAF(ctx, "a.test", openAppsecSiteConfig()); err == nil {
		t.Fatal("a rejected policy succeeded")
	}
	// The files are as they were: the site file still asks for ModSecurity, and
	// neither the open-appsec loader nor the policy exists.
	if got := string(r.fs.files["/www/panel/nginx/sites/a.test.conf"]); got != beforeSite {
		t.Errorf("the site file changed after a rolled-back apply:\n%s", got)
	}
	if r.fs.has(appsecModulePath) || r.fs.has(appsecPolicyPath) {
		t.Error("an open-appsec file survived the rollback")
	}
	// And the stored settings are the old ones, so the next render agrees.
	got, _, err := r.m.WAFConfig(ctx, "a.test")
	if err != nil {
		t.Fatal(err)
	}
	if got.Engine != waf.EngineModSecurity || got.Mode != waf.ModeBlock {
		t.Errorf("after the rollback the stored config is %+v, want the previous one", got)
	}
}

// Deleting the last open-appsec site removes the policy and the module with it,
// so the module and its per-server directive never outlive the sites that use
// them (the directive without the module makes `nginx -t` fail).
func TestDeleteRemovesTheOpenAppsecFiles(t *testing.T) {
	r, _ := newAppsecRig(t)
	for _, d := range []string{"a.test", "b.test"} {
		if _, err := r.m.Create(ctx, CreateInput{Domain: d, Kind: KindStatic}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.m.SetWAF(ctx, d, openAppsecSiteConfig()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.m.Delete(ctx, "a.test"); err != nil {
		t.Fatal(err)
	}
	policy := string(r.fs.files[appsecPolicyPath])
	if strings.Contains(policy, "a.test") || !strings.Contains(policy, "b.test") {
		t.Errorf("the policy was not re-rendered without the deleted site:\n%s", policy)
	}
	if !r.fs.has(appsecModulePath) {
		t.Error("the module loader went while a site still uses the engine")
	}
	if _, err := r.m.Delete(ctx, "b.test"); err != nil {
		t.Fatal(err)
	}
	if r.fs.has(appsecModulePath) || r.fs.has(appsecPolicyPath) {
		t.Error("the open-appsec files stayed after the last site that used them was deleted")
	}
}

// A rename or an alias change re-renders the policy in the same apply, because
// the policy names every name the site answers to.
func TestAliasChangeRerendersThePolicy(t *testing.T) {
	r, _ := newAppsecRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.SetWAF(ctx, "a.test", openAppsecSiteConfig()); err != nil {
		t.Fatal(err)
	}
	aliases := []string{"www.a.test", "shop.a.test"}
	if _, err := r.m.Update(ctx, "a.test", UpdateInput{Aliases: &aliases}); err != nil {
		t.Fatal(err)
	}
	policy := string(r.fs.files[appsecPolicyPath])
	for _, want := range []string{"host: www.a.test", "host: shop.a.test"} {
		if !strings.Contains(policy, want) {
			t.Errorf("the policy does not carry the new alias %q:\n%s", want, policy)
		}
	}
	// Dropping one removes it again.
	aliases = []string{"www.a.test"}
	if _, err := r.m.Update(ctx, "a.test", UpdateInput{Aliases: &aliases}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(r.fs.files[appsecPolicyPath]), "shop.a.test") {
		t.Error("a removed alias stayed in the policy")
	}
}

// The whole point of a deterministic render: an unrelated change writes nothing,
// so the agent is not reloaded for it.
func TestUnrelatedChangeDoesNotRewriteThePolicy(t *testing.T) {
	r, _ := newAppsecRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.SetWAF(ctx, "a.test", openAppsecSiteConfig()); err != nil {
		t.Fatal(err)
	}
	policy := string(r.fs.files[appsecPolicyPath])

	// A second save with nothing changed: the render is the same bytes.
	if _, err := r.m.SetWAF(ctx, "a.test", openAppsecSiteConfig()); err != nil {
		t.Fatal(err)
	}
	if got := string(r.fs.files[appsecPolicyPath]); got != policy {
		t.Errorf("an unchanged save rewrote the policy:\n--- before ---\n%s\n--- after ---\n%s", policy, got)
	}

	// A native protection on a ModSecurity site must not touch the policy either.
	if _, err := r.m.Create(ctx, CreateInput{Domain: "c.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	c := waf.Default()
	c.PathBlocklist = []string{"/x"}
	if _, err := r.m.SetWAF(ctx, "c.test", c); err != nil {
		t.Fatal(err)
	}
	if got := string(r.fs.files[appsecPolicyPath]); got != policy {
		t.Errorf("a ModSecurity change rewrote the open-appsec policy:\n%s", got)
	}
}

// A host with no agent refuses the engine with a field-named 422, before
// anything is stored or written.
func TestOpenAppsecRefusedWithoutTheAgent(t *testing.T) {
	r := wafRig(t, true)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	before := len(r.ap.applied)
	_, err := r.m.SetWAF(ctx, "a.test", openAppsecSiteConfig())
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "engine" {
		t.Fatalf("err = %v, want a ValidationError on engine", err)
	}
	if len(r.ap.applied) != before {
		t.Error("a refused change reached the applier")
	}
}

// Switching away from open-appsec removes the policy's rule for that site, and
// the loader when no site is left; a ModSecurity site keeps its rules file.
func TestSwitchBackFromOpenAppsec(t *testing.T) {
	r, _ := newAppsecRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.SetWAF(ctx, "a.test", openAppsecSiteConfig()); err != nil {
		t.Fatal(err)
	}
	back := waf.Default()
	back.Mode = waf.ModeBlock
	if _, err := r.m.SetWAF(ctx, "a.test", back); err != nil {
		t.Fatal(err)
	}
	if r.fs.has(appsecModulePath) || r.fs.has(appsecPolicyPath) {
		t.Error("the open-appsec files stayed after the last site switched away")
	}
	if !strings.Contains(blockOf(t, string(r.fs.files["/www/panel/nginx/sites/a.test.conf"]), nginx.BlockWAF), "modsecurity on;") {
		t.Error("the site did not go back to ModSecurity")
	}
}
