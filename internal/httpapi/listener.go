package httpapi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// DefaultGrace is how long the old listener keeps accepting after a port
// change, so the response that carried the change reaches the browser and the
// operator can follow the new URL without a gap.
const DefaultGrace = 5 * time.Second

// Listener owns the panel's live HTTPS server and can move it to another port
// without locking the operator out: the new port is bound and serving before
// the old one is closed, and a failed bind leaves the old one untouched.
type Listener struct {
	handler http.Handler
	tlsCfg  *tls.Config
	certs   *CertReloader
	bind    string
	grace   time.Duration

	// mu serialises rebinds and Close: two concurrent changes must not both
	// believe they own "the old listener".
	mu      sync.Mutex
	cur     *http.Server
	port    int
	retired []*http.Server
	timers  []*time.Timer
	closed  bool
	wg      sync.WaitGroup
}

// NewListener loads the certificate pair and returns a Listener that is not
// serving yet. bind is the interface address (panel.bind); grace is how long a
// retired listener lingers, with zero meaning DefaultGrace.
func NewListener(h http.Handler, paths CertPaths, bind string, grace time.Duration) (*Listener, error) {
	certs, err := NewCertReloader(paths)
	if err != nil {
		return nil, fmt.Errorf("httpapi: load certificate pair: %w", err)
	}
	if grace <= 0 {
		grace = DefaultGrace
	}
	return &Listener{
		handler: h,
		bind:    bind,
		grace:   grace,
		certs:   certs,
		// GetCertificate, not Certificates: every listener the Listener ever binds
		// (including a rebound one) shares this config, so one swap in the
		// reloader reaches them all.
		tlsCfg: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: certs.GetCertificate,
		},
	}, nil
}

// Start binds port and serves. Port 0 picks a free one; Port reports it.
func (l *Listener) Start(port int) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.cur != nil {
		return errors.New("httpapi: listener already started")
	}
	srv, actual, err := l.bindServe(port)
	if err != nil {
		return err
	}
	l.cur, l.port = srv, actual
	return nil
}

// Port is the port currently serving.
func (l *Listener) Port() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.port
}

// ChangePort moves the panel to port. The new listener is serving before the
// old one is scheduled to close, so the caller can return the new URL on the
// connection that asked. If the bind fails nothing changes and the error is
// returned. It returns the URL of the new listener.
func (l *Listener) ChangePort(port int) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return "", errors.New("httpapi: listener is closed")
	}
	if l.cur == nil {
		return "", errors.New("httpapi: listener is not started")
	}
	if port == l.port {
		return l.urlLocked(), nil
	}

	srv, actual, err := l.bindServe(port)
	if err != nil {
		return "", err
	}

	old := l.cur
	l.cur, l.port = srv, actual
	l.retired = append(l.retired, old)
	l.timers = append(l.timers, time.AfterFunc(l.grace, func() { shutdown(old) }))
	return l.urlLocked(), nil
}

// Close stops the live listener and any retired one still in its grace period,
// waiting for in-flight requests up to ctx.
func (l *Listener) Close(ctx context.Context) error {
	l.mu.Lock()
	l.closed = true
	servers := append([]*http.Server{}, l.retired...)
	if l.cur != nil {
		servers = append(servers, l.cur)
	}
	for _, t := range l.timers {
		t.Stop()
	}
	l.mu.Unlock()

	var first error
	for _, srv := range servers {
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
			if first == nil {
				first = err
			}
		}
	}
	l.wg.Wait()
	return first
}

func (l *Listener) urlLocked() string {
	return "https://" + net.JoinHostPort(l.bind, strconv.Itoa(l.port))
}

// bindServe binds first and only then starts serving, so a bind failure is
// returned synchronously instead of surfacing later from a goroutine.
func (l *Listener) bindServe(port int) (*http.Server, int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(l.bind, strconv.Itoa(port)))
	if err != nil {
		return nil, 0, fmt.Errorf("listen on port %d: %w", port, err)
	}
	srv := &http.Server{
		Handler:           l.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		_ = srv.Serve(tls.NewListener(ln, l.tlsCfg))
	}()
	return srv, ln.Addr().(*net.TCPAddr).Port, nil
}

// shutdown closes a retired server: gracefully for in-flight requests, hard
// after ten seconds so a stuck stream cannot keep a dead port open.
func shutdown(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
	}
}

// Certs is the reloader behind the listener's certificate, so the API that
// replaces the panel certificate can swap it without a restart.
func (l *Listener) Certs() *CertReloader { return l.certs }
