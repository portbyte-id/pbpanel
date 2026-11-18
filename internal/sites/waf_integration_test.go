//go:build integration && linux

package sites

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/confedit"
	"github.com/portbyte-id/server-panel/internal/nginx"
	"github.com/portbyte-id/server-panel/internal/waf"
	"github.com/portbyte-id/server-panel/internal/wafmon"
)

// wafGet requests a path from the site through nginx on the host's port 80.
func wafGet(t *testing.T, host, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
	req.Host = host
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

const xss = "/?q=%3Cscript%3Ealert(1)%3C%2Fscript%3E"

// TestRealWAFEndToEnd drives the firewall against the real post-quantum nginx
// image (ModSecurity and the Core Rule Set inside it): every change goes
// through confedit and `nginx -t`, and the effect is read off real responses
// and the real audit log. It needs port 80 free on the host and a Docker
// Engine, and the first run builds the image.
func TestRealWAFEndToEnd(t *testing.T) {
	eng := requireNativeHost(t)
	bg := context.Background()
	h := newNativeHost(t, eng, "panel-waf-it-", nativeLayout{})
	// ModSecurity writes the audit log from a worker running as www, which owns
	// nothing here: the daemon would have chowned the file, so the directory is open.
	if err := os.Chmod(filepath.Join(h.root, "panel/logs/sites"), 0o777); err != nil {
		t.Fatal(err)
	}

	paths := h.paths
	global := h.global
	// Every native build ships ModSecurity, so the WAF is available once a build
	// serves, which the reconcile below makes happen.
	engine := waf.New(h.st, paths, func(context.Context) (waf.Availability, error) {
		return waf.Availability{Available: true, Flavor: "pqc", CRSVersion: nginx.CRSVersion}, nil
	})
	mgr := h.manager(t, func(o *Options) {
		o.WAF = engine
		o.Settings = func(ctx context.Context) ([]confedit.File, error) {
			s, err := global.Settings(ctx)
			if err != nil {
				return nil, err
			}
			return global.SettingsFiles(s)
		}
	})
	rec := h.reconciler(t, mgr)
	if s := rec.Run(bg); !s.OK {
		t.Fatalf("first reconcile: %+v", s)
	}
	if _, err := mgr.Create(bg, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatalf("create: %v", err)
	}
	waitFor(t, "the site to be served", func() bool { c, _ := wafGet(t, "a.test", "/"); return c == 200 })

	set := func(mutate func(*waf.Config)) {
		t.Helper()
		c := waf.Default()
		mutate(&c)
		if _, err := mgr.SetWAF(bg, "a.test", c); err != nil {
			t.Fatalf("SetWAF(%+v): %v", c, err)
		}
		_ = os.Chmod(paths.WAFAuditLog("a.test"), 0o666)
		// The reload is asynchronous for the workers; give them a moment.
		time.Sleep(600 * time.Millisecond)
	}
	status := func(path string) int { c, _ := wafGet(t, "a.test", path); return c }

	t.Run("detection only logs and lets the request through", func(t *testing.T) {
		set(func(c *waf.Config) { c.Mode = waf.ModeDetect })
		if got := status(xss); got != 200 {
			t.Fatalf("detection mode answered %d, want 200", got)
		}
		waitFor(t, "an audit event", func() bool {
			b, _ := os.ReadFile(paths.WAFAuditLog("a.test"))
			return strings.Contains(string(b), `"unique_id"`)
		})
		b, _ := os.ReadFile(paths.WAFAuditLog("a.test"))
		line := strings.Split(strings.TrimSpace(string(b)), "\n")[0]
		var ev struct {
			Transaction struct {
				UniqueID string            `json:"unique_id"`
				Messages []json.RawMessage `json:"messages"`
			} `json:"transaction"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("the audit log line is not JSON: %v\n%s", err, line)
		}
		if len(ev.Transaction.Messages) == 0 {
			t.Errorf("the audit event carries no rule matches:\n%s", line)
		}
		t.Logf("audit sample: %s", line)
	})

	t.Run("blocking stops the attack and serves clean requests", func(t *testing.T) {
		set(func(c *waf.Config) { c.Mode = waf.ModeBlock })
		if got := status(xss); got != 403 {
			t.Errorf("blocking mode answered the XSS with %d, want 403", got)
		}
		if got := status("/?q=hello"); got != 200 {
			t.Errorf("a clean request answered %d, want 200", got)
		}
	})

	t.Run("paranoia level and exclusions pass nginx -t", func(t *testing.T) {
		set(func(c *waf.Config) {
			c.Mode, c.Paranoia = waf.ModeBlock, 3
			c.Exclusions = []waf.Exclusion{
				{Kind: waf.KindRule, RuleID: 920420, Note: "rule removal"},
				{Kind: waf.KindRulePath, RuleID: 942100, Path: "/api/"},
				{Kind: waf.KindRuleArg, RuleID: 942200, Arg: "password"},
			}
		})
		if got := status("/?q=hello"); got != 200 {
			t.Errorf("a clean request answered %d", got)
		}
	})

	t.Run("path blocklist", func(t *testing.T) {
		set(func(c *waf.Config) { c.PathBlocklist = []string{"/secret", "/.git"} })
		for path, want := range map[string]int{"/secret/x": 403, "/SECRET": 403, "/.git/config": 403, "/": 200, "/%73ecret": 403} {
			if got := status(path); got != want {
				t.Errorf("%s answered %d, want %d", path, got, want)
			}
		}
	})

	t.Run("ip deny and allow, and certificate challenges stay open", func(t *testing.T) {
		set(func(c *waf.Config) { c.IPRules.Deny = []string{"127.0.0.1"} })
		if got := status("/"); got != 403 {
			t.Errorf("a denied address answered %d, want 403", got)
		}
		if got := status("/.well-known/acme-challenge/token"); got != 404 {
			t.Errorf("the challenge path answered %d for a denied address, want 404 (reached the site)", got)
		}
		set(func(c *waf.Config) { c.IPRules.Allow = []string{"10.99.0.0/16"} })
		if got := status("/"); got != 403 {
			t.Errorf("an address outside the allow list answered %d, want 403", got)
		}
		set(func(c *waf.Config) { c.IPRules.Allow = []string{"127.0.0.0/8"}; c.IPRules.Deny = []string{"127.0.0.2"} })
		if got := status("/"); got != 200 {
			t.Errorf("an allowed address answered %d, want 200", got)
		}
	})

	t.Run("rate limit", func(t *testing.T) {
		set(func(c *waf.Config) { c.RateLimit = waf.RateLimit{Enabled: true, RequestsPerSecond: 1, Burst: 2} })
		limited := 0
		for range 12 {
			if status("/") == 429 {
				limited++
			}
		}
		if limited == 0 {
			t.Error("twelve quick requests against 1 r/s, burst 2 were never limited")
		}
	})

	t.Run("regenerate keeps the firewall", func(t *testing.T) {
		set(func(c *waf.Config) { c.Mode = waf.ModeBlock })
		if _, err := mgr.Regenerate(bg, "a.test"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(600 * time.Millisecond)
		if got := status(xss); got != 403 {
			t.Errorf("after regenerating the site file the attack answered %d, want 403", got)
		}
	})

	t.Run("a site file from before the block gets it", func(t *testing.T) {
		file := paths.SiteFile("a.test")
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var kept []string
		skip := false
		for _, l := range strings.Split(string(b), "\n") {
			switch strings.TrimSpace(l) {
			case "# >>> pb:waf":
				skip = true
			case "# <<< pb:waf":
				skip = false
			default:
				if !skip {
					kept = append(kept, l)
				}
			}
		}
		if err := os.WriteFile(file, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
		set(func(c *waf.Config) { c.Mode = waf.ModeBlock })
		_, stt, err := mgr.WAFConfig(bg, "a.test")
		if err != nil || !stt.Attached {
			t.Fatalf("the block was not attached: %+v, %v", stt, err)
		}
		if got := status(xss); got != 403 {
			t.Errorf("after attaching the block the attack answered %d, want 403", got)
		}
	})

	t.Run("turning it off removes the files and keeps nginx serving", func(t *testing.T) {
		set(func(c *waf.Config) {})
		for _, p := range []string{paths.WAFRulesFile("a.test"), paths.WAFModuleFile()} {
			if _, err := os.Stat(p); err == nil {
				t.Errorf("%s still exists with the firewall off", p)
			}
		}
		if got := status(xss); got != 200 {
			t.Errorf("with the firewall off the request answered %d, want 200", got)
		}
	})

	t.Run("a restart reconcile finds everything current", func(t *testing.T) {
		set(func(c *waf.Config) {
			c.Mode = waf.ModeBlock
			c.RateLimit = waf.RateLimit{Enabled: true, RequestsPerSecond: 50, Burst: 100}
			c.PathBlocklist = []string{"/secret"}
		})
		s := rec.Run(bg)
		if !s.OK {
			t.Fatalf("reconcile: %+v", s)
		}
		for _, step := range s.Steps {
			if step.Name == "render" && !strings.Contains(step.Detail, "current") {
				t.Errorf("the reconcile rewrote files the firewall had already written: %q", step.Detail)
			}
		}
		if got := status(xss); got != 403 {
			t.Errorf("after the reconcile the attack answered %d, want 403", got)
		}
	})

	t.Run("deleting the site removes its firewall files", func(t *testing.T) {
		if _, err := mgr.Delete(bg, "a.test"); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{paths.WAFHTTPFile("a.test"), paths.WAFRulesFile("a.test"), paths.WAFModuleFile()} {
			if _, err := os.Stat(p); err == nil {
				t.Errorf("%s survived the site", p)
			}
		}
	})
}

// TestRealAccessLogFormatIsReadByTheMonitor checks the access log nginx really
// writes for a new site (the pbpanel format) against the monitor's parser, which
// is what feeds the traffic page.
func TestRealAccessLogFormatIsReadByTheMonitor(t *testing.T) {
	eng := requireNativeHost(t)
	bg := context.Background()
	h := newNativeHost(t, eng, "panel-waf-log-it-", nativeLayout{})
	paths := h.paths
	mgr := h.manager(t, nil)
	rec := h.reconciler(t, mgr)
	if s := rec.Run(bg); !s.OK {
		t.Fatalf("reconcile: %+v", s)
	}
	if _, err := mgr.Create(bg, CreateInput{Domain: "a.test", Kind: KindStatic}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the site to be served", func() bool { c, _ := wafGet(t, "a.test", "/"); return c == 200 })
	wafGet(t, "a.test", "/missing?x=1")

	var lines []string
	waitFor(t, "an access log line", func() bool {
		b, _ := os.ReadFile(paths.AccessLog("a.test"))
		lines = strings.Split(strings.TrimSpace(string(b)), "\n")
		return len(lines) >= 2 && lines[0] != ""
	})
	var saw404 bool
	for _, l := range lines {
		r, ok := wafmon.ParseAccess(l)
		if !ok {
			t.Fatalf("the monitor cannot read nginx's own line: %q", l)
		}
		if r.RequestID == "" || len(r.RequestID) != 32 {
			t.Errorf("request id = %q in %q", r.RequestID, l)
		}
		if strings.HasPrefix(r.Path, "/missing") && r.Status == 404 {
			saw404 = true
		}
	}
	if !saw404 {
		t.Errorf("the 404 was not seen in %q", lines)
	}
}
