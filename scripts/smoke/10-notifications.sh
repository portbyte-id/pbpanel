#!/bin/sh
# Smoke 10: notifications, end to end, against a VM the panel is installed on.
# Plan: docs/design/notifications-plan.md §6 (host gate).
#
#   SMOKE_HOST=root@10.10.11.106 sh scripts/smoke/10-notifications.sh
#   SMOKE_DRY_RUN=1 sh scripts/smoke/10-notifications.sh   # print the plan only
#
# It starts two loopback helpers on the VM (a webhook receiver and a plain SMTP
# sink, both in testdata/), creates a signed webhook channel and an email channel
# pointed at them, and checks: test sends; the HMAC signature (recomputed by
# Python's hmac, not by the panel); the secret at rest and in every API answer,
# audit log and journal; cron.failed opening, dedupe and resolution; the
# firewall.disabled and its resolution through the notify-sweep built-in; quiet
# hours; delivery retry across a receiver outage and a panel restart; and the bell
# counts and the live stream.
#
# NOT COVERED, reported as SKIP: tls.renew_failed (needs a staging certificate
# that fails to renew), a critical-severity event (no safe producer on this VM),
# and the Telegram and Discord senders (no operator credentials).
#
# DISRUPTIVE: the firewall is disabled and then enabled again; the panel service
# is restarted once. Use a disposable VM. The script restores the firewall state
# it found and removes every object it created.
#
# Environment:
#   SMOKE_HOST        user@host of the VM; the user must be root
#   SMOKE_PASSWORD    admin password. When unset the script runs
#                     `panel reset-password` on the VM, which signs everyone out.
#   SMOKE_USER        admin username (default admin)
#   SMOKE_SSH_OPTS    extra ssh options

set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

SMOKE_USER=${SMOKE_USER:-admin}
PANEL=/www/panel/bin/pbp
DB=/www/panel/data/panel.db
RUN=/run/smoke-notify
RX=$RUN/rx.jsonl
FAILF=$RUN/fail
SMTPLOG=$RUN/smtp.log
RXPORT=18080
SMTPPORT=2525
HOOK_URL="http://127.0.0.1:$RXPORT/hook"
HMAC_KEY=smoke-hmac-10
SMTP_USER=smoke10-user
SMTP_PASS=smoke-pass-10
CHAN_HOOK=smoke10-hook
CHAN_MAIL=smoke10-mail
JOB_FAIL=smoke10-fail
JOB_QUIET=smoke10-quiet
JOB_RETRY=smoke10-retry
JOB_BELL=smoke10-bell

smoke_init
host=$(vm_host)
port=
pass=
HOOK_ID=
MAIL_ID=
FW_WAS_ENABLED=
CLEANED=0

# jget KEY: the first "KEY":value of the last API answer, quotes removed.
jget() {
	sed -n "s/.*\"$1\":\"\{0,1\}\([^,\"}]*\).*/\1/p" "$SMOKE_TMP/last.json" | head -n 1
}

# rx_count: how many requests the webhook receiver has recorded.
rx_count() {
	vm "test -f $RX && wc -l < $RX || echo 0" 2>/dev/null | tr -d ' \r'
}

# rx_at_least N: the receiver has recorded at least N requests.
rx_at_least() {
	[ "$(rx_count)" -ge "$1" ]
}

# deliveries CHANNEL STATE: how many delivery rows the log holds for that state.
deliveries() {
	api_call GET "$host" "$port" "/api/notify/deliveries?channel=$1&state=$2&limit=200" >/dev/null
	grep -o '"id":' "$SMOKE_TMP/last.json" | wc -l | tr -d ' '
}

# deliveries_at_least CHANNEL STATE N: the log holds at least N rows in that state.
deliveries_at_least() {
	[ "$(deliveries "$1" "$2")" -ge "$3" ]
}

# attempts_at_least_two: the queued webhook delivery has been tried twice.
attempts_at_least_two() {
	api_call GET "$host" "$port" "/api/notify/deliveries?channel=$HOOK_ID&state=queued&limit=5" >/dev/null
	grep -q '"attempts":[2-9]' "$SMOKE_TMP/last.json"
}

# events_count: how many events the list holds (newest 200).
events_count() {
	api_call GET "$host" "$port" "/api/notify/events?limit=200" >/dev/null
	grep -o '"id":' "$SMOKE_TMP/last.json" | wc -l | tr -d ' '
}

