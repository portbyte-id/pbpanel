package sites

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/nginx"
)

// Feature is one thing the panel controls through an include file.
type Feature string

// Features, in the order the site file lists them.
const (
	FeatureListen Feature = "listen"
	FeatureNames  Feature = "names"
	FeatureACME   Feature = "acme"
	FeatureTLS    Feature = "tls"
	FeatureProxy  Feature = "proxy"
	FeaturePHP    Feature = "php"
	// FeatureWAF is not in featuresOf: a site that predates the firewall block
	// is not broken, it has not been asked for one yet. The firewall routes
	// report the block's presence themselves.
	FeatureWAF Feature = "waf"
)

// featureBlock names the marked block a feature lives in inside the site file.
// The proxy has none: it stays an include file.
var featureBlock = map[Feature]string{
	FeatureListen: nginx.BlockListen,
	FeatureNames:  nginx.BlockNames,
	FeatureACME:   nginx.BlockACME,
	FeatureTLS:    nginx.BlockTLS,
	FeaturePHP:    nginx.BlockPHP,
	FeatureWAF:    nginx.BlockWAF,
}

// featureFile is the include file a feature used before blocks, and the proxy's.
var featureFile = map[Feature]string{
	FeatureListen: nginx.IncludeListen,
	FeatureNames:  nginx.IncludeNames,
	FeatureACME:   nginx.IncludeACME,
	FeatureTLS:    nginx.IncludeTLS,
	FeatureProxy:  nginx.IncludeProxy,
	FeaturePHP:    nginx.IncludePHP,
}

// FeatureStatus is whether a site file still includes a feature's file.
type FeatureStatus struct {
	Feature  Feature `json:"feature"`
	Include  string  `json:"include"`
	Attached bool    `json:"attached"`
	// Reason says, for a detached feature, what to do about it.
	Reason string `json:"reason,omitempty"`
}

// Detail is a site with the state of its include ownership.
type Detail struct {
	Site
	SiteFile        string          `json:"site_file"`
	SiteFileMissing bool            `json:"site_file_missing"`
	Features        []FeatureStatus `json:"features"`
}

// DetachedError is returned when a change needs a control the site file has
// detached. It matches ErrDetached.
type DetachedError struct {
	Domain  string
	Feature Feature
	Include string
}

func (e *DetachedError) Error() string {
	return fmt.Sprintf("sites: %s: the %s control is off because the site file no longer has `include %s;`; restore the line or regenerate the site file",
		e.Domain, e.Feature, e.Include)
}

// Is makes errors.Is(err, ErrDetached) true.
func (e *DetachedError) Is(target error) bool { return target == ErrDetached }

func (m *Manager) featuresOf(s Site) []Feature {
	f := []Feature{FeatureListen, FeatureNames, FeatureACME, FeatureTLS}
	// The proxy include exists for every site now, but only a site with routes
	// has anything in it to keep honest.
	if len(s.ProxyRoutes) > 0 {
		f = append(f, FeatureProxy)
	}
	if s.PHPVersion != nil {
		f = append(f, FeaturePHP)
	}
	return f
}

// scan checks the site file for each expected include line. It is a literal
// line match (risk 4): nothing about the file is parsed, so a file the panel
// cannot understand still gets a straight answer.
func (m *Manager) scan(s Site) (Detail, error) {
	d := Detail{Site: s, SiteFile: m.p.SiteFile(s.Domain)}
	content, err := m.fs.ReadFile(d.SiteFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		d.SiteFileMissing = true
	case err != nil:
		return Detail{}, fmt.Errorf("sites: read %s: %w", d.SiteFile, err)
	}
	for _, f := range m.featuresOf(s) {
		inc := m.p.SiteInclude(s.Domain, featureFile[f])
		block, hasBlockName := featureBlock[f]
		inBlock := hasBlockName && nginx.HasBlock(content, block)
		st := FeatureStatus{Feature: f, Include: inc, Attached: !d.SiteFileMissing && (inBlock || nginx.HasIncludeLine(content, inc))}
		switch {
		case d.SiteFileMissing:
			st.Reason = "the site file does not exist; regenerate it"
		case !st.Attached && hasBlockName:
			begin, end := nginx.BlockMarkers(block)
			st.Reason = "the site file has no `" + begin + "` and `" + end + "` lines; restore them or regenerate the site file"
		case !st.Attached:
			st.Reason = "the site file has no `include " + inc + ";` line; restore it or regenerate the site file"
		}
		d.Features = append(d.Features, st)
	}
	return d, nil
}

