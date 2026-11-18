package sites

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/store"
)

// memFS is a filesystem in a map. Directories are tracked by name only.
type memFS struct {
	mu    sync.Mutex
	files map[string][]byte
	dirs  map[string]bool
	owner map[string]int
	// failChown makes Chown fail, to test the undo path.
	failChown bool
}

func newMemFS() *memFS {
	return &memFS{files: map[string][]byte{}, dirs: map[string]bool{}, owner: map[string]int{}}
}

type memInfo struct{ fs.FileInfo }

func (m *memFS) ReadFile(n string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.files[n]; ok {
		return b, nil
	}
	return nil, &fs.PathError{Op: "open", Path: n, Err: fs.ErrNotExist}
}

func (m *memFS) Stat(n string) (fs.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[n]; ok || m.dirs[n] {
		return memInfo{}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: n, Err: fs.ErrNotExist}
}

func (m *memFS) MkdirAll(n string, _ fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirs[n] = true
	return nil
}

func (m *memFS) WriteFile(n string, d []byte, _ fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[n] = d
	return nil
}

func (m *memFS) Remove(n string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[n]; ok {
		delete(m.files, n)
		return nil
	}
	if m.dirs[n] {
		delete(m.dirs, n)
		return nil
	}
	return &fs.PathError{Op: "remove", Path: n, Err: fs.ErrNotExist}
}

func (m *memFS) Chown(n string, uid, _ int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failChown {
		return errors.New("operation not permitted")
	}
	m.owner[n] = uid
	return nil
}

func (m *memFS) has(n string) bool { _, err := m.Stat(n); return err == nil }

// memApplier writes a change into memFS the way confedit would, or fails it the
// way a rejected validation would, leaving the filesystem untouched.
type memApplier struct {
	fs      *memFS
	err     error
	applied []confedit.Change
}

func (a *memApplier) Apply(_ context.Context, ch confedit.Change) (confedit.Result, error) {
	a.applied = append(a.applied, ch)
	if a.err != nil {
		return confedit.Result{}, a.err
	}
	for _, f := range ch.Files {
		if f.Delete {
			_ = a.fs.Remove(f.Path)
			continue
		}
		_ = a.fs.WriteFile(f.Path, f.Content, 0o644)
	}
	return confedit.Result{}, nil
}

func (a *memApplier) paths() []string {
	var out []string
	for _, ch := range a.applied {
		for _, f := range ch.Files {
			out = append(out, f.Path)
		}
	}
	return out
}

type fakeSvc struct {
	validateOut string
	validateErr error
	reloads     int
}

func (s *fakeSvc) Name() string { return "nginx" }
func (s *fakeSvc) Validate(context.Context) (string, error) {
	return s.validateOut, s.validateErr
}
func (s *fakeSvc) Reload(context.Context) error  { s.reloads++; return nil }
func (s *fakeSvc) Healthy(context.Context) error { return nil }

type rig struct {
	m   *Manager
	fs  *memFS
	ap  *memApplier
	svc *fakeSvc
	st  *store.Store
}

func newRig(t *testing.T) *rig { return newRigTLS(t, nil) }

