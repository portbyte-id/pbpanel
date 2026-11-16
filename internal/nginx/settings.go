package nginx

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// HTTPSettings are the global options the common-settings form owns. They are
// rendered into panel-main.conf and panel-http.conf; nginx.conf sets none of
// them, so the form and the raw file never contradict one another.
type HTTPSettings struct {
	// WorkerProcesses is `auto` or a count.
	WorkerProcesses string `json:"worker_processes"`
	// ClientMaxBodySize is an nginx size: digits with an optional k, m or g;
	// `0` turns the check off.
	ClientMaxBodySize string `json:"client_max_body_size"`
	Gzip              bool   `json:"gzip"`
	// Timeouts are in seconds.
	KeepaliveTimeout    int  `json:"keepalive_timeout"`
	ClientBodyTimeout   int  `json:"client_body_timeout"`
	ClientHeaderTimeout int  `json:"client_header_timeout"`
	SendTimeout         int  `json:"send_timeout"`
	ServerTokens        bool `json:"server_tokens"`
}

// DefaultHTTPSettings are what a fresh install renders.
func DefaultHTTPSettings() HTTPSettings {
	return HTTPSettings{
		WorkerProcesses:     "auto",
		ClientMaxBodySize:   "100m",
		Gzip:                true,
		KeepaliveTimeout:    65,
		ClientBodyTimeout:   60,
		ClientHeaderTimeout: 60,
		SendTimeout:         60,
		ServerTokens:        false,
	}
}

// SettingsError is a rejected settings value, for a 422 that names the field.
type SettingsError struct {
	Field string
	Msg   string
}

func (e *SettingsError) Error() string { return "nginx settings: " + e.Field + ": " + e.Msg }

var bodySizeRe = regexp.MustCompile(`^[0-9]{1,6}[kKmMgG]?$`)

const maxTimeoutSeconds = 3600

// Validate checks every field. A value that passes is safe to write into a
// directive: the checks are what keep a form field from carrying a `;` or a
// newline into the config.
func (s HTTPSettings) Validate() error {
	bad := func(field, format string, args ...any) error {
		return &SettingsError{Field: field, Msg: fmt.Sprintf(format, args...)}
	}
	if s.WorkerProcesses != "auto" {
		n, err := strconv.Atoi(s.WorkerProcesses)
		if err != nil || n < 1 || n > 128 || strconv.Itoa(n) != s.WorkerProcesses {
			return bad("worker_processes", "must be `auto` or a number from 1 to 128")
		}
	}
	if !bodySizeRe.MatchString(s.ClientMaxBodySize) {
		return bad("client_max_body_size", "must be digits with an optional k, m or g, for example 100m (0 turns the limit off)")
	}
	for _, t := range []struct {
		field string
		v     int
	}{
		{"keepalive_timeout", s.KeepaliveTimeout},
		{"client_body_timeout", s.ClientBodyTimeout},
		{"client_header_timeout", s.ClientHeaderTimeout},
		{"send_timeout", s.SendTimeout},
	} {
		if t.v < 1 || t.v > maxTimeoutSeconds {
			return bad(t.field, "must be from 1 to %d seconds", maxTimeoutSeconds)
		}
	}
	return nil
}

// onOff is nginx's spelling of a bool.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// safeToken rejects anything that could end a directive or open a block when
// written into a config file. Renderers call it on every value that did not
// come from a validated type, so a caller bug cannot become a config injection.
func safeToken(what, v string) error {
	if v == "" {
		return fmt.Errorf("nginx: %s is empty", what)
	}
	if strings.ContainsAny(v, " \t\r\n;{}$\"'\\#\x00") {
		return fmt.Errorf("nginx: %s %q contains a character that is not allowed in a config value", what, v)
	}
	return nil
}
