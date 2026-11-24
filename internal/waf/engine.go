package waf

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/guard"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/store"
)

// ErrModuleMissing is returned when a config asks for ModSecurity on an nginx
// build that does not have it.
var ErrModuleMissing = errors.New("waf: ModSecurity is not part of the nginx build in use")

// Availability says whether the nginx build in use can run ModSecurity.
type Availability struct {
	Available bool   `json:"available"`
	Flavor    string `json:"flavor"`
	Reason    string `json:"reason"`
	// CRSVersion is the Core Rule Set the image bundles.
	CRSVersion string `json:"crs_version"`
}

// AvailabilityFunc looks at the nginx build that serves.
type AvailabilityFunc func(ctx context.Context) (Availability, error)

// ErrNoChallenge is returned when a config asks for the challenge page on a
// panel that has no secret key to sign clearance cookies with.
var ErrNoChallenge = errors.New("waf: the challenge page needs the panel's secret key (data/secret.key), which this panel does not have")

// ErrNoTurnstile is returned when a site's challenge uses Turnstile and the panel
// has no Turnstile site key or secret key set.
var ErrNoTurnstile = errors.New("waf: Turnstile needs a site key and a secret key; set both under Settings > Firewall first")

// TurnstileSource returns the panel's Turnstile keys. It is read on every use, so
// a change in Settings applies without a reload.
type TurnstileSource func(ctx context.Context) (siteKey, secret string, err error)

// Engine keeps the per-site settings and renders them.
type Engine struct {
	st    *store.Store
	p     nginx.Paths
	avail AvailabilityFunc
	now   func() time.Time

	// keys is set by WithGuard; without it the challenge cannot be switched on.
	keys *guard.Keyring
	// turnstile is set by WithTurnstile; without it no site can use Turnstile.
	turnstile TurnstileSource
}

// New returns the engine. avail may be nil, which reports ModSecurity as
// unavailable.
func New(st *store.Store, p nginx.Paths, avail AvailabilityFunc) *Engine {
	return &Engine{st: st, p: p, avail: avail, now: time.Now}
}

// WithGuard gives the engine the keys that sign the clearance cookies.
func (e *Engine) WithGuard(keys *guard.Keyring) *Engine {
	e.keys = keys
	return e
}

// WithTurnstile gives the engine the panel's Turnstile keys.
func (e *Engine) WithTurnstile(src TurnstileSource) *Engine {
	e.turnstile = src
	return e
}

// ChallengeAvailable reports whether the challenge page can be switched on. The
// proof of work needs only the keyring; Turnstile also needs its keys, which
// TurnstileReady reports.
func (e *Engine) ChallengeAvailable() bool { return e.keys != nil }

// TurnstileReady reports whether the panel has both Turnstile keys set.
func (e *Engine) TurnstileReady(ctx context.Context) (bool, error) {
	if e.turnstile == nil {
		return false, nil
	}
	siteKey, secret, err := e.turnstile(ctx)
	if err != nil {
		return false, fmt.Errorf("waf: read the Turnstile keys: %w", err)
	}
	return siteKey != "" && secret != "", nil
}

// GuardSettings is the lookup the challenge handler uses: a site's challenge,
// with the panel's Turnstile keys read now, or false when the site has none. The
// keys may be empty; the handler refuses a Turnstile site that lacks one.
func (e *Engine) GuardSettings(ctx context.Context, domain string) (guard.Settings, bool, error) {
	c, err := e.Get(ctx, domain)
	if err != nil {
		return guard.Settings{}, false, err
	}
	ch := c.Challenge
	if !ch.Enabled {
		return guard.Settings{}, false, nil
	}
	s := guard.Settings{
		Provider:   ch.Provider,
		Difficulty: ch.Difficulty,
		Clearance:  time.Duration(ch.ClearanceMinutes) * time.Minute,
		CookieName: CookieName(domain),
	}
	if ch.Provider == guard.ProviderTurnstile && e.turnstile != nil {
		siteKey, secret, err := e.turnstile(ctx)
		if err != nil {
			return guard.Settings{}, false, fmt.Errorf("waf: read the Turnstile keys for %s: %w", domain, err)
		}
		s.TurnstileSiteKey, s.TurnstileSecret = siteKey, secret
	}
	return s, true, nil
}

