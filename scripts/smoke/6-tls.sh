#!/bin/sh
# Smoke 6: the M6 exit gate, end to end, against a VM the panel is installed on.
#
#   SMOKE_HOST=root@203.0.113.7 \
#   TLS_TEST_DOMAIN=tls-smoke.example.com \
#   TLS_TEST_WILDCARD_ZONE=example.com \
#   CF_API_TOKEN=... \
#   sh scripts/smoke/6-tls.sh
#   SMOKE_DRY_RUN=1 sh scripts/smoke/6-tls.sh            # print the plan only
#
# Run it after smokes 0 and 1 (nginx must be up). It creates a static site for
# TLS_TEST_DOMAIN, issues a certificate by HTTP-01 from Let's Encrypt STAGING,
# turns the HTTP to HTTPS redirect on and checks the ACME challenge path is not
# redirected, issues a wildcard by DNS-01 through Cloudflare for a second site,
# forces a renewal, uploads a self-signed certificate, replaces the PANEL
# certificate by upload without a restart, removes the certificates and leaves
# the VM as it found it (sites, provider and panel certificate restored).
#
# Needs a VM with a PUBLIC address: Let's Encrypt must reach port 80 of it, and
# this workstation must reach ports 80 and 443. Nothing here can run on a VM
# behind NAT or on the sandbox.
#
# Environment (required):
#   SMOKE_HOST               user@host of the VM; the user must be root
#   TLS_TEST_DOMAIN          a hostname whose A record points at the VM
#   TLS_TEST_WILDCARD_ZONE   a zone in the Cloudflare account; the wildcard is
#                            issued for *.<TLS_TEST_WILDCARD_SUB>
#   CF_API_TOKEN             Cloudflare token scoped to Zone:DNS:Edit. It is
#                            never printed: the script redacts its own output,
#                            then fails the run if the raw output held it.
# Environment (optional):
#   TLS_TEST_WILDCARD_SUB    subdomain the wildcard covers (default
#                            smoke6.<TLS_TEST_WILDCARD_ZONE>)
#   TLS_TEST_EMAIL           ACME account email sent with the issue calls
#   TLS_ALLOW_PRODUCTION=1   run although tls.acme.directory is not the staging
#                            URL. It burns real rate limits; leave it unset.
#   SMOKE_PASSWORD, SMOKE_USER, SMOKE_SSH_OPTS   as in the other smokes
#
# Independent oracle: "what certificate is served" is `openssl s_client` run
# from the workstation against port 443 (or the panel port) with an SNI name,
# read with `openssl x509`; "does the site serve" is curl with --connect-to;
# "was the panel restarted" is systemctl on the VM. None is a panel endpoint.
#
# Regression: it changes nothing outside its own two sites, the cloudflare
# provider record and the panel certificate pair, and puts the last back. It
# does not touch the firewall, nginx.conf or any other site.

set -u
set +x # never trace: the Cloudflare token is in this process's environment
here=${TLS_SMOKE_DIR:-$(cd "$(dirname "$0")" && pwd)}
# shellcheck source=lib.sh
. "$here/lib.sh"

# The staging issuer is "(STAGING) ..." in the RSA and ECDSA hierarchies.
STAGING_ISSUER_RE='(\(STAGING\)|STAGING)'
STAGING_DIRECTORY='https://acme-staging-v02.api.letsencrypt.org/directory'

# ---- pure helpers (tested against fixtures, no network) ----------------------

# tls_missing_env: print the names of the required variables that are unset or
# empty, one per line.
tls_missing_env() {
	for t_name in TLS_TEST_DOMAIN TLS_TEST_WILDCARD_ZONE CF_API_TOKEN; do
		eval "t_val=\${$t_name:-}"
		[ -n "$t_val" ] || printf '%s\n' "$t_name"
	done
}

# is_staging_issuer TEXT: success when the issuer line names the staging CA.
is_staging_issuer() {
	printf '%s\n' "$1" | grep -Eq "$STAGING_ISSUER_RE"
}

# is_staging_directory URL: success for exactly the Let's Encrypt staging URL.
is_staging_directory() {
	[ "$1" = "$STAGING_DIRECTORY" ]
}

# x509_field INFO NAME: the value of `issuer=`, `serial=`, `notBefore=` or
# `notAfter=` in `openssl x509 -noout` output.
x509_field() {
	printf '%s\n' "$1" | sed -n "s/^$2=//p" | head -n 1
}

# norm_serial SERIAL: hex serial without colons or leading zeros, upper case.
norm_serial() {
	printf '%s' "$1" | tr -d ':' | tr 'a-f' 'A-F' | sed 's/^0*//'
}

# json_field KEY: the first string value of KEY in the JSON on stdin.
json_field() {
	sed -n 's/.*"'"$1"'":"\([^"]*\)".*/\1/p' | head -n 1
}

# pem_json FILE: a PEM file as the body of a JSON string (newlines escaped).
pem_json() {
	awk '{printf "%s\\n", $0}' "$1"
}

