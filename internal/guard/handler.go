package guard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Settings is what the handler needs to know about one site.
type Settings struct {
	Provider   Provider
	Difficulty int
	// Clearance is how long a passed visitor is let through.
	Clearance time.Duration
	// CookieName is the cookie the site's secure_link line reads.
	CookieName string
	// TurnstileSiteKey and TurnstileSecret are the panel's Turnstile keys, read
	// for each request. They are empty when Settings does not have them.
	TurnstileSiteKey string
	TurnstileSecret  string
}

// Lookup returns a site's settings, and false for a site that has no challenge.
type Lookup func(ctx context.Context, domain string) (Settings, bool, error)

// Verifier checks a Turnstile token.
type Verifier interface {
	Verify(ctx context.Context, secret, token, remoteIP string) error
}

// maxVerifyBody bounds what a browser posts back. A Turnstile token is at most
// 2048 characters.
const maxVerifyBody = 8 << 10

// Shown to a visitor when a Turnstile site cannot be checked because the panel
// lacks one of its keys. The fix is in Settings, so the text says so.
const (
	errTurnstileKeyUnset    = "the Turnstile site key is not set; an administrator must set it under Settings > Firewall"
	errTurnstileSecretUnset = "the Turnstile secret key is not set; an administrator must set it under Settings > Firewall"
)

// Handler answers the requests nginx passes on from the challenge page.
type Handler struct {
	keys      *Keyring
	lookup    Lookup
	turnstile Verifier
	log       *slog.Logger
	now       func() time.Time
	// siteverify is outbound, so at most this many run at once: a client
	// posting junk tokens cannot make the panel open unbounded connections.
	slots chan struct{}
}

// NewHandler returns the handler. turnstile may be nil for the Cloudflare one.
func NewHandler(keys *Keyring, lookup Lookup, turnstile Verifier, log *slog.Logger) *Handler {
	if turnstile == nil {
		turnstile = &CloudflareVerifier{}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Handler{keys: keys, lookup: lookup, turnstile: turnstile, log: log, now: time.Now, slots: make(chan struct{}, 16)}
}

type problem struct {
	Detail string `json:"detail"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	domain := r.Header.Get(HeaderSite)
	ip := r.Header.Get(HeaderClient)
	if domain == "" || ip == "" {
		writeJSON(w, http.StatusBadRequest, problem{"the request did not come through the site"})
		return
	}
	s, ok, err := h.lookup(r.Context(), domain)
	if err != nil {
		h.log.Error("guard: reading the site's challenge settings failed", "domain", domain, "error", err)
		writeJSON(w, http.StatusServiceUnavailable, problem{"the check is unavailable right now; try again shortly"})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, problem{"this site has no check"})
		return
	}
	ua := r.UserAgent()

	switch r.URL.Path {
	case NewPath:
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, problem{"use GET"})
			return
		}
		if s.Provider != ProviderPoW {
			writeJSON(w, http.StatusNotFound, problem{"this site does not use a proof-of-work check"})
			return
		}
		var rnd [12]byte
		_, _ = rand.Read(rnd[:])
		token := newPuzzle(h.keys.puzzleKey(domain), ip, ua, s.Difficulty, h.now(), hex.EncodeToString(rnd[:]))
		writeJSON(w, http.StatusOK, map[string]any{"challenge": token, "difficulty": s.Difficulty})

	case ConfigPath:
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, problem{"use GET"})
			return
		}
		if s.Provider != ProviderTurnstile {
			writeJSON(w, http.StatusNotFound, problem{"this site does not use Turnstile"})
			return
		}
		if s.TurnstileSiteKey == "" {
			writeJSON(w, http.StatusServiceUnavailable, problem{errTurnstileKeyUnset})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"sitekey": s.TurnstileSiteKey})

	case VerifyPath:
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, problem{"use POST"})
			return
		}
		if s.Provider == ProviderTurnstile && s.TurnstileSecret == "" {
			writeJSON(w, http.StatusServiceUnavailable, problem{errTurnstileSecretUnset})
			return
		}
		var body struct {
			Challenge string `json:"challenge"`
			Nonce     string `json:"nonce"`
			Token     string `json:"token"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, maxVerifyBody)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, problem{"the answer could not be read; reload the page"})
			return
		}
		if err := h.verify(r.Context(), domain, s, ip, ua, body.Challenge, body.Nonce, body.Token); err != nil {
			writeJSON(w, http.StatusForbidden, problem{err.Error()})
			return
		}
		h.setClearance(w, r, domain, s, ip, ua)
		w.WriteHeader(http.StatusNoContent)

	default:
		writeJSON(w, http.StatusNotFound, problem{"not found"})
	}
}