# job_spec NAME ARGV_JSON: an argv cron job that never fires on its own.
job_spec() {
	printf '{"name":"%s","schedule":"0 0 1 1 *","kind":"argv","user":"root","timeout_seconds":60,"argv":%s}' "$1" "$2"
}

# job_id NAME: the id of the cron job with that name, from the job list.
job_id() {
	api_call GET "$host" "$port" /api/cron >/dev/null
	grep -o "\"id\":[0-9]*,\"name\":\"$1\"" "$SMOKE_TMP/last.json" | head -n 1 | sed 's/"id":\([0-9]*\).*/\1/'
}

# run_has ID STATUS: the job's run history holds a run with that status.
run_has() {
	api_call GET "$host" "$port" "/api/cron/$1/runs" >/dev/null
	grep -q "\"status\":\"$2\"" "$SMOKE_TMP/last.json"
}

# run_job ID: start a run now and wait for it to finish, in either state.
run_done() {
	run_has "$1" succeeded || run_has "$1" failed
}

# run_job ID: start a run now and wait for it to finish, in either state.
run_job() {
	api_call POST "$host" "$port" "/api/cron/$1/run" >/dev/null
	wait_until 60 "run of job $1" run_done "$1"
}

# channel_put ID NAME TYPE CONFIG QUIET_FROM QUIET_TO: rewrite a channel; its secret is kept.
channel_put() {
	api_call PUT "$host" "$port" "/api/notify/channels/$1" \
		"{\"name\":\"$2\",\"type\":\"$3\",\"enabled\":true,\"min_severity\":\"warning\",\"kinds\":[],\"quiet_start\":\"$5\",\"quiet_end\":\"$6\",\"send_resolved\":true,\"config\":$4}"
}

# start_helper NAME SCRIPT ARGS...: start a helper on the VM with a PID file.
start_helper() {
	_name=$1
	_script=$2
	shift 2
	vm "echo 'echo \$\$ > $RUN/$_name.pid; exec python3 $RUN/$_script $*' > $RUN/$_name.sh"
	vm "nohup sh $RUN/$_name.sh >/dev/null 2>&1 &"
}

# stop_helper NAME: stop a helper by its PID file.
stop_helper() {
	vm "kill \$(cat $RUN/$1.pid 2>/dev/null) 2>/dev/null; true" >/dev/null 2>&1 || true
}