# redact_stream: copy stdin to stdout with every occurrence of $CF_API_TOKEN
# replaced by ***. The token is read from the environment, never from argv.
redact_stream() {
	awk '{
		t = ENVIRON["CF_API_TOKEN"]
		while (t != "" && (i = index($0, t)) > 0) {
			$0 = substr($0, 1, i - 1) "***" substr($0, i + length(t))
		}
		print
		fflush()
	}'
}

# token_in_file FILE: success when the file contains $CF_API_TOKEN.
token_in_file() {
	awk 'BEGIN { t = ENVIRON["CF_API_TOKEN"]; found = 0 }
		t != "" && index($0, t) > 0 { found = 1 }
		END { exit(found ? 0 : 1) }' "$1"
}

# is_ip VALUE: an IPv4 address (the only form this smoke supports for a host).
is_ip() {
	case $1 in
	*[!0-9.]* | '') return 1 ;;
	esac
	return 0
}

# a_in_list VALUE "A B C": success when VALUE is one of the words.
a_in_list() {
	for t_w in $2; do
		[ "$t_w" = "$1" ] && return 0
	done
	return 1
}

# pem_info PEM: issuer, serial and dates, as `openssl x509 -noout` prints them.
pem_info() {
	[ -n "$1" ] || return 0
	printf '%s\n' "$1" | openssl x509 -noout -issuer -serial -dates 2>/dev/null
}

# pem_names PEM: the subject alternative names of the certificate.
pem_names() {
	[ -n "$1" ] || return 0
	printf '%s\n' "$1" | openssl x509 -noout -text 2>/dev/null | grep -A1 'Subject Alternative Name' | tail -n 1
}

# selfsigned CERT KEY CN SAN: a throwaway self-signed pair, 2 days, 2048-bit RSA.
selfsigned() {
	openssl req -x509 -newkey rsa:2048 -nodes -days 2 -keyout "$2" -out "$1" \
		-subj "/CN=$3" -addext "subjectAltName=$4" >/dev/null 2>&1
}

if [ "${TLS_SMOKE_SOURCE_ONLY:-0}" = 1 ]; then
	return 0
fi

# ---- the wrapper: redact the display, then scan the raw output ----------------
# The inner copy of this script does the work. Its output goes through
# redact_stream to the terminal and, unredacted, to a file that is scanned for
# the token afterwards. If the token ever reached the output, the run fails.
if [ "${TLS_SMOKE_INNER:-}" != 1 ] && [ "$SMOKE_DRY_RUN" != 1 ]; then
	missing=$(tls_missing_env)
	if [ -n "$missing" ]; then
		log "smoke 6 needs these environment variables, unset or empty:"
		printf '%s\n' "$missing" | sed 's/^/  /'
		log "TLS_TEST_DOMAIN must resolve to the VM's public address; CF_API_TOKEN needs Zone:DNS:Edit."
		exit 2
	fi
	wrap_dir=$(mktemp -d "${TMPDIR:-/tmp}/smoke6.XXXXXX") || exit 2
	(
		TLS_SMOKE_INNER=1 sh "$0"
		echo $? >"$wrap_dir/rc"
	) 2>&1 | tee "$wrap_dir/raw" | redact_stream
	inner_rc=$(cat "$wrap_dir/rc" 2>/dev/null || echo 1)
	if token_in_file "$wrap_dir/raw"; then
		rm -rf "$wrap_dir"
		log "SMOKE FAILED: the Cloudflare token appeared in this script's output (shown above as ***)"
		exit 1
	fi
	rm -rf "$wrap_dir"
	exit "$inner_rc"
fi

# ---- configuration ------------------------------------------------------------
SMOKE_USER=${SMOKE_USER:-admin}
PANEL=/www/panel/bin/pbp
PANEL_SSL=/www/panel/ssl

if [ "$SMOKE_DRY_RUN" = 1 ]; then
	TLS_TEST_DOMAIN=${TLS_TEST_DOMAIN:-tls-smoke.example.test}
	TLS_TEST_WILDCARD_ZONE=${TLS_TEST_WILDCARD_ZONE:-example.test}
	CF_API_TOKEN=${CF_API_TOKEN:-dry-run-token}
else
	missing=$(tls_missing_env)
	if [ -n "$missing" ]; then
		log "smoke 6 needs these environment variables, unset or empty:"
		printf '%s\n' "$missing" | sed 's/^/  /'
		exit 2
	fi
fi
TLS_TEST_WILDCARD_SUB=${TLS_TEST_WILDCARD_SUB:-smoke6.$TLS_TEST_WILDCARD_ZONE}
TLS_TEST_EMAIL=${TLS_TEST_EMAIL:-}
TLS_ALLOW_PRODUCTION=${TLS_ALLOW_PRODUCTION:-0}

D1=$TLS_TEST_DOMAIN
D2=site.$TLS_TEST_WILDCARD_SUB
WILDCARD="*.$TLS_TEST_WILDCARD_SUB"
MARK1="tls-one-$$-marker"
MARK2="tls-two-$$-marker"
PROVIDER=cloudflare

