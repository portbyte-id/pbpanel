#!/bin/sh
# Smoke 7: the M7 exit gate, end to end, against a VM the panel is installed on.
#
#   SMOKE_HOST=root@10.0.0.5 sh scripts/smoke/7-dashboard-logs-cron.sh
#   SMOKE_DRY_RUN=1 sh scripts/smoke/7-dashboard-logs-cron.sh   # print the plan only
#
# Run it after smokes 0 and 1 (the panel-nginx unit must be active and
# serve port 80). It checks the system snapshot against /proc on the VM, the
# live snapshot stream, the service list and the refusal to stop the panel from
# its own API; stops the nginx unit with `systemctl stop` on the host and
# waits for the panel to report it stopped, starts it again through the API and
# checks the site serves; lists and tails log sources and searches a site access
# log (literal, regex, case-insensitive, bad pattern); and drives cron: shell
# jobs refused until cron.allow_shell is on, a script job on `* * * * *` whose
# output must show up in the run history, in its log file on the VM and in the
# log viewer within two minutes, a manual run, enable and disable, validation
# errors, a cross-job run lookup and deletion.
#
# DISRUPTIVE: step 3 stops panel-nginx for a few seconds, so every site on the
# VM goes dark meanwhile. The script restarts it, and its exit trap starts it
# again if the script dies in between. Use a disposable VM.
#
# It takes about three minutes: the scheduled run is waited for (up to 150 s).
#
# Environment:
#   SMOKE_HOST        user@host of the VM; the user must be root
#   SMOKE_PASSWORD    admin password. When unset the script runs
#                     `panel reset-password` on the VM, which signs everyone out.
#   SMOKE_USER        admin username (default admin)
#   SMOKE_SSH_OPTS    extra ssh options
#   SMOKE_NGINX       nginx service name; its unit is NAME.service (default panel-nginx)
#   SMOKE_CRON_WAIT   seconds to wait for the scheduled run (default 150)
#
# Independent oracle: "is nginx stopped / is the site serving" is `systemctl`
# `is-active` on the VM and curl from the workstation against port 80; the stop is
# `systemctl stop`, not a panel call; "what is in the access log" and "what is in
# the cron log file" are `grep` and `cat` on the VM; memory size is
# /proc/meminfo. The panel's own claims (service state, run history, log
# sources) are what is being tested, and each is cross-checked against one of
# those. Not covered: the dashboard UI and its alerts (Vitest and a browser,
# not this script), and log rotation (not required here).
#
# What it touches: one static site (smoke7.test), two or three cron jobs named
# smoke7-*, one script on the VM, the cron.allow_shell setting (put back), and
# the nginx unit (stopped and started). It removes all of it on exit.

set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

SMOKE_USER=${SMOKE_USER:-admin}
SMOKE_NGINX=${SMOKE_NGINX:-panel-nginx}
NGINX_UNIT="$SMOKE_NGINX.service"
SMOKE_CRON_WAIT=${SMOKE_CRON_WAIT:-150}
PANEL=/www/panel/bin/pbp
SITE=smoke7.test
SITE_ROOT=/www/services/sites/$SITE
ACCESS_LOG=/www/panel/logs/sites/$SITE.access.log
ACCESS_ID="site:$SITE:access"
MARK="smoke7-$$"
REQ_PATH="/req-$MARK"
CRON_SCRIPT=/usr/local/bin/smoke7-cron.sh
CRON_REQ="cron-req-$MARK"
CRON_TS="cron-ts-$MARK"
JOB_SCRIPT=smoke7-cron
JOB_ARGV=smoke7-argv
JOB_SHELL=smoke7-shell

smoke_init
host=$(vm_host)
port=
ALLOW_SHELL_ORIG=
ALLOW_SHELL_CHANGED=0
CLEANED=0

last_body() { cat "$SMOKE_TMP/last.json" 2>/dev/null; }

# assert_http_in DESCRIPTION "200 204" ACTUAL
assert_http_in() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then log "  [dry-run] assert $1 -> HTTP $2"; return 0; fi
	case " $2 " in
	*" $3 "*) _pass "$1 (HTTP $3)" ;;
	*) _fail "$1" "expected HTTP one of: $2" "actual HTTP:           $3" ;;
	esac
}

