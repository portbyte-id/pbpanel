package sites

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/guard"
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
	// ChallengeAvailable is whether the challenge page can be switched on: it
	// needs the panel's secret key.
	ChallengeAvailable bool `json:"challenge_available"`
	// TurnstileReady is whether Settings holds both the Turnstile site key and
	// secret key. A site cannot use Turnstile until it does.
	TurnstileReady bool `json:"turnstile_ready"`
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
	// Challenge is the provider of the challenge page, or "" when it is off.
	Challenge guard.Provider `json:"challenge"`
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
	ready, err := m.waf.TurnstileReady(ctx)
	if err != nil {
		return WAFStatus{}, err
	}
	return WAFStatus{
		Availability:       m.waf.Availability(ctx),
		Attached:           nginx.HasBlock(content, nginx.BlockWAF),
		ChallengeAvailable: m.waf.ChallengeAvailable(),
		TurnstileReady:     ready,
	}, nil
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
		sum := WAFSummary{
			Domain: s.Domain, Mode: c.Mode, Paranoia: c.Paranoia, RateLimit: c.RateLimit.Enabled,
			PathBlocklist: len(c.PathBlocklist), IPRules: len(c.IPRules.Allow) + len(c.IPRules.Deny), Exclusions: len(c.Exclusions),
		}
		if c.Challenge.Enabled {
			sum.Challenge = c.Challenge.Provider
		}
		out.Sites = append(out.Sites, sum)
	}
	return out, nil
}

// SetWAF changes a site's firewall. The settings are stored, rendered into the
// site's marked block and its firewall files, and applied through confedit like
// every other change: `nginx -t` runs on the result before nginx reloads, and a
// rejection puts every file and the stored settings back.
// wafApplyTimeout bounds one firewall apply: the nginx reload plus the agent's
// policy load, which is slow while the agent is busy.
const wafApplyTimeout = 5 * time.Minute

// wafApplyContext detaches an apply from the caller's context. The HTTP request
// is cancelled when the client disconnects, and a cancelled context kills the
// agent's --apply-policy mid-run and strands the rollback. The apply must finish
// or fail on its own clock, so it runs on a context that outlives the request.
func wafApplyContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), wafApplyTimeout)
}

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
		if errors.Is(err, waf.ErrOpenAppsecMissing) {
			return WAFResult{}, invalid("engine", "%s", trimPrefix(err))
		}
		if errors.Is(err, waf.ErrNoChallenge) || errors.Is(err, waf.ErrNoTurnstile) {
			return WAFResult{}, invalid("challenge.enabled", "%s", trimPrefix(err))
		}
		return WAFResult{}, err
	}
	if err := m.requireAppsec(c); err != nil {
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
	g, err := m.waf.Global(ctx)
	if err != nil {
		return WAFResult{}, err
	}
	rendered := m.waf.RenderWith(s.Domain, c, g)
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
	// The policy is rendered from the settings just stored, so a change that
	// switches the engine on, off, or to another mode lands in the same apply.
	policy, err := m.openAppsecPolicyFile(ctx, "")
	if err != nil {
		return WAFResult{}, restore(err)
	}
	files = append(files, policy)
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

	services, err := m.wafServices(ctx, "", c.UsesOpenAppsec())
	if err != nil {
		return WAFResult{}, restore(err)
	}
	res, err := m.ap.Apply(ctx, confedit.Change{Files: files, Services: services, Reason: "firewall " + s.Domain})
	if err != nil {
		return WAFResult{}, restore(err)
	}
	st, err := m.wafStatus(ctx, s)
	if err != nil {
		return WAFResult{}, err
	}
	return WAFResult{Config: c, Status: st, Apply: res}, nil
}

// wafServices is the services one firewall change has to drive: nginx first,
// then the open-appsec agent when the change leaves a site inspecting with it.
//
// Order matters and is the one step 0 found: a lone agent restart silently stops
// enforcement until nginx reloads (plan §0.4), so nginx is reloaded *before* the
// agent is told about a policy, never after. The agent has no policy when no site
// uses it, so it is left out of the change entirely.
func (m *Manager) wafServices(ctx context.Context, except string, usesOpenAppsec bool) ([]confedit.Service, error) {
	services := []confedit.Service{m.svc}
	if !usesOpenAppsec || m.appsec == nil {
		return services, nil
	}
	any, err := m.waf.AnyOpenAppsec(ctx, except)
	if err != nil {
		return nil, err
	}
	if any {
		services = append(services, m.appsec)
	}
	return services, nil
}