smoke_init
host=$(vm_host)
START_EPOCH=$(date +%s)
CLEANED=0
PROVIDER_SET=0
PANEL_REPLACED=0
JOB=

last_body() { cat "$SMOKE_TMP/last.json" 2>/dev/null; }

# api_file METHOD PATH BODYFILE: like api_call, but the body is read from a
# file, so a token or a private key never appears in a process argument list.
api_file() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then printf '200'; return 0; fi
	CSRF=$(cat "$SMOKE_TMP/csrf" 2>/dev/null)
	curl -sk --max-time 30 -b "$COOKIE_JAR" -o "$SMOKE_TMP/last.json" -w '%{http_code}' \
		-X "$1" -H 'Content-Type: application/json' -H "X-CSRF-Token: $CSRF" \
		--data-binary @"$3" "https://$host:$port$2" 2>/dev/null || true
}

# assert_http_in DESCRIPTION "200 204" ACTUAL
assert_http_in() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then log "  [dry-run] assert $1 -> HTTP $2"; return 0; fi
	if a_in_list "$3" "$2"; then _pass "$1 (HTTP $3)"; else _fail "$1" "expected HTTP one of: $2" "actual HTTP:           $3"; fi
}

# with_timeout SECONDS CMD...
with_timeout() {
	t_secs=$1
	shift
	if command -v timeout >/dev/null 2>&1; then timeout "$t_secs" "$@"; else "$@"; fi
}

# tls_pem CONNECT_HOST PORT SNI: the leaf certificate served there, as PEM, read
# by the workstation's openssl. Empty when nothing answers with a certificate.
tls_pem() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then return 0; fi
	if [ -n "$3" ]; then
		printf '' | with_timeout 20 openssl s_client -connect "$1:$2" -servername "$3" 2>/dev/null | openssl x509 2>/dev/null
	else
		printf '' | with_timeout 20 openssl s_client -connect "$1:$2" 2>/dev/null | openssl x509 2>/dev/null
	fi
}

# served SNI: the PEM of what the VM serves on 443 for that name (dialled via
# TLS_TEST_DOMAIN, so a site without a DNS record can still be checked).
served() { tls_pem "$TLS_TEST_DOMAIN" 443 "$1"; }

served_serial() { norm_serial "$(x509_field "$(pem_info "$(served "$1")")" serial)"; }

# serial_is SNI SERIAL / serial_not SNI SERIAL: for wait_until.
serial_is() { [ -n "$(served_serial "$1")" ] && [ "$(served_serial "$1")" = "$2" ]; }
serial_not() { s_now=$(served_serial "$1"); [ -n "$s_now" ] && [ "$s_now" != "$2" ]; }
no_https() { [ -z "$(served "$1")" ]; }

panel_serial() { norm_serial "$(x509_field "$(pem_info "$(tls_pem "$host" "$port" "")")" serial)"; }
panel_serial_is() { [ -n "$(panel_serial)" ] && [ "$(panel_serial)" = "$1" ]; }

# site_http DOMAIN: the body answered on port 80, dialled via TLS_TEST_DOMAIN.
site_http() {
	[ "$SMOKE_DRY_RUN" = 1 ] && return 0
	curl -s --max-time 10 --connect-to "$1:80:$TLS_TEST_DOMAIN:80" "http://$1/" 2>/dev/null || true
}

# site_https DOMAIN: the body answered on port 443; -k because staging is untrusted.
site_https() {
	[ "$SMOKE_DRY_RUN" = 1 ] && return 0
	curl -sk --max-time 10 --connect-to "$1:443:$TLS_TEST_DOMAIN:443" "https://$1/" 2>/dev/null || true
}

# http_probe DOMAIN PATH: "<status> <redirect url>" for a plain-HTTP request.
http_probe() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then printf '000 '; return 0; fi
	curl -s -o /dev/null -w '%{http_code} %{redirect_url}' --max-time 10 \
		--connect-to "$1:80:$TLS_TEST_DOMAIN:80" "http://$1$2" 2>/dev/null || true
}

# wait_job JOB_ID: poll the job's SSE stream until it carries a terminal state.
wait_job() {
	i=0
	while [ "$i" -lt 120 ]; do
		_state=$(curl -sk --max-time 10 -b "$COOKIE_JAR" "https://$host:$port/api/jobs/$1/events" 2>/dev/null \
			| grep -o '"state":"[a-z]*"' | tail -n 1)
		case $_state in
		'"state":"succeeded"' | '"state":"failed"' | '"state":"canceled"' | '"state":"interrupted"')
			printf '%s' "$_state"
			return 0
			;;
		esac
		i=$((i + 1))
	done
	printf '%s' timeout
	return 1
}

