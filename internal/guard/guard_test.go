package guard

import (
	"context"
	"crypto/md5" //nolint:gosec // the format under test is nginx's
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

var master = []byte("0123456789abcdef0123456789abcdef")

func testKeys(t *testing.T) *Keyring {
	t.Helper()
	k, err := NewKeyring(master)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestKeyringPerSite(t *testing.T) {
	k := testKeys(t)
	a, b := k.CookieSecret("a.test"), k.CookieSecret("b.test")
	if a == b {
		t.Fatal("two sites share a cookie secret")
	}
	if len(a) != 32 || strings.Trim(a, "0123456789abcdef") != "" {
		t.Fatalf("cookie secret %q is not 32 hex characters", a)
	}
	if k.CookieSecret("a.test") != a {
		t.Fatal("the secret is not stable")
	}
	if string(k.puzzleKey("a.test")) == a {
		t.Fatal("puzzle and cookie keys are the same")
	}
	if _, err := NewKeyring([]byte("short")); err == nil {
		t.Fatal("a short master key was accepted")
	}
}

// TestClearanceIsNginxFormat checks the value against nginx's documented
// secure_link_md5 recipe: base64url MD5 without padding, a comma, the expiry.
func TestClearanceIsNginxFormat(t *testing.T) {
	v := Clearance("s3cret", 1700000000, "203.0.113.9", "Mozilla/5.0 (X11)")
	hash, exp, ok := strings.Cut(v, ",")
	if !ok || exp != "1700000000" {
		t.Fatalf("value %q", v)
	}
	sum := md5.Sum([]byte("1700000000|203.0.113.9|Mozilla/5.0 (X11)|s3cret")) //nolint:gosec
	want := strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(base64.StdEncoding.EncodeToString(sum[:])), "=")
	if hash != want {
		t.Fatalf("hash %q, want %q", hash, want)
	}
	if SecureLinkMD5("s3cret") != "$secure_link_expires|$remote_addr|$http_user_agent|s3cret" {
		t.Fatal("the nginx expression and Clearance disagree on the order")
	}
}

func solve(t *testing.T, token string, difficulty int) string {
	t.Helper()
	for n := 0; n < 1<<26; n++ {
		s := strconv.Itoa(n)
		if LeadingZeroBits(sha256.Sum256([]byte(token+":"+s))) >= difficulty {
			return s
		}
	}
	t.Fatal("no nonce found")
	return ""
}

func TestPuzzle(t *testing.T) {
	key := []byte("k")
	now := time.Unix(1_800_000_000, 0)
	tok := newPuzzle(key, "198.51.100.1", "UA", 10, now, "abc")
	nonce := solve(t, tok, 10)

	if err := checkPuzzle(key, "198.51.100.1", "UA", tok, nonce, now.Add(time.Minute)); err != nil {
		t.Fatalf("a solved puzzle was refused: %v", err)
	}
	cases := map[string]struct {
		ip, ua, tok, nonce string
		at                 time.Time
		want               error
	}{
		"other address":     {"198.51.100.2", "UA", tok, nonce, now, errPuzzleInvalid},
		"other agent":       {"198.51.100.1", "UA2", tok, nonce, now, errPuzzleInvalid},
		"easier difficulty": {"198.51.100.1", "UA", strings.Replace(tok, ".10.", ".1.", 1), nonce, now, errPuzzleInvalid},
		"expired":           {"198.51.100.1", "UA", tok, nonce, now.Add(puzzleTTL + time.Second), errPuzzleExpired},
		"not a number":      {"198.51.100.1", "UA", tok, "12a", now, errPuzzleWrong},
		"garbage":           {"198.51.100.1", "UA", "x", nonce, now, errPuzzleInvalid},
	}
	for name, c := range cases {
		if err := checkPuzzle(key, c.ip, c.ua, c.tok, c.nonce, c.at); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", name, err, c.want)
		}
	}
	// A wrong answer: the first nonce that does not solve it.
	for n := 0; ; n++ {
		s := strconv.Itoa(n)
		if LeadingZeroBits(sha256.Sum256([]byte(tok+":"+s))) < 10 {
			if err := checkPuzzle(key, "198.51.100.1", "UA", tok, s, now); !errors.Is(err, errPuzzleWrong) {
				t.Fatalf("a wrong nonce: %v", err)
			}
			break
		}
	}
}