// Availability reports whether ModSecurity can be used right now.
func (e *Engine) Availability(ctx context.Context) Availability {
	if e.avail == nil {
		return Availability{Flavor: "unknown", Reason: "the nginx build in use is not known"}
	}
	a, err := e.avail(ctx)
	if err != nil {
		return Availability{Flavor: "unknown", Reason: err.Error()}
	}
	return a
}

// Check refuses a config that needs a module the build lacks.
func (e *Engine) Check(ctx context.Context, c Config) error {
	if c.Challenge.Enabled {
		if !e.ChallengeAvailable() {
			return ErrNoChallenge
		}
		if c.Challenge.Provider == guard.ProviderTurnstile {
			ready, err := e.TurnstileReady(ctx)
			if err != nil {
				return err
			}
			if !ready {
				return ErrNoTurnstile
			}
		}
	}
	if !c.UsesModSecurity() {
		return nil
	}
	if a := e.Availability(ctx); !a.Available {
		return fmt.Errorf("%w: %s", ErrModuleMissing, a.Reason)
	}
	return nil
}

// Get returns a site's settings, or the defaults for a site with none.
func (e *Engine) Get(ctx context.Context, domain string) (Config, error) {
	return get(ctx, e.st.DB(), domain)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func get(ctx context.Context, q queryRower, domain string) (Config, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT json FROM site_waf WHERE domain = ?`, domain).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("waf: read %s: %w", domain, err)
	}
	c := Default()
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Config{}, fmt.Errorf("waf: the stored settings of %s are damaged: %w", domain, err)
	}
	return c.Normalize()
}

// Put stores a site's settings. c must already be normalized.
func (e *Engine) Put(ctx context.Context, domain string, c Config) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return e.st.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO site_waf (domain, json, updated_at) VALUES (?, ?, ?)
			 ON CONFLICT(domain) DO UPDATE SET json = excluded.json, updated_at = excluded.updated_at`,
			domain, string(raw), e.now().UTC().Format(time.RFC3339Nano))
		return err
	})
}

