package nginx

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/<name>. With -update it writes the file
// instead, so a deliberate template change is one command and one reviewed diff.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	file := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs from the golden file.\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

var testPaths = Paths{Root: "/www"}

func mustRender(t *testing.T, b []byte, err error) []byte {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGoldenFiles(t *testing.T) {
	static := Site{Domain: "a.test", Kind: KindStatic, Root: "/www/services/sites/a.test"}
	proxy := Site{Domain: "b.test", Kind: KindProxy, ProxyTarget: "http://127.0.0.1:3000"}

	b, err := testPaths.RenderMainConf()
	golden(t, "nginx.conf", mustRender(t, b, err))
	b, err = RenderMainInclude(DefaultHTTPSettings())
	golden(t, "panel-main.conf", mustRender(t, b, err))
	b, err = RenderHTTPInclude(DefaultHTTPSettings())
	golden(t, "panel-http.conf", mustRender(t, b, err))
	b, err = testPaths.RenderSiteFile(static)
	golden(t, "site-static.conf", mustRender(t, b, err))
	b, err = testPaths.RenderSiteFile(proxy)
	golden(t, "site-proxy.conf", mustRender(t, b, err))
	b, err = RenderListen()
	golden(t, "listen.conf", mustRender(t, b, err))
	b, err = RenderNames("a.test", []string{"www.a.test", "xn--bcher-kva.test"})
	golden(t, "names.conf", mustRender(t, b, err))
	b, err = RenderACME()
	golden(t, "acme.conf", mustRender(t, b, err))
	b, err = RenderProxy("http://127.0.0.1:3000")
	golden(t, "proxy.conf", mustRender(t, b, err))

	phpSite := Site{Domain: "c.test", Kind: KindPHP, Root: "/www/services/sites/c.test"}
	b, err = testPaths.RenderSiteFile(phpSite)
	golden(t, "site-php.conf", mustRender(t, b, err))
	b, err = testPaths.RenderPHP("/www/panel/run/php-8.3-c_test.sock")
	golden(t, "php.conf", mustRender(t, b, err))
}

func TestRenderPHPRefusesASocketOutsideTheRunDir(t *testing.T) {
	for _, socket := range []string{"", "/tmp/x.sock", "/www/panel/run/../x.sock", "/www/panel/run/a.sock; return 200", "127.0.0.1:9000"} {
		if _, err := testPaths.RenderPHP(socket); err == nil {
			t.Errorf("php.conf was rendered for socket %q", socket)
		}
	}
}

func TestSettingsRenderEveryField(t *testing.T) {
	s := HTTPSettings{
		WorkerProcesses: "4", ClientMaxBodySize: "0", Gzip: false,
		KeepaliveTimeout: 10, ClientBodyTimeout: 11, ClientHeaderTimeout: 12, SendTimeout: 13, ServerTokens: true,
	}
	main, err := RenderMainInclude(s)
	if err != nil {
		t.Fatal(err)
	}
	http, err := RenderHTTPInclude(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(main), "worker_processes 4;") {
		t.Errorf("panel-main.conf lacks worker_processes 4:\n%s", main)
	}
	for _, want := range []string{
		"client_max_body_size 0;", "client_body_timeout 11s;", "client_header_timeout 12s;",
		"keepalive_timeout 10s;", "send_timeout 13s;", "server_tokens on;", "gzip off;",
	} {
		if !strings.Contains(string(http), want) {
			t.Errorf("panel-http.conf lacks %q:\n%s", want, http)
		}
	}
}

func TestMainConfSetsNothingTheFormOwns(t *testing.T) {
	b, err := testPaths.RenderMainConf()
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		for _, owned := range []string{"worker_processes", "client_max_body_size", "gzip", "keepalive_timeout", "send_timeout", "client_body_timeout", "client_header_timeout", "server_tokens"} {
			if strings.HasPrefix(line, owned+" ") {
				t.Errorf("nginx.conf sets %s, which the settings form owns: %q", owned, line)
			}
		}
	}
	for _, link := range []string{testPaths.MainIncludePath(), testPaths.HTTPIncludePath(), "/www/panel/nginx/sites/*.conf"} {
		if !HasIncludeLine(b, link) {
			t.Errorf("nginx.conf does not include %s", link)
		}
	}
}

