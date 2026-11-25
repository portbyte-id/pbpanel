package notify

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// dispatchEnv is an engine, a dispatcher and a clock sharing one store.
type dispatchEnv struct {
	svc *Service
	d   *Dispatcher
	r   *Repo
	clk *clock
}

func newDispatchEnv(t *testing.T) *dispatchEnv {
	t.Helper()
	svc, r, clk := engine(t, nil)
	d := NewDispatcher(DispatcherOptions{
		Service:  svc,
		Host:     "panel-test",
		PanelURL: "https://panel.example.com:8443",
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      clk.now,
	})
	return &dispatchEnv{svc: svc, d: d, r: r, clk: clk}
}

// deliveriesOf returns the delivery rows of a channel as the dispatcher left them.
func (e *dispatchEnv) deliveriesOf(t *testing.T, channelID int64) []DeliveryRow {
	t.Helper()
	return rows(t, e.r, channelID)
}

func TestValidateOutboundURL(t *testing.T) {
	ok := []string{
		"https://hooks.example.com/x",
		"https://10.0.0.5/x",
		"http://127.0.0.1:9000/x",
		"http://localhost/x",
		"http://192.168.1.9/x",
		"http://[fd00::1]/x",
	}
	for _, u := range ok {
		if err := ValidateOutboundURL(u); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	bad := []string{
		"",
		"ftp://example.com/x",
		"http://hooks.example.com/x",   // http to a public name
		"http://8.8.8.8/x",             // http to a public address
		"https://169.254.169.254/meta", // link-local
		"https://0.0.0.0/x",            // unspecified
		"https://user:pw@example.com/x",
		"https://example.com/x#frag",
	}
	for _, u := range bad {
		if err := ValidateOutboundURL(u); err == nil {
			t.Errorf("%s accepted, want refused", u)
		}
	}
}

// The dial-time half of D6, with no network needed for the refusal.
func TestGuardDialRefusesLinkLocalAndAllowsLoopbackOverHTTP(t *testing.T) {
	ctx := withScheme(context.Background(), "https")
	if _, err := guardDial(ctx, "tcp", "169.254.169.254:80"); err == nil {
		t.Fatal("link-local destination dialled")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	conn, err := guardDial(withScheme(context.Background(), "http"), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("loopback over http refused: %v", err)
	}
	_ = conn.Close()
}

func TestRedactRemovesURLsAndSecrets(t *testing.T) {
	in := `Post "https://api.telegram.org/bot123:SECRET/sendMessage": dial tcp: refused`
	out := redact(in, "123:SECRET")
	if strings.Contains(out, "SECRET") || strings.Contains(out, "api.telegram.org") {
		t.Fatalf("redact left the token or the URL: %q", out)
	}
	if !strings.Contains(out, "[url]") {
		t.Errorf("redact = %q, want the URL replaced", out)
	}
	if got := redact(strings.Repeat("x", 500)); len(got) > 300 {
		t.Errorf("redact did not cap the length: %d", len(got))
	}
}

func TestWebhookSignatureVerifiesWithIndependentHMAC(t *testing.T) {
	env := newDispatchEnv(t)
	type got struct {
		body    []byte
		headers http.Header
	}
	var mu sync.Mutex
	var seen got
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = got{body: b, headers: r.Header.Clone()}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := channelFor(t, env.r, "signed", func(w *ChannelWrite) {
		w.Config = map[string]any{"url": srv.URL, "sign": true}
		w.Secret = map[string]string{"hmac_secret": "s3cret"}
	})
	env.svc.Emit(context.Background(), warn("cron.failed:sig"))
	env.d.drain(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if seen.body == nil {
		t.Fatal("webhook not called")
	}
	ts := seen.headers.Get("X-PBPanel-Timestamp")
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(ts + "."))
	mac.Write(seen.body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if seen.headers.Get("X-PBPanel-Signature") != want {
		t.Fatalf("signature %q, want %q", seen.headers.Get("X-PBPanel-Signature"), want)
	}
	var body webhookBody
	if err := json.Unmarshal(seen.body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Version != 1 || body.Kind != KindCronFailed || body.Host != "panel-test" || body.Link != "" {
		t.Errorf("body = %+v", body)
	}
	if seen.headers.Get("X-PBPanel-Event") == "" {
		t.Error("X-PBPanel-Event missing")
	}
	expectRows(t, rows(t, env.r, ch.ID), "sent")
}

func TestRetryWithBackoffThenSent(t *testing.T) {
	env := newDispatchEnv(t)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ch := channelFor(t, env.r, "retry", func(w *ChannelWrite) {
		w.Config = map[string]any{"url": srv.URL}
		w.Secret = nil
	})
	// The channel needs the key to exist; webhookWrite-style secret not needed here.
	env.svc.Emit(context.Background(), warn("cron.failed:retry"))
	env.d.drain(context.Background())

	got := env.deliveriesOf(t, ch.ID)
	if len(got) != 1 || got[0].State != stateQueued || got[0].Attempts != 1 || got[0].NextAt == nil {
		t.Fatalf("after one failure: %+v", got)
	}
	if want := env.clk.t.Add(30 * time.Second); !got[0].NextAt.Equal(want) {
		t.Errorf("next attempt at %v, want %v (30 s backoff)", got[0].NextAt, want)
	}

	// Not due yet: nothing is sent.
	env.d.drain(context.Background())
	if hits != 1 {
		t.Fatalf("sent again before the backoff elapsed: %d", hits)
	}

	env.clk.t = env.clk.t.Add(31 * time.Second)
	env.d.drain(context.Background())
	expectRows(t, env.deliveriesOf(t, ch.ID), "sent")
	if hits != 2 {
		t.Fatalf("hits = %d, want 2", hits)
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	env := newDispatchEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	ch := channelFor(t, env.r, "limited", func(w *ChannelWrite) {
		w.Config = map[string]any{"url": srv.URL}
		w.Secret = nil
	})
	env.svc.Emit(context.Background(), warn("cron.failed:limit"))
	env.d.drain(context.Background())

	got := env.deliveriesOf(t, ch.ID)
	if len(got) != 1 || got[0].NextAt == nil || !got[0].NextAt.Equal(env.clk.t.Add(120*time.Second)) {
		t.Fatalf("Retry-After not honoured: %+v", got)
	}
}

func TestPermanentFailureMarksFailedAndRaisesBellEvent(t *testing.T) {
	env := newDispatchEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "bad payload")
	}))
	defer srv.Close()
	ch := channelFor(t, env.r, "bad", func(w *ChannelWrite) {
		w.Config = map[string]any{"url": srv.URL}
		w.Secret = nil
	})
	env.svc.Emit(context.Background(), warn("cron.failed:bad"))
	env.d.drain(context.Background())

	got := env.deliveriesOf(t, ch.ID)
	if len(got) != 1 || got[0].State != "failed" || !strings.Contains(got[0].LastError, "http 400") {
		t.Fatalf("400 not permanent: %+v", got)
	}
	events, err := env.r.ListEvents(context.Background(), EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var bell bool
	for _, ev := range events {
		if ev.Kind == KindNotifyDeliveryFail {
			bell = true
		}
	}
	if !bell {
		t.Fatal("no bell-only notify.delivery_failed event")
	}
	// The bell-only event is never routed to a channel: the only row left is the failed one.
	if all := env.deliveriesOf(t, ch.ID); len(all) != 1 {
		t.Fatalf("bell-only event was routed: %+v", all)
	}
}

func TestFiveFailuresFail(t *testing.T) {
	env := newDispatchEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	ch := channelFor(t, env.r, "flaky", func(w *ChannelWrite) {
		w.Config = map[string]any{"url": srv.URL}
		w.Secret = nil
	})
	env.svc.Emit(context.Background(), warn("cron.failed:flaky"))
	for i := 0; i < maxAttempts; i++ {
		env.clk.t = env.clk.t.Add(3 * time.Hour)
		env.d.drain(context.Background())
	}
	got := env.deliveriesOf(t, ch.ID)
	if len(got) != 1 || got[0].State != "failed" || got[0].Attempts != maxAttempts {
		t.Fatalf("after %d failures: %+v", maxAttempts, got)
	}
}

func TestTelegramTokenNeverStored(t *testing.T) {
	env := newDispatchEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo the path back, as a misbehaving proxy might: the token must not be stored.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "no such bot "+r.URL.Path)
	}))
	defer srv.Close()
	env.d.telegramBase = srv.URL
	ch := channelFor(t, env.r, "tg", func(w *ChannelWrite) {
		w.Type = TypeTelegram
		w.Config = map[string]any{"chat_id": "-100123"}
		w.Secret = map[string]string{"bot_token": "123456:TOPSECRET"}
	})
	env.svc.Emit(context.Background(), warn("cron.failed:tg"))
	env.d.drain(context.Background())

	got := env.deliveriesOf(t, ch.ID)
	if len(got) != 1 || got[0].State != "failed" {
		t.Fatalf("telegram 400 not failed: %+v", got)
	}
	if strings.Contains(got[0].LastError, "TOPSECRET") || strings.Contains(got[0].LastError, "123456") {
		t.Fatalf("token stored in last_error: %q", got[0].LastError)
	}
}

