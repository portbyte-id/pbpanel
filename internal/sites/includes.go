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
	FeatureProxy  Feature = "proxy"
)

var featureFile = map[Feature]string{
	FeatureListen: nginx.IncludeListen,
	FeatureNames:  nginx.IncludeNames,
	FeatureACME:   nginx.IncludeACME,
	FeatureProxy:  nginx.IncludeProxy,
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
	f := []Feature{FeatureListen, FeatureNames, FeatureACME}
	if s.Kind == KindProxy {
		f = append(f, FeatureProxy)
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
		st := FeatureStatus{Feature: f, Include: inc, Attached: !d.SiteFileMissing && nginx.HasIncludeLine(content, inc)}
		switch {
		case d.SiteFileMissing:
			st.Reason = "the site file does not exist; regenerate it"
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
	Aliases     *[]string
	ProxyTarget *string
}

// UpdateResult is what Update did.
type UpdateResult struct {
	Site  Site
	Apply confedit.Result
}

// Update changes a site's aliases or proxy target. Each rewrites only its own
// include file; the site file is never touched. A control the site file has
// detached is refused with a DetachedError instead of being written into a file
// nothing reads.
func (m *Manager) Update(ctx context.Context, domain string, in UpdateInput) (UpdateResult, error) {
	if in.Aliases == nil && in.ProxyTarget == nil {
		return UpdateResult{}, invalid("body", "nothing to change")
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
	var files []confedit.File

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
		files = append(files, confedit.File{Path: m.p.SiteInclude(next.Domain, nginx.IncludeNames), Content: b})
	}
	if in.ProxyTarget != nil {
		if prev.Kind != KindProxy {
			return UpdateResult{}, invalid("proxy_target", "only a reverse-proxy site has one")
		}
		if err := detail.require(FeatureProxy); err != nil {
			return UpdateResult{}, err
		}
		t, err := nginx.CheckProxyTarget(*in.ProxyTarget)
		if err != nil {
			return UpdateResult{}, invalid("proxy_target", "%s", trimPrefix(err))
		}
		next.ProxyTarget = t
		b, err := nginx.RenderProxy(t)
		if err != nil {
			return UpdateResult{}, err
		}
		files = append(files, confedit.File{Path: m.p.SiteInclude(next.Domain, nginx.IncludeProxy), Content: b})
	}

	if err := m.store(ctx, next); err != nil {
		return UpdateResult{}, err
	}
	res, err := m.ap.Apply(ctx, confedit.Change{Files: files, Services: []confedit.Service{m.svc}, Reason: "edit site " + next.Domain})
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
		if _, err := tx.ExecContext(ctx, `UPDATE sites SET proxy_target = ? WHERE domain = ?`, s.ProxyTarget, s.Domain); err != nil {
			return fmt.Errorf("sites: update %s: %w", s.Domain, err)
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
	files, err := m.allFiles(s)
	if err != nil {
		return confedit.Result{}, err
	}
	return m.ap.Apply(ctx, confedit.Change{Files: files, Services: []confedit.Service{m.svc}, Reason: "regenerate site file " + s.Domain})
}

func trimPrefix(err error) string {
	const p = "nginx: "
	if s := err.Error(); len(s) > len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return err.Error()
}
