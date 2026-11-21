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
	b, err = testPaths.RenderMainInclude(DefaultHTTPSettings())
	golden(t, "panel-main.conf", mustRender(t, b, err))
	b, err = testPaths.RenderHTTPInclude(DefaultHTTPSettings())
	golden(t, "panel-http.conf", mustRender(t, b, err))
	b, err = testPaths.RenderSiteFile(static)
	golden(t, "site-static.conf", mustRender(t, b, err))
	b, err = testPaths.RenderSiteFile(proxy)
	golden(t, "site-proxy.conf", mustRender(t, b, err))
	b, err = RenderListen()
	golden(t, "listen.conf", mustRender(t, b, err))
	b, err = RenderNames("a.test", []string{"www.a.test", "xn--bcher-kva.test"})
	golden(t, "names.conf", mustRender(t, b, err))
	b, err = testPaths.RenderACME()
	golden(t, "acme.conf", mustRender(t, b, err))
	b, err = testPaths.RenderTLS("a.test", nil)
	golden(t, "tls-none.conf", mustRender(t, b, err))
	tlsA := &TLS{CertFile: "/www/panel/ssl/sites/a.test/fullchain.pem", KeyFile: "/www/panel/ssl/sites/a.test/privkey.pem"}
	b, err = testPaths.RenderTLS("a.test", tlsA)
	golden(t, "tls.conf", mustRender(t, b, err))
	redirect := *tlsA
	redirect.RedirectHTTP = true
	b, err = testPaths.RenderTLS("a.test", &redirect)
	golden(t, "tls-redirect.conf", mustRender(t, b, err))
	b, err = RenderProxy("http://127.0.0.1:3000")
	golden(t, "proxy.conf", mustRender(t, b, err))

	phpSite := Site{Domain: "c.test", Kind: KindPHP, Root: "/www/services/sites/c.test"}
	b, err = testPaths.RenderSiteFile(phpSite)
	golden(t, "site-php.conf", mustRender(t, b, err))
	b, err = testPaths.RenderPHP("/www/panel/run/php-8.3-c_test.sock", "")
	golden(t, "php.conf", mustRender(t, b, err))
}

func TestRenderPHPRefusesASocketOutsideTheRunDir(t *testing.T) {
	for _, socket := range []string{"", "/tmp/x.sock", "/www/panel/run/../x.sock", "/www/panel/run/a.sock; return 200", "127.0.0.1:9000"} {
		if _, err := testPaths.RenderPHP(socket, ""); err == nil {
			t.Errorf("php.conf was rendered for socket %q", socket)
		}
	}
}

// The rewrite presets change only the try_files fallback's query string.
func TestRenderPHPUsesTheRewritePreset(t *testing.T) {
	const sock = "/www/panel/run/php-8.3.sock"
	for _, c := range []struct{ front, want string }{
		{"", "/index.php?$query_string"},
		{"/index.php?$query_string", "/index.php?$query_string"},
		{"/index.php?$args", "/index.php?$args"},
	} {
		b, err := testPaths.RenderPHP(sock, c.front)
		if err != nil {
			t.Fatalf("front %q: %v", c.front, err)
		}
		if !strings.Contains(string(b), "try_files $uri $uri/ "+c.want+";") {
			t.Errorf("front %q did not render %q:\n%s", c.front, c.want, b)
		}
	}
	// Anything that is not a known preset is refused.
	if _, err := testPaths.RenderPHP(sock, "/etc/passwd"); err == nil {
		t.Error("an unknown front-controller value was accepted")
	}
}

