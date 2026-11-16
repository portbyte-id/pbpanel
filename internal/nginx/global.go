package nginx

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/store"
)

// Applier is the part of confedit.Editor the managers use.
type Applier interface {
	Apply(ctx context.Context, ch confedit.Change) (confedit.Result, error)
}

const settingsName = "http"

// Global owns the settings that apply to all of nginx: the common-settings
// form, and nginx.conf as raw text.
type Global struct {
	st  *store.Store
	ap  Applier
	svc confedit.Service
	p   Paths
	// read exists so tests do not need a real filesystem.
	read func(string) ([]byte, error)

	mu sync.Mutex
}

// NewGlobal returns the global config manager. svc is the live nginx service.
func NewGlobal(st *store.Store, ap Applier, svc confedit.Service, p Paths) *Global {
	return &Global{st: st, ap: ap, svc: svc, p: p, read: os.ReadFile}
}

// Settings returns the stored settings, or the defaults on a fresh install.
func (g *Global) Settings(ctx context.Context) (HTTPSettings, error) {
	var raw string
	err := g.st.DB().QueryRowContext(ctx, `SELECT json FROM nginx_settings WHERE name = ?`, settingsName).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultHTTPSettings(), nil
	}
	if err != nil {
		return HTTPSettings{}, fmt.Errorf("nginx: read settings: %w", err)
	}
	s := DefaultHTTPSettings() // fields added later keep their default
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return HTTPSettings{}, fmt.Errorf("nginx: stored settings are damaged: %w", err)
	}
	return s, nil
}

func (g *Global) saveSettings(ctx context.Context, s HTTPSettings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return g.st.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO nginx_settings (name, json) VALUES (?, ?)
			 ON CONFLICT(name) DO UPDATE SET json = excluded.json`, settingsName, string(raw))
		return err
	})
}

// SettingsFiles renders the two include files the settings own.
func (g *Global) SettingsFiles(s HTTPSettings) ([]confedit.File, error) {
	main, err := RenderMainInclude(s)
	if err != nil {
		return nil, err
	}
	http, err := RenderHTTPInclude(s)
	if err != nil {
		return nil, err
	}
	return []confedit.File{
		{Path: g.p.MainIncludePath(), Content: main},
		{Path: g.p.HTTPIncludePath(), Content: http},
	}, nil
}

// SetSettings validates the settings, stores them, and applies them through
// confedit. The database is written first (the convention), and put back if the
// apply fails, so a rejected change leaves no trace in either place. The
// validator's output is in the returned error, verbatim.
func (g *Global) SetSettings(ctx context.Context, s HTTPSettings) (confedit.Result, error) {
	if err := s.Validate(); err != nil {
		return confedit.Result{}, err
	}
	files, err := g.SettingsFiles(s)
	if err != nil {
		return confedit.Result{}, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	prev, err := g.Settings(ctx)
	if err != nil {
		return confedit.Result{}, err
	}
	if err := g.saveSettings(ctx, s); err != nil {
		return confedit.Result{}, fmt.Errorf("nginx: store settings: %w", err)
	}
	res, err := g.ap.Apply(ctx, confedit.Change{Files: files, Services: []confedit.Service{g.svc}, Reason: "nginx settings"})
	if err != nil {
		if rbErr := g.saveSettings(context.WithoutCancel(ctx), prev); rbErr != nil {
			err = errors.Join(err, fmt.Errorf("nginx: restoring the stored settings: %w", rbErr))
		}
		return confedit.Result{}, err
	}
	return res, nil
}

// ReadConf returns nginx.conf as it is on disk.
func (g *Global) ReadConf(context.Context) ([]byte, error) {
	b, err := g.read(g.p.ConfPath())
	if err != nil {
		return nil, fmt.Errorf("nginx: read nginx.conf: %w", err)
	}
	return b, nil
}

// WriteConf replaces nginx.conf with the operator's text. A rejection by
// `nginx -t` comes back verbatim and leaves the old file in place and nginx
// serving.
func (g *Global) WriteConf(ctx context.Context, content []byte) (confedit.Result, error) {
	return g.ap.Apply(ctx, confedit.Change{
		Files:    []confedit.File{{Path: g.p.ConfPath(), Content: content}},
		Services: []confedit.Service{g.svc},
		Reason:   "edit nginx.conf",
	})
}

// ConfLinks says which of the includes nginx.conf is expected to carry are
// missing. A missing line is not an error: the operator may have meant it. It
// is reported so the UI can say which setting stopped having an effect.
func (g *Global) ConfLinks(content []byte) []string {
	var missing []string
	for _, l := range []struct{ file, what string }{
		{g.p.MainIncludePath(), "worker processes"},
		{g.p.HTTPIncludePath(), "the common settings"},
		{g.p.SitesDir() + "/*.conf", "every site"},
	} {
		if !HasIncludeLine(content, l.file) {
			missing = append(missing, l.what)
		}
	}
	return missing
}

// ReadWith replaces how nginx.conf is read and returns g. It exists for tests
// that have no real filesystem.
func (g *Global) ReadWith(read func(string) ([]byte, error)) *Global {
	g.read = read
	return g
}