// Detail returns a site with its feature states.
func (m *Manager) Detail(ctx context.Context, domain string) (Detail, error) {
	s, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return Detail{}, err
	}
	return m.scan(s)
}

func (d Detail) require(f Feature) error {
	for _, st := range d.Features {
		if st.Feature == f && !st.Attached {
			return &DetachedError{Domain: d.Domain, Feature: f, Include: st.Include}
		}
	}
	return nil
}

// UpdateInput is what a panel action may change. A nil field is left alone.
type UpdateInput struct {
	Aliases *[]string
	// ProxyRoutes replaces the whole route list. ProxyTarget is the old
	// single-root spelling: it replaces the list with one "/" route, and is
	// refused alongside ProxyRoutes rather than guessed between.
	ProxyRoutes *[]ProxyRoute
	ProxyTarget *string
	// Rewrite changes a PHP site's URL-rewrite preset; it re-renders php.conf.
	Rewrite *Rewrite
}

// UpdateResult is what Update did.
type UpdateResult struct {
	Site  Site
	Apply confedit.Result
}

// Update changes a site's aliases or proxy target. The aliases are rewritten in
// their block of the site file, the proxy target in the proxy include file. A control the site file has
// detached is refused with a DetachedError instead of being written into a file
// nothing reads.
func (m *Manager) Update(ctx context.Context, domain string, in UpdateInput) (UpdateResult, error) {
	if in.Aliases == nil && in.ProxyRoutes == nil && in.ProxyTarget == nil && in.Rewrite == nil {
		return UpdateResult{}, invalid("body", "nothing to change")
	}
	if in.ProxyRoutes != nil && in.ProxyTarget != nil {
		return UpdateResult{}, invalid("proxy_routes", "give either proxy_target or proxy_routes, not both")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	prev, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return UpdateResult{}, err
	}
	detail, err := m.scan(prev)
	if err != nil {
		return UpdateResult{}, err
	}

	next := prev
	next.Aliases = append([]string(nil), prev.Aliases...)
	patch, err := m.newPatch(prev)
	if err != nil {
		return UpdateResult{}, err
	}

	if in.Aliases != nil {
		if err := detail.require(FeatureNames); err != nil {
			return UpdateResult{}, err
		}
		if next.Aliases, err = normalizeAliases(prev.Domain, *in.Aliases); err != nil {
			return UpdateResult{}, err
		}
		b, err := nginx.RenderNames(next.Domain, next.Aliases)
		if err != nil {
			return UpdateResult{}, err
		}
		patch.set(FeatureNames, b)
	}

	// The routes: the whole list, or the old single target as a "/" route.
	var routes []ProxyRoute
	changeRoutes := false
	switch {
	case in.ProxyRoutes != nil:
		routes, changeRoutes = *in.ProxyRoutes, true
	case in.ProxyTarget != nil:
		routes, changeRoutes = []ProxyRoute{{Path: "/", Target: *in.ProxyTarget}}, true
	}
	if changeRoutes {
		// A route edit writes the proxy include, which a site file must still
		// include. A site that had no routes has no proxy feature in its detail
		// yet (the include exists from creation), so the requirement is only
		// checked when the previous site had one.
		if len(prev.ProxyRoutes) > 0 {
			if err := detail.require(FeatureProxy); err != nil {
				return UpdateResult{}, err
			}
		}
		if next.ProxyRoutes, err = m.checkRoutes(routes, prev); err != nil {
			return UpdateResult{}, err
		}
		next.ProxyTarget = ""
		for _, r := range next.ProxyRoutes {
			if r.Path == "/" {
				next.ProxyTarget = r.Target
			}
		}
		b, err := nginx.RenderProxy(next.ProxyRoutes)
		if err != nil {
			return UpdateResult{}, err
		}
		// A site file written before proxy routes existed has no route list; a
		// first route also needs the site file's include line and a static
		// site's `location /` back-fixed, which is what Regenerate is for. The
		// include line is written here; a static site that gains its first route
		// on a subpath keeps its own location, which costs nothing.
		patch.set(FeatureProxy, b)
	}
	if in.Rewrite != nil {
		if prev.PHPVersion == nil {
			return UpdateResult{}, invalid("rewrite", "only a site that runs PHP has a URL-rewrite preset")
		}
		if !ValidRewrite(*in.Rewrite) {
			return UpdateResult{}, invalid("rewrite", "choose one of the URL-rewrite presets")
		}
		if err := detail.require(FeaturePHP); err != nil {
			return UpdateResult{}, err
		}
		next.Rewrite = *in.Rewrite
		b, err := m.renderPHPInclude(next)
		if err != nil {
			return UpdateResult{}, err
		}
		patch.set(FeaturePHP, b)
	}

	if err := m.store(ctx, next); err != nil {
		return UpdateResult{}, err
	}
	files := patch.files()
	services := []confedit.Service{m.svc}
	// An alias is a name the site answers to, and the open-appsec policy names
	// every one of them, so a change to the names re-renders the policy in the
	// same apply. The module loader goes with it — the policy and the module are
	// never written one without the other — and the agent reads the new policy
	// after nginx has taken the new names.
	if in.Aliases != nil && m.waf != nil {
		shared, err := m.waf.SharedFiles(ctx, "")
		if err != nil {
			return UpdateResult{}, err
		}
		policy, err := m.openAppsecPolicyFile(ctx, "")
		if err != nil {
			return UpdateResult{}, err
		}
		files = append(append(files, shared...), policy)
		c, err := m.waf.Get(ctx, next.Domain)
		if err != nil {
			return UpdateResult{}, err
		}
		if svcs, err := m.wafServices(ctx, "", c.UsesOpenAppsec()); err != nil {
			return UpdateResult{}, err
		} else {
			services = svcs
		}
	}
	res, err := m.ap.Apply(ctx, confedit.Change{Files: files, Services: services, Reason: "edit site " + next.Domain})
	if err != nil {
		if rbErr := m.store(context.WithoutCancel(ctx), prev); rbErr != nil {
			err = errors.Join(err, fmt.Errorf("sites: restoring %s in the database: %w", prev.Domain, rbErr))
		}
		return UpdateResult{}, err
	}
	return UpdateResult{Site: next, Apply: res}, nil
}

