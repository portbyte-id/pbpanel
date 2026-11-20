#!/bin/sh
# Smoke 2: the M2 exit gate, end to end, against a VM the panel is installed on.
#
#   SMOKE_HOST=root@10.0.0.5 sh scripts/smoke/2-php.sh
#   SMOKE_DRY_RUN=1 sh scripts/smoke/2-php.sh       # print the plan only
#
# Run it after smoke 0 (or any `panel install`). It installs two PHP versions,
# creates a PHP site that prints phpinfo(), changes memory_limit in the form,
# breaks and restores php.ini, switches the site to the other version, stops the
# version's systemd unit by hand, restarts the daemon and reboots the VM. It
# deletes the site at the end and leaves the two builds and their units in place.
#
# PHP runs natively: each version is a build under /www/panel/php/<ver>/versions
# behind the `current` link, served by panel-php-<ver>.service. Docker only
# compiles, so nothing here looks for a panel-php-<ver> container.
#
# Environment:
#   SMOKE_HOST       user@host of the VM; the user must be root
#   SMOKE_PASSWORD   admin password. When unset the script runs
#                    `panel reset-password` on the VM, which signs everyone out.
#   SMOKE_USER       admin username (default admin)
#   SMOKE_SSH_OPTS   extra ssh options
#   SMOKE_PHP_A      the version the site starts on (default 8.3)
#   SMOKE_PHP_B      the version it is switched to (default 8.4)
#   SMOKE_SKIP_REBOOT=1  skip the reboot step
#
# An install compiles PHP from source in a builder image: six minutes per
# version, more with a large extension list. The waits below allow for it.
#
# Independent oracle: "which PHP serves the site" and "what is memory_limit"
# are read from phpinfo() fetched by curl from the workstation with a Host
# header; "is the version up" is `systemctl is-active` on the VM; "is the file
# unchanged" is sha256sum on the VM; "does anything listen on TCP 9000" is `ss`
# on the VM. None of them is a panel endpoint.
#
# Needs port 80 of the VM reachable from the workstation, and the VM able to pull
# from Docker Hub (the builder image), to download the PHP sources and to reach
# PECL.

set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

SMOKE_USER=${SMOKE_USER:-admin}
SMOKE_SKIP_REBOOT=${SMOKE_SKIP_REBOOT:-0}
A=${SMOKE_PHP_A:-8.3}
B=${SMOKE_PHP_B:-8.4}
PANEL=/www/panel/bin/pbp
SITE=smoke-php.test
POOL=smoke_php_test
ROOT=/www/services/sites/$SITE
INI=/www/panel/php/$A/php.ini
INI_KEY="php%2F$A%2Fphp.ini"

smoke_init
host=$(vm_host)

last_body() { cat "$SMOKE_TMP/last.json" 2>/dev/null; }

# phpinfo: the page the site serves, fetched from the workstation.
phpinfo() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then return 0; fi
	curl -s --max-time 10 -H "Host: $SITE" "http://$host/" 2>/dev/null || true
}

# served_version: the `major.minor` phpinfo() reports, empty when it does not answer.
served_version() {
	phpinfo | sed -n 's/.*PHP Version \([0-9][0-9]*\.[0-9][0-9]*\).*/\1/p' | head -n 1
}

serves() { [ "$(served_version)" = "$1" ]; }

# version_state VER: the catalogue state of a version, from GET /api/php.
version_state() {
	api_call GET "$host" "$port" /api/php >/dev/null
	last_body | sed -n 's/.*"version":"'"$1"'","image":"[^"]*","digest":"[^"]*","state":"\([a-z]*\)".*/\1/p'
}

version_ready() { [ "$(version_state "$1")" = ready ]; }

# unit_active VER: the version's FPM master runs under systemd.
unit_active() {
	[ "$(vm "systemctl is-active panel-php-$1.service" 2>/dev/null)" = active ]
}

# build_present VER: the build the version's `current` link names holds an FPM
# master. A native build is a directory, not an image.
build_present() {
	[ "$(vm "test -x /www/panel/php/$1/current/sbin/php-fpm && echo yes" 2>/dev/null)" = yes ]
}

# pool_workers_are_www POOL: every FPM worker of the pool runs as www. The
# master stays root; each pool drops to the www account.
pool_workers_are_www() {
	[ "$(vm "ps -eo user=,args= | grep 'php-fpm: pool $1' | grep -v grep | awk '{print \$1}' | sort -u" 2>/dev/null)" = www ]
}

nginx_running() {
	[ "$(vm 'systemctl is-active panel-nginx' 2>/dev/null)" = active ]
}