func newRigTLS(t *testing.T, tls TLSState) *rig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mfs := newMemFS()
	ap := &memApplier{fs: mfs}
	svc := &fakeSvc{}
	m, err := New(Options{
		Store: st, Applier: ap, Service: svc, Paths: nginx.Paths{Root: "/www"}, FS: mfs, TLS: tls,
		Now: func() time.Time { return time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{m: m, fs: mfs, ap: ap, svc: svc, st: st}
}

var ctx = context.Background()

func TestNormalizeDomain(t *testing.T) {
	good := map[string]string{
		"Example.COM":      "example.com",
		" a.test. ":        "a.test",
		"bücher.test":      "xn--bcher-kva.test",
		"xn--bcher-kva.de": "xn--bcher-kva.de",
		"localhost":        "localhost",
		"a-b.c1.test":      "a-b.c1.test",
	}
	for in, want := range good {
		if got, err := NormalizeDomain("domain", in); err != nil || got != want {
			t.Errorf("NormalizeDomain(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "   ", "*.example.com", "a b.test", "a.test/path", "http://a.test", "a.test:80", "user@a.test",
		"192.168.0.1", "::1", "-a.test", "a-.test", "a..test", ".a.test", "a_b.test", "a.test;x", "a.test\nb",
		strings.Repeat("a", 64) + ".test", strings.Repeat("a.", 130) + "test",
	} {
		if got, err := NormalizeDomain("domain", in); err == nil {
			t.Errorf("NormalizeDomain(%q) = %q, want an error", in, got)
		} else {
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != "domain" {
				t.Errorf("NormalizeDomain(%q): %v is not a ValidationError on domain", in, err)
			}
		}
	}
}

func TestCreateStaticSite(t *testing.T) {
	r := newRig(t)
	res, err := r.m.Create(ctx, CreateInput{Domain: "A.test", Aliases: []string{"www.a.test"}, Kind: KindStatic})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Site
	if s.Domain != "a.test" || s.Root != "/www/services/sites/a.test" || !slices.Equal(s.Aliases, []string{"www.a.test"}) {
		t.Errorf("site = %+v", s)
	}
	if !r.fs.has("/www/services/sites/a.test/index.html") || r.fs.owner["/www/services/sites/a.test"] != 1500 || r.fs.owner["/www/services/sites/a.test/index.html"] != 1500 {
		t.Error("the web root and its placeholder page must exist and belong to www (1500)")
	}
	for _, p := range []string{
		"/www/panel/nginx/sites/a.test.conf",
	} {
		if !r.fs.has(p) {
			t.Errorf("%s was not written", p)
		}
	}
	for _, p := range []string{"listen", "names", "acme", "tls"} {
		if r.fs.has("/www/panel/nginx/includes/a.test/" + p + ".conf") {
			t.Errorf("%s.conf was written; those parts are blocks of the site file now", p)
		}
	}
	site, _ := r.fs.ReadFile("/www/panel/nginx/sites/a.test.conf")
	if b := blockOf(t, string(site), "names"); !strings.Contains(b, "server_name a.test www.a.test;") {
		t.Errorf("names block = %q", b)
	}
	if b := blockOf(t, string(site), "listen"); !strings.Contains(b, "listen 80;") {
		t.Errorf("listen block = %q", b)
	}
	if r.fs.has("/www/panel/nginx/includes/a.test/proxy.conf") {
		t.Error("a static site got a proxy.conf")
	}
	if len(r.ap.applied) != 1 || len(r.ap.applied[0].Services) != 1 {
		t.Errorf("applies = %d, want one change through the nginx service", len(r.ap.applied))
	}
	got, err := r.m.Get(ctx, "a.test")
	if err != nil || got.Domain != "a.test" || got.CreatedAt.IsZero() || got.PHPVersion != nil {
		t.Errorf("Get = %+v, %v", got, err)
	}
}

func TestCreateProxySite(t *testing.T) {
	r := newRig(t)
	res, err := r.m.Create(ctx, CreateInput{Domain: "api.test", Kind: KindProxy, ProxyTarget: "http://127.0.0.1:3000/"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Site.ProxyTarget != "http://127.0.0.1:3000" || res.Site.Root != "" {
		t.Errorf("site = %+v", res.Site)
	}
	proxy, err := r.fs.ReadFile("/www/panel/nginx/includes/api.test/proxy.conf")
	if err != nil || !strings.Contains(string(proxy), "proxy_pass http://127.0.0.1:3000;") {
		t.Errorf("proxy.conf = %q, %v", proxy, err)
	}
	if r.fs.has("/www/services/sites/api.test") {
		t.Error("a proxy site got a web root")
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	r := newRig(t)
	for name, in := range map[string]CreateInput{
		"no domain":          {Kind: KindStatic},
		"wildcard":           {Domain: "*.a.test", Kind: KindStatic},
		"unknown kind":       {Domain: "a.test", Kind: "php"},
		"alias is primary":   {Domain: "a.test", Aliases: []string{"A.test"}, Kind: KindStatic},
		"repeated alias":     {Domain: "a.test", Aliases: []string{"b.test", "b.test"}, Kind: KindStatic},
		"proxy w/o target":   {Domain: "a.test", Kind: KindProxy},
		"proxy with path":    {Domain: "a.test", Kind: KindProxy, ProxyTarget: "http://x.test/app"},
		"static w/ target":   {Domain: "a.test", Kind: KindStatic, ProxyTarget: "http://x.test"},
		"proxy w/ root":      {Domain: "a.test", Kind: KindProxy, ProxyTarget: "http://x.test", Root: "/www/services/sites/a.test"},
		"root outside sites": {Domain: "a.test", Kind: KindStatic, Root: "/etc"},
		"root is sites dir":  {Domain: "a.test", Kind: KindStatic, Root: "/www/services/sites"},
		"root traversal":     {Domain: "a.test", Kind: KindStatic, Root: "/www/services/sites/../../../etc"},
		"root injection":     {Domain: "a.test", Kind: KindStatic, Root: "/www/services/sites/a;b"},
	} {
		_, err := r.m.Create(ctx, in)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want a ValidationError", name, err)
		}
	}
	if len(r.ap.applied) != 0 {
		t.Error("invalid input reached confedit")
	}
	if list, _ := r.m.List(ctx); len(list) != 0 {
		t.Errorf("invalid input left rows behind: %+v", list)
	}
}

func TestDuplicateDomainsAreRejected(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Aliases: []string{"www.a.test"}, Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]CreateInput{
		"same primary":             {Domain: "a.test", Kind: KindStatic},
		"primary is an alias":      {Domain: "www.a.test", Kind: KindStatic},
		"alias is a primary":       {Domain: "b.test", Aliases: []string{"a.test"}, Kind: KindStatic},
		"alias is an alias":        {Domain: "b.test", Aliases: []string{"www.a.test"}, Kind: KindStatic},
		"same after normalization": {Domain: "A.TEST.", Kind: KindStatic},
	} {
		before := len(r.ap.applied)
		_, err := r.m.Create(ctx, in)
		if !errors.Is(err, ErrConflict) {
			t.Errorf("%s: err = %v, want ErrConflict", name, err)
		}
		if len(r.ap.applied) != before {
			t.Errorf("%s: a conflicting site reached confedit", name)
		}
	}
	list, _ := r.m.List(ctx)
	if len(list) != 1 {
		t.Errorf("sites = %+v, want only a.test", list)
	}
}

func TestFailedApplyLeavesNothing(t *testing.T) {
	r := newRig(t)
	r.ap.err = &confedit.ApplyError{Stage: confedit.StageValidate, Service: "nginx", Output: "nginx: [emerg] bad", Err: errors.New("rejected")}

	_, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Aliases: []string{"www.a.test"}, Kind: KindStatic})
	var ae *confedit.ApplyError
	if !errors.As(err, &ae) || ae.Output != "nginx: [emerg] bad" {
		t.Fatalf("err = %v, want the validator output", err)
	}
	if list, _ := r.m.List(ctx); len(list) != 0 {
		t.Errorf("rows left behind: %+v", list)
	}
	if r.fs.has("/www/services/sites/a.test") || r.fs.has("/www/services/sites/a.test/index.html") {
		t.Error("the web root created for the failed site was left behind")
	}

	// The name is free again.
	r.ap.err = nil
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Aliases: []string{"www.a.test"}, Kind: KindStatic}); err != nil {
		t.Errorf("retry after a failed create: %v", err)
	}
}

func TestFailedApplyKeepsAnExistingWebRoot(t *testing.T) {
	r := newRig(t)
	r.fs.dirs["/www/services/sites/a.test"] = true
	r.fs.files["/www/services/sites/a.test/data.txt"] = []byte("mine")
	r.ap.err = errors.New("boom")

	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err == nil {
		t.Fatal("create succeeded")
	}
	if !r.fs.has("/www/services/sites/a.test") || !r.fs.has("/www/services/sites/a.test/data.txt") {
		t.Error("a directory the operator already had was removed")
	}
	if r.fs.has("/www/services/sites/a.test/index.html") {
		t.Error("a placeholder was written into an existing directory")
	}
}

func TestChownFailureUndoesTheCreate(t *testing.T) {
	r := newRig(t)
	r.fs.failChown = true
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err == nil {
		t.Fatal("create succeeded although the web root could not be given to www")
	}
	if list, _ := r.m.List(ctx); len(list) != 0 {
		t.Errorf("rows left behind: %+v", list)
	}
	if r.fs.has("/www/services/sites/a.test") || len(r.ap.applied) != 0 {
		t.Error("files or config were written for a site whose web root failed")
	}
}

func TestDeleteRemovesConfigAndKeepsTheWebRoot(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	res, err := r.m.Delete(ctx, "a.test")
	if err != nil {
		t.Fatal(err)
	}
	if res.WebRoot != "/www/services/sites/a.test" {
		t.Errorf("WebRoot = %q: the result must say where the kept content is", res.WebRoot)
	}
	for _, p := range []string{"/www/panel/nginx/sites/a.test.conf", "/www/panel/nginx/includes/a.test/names.conf"} {
		if r.fs.has(p) {
			t.Errorf("%s survived the delete", p)
		}
	}
	if !r.fs.has("/www/services/sites/a.test/index.html") {
		t.Error("the web root was deleted; it is kept until M3")
	}
	if _, err := r.m.Get(ctx, "a.test"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
	// Aliases are gone too, so the names can be reused.
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Errorf("recreate after delete: %v", err)
	}
}

func TestFailedDeleteKeepsTheSite(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Aliases: []string{"www.a.test"}, Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	r.ap.err = errors.New("nginx would not reload")
	if _, err := r.m.Delete(ctx, "a.test"); err == nil {
		t.Fatal("delete succeeded")
	}
	s, err := r.m.Get(ctx, "a.test")
	if err != nil || len(s.Aliases) != 1 {
		t.Errorf("site after a failed delete = %+v, %v", s, err)
	}
	if _, err := r.m.Delete(ctx, "nope.test"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete of an unknown site = %v, want ErrNotFound", err)
	}
}

func TestDetachedWhenAnIncludeLineIsRemoved(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindProxy, ProxyTarget: "http://127.0.0.1:3000"}); err != nil {
		t.Fatal(err)
	}
	d, err := r.m.Detail(ctx, "a.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Features) != 5 {
		t.Fatalf("features = %+v, want listen, names, acme, tls, proxy", d.Features)
	}
	for _, f := range d.Features {
		if !f.Attached {
			t.Errorf("%s is detached on a freshly created site", f.Feature)
		}
	}

	// The operator deletes the proxy include and removes the names block's markers.
	file := "/www/panel/nginx/sites/a.test.conf"
	b, _ := r.fs.ReadFile(file)
	edited := strings.Replace(string(b), "    include /www/panel/nginx/includes/a.test/proxy.conf;\n", "", 1)
	edited = strings.Replace(edited, "    # >>> pb:names\n", "", 1)
	edited = strings.Replace(edited, "    # <<< pb:names\n", "", 1)
	r.fs.files[file] = []byte(edited + "\n# my own note\n")

	d, _ = r.m.Detail(ctx, "a.test")
	state := map[Feature]bool{}
	for _, f := range d.Features {
		state[f.Feature] = f.Attached
		if !f.Attached && f.Reason == "" {
			t.Errorf("%s is detached without an explanation", f.Feature)
		}
	}
	if !state[FeatureListen] || !state[FeatureACME] || state[FeatureNames] || state[FeatureProxy] {
		t.Errorf("states = %v, want listen and acme attached, names and proxy detached", state)
	}

	before := len(r.ap.applied)
	newTarget := "http://127.0.0.1:4000"
	_, err = r.m.Update(ctx, "a.test", UpdateInput{ProxyTarget: &newTarget})
	var de *DetachedError
	if !errors.Is(err, ErrDetached) || !errors.As(err, &de) || de.Feature != FeatureProxy {
		t.Errorf("Update of a detached proxy = %v, want a DetachedError for proxy", err)
	}
	aliases := []string{"www.a.test"}
	if _, err := r.m.Update(ctx, "a.test", UpdateInput{Aliases: &aliases}); !errors.Is(err, ErrDetached) {
		t.Errorf("Update of detached names = %v, want ErrDetached", err)
	}
	if len(r.ap.applied) != before {
		t.Error("a detached control still wrote a file")
	}
	for _, f := range d.Features {
		if f.Feature == FeatureNames && !strings.Contains(f.Reason, "pb:names") {
			t.Errorf("names reason = %q, want it to name the markers", f.Reason)
		}
	}
	if s, _ := r.m.Get(ctx, "a.test"); s.ProxyTarget != "http://127.0.0.1:3000" || len(s.Aliases) != 0 {
		t.Errorf("a refused update changed the database: %+v", s)
	}

	// Regenerate brings it back.
	if _, err := r.m.Regenerate(ctx, "a.test"); err != nil {
		t.Fatal(err)
	}
	d, _ = r.m.Detail(ctx, "a.test")
	for _, f := range d.Features {
		if !f.Attached {
			t.Errorf("%s is still detached after regenerate", f.Feature)
		}
	}
	if _, err := r.m.Update(ctx, "a.test", UpdateInput{ProxyTarget: &newTarget}); err != nil {
		t.Errorf("Update after regenerate: %v", err)
	}
}

