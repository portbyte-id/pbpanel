package nginx

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"text/template"
)

// Kind is how a site serves requests.
type Kind string

// Site kinds.
const (
	KindStatic Kind = "static"
	KindProxy  Kind = "proxy"
	KindPHP    Kind = "php"
)

// Names of a site's include files. The panel owns their content; the site file
// pulls them in with one `include` line each, and a missing line is what marks
// a feature detached (step 1.7).
const (
	IncludeListen = "listen.conf"
	IncludeNames  = "names.conf"
	IncludeACME   = "acme.conf"
	IncludeTLS    = "tls.conf"
	IncludeProxy  = "proxy.conf"
	IncludePHP    = "php.conf"
)

// Site is what the templates need about one site.
type Site struct {
	Domain      string
	Aliases     []string
	Kind        Kind
	Root        string
	ProxyTarget string
}

// mainConfTemplate is nginx.conf. It sets nothing the settings form owns and
// nothing a site owns, and it is written once: after that the operator edits it.
const mainConfTemplate = `# Generated once by PB panel; from here on this file is yours to edit.
# The include lines below connect it to the settings form and to the sites.
# Remove one and the panel tells you which feature stopped working.
user www;
pid /run/nginx.pid;
error_log /dev/stderr warn;
include {{.MainInclude}};

events {
    worker_connections 1024;
}

http {
    include /etc/nginx/mime.types;
    default_type application/octet-stream;
    sendfile on;

    # Sites open their own logs; this keeps a site without one from logging here.
    access_log off;

    # Lets a proxy site pass WebSocket upgrades through.
    map $http_upgrade $connection_upgrade {
        default upgrade;
        ''      close;
    }

    include {{.HTTPInclude}};

    # Requests for a host no site claims get no answer instead of the first site.
    server {
        listen 80 default_server;
        listen [::]:80 default_server;
        server_name _;
        return 444;
    }

    include {{.SitesGlob}};
}
`

const mainIncludeTemplate = `# Managed by PB panel (Nginx settings). Rewritten on every save; edit it there.
worker_processes {{.WorkerProcesses}};
`

const httpIncludeTemplate = `# Managed by PB panel (Nginx settings). Rewritten on every save; edit it there.
client_max_body_size {{.ClientMaxBodySize}};
client_body_timeout {{.ClientBodyTimeout}}s;
client_header_timeout {{.ClientHeaderTimeout}}s;
keepalive_timeout {{.KeepaliveTimeout}}s;
send_timeout {{.SendTimeout}}s;
server_tokens {{.ServerTokens}};

gzip {{.Gzip}};
gzip_min_length 1024;
gzip_vary on;
gzip_types text/plain text/css text/xml text/javascript application/json application/javascript application/xml application/rss+xml image/svg+xml;
`

const siteTemplate = `# Managed by PB panel for {{.Domain}}.
# This file is yours to edit. The panel rewrites only the files in
# {{.IncludesDir}}/ and expects the include lines below to stay:
# remove one and the panel disables the matching control instead of fighting you.
server {
    include {{.Listen}};
    include {{.Names}};
    include {{.ACME}};
    include {{.TLS}};

    access_log {{.AccessLog}};
    error_log {{.ErrorLog}};
{{- if .Static}}

    root {{.Root}};
    index index.html index.htm;

    location / {
        try_files $uri $uri/ =404;
    }
{{- else if .PHPSite}}

    root {{.Root}};
    index index.php index.html index.htm;

    include {{.PHP}};
{{- else}}

    include {{.Proxy}};
{{- end}}
}
`

const listenIncludeTemplate = `# Managed by PB panel. Port 443 is added by tls.conf once the site has a certificate.
listen 80;
listen [::]:80;
`

const namesIncludeTemplate = `# Managed by PB panel (site domains).
server_name{{range .}} {{.}}{{end}};
`

// acmeIncludeTemplate answers HTTP-01 challenges from the shared webroot. With
// `root`, nginx appends the whole request URI, so the token file lives at
// <webroot>/.well-known/acme-challenge/<token>, which is where the ACME client
// writes it. The location is a ^~ prefix so no regex location (the PHP one) can
// take the request, and tls.conf exempts the same path from the HTTPS redirect.
const acmeIncludeTemplate = `# Managed by PB panel (certificate challenges).
location ^~ /.well-known/acme-challenge/ {
    root {{.Webroot}};
    default_type text/plain;
    try_files $uri =404;
}
`

// tlsEmptyTemplate is what a site without a certificate gets: nothing that
// nginx could fail on, since a listen 443 ssl without certificate files makes
// `nginx -t` refuse the whole configuration.
const tlsEmptyTemplate = `# Managed by PB panel (TLS). This site has no certificate yet.
`

