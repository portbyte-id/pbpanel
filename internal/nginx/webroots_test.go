package nginx

import (
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/webroots"
)

// rootedPaths is nginx's paths with the given folders on the allowlist.
func rootedPaths(t *testing.T, roots ...string) Paths {
	t.Helper()
	h := webroots.NewHolder()
	h.Set(roots)
	return Paths{Root: "/www", Roots: h}
}

func TestServesRootAcceptsSitesTreeAndAllowedFolders(t *testing.T) {
	p := rootedPaths(t, "/www/wwwroot")
	for root, want := range map[string]bool{
		"/www/services/sites/a.example": true,
		"/www/wwwroot/shop":             true,
		"/www/wwwroot":                  false,
		"/www/other/shop":               false,
	} {
		if got := p.ServesRoot(root); got != want {
			t.Errorf("ServesRoot(%q) = %v, want %v", root, got, want)
		}
	}
}

func TestRenderSiteUsesAllowedRoot(t *testing.T) {
	p := rootedPaths(t, "/www/wwwroot")
	if _, err := p.RenderSiteFile(Site{Domain: "shop.example", Kind: KindStatic, Root: "/www/wwwroot/shop"}); err != nil {
		t.Fatalf("a site under an allowed root did not render: %v", err)
	}
	_, err := p.RenderSiteFile(Site{Domain: "x.example", Kind: KindStatic, Root: "/www/other/x"})
	if err == nil || !strings.Contains(err.Error(), "allowed web roots") {
		t.Fatalf("a site outside every allowed root rendered: %v", err)
	}
}
