#!/bin/sh
# Smoke 9: the open-appsec host gate (plan section 4, step 7), end to end, against
# a VM the panel is installed on.
#
#   SMOKE_HOST=root@10.0.0.5 sh scripts/smoke/9-open-appsec.sh
#   SMOKE_DRY_RUN=1 sh scripts/smoke/9-open-appsec.sh     # print the plan only
#
# Prerequisites this script checks but does not create:
#   - the serving nginx build carries the attachment:
#     $NGINX/current/modules/ngx_cp_attachment_module.so (build it from the Nginx
#     page first; this script never builds nginx);
#   - nothing else owns the serving port 80 (plan section 6.2: a second nginx
#     makes every result ambiguous, so the script refuses to continue).
#
# What it does: installs the agent through the Store API (not by hand), makes
# three throwaway sites (open-appsec, ModSecurity, firewall off), walks the
# engine through detect and block, checks the exception clause honestly, stops
# and restarts the agent, reboots the VM, uninstalls the agent once no site uses
# it, and deletes its sites. It records egress from the agent and the footprint,
# and reports them without judging them.
#
# Environment:
#   SMOKE_HOST          user@host of the VM; the user must be root
#   SMOKE_PASSWORD      admin password. When unset the script runs
#                       `panel reset-password` on the VM, which signs everyone out.
#   SMOKE_USER          admin username (default admin)
#   SMOKE_PANEL_PORT    panel HTTPS port (default 8443)
#   SMOKE_SSH_OPTS      extra ssh options
#   SMOKE_SKIP_REBOOT=1 skip the reboot clause; it is then reported NOT RUN
#   SMOKE_LATENCY_N     requests per latency run (default 200)
#   SMOKE_API_TIMEOUT   seconds a panel API call may take; a policy apply can be slow (default 300)
#
# Independent oracle: "is the agent up" is systemctl and the nginx error log's
# "registered on client" line on the VM; "is it enforcing" is a request from the
# workstation; the policy is read from /etc/cp/conf/local_policy.yaml on the VM,
# not from the panel. Egress is `ss -tnp` on the VM, sampled every 10 seconds.
# Nothing here passes just because a panel endpoint said so.

set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

SMOKE_USER=${SMOKE_USER:-admin}
SMOKE_PASSWORD=${SMOKE_PASSWORD:-}
SMOKE_SKIP_REBOOT=${SMOKE_SKIP_REBOOT:-0}
SMOKE_LATENCY_N=${SMOKE_LATENCY_N:-200}
SMOKE_API_TIMEOUT=${SMOKE_API_TIMEOUT:-300}
export SMOKE_API_TIMEOUT
PORT=${SMOKE_PANEL_PORT:-8443}
PANEL=/www/panel/bin/pbp
NGINX=/www/panel/nginx
ERRLOG=/www/panel/logs/nginx-error.log
EGRESS_LOG=/var/tmp/oa-egress-smoke.log
SITE_APPSEC=oa-detect.smoke.test
SITE_MODSEC=oa-modsec.smoke.test
SITE_OFF=oa-off.smoke.test
SQLI='/?id=1%27%20OR%20%271%27=%271'
XSS='/?q=%3Cscript%3Ealert(1)%3C/script%3E'
BENIGN='/?page=1'
# The only external addresses the agent is known to use (plan section 6.1).
CHECKPOINT_RE='^(75\.2\.123\.205|99\.83\.172\.252):443$'

smoke_init
host=$(vm_host)

last_body() { cat "$SMOKE_TMP/last.json" 2>/dev/null; }

# serve SITE PATH: the HTTP status for a request to SITE on the VM's port 80.
serve() { http_status "http://$1$2" --resolve "$1:80:$host"; }
# serve_is WANT SITE PATH: for wait_until.
serve_is() { [ "$(serve "$2" "$3")" = "$1" ]; }
# body_of SITE PATH: the response body, to check which page answered.
body_of() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then return 0; fi
	curl -s --max-time 10 --resolve "$1:80:$host" "http://$1$2" 2>/dev/null || true
}

