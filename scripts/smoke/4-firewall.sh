#!/bin/sh
# Smoke 4: the M4 exit gate, end to end, against a VM the panel is installed on.
#
#   SMOKE_HOST=root@10.0.0.5 sh scripts/smoke/4-firewall.sh
#   SMOKE_DRY_RUN=1 sh scripts/smoke/4-firewall.sh       # print the plan only
#
# Run it after smoke 0 (or any `panel install`). It starts two throwaway HTTP
# listeners on the VM, enables the firewall, and checks from the workstation that
# a port with a listener is closed, then opened by a rule. It applies a change
# and does not confirm it, and waits for the previous state to return; it does the
# same across a daemon restart; it reboots the VM and checks the ruleset is back
# before the daemon starts. It ends by disabling the firewall, once through the
# API and once through `panel firewall disable`.
#
# Environment:
#   SMOKE_HOST       user@host of the VM; the user must be root
#   SMOKE_PASSWORD   admin password. When unset the script runs
#                    `panel reset-password` on the VM, which signs everyone out.
#   SMOKE_USER       admin username (default admin)
#   SMOKE_SSH_OPTS   extra ssh options
#   SMOKE_FW_PORT    first throwaway listener port (default 18080); the second is +1
#   SMOKE_SKIP_REBOOT=1  skip the reboot step
#   SMOKE_IPV6=1     also check ip6tables (the VM must have IPv6)
#
# Independent oracle: "is the port reachable" is a TCP connect from the
# workstation (curl); "is the rule present" is `iptables -S` on the VM; "is Docker's
# chain still there" is `iptables -S | grep DOCKER`; "did the restore run before
# the daemon" is systemd's monotonic start timestamps. None of them is a panel
# endpoint. SSH and the panel stay reachable throughout; a failure to reach either
# is itself a failed check.
#
# Needs the workstation to reach the VM's TCP ports directly, and python3 on the VM.
# A firewall in front of the VM that already blocks the listener ports makes the
# "open" checks fail, which is the correct answer.

set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

SMOKE_USER=${SMOKE_USER:-admin}
SMOKE_SKIP_REBOOT=${SMOKE_SKIP_REBOOT:-0}
SMOKE_IPV6=${SMOKE_IPV6:-0}
P1=${SMOKE_FW_PORT:-18080}
P2=$((P1 + 1))
PANEL=/www/panel/bin/panel

smoke_init
host=$(vm_host)

last_body() { cat "$SMOKE_TMP/last.json" 2>/dev/null; }
token() { last_body | sed -n 's/.*"token":"\([^"]*\)".*/\1/p'; }

# reach PORT: prints open or closed, by a TCP connect from the workstation.
reach() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then printf 'open'; return 0; fi
	if curl -s -o /dev/null --connect-timeout 3 --max-time 5 "http://$host:$1/" 2>/dev/null; then
		printf 'open'
	else
		printf 'closed'
	fi
}

is_open() { [ "$(reach "$1")" = open ]; }

listen() {
	vm "setsid nohup python3 -m http.server $1 --bind 0.0.0.0 >/dev/null 2>&1 < /dev/null &"
}

unlisten() { vm "pkill -f '[h]ttp.server $1'" >/dev/null 2>&1 || true; }

