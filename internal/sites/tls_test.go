package sites

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/nginx"
)

// fakeTLS is a certificate store in a map; err makes every lookup fail.
type fakeTLS struct {
	certs map[string]TLSInfo
	err   error
}

func (f *fakeTLS) Lookup(_ context.Context, domain string) (TLSInfo, bool, error) {
	if f.err != nil {
		return TLSInfo{}, false, f.err
	}
	i, ok := f.certs[domain]
	return i, ok, nil
}

func certFor(domain string, redirect bool) TLSInfo {
	dir := "/www/panel/ssl/sites/" + domain + "/"
	return TLSInfo{CertFile: dir + "fullchain.pem", KeyFile: dir + "privkey.pem", RedirectHTTP: redirect}
}

const (
	tlsPath  = "/www/panel/nginx/includes/a.test/tls.conf"
	siteFile = "/www/panel/nginx/sites/a.test.conf"
)

// tlsBlock is the TLS block of a.test's site file.
func tlsBlock(t *testing.T, r *rig) string {
	t.Helper()
	b, err := r.fs.ReadFile(siteFile)
	if err != nil {
		t.Fatal(err)
	}
	return blockOf(t, string(b), "tls")
}

func TestSiteWithoutACertificateGetsAnEmptyTLSBlock(t *testing.T) {
	for name, state := range map[string]TLSState{"no store": nil, "empty store": &fakeTLS{}} {
		r := newRigTLS(t, state)
		if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
			t.Fatal(err)
		}
		got := tlsBlock(t, r)
		if strings.Contains(got, "listen") || strings.Contains(got, "ssl_certificate") {
			t.Errorf("%s: the tls block of a site with no certificate has directives:\n%s", name, got)
		}
		site, _ := r.fs.ReadFile(siteFile)
		if !nginx.HasBlock(site, nginx.BlockTLS) || nginx.HasIncludeLine(site, tlsPath) {
			t.Errorf("%s: the site file must carry the tls block and no tls include", name)
		}
		if r.fs.has(tlsPath) {
			t.Errorf("%s: tls.conf was written", name)
		}
	}
}

func TestCreateOfASiteWithAStoredCertificateIsHTTPS(t *testing.T) {
	r := newRigTLS(t, &fakeTLS{certs: map[string]TLSInfo{"a.test": certFor("a.test", true)}})
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	got := tlsBlock(t, r)
	for _, want := range []string{"listen 443 ssl;", "ssl_certificate /www/panel/ssl/sites/a.test/fullchain.pem;", "return 301 https://$host$request_uri;"} {
		if !strings.Contains(got, want) {
			t.Errorf("tls block lacks %q:\n%s", want, got)
		}
	}
}

func TestRefreshTLSRewritesOnlyTheTLSBlock(t *testing.T) {
	store := &fakeTLS{certs: map[string]TLSInfo{}}
	r := newRigTLS(t, store)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	// The operator's own lines in the site file must outlive a refresh.
	cur, _ := r.fs.ReadFile(siteFile)
	site := strings.Replace(string(cur), "    access_log", "    client_max_body_size 50m;\n    access_log", 1)
	r.fs.files[siteFile] = []byte(site)
	r.ap.applied = nil

	store.certs["a.test"] = certFor("a.test", false)
	if _, err := r.m.RefreshTLS(ctx, "a.test"); err != nil {
		t.Fatal(err)
	}
	if len(r.ap.applied) != 1 || len(r.ap.applied[0].Files) != 1 || r.ap.applied[0].Files[0].Path != siteFile {
		t.Fatalf("refresh wrote %v, want only the site file", r.ap.paths())
	}
	if len(r.ap.applied[0].Services) != 1 || r.ap.applied[0].Services[0] != r.svc {
		t.Errorf("refresh must validate and reload through the nginx service: %+v", r.ap.applied[0].Services)
	}
	got := tlsBlock(t, r)
	if !strings.Contains(got, "listen 443 ssl;") || strings.Contains(got, "return 301") {
		t.Errorf("tls block after issuing:\n%s", got)
	}
	after, _ := r.fs.ReadFile(siteFile)
	if !strings.Contains(string(after), "client_max_body_size 50m;") {
		t.Error("RefreshTLS lost the operator's line")
	}
	if outsideBlock(string(after), nginx.BlockTLS) != outsideBlock(site, nginx.BlockTLS) {
		t.Errorf("RefreshTLS changed something besides the tls block:\n%s", after)
	}
	if r.fs.has(tlsPath) {
		t.Error("tls.conf was written for a site that keeps TLS in a block")
	}

	// Removing the certificate takes the listener away again.
	delete(store.certs, "a.test")
	if _, err := r.m.RefreshTLS(ctx, "a.test"); err != nil {
		t.Fatal(err)
	}
	if got := tlsBlock(t, r); strings.Contains(got, "listen") {
		t.Errorf("tls block after removal:\n%s", got)
	}
}

