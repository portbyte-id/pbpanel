#!/bin/sh
# Smoke 8: the M8 exit gate, end to end, against a VM the panel is installed on.
#
#   SMOKE_HOST=root@10.0.0.5 sh scripts/smoke/8-docker-mgmt.sh
#   SMOKE_DRY_RUN=1 sh scripts/smoke/8-docker-mgmt.sh   # print the plan only
#
# Run it after smoke 2: it needs PHP installed and a PHP site that works. It
# builds PHP with intl and redis and swaps the build under a live site while a
# request loop runs; asks for a build of an extension that does not exist and
# checks that the serving build is the same one; updates the nginx build and
# rolls it back under a request loop; and checks that a version a site uses
# cannot be removed.
#
# PHP is no longer a Docker workload: it is compiled by a throwaway builder image
# and runs from a directory under /www/panel/php/<ver>/versions behind systemd.
# The PHP checks here therefore read the serving link, the master's /proc path
# and the native `php -m`; no PHP container or image is involved. The database
# container and Docker's own surfaces are still real and are exercised by the
# Docker page's checks.
#
# Environment:
#   SMOKE_HOST       user@host of the VM; the user must be root
#   SMOKE_PASSWORD   admin password. When unset the script runs
#                    `panel reset-password` on the VM, which signs everyone out.
#   SMOKE_USER       admin username (default admin)
#   SMOKE_SSH_OPTS   extra ssh options
#   SMOKE_PHP        the version the site runs on (default 8.3)
#   SMOKE_SWAP_TOLERANCE  failed requests allowed per swap, in percent (default 5)
#
# Independent oracle: the modules of the serving PHP are read with `php -m` from
# the build the `current` link names; the nginx build the master runs (/proc) and
# the one the serving link names (readlink); the request loop is curl from the
# workstation with a Host header. None of them is a panel endpoint. The panel's
# answers are checked against them.
#
# Needs port 80 and the panel port of the VM reachable from the workstation, and
# the VM able to pull from Docker Hub, to download the PHP sources and the PECL
# packages and to fetch the nginx sources (both compile inside builder containers).

set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

SMOKE_USER=${SMOKE_USER:-admin}
PHP=${SMOKE_PHP:-8.3}
TOLERANCE=${SMOKE_SWAP_TOLERANCE:-5}
SITE=smoke-docker.test
ROOT=/www/services/sites/$SITE

smoke_init
host=$(vm_host)

last_body() { cat "$SMOKE_TMP/last.json" 2>/dev/null; }

# phpinfo: the page the site serves, fetched from the workstation.
phpinfo() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then return 0; fi
	curl -s --max-time 10 -H "Host: $SITE" "http://$host/" 2>/dev/null || true
}

serves_php() {
	phpinfo | grep -q "PHP Version $PHP"
}

# modules_of VER: `php -m` of the build the version's `current` link names, with
# the scan directory the unit gives it: the binary that serves, not a copy.
modules_of() {
	vm "PHP_INI_SCAN_DIR=:/www/panel/php/$1/conf.d /www/panel/php/$1/current/bin/php -c /www/panel/php/$1/php.ini -m" 2>/dev/null
}

has_module() {
	modules_of "$1" | grep -qx "$2"
}

# php_build_id VER: the id of the build the version's serving link names.
php_build_id() { vm "basename \"\$(readlink /www/panel/php/$1/current)\"" 2>/dev/null; }

# php_running_build VER: the id of the build the FPM master runs, from its
# executable's path under /proc (.../versions/<id>/sbin/php-fpm).
php_running_build() {
	vm "p=\$(systemctl show -p MainPID --value panel-php-$1.service); basename \"\$(dirname \"\$(dirname \"\$(readlink /proc/\$p/exe)\")\")\"" 2>/dev/null
}

# php_build_changed VER OLD: the master runs a build other than OLD.
php_build_changed() { _pb=$(php_running_build "$1"); [ -n "$_pb" ] && [ "$_pb" != "$2" ]; }

# nginx runs natively: its build is a directory under versions/, and the serving
# link names it. nginx_build_id is what the link names; nginx_running_build is
# what the master runs, read from its executable's path under /proc.
nginx_build_id() { vm 'basename "$(readlink /www/panel/nginx/current)"' 2>/dev/null; }
nginx_running_build() {
	vm 'p=$(systemctl show -p MainPID --value panel-nginx.service); basename "$(dirname "$(dirname "$(readlink /proc/$p/exe)")")"' 2>/dev/null
}
nginx_unit_active() { [ "$(vm 'systemctl is-active panel-nginx.service' 2>/dev/null)" = active ]; }
nginx_master_on_link() { [ "$(nginx_build_id)" = "$(nginx_running_build)" ]; }

# nginx_image_differs OLD: the serving link names a build other than OLD.
nginx_image_differs() { [ "$(nginx_build_id)" != "$1" ]; }
nginx_image_is() { [ "$(nginx_build_id)" = "$1" ]; }

