package tls

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/http/webroot"
	"github.com/go-acme/lego/v4/registration"
)

var (
	// ErrWildcardNeedsDNS is returned when HTTP-01 is asked for a wildcard name.
	// The CA only validates a wildcard through DNS-01.
	ErrWildcardNeedsDNS = errors.New("tls: a wildcard name needs DNS-01")
	// ErrNoDirectory is returned when no ACME directory URL is configured.
	ErrNoDirectory = errors.New("tls: no ACME directory is configured")
)

// IssuerOptions configures an Issuer.
type IssuerOptions struct {
	Store *Store
	// Root is the web root the ssl directory lives under (`/www`).
	Root string
	// Directory returns the ACME directory URL to order from. It is a function
	// so a change of the tls.acme.directory setting applies to the next order
	// without a restart.
	Directory func() string
	// EAB returns the External Account Binding key ID and base64url HMAC key,
	// both empty when none is configured. nil means never. It is read only when
	// an account is first registered at a directory.
	EAB func() (kid, hmacKey string)
	// Email returns the default ACME contact, used when an order names none
	// (renewals and the panel's own certificate). nil means no default.
	Email func() string
	// HTTPClient talks to the CA. nil means a client with a two-minute timeout.
	// Tests and Pebble inject one that trusts a private CA.
	HTTPClient *http.Client
	// ProbeClient is used by the HTTP-01 pre-flight. nil means a client that
	// dials the name normally with a 10 s timeout and follows no redirect.
	ProbeClient *http.Client
	// Providers builds a DNS provider from a stored kind and credentials. nil
	// means ProviderFactory; tests inject fakes.
	Providers func(kind string, credentials []byte) (DNSProvider, error)
	// SkipDNSPropagationCheck makes lego notify the CA without first querying
	// DNS for the TXT record. It exists for tests against Pebble, which
	// validates against a fake resolver; production leaves it false.
	SkipDNSPropagationCheck bool
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Result describes an issued certificate. The files are already on disk and the
// row is stored; the caller applies them to nginx with sites.RefreshTLS.
type Result struct {
	Domain    string
	Source    string
	Names     []string
	NotBefore time.Time
	NotAfter  time.Time
	Issuer    string
	Serial    string
	// Directory is the CA directory the certificate came from.
	Directory string
}

// Issuer obtains certificates from an ACME CA with lego. It does not touch
// nginx: writing files and the certs row is its whole effect.
type Issuer struct {
	st          *Store
	root        string
	directory   func() string
	eab         func() (string, string)
	defEmail    func() string
	hc          *http.Client
	probeClient *http.Client
	providers   func(string, []byte) (DNSProvider, error)
	skipDNS     bool
	now         func() time.Time

	// accountMu serialises first-time account registration, so two orders at
	// startup do not register two accounts for one directory.
	accountMu sync.Mutex
}

// NewIssuer returns an Issuer.
func NewIssuer(o IssuerOptions) (*Issuer, error) {
	if o.Store == nil || o.Root == "" || o.Directory == nil {
		return nil, errors.New("tls: Store, Root and Directory are required")
	}
	i := &Issuer{
		st: o.Store, root: o.Root, directory: o.Directory, eab: o.EAB, defEmail: o.Email, hc: o.HTTPClient,
		probeClient: o.ProbeClient, providers: o.Providers, skipDNS: o.SkipDNSPropagationCheck, now: o.Now,
	}
	if i.hc == nil {
		i.hc = &http.Client{Timeout: 2 * time.Minute}
	}
	if i.probeClient == nil {
		i.probeClient = newProbeClient()
	}
	if i.providers == nil {
		i.providers = ProviderFactory
	}
	if i.now == nil {
		i.now = time.Now
	}
	return i, nil
}

// acmeUser is lego's view of an account.
type acmeUser struct {
	email string
	key   *ecdsa.PrivateKey
	reg   *registration.Resource
}

func (u *acmeUser) GetEmail() string                        { return u.email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.reg }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// ctxTransport stamps every ACME request with the issuance's context. lego's API
// has no context parameter, so this is what makes cancellation abort an order
// at its next request instead of after it finishes.
type ctxTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t ctxTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(r.WithContext(t.ctx))
}