func TestSettingsRenderEveryField(t *testing.T) {	s := HTTPSettings{
		WorkerProcesses: "4", ClientMaxBodySize: "0", Gzip: false,
		KeepaliveTimeout: 10, ClientBodyTimeout: 11, ClientHeaderTimeout: 12, SendTimeout: 13, ServerTokens: true,
	}
	main, err := testPaths.RenderMainInclude(s)
	if err != nil {
		t.Fatal(err)
	}
	http, err := testPaths.RenderHTTPInclude(s)
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
		if _, err := testPaths.RenderHTTPInclude(s); err == nil {
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

// A native nginx has no /etc/nginx: every file the generated configuration
// includes comes from the build or from the panel's own tree.
func TestRenderedConfigNamesNoContainerPaths(t *testing.T) {
	main, err := testPaths.RenderMainConf()
	if err != nil {
		t.Fatal(err)
	}
	php, err := testPaths.RenderPHP("/www/panel/run/php-8.3.sock", "")
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{"nginx.conf": string(main), "php.conf": string(php)} {
		if strings.Contains(got, "/etc/nginx") || strings.Contains(got, "/dev/stderr") {
			t.Errorf("%s names a path that only existed in the container image:\n%s", name, got)
		}
	}
	for _, want := range []string{
		"include " + testPaths.MimeTypes() + ";",
		"error_log " + testPaths.MainErrorLog() + " warn;",
	} {
		if !strings.Contains(string(main), want) {
			t.Errorf("nginx.conf lacks %q", want)
		}
	}
	if !strings.Contains(string(php), "include "+testPaths.FastCGIParams()+";") {
		t.Errorf("php.conf does not include the build's fastcgi_params:\n%s", php)
	}
}

// A `reuseport` listen option may appear on only one listen per address:port in
// the whole configuration. Each site's TLS block is its own file, so emitting it
// here made the second certificate-bearing site fail nginx -t with "duplicate
// listen options". The rendered block must therefore never carry it.
func TestTLSNeverUsesReuseport(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		b, err := testPaths.RenderTLS("a.test", &TLS{
			CertFile:     "/www/panel/ssl/sites/a.test/fullchain.pem",
			KeyFile:      "/www/panel/ssl/sites/a.test/privkey.pem",
			RedirectHTTP: redirect,
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "reuseport") {
			t.Errorf("tls.conf (redirect=%v) uses reuseport; a second TLS site would fail nginx -t:\n%s", redirect, b)
		}
	}
}

func TestRenderTLSRefusesFilesOutsideTheSiteCertDir(t *testing.T) {
	ok := "/www/panel/ssl/sites/a.test/"
	for _, c := range []TLS{
		{CertFile: "/www/panel/ssl/panel.crt", KeyFile: ok + "privkey.pem"},
		{CertFile: ok + "fullchain.pem", KeyFile: "/www/panel/ssl/panel.key"},
		{CertFile: ok + "../b.test/fullchain.pem", KeyFile: ok + "privkey.pem"},
		{CertFile: "/www/panel/ssl/sites/a.test.evil/fullchain.pem", KeyFile: ok + "privkey.pem"},
		{CertFile: ok + "x.pem; include /etc/passwd", KeyFile: ok + "privkey.pem"},
		{CertFile: "", KeyFile: ok + "privkey.pem"},
	} {
		if _, err := testPaths.RenderTLS("a.test", &c); err == nil {
			t.Errorf("tls.conf was rendered for %+v", c)
		}
	}
}

// The redirect is the one place a mistake locks certificate issuance out, so
// the ACME path is checked as text against the order nginx evaluates it in:
// the exemption must come after the flag is set and before it is acted on.
func TestTLSRedirectExemptsTheChallengePath(t *testing.T) {
	b, err := testPaths.RenderTLS("a.test", &TLS{
		CertFile: "/www/panel/ssl/sites/a.test/fullchain.pem", KeyFile: "/www/panel/ssl/sites/a.test/privkey.pem", RedirectHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	set := strings.Index(got, "if ($scheme = http)")
	exempt := strings.Index(got, `if ($uri ~ ^/\.well-known/acme-challenge/)`)
	ret := strings.Index(got, "return 301 https://$host$request_uri;")
	if set < 0 || exempt < 0 || ret < 0 || set >= exempt || exempt >= ret {
		t.Errorf("redirect order wrong (scheme=%d exempt=%d return=%d):\n%s", set, exempt, ret, got)
	}
	plain, _ := testPaths.RenderTLS("a.test", &TLS{
		CertFile: "/www/panel/ssl/sites/a.test/fullchain.pem", KeyFile: "/www/panel/ssl/sites/a.test/privkey.pem",
	})
	if strings.Contains(string(plain), "return 301") {
		t.Errorf("tls.conf redirects without being asked:\n%s", plain)
	}
}

func TestACMEServesTheWebrootWithTheFullURI(t *testing.T) {
	b, err := testPaths.RenderACME()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"location ^~ /.well-known/acme-challenge/ {", "root /www/panel/ssl/acme-webroot;", "try_files $uri =404;"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("acme.conf lacks %q:\n%s", want, b)
		}
	}
}

func TestReplaceBlock(t *testing.T) {
	content := "# header" + NL + "server {" + NL +
		"    # >>> pb:names" + NL + "    server_name old.test;" + NL + "    # <<< pb:names" + NL +
		"    # >>> pb:tls" + NL + "    # <<< pb:tls" + NL +
		"" + NL + "\troot /x;   " + NL + "}" + NL + "# footer, no newline"

	if !HasBlock([]byte(content), BlockNames) || !HasBlock([]byte(content), BlockTLS) {
		t.Fatal("HasBlock misses a block that is there")
	}
	for _, name := range []string{BlockPHP, BlockACME, "nope"} {
		if HasBlock([]byte(content), name) {
			t.Errorf("HasBlock(%q) = true for a block that is not there", name)
		}
		got, ok := ReplaceBlock([]byte(content), name, []byte("x;"+NL))
		if ok || string(got) != content {
			t.Errorf("ReplaceBlock(%q) = %v, changed=%v; want false and the content untouched", name, ok, string(got) != content)
		}
	}

	// The body is indented like the begin marker, blank lines stay empty, and the
	// leading "# Managed by PB panel" comment is dropped.
	body := "# Managed by PB panel for a.test." + NL + "server_name a.test www.a.test;" + NL + NL + "location / {" + NL + "    return 204;" + NL + "}" + NL
	got, ok := ReplaceBlock([]byte(content), BlockNames, []byte(body))
	if !ok {
		t.Fatal("ReplaceBlock did not find the names block")
	}
	want := strings.Replace(content, "    server_name old.test;", "    server_name a.test www.a.test;"+NL+NL+"    location / {"+NL+"        return 204;"+NL+"    }", 1)
	if string(got) != want {
		t.Errorf("ReplaceBlock:%s%s%swant:%s%s", NL, got, NL, NL, want)
	}
	if strings.Contains(string(got), "Managed by PB panel for a.test") || strings.Contains(string(got), "old.test") {
		t.Error("the managed comment survived or the old body was kept")
	}

	// Filling an empty block, then emptying it again, leaves everything outside
	// the markers byte for byte as it was (trailing spaces, tab, no final newline).
	filled, ok := ReplaceBlock([]byte(content), BlockTLS, []byte("listen 443 ssl;"+NL))
	if !ok || !strings.Contains(string(filled), "    # >>> pb:tls"+NL+"    listen 443 ssl;"+NL+"    # <<< pb:tls"+NL) {
		t.Fatalf("filled = %q", filled)
	}
	// A comment-only body (what an empty TLS block renders as) leaves the block empty.
	emptied, _ := ReplaceBlock(filled, BlockTLS, []byte("# Managed by PB panel"+NL))
	if !strings.Contains(string(emptied), "    # >>> pb:tls"+NL+"    # <<< pb:tls"+NL) {
		t.Errorf("a comment-only body did not leave the block empty: %q", emptied)
	}
	outside := func(s string) string {
		var out []string
		in := false
		for _, l := range strings.Split(s, NL) {
			tl := strings.TrimSpace(l)
			switch {
			case tl == "# >>> pb:tls":
				in = true
			case tl == "# <<< pb:tls":
				in = false
			case !in:
				out = append(out, l)
			}
		}
		return strings.Join(out, NL)
	}
	if outside(string(filled)) != outside(content) || outside(string(emptied)) != outside(content) {
		t.Errorf("text outside the tls block changed:%s%s", NL, filled)
	}

	// A begin marker without its end, or an end before the begin, is not a block.
	for name, c := range map[string]string{
		"no end":       "    # >>> pb:names" + NL + "x" + NL,
		"end first":    "    # <<< pb:names" + NL + "    # >>> pb:names" + NL,
		"only the end": "    # <<< pb:names" + NL,
	} {
		if HasBlock([]byte(c), BlockNames) {
			t.Errorf("%s: HasBlock = true", name)
		}
		if _, ok := ReplaceBlock([]byte(c), BlockNames, []byte("y;")); ok {
			t.Errorf("%s: ReplaceBlock reported a block", name)
		}
	}
}

func TestRenderSiteFileHasEveryBlockEmpty(t *testing.T) {
	p := Paths{Root: "/www"}
	for _, s := range []Site{
		{Domain: "a.test", Kind: KindStatic, Root: "/www/services/sites/a.test"},
		{Domain: "a.test", Kind: KindPHP, Root: "/www/services/sites/a.test"},
	} {
		b, err := p.RenderSiteFile(s)
		if err != nil {
			t.Fatalf("%v: %v", s.Kind, err)
		}
		names := []string{BlockListen, BlockNames, BlockACME, BlockTLS}
		if s.Kind == KindPHP {
			names = append(names, BlockPHP)
		}
		for _, n := range names {
			if !HasBlock(b, n) {
				t.Errorf("%v site file lacks the %s block:%s%s", s.Kind, n, NL, b)
			}
		}
	}
}

// NL keeps the multi-line fixtures above readable.
const NL = "\n"

func TestInsertBlock(t *testing.T) {
	// A site made after blocks existed already has the markers: nothing changes.
	fresh, err := testPaths.RenderSiteFile(Site{Domain: "a.test", Kind: KindStatic, Root: "/www/services/sites/a.test"})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := InsertBlock(fresh, BlockWAF); !ok || string(got) != string(fresh) {
		t.Error("a file with the block must come back untouched")
	}

	// A site from before the firewall block: it goes after the TLS block, indented
	// like it, and the rest of the file is byte for byte what it was.
	old := strings.Replace(string(fresh), "    # >>> pb:waf\n    # <<< pb:waf\n", "", 1)
	if strings.Contains(old, "pb:waf") {
		t.Fatal("test setup: the waf block is still there")
	}
	got, ok := InsertBlock([]byte(old), BlockWAF)
	if !ok || !HasBlock(got, BlockWAF) {
		t.Fatalf("no block inserted:\n%s", got)
	}
	if string(got) != string(fresh) {
		t.Errorf("inserting the block did not give back the fresh file.\n--- want\n%s\n--- got\n%s", fresh, got)
	}

	// An operator's file with none of the panel's blocks: after `server {`.
	bare := "server {\n    listen 80;\n}\n"
	got, ok = InsertBlock([]byte(bare), BlockWAF)
	if !ok || !strings.HasPrefix(string(got), "server {\n    # >>> pb:waf\n    # <<< pb:waf\n    listen 80;") {
		t.Errorf("bare server block: ok=%v\n%s", ok, got)
	}

	// Nothing to attach to.
	if _, ok := InsertBlock([]byte("# nothing\n"), BlockWAF); ok {
		t.Error("a file with no server block cannot take the block")
	}
}