func TestDiscordEmbedShape(t *testing.T) {
	env := newDispatchEnv(t)
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	ch := channelFor(t, env.r, "dc", func(w *ChannelWrite) {
		w.Type = TypeDiscord
		w.Config = map[string]any{}
		w.Secret = map[string]string{"webhook_url": srv.URL + "/api/webhooks/1/tok"}
	})
	env.svc.Emit(context.Background(), Event{Kind: KindTLSRenewFailed, Key: "tls.renew_failed:x", Subject: "renew failed", Source: "tls", Link: "/tls"})
	env.d.drain(context.Background())

	expectRows(t, env.deliveriesOf(t, ch.ID), "sent")
	mentions := body["allowed_mentions"].(map[string]any)["parse"].([]any)
	if len(mentions) != 0 {
		t.Errorf("allowed_mentions.parse = %v, want empty", mentions)
	}
	embed := body["embeds"].([]any)[0].(map[string]any)
	if embed["url"] != "https://panel.example.com:8443/tls" || embed["title"] != "renew failed" {
		t.Errorf("embed = %v", embed)
	}
}

func TestUnreadableSecretFailsPermanently(t *testing.T) {
	env := newDispatchEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ch := channelFor(t, env.r, "locked", func(w *ChannelWrite) {
		w.Config = map[string]any{"url": srv.URL, "sign": true}
	})
	env.svc.Emit(context.Background(), warn("cron.failed:locked"))

	// A host with another key reads the same sealed secret and cannot open it.
	env.d.repo = NewRepo(env.r.st, nil, env.clk.now)
	env.d.drain(context.Background())
	_ = ch
	got := env.deliveriesOf(t, ch.ID)
	if len(got) != 1 || got[0].State != "failed" || !strings.Contains(got[0].LastError, "credentials unreadable") {
		t.Fatalf("unreadable secret: %+v", got)
	}
}