// IssueHTTP01 orders a certificate for names, proving control by serving tokens
// from the ACME webroot. domain is the site the certificate is stored under.
func (i *Issuer) IssueHTTP01(ctx context.Context, domain string, names []string, email string) (Result, error) {
	names, err := checkNames(domain, names, false)
	if err != nil {
		return Result{}, err
	}
	// An IP name has no server block of its own in nginx, whose default server
	// refuses it, so when nothing holds port 80 the panel answers the challenge
	// itself. With port 80 taken the webroot path is the only one left.
	var provider challenge.Provider
	if anyIP(names) && port80Free() {
		provider = http01.NewProviderServer("", "80")
	} else {
		if err := EnsureACMEWebroot(i.root); err != nil {
			return Result{}, err
		}
		// Before the CA: a name that does not reach this server would burn a
		// failed-validation allowance for nothing.
		if err := i.preflight(ctx, names); err != nil {
			return Result{}, err
		}
		wp, err := webroot.NewHTTPProvider(filepath.FromSlash(ACMEWebroot(i.root)))
		if err != nil {
			return Result{}, fmt.Errorf("tls: http-01 provider: %w", err)
		}
		provider = wp
	}
	return i.issue(ctx, domain, names, email, SourceHTTP01, "", profileFor(names), func(c *lego.Client) error {
		return c.Challenge.SetHTTP01Provider(provider)
	})
}

// IssueDNS01 orders a certificate for names, proving control with TXT records
// published through the stored provider providerName. Wildcard names are allowed.
func (i *Issuer) IssueDNS01(ctx context.Context, domain string, names []string, providerName, email string) (Result, error) {
	names, err := checkNames(domain, names, true)
	if err != nil {
		return Result{}, err
	}
	if anyIP(names) {
		return Result{}, fmt.Errorf("%w: an IP address cannot be validated with DNS-01", ErrInvalid)
	}
	rec, err := i.st.GetProvider(ctx, providerName)
	if err != nil {
		return Result{}, err
	}
	p, err := i.providers(rec.Kind, rec.Credentials)
	if err != nil {
		return Result{}, fmt.Errorf("tls: DNS provider %s: %w", providerName, err)
	}
	return i.issue(ctx, domain, names, email, SourceDNS01, providerName, "", func(c *lego.Client) error {
		var opts []dns01.ChallengeOption
		if i.skipDNS {
			opts = append(opts, dns01.WrapPreCheck(func(string, string, string, dns01.PreCheckFunc) (bool, error) {
				return true, nil
			}))
		}
		return c.Challenge.SetDNS01Provider(legoDNS{ctx: ctx, p: p}, opts...)
	})
}