# job_started DESCRIPTION STATUS: assert a 202 and keep its job id in $JOB.
job_started() {
	assert_http "$1" 202 "$2"
	JOB=$(last_body | json_field job_id)
	[ "$SMOKE_DRY_RUN" = 1 ] && JOB=dryrunjob
	assert_not_equal "$1 returned a job id" "" "$JOB"
}

# job_done DESCRIPTION: wait for $JOB and assert it succeeded; on failure show
# the job's events so the ACME error is in the log.
job_done() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then log "  [dry-run] wait for job: $1"; return 0; fi
	t_state=$(wait_job "$JOB")
	assert_equal "$1" '"state":"succeeded"' "$t_state"
	if [ "$t_state" != '"state":"succeeded"' ]; then
		curl -sk --max-time 10 -b "$COOKIE_JAR" "https://$host:$port/api/jobs/$JOB/events" 2>/dev/null | tail -n 20
	fi
}

# issue_body METHOD NAMES_JSON [PROVIDER]: the JSON for POST .../tls/issue.
issue_body() {
	t_b="{\"method\":\"$1\",\"names\":$2"
	[ -n "${3:-}" ] && t_b="$t_b,\"provider\":\"$3\""
	[ -n "$TLS_TEST_EMAIL" ] && t_b="$t_b,\"email\":\"$TLS_TEST_EMAIL\""
	printf '%s}' "$t_b"
}

# upload_body CERT KEY FILE: write the upload JSON for a pair into FILE.
upload_body() {
	printf '{"certificate":"%s","private_key":"%s"}' "$(pem_json "$1")" "$(pem_json "$2")" >"$3"
}

# ---- cleanup: runs on exit, even after a failed assertion -----------------------
tls_cleanup() {
	if [ "$CLEANED" != 1 ] && [ "$SMOKE_DRY_RUN" != 1 ] && [ -n "${port:-}" ]; then
		CLEANED=1
		if [ "$PANEL_REPLACED" = 1 ] && [ -s "$SMOKE_TMP/orig-panel.crt" ] && [ -s "$SMOKE_TMP/orig-panel.key" ]; then
			upload_body "$SMOKE_TMP/orig-panel.crt" "$SMOKE_TMP/orig-panel.key" "$SMOKE_TMP/restore.json"
			api_file POST /api/panel/cert/upload "$SMOKE_TMP/restore.json" >/dev/null
			rm -f "$SMOKE_TMP/restore.json"
		fi
		for t_d in $D2 $D1; do
			api_call DELETE "$host" "$port" "/api/sites/$t_d/tls" >/dev/null
			api_call DELETE "$host" "$port" "/api/sites/$t_d?confirm=$t_d" >/dev/null
			vm "rm -rf /www/services/sites/$t_d $PANEL_SSL/sites/$t_d" >/dev/null 2>&1
		done
		[ "$PROVIDER_SET" = 1 ] && api_call DELETE "$host" "$port" "/api/tls/providers/$PROVIDER" >/dev/null
	fi
	smoke_cleanup
}
trap tls_cleanup EXIT INT TERM

# --- 0. preflight ------------------------------------------------------------------
step 0 "preflight: tools, panel, staging guard, public address"
assert_ok "openssl is installed on the workstation" command -v openssl
assert_ok "curl is installed on the workstation" command -v curl
assert_equal "panel.service is active (systemctl)" active "$(vm 'systemctl is-active panel' 2>/dev/null)"
port=$(vm "$PANEL info" 2>/dev/null | sed -n 's/^ *port  *\([0-9][0-9]*\).*/\1/p' | head -n 1)
[ "$SMOKE_DRY_RUN" = 1 ] && port=8443
assert_not_equal "panel info reports a port" "" "$port"

pass=${SMOKE_PASSWORD:-}
if [ -z "$pass" ]; then
	reset_out=$(vm "$PANEL reset-password" 2>&1)
	[ "$SMOKE_DRY_RUN" = 1 ] && reset_out="    DRYRUNNEWPASSWORD"
	pass=$(printf '%s\n' "$reset_out" | sed -n 's/^ *\([A-Za-z0-9]\{12,\}\) *$/\1/p' | head -n 1)
fi
assert_not_equal "an admin password is available" "" "$pass"
assert_http "login" 200 "$(api_login "$host" "$port" "$SMOKE_USER" "$pass")"

assert_http "GET /api/settings" 200 "$(api_call GET "$host" "$port" /api/settings)"
acme_dir=$(last_body | tr '}' '\n' | grep '"name":"tls.acme.directory"' | json_field value)
[ "$SMOKE_DRY_RUN" = 1 ] && acme_dir=$STAGING_DIRECTORY
log "  tls.acme.directory = ${acme_dir:-<unreadable>}"
if ! is_staging_directory "$acme_dir"; then
	if [ "$TLS_ALLOW_PRODUCTION" = 1 ]; then
		log "  WARNING: tls.acme.directory is not the staging URL; TLS_ALLOW_PRODUCTION=1 is set, so this run spends real rate limits"
	else
		log "REFUSING to run: tls.acme.directory is not $STAGING_DIRECTORY"
		log "Set it back to staging in the panel, or set TLS_ALLOW_PRODUCTION=1 to burn real Let's Encrypt rate limits."
		exit 2
	fi