# qcall PATH key=value...: GET with url-encoded query parameters. Prints the status.
qcall() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then printf '200'; return 0; fi
	_qp=$1
	shift
	_qn=$#
	_qi=0
	while [ "$_qi" -lt "$_qn" ]; do
		_qa=$1
		shift
		set -- "$@" --data-urlencode "$_qa"
		_qi=$((_qi + 1))
	done
	# MSYS2_ARG_CONV_EXCL: on Git Bash for Windows a "q=/path" argument is rewritten
	# into a Windows path before curl sees it; this call must send it as typed.
	MSYS2_ARG_CONV_EXCL='q=' curl -sk -G --max-time 30 -b "$COOKIE_JAR" -o "$SMOKE_TMP/last.json" -w '%{http_code}' \
		"$@" "https://$host:$port$_qp" 2>/dev/null || true
}

# stream_get PATH SECONDS: GET a text/event-stream (or any long) response for at
# most SECONDS, body into last.json. Prints the status. curl exits 28 on the
# time cap; that is the normal end of an endless stream, not an error.
stream_get() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then printf '200'; return 0; fi
	curl -sk -N --max-time "$2" -b "$COOKIE_JAR" -o "$SMOKE_TMP/last.json" -w '%{http_code}' \
		"https://$host:$port$1" 2>/dev/null || true
}

# num_field KEY: the first number after "KEY": in the JSON on stdin.
num_field() {
	sed -n 's/.*"'"$1"'":\([0-9][0-9]*\).*/\1/p' | head -n 1
}

# str_field KEY: the first string value of KEY in the JSON on stdin.
str_field() {
	sed -n 's/.*"'"$1"'":"\([^"]*\)".*/\1/p' | head -n 1
}

# err_class: the "error" class of the last response.
err_class() { last_body | str_field error; }

# svc_state NAME: the state the panel reports for a service row.
svc_state() {
	api_call GET "$host" "$port" /api/services >/dev/null
	last_body | grep -o "{\"name\":\"$1\"[^}]*}" | str_field state
}

nginx_running() { [ "$(vm "systemctl is-active $NGINX_UNIT" 2>/dev/null)" = active ]; }
nginx_state_is() { [ "$(svc_state "$SMOKE_NGINX")" = "$1" ]; }
site_body() { curl -s --max-time 10 -H "Host: $SITE" "http://$host/${1:-}" 2>/dev/null || true; }
site_serves() { [ "$(site_body)" = "$MARK" ]; }
site_down() { [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 -H "Host: $SITE" "http://$host/" 2>/dev/null)" = 000 ]; }
access_has() { vm "grep -c '$1' $ACCESS_LOG" 2>/dev/null | grep -qv '^0$'; }

# run_state JOB_ID RUN_ID: the status of a run.
run_status() {
	api_call GET "$host" "$port" "/api/cron/$1/runs/$2" >/dev/null
	last_body | str_field status
}
run_finished() {
	case $(run_status "$1" "$2") in
	succeeded | failed | timeout | canceled | interrupted | skipped) return 0 ;;
	esac
	return 1
}

# sched_run_done JOB_ID: some run of the job came from the scheduler and succeeded.
sched_run_done() {
	api_call GET "$host" "$port" "/api/cron/$1/runs" >/dev/null
	last_body | tr '}' '\n' | grep '"source":"schedule"' | grep -q '"status":"succeeded"'
}

# job_body NAME KIND EXTRA: the JSON for POST /api/cron.
job_body() {
	printf '{"name":"%s","schedule":"* * * * *","kind":"%s","user":"root","timeout_seconds":60%s}' "$1" "$2" "$3"
}

set_allow_shell() {
	api_call PUT "$host" "$port" /api/settings "{\"settings\":[{\"name\":\"cron.allow_shell\",\"value\":\"$1\"}]}"
}

cleanup_vm() {
	[ -n "$port" ] || return 0
	for _j in $JOB_SCRIPT $JOB_ARGV $JOB_SHELL; do
		api_call GET "$host" "$port" /api/cron >/dev/null 2>&1
		_id=$(last_body | tr '}' '\n' | grep "\"name\":\"$_j\"" | num_field id)
		[ -n "$_id" ] && api_call DELETE "$host" "$port" "/api/cron/$_id" >/dev/null 2>&1
	done
	api_call DELETE "$host" "$port" "/api/sites/$SITE?confirm=$SITE" >/dev/null 2>&1 || true
	vm "rm -rf $SITE_ROOT $CRON_SCRIPT" >/dev/null 2>&1 || true
}