// checkNames validates and lowercases the names, and rejects wildcards when
// wildcards is false.
func checkNames(domain string, names []string, wildcards bool) ([]string, error) {
	if err := CheckDomain(domain); err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%w: no names to certify", ErrInvalid)
	}
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		host, wild := strings.CutPrefix(n, "*.")
		if wild && !wildcards {
			return nil, fmt.Errorf("%w: %s", ErrWildcardNeedsDNS, n)
		}
		if err := CheckDomain(host); err != nil {
			return nil, fmt.Errorf("%w: name %q", ErrInvalid, n)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// issue is the shared tail of both challenges: account, order, persist.
func (i *Issuer) issue(ctx context.Context, domain string, names []string, email, source, provider, profile string,
	setSolver func(*lego.Client) error) (Result, error) {
	dir := strings.TrimSpace(i.directory())
	if dir == "" {
		return Result{}, ErrNoDirectory
	}
	if email != "" && !strings.Contains(email, "@") {
		return Result{}, fmt.Errorf("%w: contact email %q", ErrInvalid, email)
	}
	client, err := i.client(ctx, dir, email)
	if err != nil {
		return Result{}, err
	}
	if err := setSolver(client); err != nil {
		return Result{}, fmt.Errorf("tls: set challenge solver: %w", err)
	}
	// A fresh key per order, not the previous one: renewal is the moment to
	// retire a key that may have been exposed.
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Result{}, fmt.Errorf("tls: generate certificate key: %w", err)
	}

	type outcome struct {
		res *certificate.Resource
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		var res *certificate.Resource
		var err error
		if hasIPName(names) {
			// lego would put the first name in the CSR's Common Name, and the CA
			// refuses an IP address there. Build the CSR with the names as SANs only.
			var csr *x509.CertificateRequest
			if csr, err = sanOnlyCSR(certKey, names); err == nil {
				res, err = client.Certificate.ObtainForCSR(certificate.ObtainForCSRRequest{
					CSR: csr, Bundle: true, PrivateKey: certKey, Profile: profile,
				})
			}
		} else {
			res, err = client.Certificate.Obtain(certificate.ObtainRequest{
				Domains: names, Bundle: true, PrivateKey: certKey, Profile: profile,
			})
		}
		done <- outcome{res, err}
	}()
	var res *certificate.Resource
	select {
	case <-ctx.Done():
		// lego has no context of its own; the transport fails its next request,
		// so the goroutine ends promptly. Its late result is discarded.
		return Result{}, fmt.Errorf("tls: order for %s: %w", domain, ctx.Err())
	case o := <-done:
		if o.err != nil {
			if ctx.Err() != nil {
				return Result{}, fmt.Errorf("tls: order for %s: %w", domain, ctx.Err())
			}
			return Result{}, fmt.Errorf("tls: order for %s: %w", domain, o.err)
		}
		res = o.res
	}

	leaf, err := certcrypto.ParsePEMCertificate(res.Certificate)
	if err != nil {
		return Result{}, fmt.Errorf("tls: parse issued certificate: %w", err)
	}
	keyPEM := certcrypto.PEMEncode(certKey)
	if err := WriteSiteFiles(i.root, domain, res.Certificate, keyPEM); err != nil {
		return Result{}, err
	}

	// A re-issue keeps the operator's redirect choice; PutCert replaces the row.
	redirect := false
	if cur, err := i.st.GetCert(ctx, domain); err == nil {
		redirect = cur.RedirectHTTP
	} else if !errors.Is(err, ErrNotFound) {
		return Result{}, err
	}
	now := i.now()
	row := Cert{
		Domain: domain, Source: source, Names: names,
		NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter,
		Issuer: leaf.Issuer.String(), Serial: leaf.SerialNumber.Text(16),
		LastRenewalAt: now, LastRenewalResult: RenewalOK, RedirectHTTP: redirect,
		Provider: provider,
	}
	if err := i.st.PutCert(ctx, row); err != nil {
		return Result{}, err
	}
	return Result{
		Domain: domain, Source: source, Names: names,
		NotBefore: row.NotBefore, NotAfter: row.NotAfter, Issuer: row.Issuer, Serial: row.Serial,
		Directory: dir,
	}, nil
}

// client returns a lego client for the directory, registering an account the
// first time that directory is used and reusing the stored one afterwards.
func (i *Issuer) client(ctx context.Context, dir, email string) (*lego.Client, error) {
	user, err := i.account(ctx, dir, email)
	if err != nil {
		return nil, err
	}
	return i.legoClient(ctx, dir, user)
}

func (i *Issuer) legoClient(ctx context.Context, dir string, user *acmeUser) (*lego.Client, error) {
	cfg := lego.NewConfig(user)
	cfg.CADirURL = dir
	cfg.Certificate.KeyType = certcrypto.EC256
	cfg.UserAgent = "pbpanel"
	base := i.hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	hc := *i.hc
	hc.Transport = ctxTransport{ctx: ctx, base: base}
	cfg.HTTPClient = &hc
	c, err := lego.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("tls: ACME client for %s: %w", dir, err)
	}
	return c, nil
}

