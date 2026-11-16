package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	// DefaultSocket is where Docker Engine listens on every supported host.
	DefaultSocket = "/var/run/docker.sock"

	// apiVersion is pinned in every request path. An unversioned path would
	// mean "whatever the daemon's newest is", so an Engine upgrade could change
	// the meaning of a request the panel has not changed. 1.44 is the floor
	// current Engines still accept and the first with the fields used here.
	apiVersion = "1.44"

	// maxErrorBody bounds how much of an error response is read. Engine errors
	// are one JSON object; anything larger is not an error message.
	maxErrorBody = 64 << 10
)

var (
	// ErrNotFound matches any Engine 404: no such image, container or exec.
	ErrNotFound = errors.New("docker: not found")
	// ErrConflict matches any Engine 409, e.g. a container name already in use.
	ErrConflict = errors.New("docker: conflict")
)

// APIError is a non-2xx answer from the Engine, with its message kept verbatim
// so it can reach the UI unchanged (development plan §1.4, "errors reach the UI").
type APIError struct {
	Op      string
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("docker: %s: %s (HTTP %d)", e.Op, e.Message, e.Status)
}

// Is lets callers test errors.Is(err, ErrNotFound) without knowing status codes.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrConflict:
		return e.Status == http.StatusConflict
	}
	return false
}

// Client talks to one Docker Engine over its Unix socket. It is safe for
// concurrent use.
type Client struct {
	socket string
	http   *http.Client
	// dial opens one connection to the Engine. It is a field only so the
	// package's own tests can run where Unix sockets cannot be dialled; New
	// always sets it to a Unix dial and nothing outside the tests replaces it.
	dial func(ctx context.Context) (net.Conn, error)
}

// New returns a client for the Engine socket at path. The path must be an
// absolute filesystem path: there is no TCP transport and no fallback to one,
// so a mistyped setting can never make the root daemon talk to a network host.
func New(socket string) (*Client, error) {
	if socket == "" {
		return nil, errors.New("docker: socket path is empty")
	}
	if strings.Contains(socket, "://") || !filepath.IsAbs(socket) {
		return nil, fmt.Errorf("docker: socket %q must be an absolute path to a Unix socket", socket)
	}
	dialer := &net.Dialer{}
	c := &Client{socket: socket}
	c.dial = func(ctx context.Context) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socket)
	}
	tr := &http.Transport{
		// The address argument is the placeholder host from the request URL and
		// is ignored on purpose: every connection goes to the socket.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return c.dial(ctx)
		},
		MaxIdleConns:       4,
		DisableCompression: true,
	}
	// No client timeout: pulls, builds and followed logs are long-lived by
	// nature. Every call takes a context and that is the only deadline.
	c.http = &http.Client{Transport: tr}
	return c, nil
}

// Socket reports the socket path the client dials.
func (c *Client) Socket() string { return c.socket }

// Close releases idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// request describes one Engine call.
type request struct {
	op          string
	method      string
	path        string // without the version prefix, e.g. /containers/x/json
	query       url.Values
	body        io.Reader
	contentType string
	unversioned bool
	// okStatus lists non-2xx codes that are not errors for this call (304 for
	// "already started").
	okStatus []int
}

func (c *Client) do(ctx context.Context, r request) (*http.Response, error) {
	u := "http://docker"
	if !r.unversioned {
		u += "/v" + apiVersion
	}
	u += r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, r.method, u, r.body)
	if err != nil {
		return nil, fmt.Errorf("docker: %s: %w", r.op, err)
	}
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker: %s: %w", r.op, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	if slices.Contains(r.okStatus, resp.StatusCode) {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	var body struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &body) == nil && body.Message != "" {
		msg = body.Message
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return nil, &APIError{Op: r.op, Status: resp.StatusCode, Message: msg}
}

// call runs a request whose answer is either nothing or one JSON object.
func (c *Client) call(ctx context.Context, r request, out any) error {
	resp, err := c.do(ctx, r)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("docker: %s: decode response: %w", r.op, err)
	}
	return nil
}

// post runs a request with a JSON body.
func (c *Client) post(ctx context.Context, r request, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("docker: %s: %w", r.op, err)
	}
	r.method = http.MethodPost
	r.body = bytes.NewReader(raw)
	r.contentType = "application/json"
	return c.call(ctx, r, out)
}

// Info is what Ping learns about the Engine.
type Info struct {
	// APIVersion is the newest API version the Engine speaks.
	APIVersion string
}

// Ping checks that the Engine answers and speaks at least the pinned API
// version. It is the readiness probe: a missing socket, a stopped daemon and a
// too-old Engine all fail here with a message that says which.
func (c *Client) Ping(ctx context.Context) (Info, error) {
	resp, err := c.do(ctx, request{op: "ping", method: http.MethodGet, path: "/_ping", unversioned: true})
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	info := Info{APIVersion: resp.Header.Get("API-Version")}
	if info.APIVersion == "" {
		return info, errors.New("docker: ping: the Engine did not report an API version")
	}
	older, err := versionLess(info.APIVersion, apiVersion)
	if err != nil {
		return info, fmt.Errorf("docker: ping: %w", err)
	}
	if older {
		return info, fmt.Errorf("docker: Engine speaks API %s, the panel needs at least %s: upgrade Docker Engine", info.APIVersion, apiVersion)
	}
	return info, nil
}

func versionLess(a, b string) (bool, error) {
	pa, err := parseVersion(a)
	if err != nil {
		return false, err
	}
	pb, err := parseVersion(b)
	if err != nil {
		return false, err
	}
	if pa[0] != pb[0] {
		return pa[0] < pb[0], nil
	}
	return pa[1] < pb[1], nil
}

func parseVersion(s string) ([2]int, error) {
	major, minor, ok := strings.Cut(s, ".")
	if !ok {
		return [2]int{}, fmt.Errorf("bad API version %q", s)
	}
	ma, err1 := strconv.Atoi(major)
	mi, err2 := strconv.Atoi(minor)
	if err1 != nil || err2 != nil {
		return [2]int{}, fmt.Errorf("bad API version %q", s)
	}
	return [2]int{ma, mi}, nil
}

// Names and references end up in request paths, so they are checked against
// the grammar Docker itself uses before any request is built. That is not
// defence against the operator (who is root) but against a bug upstream that
// would otherwise turn into a request for a different resource.
var (
	nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	refRe  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]*$`)
)

func checkName(kind, s string) error {
	if !nameRe.MatchString(s) {
		return fmt.Errorf("docker: invalid %s %q", kind, s)
	}
	return nil
}

func checkRef(s string) error {
	if !refRe.MatchString(s) || strings.Contains(s, "..") || strings.Contains(s, "//") {
		return fmt.Errorf("docker: invalid image reference %q", s)
	}
	return nil
}