fi

if command -v getent >/dev/null 2>&1 && [ "$SMOKE_DRY_RUN" != 1 ]; then
	resolved=$(getent ahostsv4 "$D1" 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' ')
	log "  $D1 resolves to: ${resolved:-<nothing>}"
	if is_ip "$host"; then
		a_in_list "$host" "$resolved" || log "  WARNING: $D1 does not resolve to $host; HTTP-01 will fail unless the VM is reached through NAT"
	fi
fi

orig_panel_serial=$(panel_serial)
[ "$SMOKE_DRY_RUN" = 1 ] && orig_panel_serial=ORIGINAL
assert_not_equal "the panel serves a certificate before the test" "" "$orig_panel_serial"
assert_equal "the cloudflare provider is not configured yet (no token on file)" "no" \
	"$(api_call GET "$host" "$port" /api/tls/providers >/dev/null; last_body | grep -q "\"$PROVIDER\"" && echo yes || echo no)"

# --- 1. a plain HTTP site -----------------------------------------------------------
step 1 "create a static site for $D1: plain HTTP serves"
assert_http "POST /api/sites ($D1)" 201 \
	"$(api_call POST "$host" "$port" /api/sites "{\"domain\":\"$D1\",\"kind\":\"static\"}")"
vm "printf '%s\n' '$MARK1' > /www/services/sites/$D1/index.html"
assert_contains "plain HTTP serves the page" "$MARK1" "$(site_http "$D1")"
assert_http "HTTPS is not served yet" 000 "$(http_status "https://$D1/" --connect-to "$D1:443:$TLS_TEST_DOMAIN:443")"
assert_equal "no certificate is served on 443 for $D1" "" "$(served "$D1")"

# --- 2. HTTP-01 ---------------------------------------------------------------------
step 2 "issue by HTTP-01 from Let's Encrypt staging: HTTPS serves, redirect is off"
job_started "POST tls/issue http01 ($D1)" \
	"$(api_call POST "$host" "$port" "/api/sites/$D1/tls/issue" "$(issue_body http01 "[\"$D1\"]")")"
job_done "the HTTP-01 job succeeded"
wait_until 60 "nginx to serve a certificate for $D1" sh -c "[ -n \"\$(printf '' | openssl s_client -connect $TLS_TEST_DOMAIN:443 -servername $D1 2>/dev/null | openssl x509 2>/dev/null)\" ]"
pem1=$(served "$D1")
info1=$(pem_info "$pem1")
log "$info1" | sed 's/^/        /'
assert_not_equal "a certificate is served for $D1" "" "$pem1"
issuer1=$(x509_field "$info1" issuer)
if [ "$SMOKE_DRY_RUN" = 1 ]; then
	log "  [dry-run] assert issuer matches $STAGING_ISSUER_RE"
elif is_staging_issuer "$issuer1"; then
	_pass "the issuer is the staging CA ($issuer1)"
else
	_fail "the issuer is the staging CA" "issuer: $issuer1" "pattern: $STAGING_ISSUER_RE"
fi
serial1=$(norm_serial "$(x509_field "$info1" serial)")
assert_contains "the certificate names $D1" "$D1" "$(pem_names "$pem1")"
assert_contains "HTTPS serves the page" "$MARK1" "$(site_https "$D1")"
assert_http "GET tls state" 200 "$(api_call GET "$host" "$port" "/api/sites/$D1/tls")"
assert_contains "the source is http01" '"source":"http01"' "$(last_body)"
assert_contains "the redirect is off by default" '"redirect_http":false' "$(last_body)"
assert_equal "plain HTTP still answers 200 with the redirect off" "200 " "$(http_probe "$D1" /)"

# --- 3. redirect --------------------------------------------------------------------
step 3 "enable the redirect: 301 to https, the ACME challenge path stays on HTTP"
assert_http "PUT tls/redirect true" 200 \
	"$(api_call PUT "$host" "$port" "/api/sites/$D1/tls/redirect" '{"redirect_http":true}')"
wait_until 30 "the redirect to apply" sh -c "curl -s -o /dev/null -w '%{http_code}' --max-time 5 --connect-to $D1:80:$TLS_TEST_DOMAIN:80 http://$D1/ | grep -q 301"
assert_equal "GET http:// is a 301 to https://$D1/" "301 https://$D1/" "$(http_probe "$D1" /)"
assert_equal "the ACME challenge path is not redirected (404 from nginx, not 301)" "404 " "$(http_probe "$D1" /.well-known/acme-challenge/x)"
assert_contains "HTTPS still serves the page" "$MARK1" "$(site_https "$D1")"
assert_contains "the state reports the redirect" '"redirect_http":true' "$(api_call GET "$host" "$port" "/api/sites/$D1/tls" >/dev/null; last_body)"