// account loads the account for dir, or creates and registers one. Accounts are
// per directory: a staging account means nothing to production, and switching
// the setting must not reuse the wrong one.
func (i *Issuer) account(ctx context.Context, dir, email string) (*acmeUser, error) {
	i.accountMu.Lock()
	defer i.accountMu.Unlock()

	acct, err := i.st.GetAccount(ctx, dir)
	switch {
	case err == nil:
		key, perr := certcrypto.ParsePEMPrivateKey(acct.Key)
		if perr != nil {
			return nil, fmt.Errorf("tls: stored ACME account key for %s: %w", dir, perr)
		}
		ek, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("tls: stored ACME account key for %s is not ECDSA", dir)
		}
		u := &acmeUser{email: acct.Email, key: ek}
		if len(acct.Registration) > 0 {
			var reg registration.Resource
			if err := json.Unmarshal(acct.Registration, &reg); err != nil {
				return nil, fmt.Errorf("tls: stored ACME registration for %s: %w", dir, err)
			}
			u.reg = &reg
			return u, nil
		}
		// A key without a registration is a registration that never finished.
		return i.register(ctx, dir, u)
	case errors.Is(err, ErrNotFound):
		key, kerr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if kerr != nil {
			return nil, fmt.Errorf("tls: generate ACME account key: %w", kerr)
		}
		return i.register(ctx, dir, &acmeUser{email: email, key: key})
	default:
		return nil, err
	}
}

func hasIPName(names []string) bool {
	for _, n := range names {
		if net.ParseIP(n) != nil {
			return true
		}
	}
	return false
}

// sanOnlyCSR builds a CSR with no Common Name: DNS names and IP addresses go
// in the Subject Alternative Name extension only.
func sanOnlyCSR(key crypto.Signer, names []string) (*x509.CertificateRequest, error) {
	tmpl := x509.CertificateRequest{}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &tmpl, key)
	if err != nil {
		return nil, fmt.Errorf("tls: build CSR: %w", err)
	}
	return x509.ParseCertificateRequest(der)
}

// requiresEAB reports a CA known to refuse an account without External Account
// Binding, so the operator gets the missing-setting message instead of the CA's
// terse rejection. Other CAs are tried without EAB.
func requiresEAB(dir string) bool {
	u, err := url.Parse(dir)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "pki.goog" || strings.HasSuffix(h, ".pki.goog")
}

// register creates the account at the CA and stores it. The key is stored
// before the registration call so a crash between the two leaves a key to retry
// with rather than an orphaned account at the CA that nothing can use.
func (i *Issuer) register(ctx context.Context, dir string, u *acmeUser) (*acmeUser, error) {
	if u.email == "" && i.defEmail != nil {
		u.email = strings.TrimSpace(i.defEmail())
	}
	if u.email == "" && requiresEAB(dir) {
		return nil, fmt.Errorf("%w: %s needs a contact email; set tls.acme.email", ErrInvalid, dir)
	}
	keyPEM := certcrypto.PEMEncode(u.key)
	if err := i.st.PutAccount(ctx, Account{DirectoryURL: dir, Email: u.email, Key: keyPEM}); err != nil {
		return nil, err
	}
	c, err := i.legoClient(ctx, dir, u)
	if err != nil {
		return nil, err
	}
	// Agreeing to the CA's terms is implied by configuring the panel to issue
	// from it; there is no per-order prompt to put the question to.
	var kid, hmacKey string
	if i.eab != nil {
		kid, hmacKey = i.eab()
		kid, hmacKey = strings.TrimSpace(kid), strings.TrimSpace(hmacKey)
	}
	if (kid == "") != (hmacKey == "") {
		return nil, fmt.Errorf("%w: External Account Binding needs both a key ID and an HMAC key", ErrInvalid)
	}
	var reg *registration.Resource
	switch {
	case kid != "":
		reg, err = c.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{
			TermsOfServiceAgreed: true, Kid: kid, HmacEncoded: hmacKey,
		})
	case requiresEAB(dir):
		return nil, fmt.Errorf("%w: %s needs External Account Binding; set tls.acme.eab_kid and tls.acme.eab_hmac_key", ErrInvalid, dir)
	default:
		reg, err = c.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	}
	if err != nil {
		return nil, fmt.Errorf("tls: register ACME account at %s: %w", dir, err)
	}
	u.reg = reg
	regJSON, err := json.Marshal(reg)
	if err != nil {
		return nil, fmt.Errorf("tls: encode ACME registration: %w", err)
	}
	if err := i.st.PutAccount(ctx, Account{DirectoryURL: dir, Email: u.email, Key: keyPEM, Registration: regJSON}); err != nil {
		return nil, err
	}
	return u, nil
}