// tlsIncludeTemplate adds the HTTPS listener to the site's one server block.
//
// The optional redirect lives here, not in a second server block, because the
// panel owns only include files and the site file stays the operator's: a
// second block would have to be a file nginx.conf globs, which is the operator's
// territory. A server-level `if` runs before location matching, so it would
// also redirect the challenge path; the second `if` clears the flag for exactly
// that prefix. The flag is built with set rather than a nested condition
// because nginx has no "and". The scheme test makes it a no-op on :443.
const tlsIncludeTemplate = `# Managed by PB panel (TLS). Rewritten when the certificate changes.
listen 443 ssl;
listen [::]:443 ssl;
http2 on;

ssl_certificate {{.Cert}};
ssl_certificate_key {{.Key}};
ssl_protocols TLSv1.2 TLSv1.3;
ssl_prefer_server_ciphers off;
ssl_session_cache shared:SSL:10m;
ssl_session_timeout 1d;
ssl_session_tickets off;
{{- if .Redirect}}

set $pb_https_redirect "";
if ($scheme = http) {
    set $pb_https_redirect 1;
}
if ($uri ~ ^/\.well-known/acme-challenge/) {
    set $pb_https_redirect "";
}
if ($pb_https_redirect) {
    return 301 https://$host$request_uri;
}
{{- end}}
`

const proxyIncludeTemplate = `# Managed by PB panel (proxy target).
location / {
    proxy_pass {{.Target}};
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
}
`

// phpIncludeTemplate sends a request for a file that does not exist to
// index.php, which is what front-controller applications expect, and passes
// only scripts that exist to FPM, so a crafted path cannot make PHP run a
// file that is not a script.
const phpIncludeTemplate = `# Managed by PB panel (PHP version and pool).
location / {
    try_files $uri $uri/ /index.php?$query_string;
}

location ~ \.php$ {
    try_files $uri =404;
    include /etc/nginx/fastcgi_params;
    fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
    fastcgi_pass unix:{{.Socket}};
}
`

var tmpl = template.Must(template.New("nginx").Option("missingkey=error").Parse(""))

func init() {
	for name, text := range map[string]string{
		"nginx.conf":      mainConfTemplate,
		"panel-main.conf": mainIncludeTemplate,
		"panel-http.conf": httpIncludeTemplate,
		"site":            siteTemplate,
		"listen":          listenIncludeTemplate,
		"names":           namesIncludeTemplate,
		"acme":            acmeIncludeTemplate,
		"tls":             tlsIncludeTemplate,
		"tls-empty":       tlsEmptyTemplate,
		"proxy":           proxyIncludeTemplate,
		"php":             phpIncludeTemplate,
	} {
		template.Must(tmpl.New(name).Parse(text))
	}
}

func execute(name string, data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return nil, fmt.Errorf("nginx: render %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

// RenderMainConf renders nginx.conf.
func (p Paths) RenderMainConf() ([]byte, error) {
	return execute("nginx.conf", struct{ MainInclude, HTTPInclude, SitesGlob string }{
		p.MainIncludePath(), p.HTTPIncludePath(), path.Join(p.SitesDir(), "*.conf"),
	})
}

// RenderMainInclude renders panel-main.conf.
func RenderMainInclude(s HTTPSettings) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return execute("panel-main.conf", s)
}

// RenderHTTPInclude renders panel-http.conf.
func RenderHTTPInclude(s HTTPSettings) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return execute("panel-http.conf", struct {
		HTTPSettings
		Gzip, ServerTokens string
	}{s, onOff(s.Gzip), onOff(s.ServerTokens)})
}

// RenderSiteFile renders a site's own file.
func (p Paths) RenderSiteFile(s Site) ([]byte, error) {
	if err := checkSite(p, s); err != nil {
		return nil, err
	}
	return execute("site", struct {
		Domain, IncludesDir        string
		Listen, Names, ACME, Proxy string
		TLS                        string
		AccessLog, ErrorLog, Root  string
		Static                     bool
		PHPSite                    bool
		PHP                        string
	}{
		s.Domain, p.SiteIncludesDir(s.Domain),
		p.SiteInclude(s.Domain, IncludeListen), p.SiteInclude(s.Domain, IncludeNames),
		p.SiteInclude(s.Domain, IncludeACME), p.SiteInclude(s.Domain, IncludeProxy),
		p.SiteInclude(s.Domain, IncludeTLS),
		p.AccessLog(s.Domain), p.ErrorLog(s.Domain), s.Root,
		s.Kind == KindStatic, s.Kind == KindPHP, p.SiteInclude(s.Domain, IncludePHP),
	})
}

// RenderListen, RenderNames, RenderACME and RenderProxy render a site's
// include files.
func RenderListen() ([]byte, error) { return execute("listen", nil) }