// store writes the editable fields of an existing site.
func (m *Manager) store(ctx context.Context, s Site) error {
	return m.st.WithTx(ctx, func(tx *sql.Tx) error {
		if name, taken, err := claimed(ctx, tx, s.Aliases, s.Domain); err != nil {
			return err
		} else if taken {
			return fmt.Errorf("%w: %s", ErrConflict, name)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sites SET rewrite = ? WHERE domain = ?`, string(s.Rewrite), s.Domain); err != nil {
			return fmt.Errorf("sites: update %s: %w", s.Domain, err)
		}
		if err := setProxyRoutes(ctx, tx, s.Domain, s.ProxyRoutes); err != nil {
			return err
		}
		return setAliases(ctx, tx, s.Domain, s.Aliases)
	})
}

// Regenerate rewrites a site's file, and its include files, from the templates
// and the database. It is the way back from a detached feature, and it
// discards the operator's edits to the site file, so callers confirm first.
func (m *Manager) Regenerate(ctx context.Context, domain string) (confedit.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return confedit.Result{}, err
	}
	files, err := m.allFiles(ctx, s)
	if err != nil {
		return confedit.Result{}, err
	}
	return m.ap.Apply(ctx, confedit.Change{Files: files, Services: []confedit.Service{m.svc}, Reason: "regenerate site file " + s.Domain})
}

// RefreshTLS re-renders a site's TLS block from the certificate store, so a
// newly issued, renewed or removed certificate reaches nginx. Only that block is
// rewritten, validated and reloaded. A site file that no longer
// includes it is refused with a DetachedError: writing the file would change
// nothing nginx reads, and the caller must not report a certificate as live.
func (m *Manager) RefreshTLS(ctx context.Context, domain string) (confedit.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return confedit.Result{}, err
	}
	detail, err := m.scan(s)
	if err != nil {
		return confedit.Result{}, err
	}
	if err := detail.require(FeatureTLS); err != nil {
		return confedit.Result{}, err
	}
	body, err := m.tlsBody(ctx, s)
	if err != nil {
		return confedit.Result{}, err
	}
	patch, err := m.newPatch(s)
	if err != nil {
		return confedit.Result{}, err
	}
	patch.set(FeatureTLS, body)
	return m.ap.Apply(ctx, confedit.Change{Files: patch.files(), Services: []confedit.Service{m.svc}, Reason: "refresh TLS " + s.Domain})
}

func trimPrefix(err error) string {
	const p = "nginx: "
	if s := err.Error(); len(s) > len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return err.Error()
}

// sitePatch is an edit of one site's configuration in progress: the site file as
// it is on disk, with managed parts replaced in place. A part whose block is not
// in the file but whose include line is (a site made before blocks) is written to
// its include file instead, so older sites keep working untouched.
type sitePatch struct {
	m       *Manager
	s       Site
	content []byte
	changed bool
	extra   []confedit.File
}

func (m *Manager) newPatch(s Site) (*sitePatch, error) {
	content, err := m.fs.ReadFile(m.p.SiteFile(s.Domain))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("sites: read %s: %w", m.p.SiteFile(s.Domain), err)
	}
	return &sitePatch{m: m, s: s, content: content}, nil
}

// set replaces one feature's managed part with body.
func (p *sitePatch) set(f Feature, body []byte) {
	if block, ok := featureBlock[f]; ok {
		if next, found := nginx.ReplaceBlock(p.content, block, body); found {
			p.content, p.changed = next, true
			return
		}
	}
	if f == FeatureWAF {
		// The firewall never had an include file: a site file without its block
		// simply has no firewall yet.
		return
	}
	inc := p.m.p.SiteInclude(p.s.Domain, featureFile[f])
	// A block feature whose markers and include line are both gone is detached:
	// writing its include file would leave a file nothing reads.
	if _, isBlock := featureBlock[f]; isBlock && len(p.content) > 0 && !nginx.HasIncludeLine(p.content, inc) {
		return
	}
	p.extra = append(p.extra, confedit.File{Path: inc, Content: body})
}

// files is everything the patch wants written.
func (p *sitePatch) files() []confedit.File {
	out := append([]confedit.File(nil), p.extra...)
	if p.changed {
		out = append(out, confedit.File{Path: p.m.p.SiteFile(p.s.Domain), Content: p.content})
	}
	return out
}

// managedFiles is what the panel wants on disk for a site's managed parts, each
// written where the site keeps it: in its block of the site file, or in its
// include file for a site made before blocks. The boot reconcile writes only the
// files that differ.
func (m *Manager) managedFiles(ctx context.Context, s Site) ([]confedit.File, error) {
	patch, err := m.newPatch(s)
	if err != nil {
		return nil, err
	}
	listen, err := nginx.RenderListen()
	if err != nil {
		return nil, err
	}
	names, err := nginx.RenderNames(s.Domain, s.Aliases)
	if err != nil {
		return nil, err
	}
	acme, err := m.p.RenderACME()
	if err != nil {
		return nil, err
	}
	tls, err := m.tlsBody(ctx, s)
	if err != nil {
		return nil, err
	}
	patch.set(FeatureListen, listen)
	patch.set(FeatureNames, names)
	patch.set(FeatureACME, acme)
	patch.set(FeatureTLS, tls)
	files := []confedit.File(nil)
	if rendered, ok, err := m.wafSiteFiles(ctx, s.Domain); err != nil {
		return nil, err
	} else if ok {
		// Only a site file that already has the block is touched here; one that
		// predates it gets the block when the firewall is first switched on.
		patch.set(FeatureWAF, rendered.Block)
		files = rendered.Files
	}
	// The proxy include is kept current for every site: a site with routes has
	// its locations in it, one without has a comment saying so.
	proxy, err := nginx.RenderProxy(s.ProxyRoutes)
	if err != nil {
		return nil, err
	}
	patch.set(FeatureProxy, proxy)
	if s.PHPVersion != nil {
		body, err := m.renderPHPInclude(s)
		if err != nil {
			return nil, err
		}
		patch.set(FeaturePHP, body)
	}
	return append(patch.files(), files...), nil
}
