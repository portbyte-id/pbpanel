package sites

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/portbyte-id/server-panel/internal/confedit"
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
