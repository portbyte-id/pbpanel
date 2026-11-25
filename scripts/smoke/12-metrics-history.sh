#!/bin/sh
# Smoke 12: metrics history and threshold alerts, end to end, against a VM the
# panel is installed on. Plan: docs/design/metrics-history-plan.md section 6.
#
#   SMOKE_HOST=root@10.10.11.106 sh scripts/smoke/12-metrics-history.sh
#   SMOKE_DRY_RUN=1 sh scripts/smoke/12-metrics-history.sh   # print the plan only
#
# It checks, in order: the history database and its status route; that a load shows
# up in the host and disk charts (and in a site's chart when SMOKE_SITE is set); a
# disk alert that fires once and resolves once (a short test rule, so the run takes
# minutes and not the built-in 10); a service alert; a daemon restart that neither
# opens a gap nor double counts a site minute; the retention setting; and the off
# switch. Items 7 and 8 of the plan's host gate (reboot, the 24 hour soak) are not
# automated here: 12-metrics-soak.sh records the soak, and the reboot is a manual
# step listed in the plan.
#
# DISRUPTIVE: the service named by SMOKE_SERVICE is stopped for a few minutes and
# started again, the panel service is restarted once, and a file is allocated on the
# disk the panel data lives on to push it past the test rule's threshold. Use a
# disposable VM. Everything created is removed, and the settings it changes are put
# back, on exit.
#
# Environment:
#   SMOKE_HOST          user@host of the VM; the user must be root
#   SMOKE_PASSWORD      admin password. When unset the script runs
#                       `panel reset-password` on the VM, which signs everyone out.
#   SMOKE_USER          admin username (default admin)
#   SMOKE_SSH_OPTS      extra ssh options
#   SMOKE_SERVICE       service stopped for the service alert (default panel-supervisord)
#   SMOKE_SITE          a site's domain on the VM: adds the traffic checks
#   SMOKE_LOAD_SECONDS  how long the load runs (default 150)
#
# Needs python3 on the workstation, to read the JSON the panel answers.

set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

SMOKE_USER=${SMOKE_USER:-admin}
SMOKE_SERVICE=${SMOKE_SERVICE:-panel-supervisord}
SMOKE_SITE=${SMOKE_SITE:-}
SMOKE_LOAD_SECONDS=${SMOKE_LOAD_SECONDS:-150}
PANEL=/www/panel/bin/pbp
DATADIR=/www/panel/data
FILL=$DATADIR/smoke12-fill

smoke_init
host=$(vm_host)
port=
pass=
DISK_RULE=
SVC_RULE=
OLD_ENABLED=
OLD_RAW=
CLEANED=0

# pyjson EXPR: evaluate a python expression over the last API answer, as `d`.
pyjson() {
	python3 -c "import json,sys; d=json.load(open(sys.argv[1])); print($1)" "$SMOKE_TMP/last.json" 2>/dev/null
}

# setting NAME: the current value of a setting.
setting() {
	api_call GET "$host" "$port" /api/settings >/dev/null
	pyjson "[s['value'] for s in (d['settings'] if isinstance(d,dict) else d) if s['name']=='$1'][0]"
}

# set_setting NAME VALUE: prints the HTTP status.
set_setting() {
	api_call PUT "$host" "$port" /api/settings "{\"settings\":[{\"name\":\"$1\",\"value\":\"$2\"}]}"
}

# panel_pid: the daemon's PID on the VM.
panel_pid() { vm "systemctl show -p MainPID --value panel" | tr -d ' \r'; }

# write_bytes PID: the process's cumulative write_bytes.
write_bytes() { vm "sed -n 's/^write_bytes: //p' /proc/$1/io" | tr -d ' \r'; }

# open_count RULE_ID: how many targets of the rule are firing or clearing right now.
open_count() {
	api_call GET "$host" "$port" /api/alerts >/dev/null
	pyjson "len([s for r in d['rules'] if r['id']==$1 for s in r['states'] if s['state'] in ('firing','clearing')])"
}

# alert_open RULE_ID and alert_closed RULE_ID are predicates for wait_until, which
# runs them again on every poll.
alert_open() { [ "$(open_count "$1")" -ge 1 ]; }
alert_closed() { [ "$(open_count "$1")" -eq 0 ]; }

# events_for RULE_ID RESOLVED: notification events this rule emitted, fired (False) or resolved (True).
events_for() {
	api_call GET "$host" "$port" "/api/notify/events?limit=200" >/dev/null
	pyjson "len([e for e in (d['events'] if isinstance(d,dict) else d) if e['key'].startswith('metrics.threshold:$1:') and e['resolved']==$2])"
}

