package php

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"text/template"
)

// InputError is a rejected form value, for a 422 that names the field.
type InputError struct {
	Field string
	Msg   string
}

func (e *InputError) Error() string { return e.Field + ": " + e.Msg }

func bad(field, format string, args ...any) error {
	return &InputError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// Settings are the php.ini directives the form owns. They are written to
// conf.d/zz-panel.ini, which is read last; php.ini itself stays raw text.
type Settings struct {
	MemoryLimit       string `json:"memory_limit"`
	UploadMaxFilesize string `json:"upload_max_filesize"`
	PostMaxSize       string `json:"post_max_size"`
	// MaxExecutionTime is in seconds; 0 means no limit.
	MaxExecutionTime int    `json:"max_execution_time"`
	DateTimezone     string `json:"date_timezone"`
}

// DefaultSettings are what a freshly pulled version renders.
func DefaultSettings() Settings {
	return Settings{
		MemoryLimit:       "256M",
		UploadMaxFilesize: "64M",
		PostMaxSize:       "64M",
		MaxExecutionTime:  60,
		DateTimezone:      "UTC",
	}
}

var (
	iniSizeRe  = regexp.MustCompile(`^[0-9]{1,7}[KMG]?$`)
	timezoneRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+){0,2}$`)
)

// Validate checks every field. A value that passes is safe to write after `=`
// in an ini file.
func (s Settings) Validate() error {
	const sizeHelp = "must be digits with an optional K, M or G, for example 256M"
	if s.MemoryLimit != "-1" && !iniSizeRe.MatchString(s.MemoryLimit) {
		return bad("memory_limit", "%s (-1 removes the limit)", sizeHelp)
	}
	if !iniSizeRe.MatchString(s.UploadMaxFilesize) {
		return bad("upload_max_filesize", "%s", sizeHelp)
	}
	if !iniSizeRe.MatchString(s.PostMaxSize) {
		return bad("post_max_size", "%s", sizeHelp)
	}
	if s.MaxExecutionTime < 0 || s.MaxExecutionTime > 86400 {
		return bad("max_execution_time", "must be from 0 (no limit) to 86400 seconds")
	}
	if len(s.DateTimezone) > 64 || !timezoneRe.MatchString(s.DateTimezone) {
		return bad("date_timezone", "must be a timezone name such as UTC or Europe/Rome")
	}
	return nil
}

// PoolSettings are the process-manager options of one site's pool.
type PoolSettings struct {
	// PM is static, dynamic or ondemand.
	PM          string `json:"pm"`
	MaxChildren int    `json:"max_children"`
	// StartServers, MinSpareServers and MaxSpareServers apply to dynamic only.
	StartServers    int `json:"start_servers"`
	MinSpareServers int `json:"min_spare_servers"`
	MaxSpareServers int `json:"max_spare_servers"`
	// MaxRequests recycles a child after that many requests; 0 never does.
	MaxRequests int `json:"max_requests"`
	// IdleTimeout is how long an ondemand child lives without work, in seconds.
	IdleTimeout int `json:"idle_timeout"`
}

// DefaultPoolSettings suit a small site.
func DefaultPoolSettings() PoolSettings {
	return PoolSettings{
		PM: "dynamic", MaxChildren: 5, StartServers: 2, MinSpareServers: 1, MaxSpareServers: 3,
		MaxRequests: 500, IdleTimeout: 10,
	}
}

// Validate checks the fields, including the relations FPM itself insists on,
// so the form says which field is wrong instead of relaying `php-fpm -t`.
func (s PoolSettings) Validate() error {
	switch s.PM {
	case "static", "dynamic", "ondemand":
	default:
		return bad("pm", "must be static, dynamic or ondemand")
	}
	if s.MaxChildren < 1 || s.MaxChildren > 1000 {
		return bad("max_children", "must be from 1 to 1000")
	}
	if s.MaxRequests < 0 || s.MaxRequests > 1000000 {
		return bad("max_requests", "must be from 0 (never recycle) to 1000000")
	}
	if s.IdleTimeout < 1 || s.IdleTimeout > 3600 {
		return bad("idle_timeout", "must be from 1 to 3600 seconds")
	}
	if s.PM == "dynamic" {
		switch {
		case s.MinSpareServers < 1 || s.MinSpareServers > s.MaxChildren:
			return bad("min_spare_servers", "must be from 1 to max_children")
		case s.MaxSpareServers < s.MinSpareServers || s.MaxSpareServers > s.MaxChildren:
			return bad("max_spare_servers", "must be from min_spare_servers to max_children")
		case s.StartServers < s.MinSpareServers || s.StartServers > s.MaxSpareServers:
			return bad("start_servers", "must be from min_spare_servers to max_spare_servers")
		}
	}
	return nil
}

// maxPoolName keeps the socket path well inside the 108 bytes a Unix socket
// address may be.
const maxPoolName = 48

// PoolName maps a domain to an FPM pool name: dots and dashes become
// underscores. A name that would be too long is cut and given a short hash of
// the whole domain, so two long domains with the same beginning stay apart.
// Different domains can still map to one name (`a-b.test`, `a.b.test`); the
// caller rejects that collision rather than letting two sites share a socket.
func PoolName(domain string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '_'
	}, domain)
	if len(name) > maxPoolName {
		sum := sha256.Sum256([]byte(domain))
		name = name[:maxPoolName-9] + "_" + hex.EncodeToString(sum[:4])
	}
	return name
}

var poolNameRe = regexp.MustCompile(`^[a-z0-9_]{1,48}$`)

// Pool is everything a pool file is rendered from.
type Pool struct {
	Name     string
	Domain   string
	Version  string
	Root     string
	Settings PoolSettings
}

// idlePool is the pool php-fpm.conf itself defines. FPM refuses to run with no
// pool at all, and the container must stay up after its last site is removed
// (step 2.7), so one pool that serves nothing always exists. It listens on a
// Unix socket, never on TCP.
const idlePool = "panel_idle"

const fpmConfTemplate = `; Managed by PB panel. Rewritten when it differs; edit pools and php.ini instead.
[global]
; A real file, not /proc/self/fd/2 or /dev/stderr: under systemd neither is
; openable and FPM exits 78 before it reads a pool. The unit lists this file in
; ReadWritePaths and the panel creates it before the first start.
error_log = {{.ErrorLog}}
daemonize = no

; FPM does not start without a pool. This one serves no site; it keeps the
; version up when the last PHP site on it is removed.
[` + idlePool + `]
user = {{.User}}
group = {{.User}}
listen = {{.IdleSocket}}
listen.owner = {{.User}}
listen.group = {{.User}}
listen.mode = 0600
pm = ondemand
pm.max_children = 1

include = {{.PoolGlob}}
`

const formIniTemplate = `; Managed by PB panel (PHP settings). Rewritten on every save; edit it there.
memory_limit = {{.MemoryLimit}}
upload_max_filesize = {{.UploadMaxFilesize}}
post_max_size = {{.PostMaxSize}}
max_execution_time = {{.MaxExecutionTime}}
date.timezone = {{.DateTimezone}}
`

const poolTemplate = `; Managed by PB panel for {{.Domain}} (PHP pool). Rewritten on every save.
[{{.Name}}]
user = {{.User}}
group = {{.User}}
listen = {{.Socket}}
listen.owner = {{.User}}
listen.group = {{.User}}
listen.mode = 0660
chdir = {{.Root}}

pm = {{.S.PM}}
pm.max_children = {{.S.MaxChildren}}
{{- if eq .S.PM "dynamic"}}
pm.start_servers = {{.S.StartServers}}
pm.min_spare_servers = {{.S.MinSpareServers}}
pm.max_spare_servers = {{.S.MaxSpareServers}}
{{- end}}
{{- if eq .S.PM "ondemand"}}
pm.process_idle_timeout = {{.S.IdleTimeout}}s
{{- end}}
pm.max_requests = {{.S.MaxRequests}}

catch_workers_output = yes
php_admin_flag[log_errors] = on
php_admin_value[error_log] = {{.ErrorLog}}
`

var tmpl = template.Must(template.New("php").Option("missingkey=error").Parse(""))

func init() {
	for name, text := range map[string]string{
		"php-fpm.conf": fpmConfTemplate,
		"zz-panel.ini": formIniTemplate,
		"pool":         poolTemplate,
	} {
		template.Must(tmpl.New(name).Parse(text))
	}
}

func execute(name string, data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return nil, fmt.Errorf("php: render %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

// RenderFPMConf renders php-fpm.conf for a version.
func (p Paths) RenderFPMConf(ver string) ([]byte, error) {
	if err := CheckVersion(ver); err != nil {
		return nil, err
	}
	return execute("php-fpm.conf", struct{ User, IdleSocket, PoolGlob, ErrorLog string }{
		WWWUser, p.Socket(ver, idlePool), path.Join(p.PoolDir(ver), "*.conf"), p.FPMErrorLog(ver),
	})
}

// RenderFormIni renders conf.d/zz-panel.ini.
func RenderFormIni(s Settings) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return execute("zz-panel.ini", s)
}

// RenderPool renders a site's pool file.
func (p Paths) RenderPool(pool Pool) ([]byte, error) {
	if err := CheckVersion(pool.Version); err != nil {
		return nil, err
	}
	if !poolNameRe.MatchString(pool.Name) || pool.Name == idlePool {
		return nil, fmt.Errorf("php: %q is not a usable pool name", pool.Name)
	}
	if err := pool.Settings.Validate(); err != nil {
		return nil, err
	}
	// An ini value ends at a newline and `;` starts a comment; the sites
	// package validates both values, this is the second line.
	for what, v := range map[string]string{"domain": pool.Domain, "web root": pool.Root} {
		if v == "" || strings.ContainsAny(v, " \t\r\n;\"'\\#[]=$\x00") {
			return nil, fmt.Errorf("php: %s %q cannot be written into a pool file", what, v)
		}
	}
	if !p.ServesRoot(pool.Root) || path.Clean(pool.Root) != pool.Root {
		return nil, fmt.Errorf("php: web root %q is outside %s and the allowed web roots, where the container cannot see it", pool.Root, p.SitesRoot())
	}
	return execute("pool", struct {
		Name, Domain, User, Socket, Root, ErrorLog string
		S                                          PoolSettings
	}{
		pool.Name, pool.Domain, WWWUser, p.Socket(pool.Version, pool.Name), pool.Root,
		p.ErrorLog(pool.Domain), pool.Settings,
	})
}
