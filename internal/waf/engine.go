package waf

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
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

// Engine keeps the per-site settings and renders them.
type Engine struct {
	st    *store.Store
	p     nginx.Paths
	avail AvailabilityFunc
	now   func() time.Time
}

// New returns the engine. avail may be nil, which reports ModSecurity as
// unavailable.
func New(st *store.Store, p nginx.Paths, avail AvailabilityFunc) *Engine {
	return &Engine{st: st, p: p, avail: avail, now: time.Now}
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
	return e.RenderConfig(domain, c), nil
}

// RenderConfig renders a config that is not necessarily stored yet.
func (e *Engine) RenderConfig(domain string, c Config) Rendered {
	r := Rendered{
		Block:      BlockBody(e.p, domain, c),
		NeedsBlock: !c.IsDefault(),
		Files:      []confedit.File{{Path: e.p.WAFHTTPFile(domain), Content: HTTPInclude(domain, c)}},
	}
	if c.UsesModSecurity() {
		r.Files = append(r.Files, confedit.File{Path: e.p.WAFRulesFile(domain), Content: RulesFile(e.p, domain, c)})
	} else {
		r.Files = append(r.Files, confedit.File{Path: e.p.WAFRulesFile(domain), Delete: true})
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
	if any {
		return []confedit.File{{Path: e.p.WAFModuleFile(), Content: MainInclude()}}, nil
	}
	return []confedit.File{{Path: e.p.WAFModuleFile(), Delete: true}}, nil
}

// RemovedFiles are a deleted site's firewall files.
func (e *Engine) RemovedFiles(domain string) []confedit.File {
	return []confedit.File{
		{Path: e.p.WAFHTTPFile(domain), Delete: true},
		{Path: e.p.WAFRulesFile(domain), Delete: true},
	}
}

// AuditLog is where a site's audit events are written.
func (e *Engine) AuditLog(domain string) string { return e.p.WAFAuditLog(domain) }
