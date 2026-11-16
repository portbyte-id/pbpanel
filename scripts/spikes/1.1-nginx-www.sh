#!/bin/sh
# Spike for development plan step 1.1: how does the `www` account (fixed UID/GID
# 1500) get into the official nginx image?
#
#   A. derived image   FROM nginx:<ver> + groupadd/useradd
#   B. mounted passwd  the image's /etc/passwd and /etc/group plus one line,
#                      bind-mounted read-only over the originals
#
# For each variant it also answers the three questions the step lists:
#   - host networking works (curl from the host, no published port)
#   - `nginx -t` runs against the mounted config, by exec and by a one-shot container
#   - reload works by signal to the main process (no exec'd shell)
#
# Throwaway: it needs a Docker host and nothing else. Usage:
#   sh scripts/spikes/1.1-nginx-www.sh [image]        default nginx:1.29
set -eu

IMAGE=${1:-nginx:1.29}
ROOT=${SPIKE_ROOT:-/tmp/panel-spike}
PORT=${SPIKE_PORT:-18080}
UID_WWW=1500
SOCK=/var/run/docker.sock

say() { printf '\n== %s\n' "$*"; }
ok() { printf 'ok   %s\n' "$*"; }
bad() {
	printf 'FAIL %s\n' "$*"
	FAILED=1
}
FAILED=0

