# Challenge page (WAF)

A per-site "checking your browser" page in front of the site, part of the firewall
(Sites → site → Firewall → Challenge page). Two checks:

| Check | What the visitor does | Leaves the server |
|---|---|---|
| Proof of work (`pow`) | The browser finds a nonce so that `sha256(challenge:nonce)` starts with N zero bits | Nothing |
| Cloudflare Turnstile (`turnstile`) | Cloudflare's widget, usually no click | The browser talks to Cloudflare; the panel calls `siteverify` |

## Request flow

1. nginx checks the clearance cookie `pbg_<siteid>` itself with `secure_link`:
   `md5("<expires>|$remote_addr|$http_user_agent|<site secret>")`. A visitor
   with a valid cookie costs one MD5; the panel is not involved.
2. Without one, a server-level `if` returns 418, and `error_page 418 =403 /.pb-guard.html`
   serves the static page `panel/nginx/waf/guard/<domain>.html` with **403**. An
   `error_page` with a URI turns any method into GET, so POSTs get the page too.
3. The page's script calls `/.pb-challenge/new` (puzzle) and `/.pb-challenge/verify`.
   nginx proxies `^~ /.pb-challenge/` to the daemon on `panel/run/guard.sock`
   (`0660`, group `www`), with `X-PB-Site`, `X-PB-Client`, `X-PB-Scheme`.
4. The daemon checks the answer and sets the cookie. The page reloads with GET.

Never challenged: `/.well-known/acme-challenge/`, the two challenge paths, and
the site's exempt paths and IPs.

## ModSecurity

The challenge's locations carry `modsecurity off;` whenever the site's firewall
has ModSecurity on. The puzzle fetch and the answer are the requests a visitor
must make to get through, and the page itself is the 403 body, so a rule that
flags any of them leaves a visitor at a check they cannot pass.

The switch lives in the locations rather than in the rules file because nginx
re-runs the request phases after the internal redirect to the page and takes the
configuration of the location it lands in. The engine sees the rules file under
the URI the visitor asked for, so a `SecRule` there would not cover the page.

## Keys and secrets

- Per-site secrets come from `data/secret.key` through HKDF (`pbpanel guard v1 cookie|puzzle <domain>`).
  Nothing new is stored, and a site's cookies survive a panel restart. Without
  `secret.key` the challenge cannot be switched on. A challenge stored on a panel that
  has since lost the key renders as off, so visitors are never locked out.
- Puzzles are stateless: `<expires>.<random>.<difficulty>.<hmac>`, bound to the
  client address and user agent, valid for 5 minutes.
- The Turnstile site key and secret key are panel settings (`waf.turnstile.site_key`,
  `waf.turnstile.secret`), shared by every site that uses Turnstile. The firewall
  reads them on each check, so a change applies to the next visitor without a
  save or a reload, and a site can only be switched to Turnstile while both are
  set. The site key is served to the page by `/.pb-challenge/config`, never
  written into the page on disk.
- At most 16 `siteverify` calls run at once.

## nginx traps found against a real nginx

- **`map` values are cached for the whole request**, internal redirects included,
  and the server-level `if` runs again after each redirect. A cached verdict
  challenged the page itself, and nginx gave up with a bare 418. Making the
  exemption map `volatile` would break an exempt `/api/` that a front controller
  rewrites to `/index.php`. So the exemption stays cached, while the verdict and a
  "this is the page" flag are `volatile`.
- `error_page ... @named` keeps the method: a POST reaches the static file and gets 405.
  The URI form is used instead.
- The locations are `= /.pb-guard.html` (internal) and `^~ /.pb-challenge/`, so a site's
  own `location ~ /\.` rule that denies dotfiles cannot take them.
- `net/http` quotes a cookie value containing a comma, and `secure_link` then rejects
  it, so the `Set-Cookie` header is written by hand.

## Page

The page is static HTML with an inline script and style, served with a CSP that allows
exactly their SHA-256 hashes, plus `challenges.cloudflare.com` for Turnstile. The SHA-256
in the page is a small synchronous implementation, because `crypto.subtle` exists only on
HTTPS pages. A Go test runs it in Node against Go's `sha256`.

## Tests

- `internal/guard`: cookie format, puzzles, the handler for both checks, CSP hashes, and the JS SHA-256 vs Go.
- `internal/waf/challenge_test.go`: validation, rendering, files, refusal without keys, sealing.
- `internal/sites/challenge_test.go`: `SetWAF` end to end, secret redaction and keeping.
- `internal/waf/waf_test.go` `TestChallengeExemptFromModSecurity`: the challenge
  locations turn ModSecurity off, and nothing else does.
- `cmd/panel/nginx_test.go` `TestTurnstileKeysComeFromSettings`: the firewall's
  Turnstile source reads the two settings, live, and satisfies the engine's check.
- `internal/waf/challenge_nginx_integration_test.go` (`-tags integration`): the rendered
  config in stock `nginx:1.28-alpine` with the real handler on the socket, for both checks.
  Passed 2026-11-19 in WSL Docker.

## Not done

- Not run on the native nginx build on a host (Ubuntu `.106` / EL9), nor in a real browser
  against a live site. The page was viewed from a file, and the Turnstile widget loaded and passed with
  Cloudflare's test key.
- No automatic "under attack" mode: the challenge is on or off per site.
- Search engine crawlers are not recognised. The UI warns that pages behind the check
  leave search results.
- No smoke script step.