# series_max SERIES: the largest non-null value of a host series in the last hour.
series_max() {
	api_call GET "$host" "$port" "/api/metrics/host?range=1h" >/dev/null
	pyjson "max([v for v in d['$1'] if v is not None] or [0])"
}

# num_ge A B: A >= B as numbers.
num_ge() { python3 -c "import sys; sys.exit(0 if float(sys.argv[1]) >= float(sys.argv[2]) else 1)" "$1" "$2"; }

cleanup() {
	[ "$CLEANED" = 1 ] && return
	CLEANED=1
	vm "rm -f $FILL $DATADIR/smoke12-dd /tmp/smoke12-load.sh; pkill -f smoke12-load 2>/dev/null; true" >/dev/null 2>&1 || true
	vm "systemctl start $SMOKE_SERVICE" >/dev/null 2>&1 || true
	if [ "$SMOKE_DRY_RUN" != 1 ] && [ -n "$port" ]; then
		api_login "$host" "$port" "$SMOKE_USER" "$pass" >/dev/null 2>&1 || true
		for id in "$DISK_RULE" "$SVC_RULE"; do
			[ -n "$id" ] && api_call DELETE "$host" "$port" "/api/alerts/rules/$id" >/dev/null 2>&1
		done
		[ -n "$OLD_ENABLED" ] && set_setting metrics.enabled "$OLD_ENABLED" >/dev/null 2>&1
		[ -n "$OLD_RAW" ] && set_setting metrics.raw_retention "$OLD_RAW" >/dev/null 2>&1
	fi
}
trap cleanup EXIT INT TERM

if [ "$SMOKE_DRY_RUN" != 1 ] && ! command -v python3 >/dev/null 2>&1; then
	log "python3 is required on the workstation"
	exit 2
fi

# ---- 1. login and the database ------------------------------------------------
step 1 "log in, and the history database is there"
port=$(vm "$PANEL info" 2>/dev/null | sed -n 's/^ *port  *\([0-9][0-9]*\).*/\1/p' | head -n 1)
[ "$SMOKE_DRY_RUN" = 1 ] && port=8443
pass=${SMOKE_PASSWORD:-}
if [ -z "$pass" ] && [ "$SMOKE_DRY_RUN" != 1 ]; then
	reset_out=$(vm "$PANEL reset-password" 2>&1)
	pass=$(printf '%s\n' "$reset_out" | sed -n 's/^ *\([A-Za-z0-9]\{12,\}\) *$/\1/p' | head -n 1)
fi
assert_http "login" 200 "$(api_login "$host" "$port" "$SMOKE_USER" "$pass")"
assert_ok "metrics.db exists next to panel.db" vm "test -s $DATADIR/metrics.db"
assert_http "GET /api/metrics/status" 200 "$(api_call GET "$host" "$port" /api/metrics/status)"
assert_equal "recording is on" "True" "$(pyjson "d['enabled']")"
OLD_ENABLED=$(setting metrics.enabled)
OLD_RAW=$(setting metrics.raw_retention)

pid0=$(panel_pid)
rss0=$(vm "ps -o rss= -p $pid0" | tr -d ' \r')
log "  baseline: panel pid $pid0, RSS ${rss0} KiB"

# ---- 2. a load shows up in the charts -----------------------------------------
step 2 "a ${SMOKE_LOAD_SECONDS}s CPU, memory and disk load shows in the charts"
vm "cat > /tmp/smoke12-load.sh <<'EOS'
#!/bin/sh
end=\$(( \$(date +%s) + $SMOKE_LOAD_SECONDS ))
if command -v stress-ng >/dev/null 2>&1; then
  stress-ng --cpu 1 --vm 1 --vm-bytes 40% --timeout ${SMOKE_LOAD_SECONDS}s >/dev/null 2>&1 &
else
  ( while [ \$(date +%s) -lt \$end ]; do : ; done ) &
fi
while [ \$(date +%s) -lt \$end ]; do dd if=/dev/zero of=$DATADIR/smoke12-dd bs=1M count=64 conv=fsync 2>/dev/null; sleep 1; done
rm -f $DATADIR/smoke12-dd
EOS
chmod +x /tmp/smoke12-load.sh; nohup sh /tmp/smoke12-load.sh smoke12-load >/dev/null 2>&1 &"
if [ -n "$SMOKE_SITE" ]; then
	vm "i=0; while [ \$i -lt $SMOKE_LOAD_SECONDS ]; do curl -sk -o /dev/null --resolve $SMOKE_SITE:443:127.0.0.1 https://$SMOKE_SITE/; curl -sk -o /dev/null --resolve $SMOKE_SITE:443:127.0.0.1 https://$SMOKE_SITE/does-not-exist-smoke12; i=\$((i+1)); sleep 1; done" >/dev/null 2>&1 &
