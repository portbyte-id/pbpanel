package tls

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/portbyte-id/server-panel/internal/secretbox"
	"github.com/portbyte-id/server-panel/internal/store"
)

// Certificate sources, the values of certs.source.
const (
	SourceUpload = "upload"
	SourceHTTP01 = "http01"
	SourceDNS01  = "dns01"
)

// Renewal results, the values of certs.last_renewal_result. Empty means the
// certificate has never been through the renewal job.
const (
	RenewalOK     = "ok"
	RenewalFailed = "failed"
)

var (
	// ErrNotFound is returned when a certificate, account or provider is absent.
	ErrNotFound = errors.New("tls: not found")
	// ErrInvalid is returned for a record that fails validation before it is stored.
	ErrInvalid = errors.New("tls: invalid")
)

// Cert is the index row of a site's certificate. The PEM files are on disk; the
// private key is never part of this type.
type Cert struct {
	ID     int64
	Domain string
	Source string
	// Names are the host names the certificate covers, wildcards included.
	Names     []string
	NotBefore time.Time
	NotAfter  time.Time
	Issuer    string
	Serial    string
	// LastRenewalAt is the zero time until the renewal job has touched the row.
	LastRenewalAt     time.Time
	LastRenewalResult string
	LastRenewalError  string
	// RedirectHTTP sends plain-HTTP requests to HTTPS, except the ACME path.
	RedirectHTTP bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Account is an ACME account at one CA. Key and Registration are plaintext here
// and sealed in the database.
type Account struct {
	DirectoryURL string
	Email        string
	// Key is the account's private key, PEM.
	Key []byte
	// Registration is the CA's registration resource, as the ACME client
	// serialised it. May be empty before the first registration completes.
	Registration []byte
}

// Provider is a DNS-01 provider with its credentials in the clear. Only Get
// returns it; List returns ProviderInfo.
type Provider struct {
	Name string
	Kind string
	// Credentials is whatever the provider implementation serialised, usually JSON.
	Credentials []byte
}

// ProviderInfo is a provider without its secret, safe to list in the API.
type ProviderInfo struct {
	Name      string
	Kind      string
	UpdatedAt time.Time
}

// Options configures a Store.
type Options struct {
	DB  *store.Store
	Box *secretbox.Box
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Store persists certificates, ACME accounts and DNS providers.
type Store struct {
	db  *sql.DB
	box *secretbox.Box
	now func() time.Time
}

// NewStore returns a Store.
func NewStore(o Options) (*Store, error) {
	if o.DB == nil || o.Box == nil {
		return nil, errors.New("tls: DB and Box are required")
	}
	s := &Store{db: o.DB.DB(), box: o.Box, now: o.Now}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

const timeLayout = time.RFC3339

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

const certColumns = `id, domain, source, names, not_before, not_after, issuer, serial,
	last_renewal_at, last_renewal_result, last_renewal_error, redirect_http, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanCert(r scanner) (Cert, error) {
	var (
		c                        Cert
		names                    string
		nb, na, created, updated string
		renewedAt                sql.NullString
		redirect                 int
	)
	if err := r.Scan(&c.ID, &c.Domain, &c.Source, &names, &nb, &na, &c.Issuer, &c.Serial,
		&renewedAt, &c.LastRenewalResult, &c.LastRenewalError, &redirect, &created, &updated); err != nil {
		return Cert{}, err
	}
	if err := json.Unmarshal([]byte(names), &c.Names); err != nil {
		return Cert{}, fmt.Errorf("names of %s: %w", c.Domain, err)
	}
	var err error
	if c.NotBefore, err = parseTime(nb); err != nil {
		return Cert{}, fmt.Errorf("not_before of %s: %w", c.Domain, err)
	}
	if c.NotAfter, err = parseTime(na); err != nil {
		return Cert{}, fmt.Errorf("not_after of %s: %w", c.Domain, err)
	}
	if renewedAt.Valid {
		if c.LastRenewalAt, err = parseTime(renewedAt.String); err != nil {
			return Cert{}, fmt.Errorf("last_renewal_at of %s: %w", c.Domain, err)
		}
	}
	if c.CreatedAt, err = parseTime(created); err != nil {
		return Cert{}, fmt.Errorf("created_at of %s: %w", c.Domain, err)
	}
	if c.UpdatedAt, err = parseTime(updated); err != nil {
		return Cert{}, fmt.Errorf("updated_at of %s: %w", c.Domain, err)
	}
	c.RedirectHTTP = redirect != 0
	return c, nil
}

// GetCert returns the certificate of a site domain, or ErrNotFound.
func (s *Store) GetCert(ctx context.Context, domain string) (Cert, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+certColumns+` FROM certs WHERE domain = ?`, domain)
	c, err := scanCert(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Cert{}, fmt.Errorf("%w: certificate for %s", ErrNotFound, domain)
	}
	if err != nil {
		return Cert{}, fmt.Errorf("tls: get certificate: %w", err)
	}
	return c, nil
}

// ListCerts returns every certificate, soonest expiry first.
func (s *Store) ListCerts(ctx context.Context) ([]Cert, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+certColumns+` FROM certs ORDER BY not_after, domain`)
	if err != nil {
		return nil, fmt.Errorf("tls: list certificates: %w", err)
	}
	defer rows.Close()
	var out []Cert
	for rows.Next() {
		c, err := scanCert(rows)
		if err != nil {
			return nil, fmt.Errorf("tls: list certificates: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tls: list certificates: %w", err)
	}
	return out, nil
}

// PutCert inserts the certificate of c.Domain or replaces the existing one: a
// site has one active certificate. ID, CreatedAt and UpdatedAt are set by the
// store; CreatedAt survives a replacement.
func (s *Store) PutCert(ctx context.Context, c Cert) error {
	if err := CheckDomain(c.Domain); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	switch c.Source {
	case SourceUpload, SourceHTTP01, SourceDNS01:
	default:
		return fmt.Errorf("%w: source %q", ErrInvalid, c.Source)
	}
	switch c.LastRenewalResult {
	case "", RenewalOK, RenewalFailed:
	default:
		return fmt.Errorf("%w: renewal result %q", ErrInvalid, c.LastRenewalResult)
	}
	if c.NotBefore.IsZero() || c.NotAfter.IsZero() {
		return fmt.Errorf("%w: validity period is required", ErrInvalid)
	}
	names := c.Names
	if names == nil {
		names = []string{}
	}
	namesJSON, err := json.Marshal(names)
	if err != nil {
		return fmt.Errorf("tls: encode names: %w", err)
	}
	var renewedAt any
	if !c.LastRenewalAt.IsZero() {
		renewedAt = formatTime(c.LastRenewalAt)
	}
	redirect := 0
	if c.RedirectHTTP {
		redirect = 1
	}
	now := formatTime(s.now())
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO certs (domain, source, names, not_before, not_after, issuer, serial,
			last_renewal_at, last_renewal_result, last_renewal_error, redirect_http, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (domain) DO UPDATE SET
			source = excluded.source, names = excluded.names,
			not_before = excluded.not_before, not_after = excluded.not_after,
			issuer = excluded.issuer, serial = excluded.serial,
			last_renewal_at = excluded.last_renewal_at,
			last_renewal_result = excluded.last_renewal_result,
			last_renewal_error = excluded.last_renewal_error,
			redirect_http = excluded.redirect_http,
			updated_at = excluded.updated_at`,
		c.Domain, c.Source, string(namesJSON), formatTime(c.NotBefore), formatTime(c.NotAfter),
		c.Issuer, c.Serial, renewedAt, c.LastRenewalResult, c.LastRenewalError, redirect, now, now)
	if err != nil {
		return fmt.Errorf("tls: put certificate: %w", err)
	}
	return nil
}

// DeleteCert removes a site's certificate row, or returns ErrNotFound. It does
// not touch the files; RemoveSiteFiles does, and the caller orders the two.
func (s *Store) DeleteCert(ctx context.Context, domain string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM certs WHERE domain = ?`, domain)
	if err != nil {
		return fmt.Errorf("tls: delete certificate: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: certificate for %s", ErrNotFound, domain)
	}
	return nil
}

// aad binds a sealed value to its row and column, so copying a sealed string to
// another row in the database makes it unreadable instead of silently reused.
func aad(table, column, id string) string { return table + "." + column + ":" + id }

// GetAccount returns the ACME account for a CA directory, or ErrNotFound.
func (s *Store) GetAccount(ctx context.Context, directoryURL string) (Account, error) {
	var a Account
	var keySealed, regSealed string
	err := s.db.QueryRowContext(ctx,
		`SELECT directory_url, email, key_sealed, registration_sealed FROM acme_accounts WHERE directory_url = ?`,
		directoryURL).Scan(&a.DirectoryURL, &a.Email, &keySealed, &regSealed)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, fmt.Errorf("%w: ACME account for %s", ErrNotFound, directoryURL)
	}
	if err != nil {
		return Account{}, fmt.Errorf("tls: get account: %w", err)
	}
	if a.Key, err = s.box.Open(keySealed, aad("acme_accounts", "key", directoryURL)); err != nil {
		return Account{}, fmt.Errorf("tls: open account key: %w", err)
	}
	if regSealed != "" {
		if a.Registration, err = s.box.Open(regSealed, aad("acme_accounts", "registration", directoryURL)); err != nil {
			return Account{}, fmt.Errorf("tls: open account registration: %w", err)
		}
	}
	return a, nil
}

// PutAccount stores or replaces the account for a.DirectoryURL, sealing the key
// and the registration.
func (s *Store) PutAccount(ctx context.Context, a Account) error {
	if a.DirectoryURL == "" || len(a.Key) == 0 {
		return fmt.Errorf("%w: account needs a directory URL and a key", ErrInvalid)
	}
	keySealed, err := s.box.Seal(a.Key, aad("acme_accounts", "key", a.DirectoryURL))
	if err != nil {
		return fmt.Errorf("tls: seal account key: %w", err)
	}
	var regSealed string
	if len(a.Registration) > 0 {
		if regSealed, err = s.box.Seal(a.Registration, aad("acme_accounts", "registration", a.DirectoryURL)); err != nil {
			return fmt.Errorf("tls: seal account registration: %w", err)
		}
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO acme_accounts (directory_url, email, key_sealed, registration_sealed, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (directory_url) DO UPDATE SET
			email = excluded.email, key_sealed = excluded.key_sealed,
			registration_sealed = excluded.registration_sealed`,
		a.DirectoryURL, a.Email, keySealed, regSealed, formatTime(s.now()))
	if err != nil {
		return fmt.Errorf("tls: put account: %w", err)
	}
	return nil
}

var providerNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// GetProvider returns a DNS provider with its credentials opened, or ErrNotFound.
func (s *Store) GetProvider(ctx context.Context, name string) (Provider, error) {
	var p Provider
	var sealed string
	err := s.db.QueryRowContext(ctx,
		`SELECT name, kind, credentials_sealed FROM dns_providers WHERE name = ?`, name).
		Scan(&p.Name, &p.Kind, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, fmt.Errorf("%w: DNS provider %s", ErrNotFound, name)
	}
	if err != nil {
		return Provider{}, fmt.Errorf("tls: get provider: %w", err)
	}
	if p.Credentials, err = s.box.Open(sealed, aad("dns_providers", "credentials", name)); err != nil {
		return Provider{}, fmt.Errorf("tls: open provider credentials: %w", err)
	}
	return p, nil
}

// ListProviders returns every provider without credentials, by name.
func (s *Store) ListProviders(ctx context.Context) ([]ProviderInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, kind, updated_at FROM dns_providers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("tls: list providers: %w", err)
	}
	defer rows.Close()
	var out []ProviderInfo
	for rows.Next() {
		var p ProviderInfo
		var updated string
		if err := rows.Scan(&p.Name, &p.Kind, &updated); err != nil {
			return nil, fmt.Errorf("tls: list providers: %w", err)
		}
		if p.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, fmt.Errorf("tls: list providers: updated_at of %s: %w", p.Name, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tls: list providers: %w", err)
	}
	return out, nil
}

// PutProvider stores or replaces a provider, sealing its credentials.
func (s *Store) PutProvider(ctx context.Context, p Provider) error {
	if !providerNameRe.MatchString(p.Name) {
		return fmt.Errorf("%w: provider name %q", ErrInvalid, p.Name)
	}
	if p.Kind == "" || len(p.Credentials) == 0 {
		return fmt.Errorf("%w: provider needs a kind and credentials", ErrInvalid)
	}
	sealed, err := s.box.Seal(p.Credentials, aad("dns_providers", "credentials", p.Name))
	if err != nil {
		return fmt.Errorf("tls: seal provider credentials: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO dns_providers (name, kind, credentials_sealed, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET
			kind = excluded.kind, credentials_sealed = excluded.credentials_sealed,
			updated_at = excluded.updated_at`,
		p.Name, p.Kind, sealed, formatTime(s.now()))
	if err != nil {
		return fmt.Errorf("tls: put provider: %w", err)
	}
	return nil
}

// DeleteProvider removes a provider, or returns ErrNotFound.
func (s *Store) DeleteProvider(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM dns_providers WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("tls: delete provider: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: DNS provider %s", ErrNotFound, name)
	}
	return nil
}
