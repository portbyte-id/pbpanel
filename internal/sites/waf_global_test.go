package sites

import (
	"strings"
	"testing"

	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/waf"
)

// TestSetWAFGlobalReachesEverySite checks the global firewall is written into
// each site's block and the shared files in one apply, and that turning it off
// takes the rules back out of every site.
func TestSetWAFGlobalReachesEverySite(t *testing.T) {
	r := wafRig(t, true)
	for _, d := range []string{"a.test", "b.test"} {
		if _, err := r.m.Create(ctx, CreateInput{Domain: d, Kind: KindStatic}); err != nil {
			t.Fatal(err)
		}
	}

	g := waf.DefaultGlobal()
	g.BlockTrace = true
	g.ErrorPage.Enabled = true
	res, err := r.m.SetWAFGlobal(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Global.BlockTrace || !res.Global.ErrorPage.Enabled {
		t.Fatalf("result = %+v", res.Global)
	}

	for _, d := range []string{"a.test", "b.test"} {
		body := blockOf(t, string(r.fs.files["/www/panel/nginx/sites/"+d+".conf"]), nginx.BlockWAF)
		for _, want := range []string{"if ($pbwaf_g_trace) { return 403; }", "error_page 403 =403 /pb-waf-blocked;"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: block lacks %q:\n%s", d, want, body)
			}
		}
	}
	if !strings.Contains(string(r.fs.files["/www/panel/nginx/waf/http/_global.conf"]), "pbwaf_g_trace") {
		t.Error("the global include does not define the TRACE variable")
	}
	if got := string(r.fs.files["/www/panel/nginx/waf/errors/blocked.html"]); got != waf.DefaultErrorPageHTML {
		t.Errorf("the block page on disk is not the default:\n%s", got)
	}

	if _, err := r.m.SetWAFGlobal(ctx, waf.DefaultGlobal()); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"a.test", "b.test"} {
		body := blockOf(t, string(r.fs.files["/www/panel/nginx/sites/"+d+".conf"]), nginx.BlockWAF)
		if strings.Contains(body, "pbwaf_g_") || strings.Contains(body, "error_page") {
			t.Errorf("%s: the switched-off global rules are still in the block:\n%s", d, body)
		}
	}
	if r.fs.has("/www/panel/nginx/waf/errors/blocked.html") {
		t.Error("the block page is still on disk after it was switched off")
	}
	if got, err := r.m.WAFGlobal(ctx); err != nil || got.BlockTrace || got.ErrorPage.Enabled {
		t.Errorf("stored global = %+v, %v", got, err)
	}
}