fi
# The load, then the last minute waiting out the flush and the rollup grace.
[ "$SMOKE_DRY_RUN" = 1 ] || sleep $((SMOKE_LOAD_SECONDS + 90))
cpu_max=$(series_max cpu_max)
assert_ok "the CPU chart shows the load (max ${cpu_max}%, want 50 or more)" num_ge "${cpu_max:-0}" 50
api_call GET "$host" "$port" "/api/metrics/disks?range=1h" >/dev/null
wr_max=$(pyjson "max([v for m in d['mounts'] for v in m['wr_bps'] if v is not None] or [0])")
assert_ok "the disk IO chart shows the writes (max ${wr_max} B/s, want 1000000 or more)" num_ge "${wr_max:-0}" 1000000
log "  info  network rx max $(series_max net_rx) B/s (informational)"
assert_http "GET /api/metrics/services" 200 "$(api_call GET "$host" "$port" "/api/metrics/services?range=1h")"
assert_contains "the service history names the panel" '"panel"' "$(cat "$SMOKE_TMP/last.json" 2>/dev/null)"
if [ -n "$SMOKE_SITE" ]; then
	assert_http "GET /api/metrics/sites/$SMOKE_SITE" 200 "$(api_call GET "$host" "$port" "/api/metrics/sites/$SMOKE_SITE?range=1h")"
	assert_ok "the site chart counted requests (want 100 or more)" num_ge "$(pyjson "sum([v for v in d['req'] if v])")" 100
	assert_ok "the site chart counted the 404s (want 50 or more)" num_ge "$(pyjson "sum([v for v in d['s4'] if v])")" 50
else
	log "  SKIP  site traffic: SMOKE_SITE is not set"
fi

# ---- 3. a disk alert fires once and resolves once -----------------------------
step 3 "a disk alert fires exactly once and resolves exactly once"
mount=$(vm "df --output=target $DATADIR | tail -n 1" | tr -d ' \r')
used_pct=$(vm "df --output=pcent $DATADIR | tail -n 1 | tr -d ' %\r'")
[ "$SMOKE_DRY_RUN" = 1 ] && used_pct=50
thr=$((${used_pct:-50} + 3))
clr=$((${used_pct:-50} + 1))
if [ "$thr" -gt 99 ]; then thr=99; clr=98; fi
log "  mount $mount is at ${used_pct}%: the test rule fires at ${thr}% and clears at ${clr}%"
api_call POST "$host" "$port" /api/alerts/rules \
	"{\"kind\":\"disk_pct\",\"target\":\"$mount\",\"threshold\":$thr,\"clear_threshold\":$clr,\"for_seconds\":60,\"clear_for_seconds\":60,\"severity\":\"warning\"}" >/dev/null
