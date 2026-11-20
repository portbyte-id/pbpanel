package php

import (
	"slices"
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/webroots"
)

// rootedPHPPaths is PHP's paths with the given folders on the allowlist.
func rootedPHPPaths(t *testing.T, roots ...string) Paths {
	t.Helper()
	h := webroots.NewHolder()
	h.Set(roots)
	return Paths{Root: "/www", Roots: h, DataDir: "/www/panel/data"}
}

// An allowed root must be writable by the pool, or an application under it
// cannot write its uploads. Under systemd that is the unit's ReadWritePaths.
func TestUnitTextAllowsWritableRoots(t *testing.T) {
	p := rootedPHPPaths(t, "/www/wwwroot")
	u := UnitText(p, "8.3")
	rw := ""
	for line := range strings.SplitSeq(u, "\n") {
		if v, ok := strings.CutPrefix(line, "ReadWritePaths="); ok {
			rw = v
		}
	}
	if !slices.Contains(strings.Fields(rw), "/www/wwwroot") {
		t.Fatalf("ReadWritePaths lacks the allowed root: %q", rw)
	}
}

func TestRenderPoolUsesAllowedRoot(t *testing.T) {
	p := rootedPHPPaths(t, "/www/wwwroot")
	pool := Pool{Name: "shop", Domain: "shop.example", Version: "8.3", Root: "/www/wwwroot/shop", Settings: DefaultPoolSettings()}
	if _, err := p.RenderPool(pool); err != nil {
		t.Fatalf("a pool under an allowed root did not render: %v", err)
	}
	pool.Root = "/www/other/shop"
	if _, err := p.RenderPool(pool); err == nil || !strings.Contains(err.Error(), "allowed web roots") {
		t.Fatalf("a pool outside every allowed root rendered: %v", err)
	}
}