func TestMissingSiteFileDetachesEverything(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	_ = r.fs.Remove("/www/panel/nginx/sites/a.test.conf")
	d, err := r.m.Detail(ctx, "a.test")
	if err != nil || !d.SiteFileMissing {
		t.Fatalf("Detail = %+v, %v", d, err)
	}
	for _, f := range d.Features {
		if f.Attached {
			t.Errorf("%s attached to a site file that does not exist", f.Feature)
		}
	}
}

// blockOf returns the text between a block's marker lines, or fails the test
// when the block is not there.
func blockOf(t *testing.T, content, name string) string {
	t.Helper()
	begin, end := nginx.BlockMarkers(name)
	lines := strings.Split(content, "\n")
	from := -1
	for i, l := range lines {
		switch tl := strings.TrimSpace(l); {
		case from < 0 && tl == begin:
			from = i
		case from >= 0 && tl == end:
			return strings.Join(lines[from+1:i], "\n")
		}
	}
	t.Fatalf("no %q block in:\n%s", name, content)
	return ""
}

// legacySite replaces a site's one-file config by the layout older versions
// wrote: a site file with include lines and no blocks, and one file per part.
// Returns the include path of each part.
func legacySite(t *testing.T, r *rig, domain string, aliases []string) map[string]string {
	t.Helper()
	dir := "/www/panel/nginx/includes/" + domain + "/"
	inc := map[string]string{}
	for _, n := range []string{"listen", "names", "acme", "tls", "proxy"} {
		inc[n] = dir + n + ".conf"
	}
	names, _ := nginx.RenderNames(domain, aliases)
	r.fs.files[inc["names"]] = names
	r.fs.files[inc["tls"]] = []byte("# Managed by PB panel\n")
	r.fs.files[inc["listen"]] = []byte("listen 80;\n")
	r.fs.files[inc["acme"]] = []byte("# acme\n")
	r.fs.files[inc["proxy"]] = []byte("proxy_pass http://127.0.0.1:3000;\n")
	site := "# Managed by PB panel for " + domain + ".\nserver {\n"
	for _, n := range []string{"listen", "names", "acme", "tls"} {
		site += "    include " + inc[n] + ";\n"
	}
	site += "\n    include " + inc["proxy"] + ";\n}\n"
	r.fs.files["/www/panel/nginx/sites/"+domain+".conf"] = []byte(site)
	return inc
}