cleanup() {
	docker rm -f spike-nginx-a spike-nginx-b >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

rm -rf "$ROOT"
mkdir -p "$ROOT/panel/nginx/sites" "$ROOT/panel/logs" "$ROOT/services/sites/a.test" "$ROOT/build"
echo "spike-content" >"$ROOT/services/sites/a.test/index.html"
chmod -R a+rX "$ROOT"

write_conf() {
	# $1: server_name marker so a reload is observable
	cat >"$ROOT/panel/nginx/nginx.conf" <<EOF
user www;
worker_processes 1;
pid /run/nginx.pid;
error_log $ROOT/panel/logs/error.log;
events { worker_connections 64; }
http {
    include /etc/nginx/mime.types;
    access_log $ROOT/panel/logs/access.log;
    include $ROOT/panel/nginx/sites/*.conf;
}
EOF
	cat >"$ROOT/panel/nginx/sites/a.test.conf" <<EOF
server {
    listen $PORT;
    server_name a.test;
    root $ROOT/services/sites/a.test;
    add_header X-Spike "$1" always;
}
EOF
}

check_variant() {
	# $1 container name
	name=$1
	sleep 1
	if [ "$(docker inspect -f '{{.State.Running}}' "$name")" = true ]; then ok "$name running"; else
		bad "$name not running"
		docker logs "$name" 2>&1 | tail -5
		return
	fi

	workers=$(docker top "$name" -eo pid,uid,args | awk '/worker process/ {print $2}' | sort -u)
	if [ "$workers" = "$UID_WWW" ]; then ok "worker uid is $UID_WWW"; else bad "worker uid is '$workers'"; fi

	body=$(curl -s -H 'Host: a.test' "http://127.0.0.1:$PORT/" || true)
	if [ "$body" = "spike-content" ]; then ok "host network: served from the host without -p"; else bad "curl got '$body'"; fi

	if docker exec "$name" nginx -t -c "$ROOT/panel/nginx/nginx.conf" >/dev/null 2>&1; then ok "nginx -t by exec"; else bad "nginx -t by exec"; fi

	# Broken config: -t must fail and say why, the running server must not notice.
	echo "server { listen $PORT; bogus_directive on; }" >"$ROOT/panel/nginx/sites/broken.conf"
	out=$(docker exec "$name" nginx -t -c "$ROOT/panel/nginx/nginx.conf" 2>&1) && bad "nginx -t accepted a broken file" ||
		ok "nginx -t rejects: $(printf '%s' "$out" | head -1)"
	rm "$ROOT/panel/nginx/sites/broken.conf"

	# Reload by signal to PID 1 of the container.
	write_conf "reloaded-$name"
	docker kill -s HUP "$name" >/dev/null
	sleep 1
	hdr=$(curl -s -o /dev/null -D - -H 'Host: a.test' "http://127.0.0.1:$PORT/" | tr -d '\r' | awk -F': ' 'tolower($1)=="x-spike" {print $2}')
	if [ "$hdr" = "reloaded-$name" ]; then ok "reload by docker kill -s HUP"; else bad "reload: header is '$hdr'"; fi

	log_owner=$(stat -c %u "$ROOT/panel/logs/access.log")
	ok "access.log owner uid $log_owner (opened by the master)"
}

docker image inspect "$IMAGE" >/dev/null 2>&1 || docker pull "$IMAGE"

say "image facts"
docker run --rm --entrypoint cat "$IMAGE" /etc/os-release | sed -n 1,2p
docker run --rm --entrypoint sh "$IMAGE" -c 'getent passwd nginx; command -v useradd adduser || true'
docker image inspect -f 'entrypoint={{json .Config.Entrypoint}} cmd={{json .Config.Cmd}} stopsignal={{.Config.StopSignal}}' "$IMAGE"

# --- A: derived image -------------------------------------------------------
say "A. derived image"
if docker run --rm --entrypoint sh "$IMAGE" -c 'command -v useradd' >/dev/null 2>&1; then
	ADD="groupadd -g $UID_WWW www && useradd -u $UID_WWW -g www -M -d /nonexistent -s /usr/sbin/nologin www"
else
	ADD="addgroup -g $UID_WWW www && adduser -u $UID_WWW -G www -H -D -s /sbin/nologin www"
fi
printf 'FROM %s\nRUN %s\n' "$IMAGE" "$ADD" >"$ROOT/build/Dockerfile"
cat "$ROOT/build/Dockerfile"

# Build through the Engine API the way the panel will: a tar context POSTed to
# /build, no CLI, no BuildKit session. The stream is not trusted: the tag is
# inspected afterwards.
tar -C "$ROOT/build" -cf "$ROOT/build.tar" Dockerfile
start=$(date +%s)
curl -s --unix-socket "$SOCK" -X POST -H 'Content-Type: application/x-tar' \
	--data-binary @"$ROOT/build.tar" \
	"http://d/v1.44/build?t=panel/nginx:spike&rm=1&forcerm=1" >"$ROOT/build.log"
tail -2 "$ROOT/build.log"
if docker image inspect panel/nginx:spike >/dev/null 2>&1; then
	ok "API build without BuildKit session ($(($(date +%s) - start))s, $(docker image inspect -f '{{.Size}}' panel/nginx:spike) bytes)"
else
	bad "API build produced no image"
fi

write_conf "initial"
# One-shot validation before any long-lived container exists.
if docker run --rm --network host --entrypoint nginx \
	-v "$ROOT/panel:$ROOT/panel" -v "$ROOT/services/sites:$ROOT/services/sites:ro" \
	panel/nginx:spike -t -c "$ROOT/panel/nginx/nginx.conf" >/dev/null 2>&1; then ok "nginx -t by one-shot container"; else bad "nginx -t by one-shot container"; fi

docker run -d --name spike-nginx-a --network host --restart unless-stopped \
	--entrypoint nginx \
	-v "$ROOT/panel/nginx:$ROOT/panel/nginx:ro" \
	-v "$ROOT/panel/logs:$ROOT/panel/logs" \
	-v "$ROOT/services/sites:$ROOT/services/sites:ro" \
	panel/nginx:spike -c "$ROOT/panel/nginx/nginx.conf" -g 'daemon off;' >/dev/null
check_variant spike-nginx-a
# The image's own config tree is still there because nothing is mounted over it.
if docker exec spike-nginx-a test -f /etc/nginx/mime.types; then ok "image /etc/nginx intact"; else bad "image /etc/nginx hidden"; fi
docker rm -f spike-nginx-a >/dev/null

# --- B: mounted passwd ------------------------------------------------------
say "B. mounted passwd/group"
docker run --rm --entrypoint cat "$IMAGE" /etc/passwd >"$ROOT/build/passwd"
docker run --rm --entrypoint cat "$IMAGE" /etc/group >"$ROOT/build/group"
echo "www:x:$UID_WWW:$UID_WWW::/nonexistent:/usr/sbin/nologin" >>"$ROOT/build/passwd"
echo "www:x:$UID_WWW:" >>"$ROOT/build/group"
write_conf "initial"
docker run -d --name spike-nginx-b --network host --restart unless-stopped \
	--entrypoint nginx \
	-v "$ROOT/build/passwd:/etc/passwd:ro" -v "$ROOT/build/group:/etc/group:ro" \
	-v "$ROOT/panel/nginx:$ROOT/panel/nginx:ro" \
	-v "$ROOT/panel/logs:$ROOT/panel/logs" \
	-v "$ROOT/services/sites:$ROOT/services/sites:ro" \
	"$IMAGE" -c "$ROOT/panel/nginx/nginx.conf" -g 'daemon off;' >/dev/null
check_variant spike-nginx-b
docker rm -f spike-nginx-b >/dev/null

docker rmi panel/nginx:spike >/dev/null 2>&1 || true
say "result"
[ "$FAILED" -eq 0 ] && echo "all checks passed" || echo "some checks FAILED"
exit "$FAILED"