# appsec_json: the availability object from GET /api/appsec.
appsec_json() { api_call GET "$host" "$PORT" /api/appsec >/dev/null; last_body; }
# appsec_is FIELD VALUE: e.g. appsec_is agent_active true, for wait_until.
appsec_is() { appsec_json | grep -q "\"$1\":$2"; }
# engine_events SITE: the events for SITE, from the panel's events endpoint.
engine_events() { api_call GET "$host" "$PORT" "/api/waf/events?site=$1" >/dev/null; last_body; }
event_engine_seen() { engine_events "$1" | grep -q '"engine":"open-appsec"'; }
# set_waf SITE JSON: PUT the site's firewall; prints the HTTP status.
set_waf() { api_call PUT "$host" "$PORT" "/api/sites/$1/waf" "$2"; }
# mk_site SITE: create a static site; prints the HTTP status.
mk_site() { api_call POST "$host" "$PORT" /api/sites "{\"domain\":\"$1\",\"kind\":\"static\"}"; }
# drop_site SITE: delete a site the script created.
drop_site() { api_call DELETE "$host" "$PORT" "/api/sites/$1?confirm=$1" >/dev/null; }

# owners_on_80: the executables that hold the serving port's listeners.
owners_on_80() {
	vm 'for p in $(ss -tlnp "sport = :80" | grep -o "pid=[0-9]*" | cut -d= -f2 | sort -u); do readlink /proc/$p/exe; done | sort -u'
}
# one_owner_on_80: every listener on :80 is a panel nginx build.
one_owner_on_80() {
	_o=$(owners_on_80)
	[ -n "$_o" ] && [ -z "$(printf '%s\n' "$_o" | grep -v "^$NGINX/versions/")" ]
}

# agent_rss_mb: resident memory of the agent's processes, in MB.
agent_rss_mb() {
	vm "ps -eo rss=,args= | grep '[c]p-nano' | awk '{s+=\$1} END {printf \"%.1f\", s/1024}'"
}

# latency SITE N: prints p50 p95 max of N sequential requests, in milliseconds.
latency() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then printf '0 0 0'; return 0; fi
	_f=$SMOKE_TMP/lat.$1
	: > "$_f"
	_i=0
	while [ "$_i" -lt "$2" ]; do
		curl -s -o /dev/null -w '%{time_total}\n' --max-time 10 \
			--resolve "$1:80:$host" "http://$1$BENIGN" >> "$_f" 2>/dev/null
		_i=$((_i + 1))
	done
	sort -n "$_f" | awk '{a[NR] = $1 * 1000} END {printf "%.1f %.1f %.1f", a[int(NR * 0.5)], a[int(NR * 0.95)], a[NR]}'
}

# ---- 0. prerequisites and egress recording ----------------------------------
step 0 "prerequisites: the VM, the panel, the module, one owner on :80"
assert_ok "the VM answers over SSH" vm true
assert_equal "panel.service is active" active "$(vm 'systemctl is-active panel.service' 2>/dev/null)"
assert_equal "panel-nginx.service is active" active "$(vm 'systemctl is-active panel-nginx.service' 2>/dev/null)"
assert_ok "the serving build carries the attachment module" \
	vm "test -f $NGINX/current/modules/ngx_cp_attachment_module.so"
assert_ok "only the panel's nginx owns port 80 (plan 6.2)" one_owner_on_80

# Egress is recorded from before the install to the end of the run, on the VM.
EGRESS_PID=$(vm "nohup sh -c 'while :; do date -u +%FT%TZ; ss -tnp state established | grep cp-nano; sleep 10; done' > $EGRESS_LOG 2>&1 < /dev/null & echo \$!" 2>/dev/null | tail -n 1)
log "  egress sampler started on the VM (pid ${EGRESS_PID:-unknown}), log $EGRESS_LOG"
log "  RECORDED agent RSS before install: $(agent_rss_mb 2>/dev/null) MB (empty means no agent)"

step 0 "log in to the panel"
if [ -z "$SMOKE_PASSWORD" ]; then
	reset_out=$(vm "$PANEL reset-password" 2>&1)
	[ "$SMOKE_DRY_RUN" = 1 ] && reset_out="    DRYRUNNEWPASSWORD"
	SMOKE_PASSWORD=$(printf '%s\n' "$reset_out" | sed -n 's/^ *\([A-Za-z0-9]\{12,\}\) *$/\1/p' | head -n 1)
