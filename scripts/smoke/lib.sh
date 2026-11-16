#!/bin/sh
# Shared library for the smoke scripts. Source it; do not execute it.
#
# POSIX sh. The only non-POSIX tools are the ones the scripts name on purpose:
# ssh, scp and curl on the workstation, systemctl and ss on the VM.
#
# Configuration (environment):
#   SMOKE_HOST       user@host of the VM (required unless SMOKE_DRY_RUN=1)
#   SMOKE_SSH_OPTS   extra ssh/scp options, e.g. "-i ~/.ssh/key"
#   SMOKE_DRY_RUN=1  print the plan, touch nothing
#
# Independent oracle: "is it serving" is always answered by curl from the
# workstation and by systemctl/ss on the VM, never by a panel status endpoint.

SMOKE_DRY_RUN=${SMOKE_DRY_RUN:-0}
SMOKE_SSH_OPTS=${SMOKE_SSH_OPTS:-}
SMOKE_FAILURES=0
SMOKE_CHECKS=0
SMOKE_TMP=

# ssh options: no prompts, no host-key pollution (the VM is rebuilt often).
_ssh_base="-o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=10"

log() { printf '%s\n' "$*"; }
step() { SMOKE_STEP=$1; shift; printf '\n== %s: %s\n' "$SMOKE_STEP" "$*"; }

smoke_init() {
	if [ "$SMOKE_DRY_RUN" != 1 ] && [ -z "${SMOKE_HOST:-}" ]; then
		log "SMOKE_HOST is required (user@host of an Ubuntu VM), or set SMOKE_DRY_RUN=1"
		exit 2
	fi
	SMOKE_TMP=$(mktemp -d "${TMPDIR:-/tmp}/smoke.XXXXXX") || exit 2
	trap smoke_cleanup EXIT INT TERM
	COOKIE_JAR=$SMOKE_TMP/cookies
	: > "$COOKIE_JAR"
}

# smoke_cleanup removes the workstation temp dir. It never touches the VM: the
# VM is disposable and a failed run is easier to debug if it is left as it was.
smoke_cleanup() {
	[ -n "$SMOKE_TMP" ] && rm -rf "$SMOKE_TMP"
}

# vm CMD...: run a command on the VM as the SSH user. The command is one string
# interpreted by the VM's shell; callers pass constants, never untrusted input.
vm() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then
		log "  [dry-run] ssh \$SMOKE_HOST: $*"
		return 0
	fi
	# shellcheck disable=SC2086 # word splitting of the option strings is intended
	ssh $_ssh_base $SMOKE_SSH_OPTS "$SMOKE_HOST" "$@"
}

# vm_copy LOCAL REMOTE
vm_copy() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then
		log "  [dry-run] scp $1 \$SMOKE_HOST:$2"
		return 0
	fi
	# shellcheck disable=SC2086
	scp $_ssh_base $SMOKE_SSH_OPTS "$1" "$SMOKE_HOST:$2"
}

# vm_host prints the host part of SMOKE_HOST (what curl dials).
vm_host() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then
		printf '%s' "vm.example"
		return
	fi
	printf '%s' "${SMOKE_HOST#*@}"
}

# ---- assertions -------------------------------------------------------------
# Every assertion prints what it checked and, on failure, the actual value.

_pass() { SMOKE_CHECKS=$((SMOKE_CHECKS + 1)); log "  ok    $1"; }
_fail() {
	SMOKE_CHECKS=$((SMOKE_CHECKS + 1))
	SMOKE_FAILURES=$((SMOKE_FAILURES + 1))
	log "  FAIL  $1"
	shift
	for line in "$@"; do log "        $line"; done
}

# assert_equal DESCRIPTION EXPECTED ACTUAL
assert_equal() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then log "  [dry-run] assert $1 == '$2'"; return 0; fi
	if [ "$2" = "$3" ]; then _pass "$1"; else _fail "$1" "expected: $2" "actual:   $3"; fi
}

# assert_not_equal DESCRIPTION UNWANTED ACTUAL
assert_not_equal() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then log "  [dry-run] assert $1 != '$2'"; return 0; fi
	if [ "$2" != "$3" ]; then _pass "$1"; else _fail "$1" "must not be: $2" "actual:      $3"; fi
}