cleanup() {
	[ "$CLEANED" = 1 ] && return
	CLEANED=1
	if [ "$SMOKE_DRY_RUN" != 1 ] && [ -n "$port" ]; then
		api_login "$host" "$port" "$SMOKE_USER" "$pass" >/dev/null 2>&1 || true
		for name in "$JOB_FAIL" "$JOB_QUIET" "$JOB_RETRY" "$JOB_BELL"; do
			id=$(job_id "$name" 2>/dev/null)
			[ -n "$id" ] && api_call DELETE "$host" "$port" "/api/cron/$id" >/dev/null 2>&1
		done
		for id in "$HOOK_ID" "$MAIL_ID"; do
			[ -n "$id" ] && api_call DELETE "$host" "$port" "/api/notify/channels/$id" >/dev/null 2>&1
		done
		if [ "$FW_WAS_ENABLED" = true ]; then
			api_call POST "$host" "$port" /api/firewall/enable >/dev/null 2>&1
			FW_TOKEN=$(jget token)
			api_call POST "$host" "$port" /api/firewall/confirm "{\"token\":\"$FW_TOKEN\"}" >/dev/null 2>&1
		else
			api_call POST "$host" "$port" /api/firewall/disable >/dev/null 2>&1
		fi
	fi
	stop_helper notify_receiver
	stop_helper notify_smtp_sink
	vm "rm -rf $RUN" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

# ---- 1. helpers on the VM ---------------------------------------------------
step 1 "start the loopback webhook receiver and SMTP sink on the VM"
vm "mkdir -p $RUN && rm -f $RX $FAILF $SMTPLOG" || exit 1
vm_copy "$here/testdata/notify_receiver.py" "$RUN/notify_receiver.py"
vm_copy "$here/testdata/notify_smtp_sink.py" "$RUN/notify_smtp_sink.py"
vm_copy "$here/testdata/notify_verify.py" "$RUN/notify_verify.py"
start_helper notify_receiver notify_receiver.py "$RXPORT" "$RX" "$FAILF"
start_helper notify_smtp_sink notify_smtp_sink.py "$SMTPPORT" "$SMTPLOG"
wait_until 15 "the receiver to listen" vm "ss -ltn | grep -q '127.0.0.1:$RXPORT'"
assert_ok "the receiver listens on 127.0.0.1:$RXPORT" vm "ss -ltn | grep -q '127.0.0.1:$RXPORT'"
assert_ok "the SMTP sink listens on 127.0.0.1:$SMTPPORT" vm "ss -ltn | grep -q '127.0.0.1:$SMTPPORT'"

# ---- 2. the panel and a session ---------------------------------------------
step 2 "log in to the panel"
port=$(vm "$PANEL info" 2>/dev/null | sed -n 's/^ *port  *\([0-9][0-9]*\).*/\1/p' | head -n 1)
[ "$SMOKE_DRY_RUN" = 1 ] && port=8443
pass=${SMOKE_PASSWORD:-}
if [ -z "$pass" ] && [ "$SMOKE_DRY_RUN" != 1 ]; then
	reset_out=$(vm "$PANEL reset-password" 2>&1)
	pass=$(printf '%s\n' "$reset_out" | sed -n 's/^ *\([A-Za-z0-9]\{12,\}\) *$/\1/p' | head -n 1)
fi
assert_http "login" 200 "$(api_login "$host" "$port" "$SMOKE_USER" "$pass")"

api_call GET "$host" "$port" /api/firewall >/dev/null
if grep -q '"enabled":true' "$SMOKE_TMP/last.json"; then FW_WAS_ENABLED=true; else FW_WAS_ENABLED=false; fi
log "  firewall enabled at start: $FW_WAS_ENABLED"

# ---- 3. channels and test sends ---------------------------------------------
step 3 "create a signed webhook channel and an email channel"
hook_body="{\"name\":\"$CHAN_HOOK\",\"type\":\"webhook\",\"enabled\":true,\"min_severity\":\"warning\",\"kinds\":[],\"quiet_start\":\"\",\"quiet_end\":\"\",\"send_resolved\":true,\"config\":{\"url\":\"$HOOK_URL\",\"sign\":true},\"secret\":{\"hmac_secret\":\"$HMAC_KEY\"}}"
assert_http "create the webhook channel" 201 "$(api_call POST "$host" "$port" /api/notify/channels "$hook_body")"
HOOK_ID=$(jget id)
mail_body="{\"name\":\"$CHAN_MAIL\",\"type\":\"smtp\",\"enabled\":true,\"min_severity\":\"warning\",\"kinds\":[],\"quiet_start\":\"\",\"quiet_end\":\"\",\"send_resolved\":true,\"config\":{\"host\":\"127.0.0.1\",\"port\":$SMTPPORT,\"security\":\"none\",\"username\":\"$SMTP_USER\",\"from\":\"panel@smoke.test\",\"to\":[\"ops@smoke.test\"]},\"secret\":{\"password\":\"$SMTP_PASS\"}}"
assert_http "create the email channel" 201 "$(api_call POST "$host" "$port" /api/notify/channels "$mail_body")"
MAIL_ID=$(jget id)

step 3 "test send on each channel"
assert_http "test send to the webhook" 200 "$(api_call POST "$host" "$port" "/api/notify/channels/$HOOK_ID/test")"
assert_contains "the webhook test reports ok" '"ok":true' "$(cat "$SMOKE_TMP/last.json")"
assert_http "test send to the email channel" 200 "$(api_call POST "$host" "$port" "/api/notify/channels/$MAIL_ID/test")"
assert_contains "the email test reports ok" '"ok":true' "$(cat "$SMOKE_TMP/last.json")"
wait_until 30 "the webhook request" rx_at_least 1
assert_ok "the receiver's signature verifies with an independent HMAC" \
	vm "python3 $RUN/notify_verify.py $RX $HMAC_KEY >/dev/null"
assert_ok "the email sink got the test message, authenticated as the channel user" \
	vm "grep -q 'Test notification' $SMTPLOG && grep -q 'AUTH $SMTP_USER' $SMTPLOG"

# ---- 4. the secret at rest and in every answer ------------------------------
step 4 "the credentials are sealed and never leave the panel"
assert_ok "the stored webhook secret is sealed (v1:)" \
	vm "sqlite3 $DB \"SELECT secret FROM notify_channels WHERE name = '$CHAN_HOOK'\" | grep -q '^v1:'"
assert_ok "the stored secrets hold neither plain value" \
	vm "! sqlite3 $DB 'SELECT secret FROM notify_channels' | grep -q -e '$HMAC_KEY' -e '$SMTP_PASS'"
api_call GET "$host" "$port" /api/notify/channels >/dev/null
assert_not_contains "the channel list does not carry the HMAC key" "$HMAC_KEY" "$(cat "$SMOKE_TMP/last.json")"
assert_not_contains "the channel list does not carry the SMTP password" "$SMTP_PASS" "$(cat "$SMOKE_TMP/last.json")"
assert_contains "the channel list says the secret is set" '"hmac_secret":true' "$(cat "$SMOKE_TMP/last.json")"
assert_ok "neither secret is in the audit log or the panel journal" \
	vm "! grep -rq -e '$HMAC_KEY' -e '$SMTP_PASS' /www/panel/logs 2>/dev/null && ! journalctl -u panel --no-pager 2>/dev/null | grep -q -e '$HMAC_KEY' -e '$SMTP_PASS'"

# ---- 5. cron.failed: open, dedupe, resolve ----------------------------------
step 5 "a failing cron job opens cron.failed; a repeat is deduped; a success resolves it"
api_call POST "$host" "$port" /api/cron "$(job_spec "$JOB_FAIL" '["/bin/false"]')" >/dev/null
FAIL_ID=$(jget id)
before=$(rx_count)
run_job "$FAIL_ID"
wait_until 40 "the failure's webhook request" rx_at_least "$((before + 1))"
assert_equal "one webhook delivery for the first failure" "$((before + 1))" "$(rx_count)"
assert_ok "the failure is a cron.failed event" \
	vm "python3 $RUN/notify_verify.py $RX $HMAC_KEY | grep -q 'kind=cron.failed resolved=False'"

run_job "$FAIL_ID"
wait_until 30 "the repeat to be suppressed" deliveries_at_least "$HOOK_ID" suppressed 1
assert_equal "the repeat failure sends nothing (deduped)" "$((before + 1))" "$(rx_count)"

api_call PUT "$host" "$port" "/api/cron/$FAIL_ID" "$(job_spec "$JOB_FAIL" '["/bin/true"]')" >/dev/null
run_job "$FAIL_ID"
wait_until 40 "the resolution's webhook request" rx_at_least "$((before + 2))"
assert_ok "the success sends one resolved notification" \
	vm "python3 $RUN/notify_verify.py $RX $HMAC_KEY | grep -q 'kind=cron.failed resolved=True'"

# ---- 6. firewall.disabled through the sweep ---------------------------------
step 6 "notify-sweep raises firewall.disabled and resolves it when the firewall is back"
SWEEP_ID=$(job_id notify-sweep)
assert_not_equal "the notify-sweep job is seeded" "" "$SWEEP_ID"
before=$(rx_count)
if [ "$FW_WAS_ENABLED" = true ]; then
	assert_http "disable the firewall" 204 "$(api_call POST "$host" "$port" /api/firewall/disable)"
fi
run_job "$SWEEP_ID"
wait_until 40 "the sweep's webhook request" rx_at_least "$((before + 1))"
assert_ok "the sweep raised firewall.disabled" \
	vm "python3 $RUN/notify_verify.py $RX $HMAC_KEY | grep -q 'kind=firewall.disabled resolved=False'"

api_call POST "$host" "$port" /api/firewall/enable >/dev/null
assert_equal "enable the firewall answers its pending token" "1" "$(grep -c "token" "$SMOKE_TMP/last.json")"
FW_TOKEN=$(jget token)
assert_http "confirm the firewall" 204 "$(api_call POST "$host" "$port" /api/firewall/confirm "{\"token\":\"$FW_TOKEN\"}")"
run_job "$SWEEP_ID"
wait_until 40 "the resolution request" rx_at_least "$((before + 2))"
assert_ok "the sweep resolved firewall.disabled" \
	vm "python3 $RUN/notify_verify.py $RX $HMAC_KEY | grep -q 'kind=firewall.disabled resolved=True'"
if [ "$FW_WAS_ENABLED" != true ]; then
	# The VM started with the firewall off: put it back that way.
	assert_http "disable the firewall again, as it was" 204 "$(api_call POST "$host" "$port" /api/firewall/disable)"
fi

# ---- 7. certificate renewal failure: not covered here -----------------------
step 7 "tls.renew_failed"
log "  SKIP  needs a staging certificate that fails to renew; not run by this script"

# ---- 8. quiet hours ---------------------------------------------------------
step 8 "quiet hours hold a warning back; the window is local to the VM"
from=$(vm "date -d '-2 min' +%H:%M" 2>/dev/null | tr -d ' \r')
to=$(vm "date -d '+60 min' +%H:%M" 2>/dev/null | tr -d ' \r')
log "  quiet window $from-$to (VM time)"
assert_http "open a quiet window around now on the webhook channel" 200 \
	"$(channel_put "$HOOK_ID" "$CHAN_HOOK" webhook "{\"url\":\"$HOOK_URL\",\"sign\":true}" "$from" "$to")"
api_call POST "$host" "$port" /api/cron "$(job_spec "$JOB_QUIET" '["/bin/false"]')" >/dev/null
QUIET_ID=$(jget id)
before=$(rx_count)
run_job "$QUIET_ID"
wait_until 40 "the quiet suppression" deliveries_at_least "$HOOK_ID" suppressed 2
assert_equal "no webhook request inside the quiet window" "$before" "$(rx_count)"
log "  SKIP  a critical event through the quiet window needs a critical producer (see step 7)"
assert_http "close the quiet window" 200 "$(channel_put "$HOOK_ID" "$CHAN_HOOK" webhook "{\"url\":\"$HOOK_URL\",\"sign\":true}" "" "")"

# ---- 9. retry across an outage and a panel restart --------------------------
step 9 "a delivery survives a receiver outage and a panel restart"
stop_helper notify_receiver
api_call POST "$host" "$port" /api/cron "$(job_spec "$JOB_RETRY" '["/bin/false"]')" >/dev/null
RETRY_ID=$(jget id)
run_job "$RETRY_ID"
wait_until 60 "the first attempt to be recorded" deliveries_at_least "$HOOK_ID" queued 1
events_before=$(events_count)
assert_ok "attempts grow while the receiver is down" wait_until 90 "a second attempt" attempts_at_least_two

assert_ok "restart the panel service" vm "systemctl restart panel && sleep 3 && systemctl is-active panel | grep -q active"
assert_http "log in again after the restart" 200 "$(api_login "$host" "$port" "$SMOKE_USER" "$pass")"
start_helper notify_receiver notify_receiver.py "$RXPORT" "$RX" "$FAILF"
wait_until 15 "the receiver to listen again" vm "ss -ltn | grep -q '127.0.0.1:$RXPORT'"
wait_until 150 "the queued delivery to be sent" deliveries_at_least "$HOOK_ID" sent 2
assert_equal "the restart and the redelivery raised no new event" "$events_before" "$(events_count)"

# ---- 10. the bell and the live stream ---------------------------------------
step 10 "the bell counts agree with the list, and the live stream yields the new event"
api_call GET "$host" "$port" /api/notify/summary >/dev/null
unread=$(jget unread)
api_call GET "$host" "$port" "/api/notify/events?unread=1&limit=200" >/dev/null
listed=$(grep -o '"id":' "$SMOKE_TMP/last.json" | wc -l | tr -d ' ')
assert_equal "the badge count equals the unread events listed" "$unread" "$listed"

curl -sk -N -m 40 -b "$COOKIE_JAR" "https://$host:$port/api/notify/stream" > "$SMOKE_TMP/stream.txt" 2>/dev/null &
STREAM_PID=$!
sleep 2
api_call POST "$host" "$port" "/api/cron/$RETRY_ID/run" >/dev/null
wait "$STREAM_PID" 2>/dev/null || true
assert_contains "the stream yields a notification event" "event: notification" "$(cat "$SMOKE_TMP/stream.txt")"

# ---- finish -----------------------------------------------------------------
step 11 "clean up"
cleanup
smoke_finish