fi
assert_not_equal "an admin password is available" "" "$SMOKE_PASSWORD"
assert_http "log in on the panel port" 200 "$(api_login "$host" "$PORT" "$SMOKE_USER" "$SMOKE_PASSWORD")"

# ---- 1. install the agent from the Store ------------------------------------
step 1 "install the agent from the Store, through its job"
if appsec_is installed true; then
	log "  note  an agent was already installed; removing it through the Store job so the install is exercised"
	api_call POST "$host" "$PORT" /api/appsec/uninstall '{}' >/dev/null
	wait_until 900 "the existing agent to be uninstalled" appsec_is installed false
fi
assert_http "install without the acknowledgement is refused" 422 \
	"$(api_call POST "$host" "$PORT" /api/appsec/install '{}')"
ack=$(appsec_json | sed -n 's/.*"acknowledgement":"\([^"]*\)".*/\1/p')
assert_not_equal "the availability object names an acknowledgement" "" "$ack"
assert_http "install with the acknowledgement starts a job" 202 \
	"$(api_call POST "$host" "$PORT" /api/appsec/install "{\"acknowledge\":\"$ack\"}")"
wait_until 900 "the agent to be installed and active" appsec_is agent_active true
assert_equal "nano_agent.service is active" active "$(vm 'systemctl is-active nano_agent.service' 2>/dev/null)"
assert_ok "nginx registered with the agent after the install (the job reloads it)" \
	vm "tail -n 400 $ERRLOG | grep -q 'registered on client'"

# ---- 2. detect: attacks pass and are events, normal traffic is not ----------
step 2 "open-appsec on one site in detect"
assert_http "create the open-appsec site" 201 "$(mk_site "$SITE_APPSEC")"
assert_http "enable open-appsec in detect" 200 \
	"$(set_waf "$SITE_APPSEC" '{"engine":"open-appsec","mode":"detect","open_appsec":{"confidence":"high"}}')"
wait_until 180 "the site to serve after its firewall change" serve_is 200 "$SITE_APPSEC" "$BENIGN"
assert_http "SQL injection passes in detect" 200 "$(serve "$SITE_APPSEC" "$SQLI")"
assert_http "XSS passes in detect" 200 "$(serve "$SITE_APPSEC" "$XSS")"
assert_http "normal traffic passes in detect" 200 "$(serve "$SITE_APPSEC" "$BENIGN")"
wait_until 180 "the attacks to appear as open-appsec events" event_engine_seen "$SITE_APPSEC"
ev=$(engine_events "$SITE_APPSEC")
assert_contains "an SQL injection event carries the open-appsec attack type" '"attack_type":"SQL Injection"' "$ev"
assert_contains "an XSS event carries the open-appsec attack type" 'Cross Site Scripting' "$ev"
assert_not_contains "normal traffic is not a threat event" 'page=1' "$ev"

# ---- 3. block: the same attacks get 403, normal traffic still passes --------
step 3 "switch the same site to block"
assert_http "enable block (prevent)" 200 \
	"$(set_waf "$SITE_APPSEC" '{"engine":"open-appsec","mode":"block","open_appsec":{"confidence":"high"}}')"
wait_until 180 "the site to enforce after the change" serve_is 403 "$SITE_APPSEC" "$SQLI"
assert_http "SQL injection is blocked" 403 "$(serve "$SITE_APPSEC" "$SQLI")"
assert_http "XSS is blocked" 403 "$(serve "$SITE_APPSEC" "$XSS")"
assert_http "normal traffic passes in block" 200 "$(serve "$SITE_APPSEC" "$BENIGN")"
assert_not_contains "the block page is the panel's, not nginx's default" '<center>nginx' \
	"$(body_of "$SITE_APPSEC" "$SQLI")"

# ---- 4. exceptions: a path exception is not a working control ---------------
step 4 "exceptions: a path exception must not exempt an attack (plan 6.1, findings C)"
ex_code=$(set_waf "$SITE_APPSEC" '{"engine":"open-appsec","mode":"block","open_appsec":{"confidence":"high","exceptions":[{"kind":"path","path":"/","note":"smoke"}]}}')
log "  RECORDED the API answered $ex_code to a path exception (not judged)"
wait_until 120 "the site to re-enforce after the exception change" serve_is 403 "$SITE_APPSEC" "$SQLI"
assert_http "a path exception does not let SQL injection through" 403 "$(serve "$SITE_APPSEC" "$SQLI")"
assert_ok "no urlPath key is in the rendered policy" \
	vm "! grep -q urlPath /etc/cp/conf/local_policy.yaml"