# current_id_php VER: the id of the PHP build the panel reports as current. The
# builds list is one object per build; the current one says "current":true and
# carries its id in the same object.
current_id_php() {
	api_call GET "$host" "$port" "/api/php/$1/builds" >/dev/null
	last_body | tr '}' '\n' | grep '"current":true' | sed -n 's/.*"id":"\([^"]*\)".*/\1/p' | head -n 1
}

# current_id_nginx: the id of the nginx build the panel reports as current. The id
# is the build's directory name under versions/, the name the link holds.
current_id_nginx() {
	api_call GET "$host" "$port" /api/nginx/image >/dev/null
	last_body | sed -n 's/.*"current":{[^}]*"id":"\([^"]*\)".*/\1/p' | head -n 1
}

# bogus_failed: a build of the bogus extension is recorded as failed.
bogus_failed() {
	api_call GET "$host" "$port" "/api/php/$PHP/builds" >/dev/null
	last_body | grep -q '"state":"failed"' && last_body | grep -q 'm8bogus'
}

# traffic_start OUT: a background loop of requests through the site, one status
# code per line in OUT. The loop's pid is kept in OUT.pid.
traffic_start() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then return 0; fi
	(
		while :; do
			curl -s -o /dev/null -w '%{http_code}\n' --max-time 5 -H "Host: $SITE" "http://$host/" 2>/dev/null || echo 000
			sleep 0.2
		done
	) >"$1" &
	echo $! >"$1.pid"
}

# traffic_failures OUT: "failed total" for the requests in OUT, after stopping the loop.
traffic_failures() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then echo "0 0"; return 0; fi
	kill "$(cat "$1.pid")" 2>/dev/null
	total=$(wc -l <"$1" | tr -d ' ')
	bad=$(grep -vc '^200$' "$1" || true)
	echo "$bad $total"
}

# tolerated FAILED TOTAL: the request loop lost no more than the allowed share.
tolerated() {
	allowed=$(($2 * TOLERANCE / 100))
	[ "$SMOKE_DRY_RUN" = 1 ] || [ "$1" -le "$allowed" ]
}

# --- 1. the panel, and the PHP site ------------------------------------------
step 1 "the panel is up, PHP $PHP serves a site"
assert_equal "panel.service is active (systemctl)" active "$(vm 'systemctl is-active panel' 2>/dev/null)"
port=$(vm "/www/panel/bin/panel info" 2>/dev/null | sed -n 's/^ *port  *\([0-9][0-9]*\).*/\1/p' | head -n 1)
[ "$SMOKE_DRY_RUN" = 1 ] && port=8443
assert_not_equal "panel info reports a port" "" "$port"

pass=${SMOKE_PASSWORD:-}
if [ -z "$pass" ]; then
	reset_out=$(vm "/www/panel/bin/panel reset-password" 2>&1)
	[ "$SMOKE_DRY_RUN" = 1 ] && reset_out="    DRYRUNNEWPASSWORD"
	pass=$(printf '%s\n' "$reset_out" | sed -n 's/^ *\([A-Za-z0-9]\{12,\}\) *$/\1/p' | head -n 1)
fi
assert_not_equal "an admin password is available" "" "$pass"
assert_http "login" 200 "$(api_login "$host" "$port" "$SMOKE_USER" "$pass")"
# The first start compiles nginx in a builder container, which takes several
# minutes on a VM that has not built one yet.
wait_until 900 "the nginx unit to run" nginx_unit_active
assert_equal "the master runs the build the serving link names" "$(nginx_build_id)" "$(nginx_running_build)"

assert_http "the catalogue lists PHP $PHP" 200 "$(api_call GET "$host" "$port" /api/php)"
assert_contains "PHP $PHP is installed" '"state":"ready"' "$(last_body)"

assert_http "POST /api/sites (a PHP site on $PHP)" 201 \
	"$(api_call POST "$host" "$port" /api/sites "{\"domain\":\"$SITE\",\"kind\":\"php\",\"php_version\":\"$PHP\",\"phpinfo\":true}")"
wait_until 60 "phpinfo() to answer on $PHP" serves_php
assert_equal "the site serves PHP $PHP" 0 "$(serves_php >/dev/null 2>&1; echo $?)"
assert_equal "the master runs the build the serving link names" "$(php_build_id "$PHP")" "$(php_running_build "$PHP")"

# --- 2. build with intl and redis, swap under a live site --------------------
step 2 "build PHP $PHP with intl and redis and swap it under the live site"
before_id=$(php_running_build "$PHP")
assert_http "GET /api/php/extensions" 200 "$(api_call GET "$host" "$port" /api/php/extensions)"
assert_contains "intl is in the catalogue" '"name":"intl"' "$(last_body)"
assert_contains "redis is in the catalogue" '"name":"redis"' "$(last_body)"

traffic_start "$SMOKE_TMP/swap1.txt"
assert_http "POST /api/php/$PHP/build starts a job" 202 \
	"$(api_call POST "$host" "$port" "/api/php/$PHP/build" '{"extensions":["intl","redis"]}')"