func TestUpdateRewritesOnlyItsOwnPart(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Aliases: []string{"old.a.test"}, Kind: KindProxy, ProxyTarget: "http://127.0.0.1:3000"}); err != nil {
		t.Fatal(err)
	}
	const siteFile = "/www/panel/nginx/sites/a.test.conf"
	// The operator's own lines around the blocks must survive an edit.
	b, _ := r.fs.ReadFile(siteFile)
	custom := strings.Replace(string(b), "    access_log", "    # my own directive\n    client_max_body_size 50m;\n    access_log", 1)
	custom = "# header note\n" + custom
	r.fs.files[siteFile] = []byte(custom)
	r.ap.applied = nil

	aliases := []string{"new.a.test", "B.test"}
	res, err := r.m.Update(ctx, "a.test", UpdateInput{Aliases: &aliases})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Site.Aliases, []string{"new.a.test", "b.test"}) {
		t.Errorf("aliases = %v", res.Site.Aliases)
	}
	if got := r.ap.paths(); !slices.Equal(got, []string{siteFile}) {
		t.Errorf("an alias edit wrote %v, want only the site file", got)
	}
	after, _ := r.fs.ReadFile(siteFile)
	if got := blockOf(t, string(after), "names"); !strings.Contains(got, "server_name a.test new.a.test b.test;") || strings.Contains(got, "old.a.test") {
		t.Errorf("names block = %q", got)
	}
	// Only the names block moved: swap it back and the file is byte-identical.
	oldNames := blockOf(t, custom, "names")
	if got := strings.Replace(string(after), blockOf(t, string(after), "names"), oldNames, 1); got != custom {
		t.Errorf("something besides the names block changed:\n%s", after)
	}
	if r.fs.has("/www/panel/nginx/includes/a.test/names.conf") {
		t.Error("names.conf was written for a site that keeps its names in a block")
	}

	r.ap.applied = nil
	target := "https://backend.test:8443"
	if _, err := r.m.Update(ctx, "a.test", UpdateInput{ProxyTarget: &target}); err != nil {
		t.Fatal(err)
	}
	if got := r.ap.paths(); !slices.Equal(got, []string{"/www/panel/nginx/includes/a.test/proxy.conf"}) {
		t.Errorf("a target edit wrote %v, want only proxy.conf", got)
	}
}