func TestLeadingZeroBits(t *testing.T) {
	var s [32]byte
	if LeadingZeroBits(s) != 256 {
		t.Fatal("all zero")
	}
	s[2] = 0x10
	if got := LeadingZeroBits(s); got != 19 {
		t.Fatalf("got %d, want 19", got)
	}
}

type fakeVerifier struct {
	ok          bool
	gotSecret   string
	gotRemoteIP string
}

func (f *fakeVerifier) Verify(_ context.Context, secret, _, ip string) error {
	f.gotSecret, f.gotRemoteIP = secret, ip
	if !f.ok {
		return errors.New("invalid-input-response")
	}
	return nil
}

func newTestHandler(t *testing.T, s Settings, v Verifier) *Handler {
	t.Helper()
	h := NewHandler(testKeys(t), func(_ context.Context, d string) (Settings, bool, error) {
		switch d {
		case "a.test":
			return s, true, nil
		case "broken.test":
			return Settings{}, false, errors.New("db down")
		}
		return Settings{}, false, nil
	}, v, nil)
	h.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	return h
}

func do(h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set(HeaderSite, "a.test")
	r.Header.Set(HeaderClient, "203.0.113.9")
	r.Header.Set("User-Agent", "TestUA/1.0")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHandlerProofOfWork(t *testing.T) {
	s := Settings{Provider: ProviderPoW, Difficulty: 8, Clearance: time.Hour, CookieName: "pbg_wabc"}
	h := newTestHandler(t, s, &fakeVerifier{})

	w := do(h, http.MethodGet, NewPath, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("new: %d %s", w.Code, w.Body)
	}
	var p struct {
		Challenge  string `json:"challenge"`
		Difficulty int    `json:"difficulty"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Difficulty != 8 {
		t.Fatalf("new body %s: %v", w.Body, err)
	}
	nonce := solve(t, p.Challenge, 8)

	body := fmt.Sprintf(`{"challenge":%q,"nonce":%q}`, p.Challenge, nonce)
	w = do(h, http.MethodPost, VerifyPath, body, map[string]string{HeaderScheme: "https"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("verify: %d %s", w.Code, w.Body)
	}
	cookie := w.Header().Get("Set-Cookie")
	exp := time.Unix(1_800_000_000, 0).Add(time.Hour).Unix()
	want := "pbg_wabc=" + Clearance(testKeys(t).CookieSecret("a.test"), exp, "203.0.113.9", "TestUA/1.0") +
		"; Path=/; Max-Age=3600; HttpOnly; SameSite=Lax; Secure"
	if cookie != want {
		t.Fatalf("cookie\n got %s\nwant %s", cookie, want)
	}

	// Over plain HTTP the cookie is not Secure, or the browser would drop it.
	w = do(h, http.MethodPost, VerifyPath, body, map[string]string{HeaderScheme: "http"})
	if c := w.Header().Get("Set-Cookie"); w.Code != http.StatusNoContent || strings.Contains(c, "Secure") {
		t.Fatalf("http verify: %d %q", w.Code, c)
	}

	// The same answer from another client is refused.
	w = do(h, http.MethodPost, VerifyPath, body, map[string]string{HeaderClient: "198.51.100.1"})
	if w.Code != http.StatusForbidden || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("replayed from elsewhere: %d", w.Code)
	}
}

// TestHandlerExpiredPuzzle: a correct answer given after the puzzle's lifetime
// is refused with expired set, which is what makes the page fetch a new puzzle.
// A wrong answer is refused without it.
func TestHandlerExpiredPuzzle(t *testing.T) {
	s := Settings{Provider: ProviderPoW, Difficulty: 8, Clearance: time.Hour, CookieName: "pbg_wexp"}
	h := newTestHandler(t, s, nil)

	w := do(h, http.MethodGet, NewPath, "", nil)
	var p struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("new body %s: %v", w.Body, err)
	}
	nonce := solve(t, p.Challenge, 8)

	h.now = func() time.Time { return time.Unix(1_800_000_000, 0).Add(puzzleTTL + time.Second) }
	body := fmt.Sprintf(`{"challenge":%q,"nonce":%q}`, p.Challenge, nonce)
	w = do(h, http.MethodPost, VerifyPath, body, nil)
	var got struct {
		Detail  string `json:"detail"`
		Expired bool   `json:"expired"`
	}
	if w.Code != http.StatusForbidden || json.Unmarshal(w.Body.Bytes(), &got) != nil || !got.Expired {
		t.Fatalf("expired puzzle: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Fatal("an expired puzzle set a clearance cookie")
	}

	h.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	w = do(h, http.MethodPost, VerifyPath, fmt.Sprintf(`{"challenge":%q,"nonce":"x"}`, p.Challenge), nil)
	got.Expired = false
	if w.Code != http.StatusForbidden || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Expired {
		t.Fatalf("expired puzzle with a wrong answer: %d %s", w.Code, w.Body)
	}
}

func TestHandlerTurnstile(t *testing.T) {
	s := Settings{Provider: ProviderTurnstile, Clearance: 10 * time.Minute, CookieName: "pbg_w1", TurnstileSiteKey: "0xSITE", TurnstileSecret: "0xSECRET"}
	v := &fakeVerifier{ok: true}
	h := newTestHandler(t, s, v)

	if w := do(h, http.MethodGet, NewPath, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("a Turnstile site handed out a puzzle: %d", w.Code)
	}
	if w := do(h, http.MethodGet, ConfigPath, "", nil); w.Code != http.StatusOK || w.Body.String() != `{"sitekey":"0xSITE"}`+"\n" {
		t.Fatalf("config: %d %q", w.Code, w.Body.String())
	}
	if w := do(h, http.MethodPost, ConfigPath, "", nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST config: %d", w.Code)
	}
	w := do(h, http.MethodPost, VerifyPath, `{"token":"tok"}`, nil)
	if w.Code != http.StatusNoContent || !strings.HasPrefix(w.Header().Get("Set-Cookie"), "pbg_w1=") {
		t.Fatalf("verify: %d %s", w.Code, w.Body)
	}
	if v.gotSecret != "0xSECRET" || v.gotRemoteIP != "203.0.113.9" {
		t.Fatalf("siteverify got secret %q ip %q", v.gotSecret, v.gotRemoteIP)
	}
	if w := do(h, http.MethodPost, VerifyPath, `{"token":""}`, nil); w.Code != http.StatusForbidden {
		t.Fatalf("empty token: %d", w.Code)
	}
	v.ok = false
	if w := do(h, http.MethodPost, VerifyPath, `{"token":"bad"}`, nil); w.Code != http.StatusForbidden || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("refused token: %d", w.Code)
	}
}

// TestHandlerTurnstileKeysUnset: a Turnstile site whose panel lacks a key answers
// 503 with the reason, and never calls siteverify with an empty secret.
func TestHandlerTurnstileKeysUnset(t *testing.T) {
	v := &fakeVerifier{ok: true}
	noSite := newTestHandler(t, Settings{Provider: ProviderTurnstile, Clearance: time.Minute, CookieName: "c", TurnstileSecret: "0xSECRET"}, v)
	if w := do(noSite, http.MethodGet, ConfigPath, "", nil); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "site key") {
		t.Fatalf("config without a site key: %d %s", w.Code, w.Body)
	}
	noSecret := newTestHandler(t, Settings{Provider: ProviderTurnstile, Clearance: time.Minute, CookieName: "c", TurnstileSiteKey: "0xSITE"}, v)
	if w := do(noSecret, http.MethodPost, VerifyPath, `{"token":"tok"}`, nil); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "secret key") {
		t.Fatalf("verify without a secret: %d %s", w.Code, w.Body)
	}
	if v.gotSecret != "" {
		t.Fatalf("siteverify was called with secret %q", v.gotSecret)
	}
}

func TestHandlerRefusals(t *testing.T) {
	h := newTestHandler(t, Settings{Provider: ProviderPoW, Difficulty: 8, Clearance: time.Hour, CookieName: "c"}, nil)
	if w := do(h, http.MethodGet, ConfigPath, "", nil); w.Code != http.StatusNotFound {
		t.Errorf("config of a PoW site: %d", w.Code)
	}
	if w := do(h, http.MethodGet, NewPath, "", map[string]string{HeaderSite: "other.test"}); w.Code != http.StatusNotFound {
		t.Errorf("site without a challenge: %d", w.Code)
	}
	if w := do(h, http.MethodGet, NewPath, "", map[string]string{HeaderSite: "broken.test"}); w.Code != http.StatusServiceUnavailable {
		t.Errorf("lookup failure: %d", w.Code)
	}
	if w := do(h, http.MethodGet, NewPath, "", map[string]string{HeaderClient: ""}); w.Code != http.StatusBadRequest {
		t.Errorf("no client address: %d", w.Code)
	}
	if w := do(h, http.MethodGet, VerifyPath, "", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET verify: %d", w.Code)
	}
	if w := do(h, http.MethodPost, VerifyPath, "{", nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad JSON: %d", w.Code)
	}
	if w := do(h, http.MethodGet, Prefix+"other", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown path: %d", w.Code)
	}
}

func TestCloudflareVerifier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("secret") != "sec" || r.Form.Get("remoteip") != "203.0.113.9" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if r.Form.Get("response") == "good" {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
	}))
	defer srv.Close()
	v := &CloudflareVerifier{URL: srv.URL}
	if err := v.Verify(context.Background(), "sec", "good", "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	err := v.Verify(context.Background(), "sec", "bad", "203.0.113.9")
	if err == nil || !strings.Contains(err.Error(), "invalid-input-response") {
		t.Fatalf("got %v", err)
	}
}

// TestPageCSPMatchesInlineCode guards the hashes: an edit to the script or the
// style that is not reflected in the CSP would leave a blank, broken page.
func TestPageCSPMatchesInlineCode(t *testing.T) {
	for _, p := range []Provider{ProviderPoW, ProviderTurnstile} {
		page := string(Page("a.test", p))
		script := between(t, page, "<script>", "</script>")
		style := between(t, page, "<style>", "</style>")
		csp := CSP(p)
		if !strings.Contains(csp, cspHash(script)) || !strings.Contains(csp, cspHash(style)) {
			t.Fatalf("%s: CSP %q does not allow the page's own script and style", p, csp)
		}
		if strings.Contains(page, "onclick") || strings.Contains(page, "onload=\"") {
			t.Fatalf("%s: inline handlers are blocked by the CSP", p)
		}
		hasCF := strings.Contains(csp, "challenges.cloudflare.com")
		if hasCF != (p == ProviderTurnstile) {
			t.Fatalf("%s: Cloudflare in the CSP = %v", p, hasCF)
		}
	}
	if page := string(Page(`x"><script>`, ProviderTurnstile)); strings.Contains(page, `"><script>`) {
		t.Fatal("values are not escaped")
	}
}

func between(t *testing.T, s, open, closing string) string {
	t.Helper()
	i := strings.Index(s, open)
	if i < 0 {
		t.Fatalf("no %s in the page", open)
	}
	j := strings.Index(s[i+len(open):], closing)
	if j < 0 {
		t.Fatalf("no %s in the page", closing)
	}
	return s[i+len(open) : i+len(open)+j]
}

// TestPageSHA256MatchesGo runs the page's own SHA-256 in Node, so a browser
// and the server agree on what a solved puzzle is. It is skipped without Node.
func TestPageSHA256MatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	start := strings.Index(pageScript, "var C=[")
	end := strings.Index(pageScript, "function pow(){")
	if start < 0 || end < 0 {
		t.Fatal("the hash functions moved; update this test")
	}
	inputs := []string{"", "abc", strings.Repeat("a", 55), strings.Repeat("b", 56), strings.Repeat("c", 64), "1800000300.abc.16.0f:12345"}
	js := pageScript[start:end] + `
var inputs=` + mustJSON(t, inputs) + `;
console.log(JSON.stringify(inputs.map(function(s){return sha(s).map(function(w){return (w>>>0).toString(16).padStart(8,"0")}).join("")})));
console.log(JSON.stringify([zeros([0,1,0,0,0,0,0,0],32),zeros([0,1,0,0,0,0,0,0],63),zeros([0,1,0,0,0,0,0,0],64),zeros([65535,0,0,0,0,0,0,0],16)]));`
	f := t.TempDir() + "/sha.js"
	if err := os.WriteFile(f, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var got []string
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatal(err)
	}
	for i, in := range inputs {
		sum := sha256.Sum256([]byte(in))
		if want := fmt.Sprintf("%x", sum); got[i] != want {
			t.Errorf("sha(%q) = %s, want %s", in, got[i], want)
		}
	}
	if lines[1] != "[true,true,false,true]" {
		t.Errorf("zeros: %s", lines[1])
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
