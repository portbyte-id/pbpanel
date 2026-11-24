package waf

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/nginx"
)

// globalTestConfig is a switched-on global firewall with every rule and the block
// page, written where nginx can read it, with a configuration that includes it.
// It returns the nginx binary path, the configuration file and its text.
func globalTestConfig(t *testing.T, port int) (bin, confPath, conf string) {
	t.Helper()
	bin, err := exec.LookPath("nginx")
	if err != nil {
		t.Skip("nginx is not installed")
	}

	dir := t.TempDir()
	p := nginx.Paths{Root: dir}
	g := DefaultGlobal()
	g.BotBlock.Enabled = true
	g.BotBlock.EmptyAgent = true
	g.BotBlock.AICrawlers = true
	g.BotBlock.Custom = []string{"evil.bot+x"}
	g.SensitiveFiles = true
	g.BlockTrace = true
	g.IPDeny = []string{"192.0.2.0/24", "198.51.100.7/32"}
	g.ErrorPage.Enabled = true
	g, err = g.Normalize()
	if err != nil {
		t.Fatal(err)
	}

	write := func(path string, content []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(p.WAFGlobalHTTP(), GlobalHTTPInclude(g))
	write(p.WAFErrorPage(), []byte(g.ErrorPage.HTML))

	block := blockBody(p, "a.test", Default(), "", g)
	conf = fmt.Sprintf(`error_log %[1]s/error.log;
pid %[1]s/nginx.pid;
events {}
http {
    access_log off;
    include %[2]s;
    server {
        listen 127.0.0.1:%[4]d;
        server_name a.test;
        root %[1]s;
%[3]s
        location / { return 200; }
    }
}
`, dir, p.WAFGlobalHTTP(), indent(string(block)), port)
	confPath = filepath.Join(dir, "nginx.conf")
	write(confPath, []byte(conf))
	return bin, confPath, conf
}

// TestGlobalFirewallParsesInNginx renders every global rule into a real
// configuration and asks nginx to check it: the variables, the maps and the
// block page are configuration nginx accepts, not just text that looks right.
func TestGlobalFirewallParsesInNginx(t *testing.T) {
	bin, confPath, conf := globalTestConfig(t, 18080)
	dir := filepath.Dir(confPath)
	out, err := exec.Command(bin, "-t", "-p", dir+"/", "-c", confPath).CombinedOutput()
	if err != nil {
		t.Fatalf("nginx -t rejected the global firewall:\n%s\n--- config ---\n%s", out, conf)
	}
	if !strings.Contains(string(out), "successful") {
		t.Fatalf("nginx -t did not report success:\n%s", out)
	}
}

// TestGlobalFirewallBlocksInNginx runs nginx with the global firewall and checks
// what a client sees: a refused request gets the block page with its status and
// the request id filled in, a request that is not refused gets through, and the
// page does not loop back into the rules.
func TestGlobalFirewallBlocksInNginx(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	bin, confPath, _ := globalTestConfig(t, port)
	cmd := exec.Command(bin, "-p", filepath.Dir(confPath)+"/", "-c", confPath, "-g", "daemon off;")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	}()
	waitListening(t, port)

	get := func(method, path, agent string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("User-Agent", agent)
		resp, err := (&http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	cases := []struct {
		name, method, path, agent string
		wantStatus                int
		wantPage                  bool
	}{
		{"scanner agent", http.MethodGet, "/", "sqlmap/1.0", http.StatusForbidden, true},
		{"AI crawler", http.MethodGet, "/", "Mozilla/5.0 GPTBot/1.1", http.StatusForbidden, true},
		{"custom agent", http.MethodGet, "/", "evil.bot+x/2", http.StatusForbidden, true},
		{"dotfile", http.MethodGet, "/.env", "Mozilla/5.0", http.StatusForbidden, true},
		{"TRACE method", http.MethodTrace, "/", "Mozilla/5.0", 0, false},
		{"ordinary browser", http.MethodGet, "/", "Mozilla/5.0", http.StatusOK, false},
		{"certificate challenge", http.MethodGet, "/.well-known/acme-challenge/token", "sqlmap/1.0", http.StatusOK, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := get(c.method, c.path, c.agent)
			if (c.wantStatus == 0 && status < 400) || (c.wantStatus != 0 && status != c.wantStatus) {
				t.Fatalf("status %d, want %d; body: %.200s", status, c.wantStatus, body)
			}
			if c.wantPage {
				if !strings.Contains(body, "Request blocked") {
					t.Fatalf("block page missing: %.300s", body)
				}
				if strings.Contains(body, "<!--#") {
					t.Fatalf("server-side include not processed: %.300s", body)
				}
			}
		})
	}
}

func waitListening(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("nginx did not listen on %d", port)
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "        " + l
	}
	return strings.Join(lines, "\n")
}