# The exit trap runs even after a failed assertion: it must not leave the nginx
# container stopped, the shell setting changed or the test objects behind.
m7_cleanup() {
	if [ "$CLEANED" != 1 ] && [ "$SMOKE_DRY_RUN" != 1 ]; then
		CLEANED=1
		nginx_running || vm "systemctl start $NGINX_UNIT" >/dev/null 2>&1
		if [ -n "$port" ]; then
			[ "$ALLOW_SHELL_CHANGED" = 1 ] && set_allow_shell "${ALLOW_SHELL_ORIG:-false}" >/dev/null 2>&1
			cleanup_vm
		fi
	fi
	smoke_cleanup
}
trap m7_cleanup EXIT INT TERM

# --- 0. preflight -------------------------------------------------------------
step 0 "preflight: panel up, docker on the VM, login, nginx unit active"
assert_equal "panel.service is active (systemctl)" active "$(vm 'systemctl is-active panel' 2>/dev/null)"
assert_ok "docker answers on the VM" vm "docker version --format '{{.Server.Version}}'"
assert_equal "the nginx unit is active (systemctl)" active \
	"$(vm "systemctl is-active $NGINX_UNIT" 2>/dev/null)"
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
cleanup_vm

assert_http "GET /api/settings" 200 "$(api_call GET "$host" "$port" /api/settings)"
ALLOW_SHELL_ORIG=$(last_body | tr '}' '\n' | grep '"name":"cron.allow_shell"' | sed -n 's/.*"value":"\{0,1\}\([^",}]*\).*/\1/p' | head -n 1)
[ "$SMOKE_DRY_RUN" = 1 ] && ALLOW_SHELL_ORIG=false
log "  cron.allow_shell = ${ALLOW_SHELL_ORIG:-<unreadable>}"
assert_not_equal "cron.allow_shell is in the settings catalogue" "" "$ALLOW_SHELL_ORIG"
if [ "$ALLOW_SHELL_ORIG" != false ]; then
	ALLOW_SHELL_CHANGED=1
	assert_http "PUT cron.allow_shell=false (so the refusal can be tested)" 200 "$(set_allow_shell false)"
fi

# --- 1. sysinfo ---------------------------------------------------------------
step 1 "system snapshot agrees with /proc on the VM; the stream yields events"
assert_http "GET /api/sysinfo" 200 "$(api_call GET "$host" "$port" /api/sysinfo)"
sys_body=$(last_body)
assert_contains "the response has a latest snapshot" '"latest":{' "$sys_body"
assert_contains "the snapshot has cpu_percent" '"cpu_percent":' "$sys_body"
assert_contains "the snapshot has per-mount disks" '"disks":[{' "$sys_body"
assert_contains "the snapshot has the load averages" '"fifteen":' "$sys_body"
assert_contains "the response reports a sampling interval" '"interval_seconds":' "$sys_body"
panel_mem=$(printf '%s' "$sys_body" | grep -o '"memory":{[^}]*}' | head -n 1 | num_field total_bytes)
vm_mem=$(vm "awk '/^MemTotal:/ {print \$2 * 1024}' /proc/meminfo" 2>/dev/null)
[ "$SMOKE_DRY_RUN" = 1 ] && panel_mem=1 && vm_mem=1
assert_not_equal "the snapshot reports memory.total_bytes" "" "$panel_mem"
assert_equal "memory.total_bytes equals MemTotal in /proc/meminfo" "$vm_mem" "$panel_mem"
panel_up=$(printf '%s' "$sys_body" | num_field uptime_seconds)
vm_up=$(vm "cut -d. -f1 /proc/uptime" 2>/dev/null)
if [ "$SMOKE_DRY_RUN" = 1 ]; then
	log "  [dry-run] assert uptime_seconds is within 120 s of /proc/uptime"
elif [ -n "$panel_up" ] && [ -n "$vm_up" ] && [ "$((vm_up - panel_up))" -ge -120 ] && [ "$((vm_up - panel_up))" -le 120 ]; then
	_pass "uptime_seconds is within 120 s of /proc/uptime ($panel_up vs $vm_up)"
else
	_fail "uptime_seconds is within 120 s of /proc/uptime" "panel: ${panel_up:-<none>}" "vm:    ${vm_up:-<none>}"
fi
assert_http "GET /api/sysinfo?limit=-1 is refused" 400 "$(api_call GET "$host" "$port" '/api/sysinfo?limit=-1')"
assert_http "GET /api/sysinfo?limit=1" 200 "$(api_call GET "$host" "$port" '/api/sysinfo?limit=1')"