// outsideBlock is content with the lines between a block's markers removed.
func outsideBlock(content, name string) string {
	begin, end := nginx.BlockMarkers(name)
	var out []string
	in := false
	for _, l := range strings.Split(content, "\n") {
		switch tl := strings.TrimSpace(l); {
		case !in && tl == begin:
			in = true
			out = append(out, l)
		case in && tl == end:
			in = false
			out = append(out, l)
		case !in:
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func TestRefreshTLSHonoursADetachedBlock(t *testing.T) {
	r := newRigTLS(t, &fakeTLS{certs: map[string]TLSInfo{"a.test": certFor("a.test", false)}})
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	b, _ := r.fs.ReadFile(siteFile)
	edited := strings.Replace(string(b), "    # >>> pb:tls\n", "", 1)
	edited = strings.Replace(edited, "    # <<< pb:tls\n", "", 1)
	if edited == string(b) {
		t.Fatal("the markers were not found to remove")
	}
	r.fs.files[siteFile] = []byte(edited)
	r.ap.applied = nil

	_, err := r.m.RefreshTLS(ctx, "a.test")
	var de *DetachedError
	if !errors.Is(err, ErrDetached) || !errors.As(err, &de) || de.Feature != FeatureTLS {
		t.Fatalf("RefreshTLS = %v, want a DetachedError for tls", err)
	}
	if len(r.ap.applied) != 0 {
		t.Errorf("a detached tls block was written anyway: %v", r.ap.paths())
	}
	d, _ := r.m.Detail(ctx, "a.test")
	for _, f := range d.Features {
		if f.Feature == FeatureTLS && (f.Attached || !strings.Contains(f.Reason, "pb:tls")) {
			t.Errorf("tls feature = %+v, want detached with a reason naming the markers", f)
		}
	}
}

// A site file from before blocks keeps its tls.conf include: a refresh writes that
// file and leaves the site file alone, and an include line removed still detaches.
func TestRefreshTLSOfALegacySiteWritesTLSConf(t *testing.T) {
	store := &fakeTLS{certs: map[string]TLSInfo{"a.test": certFor("a.test", false)}}
	r := newRigTLS(t, store)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindProxy, ProxyTarget: "http://127.0.0.1:3000"}); err != nil {
		t.Fatal(err)
	}
	inc := legacySite(t, r, "a.test", nil)
	siteBefore, _ := r.fs.ReadFile(siteFile)
	r.ap.applied = nil

	if _, err := r.m.RefreshTLS(ctx, "a.test"); err != nil {
		t.Fatal(err)
	}
	if got := r.ap.paths(); len(got) != 1 || got[0] != inc["tls"] || inc["tls"] != tlsPath {
		t.Fatalf("refresh of a legacy site wrote %v, want only tls.conf", got)
	}
	got, _ := r.fs.ReadFile(tlsPath)
	if !strings.Contains(string(got), "listen 443 ssl;") {
		t.Errorf("tls.conf after issuing:\n%s", got)
	}
	after, _ := r.fs.ReadFile(siteFile)
	if string(after) != string(siteBefore) {
		t.Error("RefreshTLS touched the legacy site file")
	}

	// Remove the include line: detached, nothing written.
	r.fs.files[siteFile] = []byte(strings.Replace(string(siteBefore), "    include "+tlsPath+";\n", "", 1))
	r.ap.applied = nil
	if _, err := r.m.RefreshTLS(ctx, "a.test"); !errors.Is(err, ErrDetached) {
		t.Errorf("RefreshTLS with the include line gone = %v, want ErrDetached", err)
	}
	if len(r.ap.applied) != 0 {
		t.Errorf("a detached include was written: %v", r.ap.paths())
	}
}

func TestRefreshTLSErrors(t *testing.T) {
	r := newRigTLS(t, &fakeTLS{err: errors.New("store is locked")})
	if _, err := r.m.RefreshTLS(ctx, "nope.test"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown site = %v, want ErrNotFound", err)
	}
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err == nil {
		t.Error("a certificate lookup failure was swallowed on create")
	}

	bad := newRigTLS(t, &fakeTLS{certs: map[string]TLSInfo{"a.test": {CertFile: "/etc/passwd", KeyFile: "/etc/shadow"}}})
	if _, err := bad.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err == nil {
		t.Error("certificate paths outside the site's directory were rendered")
	}
}

func TestReconcileReproducesTheTLSBlock(t *testing.T) {
	r := newRigTLS(t, &fakeTLS{certs: map[string]TLSInfo{"a.test": certFor("a.test", true)}})
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	want := tlsBlock(t, r)
	if !strings.Contains(want, "listen 443 ssl;") {
		t.Fatalf("the created site has no HTTPS block:\n%s", want)
	}
	// The block is emptied (a restore from an older backup, say) next to an operator line.
	cur, _ := r.fs.ReadFile(siteFile)
	broken := strings.Replace(string(cur), want, "    # stale", 1) + "# operator edit\n"
	r.fs.files[siteFile] = []byte(broken)

	rec, _ := newReconciler(t, r, &fakeRuntime{present: true, action: nginx.ActionNone})
	if st := rec.Run(ctx); !st.OK {
		t.Fatalf("status = %+v", st)
	}
	if got := tlsBlock(t, r); got != want {
		t.Errorf("tls block after reconcile = %q; want the rendered one %q", got, want)
	}
	after, _ := r.fs.ReadFile(siteFile)
	if !strings.HasSuffix(string(after), "# operator edit\n") {
		t.Error("reconcile lost the operator's line")
	}
}

// Reconcile of a legacy site restores its tls.conf include file, not a block.
func TestReconcileRestoresALegacyTLSInclude(t *testing.T) {
	r := newRigTLS(t, &fakeTLS{certs: map[string]TLSInfo{"a.test": certFor("a.test", true)}})
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindProxy, ProxyTarget: "http://127.0.0.1:3000"}); err != nil {
		t.Fatal(err)
	}
	legacySite(t, r, "a.test", nil)
	siteBefore, _ := r.fs.ReadFile(siteFile)
	rec, _ := newReconciler(t, r, &fakeRuntime{present: true, action: nginx.ActionNone})
	if st := rec.Run(ctx); !st.OK {
		t.Fatalf("status = %+v", st)
	}
	got, _ := r.fs.ReadFile(tlsPath)
	if !strings.Contains(string(got), "listen 443 ssl;") {
		t.Errorf("tls.conf after reconcile:\n%s", got)
	}
	if after, _ := r.fs.ReadFile(siteFile); string(after) != string(siteBefore) {
		t.Error("reconcile rewrote a legacy site file")
	}
}