func (h *Handler) verify(ctx context.Context, domain string, s Settings, ip, ua, challenge, nonce, token string) error {
	switch s.Provider {
	case ProviderPoW:
		return checkPuzzle(h.keys.puzzleKey(domain), ip, ua, challenge, nonce, h.now())
	case ProviderTurnstile:
		if token == "" || len(token) > 2048 {
			return errors.New("the check was not completed; reload the page")
		}
		select {
		case h.slots <- struct{}{}:
			defer func() { <-h.slots }()
		case <-ctx.Done():
			return errors.New("the check timed out; reload the page")
		}
		if err := h.turnstile.Verify(ctx, s.TurnstileSecret, token, ip); err != nil {
			h.log.Info("guard: Turnstile refused a token", "domain", domain, "ip", ip, "error", err)
			return errors.New("the check did not pass; reload the page and try again")
		}
		return nil
	}
	return fmt.Errorf("unknown check %q", s.Provider)
}

// setClearance writes the cookie by hand: net/http would quote a value with a
// comma in it, and nginx's secure_link would then not recognise it.
func (h *Handler) setClearance(w http.ResponseWriter, r *http.Request, domain string, s Settings, ip, ua string) {
	exp := h.now().Add(s.Clearance).Unix()
	v := Clearance(h.keys.CookieSecret(domain), exp, ip, ua)
	c := s.CookieName + "=" + v + "; Path=/; Max-Age=" + strconv.FormatInt(int64(s.Clearance/time.Second), 10) + "; HttpOnly; SameSite=Lax"
	if r.Header.Get(HeaderScheme) == "https" {
		c += "; Secure"
	}
	w.Header().Add("Set-Cookie", c)
	w.Header().Set("Cache-Control", "no-store")
}

// CloudflareVerifier calls Turnstile's siteverify.
type CloudflareVerifier struct {
	// URL overrides the endpoint, for tests.
	URL    string
	Client *http.Client
}

const siteverifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// Verify reports whether Cloudflare accepts token.
func (v *CloudflareVerifier) Verify(ctx context.Context, secret, token, remoteIP string) error {
	endpoint := v.URL
	if endpoint == "" {
		endpoint = siteverifyURL
	}
	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	form := url.Values{"secret": {secret}, "response": {token}, "remoteip": {remoteIP}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("siteverify: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Success bool     `json:"success"`
		Errors  []string `json:"error-codes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return fmt.Errorf("siteverify: HTTP %d: %w", resp.StatusCode, err)
	}
	if !out.Success {
		return fmt.Errorf("siteverify: %s", strings.Join(out.Errors, ", "))
	}
	return nil
}

// Listen opens the unix socket nginx connects to. A socket left by a previous
// run is removed first. The socket is readable and writable by its owner, the
// daemon, and by gid, the group nginx's workers run as.
func Listen(path string, gid int) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("guard: remove the old socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("guard: listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("guard: chmod %s: %w", path, err)
	}
	if err := os.Chown(path, -1, gid); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("guard: chown %s: %w", path, err)
	}
	return ln, nil
}

// Serve serves h on ln until ctx ends.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