assert_contains "the job is a php.build" '"kind":"php.build"' "$(last_body)"
# A build compiles the core again in a builder image: six minutes, more with
# extensions, so the waits here are generous.
wait_until 1800 "intl to be loaded by PHP $PHP" has_module "$PHP" intl
wait_until 300 "redis to be loaded by PHP $PHP" has_module "$PHP" redis
# The modules may already be there from an earlier run, so the module checks can
# pass before the job has swapped anything: wait for the swap itself.
wait_until 1800 "the swap to restart the master onto another build" php_build_changed "$PHP" "$before_id"
set -- $(traffic_failures "$SMOKE_TMP/swap1.txt")
if tolerated "$1" "$2"; then
	_pass "the site answered through the swap: $1 of $2 requests failed"
else
	_fail "the site answered through the swap: $1 of $2 requests failed (tolerance $TOLERANCE%)"
fi
assert_equal "the unit is active after the swap" active "$(vm "systemctl is-active panel-php-$PHP.service" 2>/dev/null)"
assert_equal "the master runs the build the serving link names after the swap" \
	"$(php_build_id "$PHP")" "$(php_running_build "$PHP")"
assert_equal "phpinfo() still reports PHP $PHP" 0 "$(serves_php >/dev/null 2>&1; echo $?)"
assert_contains "the modules include intl" intl "$(modules_of "$PHP")"
assert_contains "the modules include redis" redis "$(modules_of "$PHP")"
assert_equal "the master runs the build the panel reports as current" "$(current_id_php "$PHP")" "$(php_running_build "$PHP")"

# --- 3. a build that cannot be made leaves the serving build alone -----------
step 3 "a build of an extension that does not exist fails; the serving build is the same"
serving_id=$(php_running_build "$PHP")
assert_http "POST /api/php/$PHP/build with m8bogus starts a job" 202 \
	"$(api_call POST "$host" "$port" "/api/php/$PHP/build" '{"extensions":["m8bogus"]}')"
wait_until 900 "the bogus build to be recorded as failed" bogus_failed
assert_equal "the serving build is the same one" "$serving_id" "$(php_running_build "$PHP")"
assert_equal "the serving link still names it" "$serving_id" "$(php_build_id "$PHP")"
assert_equal "the serving unit still runs" active "$(vm "systemctl is-active panel-php-$PHP.service" 2>/dev/null)"
assert_equal "phpinfo() still answers PHP $PHP" 0 "$(serves_php >/dev/null 2>&1; echo $?)"

# --- 4. update the nginx image, roll it back, under a live site ---------------
step 4 "update the nginx image and roll it back"
nginx_before=$(current_id_nginx)
traffic_start "$SMOKE_TMP/nginx.txt"
assert_http "POST /api/nginx/image/update starts a job" 202 \
	"$(api_call POST "$host" "$port" /api/nginx/image/update '{}')"
assert_contains "the job is an nginx.update" '"kind":"nginx.update"' "$(last_body)"
# A native build compiles nginx from source in a builder container: minutes, not seconds.
wait_until 1500 "the serving link to name a new nginx build" nginx_image_differs "$nginx_before"
wait_until 120 "the master to run the build the link names" nginx_master_on_link
assert_equal "nginx runs the build the panel reports as current" "$(current_id_nginx)" "$(nginx_running_build)"

api_call GET "$host" "$port" /api/nginx/image >/dev/null
prev_id=$(last_body | sed -n 's/.*"previous":{[^}]*"id":"\([^"]*\)".*/\1/p' | head -n 1)
assert_not_equal "a previous nginx build is kept for rollback" "" "$prev_id"
assert_http "POST activate the previous nginx build" 202 \
	"$(api_call POST "$host" "$port" "/api/nginx/image/builds/$prev_id/activate" '{}')"
wait_until 300 "the serving link to name the previous nginx build" nginx_image_is "$nginx_before"
wait_until 120 "the master to run the previous build again" nginx_master_on_link
assert_equal "nginx runs the previous build again" "$nginx_before" "$(nginx_running_build)"
set -- $(traffic_failures "$SMOKE_TMP/nginx.txt")
if tolerated "$1" "$2"; then
	_pass "the site answered through the update and the rollback: $1 of $2 requests failed"
else
	_fail "the site answered through the update and the rollback: $1 of $2 requests failed (tolerance $TOLERANCE%)"
fi

# --- 5. a version a site uses cannot be removed -------------------------------
step 5 "removing a PHP version a site uses is refused"
assert_http "DELETE /api/php/$PHP while a site uses it is refused" 409 \
	"$(api_call DELETE "$host" "$port" "/api/php/$PHP")"
assert_equal "the unit still runs after the refusal" active "$(vm "systemctl is-active panel-php-$PHP.service" 2>/dev/null)"

# --- 6. clean up -------------------------------------------------------------
step 6 "remove the smoke site"
assert_http "DELETE the smoke site" 200 \
	"$(api_call DELETE "$host" "$port" "/api/sites/$SITE?confirm=$SITE")"
vm "rm -rf $ROOT" >/dev/null 2>&1 || true

smoke_finish