assert_http "GET /api/sysinfo/stream (read for 12 s)" 200 "$(stream_get /api/sysinfo/stream 12)"
assert_contains "the stream carried at least one snapshot event" "event: snapshot" "$(last_body)"
assert_contains "the snapshot event carries JSON data" 'data: {' "$(last_body)"

# --- 2. services --------------------------------------------------------------
step 2 "service list, control validation, and the panel refuses to stop itself"
assert_http "GET /api/services" 200 "$(api_call GET "$host" "$port" /api/services)"
assert_contains "the list has panel" '"name":"panel"' "$(last_body)"
assert_contains "the list has $SMOKE_NGINX" "\"name\":\"$SMOKE_NGINX\"" "$(last_body)"
assert_equal "$SMOKE_NGINX is a systemd row" systemd \
	"$(last_body | grep -o "{\"name\":\"$SMOKE_NGINX\"[^}]*}" | str_field kind)"
assert_equal "the panel reports $SMOKE_NGINX running" running "$(svc_state "$SMOKE_NGINX")"
assert_http "POST /api/services/panel/stop is refused" 403 "$(api_call POST "$host" "$port" /api/services/panel/stop '{}')"
assert_equal "the refusal class is not_allowed" not_allowed "$(err_class)"
assert_equal "panel.service is still active after the refused stop (systemctl)" active "$(vm 'systemctl is-active panel' 2>/dev/null)"
assert_http "an unknown service name is 404" 404 "$(api_call POST "$host" "$port" /api/services/no-such-service/restart '{}')"
assert_http "an unknown action is 400" 400 "$(api_call POST "$host" "$port" "/api/services/$SMOKE_NGINX/explode" '{}')"
assert_equal "$SMOKE_NGINX was not touched by the refused calls" active \
	"$(vm "systemctl is-active $NGINX_UNIT" 2>/dev/null)"

# --- 3. the exit gate: stop nginx from the host -------------------------------
step 3 "create a site; stop the nginx unit on the host; the panel shows it stopped; restart via the API; the site serves"
assert_http "POST /api/sites ($SITE)" 201 "$(api_call POST "$host" "$port" /api/sites "{\"domain\":\"$SITE\",\"kind\":\"static\"}")"
vm "printf '%s' '$MARK' > $SITE_ROOT/index.html"
wait_until 30 "the site to serve" site_serves
assert_equal "the site serves its marker (curl, Host header)" "$MARK" "$(site_body)"

assert_ok "systemctl stop $NGINX_UNIT (on the host, not through the panel)" vm "systemctl stop $NGINX_UNIT"
assert_equal "systemctl says the unit is not active" inactive \
	"$(vm "systemctl is-active $NGINX_UNIT" 2>/dev/null)"
assert_ok "port 80 stops answering (curl)" wait_until 15 "port 80 to close" site_down
wait_until 45 "the panel to report $SMOKE_NGINX stopped" nginx_state_is stopped
assert_equal "GET /api/services shows $SMOKE_NGINX stopped" stopped "$(svc_state "$SMOKE_NGINX")"
assert_contains "the stopped row offers start" '"start"' \
	"$(last_body | grep -o "{\"name\":\"$SMOKE_NGINX\"[^}]*}")"

assert_http "POST /api/services/$SMOKE_NGINX/start" 204 "$(api_call POST "$host" "$port" "/api/services/$SMOKE_NGINX/start" '{}')"
wait_until 60 "the site to serve again" site_serves
assert_equal "systemctl says the unit is active again" active \
	"$(vm "systemctl is-active $NGINX_UNIT" 2>/dev/null)"
assert_equal "the site serves again (curl, Host header)" "$MARK" "$(site_body)"
wait_until 45 "the panel to report $SMOKE_NGINX running" nginx_state_is running
assert_equal "GET /api/services shows $SMOKE_NGINX running" running "$(svc_state "$SMOKE_NGINX")"
assert_http "POST /api/services/$SMOKE_NGINX/restart" 204 "$(api_call POST "$host" "$port" "/api/services/$SMOKE_NGINX/restart" '{}')"
wait_until 60 "the site to serve after the restart" site_serves
assert_equal "the site still serves after a restart through the API" "$MARK" "$(site_body)"

