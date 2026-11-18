// Package webroots is the allowlist of folders outside the sites tree that a site
// may use as its web root. The nginx and PHP containers mount only the sites tree
// by default; an allowed folder is mounted into them as well, at the same path.
//
// The list is one value in the settings registry. Parse turns it into folders and
// is the only check on what may be mounted, so it is strict: every folder is an
// absolute clean path strictly inside the install root, outside the panel's own
// tree and outside the sites tree (which is mounted already).
package webroots

import (
	"fmt"
	"path"
	"strings"
	"sync"
)

// unsafe are the bytes a web root may not carry. The nginx config and the PHP pool
// file are written from the path, and a bind mount cannot carry a colon.
const unsafe = " \t\r\n;{}$\"'\\#[]=:\x00"

// Holder is the current allowlist. The nginx and PHP paths share one holder, so
// a change on the Settings page is seen by the next spec and render without
// rebuilding the services. A nil *Holder allows nothing.
type Holder struct {
	mu    sync.RWMutex
	roots []string
}

// NewHolder returns an empty allowlist.
func NewHolder() *Holder { return &Holder{} }

// Set replaces the allowlist. The caller has parsed it with Parse.
func (h *Holder) Set(roots []string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.roots = append([]string(nil), roots...)
}

// List returns the allowed folders in the order they were set.
func (h *Holder) List() []string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]string(nil), h.roots...)
}

// Parse reads the value of the setting: folders separated by commas or line
// breaks. root is the install root (`/www` in production). Each folder must be an
// absolute clean path strictly inside root, outside root/panel and outside
// root/services/sites, and no folder may sit inside another. An empty value is the
// empty allowlist.
func Parse(value, root string) ([]string, error) {
	if !path.IsAbs(root) || path.Clean(root) != root || root == "/" {
		return nil, fmt.Errorf("webroots: install root %q must be a clean absolute path below /", root)
	}
	panelDir := path.Join(root, "panel")
	sitesDir := path.Join(root, "services", "sites")

	var out []string
	for _, raw := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		switch {
		case !path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, unsafe):
			return nil, fmt.Errorf("%q must be a clean absolute folder path without spaces or special characters, for example %s/wwwroot", p, root)
		case !within(p, root):
			return nil, fmt.Errorf("%q must be inside %s", p, root)
		case p == panelDir || within(p, panelDir):
			return nil, fmt.Errorf("%q is the panel's own tree and cannot be a web root", p)
		case p == sitesDir || within(p, sitesDir) || within(sitesDir, p):
			return nil, fmt.Errorf("%q overlaps %s, which is already available to sites", p, sitesDir)
		}
		for _, other := range out {
			if p == other || within(p, other) || within(other, p) {
				return nil, fmt.Errorf("%q overlaps %q: list each folder once and never inside another", p, other)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// Servable reports whether a web root is one a site may use: strictly inside the
// sites tree, or strictly inside a folder on the allowlist.
func Servable(root, sitesRoot string, h *Holder) bool {
	if within(root, sitesRoot) {
		return true
	}
	for _, r := range h.List() {
		if within(root, r) {
			return true
		}
	}
	return false
}

// within reports whether p is strictly below dir.
func within(p, dir string) bool {
	return strings.HasPrefix(p, dir+"/") && path.Clean(p) == p
}
