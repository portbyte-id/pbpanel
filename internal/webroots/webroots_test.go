package webroots

import (
	"reflect"
	"strings"
	"testing"
)

const root = "/www"

func TestParseAcceptsFoldersInsideTheRoot(t *testing.T) {
	got, err := Parse("/www/wwwroot, /www/public_html\n/www/shared", root)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"/www/wwwroot", "/www/public_html", "/www/shared"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse = %v, want %v", got, want)
	}
}

func TestParseEmptyIsEmptyAllowlist(t *testing.T) {
	for _, v := range []string{"", "  ", " , \n"} {
		got, err := Parse(v, root)
		if err != nil || len(got) != 0 {
			t.Fatalf("Parse(%q) = %v, %v; want empty and no error", v, got, err)
		}
	}
}

func TestParseRefusesFoldersThatMustNotBeMounted(t *testing.T) {
	cases := map[string]string{
		"relative":         "wwwroot",
		"dot dot":          "/www/../etc",
		"trailing slash":   "/www/wwwroot/",
		"outside the root": "/srv/site",
		"the root itself":  "/www",
		"the filesystem":   "/",
		"etc":              "/etc",
		"panel tree":       "/www/panel",
		"inside panel":     "/www/panel/ssl",
		"sites tree":       "/www/services/sites",
		"inside sites":     "/www/services/sites/a.example",
		"sites ancestor":   "/www/services",
		"colon":            "/www/a:b",
		"space":            "/www/my site",
		"semicolon":        "/www/a;b",
		"nul":              "/www/a\x00b",
	}
	for name, p := range cases {
		if _, err := Parse(p, root); err == nil {
			t.Errorf("%s: Parse(%q) accepted it", name, p)
		}
	}
}

func TestParseRefusesNesting(t *testing.T) {
	for _, v := range []string{"/www/a,/www/a", "/www/a,/www/a/b", "/www/a/b,/www/a"} {
		if _, err := Parse(v, root); err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Errorf("Parse(%q) = %v; want an overlap refusal", v, err)
		}
	}
}

func TestParseRejectsBadInstallRoot(t *testing.T) {
	for _, r := range []string{"", "/", "www", "/www/"} {
		if _, err := Parse("/www/a", r); err == nil {
			t.Errorf("Parse with root %q: no error", r)
		}
	}
}

func TestServableNeedsStrictlyInsideAFolder(t *testing.T) {
	sites := "/www/services/sites"
	h := NewHolder()
	h.Set([]string{"/www/wwwroot"})

	cases := []struct {
		root string
		want bool
	}{
		{"/www/services/sites/a.example", true},
		{"/www/wwwroot/site", true},
		{"/www/wwwroot/site/public", true},
		{"/www/wwwroot", false},
		{"/www/wwwrootx/site", false},
		{"/www/services/sites", false},
		{"/www/other/site", false},
		{"/www/wwwroot/../etc", false},
	}
	for _, c := range cases {
		if got := Servable(c.root, sites, h); got != c.want {
			t.Errorf("Servable(%q) = %v, want %v", c.root, got, c.want)
		}
	}
}

func TestHolderIsSafeWhenNilAndCopies(t *testing.T) {
	var nilHolder *Holder
	nilHolder.Set([]string{"/www/a"})
	if got := nilHolder.List(); got != nil {
		t.Fatalf("nil holder List = %v", got)
	}
	if Servable("/www/a/site", "/www/services/sites", nilHolder) {
		t.Fatal("a nil holder allowed a folder")
	}

	h := NewHolder()
	in := []string{"/www/a"}
	h.Set(in)
	in[0] = "/www/changed"
	if got := h.List(); !reflect.DeepEqual(got, []string{"/www/a"}) {
		t.Fatalf("holder kept the caller's slice: %v", got)
	}
}