func TestSettingsValidation(t *testing.T) {
	ok := DefaultHTTPSettings()
	if err := ok.Validate(); err != nil {
		t.Fatalf("defaults are invalid: %v", err)
	}
	cases := map[string]func(*HTTPSettings){
		"workers zero":          func(s *HTTPSettings) { s.WorkerProcesses = "0" },
		"workers injection":     func(s *HTTPSettings) { s.WorkerProcesses = "auto; user root" },
		"workers padded":        func(s *HTTPSettings) { s.WorkerProcesses = "04" },
		"body size injection":   func(s *HTTPSettings) { s.ClientMaxBodySize = "100m; include /etc/passwd" },
		"body size fraction":    func(s *HTTPSettings) { s.ClientMaxBodySize = "1.5m" },
		"body size empty":       func(s *HTTPSettings) { s.ClientMaxBodySize = "" },
		"keepalive zero":        func(s *HTTPSettings) { s.KeepaliveTimeout = 0 },
		"send timeout too high": func(s *HTTPSettings) { s.SendTimeout = 3601 },
		"body timeout negative": func(s *HTTPSettings) { s.ClientBodyTimeout = -1 },
	}
	for name, mutate := range cases {
		s := DefaultHTTPSettings()
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: %+v was accepted", name, s)
		}
		if _, err := RenderHTTPInclude(s); err == nil {
			t.Errorf("%s: the renderer wrote a config from invalid settings", name)
		}
	}
}

func TestCheckProxyTarget(t *testing.T) {
	good := map[string]string{
		"http://127.0.0.1:3000":  "http://127.0.0.1:3000",
		" https://app.test ":     "https://app.test",
		"http://[::1]:8080/":     "http://[::1]:8080",
		"http://localhost":       "http://localhost",
		"https://Example.test:1": "https://Example.test:1",
	}
	for in, want := range good {
		got, err := CheckProxyTarget(in)
		if err != nil || got != want {
			t.Errorf("CheckProxyTarget(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "127.0.0.1:3000", "ftp://a.test", "http://", "http://a.test/app", "http://a.test?x=1",
		"http://u:p@a.test", "http://a.test:0", "http://a.test:70000", "http://a.test;return 200",
		"http://a.test\nlocation / {}", "http://a.test/$uri", "unix:/run/app.sock",
	} {
		if got, err := CheckProxyTarget(in); err == nil {
			t.Errorf("CheckProxyTarget(%q) = %q, want an error", in, got)
		}
	}
}

func TestRenderRefusesInjection(t *testing.T) {
	if _, err := RenderNames("a.test", []string{"b.test; return 200"}); err == nil {
		t.Error("an alias with a semicolon was rendered")
	}
	if _, err := RenderNames("a.test", []string{"b.test\nserver {"}); err == nil {
		t.Error("an alias with a newline was rendered")
	}
	for _, s := range []Site{
		{Domain: "a.test", Kind: KindStatic, Root: "/www/services/sites/a.test; root /"},
		{Domain: "a.test", Kind: KindStatic, Root: "/etc"},
		{Domain: "a.test", Kind: KindStatic, Root: "/www/services/sites/../../../etc"},
		{Domain: "a.test", Kind: KindStatic, Root: "relative"},
		{Domain: "a b", Kind: KindProxy},
		{Domain: "a.test", Kind: "php"},
	} {
		if _, err := testPaths.RenderSiteFile(s); err == nil {
			t.Errorf("site %+v was rendered", s)
		}
	}
}

func TestHasIncludeLine(t *testing.T) {
	content := []byte("server {\n    include /x/listen.conf;\n    # include /x/names.conf;\n\tinclude /x/acme.conf;  \n  include /x/proxy.conf; # trailing\n}\n")
	for file, want := range map[string]bool{
		"/x/listen.conf": true,  // indented
		"/x/names.conf":  false, // commented out
		"/x/acme.conf":   true,  // tab and trailing space
		"/x/proxy.conf":  false, // trailing text: not the literal line
		"/x/other.conf":  false,
	} {
		if got := HasIncludeLine(content, file); got != want {
			t.Errorf("HasIncludeLine(%s) = %v, want %v", file, got, want)
		}
	}
}

func TestSpecFollowsTheMountRule(t *testing.T) {
	spec := Spec(testPaths)
	if err := spec.Validate(testPaths.Root); err != nil {
		t.Fatalf("the nginx spec breaks the mount rule: %v", err)
	}
	if spec.Hash() != Spec(testPaths).Hash() {
		t.Error("the spec hash is not stable")
	}
	if cfg := spec.Config(); cfg.NetworkMode != "host" {
		t.Errorf("network mode = %q, want host", cfg.NetworkMode)
	}
}

func TestDockerfileAddsWWWAtTheFixedID(t *testing.T) {
	d := string(Dockerfile())
	for _, want := range []string{"FROM " + BaseImage, "groupadd -g 1500 www", "useradd -u 1500 -g www"} {
		if !strings.Contains(d, want) {
			t.Errorf("Dockerfile lacks %q:\n%s", want, d)
		}
	}
}
