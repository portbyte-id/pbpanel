#!/bin/sh
# Soak recorder for the metrics history (docs/design/metrics-history-plan.md section 6,
# item 8). It does not assert anything: it samples the panel's resident memory, the
# size of metrics.db and its WAL, and the daemon's cumulative write_bytes, so the
# plan's pass criteria can be judged from the file it writes.
#
#   SMOKE_HOST=root@10.10.11.106 sh scripts/smoke/12-metrics-soak.sh soak.csv
#
# Run it for 24 hours next to a light request loop (`hey` or `ab`) against a site.
# Samples go to the CSV named by the first argument, one line per sample, and the
# script prints each line as well. Interrupt it with Ctrl-C when the time is up.
#
# The pass criteria, from the plan:
#   - RSS grows by at most 8 MiB over the build before the feature, and is flat
#     between T+6 h and T+24 h
#   - metrics.db plus its -wal stay within 25 percent of the plan's 24 hour size
#     projection for this host's service and site count
#   - write_bytes grows by at most 0.5 GiB a day
#   - the WAL stays under 4 MiB
#
# Environment:
#   SMOKE_HOST            user@host of the VM
#   SMOKE_SSH_OPTS        extra ssh options
#   SMOKE_SOAK_INTERVAL   seconds between samples (default 3600; 600 for a short run)

set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

out=${1:-}
if [ -z "$out" ]; then
	log "usage: SMOKE_HOST=user@host sh $0 OUTPUT.csv"
	exit 2
fi
interval=${SMOKE_SOAK_INTERVAL:-3600}
DATADIR=/www/panel/data

smoke_init
[ -s "$out" ] || printf 'time,rss_kib,db_bytes,wal_bytes,write_bytes\n' > "$out"

while :; do
	pid=$(vm "systemctl show -p MainPID --value panel" | tr -d ' \r')
	rss=$(vm "ps -o rss= -p $pid" | tr -d ' \r')
	db=$(vm "stat -c %s $DATADIR/metrics.db 2>/dev/null || echo 0" | tr -d ' \r')
	wal=$(vm "stat -c %s $DATADIR/metrics.db-wal 2>/dev/null || echo 0" | tr -d ' \r')
	wb=$(vm "sed -n 's/^write_bytes: //p' /proc/$pid/io" | tr -d ' \r')
	line="$(date -u +%Y-%m-%dT%H:%M:%SZ),$rss,$db,$wal,$wb"
	printf '%s\n' "$line" | tee -a "$out"
	[ "$SMOKE_DRY_RUN" = 1 ] && break
	sleep "$interval"
done