panel_healthy() {
	[ "$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 "https://$host:$port/api/health")" = 200 ]
}

# fw_state KEY: a top-level scalar of GET /api/firewall (true, false, or a number).
fw_state() {
	api_call GET "$host" "$port" /api/firewall >/dev/null
	last_body | sed -n 's/.*"'"$1"'":\([a-z0-9]*\).*/\1/p'
}

pending_cleared() { [ "$(fw_state pending)" = null ]; }

rules_text() { vm "iptables -S" 2>/dev/null; }
chain_has_port() { vm "iptables -S PANEL-INPUT" 2>/dev/null | grep -q -- "--dport $1 "; }
jump_count() { rules_text | grep -c -- '^-A INPUT -j PANEL-INPUT$' || true; }
docker_lines() { rules_text | grep -c DOCKER || true; }

# --- 1. the panel, and Docker's baseline --------------------------------------
step 1 "the panel is up; record Docker's chains"
assert_equal "panel.service is active (systemctl)" active "$(vm 'systemctl is-active panel' 2>/dev/null)"
assert_equal "panel-firewall.service is enabled (systemctl)" enabled "$(vm 'systemctl is-enabled panel-firewall' 2>/dev/null)"
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

docker_before=$(docker_lines)
log "  Docker lines in iptables -S before: $docker_before"
[ "$SMOKE_DRY_RUN" = 1 ] || assert_not_equal "Docker has inserted its chains (is Docker running?)" 0 "$docker_before"

unlisten "$P1"
unlisten "$P2"
listen "$P1"
listen "$P2"
wait_until 15 "the listener on $P1" is_open "$P1"
assert_equal "port $P1 is reachable before the firewall exists" open "$(reach "$P1")"
assert_equal "port $P2 is reachable before the firewall exists" open "$(reach "$P2")"
assert_equal "the firewall starts disabled" false "$(fw_state enabled)"

# --- 2. enable ----------------------------------------------------------------
step 2 "enable: ufw is masked, listeners close, SSH and the panel stay up"
assert_http "POST /api/firewall/apply before enabling is refused" 409 "$(api_call POST "$host" "$port" /api/firewall/apply '{}')"
assert_http "POST /api/firewall/enable" 200 "$(api_call POST "$host" "$port" /api/firewall/enable '{}')"
tok=$(token)
assert_not_equal "enable returned a confirmation token" "" "$tok"
assert_equal "port $P1 is closed once the firewall is applied" closed "$(reach "$P1")"
assert_equal "port $P2 is closed once the firewall is applied" closed "$(reach "$P2")"
assert_ok "SSH still works (a command ran on the VM)" vm true
assert_ok "the panel still answers /api/health" panel_healthy
assert_equal "the INPUT jump to PANEL-INPUT is present exactly once (iptables -S)" 1 "$(jump_count)"
assert_equal "ufw is masked (systemctl)" masked "$(vm 'systemctl is-enabled ufw' 2>/dev/null || true)"
assert_http "a second apply while one is pending is refused" 409 "$(api_call POST "$host" "$port" /api/firewall/apply '{}')"
assert_http "confirm with a wrong token is refused" 409 "$(api_call POST "$host" "$port" /api/firewall/confirm '{"token":"nope"}')"
assert_http "confirm with the right token" 204 "$(api_call POST "$host" "$port" /api/firewall/confirm "{\"token\":\"$tok\"}")"
assert_equal "the firewall is now enabled" true "$(fw_state enabled)"

# --- 3. a rule opens a port ---------------------------------------------------
step 3 "add a rule for $P1: stored rules are not live until applied"
assert_http "POST /api/firewall/rules" 201 \
	"$(api_call POST "$host" "$port" /api/firewall/rules "{\"portStart\":$P1,\"portEnd\":$P1,\"protocol\":\"tcp\",\"enabled\":true,\"comment\":\"smoke\"}")"
rule_id=$(last_body | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
assert_not_equal "the rule has an id" "" "$rule_id"
assert_equal "the state is dirty after a rule change" true "$(fw_state dirty)"
assert_equal "port $P1 is still closed before apply" closed "$(reach "$P1")"
assert_http "an invalid rule is rejected" 400 \
	"$(api_call POST "$host" "$port" /api/firewall/rules '{"portStart":0,"portEnd":0,"protocol":"tcp","enabled":true}')"

assert_http "POST /api/firewall/apply" 200 "$(api_call POST "$host" "$port" /api/firewall/apply '{}')"
tok=$(token)
wait_until 10 "port $P1 to open" is_open "$P1"
assert_equal "port $P1 is open after apply" open "$(reach "$P1")"
assert_equal "port $P2 is still closed" closed "$(reach "$P2")"
assert_http "confirm" 204 "$(api_call POST "$host" "$port" /api/firewall/confirm "{\"token\":\"$tok\"}")"
assert_equal "the state is clean after confirm" false "$(fw_state dirty)"
assert_equal "the INPUT jump is still present exactly once after a second apply" 1 "$(jump_count)"

# --- 4. an apply nobody confirms ------------------------------------------------
step 4 "apply a change and do not confirm: the previous state returns after 60 seconds"
assert_http "DELETE the rule" 204 "$(api_call DELETE "$host" "$port" "/api/firewall/rules/$rule_id")"
assert_http "apply the removal" 200 "$(api_call POST "$host" "$port" /api/firewall/apply '{}')"
wait_until 10 "port $P1 to close" sh -c "! curl -s -o /dev/null --connect-timeout 2 --max-time 3 http://$host:$P1/"
assert_equal "port $P1 is closed while the removal is pending" closed "$(reach "$P1")"
assert_ok "SSH works while the apply is pending" vm true
wait_until 100 "the 60 second rollback to reopen port $P1" is_open "$P1"
assert_equal "port $P1 is open again after the timeout" open "$(reach "$P1")"
assert_equal "nothing is pending after the rollback" null "$(fw_state pending)"
assert_ok "the rule is in the chain again (iptables -S)" chain_has_port "$P1"

# --- 5. the daemon restarts mid-apply -------------------------------------------
step 5 "restart the daemon while an apply is pending: it rolls back on start"
assert_http "apply the removal again" 200 "$(api_call POST "$host" "$port" /api/firewall/apply '{}')"
wait_until 10 "port $P1 to close" sh -c "! curl -s -o /dev/null --connect-timeout 2 --max-time 3 http://$host:$P1/"
vm "systemctl restart panel"
wait_until 60 "the panel to answer after the restart" panel_healthy
wait_until 20 "port $P1 to reopen after Recover" is_open "$P1"
assert_equal "port $P1 is open after the daemon recovered" open "$(reach "$P1")"
api_login "$host" "$port" "$SMOKE_USER" "$pass" >/dev/null
assert_equal "nothing is pending after the restart" null "$(fw_state pending)"
assert_equal "the INPUT jump is present exactly once after recovery" 1 "$(jump_count)"

# --- 6. Docker coexistence --------------------------------------------------------
step 6 "Docker's chains survived every apply and rollback"
assert_equal "Docker lines in iptables -S are unchanged" "$docker_before" "$(docker_lines)"
assert_equal "the INPUT policy was not changed by the panel (iptables -S)" "-P INPUT ACCEPT" \
	"$(rules_text | grep '^-P INPUT ' | head -n 1)"

# --- 7. IPv6 (optional) -------------------------------------------------------------
if [ "$SMOKE_IPV6" = 1 ]; then
	step 7 "IPv6: the same chains exist in ip6tables"
	assert_ok "ip6tables has PANEL-INPUT hooked from INPUT" \
		vm "ip6tables -S INPUT | grep -q -- '-j PANEL-INPUT'"
	assert_ok "ip6tables permits neighbour discovery (type 135)" \
		vm "ip6tables -S PANEL-INPUT | grep -q -- 'icmpv6-type 135'"
fi

# --- 8. reboot ----------------------------------------------------------------------
if [ "$SMOKE_SKIP_REBOOT" = 1 ]; then
	step 8 "reboot skipped (SMOKE_SKIP_REBOOT=1)"
else
	step 8 "reboot: the ruleset is back before the daemon starts"
	vm "systemctl reboot" >/dev/null 2>&1 || true
	[ "$SMOKE_DRY_RUN" = 1 ] || sleep 15
	wait_until 300 "the VM to come back (ssh)" vm true
	wait_until 120 "the panel to answer" panel_healthy
	assert_ok "the confirmed rule for $P1 is in PANEL-INPUT after boot (iptables -S)" chain_has_port "$P1"
	assert_equal "the INPUT jump is present exactly once after boot" 1 "$(jump_count)"
	fw_ts=$(vm "systemctl show panel-firewall -p ActiveEnterTimestampMonotonic --value" 2>/dev/null)
	pn_ts=$(vm "systemctl show panel -p ActiveEnterTimestampMonotonic --value" 2>/dev/null)
	log "  panel-firewall active at ${fw_ts}us, panel at ${pn_ts}us (monotonic)"
	if [ "$SMOKE_DRY_RUN" != 1 ]; then
		if [ -n "$fw_ts" ] && [ -n "$pn_ts" ] && [ "$fw_ts" -lt "$pn_ts" ]; then
			_pass "panel-firewall.service became active before panel.service"
		else
			_fail "panel-firewall.service became active before panel.service" "firewall: $fw_ts" "panel:    $pn_ts"
		fi
	fi
	assert_equal "docker lines after boot are present" "$((docker_before > 0))" "$(($(docker_lines) > 0))"
	unlisten "$P1"
	unlisten "$P2"
	listen "$P1"
	listen "$P2"
	wait_until 15 "the listener on $P1" is_open "$P1"
	assert_equal "port $P1 is reachable after boot" open "$(reach "$P1")"
	assert_equal "port $P2 is closed after boot" closed "$(reach "$P2")"
	api_login "$host" "$port" "$SMOKE_USER" "$pass" >/dev/null
fi

# --- 9. disable ----------------------------------------------------------------------
step 9 "disable through the API, then through the console command"
assert_http "POST /api/firewall/disable" 204 "$(api_call POST "$host" "$port" /api/firewall/disable '{}')"
assert_equal "no jump to PANEL-INPUT remains (iptables -S)" 0 "$(jump_count)"
wait_until 10 "port $P2 to open" is_open "$P2"
assert_equal "port $P2 is reachable with the firewall disabled" open "$(reach "$P2")"
assert_equal "the firewall reports disabled" false "$(fw_state enabled)"
assert_equal "ufw stays masked after disable (systemctl)" masked "$(vm 'systemctl is-enabled ufw' 2>/dev/null || true)"
vm "$PANEL firewall restore" >/dev/null 2>&1
assert_equal "firewall restore does nothing while disabled" 0 "$(jump_count)"

assert_http "enable again" 200 "$(api_call POST "$host" "$port" /api/firewall/enable '{}')"
tok=$(token)
assert_http "confirm the second enable" 204 "$(api_call POST "$host" "$port" /api/firewall/confirm "{\"token\":\"$tok\"}")"
assert_equal "port $P2 is closed again" closed "$(reach "$P2")"
vm "$PANEL firewall disable" >/dev/null 2>&1
wait_until 10 "port $P2 to open after the console disable" is_open "$P2"
assert_equal "port $P2 is reachable after 'panel firewall disable'" open "$(reach "$P2")"
assert_equal "no jump remains after the console disable" 0 "$(jump_count)"
api_login "$host" "$port" "$SMOKE_USER" "$pass" >/dev/null
assert_equal "the API agrees the firewall is disabled" false "$(fw_state enabled)"

# --- cleanup ---------------------------------------------------------------------------
step 10 "clean up"
unlisten "$P1"
unlisten "$P2"
smoke_finish