// A site file from before blocks has include lines and no markers: an alias edit
// keeps writing names.conf and leaves the site file alone.
func TestUpdateOfALegacySiteWritesItsNamesInclude(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindProxy, ProxyTarget: "http://127.0.0.1:3000"}); err != nil {
		t.Fatal(err)
	}
	inc := legacySite(t, r, "a.test", nil)
	siteBefore, _ := r.fs.ReadFile("/www/panel/nginx/sites/a.test.conf")
	r.ap.applied = nil

	d, err := r.m.Detail(ctx, "a.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range d.Features {
		if !f.Attached {
			t.Errorf("%s of a legacy site is detached: %s", f.Feature, f.Reason)
		}
	}
	aliases := []string{"www.a.test"}
	if _, err := r.m.Update(ctx, "a.test", UpdateInput{Aliases: &aliases}); err != nil {
		t.Fatal(err)
	}
	if got := r.ap.paths(); !slices.Equal(got, []string{inc["names"]}) {
		t.Errorf("an alias edit of a legacy site wrote %v, want only names.conf", got)
	}
	names, _ := r.fs.ReadFile(inc["names"])
	if !strings.Contains(string(names), "server_name a.test www.a.test;") {
		t.Errorf("names.conf = %q", names)
	}
	siteAfter, _ := r.fs.ReadFile("/www/panel/nginx/sites/a.test.conf")
	if string(siteBefore) != string(siteAfter) {
		t.Error("the legacy site file changed")
	}
}