// requireAppsec refuses an open-appsec config on a host where the agent is not
// wired in. It runs before anything is stored or written, so the previous state
// is untouched.
func (m *Manager) requireAppsec(c waf.Config) error {
	if !c.UsesOpenAppsec() || m.appsec != nil {
		return nil
	}
	return invalid("engine", "%s", "open-appsec is not installed on this host; install it from the Store first")
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
//
// The engine renders the module loaders and the block page; the open-appsec
// policy is added here because it names hosts, which live in this package's
// table. Every site with the engine on becomes one specificRules entry, with its
// domain and aliases.
func (m *Manager) WAFSharedFiles(ctx context.Context) ([]confedit.File, error) {
	if m.waf == nil {
		return nil, nil
	}
	files, err := m.waf.SharedFiles(ctx, "")
	if err != nil {
		return nil, err
	}
	policy, err := m.openAppsecPolicyFile(ctx, "")
	if err != nil {
		return nil, err
	}
	return append(files, policy), nil
}

// openAppsecPolicyFile renders the agent's local policy from every site that
// inspects with open-appsec, a site named in except left out. It is a Delete
// entry when no site uses the engine, so the file goes with the module that
// needs it.
//
// The result is deterministic: the same sites render the same bytes, so an
// unrelated change does not rewrite the policy and, with it, reload the agent.
func (m *Manager) openAppsecPolicyFile(ctx context.Context, except string) (confedit.File, error) {
	file := confedit.File{Path: m.waf.OpenAppsecPolicyFile(), Delete: true}
	any, err := m.waf.AnyOpenAppsec(ctx, except)
	if err != nil {
		return confedit.File{}, err
	}
	if !any {
		return file, nil
	}
	sites, err := m.list(ctx, m.st.DB())
	if err != nil {
		return confedit.File{}, err
	}
	all, err := m.waf.All(ctx)
	if err != nil {
		return confedit.File{}, err
	}
	entries := make([]waf.OpenAppsecPolicy, 0, len(sites))
	for _, s := range sites {
		if s.Domain == except {
			continue
		}
		c, ok := all[s.Domain]
		if !ok || !c.UsesOpenAppsec() {
			continue
		}
		hosts := append([]string{s.Domain}, s.Aliases...)
		entries = append(entries, waf.OpenAppsecPolicy{Hosts: hosts, Mode: c.Mode, Confidence: c.OpenAppsec.Confidence})
	}
	// The module exists because a site uses the engine, so an empty list here
	// means the two reads disagree. An empty policy would leave every host
	// inactive while the per-server directive still claims inspection; refusing
	// is the only honest answer.
	if len(entries) == 0 {
		return confedit.File{}, fmt.Errorf("sites: a site uses open-appsec but none was found; refusing to write a policy that would inspect nothing")
	}
	file.Delete, file.Content = false, waf.RenderOpenAppsecPolicy(entries)
	return file, nil
}

// WAFGlobalResult is what SetWAFGlobal did.
type WAFGlobalResult struct {
	Global waf.Global
	Apply  confedit.Result
}

// WAFGlobal returns the global firewall.
func (m *Manager) WAFGlobal(ctx context.Context) (waf.Global, error) {
	if m.waf == nil {
		return waf.Global{}, ErrNoWAF
	}
	return m.waf.Global(ctx)
}

// SetWAFGlobal changes the global firewall. Every site's block is rendered again
// under the new settings, and a site that now needs a block gets one inserted,
// all in one apply: `nginx -t` runs on the result before nginx reloads, and a
// rejection puts every file and the stored settings back.
func (m *Manager) SetWAFGlobal(ctx context.Context, in waf.Global) (WAFGlobalResult, error) {
	if m.waf == nil {
		return WAFGlobalResult{}, ErrNoWAF
	}
	g, err := in.Normalize()
	if err != nil {
		return WAFGlobalResult{}, wafInvalid(err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	prev, err := m.waf.Global(ctx)
	if err != nil {
		return WAFGlobalResult{}, err
	}
	if err := m.waf.PutGlobal(ctx, g); err != nil {
		return WAFGlobalResult{}, fmt.Errorf("sites: store the global firewall: %w", err)
	}
	restore := func(err error) error {
		if rbErr := m.waf.PutGlobal(context.WithoutCancel(ctx), prev); rbErr != nil {
			return errors.Join(err, fmt.Errorf("sites: restoring the global firewall in the database: %w", rbErr))
		}
		return err
	}

	list, err := m.list(ctx, m.st.DB())
	if err != nil {
		return WAFGlobalResult{}, restore(err)
	}
	var files []confedit.File
	for _, s := range list {
		c, err := m.waf.Get(ctx, s.Domain)
		if err != nil {
			return WAFGlobalResult{}, restore(err)
		}
		patch, err := m.newPatch(s)
		if err != nil {
			return WAFGlobalResult{}, restore(err)
		}
		rendered := m.waf.RenderWith(s.Domain, c, g)
		if rendered.NeedsBlock && len(patch.content) > 0 && !nginx.HasBlock(patch.content, nginx.BlockWAF) {
			next, ok := nginx.InsertBlock(patch.content, nginx.BlockWAF)
			if !ok {
				begin, end := nginx.BlockMarkers(nginx.BlockWAF)
				return WAFGlobalResult{}, restore(invalid("site_file", "%s has no `server {` line to attach the firewall to; add the two lines `%s` and `%s` inside the server block, or regenerate the site file", s.Domain, begin, end))
			}
			patch.content, patch.changed = next, true
		}
		patch.set(FeatureWAF, rendered.Block)
		files = append(files, patch.files()...)
		files = append(files, rendered.Files...)
		if c.UsesModSecurity() {
			if err := m.prepareAuditLog(s.Domain); err != nil {
				return WAFGlobalResult{}, restore(err)
			}
		}
	}
	shared, err := m.waf.SharedFiles(ctx, "")
	if err != nil {
		return WAFGlobalResult{}, restore(err)
	}
	files = append(files, shared...)
	// The global firewall does not touch an engine's settings, but every site's
	// block was rendered again above, so the policy is re-rendered with it: the
	// two are one change or neither.
	policy, err := m.openAppsecPolicyFile(ctx, "")
	if err != nil {
		return WAFGlobalResult{}, restore(err)
	}
	files = append(files, policy)
	settings, err := m.staleSettingsFiles(ctx)
	if err != nil {
		return WAFGlobalResult{}, restore(err)
	}
	files = append(files, settings...)

	services, err := m.wafServices(ctx, "", true)
	if err != nil {
		return WAFGlobalResult{}, restore(err)
	}
	applyCtx, cancel := wafApplyContext(ctx)
	defer cancel()
	res, err := m.ap.Apply(applyCtx, confedit.Change{Files: files, Services: services, Reason: "firewall: global"})
	if err != nil {
		return WAFGlobalResult{}, restore(err)
	}
	return WAFGlobalResult{Global: g, Apply: res}, nil
}