func TestQueuedRowsResumeAfterRestart(t *testing.T) {
	env := newDispatchEnv(t)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ch := channelFor(t, env.r, "resume", func(w *ChannelWrite) {
		w.Config = map[string]any{"url": srv.URL}
		w.Secret = nil
	})
	env.svc.Emit(context.Background(), warn("cron.failed:resume"))
	if hits != 0 {
		t.Fatal("sent before any dispatcher ran")
	}

	// A new engine and dispatcher over the same database pick up the queue.
	svc2 := New(Options{Repo: env.r, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: env.clk.now})
	d2 := NewDispatcher(DispatcherOptions{Service: svc2, Host: "panel-test", Now: env.clk.now})
	d2.drain(context.Background())

	expectRows(t, env.deliveriesOf(t, ch.ID), "sent")
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
}

func TestDiscordCapsAndSeverityColour(t *testing.T) {
	long := strings.Repeat("é", 300) // 600 bytes
	if got := clampText(long, 256); len(got) > 256 || !strings.HasPrefix(long, got) {
		t.Errorf("clampText broke a rune or passed the cap: len %d", len(got))
	}
	if discordColour(Critical) == discordColour(Info) {
		t.Error("severity colours do not differ")
	}
}

func TestSMTPDeliversWithAuth(t *testing.T) {
	env := newDispatchEnv(t)
	srv := newFakeSMTP(t, "pb-user", "pb-pass")
	ch := channelFor(t, env.r, "mail", func(w *ChannelWrite) {
		w.Type = TypeSMTP
		w.Config = map[string]any{
			"host": "127.0.0.1", "port": float64(srv.port), "security": "none",
			"username": "pb-user", "from": "pb@example.com", "to": []any{"ops@example.com"},
		}
		w.Secret = map[string]string{"password": "pb-pass"}
	})
	env.svc.Emit(context.Background(), warn("cron.failed:mail"))
	env.d.drain(context.Background())

	expectRows(t, env.deliveriesOf(t, ch.ID), "sent")
	msg := srv.message()
	if !strings.Contains(msg, "Subject: ") || !strings.Contains(msg, "kind: cron.failed") {
		t.Fatalf("message = %q", msg)
	}
	if srv.authed() != "pb-user" {
		t.Fatalf("auth user = %q", srv.authed())
	}
}

