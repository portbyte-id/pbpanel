//go:build integration && linux

package waf

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/portbyte-id/server-panel/internal/guard"
	"github.com/portbyte-id/server-panel/internal/nginx"
)

// challengeImage is a stock nginx with secure_link, the module the challenge
// relies on. The panel's own build has it too; this test proves the rendered
// configuration and the cookie format against a real nginx without needing the
// root, systemd host the native tests run on.
const challengeImage = "nginx:1.28-alpine"

type tsOK struct{}

func (tsOK) Verify(_ context.Context, _, token, _ string) error {
	if token != "good" {
		return fmt.Errorf("invalid-input-response")
	}
	return nil
}

// TestChallengeAgainstRealNginx runs the rendered block and http include in a
// real nginx, with the guard handler on its unix socket, and walks a visitor
// through: the page with 403, a puzzle, the answer, the cookie, the site.
func TestChallengeAgainstRealNginx(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	if out, err := exec.Command("docker", "image", "inspect", challengeImage).CombinedOutput(); err != nil {
		if out, err := exec.Command("docker", "pull", challengeImage).CombinedOutput(); err != nil {
			t.Skipf("cannot pull %s: %v\n%s", challengeImage, err, out)
		}
		_ = out
	}

	root := t.TempDir()
	_ = os.Chmod(root, 0o755)
	p := nginx.Paths{Root: root}
	e := guardedEngine(t)
	e.p = p
	// Google's published test keys: the Turnstile branch needs the panel's keys.
	e.WithTurnstile(func(context.Context) (string, string, error) {
		return "1x00000000000000000000AA", "1x0000000000000000000000000000000AA", nil
	})

	for _, provider := range []guard.Provider{guard.ProviderPoW, guard.ProviderTurnstile} {
		t.Run(string(provider), func(t *testing.T) {
			c, err := challengeConfig(func(ch *Challenge) {
				ch.Provider, ch.Difficulty = provider, 10
				ch.ExemptPaths = []string{"/api/", "/hook/"}
			}).Normalize()
			if err != nil {
				t.Fatal(err)
			}
			c.RateLimit.Enabled = true // the challenge has to live with the other protections
			c.IPRules.Deny = []string{"192.0.2.1/32"}
			r := e.RenderConfig("a.test", c)
			for _, f := range r.Files {
				if f.Delete {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.Path, f.Content, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			conf := fmt.Sprintf(`worker_processes 1;
error_log /dev/stderr info;
pid /tmp/nginx.pid;
events {}
http {
    include /etc/nginx/mime.types;
    access_log off;
    include %s;
    server {
        listen 18089;
        server_name a.test;
        # A site's own dotfile rule must not take the challenge's locations.
        location ~ /\. { deny all; }
        location / { return 200 "site ok\n"; }
        location /api/ { return 200 "api ok\n"; }
        # A front controller: the exempt /hook/ is rewritten to a path that is not.
        location /hook/ { rewrite ^ /backend last; }
        location = /backend { return 200 "backend ok\n"; }
%s
    }
}
`, p.WAFHTTPFile("a.test"), indent(string(r.Block)))
			confPath := filepath.Join(root, "nginx.conf")
			if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
				t.Fatal(err)
			}

			sock := p.GuardSocket()
			if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
				t.Fatal(err)
			}
			ln, err := guard.Listen(sock, os.Getgid())
			if err != nil {
				t.Fatal(err)
			}
			_ = os.Chmod(sock, 0o666) // the container's nginx user is not in our group
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h := guard.NewHandler(e.keys, func(ctx context.Context, d string) (guard.Settings, bool, error) {
				// The Turnstile keys are panel settings now: the handler refuses a
				// Turnstile site without them, so the lookup supplies the test pair.
				s := guard.Settings{
					Provider: provider, Difficulty: 10, Clearance: time.Hour, CookieName: CookieName(d),
					TurnstileSiteKey: "1x00000000000000000000AA", TurnstileSecret: "1x0000000000000000000000000000000AA",
				}
				return s, d == "a.test", nil
			}, tsOK{}, nil)
			go func() { _ = guard.Serve(ctx, ln, h) }()

			name := "pb-challenge-it-" + strconv.Itoa(os.Getpid()) + "-" + string(provider)
			out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "--network", "host",
				"-v", root+":"+root, challengeImage, "nginx", "-c", confPath, "-g", "daemon off;").CombinedOutput()
			if err != nil {
				t.Fatalf("docker run: %v\n%s", err, out)
			}
			t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
			if out, err := exec.Command("docker", "exec", name, "nginx", "-t", "-c", confPath).CombinedOutput(); err != nil {
				t.Fatalf("nginx -t refused the rendered config: %v\n%s\n%s", err, out, conf)
			}

			const ua = "ChallengeTest/1.0"
			get := func(method, path, cookie string, body io.Reader) (*http.Response, string) {
				t.Helper()
				var resp *http.Response
				deadline := time.Now().Add(10 * time.Second)
				for {
					req, _ := http.NewRequest(method, "http://127.0.0.1:18089"+path, body)
					req.Host = "a.test"
					req.Header.Set("User-Agent", ua)
					if cookie != "" {
						req.Header.Set("Cookie", cookie)
					}
					if body != nil {
						req.Header.Set("Content-Type", "application/json")
					}
					resp, err = http.DefaultClient.Do(req)
					if err == nil || time.Now().After(deadline) {
						break
					}
					time.Sleep(200 * time.Millisecond)
				}
				if err != nil {
					logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
					t.Fatalf("%s %s: %v\n%s", method, path, err, logs)
				}
				defer func() { _ = resp.Body.Close() }()
				b, _ := io.ReadAll(resp.Body)
				return resp, string(b)
			}

			// No cookie: the page, with 403, for any method.
			resp, body := get(http.MethodGet, "/some/page", "", nil)
			if resp.StatusCode != 403 || !strings.Contains(body, "Checking your browser") {
				t.Fatalf("GET without a cookie: %d\n%s", resp.StatusCode, body)
			}
			if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'sha256-") {
				t.Fatalf("the page has no CSP: %q", csp)
			}
			if resp, body := get(http.MethodPost, "/form", "", strings.NewReader("{}")); resp.StatusCode != 403 || !strings.Contains(body, "Checking your browser") {
				t.Fatalf("POST without a cookie: %d\n%s", resp.StatusCode, body)
			}
			// The page cannot be fetched directly, exempt paths pass, and so do certificate challenges.
			if resp, _ := get(http.MethodGet, guard.PagePath, "", nil); resp.StatusCode != 404 {
				t.Errorf("the internal page answered %d directly", resp.StatusCode)
			}
			if resp, body := get(http.MethodGet, "/api/x", "", nil); resp.StatusCode != 200 || body != "api ok\n" {
				t.Errorf("an exempt path: %d %q", resp.StatusCode, body)
			}
			if resp, body := get(http.MethodGet, "/hook/x", "", nil); resp.StatusCode != 200 || body != "backend ok\n" {
				t.Errorf("an exempt path rewritten internally: %d %q", resp.StatusCode, body)
			}
			if resp, _ := get(http.MethodGet, "/backend", "", nil); resp.StatusCode != 403 {
				t.Errorf("the rewrite target asked for directly answered %d", resp.StatusCode)
			}
			// A forged or foreign cookie does not pass.
			forged := CookieName("a.test") + "=" + guard.Clearance("wrong", time.Now().Add(time.Hour).Unix(), "127.0.0.1", ua)
			if resp, _ := get(http.MethodGet, "/", forged, nil); resp.StatusCode != 403 {
				t.Errorf("a forged cookie answered %d", resp.StatusCode)
			}

			// Pass the check the way the page does.
			var verifyBody string
			if provider == guard.ProviderPoW {
				resp, body = get(http.MethodGet, guard.NewPath, "", nil)
				if resp.StatusCode != 200 {
					t.Fatalf("puzzle: %d %s", resp.StatusCode, body)
				}
				var pz struct {
					Challenge  string `json:"challenge"`
					Difficulty int    `json:"difficulty"`
				}
				if err := json.Unmarshal([]byte(body), &pz); err != nil {
					t.Fatal(err)
				}
				n := 0
				for guard.LeadingZeroBits(sha256.Sum256([]byte(pz.Challenge+":"+strconv.Itoa(n)))) < pz.Difficulty {
					n++
				}
				verifyBody = fmt.Sprintf(`{"challenge":%q,"nonce":"%d"}`, pz.Challenge, n)
			} else {
				if resp, _ := get(http.MethodPost, guard.VerifyPath, "", strings.NewReader(`{"token":"bad"}`)); resp.StatusCode != 403 {
					t.Errorf("a refused Turnstile token answered %d", resp.StatusCode)
				}
				verifyBody = `{"token":"good"}`
			}
			resp, body = get(http.MethodPost, guard.VerifyPath, "", strings.NewReader(verifyBody))
			if resp.StatusCode != 204 {
				t.Fatalf("verify: %d %s", resp.StatusCode, body)
			}
			setCookie := resp.Header.Get("Set-Cookie")
			cookie, _, _ := strings.Cut(setCookie, ";")
			if !strings.HasPrefix(cookie, CookieName("a.test")+"=") {
				t.Fatalf("Set-Cookie %q", setCookie)
			}

			// With the cookie nginx lets the visitor through on its own.
			if resp, body := get(http.MethodGet, "/some/page", cookie, nil); resp.StatusCode != 200 || body != "site ok\n" {
				t.Fatalf("with the cookie: %d %q", resp.StatusCode, body)
			}
			// The cookie is bound to the browser: another user agent is checked again.
			req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:18089/", nil)
			req.Host = "a.test"
			req.Header.Set("User-Agent", "Other/2.0")
			req.Header.Set("Cookie", cookie)
			if r2, err := http.DefaultClient.Do(req); err != nil || r2.StatusCode != 403 {
				t.Fatalf("the cookie with another user agent: %v %v", r2, err)
			} else {
				_ = r2.Body.Close()
			}

			cancel()
			_ = exec.Command("docker", "rm", "-f", name).Run()
		})
	}
}

