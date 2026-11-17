package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/secretbox"
	"github.com/portbyte-id/server-panel/internal/store"
	"github.com/portbyte-id/server-panel/internal/tls"
)

func testCertStore(t *testing.T) *tls.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{3}, secretbox.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := tls.NewStore(tls.Options{DB: st, Box: box})
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestTLSStateLooksUpTheStore(t *testing.T) {
	cs := testCertStore(t)
	state := tlsState{st: cs, root: "/www"}
	ctx := context.Background()

	if _, ok, err := state.Lookup(ctx, "a.test"); ok || err != nil {
		t.Fatalf("no row: ok=%v err=%v", ok, err)
	}
	now := time.Now()
	if err := cs.PutCert(ctx, tls.Cert{
		Domain: "a.test", Source: tls.SourceUpload, Names: []string{"a.test"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), RedirectHTTP: true,
	}); err != nil {
		t.Fatal(err)
	}
	info, ok, err := state.Lookup(ctx, "a.test")
	if err != nil || !ok {
		t.Fatalf("with a row: ok=%v err=%v", ok, err)
	}
	if info.CertFile != "/www/panel/ssl/sites/a.test/fullchain.pem" || info.KeyFile != "/www/panel/ssl/sites/a.test/privkey.pem" || !info.RedirectHTTP {
		t.Errorf("info = %+v", info)
	}
	if _, ok, _ := state.Lookup(ctx, "b.test"); ok {
		t.Error("another domain has a certificate")
	}
}

func TestRenewAdapterTurnsARecordedFailureIntoAnError(t *testing.T) {
	cs := testCertStore(t)
	ctx := context.Background()
	now := time.Now()
	row := tls.Cert{
		Domain: "a.test", Source: tls.SourceHTTP01, Names: []string{"a.test"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
	}
	if err := cs.PutCert(ctx, row); err != nil {
		t.Fatal(err)
	}
	iss := failingIssuer{errors.New("the CA refused")}
	r, err := tls.NewRenewer(tls.RenewerOptions{Store: cs, Issuer: iss})
	if err != nil {
		t.Fatal(err)
	}
	a := renewAdapter{r}
	if err := a.RenewNow(ctx, "a.test"); err == nil || err.Error() != "the CA refused" {
		t.Errorf("failed order: %v", err)
	}
	if err := a.RenewNow(ctx, "ghost.test"); !errors.Is(err, tls.ErrNotFound) {
		t.Errorf("unknown domain: %v", err)
	}
	up := row
	up.Domain, up.Source = "up.test", tls.SourceUpload
	if err := cs.PutCert(ctx, up); err != nil {
		t.Fatal(err)
	}
	if err := a.RenewNow(ctx, "up.test"); !errors.Is(err, tls.ErrNotRenewable) {
		t.Errorf("upload: %v", err)
	}
}

type failingIssuer struct{ err error }

func (f failingIssuer) IssueHTTP01(context.Context, string, []string, string) (tls.Result, error) {
	return tls.Result{}, f.err
}

func (f failingIssuer) IssueDNS01(context.Context, string, []string, string, string) (tls.Result, error) {
	return tls.Result{}, f.err
}
