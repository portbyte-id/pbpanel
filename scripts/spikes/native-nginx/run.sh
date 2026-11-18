#!/usr/bin/env bash
# Spike driver: build the native nginx prefix in a builder image matching this
# host, extract it, and prove it runs here without any of the build dependencies.
# Run inside the Linux host (WSL), not on Windows.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
. /etc/os-release
BASE=${BASE:-$ID:$VERSION_ID}
BUILD_ID=${BUILD_ID:-spike}
OUT=${OUT:-$HOME/native-nginx-spike}
ROOT=/www/panel/nginx
P=$ROOT/versions/$BUILD_ID

echo "== builder image: $BASE"
rm -rf "$OUT"; mkdir -p "$OUT"
docker build --build-arg "BASE=$BASE" --build-arg "BUILD_ID=$BUILD_ID" \
  --target out --output "type=local,dest=$OUT" "$here"

T=$OUT$P
echo "== extracted tree"; (cd "$T" && find . -maxdepth 2 | sort | head -50; du -sh .)

echo "== ldd outside the bundle (anything other than glibc or lib/ is a leak)"
export LD_LIBRARY_PATH=
for b in "$T/sbin/nginx" "$T"/modules/*.so; do
  echo "-- $b"; ldd "$b" | sed "s#$T#<prefix>#" | grep -vE '<prefix>/lib/|linux-vdso' || true
  if ldd "$b" | grep -q 'not found'; then echo "MISSING LIBRARY"; exit 1; fi
done

echo "== run in a clean $BASE container (no -dev packages), prefix mounted at the same path"
mkdir -p "$OUT/conf"
cat > "$OUT/conf/nginx.conf" <<EOF
load_module $P/modules/ngx_http_modsecurity_module.so;
worker_processes 1;
pid /tmp/nginx-spike.pid;
error_log /dev/stderr info;
events { worker_connections 64; }
http {
  access_log off;
  server {
    listen 8099;
    modsecurity on;
    modsecurity_rules 'SecRuleEngine On
SecRule ARGS:probe "@streq block" "id:1,phase:1,deny,status:403"';
    location / { return 200 "ok\n"; }
  }
}
EOF
docker run --rm -v "$T:$P:ro" -v "$OUT/conf:/spike:ro" "$BASE" bash -c "
  set -e
  $P/sbin/nginx -V 2>&1 | grep -E 'nginx version|built with OpenSSL'
  id www >/dev/null 2>&1 || useradd -r -M www
  mkdir -p /www/panel/logs /www/panel/run /www/panel/cache/nginx
  $P/sbin/nginx -t -c /spike/nginx.conf
  $P/sbin/nginx -c /spike/nginx.conf &
  sleep 1
  get() { timeout 3 bash -c \"exec 3<>/dev/tcp/127.0.0.1/8099; printf 'GET \$1 HTTP/1.0\r\n\r\n' >&3; head -1 <&3\"; }
  echo \"GET / -> \$(get /)\"
  echo \"GET /?probe=block -> \$(get '/?probe=block')\"
  $P/sbin/nginx -s quit -c /spike/nginx.conf
"
echo "== SPIKE OK"