// RenderACME renders acme.conf, which serves the shared challenge webroot.
func (p Paths) RenderACME() ([]byte, error) {
	return execute("acme", struct{ Webroot string }{p.ACMEWebroot()})
}

// TLS is a site's certificate as nginx.conf needs it.
type TLS struct {
	// CertFile is the full chain and KeyFile the private key, both inside the
	// site's certificate directory, where the container can read them.
	CertFile, KeyFile string
	// RedirectHTTP answers every plain-HTTP request except ACME challenges
	// with a 301 to the same URL on HTTPS.
	RedirectHTTP bool
}

// RenderTLS renders tls.conf. A nil t is a site without a certificate and
// renders no directive at all.
func (p Paths) RenderTLS(domain string, t *TLS) ([]byte, error) {
	if t == nil {
		return execute("tls-empty", nil)
	}
	if err := safeToken("domain", domain); err != nil {
		return nil, err
	}
	dir := p.SiteCertDir(domain)
	for what, f := range map[string]string{"certificate": t.CertFile, "private key": t.KeyFile} {
		if err := safeToken(what+" path", f); err != nil {
			return nil, err
		}
		if path.Clean(f) != f || !strings.HasPrefix(f, dir+"/") {
			return nil, fmt.Errorf("nginx: %s %q is outside %s, where the container cannot see it", what, f, dir)
		}
	}
	return execute("tls", struct {
		Cert, Key string
		Redirect  bool
	}{t.CertFile, t.KeyFile, t.RedirectHTTP})
}

// RenderNames renders names.conf: the primary domain first, then the aliases.
func RenderNames(domain string, aliases []string) ([]byte, error) {
	names := append([]string{domain}, aliases...)
	for _, n := range names {
		if err := safeToken("domain", n); err != nil {
			return nil, err
		}
	}
	return execute("names", names)
}

// RenderProxy renders proxy.conf for a target.
func RenderProxy(target string) ([]byte, error) {
	t, err := CheckProxyTarget(target)
	if err != nil {
		return nil, err
	}
	return execute("proxy", struct{ Target string }{t})
}

// RenderPHP renders php.conf for a pool socket.
func (p Paths) RenderPHP(socket string) ([]byte, error) {
	if err := safeToken("php socket", socket); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(socket, p.RunDir()+"/") || path.Clean(socket) != socket {
		return nil, fmt.Errorf("nginx: php socket %q is outside %s, where the container cannot see it", socket, p.RunDir())
	}
	return execute("php", struct{ Socket string }{socket})
}

// CheckProxyTarget validates a proxy target and returns its canonical form:
// `http` or `https`, a host, an optional port, and nothing else. A path would
// change what proxy_pass does to the request URI, so it is refused rather than
// passed through.
func CheckProxyTarget(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if err := safeToken("proxy target", raw); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("nginx: proxy target %q: %w", raw, err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return "", fmt.Errorf("nginx: proxy target %q must start with http:// or https://", raw)
	case u.Hostname() == "":
		return "", fmt.Errorf("nginx: proxy target %q has no host", raw)
	case u.User != nil, u.RawQuery != "", u.Fragment != "", (u.Path != "" && u.Path != "/"):
		return "", fmt.Errorf("nginx: proxy target %q must be only scheme, host and optional port", raw)
	}
	if p := u.Port(); p != "" {
		n := 0
		for _, c := range p {
			n = n*10 + int(c-'0')
		}
		if n < 1 || n > 65535 {
			return "", fmt.Errorf("nginx: proxy target %q has port %s outside 1-65535", raw, p)
		}
	}
	return u.Scheme + "://" + u.Host, nil
}

// checkSite guards the site template. Domains are validated by the sites
// package; this is the second line, and it also checks the path the template
// writes as root.
func checkSite(p Paths, s Site) error {
	if err := safeToken("domain", s.Domain); err != nil {
		return err
	}
	switch s.Kind {
	case KindStatic, KindPHP:
		if err := safeToken("web root", s.Root); err != nil {
			return err
		}
		if !path.IsAbs(s.Root) || path.Clean(s.Root) != s.Root {
			return fmt.Errorf("nginx: web root %q must be a clean absolute path", s.Root)
		}
		if !strings.HasPrefix(s.Root, p.SitesRoot()+"/") {
			return fmt.Errorf("nginx: web root %q is outside %s, where the container cannot see it", s.Root, p.SitesRoot())
		}
	case KindProxy:
	default:
		return errors.New("nginx: unknown site kind " + string(s.Kind))
	}
	return nil
}

// HasIncludeLine reports whether content has a line that is exactly
// `include <file>;`, ignoring surrounding whitespace. It is a literal line
// match, not a parse (risk 4): a commented-out include does not count, and a
// config the panel cannot parse still gets a straight answer.
func HasIncludeLine(content []byte, file string) bool {
	want := "include " + file + ";"
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}