# --- 4. logs: list and tail ----------------------------------------------------
step 4 "log sources and a tail of the site access log"
site_body "req-$MARK" >/dev/null
wait_until 20 "nginx to write the request to the access log" access_has "$REQ_PATH"
assert_ok "the access log on the VM holds the request (grep)" access_has "$REQ_PATH"
assert_http "GET /api/logs" 200 "$(api_call GET "$host" "$port" /api/logs)"
assert_contains "the sources include the site access log" "\"id\":\"$ACCESS_ID\"" "$(last_body)"
assert_contains "the sources include the site error log" "\"id\":\"site:$SITE:error\"" "$(last_body)"
assert_contains "the sources include the nginx error log" "\"id\":\"nginx:error\"" "$(last_body)"
assert_contains "the sources include the audit log" '"id":"panel:audit"' "$(last_body)"
if [ "$SMOKE_DRY_RUN" != 1 ]; then
	case $(last_body) in
	*/www/*) _fail "the listing exposes no server paths" "$(last_body)" ;;
	*) _pass "the listing exposes no server paths" ;;
	esac
fi

assert_http "GET .../tail?lines=50 of the site access log" 200 "$(stream_get "/api/logs/$ACCESS_ID/tail?lines=50" 15)"
assert_contains "the tail has line events" "event: line" "$(last_body)"
assert_contains "the tail carries the request made by this script" "$REQ_PATH" "$(last_body)"
assert_contains "a tail without follow ends with an end event" "event: end" "$(last_body)"
assert_http "an unknown log id is 404" 404 "$(api_call GET "$host" "$port" /api/logs/site:no-such.test:access/tail)"
assert_http "lines=abc is 400" 400 "$(api_call GET "$host" "$port" "/api/logs/$ACCESS_ID/tail?lines=abc")"
assert_http "the nginx error log tails" 200 "$(stream_get "/api/logs/nginx:error/tail?lines=5" 15)"

# --- 5. logs: search -----------------------------------------------------------
step 5 "search the site access log: literal, regex, case-insensitive, no hit, bad pattern"
vm_hits=$(vm "grep -c '$REQ_PATH' $ACCESS_LOG" 2>/dev/null)
[ "$SMOKE_DRY_RUN" = 1 ] && vm_hits=1
assert_http "search literal q=$REQ_PATH" 200 "$(qcall "/api/logs/$ACCESS_ID/search" "q=$REQ_PATH")"
assert_contains "the literal search matched the request" '"type":"match"' "$(last_body)"
assert_contains "the match text names the request path" "$REQ_PATH" "$(last_body)"
assert_contains "the search ended with a done line" '"type":"done"' "$(last_body)"
assert_equal "stats.matches equals grep -c on the VM" "$vm_hits" "$(last_body | grep '"type":"done"' | num_field matches)"
assert_http "search regex=1" 200 "$(qcall "/api/logs/$ACCESS_ID/search" "q=req-smoke7-[0-9]+" regex=1)"
assert_contains "the regex search matched" '"type":"match"' "$(last_body)"
assert_http "search ci=1 with an upper-case pattern" 200 "$(qcall "/api/logs/$ACCESS_ID/search" "q=REQ-$MARK" ci=1)"
assert_contains "the case-insensitive search matched" '"type":"match"' "$(last_body)"
assert_http "search without ci does not match upper case" 200 "$(qcall "/api/logs/$ACCESS_ID/search" "q=REQ-$MARK")"
case $(last_body) in
*'"type":"match"'*) _fail "a case-sensitive search for the wrong case finds nothing" "$(last_body)" ;;
*) _pass "a case-sensitive search for the wrong case finds nothing" ;;
esac
assert_contains "the no-hit search still ends with done" '"type":"done"' "$(last_body)"
assert_http "an uncompilable regex is 400" 400 "$(qcall "/api/logs/$ACCESS_ID/search" 'q=(unclosed' regex=1)"
assert_equal "the error names field q" q "$(last_body | str_field field)"
assert_http "a search without q is 400" 400 "$(qcall "/api/logs/$ACCESS_ID/search")"
assert_http "a search of an unknown id is 404" 404 "$(qcall /api/logs/site:no-such.test:access/search "q=x")"

# --- 6. cron: shell jobs are refused until the setting is on -------------------
step 6 "cron: a shell job is refused (403) while cron.allow_shell is off"
assert_http "GET /api/cron" 200 "$(api_call GET "$host" "$port" /api/cron)"
assert_contains "the job list is an object with jobs" '"jobs":' "$(last_body)"
shell_json=$(job_body "$JOB_SHELL" shell ",\"script_body\":\"echo smoke7-shell-ok\"")
assert_http "POST /api/cron kind=shell with allow_shell off" 403 "$(api_call POST "$host" "$port" /api/cron "$shell_json")"
assert_equal "the refusal class is shell_not_allowed" shell_not_allowed "$(err_class)"
api_call GET "$host" "$port" /api/cron >/dev/null
case $(last_body) in
*"\"name\":\"$JOB_SHELL\""*) _fail "the refused shell job was not stored" "$(last_body)" ;;
*) _pass "the refused shell job was not stored" ;;
esac
assert_http "an invalid schedule is 400" 400 "$(api_call POST "$host" "$port" /api/cron "$(job_body smoke7-bad argv ',"argv":["/bin/true"]' | sed 's/\* \* \* \* \*/61 * * * */')")"
assert_equal "the validation class is invalid_input" invalid_input "$(err_class)"
assert_http "an argv job with a relative program is 400" 400 "$(api_call POST "$host" "$port" /api/cron "$(job_body smoke7-bad argv ',"argv":["true"]')")"