set_waf "$SITE_APPSEC" '{"engine":"open-appsec","mode":"block","open_appsec":{"confidence":"high"}}' >/dev/null

# ---- 5. regression: ModSecurity and a site with the firewall off ------------
step 5 "a ModSecurity site and a site with the firewall off behave as before"
assert_http "create the ModSecurity site" 201 "$(mk_site "$SITE_MODSEC")"
assert_http "enable ModSecurity in block" 200 "$(set_waf "$SITE_MODSEC" '{"mode":"block"}')"
assert_http "create the firewall-off site" 201 "$(mk_site "$SITE_OFF")"
wait_until 180 "the ModSecurity site to enforce" serve_is 403 "$SITE_MODSEC" "$SQLI"
assert_http "ModSecurity blocks SQL injection" 403 "$(serve "$SITE_MODSEC" "$SQLI")"
assert_http "ModSecurity passes normal traffic" 200 "$(serve "$SITE_MODSEC" "$BENIGN")"
assert_http "firewall-off site passes SQL injection" 200 "$(serve "$SITE_OFF" "$SQLI")"
assert_http "firewall-off site passes XSS" 200 "$(serve "$SITE_OFF" "$XSS")"

# Footprint, recorded next to the step 0 numbers (plan section 6.8: 218.8 MB idle).
log "  RECORDED latency, same benign URI, $SMOKE_LATENCY_N requests (p50 / p95 / max, ms)"
set_waf "$SITE_APPSEC" '{"engine":"open-appsec","mode":"block","open_appsec":{"confidence":"high"}}' >/dev/null
wait_until 120 "the open-appsec site to enforce for the latency run" serve_is 403 "$SITE_APPSEC" "$SQLI"
log "        firewall off : $(latency "$SITE_OFF" "$SMOKE_LATENCY_N")"
log "        open-appsec  : $(latency "$SITE_APPSEC" "$SMOKE_LATENCY_N")"
log "  RECORDED agent RSS now: $(agent_rss_mb 2>/dev/null) MB"

# ---- 6. stop the agent: fail-open, and the panel reports it down ------------
step 6 "stop the agent: every site still serves, the API reports it down"
assert_ok "stop nano_agent.service" vm 'systemctl stop nano_agent.service'
assert_equal "nano_agent.service is inactive" inactive "$(vm 'systemctl is-active nano_agent.service' 2>/dev/null)"
assert_http "open-appsec site serves with the agent down (fail-open)" 200 "$(serve "$SITE_APPSEC" "$SQLI")"
assert_http "ModSecurity site still serves" 403 "$(serve "$SITE_MODSEC" "$SQLI")"
assert_http "firewall-off site still serves" 200 "$(serve "$SITE_OFF" "$BENIGN")"
assert_ok "the API reports the agent down" appsec_is agent_active false
assert_equal "panel-nginx.service is still active" active "$(vm 'systemctl is-active panel-nginx.service' 2>/dev/null)"
# Agent first, then nginx: a lone agent restart leaves nginx on stale sockets (plan 0.4).
assert_ok "start nano_agent.service" vm 'systemctl start nano_agent.service'
assert_ok "reload panel-nginx.service after the agent" vm 'systemctl reload panel-nginx.service'
wait_until 300 "the agent to be active again" appsec_is agent_active true
wait_until 120 "enforcement to return" serve_is 403 "$SITE_APPSEC" "$SQLI"
assert_http "enforcement returns after the agent restart and reload" 403 "$(serve "$SITE_APPSEC" "$SQLI")"

# ---- 7. reboot: nginx, the agent and the policy all come back ---------------
step 7 "reboot the VM: everything returns with no panel action"
if [ "$SMOKE_SKIP_REBOOT" = 1 ]; then
	log "  NOT RUN  reboot skipped (SMOKE_SKIP_REBOOT=1)"
