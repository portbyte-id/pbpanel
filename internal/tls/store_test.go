package tls

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/secretbox"
	"github.com/portbyte-id/server-panel/internal/store"
)

type rig struct {
	st  *Store
	db  *store.Store
	box *secretbox.Box
	now time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{9}, secretbox.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{db: db, box: box, now: testNow}
	r.st, err = NewStore(Options{DB: db, Box: box, Now: func() time.Time { return r.now }})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func sampleCert(domain string) Cert {
	return Cert{
		Domain: domain, Source: SourceUpload, Names: []string{domain, "www." + domain},
		NotBefore: testNow.Add(-time.Hour), NotAfter: testNow.Add(90 * 24 * time.Hour),
		Issuer: "CN=Test", Serial: "1f",
	}
}

func TestCertRoundTrip(t *testing.T) {
	r, ctx := newRig(t), context.Background()
	in := sampleCert("example.com")
	in.RedirectHTTP = true
	if err := r.st.PutCert(ctx, in); err != nil {
		t.Fatalf("PutCert: %v", err)
	}
	got, err := r.st.GetCert(ctx, "example.com")
	if err != nil {
		t.Fatalf("GetCert: %v", err)
	}
	if got.ID == 0 || got.Source != SourceUpload || !got.RedirectHTTP || got.Issuer != "CN=Test" || got.Serial != "1f" {
		t.Fatalf("got %+v", got)
	}
	if strings.Join(got.Names, ",") != "example.com,www.example.com" {
		t.Fatalf("names = %v", got.Names)
	}
	if !got.NotAfter.Equal(in.NotAfter) || !got.LastRenewalAt.IsZero() || !got.CreatedAt.Equal(testNow) {
		t.Fatalf("times = %+v", got)
	}
}

func TestPutCertReplacesAndKeepsIdentity(t *testing.T) {
	r, ctx := newRig(t), context.Background()
	if err := r.st.PutCert(ctx, sampleCert("example.com")); err != nil {
		t.Fatal(err)
	}
	first, _ := r.st.GetCert(ctx, "example.com")

	r.now = testNow.Add(48 * time.Hour)
	next := sampleCert("example.com")
	next.Source, next.Serial = SourceHTTP01, "aa"
	next.LastRenewalAt, next.LastRenewalResult = r.now, RenewalOK
	if err := r.st.PutCert(ctx, next); err != nil {
		t.Fatal(err)
	}
	got, _ := r.st.GetCert(ctx, "example.com")
	if got.ID != first.ID || !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("replacement changed identity: %+v vs %+v", got, first)
	}
	if got.Source != SourceHTTP01 || got.Serial != "aa" || !got.UpdatedAt.Equal(r.now) || got.LastRenewalResult != RenewalOK || !got.LastRenewalAt.Equal(r.now) {
		t.Fatalf("replacement not applied: %+v", got)
	}
	all, _ := r.st.ListCerts(ctx)
	if len(all) != 1 {
		t.Fatalf("%d certificates after a replacement, want 1", len(all))
	}
}