panel_healthy() {
	[ "$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 "https://$host:$port/api/health")" = 200 ]
}

# --- 1. the panel, and nginx under it -----------------------------------------
step 1 "the panel is up, nginx is running"
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
wait_until 300 "the nginx unit to run" nginx_running

# --- 2. the catalogue and the installs ------------------------------------------
step 2 "install PHP $A and PHP $B"
assert_http "GET /api/php" 200 "$(api_call GET "$host" "$port" /api/php)"
assert_contains "PHP $A is offered" "\"version\":\"$A\"" "$(last_body)"
assert_contains "PHP $B is offered" "\"version\":\"$B\"" "$(last_body)"
assert_http "an unknown version cannot be pulled" 404 "$(api_call POST "$host" "$port" /api/php/5.6/pull)"

for v in "$A" "$B"; do
	if [ "$SMOKE_DRY_RUN" != 1 ] && version_ready "$v"; then
		log "  PHP $v is already installed"
		continue
	fi
	assert_http "POST /api/php/$v/pull starts a job" 202 "$(api_call POST "$host" "$port" "/api/php/$v/pull")"
	assert_contains "the job is a php.pull" '"kind":"php.pull"' "$(last_body)"
	# A first install compiles the version: about six minutes on a 4 vCPU VM.
	wait_until 1800 "PHP $v to be ready" version_ready "$v"
	[ "$SMOKE_DRY_RUN" = 1 ] || assert_equal "PHP $v is ready in the catalogue" ready "$(version_state "$v")"
done
for v in "$A" "$B"; do
	assert_ok "the build of PHP $v is on disk (test -x current/sbin/php-fpm)" build_present "$v"
	assert_equal "the www account is on the host at uid 1500 (getent)" 1500 \
		"$(vm "getent passwd www | cut -d: -f3" 2>/dev/null)"
done

# --- 3. a PHP site on version A -----------------------------------------------
step 3 "create a PHP site on $A: phpinfo() is served through nginx"
assert_http "a PHP site on a version that is not installed is refused" 422 \
	"$(api_call POST "$host" "$port" /api/sites "{\"domain\":\"$SITE\",\"kind\":\"php\",\"php_version\":\"5.6\"}")"
assert_http "POST /api/sites (php, with the phpinfo page)" 201 \
	"$(api_call POST "$host" "$port" /api/sites "{\"domain\":\"$SITE\",\"kind\":\"php\",\"php_version\":\"$A\",\"phpinfo\":true}")"
wait_until 60 "phpinfo() to answer on $A" serves "$A"
assert_equal "phpinfo() reports PHP $A" "$A" "$(served_version)"
assert_contains "the request went through FPM" "FPM/FastCGI" "$(phpinfo)"

# The version's unit is up: the first site of a version starts it.
assert_equal "systemd reports panel-php-$A active" active "$(vm "systemctl is-active panel-php-$A.service" 2>/dev/null)"
assert_equal "nothing listens on TCP 9000 (ss on the VM)" 0 "$(vm "ss -ltnH 'sport = :9000' | wc -l" 2>/dev/null)"
assert_equal "the pool socket belongs to www, mode 660 (stat)" "www:www 660" \
	"$(vm "stat -c '%U:%G %a' /www/panel/run/php-$A-$POOL.sock" 2>/dev/null)"
assert_equal "the web root belongs to www (stat)" www "$(vm "stat -c %U $ROOT" 2>/dev/null)"
assert_ok "pool workers run as www (ps on the VM)" pool_workers_are_www "$POOL"
assert_equal "the master is root (ps on the VM)" root \
	"$(vm "ps -eo user=,args= | grep 'php-fpm: master process (/www/panel/php/$A/php-fpm.conf)' | grep -v grep | awk '{print \$1}' | sort -u" 2>/dev/null)"

# --- 4. memory_limit from the form --------------------------------------------
step 4 "change memory_limit in the form: phpinfo() shows the new value"
assert_http "an invalid value is refused" 422 \
	"$(api_call PUT "$host" "$port" "/api/php/$A/settings" '{"memory_limit":"lots","upload_max_filesize":"64M","post_max_size":"64M","max_execution_time":60,"date_timezone":"UTC"}')"
assert_contains "the refusal names memory_limit" '"field":"memory_limit"' "$(last_body)"
assert_http "PUT /api/php/$A/settings" 200 \
	"$(api_call PUT "$host" "$port" "/api/php/$A/settings" '{"memory_limit":"333M","upload_max_filesize":"64M","post_max_size":"64M","max_execution_time":60,"date_timezone":"UTC"}')"
assert_contains "zz-panel.ini carries the value (cat on the VM)" "memory_limit = 333M" \
	"$(vm "cat /www/panel/php/$A/conf.d/zz-panel.ini" 2>/dev/null)"
wait_until 30 "phpinfo() to show 333M" sh -c "curl -s --max-time 5 -H 'Host: $SITE' http://$host/ | grep -q 333M"
assert_contains "phpinfo() shows memory_limit 333M" "333M" "$(phpinfo)"

# --- 5. the pool form ---------------------------------------------------------
step 5 "change the pool: written to the pool file, site still served"
assert_http "GET the pool" 200 "$(api_call GET "$host" "$port" "/api/sites/$SITE/pool")"
assert_contains "the pool is named after the domain" "\"name\":\"$POOL\"" "$(last_body)"
assert_http "an impossible pool is refused" 422 \
	"$(api_call PUT "$host" "$port" "/api/sites/$SITE/pool" '{"pm":"dynamic","max_children":2,"start_servers":9,"min_spare_servers":1,"max_spare_servers":2,"max_requests":0,"idle_timeout":10}')"
assert_http "PUT the pool" 200 \
	"$(api_call PUT "$host" "$port" "/api/sites/$SITE/pool" '{"pm":"ondemand","max_children":4,"start_servers":2,"min_spare_servers":1,"max_spare_servers":3,"max_requests":200,"idle_timeout":15}')"
assert_contains "the pool file carries the mode (cat on the VM)" "pm = ondemand" \
	"$(vm "cat /www/panel/php/$A/pool.d/$POOL.conf" 2>/dev/null)"
wait_until 30 "the site to answer after the pool change" serves "$A"
assert_equal "phpinfo() still reports PHP $A" "$A" "$(served_version)"

# --- 6. an invalid raw php.ini ------------------------------------------------
step 6 "write an invalid php.ini: rejected, previous file restored, site still serves"
before=$(vm "sha256sum < $INI" 2>/dev/null)
assert_not_equal "php.ini exists on the VM" "" "$before"
assert_http "PUT a php.ini with a syntax error" 422 \
	"$(api_call PUT "$host" "$port" "/api/php/$A/ini" '{"content":"memory_limit = = =\n"}')"
assert_contains "the error class is config_rejected" '"error":"config_rejected"' "$(last_body)"
assert_contains "the response carries PHP's own message" 'syntax error' "$(last_body)"
assert_equal "php.ini is byte-for-byte what it was (sha256sum)" "$before" "$(vm "sha256sum < $INI" 2>/dev/null)"
assert_http "PUT a php.ini that loads an extension that does not exist" 422 \
	"$(api_call PUT "$host" "$port" "/api/php/$A/ini" '{"content":"extension=no_such_extension.so\n"}')"
assert_equal "php.ini is still what it was (sha256sum)" "$before" "$(vm "sha256sum < $INI" 2>/dev/null)"
assert_equal "panel-php-$A is still active (systemctl)" active "$(vm "systemctl is-active panel-php-$A.service" 2>/dev/null)"
assert_equal "the site still serves on PHP $A" "$A" "$(served_version)"

# A valid edit goes live and the history can put the original back.
assert_http "PUT a valid php.ini" 200 \
	"$(api_call PUT "$host" "$port" "/api/php/$A/ini" '{"content":"; smoke 2\nexpose_php = Off\n"}')"
assert_contains "the edit is on disk (cat on the VM)" "; smoke 2" "$(vm "cat $INI" 2>/dev/null)"
assert_http "the history of php.ini is listed" 200 "$(api_call GET "$host" "$port" "/api/config-history/$INI_KEY")"
assert_contains "the history has the edit" "edit php.ini $A" "$(last_body)"
assert_http "restore version 1" 200 "$(api_call POST "$host" "$port" "/api/config-history/$INI_KEY/1/restore")"
assert_equal "php.ini is the original again (sha256sum)" "$before" "$(vm "sha256sum < $INI" 2>/dev/null)"
assert_equal "the site serves after the restore" "$A" "$(served_version)"

# --- 7. the version switch ----------------------------------------------------
step 7 "switch the site to $B: PHP_VERSION changes"
assert_http "switching to the version it already runs on is refused" 422 \
	"$(api_call PUT "$host" "$port" "/api/sites/$SITE/php-version" "{\"version\":\"$A\"}")"
assert_http "PUT /api/sites/$SITE/php-version" 200 \
	"$(api_call PUT "$host" "$port" "/api/sites/$SITE/php-version" "{\"version\":\"$B\"}")"
wait_until 60 "phpinfo() to answer on $B" serves "$B"
assert_equal "phpinfo() reports PHP $B" "$B" "$(served_version)"
assert_equal "the old pool file is gone (test -e)" gone \
	"$(vm "test -e /www/panel/php/$A/pool.d/$POOL.conf && echo present || echo gone" 2>/dev/null)"
assert_contains "the pool moved with its settings (cat on the VM)" "pm = ondemand" \
	"$(vm "cat /www/panel/php/$B/pool.d/$POOL.conf" 2>/dev/null)"
# The pool socket is in the site file's "# >>> pb:php" block, the layout a site
# created today gets (a site made before blocks still has includes/<domain>/php.conf).
assert_contains "the site's php block points at the new socket (cat on the VM)" "fastcgi_pass unix:/www/panel/run/php-$B-$POOL.sock" \
	"$(vm "cat /www/panel/nginx/sites/$SITE.conf" 2>/dev/null)"
assert_equal "PHP $A keeps running without a site (systemctl)" active \
	"$(vm "systemctl is-active panel-php-$A.service" 2>/dev/null)"
assert_equal "nothing listens on TCP 9000 (ss on the VM)" 0 "$(vm "ss -ltnH 'sport = :9000' | wc -l" 2>/dev/null)"

# --- 8. the unit stopped by hand -----------------------------------------------
step 8 "stop the PHP $B unit by hand: a daemon restart brings it back"
vm "systemctl stop panel-php-$B.service"
assert_not_equal "the site is down while the unit is stopped" "$B" "$(served_version)"
vm "systemctl restart panel"
wait_until 60 "the panel to answer" panel_healthy
wait_until 120 "panel-php-$B to run again" unit_active "$B"
wait_until 60 "phpinfo() to answer on $B" serves "$B"
assert_equal "phpinfo() reports PHP $B after the daemon restart" "$B" "$(served_version)"
assert_http "login after the restart" 200 "$(api_login "$host" "$port" "$SMOKE_USER" "$pass")"

# --- 9. reboot ------------------------------------------------------------------
step 9 "reboot: the panel and the PHP unit come back enabled and serving"
if [ "$SMOKE_SKIP_REBOOT" = 1 ]; then
	log "  skipped (SMOKE_SKIP_REBOOT=1)"
else
	if [ "$SMOKE_DRY_RUN" = 1 ]; then
		log "  [dry-run] ssh \$SMOKE_HOST: reboot; wait for ssh down, then up"
	else
		vm 'systemctl reboot' >/dev/null 2>&1 || true
		wait_until 60 "the VM to go down" sh -c "! ssh $_ssh_base $SMOKE_SSH_OPTS -o ConnectTimeout=3 $SMOKE_HOST true"
	fi
	wait_until 300 "ssh to come back" vm true
	# The panel comes up on its own schedule after ssh does, so wait for it
	# before asking systemd about it.
	wait_until 120 "the panel to answer" panel_healthy
	# A unit that is enabled but did not come back active after a boot is the
	# failure this step exists to catch.
	assert_equal "panel.service is enabled for the next boot (systemctl)" enabled \
		"$(vm 'systemctl is-enabled panel' 2>/dev/null)"
	assert_equal "panel.service is active after the reboot (systemctl)" active \
		"$(vm 'systemctl is-active panel' 2>/dev/null)"
	assert_equal "panel-php-$B.service is enabled for the next boot (systemctl)" enabled \
		"$(vm "systemctl is-enabled panel-php-$B.service" 2>/dev/null)"
	assert_equal "panel-php-$B.service is active after the reboot (systemctl)" active \
		"$(vm "systemctl is-active panel-php-$B.service" 2>/dev/null)"
	wait_until 120 "phpinfo() to answer on $B" serves "$B"
	assert_equal "phpinfo() reports PHP $B after the reboot" "$B" "$(served_version)"
	assert_http "login after the reboot" 200 "$(api_login "$host" "$port" "$SMOKE_USER" "$pass")"
fi

# --- 10. delete -----------------------------------------------------------------
step 10 "delete the site: pool gone, unit left running"
assert_http "DELETE the PHP site" 200 "$(api_call DELETE "$host" "$port" "/api/sites/$SITE?confirm=$SITE")"
assert_not_equal "the site no longer answers" "$B" "$(served_version)"
assert_equal "the pool file is gone (test -e)" gone \
	"$(vm "test -e /www/panel/php/$B/pool.d/$POOL.conf && echo present || echo gone" 2>/dev/null)"
assert_equal "PHP $B keeps running without a site (systemctl)" active \
	"$(vm "systemctl is-active panel-php-$B.service" 2>/dev/null)"
assert_ok "the remaining FPM config passes php-fpm -t on the VM" \
	vm "PHP_INI_SCAN_DIR=:/www/panel/php/$B/conf.d /www/panel/php/$B/current/sbin/php-fpm -t -y /www/panel/php/$B/php-fpm.conf -c /www/panel/php/$B/php.ini"
assert_ok "the remaining nginx config passes nginx -t on the VM" vm "/www/panel/nginx/current/sbin/nginx -t -c /www/panel/nginx/nginx.conf"

# The kept web root (it holds a phpinfo page) is this script's own; remove it.
vm "rm -rf $ROOT"

smoke_finish