# --- 4. wildcard by DNS-01 -----------------------------------------------------------
step 4 "wildcard $WILDCARD by DNS-01 through Cloudflare, served for $D2"
(
	umask 077
	printf '{"kind":"cloudflare","credentials":{"api_token":"%s"}}' "$CF_API_TOKEN" >"$SMOKE_TMP/provider.json"
)
[ "$SMOKE_DRY_RUN" = 1 ] && : >"$SMOKE_TMP/provider.json"
PROVIDER_SET=1
assert_http_in "PUT /api/tls/providers/$PROVIDER" "200 201 204" "$(api_file PUT "/api/tls/providers/$PROVIDER" "$SMOKE_TMP/provider.json")"
rm -f "$SMOKE_TMP/provider.json"
assert_http "GET /api/tls/providers" 200 "$(api_call GET "$host" "$port" /api/tls/providers)"
if [ "$SMOKE_DRY_RUN" = 1 ]; then
	log "  [dry-run] assert the providers listing does not contain the token"
elif token_in_file "$SMOKE_TMP/last.json"; then
	_fail "the providers listing does not contain the token" "the Cloudflare token is in GET /api/tls/providers"
else
	_pass "the providers listing does not contain the token"
fi
assert_contains "the providers listing names the provider" "\"$PROVIDER\"" "$(last_body)"

assert_http "POST /api/sites ($D2)" 201 \
	"$(api_call POST "$host" "$port" /api/sites "{\"domain\":\"$D2\",\"kind\":\"static\"}")"
vm "printf '%s\n' '$MARK2' > /www/services/sites/$D2/index.html"
assert_contains "plain HTTP serves $D2" "$MARK2" "$(site_http "$D2")"
job_started "POST tls/issue dns01 ($D2, $WILDCARD)" \
	"$(api_call POST "$host" "$port" "/api/sites/$D2/tls/issue" "$(issue_body dns01 "[\"$WILDCARD\"]" "$PROVIDER")")"
job_done "the DNS-01 job succeeded"
wait_until 60 "nginx to serve a certificate for $D2" sh -c "[ -n \"\$(printf '' | openssl s_client -connect $TLS_TEST_DOMAIN:443 -servername $D2 2>/dev/null | openssl x509 2>/dev/null)\" ]"
pem2=$(served "$D2")
info2=$(pem_info "$pem2")
log "$info2" | sed 's/^/        /'
assert_not_equal "a certificate is served for $D2" "" "$pem2"
issuer2=$(x509_field "$info2" issuer)
if [ "$SMOKE_DRY_RUN" = 1 ]; then
	log "  [dry-run] assert wildcard issuer matches $STAGING_ISSUER_RE"
elif is_staging_issuer "$issuer2"; then
	_pass "the wildcard's issuer is the staging CA ($issuer2)"
else
	_fail "the wildcard's issuer is the staging CA" "issuer: $issuer2" "pattern: $STAGING_ISSUER_RE"
fi
assert_contains "the served certificate names the wildcard" "$WILDCARD" "$(pem_names "$pem2")"
assert_contains "HTTPS serves $D2" "$MARK2" "$(site_https "$D2")"
assert_http "GET /api/tls/certs" 200 "$(api_call GET "$host" "$port" /api/tls/certs)"
assert_contains "the certificate list has the wildcard" "$WILDCARD" "$(last_body)"
assert_contains "the certificate list shows the dns01 source" '"source":"dns01"' "$(last_body)"

# --- 5. forced renewal ----------------------------------------------------------------
step 5 "force a renewal of the HTTP-01 certificate: a new serial is served"
job_started "POST tls/renew ($D1)" "$(api_call POST "$host" "$port" "/api/sites/$D1/tls/renew")"
job_done "the renewal job succeeded"
wait_until 60 "the renewed certificate to be served" serial_not "$D1" "$serial1"
serial1b=$(served_serial "$D1")
assert_not_equal "the served serial changed after the renewal" "$serial1" "$serial1b"
renewed_issuer=$(x509_field "$(pem_info "$(served "$D1")")" issuer)
if [ "$SMOKE_DRY_RUN" = 1 ]; then
	log "  [dry-run] assert renewed issuer matches $STAGING_ISSUER_RE"
elif is_staging_issuer "$renewed_issuer"; then
	_pass "the renewed certificate is from the staging CA"
else
	_fail "the renewed certificate is from the staging CA" "issuer: $renewed_issuer"
fi
assert_contains "HTTPS still serves the page after the renewal" "$MARK1" "$(site_https "$D1")"