# --- 7. cron: the scheduled run (exit gate) -------------------------------------
step 7 "cron: a script job on * * * * * writes a timestamp and makes a request; output lands in history, the log file and the log viewer"
# The script prints a timestamp and requests a unique path from the site, so the
# access log gets a line made by cron and the run output carries the marker.
vm "cat > $CRON_SCRIPT && chmod 755 $CRON_SCRIPT" <<EOF
#!/bin/sh
echo "$CRON_TS \$(date +%s)"
curl -s -o /dev/null -H "Host: $SITE" "http://127.0.0.1/$CRON_REQ"
EOF
assert_equal "the script is executable on the VM (stat)" 755 "$(vm "stat -c %a $CRON_SCRIPT" 2>/dev/null)"
script_json=$(job_body "$JOB_SCRIPT" script ",\"script_path\":\"$CRON_SCRIPT\"")
assert_http "POST /api/cron kind=script" 201 "$(api_call POST "$host" "$port" /api/cron "$script_json")"
JOB=$(last_body | num_field id)
[ "$SMOKE_DRY_RUN" = 1 ] && JOB=1
assert_not_equal "the job has an id" "" "$JOB"
assert_contains "the job is enabled" '"enabled":true' "$(last_body)"
assert_contains "the job has a next_run" '"next_run":"' "$(last_body)"
assert_http "duplicate name is 409" 409 "$(api_call POST "$host" "$port" /api/cron "$script_json")"
assert_equal "the conflict names field name" name "$(last_body | str_field field)"
assert_http "GET /api/cron/$JOB" 200 "$(api_call GET "$host" "$port" "/api/cron/$JOB")"
assert_contains "the job reports kind script" '"kind":"script"' "$(last_body)"

log "  waiting up to ${SMOKE_CRON_WAIT}s for the scheduler's own run (not a manual one)"
wait_until "$SMOKE_CRON_WAIT" "a scheduled, succeeded run in the history" sched_run_done "$JOB"
assert_ok "the history has a run with source schedule and status succeeded" sched_run_done "$JOB"
api_call GET "$host" "$port" "/api/cron/$JOB/runs" >/dev/null
RUN=$(last_body | num_field id)
[ "$SMOKE_DRY_RUN" = 1 ] && RUN=1
assert_not_equal "the history lists a run id" "" "$RUN"
assert_http "GET /api/cron/$JOB/runs/$RUN" 200 "$(api_call GET "$host" "$port" "/api/cron/$JOB/runs/$RUN")"
assert_contains "the run output tail has the timestamp line" "$CRON_TS" "$(last_body)"
assert_contains "the run exit code is 0" '"exit_code":0' "$(last_body)"
LOGFILE=$(last_body | str_field log_path)
[ "$SMOKE_DRY_RUN" = 1 ] && LOGFILE=/www/panel/logs/cron/$JOB_SCRIPT/dry.log
assert_not_equal "the run reports a log_path" "" "$LOGFILE"
assert_contains "the log file is under the cron log directory" "/www/panel/logs/cron/" "$LOGFILE"
assert_contains "the log file on the VM holds the timestamp (cat)" "$CRON_TS" "$(vm "cat '$LOGFILE'" 2>/dev/null)"
assert_http "GET /api/cron/$JOB shows last_run" 200 "$(api_call GET "$host" "$port" "/api/cron/$JOB")"
assert_contains "the job's last_run is set" '"last_run":{' "$(last_body)"

