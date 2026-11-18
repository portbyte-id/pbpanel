package sites

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/waf"
)

// ErrNoWAF is returned when the firewall is not wired in.
var ErrNoWAF = errors.New("sites: the firewall is not available on this host")

// WAFStatus is what the firewall controls know about the host and the site.
type WAFStatus struct {
	waf.Availability
	// Attached is whether the site file carries the firewall block. A site made
	// before the block existed gets it the first time something is switched on.
	Attached bool `json:"attached"`
}

// WAFResult is what SetWAF did.
type WAFResult struct {
	Config waf.Config
	Status WAFStatus
	Apply  confedit.Result
}

// WAFSummary is one site's firewall at a glance, for the overview.
type WAFSummary struct {
	Domain        string   `json:"domain"`
	Mode          waf.Mode `json:"mode"`
	Paranoia      int      `json:"paranoia"`
	RateLimit     bool     `json:"rate_limit"`
	PathBlocklist int      `json:"path_blocklist"`
	IPRules       int      `json:"ip_rules"`
	Exclusions    int      `json:"exclusions"`
}

// WAFOverview is every site's firewall and whether ModSecurity can be used.
type WAFOverview struct {
	waf.Availability
	Sites []WAFSummary `json:"sites"`
}

// WAFConfig returns a site's firewall settings and status.
func (m *Manager) WAFConfig(ctx context.Context, domain string) (waf.Config, WAFStatus, error) {
	if m.waf == nil {
		return waf.Config{}, WAFStatus{}, ErrNoWAF
	}
	s, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return waf.Config{}, WAFStatus{}, err
	}
	c, err := m.waf.Get(ctx, s.Domain)
	if err != nil {
		return waf.Config{}, WAFStatus{}, err
	}
	st, err := m.wafStatus(ctx, s)
	return c, st, err
}

func (m *Manager) wafStatus(ctx context.Context, s Site) (WAFStatus, error) {
	content, err := m.fs.ReadFile(m.p.SiteFile(s.Domain))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return WAFStatus{}, fmt.Errorf("sites: read %s: %w", m.p.SiteFile(s.Domain), err)
	}
	return WAFStatus{Availability: m.waf.Availability(ctx), Attached: nginx.HasBlock(content, nginx.BlockWAF)}, nil
}

// WAFOverview lists every site's firewall.
func (m *Manager) WAFOverview(ctx context.Context) (WAFOverview, error) {
	if m.waf == nil {
		return WAFOverview{}, ErrNoWAF
	}
	list, err := m.list(ctx, m.st.DB())
	if err != nil {
		return WAFOverview{}, err
	}
	all, err := m.waf.All(ctx)
	if err != nil {
		return WAFOverview{}, err
	}
	out := WAFOverview{Availability: m.waf.Availability(ctx), Sites: make([]WAFSummary, 0, len(list))}
	for _, s := range list {
		c, ok := all[s.Domain]
		if !ok {
			c = waf.Default()
		}
		out.Sites = append(out.Sites, WAFSummary{
			Domain: s.Domain, Mode: c.Mode, Paranoia: c.Paranoia, RateLimit: c.RateLimit.Enabled,
			PathBlocklist: len(c.PathBlocklist), IPRules: len(c.IPRules.Allow) + len(c.IPRules.Deny), Exclusions: len(c.Exclusions),
		})
	}
	return out, nil
}

