package sites

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/nginx"
)

// SiteFile is a site's own file as it is on disk.
type SiteFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Missing bool   `json:"missing"`
}

// ReadFile returns the site file. A file that does not exist is reported as
// Missing with no content, not as an error: the editor then offers to
// regenerate it.
func (m *Manager) ReadFile(ctx context.Context, domain string) (SiteFile, error) {
	s, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return SiteFile{}, err
	}
	out := SiteFile{Path: m.p.SiteFile(s.Domain)}
	b, err := m.fs.ReadFile(out.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		out.Missing = true
	case err != nil:
		return SiteFile{}, fmt.Errorf("sites: read %s: %w", out.Path, err)
	}
	out.Content = string(b)
	return out, nil
}

// WriteFile replaces the site file with the operator's text, through confedit:
// nginx -t must accept it, or the old file is put back and the validator's
// output comes back in the error.
func (m *Manager) WriteFile(ctx context.Context, domain string, content []byte) (confedit.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return confedit.Result{}, err
	}
	return m.ap.Apply(ctx, confedit.Change{
		Files:    []confedit.File{{Path: m.p.SiteFile(s.Domain), Content: content}},
		Services: []confedit.Service{m.svc},
		Reason:   "edit site file " + s.Domain,
	})
}

// includeNames is the include files a site may hold, in the order the Config
// tab lists them. The proxy include is the one a reverse-proxy site keeps its
// `location` block in.
var includeNames = []struct {
	Name  string
	Label string
}{
	{nginx.IncludeProxy, "Proxy"},
	{nginx.IncludeListen, "Ports"},
	{nginx.IncludeNames, "Domains"},
	{nginx.IncludeACME, "Certificate challenges"},
	{nginx.IncludeTLS, "HTTPS"},
	{nginx.IncludePHP, "PHP"},
}

// IncludeFile is one of a site's panel-owned include files, as the Config tab
// shows it. Missing means the site does not keep this file (a site built after
// blocks keeps the feature in its own file instead).
type IncludeFile struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Missing bool   `json:"missing"`
}

// IncludeFiles lists a site's include files with their content, so the Config
// tab can offer each one for editing. Only the files that exist for this site's
// kind are returned: a static site has no proxy or PHP include.
func (m *Manager) IncludeFiles(ctx context.Context, domain string) ([]IncludeFile, error) {
	s, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return nil, err
	}
	var out []IncludeFile
	for _, inc := range includeNames {
		if !m.siteHasInclude(s, inc.Name) {
			continue
		}
		f := IncludeFile{Name: inc.Name, Label: inc.Label, Path: m.p.SiteInclude(s.Domain, inc.Name)}
		b, err := m.fs.ReadFile(f.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			f.Missing = true
		case err != nil:
			return nil, fmt.Errorf("sites: read %s: %w", f.Path, err)
		}
		f.Content = string(b)
		out = append(out, f)
	}
	return out, nil
}

// siteHasInclude reports whether an include name applies to a site's kind.
func (m *Manager) siteHasInclude(s Site, name string) bool {
	switch name {
	case nginx.IncludeProxy:
		return s.Kind == KindProxy
	case nginx.IncludePHP:
		return s.Kind == KindPHP
	default:
		return true
	}
}

// checkIncludeName refuses an include name that is not one this build knows,
// so a caller cannot ask for an arbitrary file in the includes directory.
func checkIncludeName(name string) error {
	for _, inc := range includeNames {
		if inc.Name == name {
			return nil
		}
	}
	return invalid("include", "%q is not one of this site's include files", name)
}

// ReadInclude returns one include file for editing. A file that does not exist
// is reported Missing rather than as an error.
func (m *Manager) ReadInclude(ctx context.Context, domain, name string) (IncludeFile, error) {
	if err := checkIncludeName(name); err != nil {
		return IncludeFile{}, err
	}
	s, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return IncludeFile{}, err
	}
	f := IncludeFile{Path: m.p.SiteInclude(s.Domain, name)}
	b, err := m.fs.ReadFile(f.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f.Missing = true
	case err != nil:
		return IncludeFile{}, fmt.Errorf("sites: read %s: %w", f.Path, err)
	}
	f.Content = string(b)
	f.Name = path.Base(f.Path)
	return f, nil
}

// WriteInclude replaces one include file with the operator's text, through
// confedit: nginx -t must accept it, or the old file is put back and the
// validator's output comes back in the error. This is how a reverse-proxy
// site's proxy.conf becomes editable without a panel control for every
// directive.
func (m *Manager) WriteInclude(ctx context.Context, domain, name string, content []byte) (confedit.Result, error) {
	if err := checkIncludeName(name); err != nil {
		return confedit.Result{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	s, err := m.get(ctx, m.st.DB(), domain)
	if err != nil {
		return confedit.Result{}, err
	}
	return m.ap.Apply(ctx, confedit.Change{
		Files:    []confedit.File{{Path: m.p.SiteInclude(s.Domain, name), Content: content}},
		Services: []confedit.Service{m.svc},
		Reason:   "edit " + name + " of " + s.Domain,
	})
}