else
	vm 'systemctl reboot' >/dev/null 2>&1 || true
	sleep 30
	wait_until 900 "the VM to answer over SSH after the reboot" vm true
	assert_ok "the VM rebooted (uptime under 10 minutes)" \
		vm 'test "$(awk "{print int(\$1)}" /proc/uptime)" -lt 600'
	wait_until 300 "the panel to answer after the reboot" \
		sh -c "curl -sk -o /dev/null --max-time 5 https://$host:$PORT/api/appsec"
	assert_http "log in after the reboot" 200 "$(api_login "$host" "$PORT" "$SMOKE_USER" "$SMOKE_PASSWORD")"
	assert_equal "panel-nginx.service is active after the reboot" active "$(vm 'systemctl is-active panel-nginx.service' 2>/dev/null)"
	assert_equal "nano_agent.service is active after the reboot" active "$(vm 'systemctl is-active nano_agent.service' 2>/dev/null)"
	wait_until 300 "the agent to register after the reboot" appsec_is agent_active true
	wait_until 120 "enforcement to return after the reboot" serve_is 403 "$SITE_APPSEC" "$SQLI"
	assert_http "SQL injection is still blocked after the reboot" 403 "$(serve "$SITE_APPSEC" "$SQLI")"
fi

# ---- 8. uninstall: refused while a site uses it, works once none does -------
step 8 "uninstall is refused while a site uses the engine"
assert_http "POST uninstall while a site uses open-appsec starts a job" 202 \
	"$(api_call POST "$host" "$PORT" /api/appsec/uninstall '{}')"
sleep 30
assert_equal "the agent is still installed after the refused uninstall" active "$(vm 'systemctl is-active nano_agent.service' 2>/dev/null)"
api_call GET "$host" "$PORT" /api/jobs >/dev/null
assert_contains "the job list names the uninstall job" 'appsec.uninstall' "$(last_body)"

set_waf "$SITE_APPSEC" '{"engine":"modsecurity","mode":"off"}' >/dev/null
assert_http "uninstall once no site uses open-appsec starts a job" 202 \
	"$(api_call POST "$host" "$PORT" /api/appsec/uninstall '{}')"
wait_until 900 "the agent to be uninstalled" appsec_is installed false
assert_ok "the open-appsec module line is gone from the config" \
	vm "test ! -e $NGINX/waf/main/open-appsec.conf"
assert_equal "nano_agent.service is gone" inactive "$(vm 'systemctl is-active nano_agent.service' 2>/dev/null)"
wait_until 120 "the ModSecurity site to serve after the uninstall" serve_is 403 "$SITE_MODSEC" "$SQLI"
assert_http "ModSecurity still blocks after the uninstall" 403 "$(serve "$SITE_MODSEC" "$SQLI")"
assert_http "firewall-off site still serves after the uninstall" 200 "$(serve "$SITE_OFF" "$SQLI")"

# ---- 9. egress: recorded, and any non-Check-Point destination is a finding ---
step 9 "record the agent's outbound destinations (D5)"
vm "kill $EGRESS_PID 2>/dev/null; true" >/dev/null 2>&1
egress=$(vm "cat $EGRESS_LOG" 2>/dev/null)
dests=$(printf '%s\n' "$egress" | grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}:[0-9]+' | grep -vE '^(10\.|127\.)' | sort | uniq -c)
log "  RECORDED external destinations seen by the agent (count, address:port):"
if [ -n "$dests" ]; then printf '%s\n' "$dests" | sed 's/^/        /'; else log "        none recorded"; fi
unexpected=$(printf '%s\n' "$dests" | awk '{print $2}' | grep -vE "$CHECKPOINT_RE" | grep -v '^$' || true)
if [ -n "$unexpected" ]; then
	_fail "every external destination is a known Check Point address" "not expected:" "$unexpected"
else
	_pass "every external destination is a known Check Point address (plan 6.1)"
fi
log "  RECORDED egress log on the VM: $EGRESS_LOG"

# ---- clean up ---------------------------------------------------------------
step 9 "remove the smoke sites"
drop_site "$SITE_APPSEC"
drop_site "$SITE_MODSEC"
drop_site "$SITE_OFF"
assert_equal "the smoke site is gone" 404 "$(api_call GET "$host" "$PORT" "/api/sites/$SITE_APPSEC")"

smoke_finish