assert_http "GET /api/logs after a cron run" 200 "$(api_call GET "$host" "$port" /api/logs)"
assert_contains "the log sources include the job's run log" "\"id\":\"cron:$JOB_SCRIPT\"" "$(last_body)"
assert_http "tail cron:$JOB_SCRIPT" 200 "$(stream_get "/api/logs/cron:$JOB_SCRIPT/tail?lines=20" 15)"
assert_contains "the log viewer shows the job's output" "$CRON_TS" "$(last_body)"

# The script's request must be in the site access log, found through the search.
wait_until 20 "nginx to write the cron request" access_has "$CRON_REQ"
vm_cron_hits=$(vm "grep -c '$CRON_REQ' $ACCESS_LOG" 2>/dev/null)
[ "$SMOKE_DRY_RUN" = 1 ] && vm_cron_hits=1
assert_http "search the access log for the request the script made" 200 "$(qcall "/api/logs/$ACCESS_ID/search" "q=$CRON_REQ")"
assert_contains "the search finds the request made by the cron script" "$CRON_REQ" "$(last_body)"
assert_equal "the number of matches equals grep -c on the VM" "$vm_cron_hits" "$(last_body | grep '"type":"done"' | num_field matches)"

# --- 8. cron: manual run, enable/disable, cross-job lookup, delete --------------
step 8 "cron: a manual run, disable and enable, a run is not readable through another job, delete removes history and logs"
argv_json=$(job_body "$JOB_ARGV" argv ',"argv":["/bin/date"]' | sed 's/\* \* \* \* \*/0 0 1 1 */')
assert_http "POST /api/cron kind=argv (yearly, so only the manual run fires)" 201 "$(api_call POST "$host" "$port" /api/cron "$argv_json")"
ARGV_JOB=$(last_body | num_field id)
[ "$SMOKE_DRY_RUN" = 1 ] && ARGV_JOB=2
assert_not_equal "the argv job has an id" "" "$ARGV_JOB"
assert_http_in "POST /api/cron/$ARGV_JOB/run" "202" "$(api_call POST "$host" "$port" "/api/cron/$ARGV_JOB/run" '{}')"
assert_contains "the outcome is started" '"outcome":"started"' "$(last_body)"
MRUN=$(last_body | num_field id)
[ "$SMOKE_DRY_RUN" = 1 ] && MRUN=1
assert_not_equal "the manual run has an id" "" "$MRUN"
wait_until 20 "the manual run to finish" run_finished "$ARGV_JOB" "$MRUN"
assert_equal "the manual run succeeded" succeeded "$(run_status "$ARGV_JOB" "$MRUN")"
assert_contains "the run is recorded as manual" '"source":"manual"' "$(last_body)"
assert_contains "date's output is in the run's output tail" "$(date +%Y)" "$(last_body)"
assert_http "the same run id under another job is 404" 404 "$(api_call GET "$host" "$port" "/api/cron/$JOB/runs/$MRUN")"

assert_http "POST /api/cron/$JOB/disable" 200 "$(api_call POST "$host" "$port" "/api/cron/$JOB/disable" '{}')"
assert_contains "the job is disabled" '"enabled":false' "$(last_body)"
assert_contains "a disabled job has next_run null" '"next_run":null' "$(last_body)"
assert_http "POST /api/cron/$JOB/enable" 200 "$(api_call POST "$host" "$port" "/api/cron/$JOB/enable" '{}')"
assert_contains "the job is enabled again" '"enabled":true' "$(last_body)"
assert_http "GET /api/cron/999999 is 404" 404 "$(api_call GET "$host" "$port" /api/cron/999999)"

assert_http "DELETE /api/cron/$ARGV_JOB" 204 "$(api_call DELETE "$host" "$port" "/api/cron/$ARGV_JOB")"
assert_http "the deleted job is 404" 404 "$(api_call GET "$host" "$port" "/api/cron/$ARGV_JOB")"
assert_http "DELETE /api/cron/$JOB" 204 "$(api_call DELETE "$host" "$port" "/api/cron/$JOB")"
assert_http "the deleted script job is 404" 404 "$(api_call GET "$host" "$port" "/api/cron/$JOB")"
assert_http "its run history is gone (404)" 404 "$(api_call GET "$host" "$port" "/api/cron/$JOB/runs/$RUN")"
assert_equal "the job's log directory is removed on the VM (ls)" "" \
	"$(vm "ls -d /www/panel/logs/cron/$JOB_SCRIPT 2>/dev/null" 2>/dev/null)"