// SetWAF changes a site's firewall. The settings are stored, rendered into the
// site's marked block and its firewall files, and applied through confedit like
// every other change: `nginx -t` runs on the result before nginx reloads, and a
// rejection puts every file and the stored settings back.
func (m *Manager) SetWAF(ctx context.Context, domain string, in waf.Config) (WAFResult, error) {
	if m.waf == nil {
		return WAFResult{}, ErrNoWAF
	}
	c, err := in.Normalize()
	if err != nil {
		return WAFResult{}, wafInvalid(err)
	}
	if err := m.waf.Check(ctx, c); err != nil {
		if errors.Is(err, waf.ErrModuleMissing) {
			return WAFResult{}, invalid("mode", "%s", trimPrefix(err))
		}
		return WAFResult{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return WAFResult{}, err
	}
	prev, err := m.waf.Get(ctx, s.Domain)
	if err != nil {
		return WAFResult{}, err
	}

	patch, err := m.newPatch(s)
	if err != nil {
		return WAFResult{}, err
	}
	rendered := m.waf.RenderConfig(s.Domain, c)
	if rendered.NeedsBlock && len(patch.content) > 0 && !nginx.HasBlock(patch.content, nginx.BlockWAF) {
		next, ok := nginx.InsertBlock(patch.content, nginx.BlockWAF)
		if !ok {
			begin, end := nginx.BlockMarkers(nginx.BlockWAF)
			return WAFResult{}, invalid("site_file", "the site file has no `server {` line to attach the firewall to; add the two lines `%s` and `%s` inside the server block, or regenerate the site file", begin, end)
		}
		patch.content, patch.changed = next, true
	}
	patch.set(FeatureWAF, rendered.Block)

	if err := m.waf.Put(ctx, s.Domain, c); err != nil {
		return WAFResult{}, fmt.Errorf("sites: store the firewall of %s: %w", s.Domain, err)
	}
	restore := func(err error) error {
		if rbErr := m.waf.Put(context.WithoutCancel(ctx), s.Domain, prev); rbErr != nil {
			return errors.Join(err, fmt.Errorf("sites: restoring the firewall of %s in the database: %w", s.Domain, rbErr))
		}
		return err
	}

	files := append(patch.files(), rendered.Files...)
	shared, err := m.waf.SharedFiles(ctx, "")
	if err != nil {
		return WAFResult{}, restore(err)
	}
	files = append(files, shared...)
	settings, err := m.staleSettingsFiles(ctx)
	if err != nil {
		return WAFResult{}, restore(err)
	}
	files = append(files, settings...)

	if c.UsesModSecurity() {
		if err := m.prepareAuditLog(s.Domain); err != nil {
			return WAFResult{}, restore(err)
		}
	}

	res, err := m.ap.Apply(ctx, confedit.Change{Files: files, Services: []confedit.Service{m.svc}, Reason: "firewall " + s.Domain})
	if err != nil {
		return WAFResult{}, restore(err)
	}
	st, err := m.wafStatus(ctx, s)
	if err != nil {
		return WAFResult{}, err
	}
	return WAFResult{Config: c, Status: st, Apply: res}, nil
}

// wafInvalid turns the firewall's validation error into the sites one, so the
// HTTP layer maps both the same way.
func wafInvalid(err error) error {
	var ve *waf.ValidationError
	if errors.As(err, &ve) {
		return invalid(ve.Field, "%s", ve.Msg)
	}
	return err
}

// staleSettingsFiles returns the panel-main.conf and panel-http.conf the
// settings render, but only those that differ from what is on disk. A host
// that predates the firewall has files without the include lines that read the
// firewall's directories; the first firewall change brings them current in the
// same validated apply, so a zone is never defined in a file nothing includes.
func (m *Manager) staleSettingsFiles(ctx context.Context) ([]confedit.File, error) {
	if m.settings == nil {
		return nil, nil
	}
	want, err := m.settings(ctx)
	if err != nil {
		return nil, err
	}
	var out []confedit.File
	for _, f := range want {
		have, err := m.fs.ReadFile(f.Path)
		if err == nil && bytes.Equal(have, f.Content) {
			continue
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("sites: read %s: %w", f.Path, err)
		}
		out = append(out, f)
	}
	return out, nil
}

// prepareAuditLog makes the audit log exist and belong to www before nginx is
// asked to write it: ModSecurity opens the file in a worker, which runs as www.
func (m *Manager) prepareAuditLog(domain string) error {
	p := m.p.WAFAuditLog(domain)
	if _, err := m.fs.Stat(p); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("sites: %s: %w", p, err)
	}
	if err := m.fs.MkdirAll(m.p.SiteLogDir(), 0o755); err != nil {
		return fmt.Errorf("sites: create %s: %w", m.p.SiteLogDir(), err)
	}
	if err := m.fs.WriteFile(p, nil, 0o640); err != nil {
		return fmt.Errorf("sites: create %s: %w", p, err)
	}
	if err := m.fs.Chown(p, nginx.WWWUID, nginx.WWWGID); err != nil {
		_ = m.fs.Remove(p)
		return fmt.Errorf("sites: give %s to %s: %w", p, nginx.WWWUser, err)
	}
	return nil
}

// wafSiteFiles are a site's firewall files and the body of its block, from the
// stored settings. With no firewall wired in it returns nothing, and the block
// stays empty.
func (m *Manager) wafSiteFiles(ctx context.Context, domain string) (waf.Rendered, bool, error) {
	if m.waf == nil {
		return waf.Rendered{}, false, nil
	}
	r, err := m.waf.Render(ctx, domain)
	return r, true, err
}

// WAFSharedFiles are the firewall files no single site owns. The boot reconcile
// writes them with the rest.
func (m *Manager) WAFSharedFiles(ctx context.Context) ([]confedit.File, error) {
	if m.waf == nil {
		return nil, nil
	}
	return m.waf.SharedFiles(ctx, "")
}
