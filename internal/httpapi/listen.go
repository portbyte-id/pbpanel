package httpapi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/portbyte-id/server-panel/internal/settings"
)

// ListenAndServeTLS starts the panel's HTTPS listener on the address in the
// settings registry (`panel.bind:panel.port`) and serves h on it. It blocks
// until the server stops or fails, and returns nil for a clean shutdown.
//
// Both values are read here, not passed in, so the one place that decides where
// the panel answers is the registry — the same values the Settings page shows
// and the recovery CLI prints.
func ListenAndServeTLS(ctx context.Context, reg *settings.Registry, paths CertPaths, h http.Handler) error {
	srv, err := NewServer(ctx, reg, paths, h)
	if err != nil {
		return err
	}
	return srv.ListenAndServeTLS("", "")
}

// NewServer builds the listener and the http.Server without starting it, so the
// caller can decide when to bind. Step 0.8 (a port change without lock-out)
// needs exactly this: start the new listener, then close the old one.
//
// The pair is served through TLSConfig.GetCertificate, which ListenAndServeTLS
// accepts when both of its file arguments are empty. ListenAndServe would ignore
// TLSConfig altogether and serve plain HTTP, so the TLS entry point is the only
// correct way to start this server.
func NewServer(ctx context.Context, reg *settings.Registry, paths CertPaths, h http.Handler) (*http.Server, error) {
	addr, err := ListenAddr(ctx, reg)
	if err != nil {
		return nil, err
	}

	certs, err := NewCertReloader(paths)
	if err != nil {
		return nil, fmt.Errorf("httpapi: load certificate pair: %w", err)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: h,
		// The panel serves one browser. Short timeouts keep a stalled client
		// from holding a connection for long; the write timeout is generous
		// enough for a large settings page on a slow link, and the SSE handler
		// is the one route that outlives it by design.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: certs.GetCertificate,
		},
	}

	// Shutting down on context cancellation is what makes the daemon's signal
	// handling a plain `cancel()` at the call site.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close()
		}
	}()
	return srv, nil
}

// ListenAddr reads panel.bind and panel.port and returns the address to bind.
// A wildcard bind is spelled the way net.Listen wants it.
func ListenAddr(ctx context.Context, reg *settings.Registry) (string, error) {
	bind, err := reg.Str(ctx, "panel.bind")
	if err != nil {
		return "", fmt.Errorf("httpapi: read panel.bind: %w", err)
	}
	port, err := reg.Int(ctx, "panel.port")
	if err != nil {
		return "", fmt.Errorf("httpapi: read panel.port: %w", err)
	}
	return net.JoinHostPort(bind, strconv.Itoa(port)), nil
}

// IsClosed reports whether err is the error a listener returns after a clean
// shutdown, so a caller's serve loop can treat it as a normal stop rather than
// a crash.
func IsClosed(err error) bool {
	return errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed)
}