func TestPutCertValidation(t *testing.T) {
	r, ctx := newRig(t), context.Background()
	for name, mod := range map[string]func(*Cert){
		"bad domain":  func(c *Cert) { c.Domain = "../x" },
		"bad source":  func(c *Cert) { c.Source = "magic" },
		"bad result":  func(c *Cert) { c.LastRenewalResult = "maybe" },
		"no validity": func(c *Cert) { c.NotAfter = time.Time{} },
	} {
		c := sampleCert("example.com")
		mod(&c)
		if err := r.st.PutCert(ctx, c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestListCertsOrdersBySoonestExpiry(t *testing.T) {
	r, ctx := newRig(t), context.Background()
	for d, days := range map[string]int{"late.com": 80, "soon.com": 5, "mid.com": 40} {
		c := sampleCert(d)
		c.NotAfter = testNow.Add(time.Duration(days) * 24 * time.Hour)
		if err := r.st.PutCert(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	all, err := r.st.ListCerts(ctx)
	if err != nil || len(all) != 3 || all[0].Domain != "soon.com" || all[1].Domain != "mid.com" || all[2].Domain != "late.com" {
		t.Fatalf("ListCerts = %+v, %v", all, err)
	}
}

func TestCertNotFoundAndDelete(t *testing.T) {
	r, ctx := newRig(t), context.Background()
	if _, err := r.st.GetCert(ctx, "nosuch.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v", err)
	}
	if err := r.st.DeleteCert(ctx, "nosuch.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete = %v", err)
	}
	_ = r.st.PutCert(ctx, sampleCert("example.com"))
	if err := r.st.DeleteCert(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.GetCert(ctx, "example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v", err)
	}
}

func TestAccountIsStoredSealed(t *testing.T) {
	r, ctx := newRig(t), context.Background()
	const dir = "https://acme-staging-v02.api.letsencrypt.org/directory"
	in := Account{DirectoryURL: dir, Email: "ops@example.com", Key: []byte("-----BEGIN KEY-----secret"), Registration: []byte(`{"uri":"x"}`)}
	if err := r.st.PutAccount(ctx, in); err != nil {
		t.Fatalf("PutAccount: %v", err)
	}

	var keySealed, regSealed string
	if err := r.db.DB().QueryRow(`SELECT key_sealed, registration_sealed FROM acme_accounts`).Scan(&keySealed, &regSealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(keySealed, "secret") || !strings.HasPrefix(keySealed, "v1:") || !strings.HasPrefix(regSealed, "v1:") {
		t.Fatalf("key_sealed = %q, registration_sealed = %q", keySealed, regSealed)
	}

	got, err := r.st.GetAccount(ctx, dir)
	if err != nil || !bytes.Equal(got.Key, in.Key) || !bytes.Equal(got.Registration, in.Registration) || got.Email != in.Email {
		t.Fatalf("GetAccount = %+v, %v", got, err)
	}
	if _, err := r.st.GetAccount(ctx, "https://other/dir"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other directory = %v", err)
	}

	// A registration is optional until the CA answers; the key is not.
	in.Registration = nil
	if err := r.st.PutAccount(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.st.GetAccount(ctx, dir); len(got.Registration) != 0 {
		t.Fatalf("registration = %q after clearing", got.Registration)
	}
	if err := r.st.PutAccount(ctx, Account{DirectoryURL: dir}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("keyless account = %v", err)
	}
}

func TestProvidersAreSealedAndListedWithoutSecrets(t *testing.T) {
	r, ctx := newRig(t), context.Background()
	if err := r.st.PutProvider(ctx, Provider{Name: "cf-main", Kind: "cloudflare", Credentials: []byte(`{"token":"tok-SECRET"}`)}); err != nil {
		t.Fatalf("PutProvider: %v", err)
	}
	if err := r.st.PutProvider(ctx, Provider{Name: "alpha", Kind: "cloudflare", Credentials: []byte("x")}); err != nil {
		t.Fatal(err)
	}

	var raw string
	if err := r.db.DB().QueryRow(`SELECT credentials_sealed FROM dns_providers WHERE name = 'cf-main'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "SECRET") || !strings.HasPrefix(raw, "v1:") {
		t.Fatalf("credentials stored as %q", raw)
	}

	list, err := r.st.ListProviders(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "alpha" || list[1].Kind != "cloudflare" {
		t.Fatalf("ListProviders = %+v, %v", list, err)
	}

	got, err := r.st.GetProvider(ctx, "cf-main")
	if err != nil || string(got.Credentials) != `{"token":"tok-SECRET"}` {
		t.Fatalf("GetProvider = %+v, %v", got, err)
	}

	if err := r.st.DeleteProvider(ctx, "cf-main"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.GetProvider(ctx, "cf-main"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v", err)
	}
	if err := r.st.DeleteProvider(ctx, "cf-main"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

func TestProviderValidation(t *testing.T) {
	r, ctx := newRig(t), context.Background()
	for name, p := range map[string]Provider{
		"empty name":     {Name: "", Kind: "k", Credentials: []byte("x")},
		"path name":      {Name: "../x", Kind: "k", Credentials: []byte("x")},
		"uppercase name": {Name: "CF", Kind: "k", Credentials: []byte("x")},
		"no kind":        {Name: "a", Credentials: []byte("x")},
		"no credentials": {Name: "a", Kind: "k"},
	} {
		if err := r.st.PutProvider(ctx, p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

// A sealed value copied onto another row, or sealed by another key, must fail to
// open rather than yield the wrong credentials.
func TestSealedValuesAreBoundToTheirRowAndKey(t *testing.T) {
	r, ctx := newRig(t), context.Background()
	_ = r.st.PutProvider(ctx, Provider{Name: "a", Kind: "k", Credentials: []byte("token-a")})
	_ = r.st.PutProvider(ctx, Provider{Name: "b", Kind: "k", Credentials: []byte("token-b")})

	var sealedA string
	_ = r.db.DB().QueryRow(`SELECT credentials_sealed FROM dns_providers WHERE name = 'a'`).Scan(&sealedA)
	if _, err := r.db.DB().Exec(`UPDATE dns_providers SET credentials_sealed = ? WHERE name = 'b'`, sealedA); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.GetProvider(ctx, "b"); !errors.Is(err, secretbox.ErrTampered) {
		t.Fatalf("copied value opened: %v", err)
	}

	otherBox, _ := secretbox.New(bytes.Repeat([]byte{1}, secretbox.MasterKeySize))
	other, _ := NewStore(Options{DB: r.db, Box: otherBox})
	if _, err := other.GetProvider(ctx, "a"); !errors.Is(err, secretbox.ErrUnknownKey) {
		t.Fatalf("value from another key = %v, want ErrUnknownKey", err)
	}
}

func TestNewStoreRequiresItsDependencies(t *testing.T) {
	r := newRig(t)
	if _, err := NewStore(Options{Box: r.box}); err == nil {
		t.Error("NewStore accepted a nil DB")
	}
	if _, err := NewStore(Options{DB: r.db}); err == nil {
		t.Error("NewStore accepted a nil Box")
	}
}