# --- 6. upload --------------------------------------------------------------------------
step 6 "upload a self-signed certificate for $D1: served, source upload, renew refused"
selfsigned "$SMOKE_TMP/site.crt" "$SMOKE_TMP/site.key" "$D1" "DNS:$D1"
up_info=$(openssl x509 -in "$SMOKE_TMP/site.crt" -noout -issuer -serial 2>/dev/null)
up_serial=$(norm_serial "$(x509_field "$up_info" serial)")
up_issuer=$(x509_field "$up_info" issuer)
[ "$SMOKE_DRY_RUN" = 1 ] && up_serial=SELFSIGNED
assert_not_equal "a self-signed certificate was generated" "" "$up_serial"
upload_body "$SMOKE_TMP/site.crt" "$SMOKE_TMP/site.key" "$SMOKE_TMP/upload.json"
assert_http_in "POST tls/upload" "200 201" "$(api_file POST "/api/sites/$D1/tls/upload" "$SMOKE_TMP/upload.json")"
rm -f "$SMOKE_TMP/upload.json" "$SMOKE_TMP/site.key"
wait_until 60 "the uploaded certificate to be served" serial_is "$D1" "$up_serial"
assert_equal "the served serial is the uploaded one" "$up_serial" "$(served_serial "$D1")"
assert_equal "the served issuer is the self-signed subject" "$up_issuer" \
	"$(x509_field "$(pem_info "$(served "$D1")")" issuer)"
assert_http "GET tls state" 200 "$(api_call GET "$host" "$port" "/api/sites/$D1/tls")"
assert_contains "the source is reported as upload" '"source":"upload"' "$(last_body)"
assert_http "renew on an uploaded certificate is refused" 409 \
	"$(api_call POST "$host" "$port" "/api/sites/$D1/tls/renew")"
assert_equal "the uploaded certificate is still served after the refused renew" "$up_serial" "$(served_serial "$D1")"

# --- 7. the panel certificate --------------------------------------------------------------
step 7 "replace the panel certificate by upload: served without a restart"
if [ "$SMOKE_DRY_RUN" != 1 ]; then
	# The original pair, kept on the workstation (0700 temp dir) to put back at the end.
	vm "cat $PANEL_SSL/panel.crt" >"$SMOKE_TMP/orig-panel.crt" 2>/dev/null
	vm "cat $PANEL_SSL/panel.key" >"$SMOKE_TMP/orig-panel.key" 2>/dev/null
	chmod 600 "$SMOKE_TMP/orig-panel.key"
fi
assert_ok "the original panel pair was copied for restoring" test -s "$SMOKE_TMP/orig-panel.crt" -a -s "$SMOKE_TMP/orig-panel.key"
pid_before=$(vm "systemctl show panel -p MainPID --value" 2>/dev/null)
ts_before=$(vm "systemctl show panel -p ActiveEnterTimestampMonotonic --value" 2>/dev/null)
[ "$SMOKE_DRY_RUN" = 1 ] && pid_before=1 && ts_before=1
assert_not_equal "panel.service has a main PID" "" "$pid_before"

if is_ip "$host"; then panel_san="IP:$host"; else panel_san="DNS:$host"; fi
selfsigned "$SMOKE_TMP/panel-new.crt" "$SMOKE_TMP/panel-new.key" "$host" "$panel_san"
new_serial=$(norm_serial "$(x509_field "$(openssl x509 -in "$SMOKE_TMP/panel-new.crt" -noout -serial 2>/dev/null)" serial)")
[ "$SMOKE_DRY_RUN" = 1 ] && new_serial=NEWPANEL
assert_not_equal "a self-signed panel certificate was generated" "" "$new_serial"
assert_not_equal "the new panel serial differs from the current one" "$orig_panel_serial" "$new_serial"
upload_body "$SMOKE_TMP/panel-new.crt" "$SMOKE_TMP/panel-new.key" "$SMOKE_TMP/panel-upload.json"
PANEL_REPLACED=1
assert_http_in "POST /api/panel/cert/upload" "200 201" "$(api_file POST /api/panel/cert/upload "$SMOKE_TMP/panel-upload.json")"
rm -f "$SMOKE_TMP/panel-upload.json" "$SMOKE_TMP/panel-new.key"
wait_until 30 "the panel listener to serve the new certificate" panel_serial_is "$new_serial"
assert_equal "openssl s_client against the panel port shows the new serial" "$new_serial" "$(panel_serial)"
assert_equal "the panel's main PID is unchanged (no restart)" "$pid_before" "$(vm "systemctl show panel -p MainPID --value" 2>/dev/null)"
assert_equal "panel.service's ActiveEnterTimestamp is unchanged (no restart)" "$ts_before" \
	"$(vm "systemctl show panel -p ActiveEnterTimestampMonotonic --value" 2>/dev/null)"
assert_http "the panel still answers on the same port" 200 "$(http_status "https://$host:$port/api/health")"
assert_http "GET /api/panel/cert" 200 "$(api_call GET "$host" "$port" /api/panel/cert)"
api_serial=$(norm_serial "$(last_body | json_field serial)")
[ "$SMOKE_DRY_RUN" = 1 ] && api_serial=$new_serial
assert_equal "the API reports the same serial as the handshake" "$new_serial" "$api_serial"