func TestSMTPPlainRefusedOffLoopback(t *testing.T) {
	if err := ValidateChannel(ChannelWrite{Type: TypeSMTP, Config: map[string]any{
		"host": "mail.example.com", "security": "none", "from": "a@example.com", "to": []any{"b@example.com"},
	}}, func(string) string { return "" }); err == nil {
		t.Fatal("security none to a public host accepted")
	}
}

func TestValidateChannelRules(t *testing.T) {
	none := func(string) string { return "" }
	cases := []struct {
		name string
		w    ChannelWrite
		sec  func(string) string
		ok   bool
	}{
		{"webhook ok", ChannelWrite{Type: TypeWebhook, Config: map[string]any{"url": "https://a.example.com/x"}}, none, true},
		{"webhook public http", ChannelWrite{Type: TypeWebhook, Config: map[string]any{"url": "http://a.example.com/x"}}, none, false},
		{"webhook reserved header", ChannelWrite{Type: TypeWebhook, Config: map[string]any{
			"url": "https://a.example.com/x", "headers": map[string]any{"X-PBPanel-Event": "1"}}}, none, false},
		{"webhook bad header name", ChannelWrite{Type: TypeWebhook, Config: map[string]any{
			"url": "https://a.example.com/x", "headers": map[string]any{"Bad Name": "1"}}}, none, false},
		{"telegram needs token", ChannelWrite{Type: TypeTelegram, Config: map[string]any{"chat_id": "1"}}, none, false},
		{"telegram ok", ChannelWrite{Type: TypeTelegram, Config: map[string]any{"chat_id": "1"}},
			func(f string) string {
				if f == "bot_token" {
					return "t"
				}
				return ""
			}, true},
		{"discord needs url", ChannelWrite{Type: TypeDiscord}, none, false},
		{"discord ok", ChannelWrite{Type: TypeDiscord}, func(f string) string {
			if f == "webhook_url" {
				return "https://discord.example.com/api/webhooks/1/t"
			}
			return ""
		}, true},
	}
	for _, c := range cases {
		err := ValidateChannel(c.w, c.sec)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// fakeSMTP is a minimal SMTP server: EHLO, AUTH PLAIN, MAIL, RCPT, DATA, QUIT.
type fakeSMTP struct {
	port int
	mu   sync.Mutex
	user string
	msg  string
}

func newFakeSMTP(t *testing.T, user, pass string) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &fakeSMTP{port: ln.Addr().(*net.TCPAddr).Port}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.session(conn, user, pass)
		}
	}()
	return s
}

func (s *fakeSMTP) session(conn net.Conn, user, pass string) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	write := func(line string) { _, _ = io.WriteString(conn, line+"\r\n") }
	write("220 fake ready")
	authed := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.Fields(line + " _")[0])
		switch verb {
		case "EHLO", "HELO":
			write("250-fake")
			write("250 AUTH PLAIN")
		case "AUTH":
			parts := strings.Fields(line)
			raw, _ := base64.StdEncoding.DecodeString(parts[len(parts)-1])
			fields := strings.Split(string(raw), "\x00")
			if len(fields) == 3 && fields[1] == user && fields[2] == pass {
				authed = true
				s.mu.Lock()
				s.user = fields[1]
				s.mu.Unlock()
				write("235 ok")
			} else {
				write("535 bad credentials")
			}
		case "MAIL", "RCPT":
			if !authed {
				write("530 authenticate first")
				continue
			}
			write("250 ok")
		case "DATA":
			write("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.msg = b.String()
			s.mu.Unlock()
			write("250 queued")
		case "QUIT":
			write("221 bye")
			return
		default:
			write("500 unknown " + strconv.Quote(verb))
		}
	}
}

func (s *fakeSMTP) message() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.msg
}

func (s *fakeSMTP) authed() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.user
}