DISK_RULE=$(pyjson "d['id']")
assert_ok "the test rule was created" test -n "$DISK_RULE"
need=$(vm "df -B1 --output=size,used,avail $DATADIR | tail -n 1" | awk -v t="$thr" '{ want=int($1*(t+1)/100)-$2; if (want < 0) want=0; print want, $3 }')
fill_bytes=${need% *}
avail=${need#* }
if [ "$SMOKE_DRY_RUN" = 1 ] || [ "${avail:-0}" -gt $((${fill_bytes:-0} + 1073741824)) ]; then
	assert_ok "allocate ${fill_bytes} bytes" vm "fallocate -l ${fill_bytes:-0} $FILL"
	assert_ok "the rule fires within 6 minutes" wait_until 360 "the alert to fire" alert_open "$DISK_RULE"
	assert_equal "exactly one fire event" "1" "$(events_for "$DISK_RULE" False)"
	vm "rm -f $FILL" >/dev/null
	assert_ok "the rule resolves within 6 minutes of freeing the space" wait_until 360 "the alert to resolve" alert_closed "$DISK_RULE"
	assert_equal "exactly one resolve event" "1" "$(events_for "$DISK_RULE" True)"
	assert_equal "still exactly one fire event" "1" "$(events_for "$DISK_RULE" False)"
else
	log "  SKIP  not enough free space to fill the disk safely (needs ${fill_bytes} bytes plus 1 GiB)"
fi

# ---- 4. a service alert --------------------------------------------------------
step 4 "stopping $SMOKE_SERVICE fires service_down, starting it resolves"
api_call POST "$host" "$port" /api/alerts/rules \
	"{\"kind\":\"service_down\",\"target\":\"$SMOKE_SERVICE\",\"threshold\":100,\"clear_threshold\":0,\"for_seconds\":60,\"clear_for_seconds\":60,\"severity\":\"warning\"}" >/dev/null
SVC_RULE=$(pyjson "d['id']")
assert_ok "stop $SMOKE_SERVICE" vm "systemctl stop $SMOKE_SERVICE"
assert_ok "service_down fires" wait_until 300 "the alert to fire" alert_open "$SVC_RULE"
assert_ok "start $SMOKE_SERVICE" vm "systemctl start $SMOKE_SERVICE"
assert_ok "service_down resolves" wait_until 300 "the alert to resolve" alert_closed "$SVC_RULE"
assert_equal "one fire and one resolve" "1 1" "$(events_for "$SVC_RULE" False) $(events_for "$SVC_RULE" True)"

# ---- 5. a daemon restart --------------------------------------------------------
step 5 "a restart leaves no gap beyond the restart window and does not double count"
before=0
if [ -n "$SMOKE_SITE" ]; then
	api_call GET "$host" "$port" "/api/metrics/sites/$SMOKE_SITE?range=1h" >/dev/null
	before=$(pyjson "int(sum([v for v in d['req'] if v]))")
fi
assert_ok "restart the panel" vm "systemctl restart panel && sleep 4 && systemctl is-active panel | grep -q active"
assert_http "log in again" 200 "$(api_login "$host" "$port" "$SMOKE_USER" "$pass")"
[ "$SMOKE_DRY_RUN" = 1 ] || sleep 150
api_call GET "$host" "$port" "/api/metrics/host?range=1h" >/dev/null
gaps=$(pyjson "len([1 for v in d['cpu'][-12:] if v is None])")
assert_ok "at most 6 empty buckets in the newest 12 after the restart (found ${gaps})" test "${gaps:-99}" -le 6
if [ -n "$SMOKE_SITE" ]; then
	api_call GET "$host" "$port" "/api/metrics/sites/$SMOKE_SITE?range=1h" >/dev/null
	after=$(pyjson "int(sum([v for v in d['req'] if v]))")
	lines=$(vm "wc -l < /www/panel/logs/sites/$SMOKE_SITE.access.log" | tr -d ' \r')
	log "  site requests before $before, after $after, access log lines $lines"
	assert_ok "no request was counted twice (after <= log lines)" test "${after:-0}" -le "${lines:-0}"
	assert_ok "the history kept the earlier minutes" test "${after:-0}" -ge "${before:-0}"
fi

# ---- 6. retention ---------------------------------------------------------------
step 6 "the retention setting is bounded"
assert_http "metrics.raw_retention=1h" 200 "$(set_setting metrics.raw_retention 1h)"
assert_http "an out of range retention is refused" 422 "$(set_setting metrics.raw_retention 200h)"
assert_http "restore metrics.raw_retention" 200 "$(set_setting metrics.raw_retention "$OLD_RAW")"
log "  SKIP  the sweep runs hourly: with 1h set, host_10s holds at most an hour after the next hour (manual, see the plan)"

# ---- 7. the off switch ----------------------------------------------------------
step 7 "metrics.enabled=false stops the writes and keeps the live dashboard"
assert_http "turn recording off" 200 "$(set_setting metrics.enabled false)"
pid=$(panel_pid)
w1=$(write_bytes "$pid")
[ "$SMOKE_DRY_RUN" = 1 ] || sleep 130
w2=$(write_bytes "$pid")
[ "$SMOKE_DRY_RUN" = 1 ] && w1=0 && w2=0
delta=$((${w2:-0} - ${w1:-0}))
log "  panel write_bytes grew by $delta over about 130 s"
assert_ok "writes stay small while recording is off (under 256 KiB)" test "$delta" -lt 262144
assert_http "the chart route says it is off" 409 "$(api_call GET "$host" "$port" /api/metrics/host)"
assert_http "the live dashboard is unchanged" 200 "$(api_call GET "$host" "$port" /api/sysinfo)"
assert_http "turn recording back on" 200 "$(set_setting metrics.enabled "$OLD_ENABLED")"
rss1=$(vm "ps -o rss= -p $(panel_pid)" | tr -d ' \r')
log "  RSS now ${rss1} KiB (baseline ${rss0} KiB); the 8 MiB bound is judged by the soak"

step 8 "clean up"
cleanup
smoke_finish