# assert_contains DESCRIPTION NEEDLE HAYSTACK
assert_contains() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then log "  [dry-run] assert $1 contains '$2'"; return 0; fi
	case $3 in
	*"$2"*) _pass "$1" ;;
	*) _fail "$1" "wanted substring: $2" "actual:           $3" ;;
	esac
}

# assert_http DESCRIPTION EXPECTED_STATUS ACTUAL_STATUS
assert_http() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then log "  [dry-run] assert $1 -> HTTP $2"; return 0; fi
	if [ "$2" = "$3" ]; then _pass "$1 (HTTP $3)"; else _fail "$1" "expected HTTP $2" "actual HTTP   $3"; fi
}

# assert_ok DESCRIPTION CMD...: the command must exit 0.
assert_ok() {
	_d=$1
	shift
	if [ "$SMOKE_DRY_RUN" = 1 ]; then log "  [dry-run] assert $_d: $*"; return 0; fi
	_out=$("$@" 2>&1) && _pass "$_d" || _fail "$_d" "command: $*" "output:  $_out"
}

# ---- HTTP (curl from the workstation) ---------------------------------------
# -k accepts the panel's self-signed certificate. It is for this test only.

# http_status URL [curl args...]: prints the HTTP status, or 000 if the
# connection failed, so "port closed" is an ordinary value to assert on.
http_status() {
	_url=$1
	shift
	if [ "$SMOKE_DRY_RUN" = 1 ]; then printf '000'; return 0; fi
	curl -sk -o /dev/null -w '%{http_code}' --max-time 10 "$@" "$_url" 2>/dev/null || true
}

# api_login HOST PORT USER PASSWORD: log in, store the cookie in $COOKIE_JAR and
# the CSRF token in $CSRF. Prints the HTTP status of the login.
api_login() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then
		CSRF=dry-run-csrf
		printf '200'
		return 0
	fi
	: > "$COOKIE_JAR"
	_body=$SMOKE_TMP/login.json
	_code=$(curl -sk --max-time 15 -c "$COOKIE_JAR" -o "$_body" -w '%{http_code}' \
		-H 'Content-Type: application/json' \
		-d "{\"username\":\"$3\",\"password\":\"$4\"}" \
		"https://$1:$2/api/login" 2>/dev/null || true)
	CSRF=$(sed -n 's/.*"csrf_token":"\([^"]*\)".*/\1/p' "$_body" 2>/dev/null)
	printf '%s' "$_code"
}

# api_call METHOD HOST PORT PATH [JSON_BODY]: an authenticated call using the
# cookie jar and CSRF token from api_login. Prints the HTTP status.
api_call() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then printf '200'; return 0; fi
	if [ -n "${5:-}" ]; then
		curl -sk --max-time 15 -b "$COOKIE_JAR" -o "$SMOKE_TMP/last.json" -w '%{http_code}' \
			-X "$1" -H 'Content-Type: application/json' -H "X-CSRF-Token: $CSRF" \
			-d "$5" "https://$2:$3$4" 2>/dev/null || true
	else
		curl -sk --max-time 15 -b "$COOKIE_JAR" -o "$SMOKE_TMP/last.json" -w '%{http_code}' \
			-X "$1" -H "X-CSRF-Token: $CSRF" "https://$2:$3$4" 2>/dev/null || true
	fi
}

# wait_until SECONDS DESCRIPTION CMD...: poll CMD once a second until it exits 0.
wait_until() {
	_secs=$1
	_desc=$2
	shift 2
	if [ "$SMOKE_DRY_RUN" = 1 ]; then log "  [dry-run] wait up to ${_secs}s for $_desc"; return 0; fi
	_i=0
	while [ "$_i" -lt "$_secs" ]; do
		if "$@" >/dev/null 2>&1; then return 0; fi
		_i=$((_i + 1))
		sleep 1
	done
	log "  timed out after ${_secs}s waiting for $_desc"
	return 1
}

# smoke_finish: print the summary and exit non-zero if anything failed.
smoke_finish() {
	log ""
	if [ "$SMOKE_DRY_RUN" = 1 ]; then
		log "dry run complete: nothing was executed"
		exit 0
	fi
	if [ "$SMOKE_FAILURES" -gt 0 ]; then
		log "SMOKE FAILED: $SMOKE_FAILURES of $SMOKE_CHECKS checks failed"
		exit 1
	fi
	log "SMOKE PASSED: $SMOKE_CHECKS checks"
	exit 0
}