// DeleteTx removes a site's settings inside the caller's transaction.
func DeleteTx(ctx context.Context, tx *sql.Tx, domain string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM site_waf WHERE domain = ?`, domain)
	return err
}

// All returns the settings of every site that has a row.
func (e *Engine) All(ctx context.Context) (map[string]Config, error) {
	rows, err := e.st.DB().QueryContext(ctx, `SELECT domain FROM site_waf`)
	if err != nil {
		return nil, fmt.Errorf("waf: list: %w", err)
	}
	var domains []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("waf: list: %w", err)
		}
		domains = append(domains, d)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("waf: list: %w", err)
	}
	_ = rows.Close()

	out := make(map[string]Config, len(domains))
	for _, d := range domains {
		if out[d], err = e.Get(ctx, d); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// anyModSecurity reports whether some site other than except needs the module
// loaded.
func (e *Engine) anyModSecurity(ctx context.Context, except string) (bool, error) {
	all, err := e.All(ctx)
	if err != nil {
		return false, err
	}
	for d, c := range all {
		if d != except && c.UsesModSecurity() {
			return true, nil
		}
	}
	return false, nil
}

// Rendered is what a site's settings turn into.
type Rendered struct {
	// Block is the body of the site file's waf block.
	Block []byte
	// Files are the site's own firewall files. A file the settings no longer
	// need is a Delete entry, so the directory never keeps a stale rules file.
	Files []confedit.File
	// NeedsBlock is whether the site file has to carry the block at all: false
	// for settings that ask for nothing, which lets a site that predates the
	// firewall keep its file untouched.
	NeedsBlock bool
}

// Render renders a site from its stored settings.
func (e *Engine) Render(ctx context.Context, domain string) (Rendered, error) {
	c, err := e.Get(ctx, domain)
	if err != nil {
		return Rendered{}, err
	}
	g, err := e.Global(ctx)
	if err != nil {
		return Rendered{}, err
	}
	return e.RenderWith(domain, c, g), nil
}

// RenderConfig renders a site's config with no global protections. The sites
// manager renders through RenderWith; this is the site-only view.
func (e *Engine) RenderConfig(domain string, c Config) Rendered {
	return e.RenderWith(domain, c, Global{})
}

// RenderWith renders a config that is not necessarily stored yet, under the
// global firewall g.
func (e *Engine) RenderWith(domain string, c Config, g Global) Rendered {
	// A challenge stored on a panel that has since lost its key renders as off:
	// a cookie nobody can sign would lock every visitor out.
	if c.Challenge.Enabled && e.keys == nil {
		c.Challenge.Enabled = false
	}
	secret := ""
	if e.keys != nil {
		secret = e.keys.CookieSecret(domain)
	}
	r := Rendered{
		Block:      blockBody(e.p, domain, c, secret, g),
		NeedsBlock: !c.IsDefault() || g.Active(),
		Files:      []confedit.File{{Path: e.p.WAFHTTPFile(domain), Content: HTTPInclude(domain, c)}},
	}
	if c.UsesModSecurity() {
		r.Files = append(r.Files, confedit.File{Path: e.p.WAFRulesFile(domain), Content: RulesFile(e.p, domain, c)})
	} else {
		r.Files = append(r.Files, confedit.File{Path: e.p.WAFRulesFile(domain), Delete: true})
	}
	if c.Challenge.Enabled {
		r.Files = append(r.Files, confedit.File{Path: e.p.GuardPage(domain), Content: guard.Page(domain, c.Challenge.Provider)})
	} else {
		r.Files = append(r.Files, confedit.File{Path: e.p.GuardPage(domain), Delete: true})
	}
	return r
}

// SharedFiles are the files no single site owns: the one that loads the module,
// present while a site uses ModSecurity. A site named in except is not counted,
// which is how a site's deletion stops loading a module nothing else needs.
func (e *Engine) SharedFiles(ctx context.Context, except string) ([]confedit.File, error) {
	any, err := e.anyModSecurity(ctx, except)
	if err != nil {
		return nil, err
	}
	module := confedit.File{Path: e.p.WAFModuleFile(), Delete: true}
	if any {
		module = confedit.File{Path: e.p.WAFModuleFile(), Content: MainInclude()}
	}
	g, err := e.Global(ctx)
	if err != nil {
		return nil, err
	}
	page := confedit.File{Path: e.p.WAFErrorPage(), Delete: true}
	if g.ErrorPage.Enabled {
		page = confedit.File{Path: e.p.WAFErrorPage(), Content: []byte(g.ErrorPage.HTML)}
	}
	return []confedit.File{module, {Path: e.p.WAFGlobalHTTP(), Content: GlobalHTTPInclude(g)}, page}, nil
}

// Global returns the firewall that applies to every site: the defaults until an
// operator changes something.
func (e *Engine) Global(ctx context.Context) (Global, error) {
	var raw string
	err := e.st.DB().QueryRowContext(ctx, `SELECT json FROM waf_global WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultGlobal().Normalize()
	}
	if err != nil {
		return Global{}, fmt.Errorf("waf: read the global settings: %w", err)
	}
	g := DefaultGlobal()
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		return Global{}, fmt.Errorf("waf: the stored global settings are damaged: %w", err)
	}
	return g.Normalize()
}

// PutGlobal stores the global firewall. g must already be normalized.
func (e *Engine) PutGlobal(ctx context.Context, g Global) error {
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return e.st.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO waf_global (id, json, updated_at) VALUES (1, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET json = excluded.json, updated_at = excluded.updated_at`,
			string(raw), e.now().UTC().Format(time.RFC3339Nano))
		return err
	})
}

// RemovedFiles are a deleted site's firewall files.
func (e *Engine) RemovedFiles(domain string) []confedit.File {
	return []confedit.File{
		{Path: e.p.WAFHTTPFile(domain), Delete: true},
		{Path: e.p.WAFRulesFile(domain), Delete: true},
		{Path: e.p.GuardPage(domain), Delete: true},
	}
}

// AuditLog is where a site's audit events are written.
func (e *Engine) AuditLog(domain string) string { return e.p.WAFAuditLog(domain) }