# --- 8. remove the certificates --------------------------------------------------------------
step 8 "delete the certificates: the sites are back to plain HTTP"
assert_http_in "DELETE tls ($D1)" "200 204" "$(api_call DELETE "$host" "$port" "/api/sites/$D1/tls")"
assert_http_in "DELETE tls ($D2)" "200 204" "$(api_call DELETE "$host" "$port" "/api/sites/$D2/tls")"
wait_until 60 "$D1 to stop serving HTTPS" no_https "$D1"
wait_until 60 "$D2 to stop serving HTTPS" no_https "$D2"
assert_equal "no certificate is served for $D1" "" "$(served "$D1")"
assert_equal "no certificate is served for $D2" "" "$(served "$D2")"
assert_equal "plain HTTP serves $D1 again, with no redirect" "200 " "$(http_probe "$D1" /)"
assert_contains "plain HTTP serves the page for $D1" "$MARK1" "$(site_http "$D1")"
assert_contains "plain HTTP serves the page for $D2" "$MARK2" "$(site_http "$D2")"
assert_http "GET /api/tls/certs" 200 "$(api_call GET "$host" "$port" /api/tls/certs)"
if [ "$SMOKE_DRY_RUN" != 1 ]; then
	case $(last_body) in
	*"\"$D1\""* | *"$WILDCARD"*) _fail "the certificate list no longer has these sites' certificates" "$(last_body)" ;;
	*) _pass "the certificate list no longer has these sites' certificates" ;;
	esac
fi

# --- 9. leave the VM as it was found ---------------------------------------------------------
step 9 "clean up and check: sites and provider gone, original panel certificate back, token nowhere"
restore_panel_cert() {
	# Same work as the exit trap, run now so the result can be asserted.
	if [ -s "$SMOKE_TMP/orig-panel.crt" ] && [ -s "$SMOKE_TMP/orig-panel.key" ]; then
		upload_body "$SMOKE_TMP/orig-panel.crt" "$SMOKE_TMP/orig-panel.key" "$SMOKE_TMP/restore.json"
		assert_http_in "restore the original panel certificate" "200 201" "$(api_file POST /api/panel/cert/upload "$SMOKE_TMP/restore.json")"
		rm -f "$SMOKE_TMP/restore.json"
		PANEL_REPLACED=0
	fi
}
restore_panel_cert
wait_until 30 "the original panel certificate to be served again" panel_serial_is "$orig_panel_serial"
assert_equal "the panel serves its original certificate again" "$orig_panel_serial" "$(panel_serial)"
assert_equal "the panel still was not restarted" "$pid_before" "$(vm "systemctl show panel -p MainPID --value" 2>/dev/null)"

assert_http "DELETE the site $D2" 200 "$(api_call DELETE "$host" "$port" "/api/sites/$D2?confirm=$D2")"
assert_http "DELETE the site $D1" 200 "$(api_call DELETE "$host" "$port" "/api/sites/$D1?confirm=$D1")"
vm "rm -rf /www/services/sites/$D1 /www/services/sites/$D2 $PANEL_SSL/sites/$D1 $PANEL_SSL/sites/$D2" >/dev/null 2>&1
assert_http "$D1 is gone from the panel" 404 "$(api_call GET "$host" "$port" "/api/sites/$D1")"
assert_http "$D2 is gone from the panel" 404 "$(api_call GET "$host" "$port" "/api/sites/$D2")"
assert_http_in "DELETE /api/tls/providers/$PROVIDER" "200 204" "$(api_call DELETE "$host" "$port" "/api/tls/providers/$PROVIDER")"
PROVIDER_SET=0
assert_http "GET /api/tls/providers" 200 "$(api_call GET "$host" "$port" /api/tls/providers)"
if [ "$SMOKE_DRY_RUN" != 1 ]; then
	case $(last_body) in
	*"\"$PROVIDER\""*) _fail "the provider is gone from the listing" "$(last_body)" ;;
	*) _pass "the provider is gone from the listing" ;;
	esac
	if token_in_file "$SMOKE_TMP/last.json"; then
		_fail "the providers listing does not contain the token" "the Cloudflare token is in GET /api/tls/providers"
	else
		_pass "the providers listing does not contain the token"
	fi
fi
assert_equal "nothing is served on 443 for $D1 after the cleanup" "" "$(served "$D1")"

# The panel's journal since the start of this run must not hold the token either.
if [ "$SMOKE_DRY_RUN" = 1 ]; then
	log "  [dry-run] assert the panel journal since the start of the run does not hold the token"
else
	vm "journalctl -u panel --since @$START_EPOCH --no-pager" >"$SMOKE_TMP/journal.txt" 2>/dev/null
	if token_in_file "$SMOKE_TMP/journal.txt"; then
		_fail "the panel journal does not hold the token" "the Cloudflare token is in journalctl -u panel"
	else
		_pass "the panel journal does not hold the token ($(wc -l <"$SMOKE_TMP/journal.txt") lines checked)"
	fi
fi
# The script's own output is checked for the token by the wrapper once it ends.

smoke_finish