func TestUpdateFailureRestoresTheDatabase(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Aliases: []string{"keep.test"}, Kind: KindProxy, ProxyTarget: "http://127.0.0.1:3000"}); err != nil {
		t.Fatal(err)
	}
	r.ap.err = errors.New("rejected")
	aliases := []string{"other.test"}
	target := "http://127.0.0.1:9"
	if _, err := r.m.Update(ctx, "a.test", UpdateInput{Aliases: &aliases, ProxyTarget: &target}); err == nil {
		t.Fatal("update succeeded")
	}
	s, _ := r.m.Get(ctx, "a.test")
	if !slices.Equal(s.Aliases, []string{"keep.test"}) || s.ProxyTarget != "http://127.0.0.1:3000" {
		t.Errorf("site after a failed update = %+v", s)
	}
}

func TestUpdateRejectsBadInput(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Create(ctx, CreateInput{Domain: "b.test", Kind: KindProxy, ProxyTarget: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	var ve *ValidationError
	if _, err := r.m.Update(ctx, "a.test", UpdateInput{}); !errors.As(err, &ve) {
		t.Errorf("empty update = %v", err)
	}
	target := "http://x.test"
	if _, err := r.m.Update(ctx, "a.test", UpdateInput{ProxyTarget: &target}); !errors.As(err, &ve) {
		t.Errorf("proxy target on a static site = %v", err)
	}
	bad := "http://x.test/path"
	if _, err := r.m.Update(ctx, "b.test", UpdateInput{ProxyTarget: &bad}); !errors.As(err, &ve) {
		t.Errorf("bad target = %v", err)
	}
	taken := []string{"a.test"}
	if _, err := r.m.Update(ctx, "b.test", UpdateInput{Aliases: &taken}); !errors.Is(err, ErrConflict) {
		t.Errorf("alias that is another site's domain = %v, want ErrConflict", err)
	}
	if _, err := r.m.Update(ctx, "zzz.test", UpdateInput{Aliases: &taken}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown site = %v", err)
	}
}

// --- reconcile -------------------------------------------------------------

type fakeRuntime struct {
	present  bool
	action   nginx.EnsureAction
	ensured  int
	ensureEr error
}

func (f *fakeRuntime) BuildPresent(context.Context) (bool, error) { return f.present, nil }
func (f *fakeRuntime) EnsureRunning(context.Context) (nginx.EnsureResult, error) {
	f.ensured++
	return nginx.EnsureResult{Action: f.action, Reason: "test"}, f.ensureEr
}

func newReconciler(t *testing.T, r *rig, rt *fakeRuntime) (*Reconciler, *fakeSvc) {
	t.Helper()
	live := &fakeSvc{}
	g := nginx.NewGlobal(r.st, r.ap, live, nginx.Paths{Root: "/www"})
	return &Reconciler{
		Sites: r.m, Global: g, Recover: func() ([]string, error) { return nil, nil },
		Boot: r.svc, Live: live, Runtime: rt,
	}, live
}

func TestReconcileRestoresMissingContainerAndFiles(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Aliases: []string{"www.a.test"}, Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	// A fresh host after a restore: no nginx.conf, no panel files, and the names
	// block of the site file damaged, next to a line of the operator's.
	const siteConf = "/www/panel/nginx/sites/a.test.conf"
	rt := &fakeRuntime{present: true, action: nginx.ActionStart}
	rec, live := newReconciler(t, r, rt)
	cur, _ := r.fs.ReadFile(siteConf)
	damaged := strings.Replace(string(cur), "server_name a.test www.a.test;", "server_name stale.test;", 1)
	r.fs.files[siteConf] = append([]byte(damaged), []byte("# operator edit\n")...)
	r.ap.applied = nil

	st := rec.Run(ctx)
	if !st.OK {
		t.Fatalf("status = %+v", st)
	}
	if rt.ensured != 1 {
		t.Errorf("EnsureRunning calls = %d, want 1", rt.ensured)
	}
	for _, p := range []string{
		"/www/panel/nginx/nginx.conf", "/www/panel/nginx/panel-main.conf", "/www/panel/nginx/panel-http.conf",
	} {
		if !r.fs.has(p) {
			t.Errorf("%s was not restored", p)
		}
	}
	got, _ := r.fs.ReadFile(siteConf)
	if b := blockOf(t, string(got), "names"); !strings.Contains(b, "server_name a.test www.a.test;") || strings.Contains(b, "stale.test") {
		t.Errorf("the damaged names block was not restored: %q", b)
	}
	if !strings.HasSuffix(string(got), "# operator edit\n") {
		t.Error("the operator's edit to the site file was lost")
	}
	if live.reloads != 0 {
		t.Errorf("a freshly created container was reloaded %d time(s); it started with the files", live.reloads)
	}
	if len(r.ap.applied) != 1 || r.ap.applied[0].Services[0] != confedit.Service(r.svc) {
		t.Errorf("the files must be written once and validated by the boot service: %+v", r.ap.applied)
	}
}

func TestReconcileReloadsARunningContainerOnce(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	rt := &fakeRuntime{present: true, action: nginx.ActionNone}
	rec, live := newReconciler(t, r, rt)

	if st := rec.Run(ctx); !st.OK { // first run writes the missing global files
		t.Fatalf("status = %+v", st)
	}
	r.ap.applied = nil
	live.reloads = 0
	if st := rec.Run(ctx); !st.OK {
		t.Fatalf("status = %+v", st)
	}
	if len(r.ap.applied) != 0 {
		t.Errorf("a second run on a current host wrote %v", r.ap.paths())
	}
	if live.reloads != 1 {
		t.Errorf("reloads = %d, want exactly one", live.reloads)
	}
	if got := rec.Status(); !got.Done || !got.OK || len(got.Steps) == 0 {
		t.Errorf("Status() = %+v", got)
	}
}

func TestReconcileDoesNotStartNginxOnABrokenConfig(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	rt := &fakeRuntime{present: true, action: nginx.ActionStart}
	rec, _ := newReconciler(t, r, rt)
	rec.Run(ctx) // writes everything

	r.svc.validateOut = "nginx: [emerg] unknown directive \"bogus\" in /www/panel/nginx/sites/a.test.conf:9"
	r.svc.validateErr = errors.New("nginx -t rejected the configuration (exit 1)")
	rt.ensured = 0
	st := rec.Run(ctx)
	if st.OK || rt.ensured != 0 {
		t.Errorf("status = %+v, ensured = %d: a broken config must stop the run before the container is touched", st, rt.ensured)
	}
	if !strings.Contains(st.Output, "unknown directive") {
		t.Errorf("Output = %q, want the validator's text", st.Output)
	}
}

func TestReconcileBuildsAMissingImageAndStopsWhenItCannot(t *testing.T) {
	r := newRig(t)
	rt := &fakeRuntime{present: false, action: nginx.ActionStart}
	rec, _ := newReconciler(t, r, rt)

	if st := rec.Run(ctx); st.OK || rt.ensured != 0 {
		t.Errorf("with no way to build the image: status = %+v", st)
	}

	built := 0
	rec.BuildImage = func(context.Context) error { built++; rt.present = true; return nil }
	if st := rec.Run(ctx); !st.OK || built != 1 {
		t.Errorf("status = %+v, built = %d", st, built)
	}
}

func TestReconcileRecoversBeforeAnythingElse(t *testing.T) {
	r := newRig(t)
	rt := &fakeRuntime{present: true, action: nginx.ActionNone}
	rec, _ := newReconciler(t, r, rt)
	rec.Recover = func() ([]string, error) { return []string{"/www/panel/nginx/nginx.conf"}, nil }
	st := rec.Run(ctx)
	if len(st.Steps) == 0 || st.Steps[0].Name != "recover" || !strings.Contains(st.Steps[0].Detail, "1 file") {
		t.Errorf("steps = %+v, want recover first", st.Steps)
	}

	rec.Recover = func() ([]string, error) { return nil, errors.New("journal unreadable") }
	rt.ensured = 0
	if st := rec.Run(ctx); st.OK || rt.ensured != 0 {
		t.Errorf("a failed recovery must stop the reconcile: %+v", st)
	}
}
