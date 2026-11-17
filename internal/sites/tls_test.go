package sites

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/docker"
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

const tlsPath = "/www/panel/nginx/includes/a.test/tls.conf"

func TestSiteWithoutACertificateGetsAnEmptyTLSInclude(t *testing.T) {
	for name, state := range map[string]TLSState{"no store": nil, "empty store": &fakeTLS{}} {
		r := newRigTLS(t, state)
		if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
			t.Fatal(err)
		}
		got, err := r.fs.ReadFile(tlsPath)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(string(got), "listen") || strings.Contains(string(got), "ssl_certificate") {
			t.Errorf("%s: tls.conf of a site with no certificate has directives:\n%s", name, got)
		}
		site, _ := r.fs.ReadFile("/www/panel/nginx/sites/a.test.conf")
		if !nginx.HasIncludeLine(site, tlsPath) {
			t.Errorf("%s: the site file lacks the tls include", name)
		}
	}
}

func TestCreateOfASiteWithAStoredCertificateIsHTTPS(t *testing.T) {
	r := newRigTLS(t, &fakeTLS{certs: map[string]TLSInfo{"a.test": certFor("a.test", true)}})
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	got, _ := r.fs.ReadFile(tlsPath)
	for _, want := range []string{"listen 443 ssl;", "ssl_certificate /www/panel/ssl/sites/a.test/fullchain.pem;", "return 301 https://$host$request_uri;"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("tls.conf lacks %q:\n%s", want, got)
		}
	}
}

func TestRefreshTLSRewritesOnlyTLSConf(t *testing.T) {
	store := &fakeTLS{certs: map[string]TLSInfo{}}
	r := newRigTLS(t, store)
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	site, _ := r.fs.ReadFile("/www/panel/nginx/sites/a.test.conf")
	r.ap.applied = nil

	store.certs["a.test"] = certFor("a.test", false)
	if _, err := r.m.RefreshTLS(ctx, "a.test"); err != nil {
		t.Fatal(err)
	}
	if len(r.ap.applied) != 1 || len(r.ap.applied[0].Files) != 1 || r.ap.applied[0].Files[0].Path != tlsPath {
		t.Fatalf("refresh wrote %v, want only tls.conf", r.ap.paths())
	}
	if len(r.ap.applied[0].Services) != 1 || r.ap.applied[0].Services[0] != r.svc {
		t.Errorf("refresh must validate and reload through the nginx service: %+v", r.ap.applied[0].Services)
	}
	got, _ := r.fs.ReadFile(tlsPath)
	if !strings.Contains(string(got), "listen 443 ssl;") || strings.Contains(string(got), "return 301") {
		t.Errorf("tls.conf after issuing:\n%s", got)
	}
	after, _ := r.fs.ReadFile("/www/panel/nginx/sites/a.test.conf")
	if string(after) != string(site) {
		t.Error("RefreshTLS touched the site file")
	}

	// Removing the certificate takes the listener away again.
	delete(store.certs, "a.test")
	if _, err := r.m.RefreshTLS(ctx, "a.test"); err != nil {
		t.Fatal(err)
	}
	got, _ = r.fs.ReadFile(tlsPath)
	if strings.Contains(string(got), "listen") {
		t.Errorf("tls.conf after removal:\n%s", got)
	}
}

func TestRefreshTLSHonoursADetachedInclude(t *testing.T) {
	r := newRigTLS(t, &fakeTLS{certs: map[string]TLSInfo{"a.test": certFor("a.test", false)}})
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	file := "/www/panel/nginx/sites/a.test.conf"
	b, _ := r.fs.ReadFile(file)
	r.fs.files[file] = []byte(strings.Replace(string(b), "    include "+tlsPath+";\n", "", 1))
	r.ap.applied = nil

	_, err := r.m.RefreshTLS(ctx, "a.test")
	var de *DetachedError
	if !errors.Is(err, ErrDetached) || !errors.As(err, &de) || de.Feature != FeatureTLS {
		t.Fatalf("RefreshTLS = %v, want a DetachedError for tls", err)
	}
	if len(r.ap.applied) != 0 {
		t.Errorf("a detached tls.conf was written anyway: %v", r.ap.paths())
	}
	d, _ := r.m.Detail(ctx, "a.test")
	for _, f := range d.Features {
		if f.Feature == FeatureTLS && (f.Attached || f.Reason == "") {
			t.Errorf("tls feature = %+v, want detached with a reason", f)
		}
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

func TestReconcileReproducesTLSConf(t *testing.T) {
	r := newRigTLS(t, &fakeTLS{certs: map[string]TLSInfo{"a.test": certFor("a.test", true)}})
	if _, err := r.m.Create(ctx, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	want, _ := r.fs.ReadFile(tlsPath)
	delete(r.fs.files, tlsPath)

	rec, _ := newReconciler(t, r, &fakeRuntime{present: true, action: docker.ActionNone})
	if st := rec.Run(ctx); !st.OK {
		t.Fatalf("status = %+v", st)
	}
	got, err := r.fs.ReadFile(tlsPath)
	if err != nil || string(got) != string(want) {
		t.Errorf("tls.conf after reconcile = %q, %v; want the rendered one", got, err)
	}
}