assert_equal "no cron:$JOB_SCRIPT source is listed any more" no \
	"$(api_call GET "$host" "$port" /api/logs >/dev/null; last_body | grep -q "cron:$JOB_SCRIPT" && echo yes || echo no)"

# --- 9. cron: enabling shell jobs through the settings API ----------------------
step 9 "cron: turn cron.allow_shell on through PUT /api/settings; a shell job is accepted, runs, and is refused again when the setting is turned off"
ALLOW_SHELL_CHANGED=1
assert_http "PUT /api/settings cron.allow_shell=true" 200 "$(set_allow_shell true)"
assert_contains "the settings response reports it on" '"cron.allow_shell"' "$(last_body)"
assert_http "POST /api/cron kind=shell with allow_shell on" 201 "$(api_call POST "$host" "$port" /api/cron "$(printf '%s' "$shell_json" | sed 's/\* \* \* \* \*/0 0 1 1 */')")"
SHELL_JOB=$(last_body | num_field id)
[ "$SMOKE_DRY_RUN" = 1 ] && SHELL_JOB=3
assert_not_equal "the shell job has an id" "" "$SHELL_JOB"
assert_http_in "POST /api/cron/$SHELL_JOB/run" "202" "$(api_call POST "$host" "$port" "/api/cron/$SHELL_JOB/run" '{}')"
SRUN=$(last_body | num_field id)
[ "$SMOKE_DRY_RUN" = 1 ] && SRUN=1
wait_until 20 "the shell run to finish" run_finished "$SHELL_JOB" "$SRUN"
assert_equal "the shell run succeeded" succeeded "$(run_status "$SHELL_JOB" "$SRUN")"
assert_contains "the shell job's output is in the run" "smoke7-shell-ok" "$(last_body)"
assert_http "PUT cron.allow_shell=false" 200 "$(set_allow_shell false)"
# The run is accepted and recorded as failed: the refusal is the run's own error.
assert_http_in "POST /api/cron/$SHELL_JOB/run is accepted" "202" "$(api_call POST "$host" "$port" "/api/cron/$SHELL_JOB/run" '{}')"
SRUN2=$(last_body | num_field id)
[ "$SMOKE_DRY_RUN" = 1 ] && SRUN2=2
wait_until 20 "the refused shell run to finish" run_finished "$SHELL_JOB" "$SRUN2"
assert_equal "the stored shell job fails once the setting is off" failed "$(run_status "$SHELL_JOB" "$SRUN2")"
assert_contains "the failed run says shell jobs are not enabled" "not enabled" "$(last_body)"
assert_http "DELETE /api/cron/$SHELL_JOB" 204 "$(api_call DELETE "$host" "$port" "/api/cron/$SHELL_JOB")"

# --- 10. leave the VM as it was found ------------------------------------------
step 10 "clean up and check: site, jobs and script gone, cron.allow_shell and the nginx unit as found"
assert_http "restore cron.allow_shell=${ALLOW_SHELL_ORIG:-false}" 200 "$(set_allow_shell "${ALLOW_SHELL_ORIG:-false}")"
ALLOW_SHELL_CHANGED=0
assert_http "DELETE the site $SITE" 200 "$(api_call DELETE "$host" "$port" "/api/sites/$SITE?confirm=$SITE")"
vm "rm -rf $SITE_ROOT $CRON_SCRIPT" >/dev/null 2>&1
assert_http "$SITE is gone from the panel" 404 "$(api_call GET "$host" "$port" "/api/sites/$SITE")"
api_call GET "$host" "$port" /api/cron >/dev/null
case $(last_body) in
*'"name":"smoke7-'*) _fail "no smoke7 cron job is left" "$(last_body)" ;;
*) _pass "no smoke7 cron job is left" ;;
esac
assert_equal "the nginx unit is active (systemctl)" active \
	"$(vm "systemctl is-active $NGINX_UNIT" 2>/dev/null)"
assert_equal "panel.service is still active (systemctl)" active "$(vm 'systemctl is-active panel' 2>/dev/null)"

smoke_finish
