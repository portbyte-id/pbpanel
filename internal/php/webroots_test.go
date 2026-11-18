package php

import (
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/webroots"
)

// rootedPHPPaths is PHP's paths with the given folders on the allowlist.
func rootedPHPPaths(t *testing.T, roots ...string) Paths {
	t.Helper()
	h := webroots.NewHolder()
	h.Set(roots)
	return Paths{Root: "/www", Roots: h}
}

func TestSpecMountsAllowedRootsReadWrite(t *testing.T) {
	spec := Spec(rootedPHPPaths(t, "/www/wwwroot"), "8.3", "img")
	found := false
	for _, m := range spec.Mounts {
		if m.Path == "/www/wwwroot" {
			found = true
			if m.ReadOnly {
				t.Fatal("an allowed web root is read-only in the PHP container; applications cannot write their uploads")
			}
		}
	}
	if !found {
		t.Fatalf("no mount for the allowed root: %+v", spec.Mounts)
	}
	if err := spec.Validate("/www"); err != nil {
		t.Fatalf("the spec with an allowed root does not validate: %v", err)
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
