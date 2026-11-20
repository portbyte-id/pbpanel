#!/bin/sh
# Smoke 1: the M1 exit gate, end to end, against a VM the panel is installed on.
#
#   SMOKE_HOST=root@10.0.0.5 sh scripts/smoke/1-nginx-sites.sh
#   SMOKE_DRY_RUN=1 sh scripts/smoke/1-nginx-sites.sh       # print the plan only
#
# Run it after smoke 0 (or any `panel install`). It creates a static site and a
# reverse-proxy site, breaks a config and checks the rollback, detaches and
# regenerates a control, removes the nginx container by hand, restarts the
# daemon and reboots the VM. It deletes both sites at the end.
#
# Environment:
#   SMOKE_HOST       user@host of the VM; the user must be root
#   SMOKE_PASSWORD   admin password. When unset the script runs
#                    `panel reset-password` on the VM, which signs everyone out.
#   SMOKE_USER       admin username (default admin)
#   SMOKE_SSH_OPTS   extra ssh options
#   SMOKE_SKIP_REBOOT=1  skip the reboot step
#
# Independent oracle: "does the site serve" is answered by curl from the
# workstation to port 80 with a Host header, "is nginx up" by `systemctl` on
# the VM, and "is the file unchanged" by sha256sum on the VM. None of them is a
# panel endpoint.
#
# Needs port 80 of the VM reachable from the workstation, and python3 on the VM
# (it is the throwaway backend of the proxy site).

set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

SMOKE_USER=${SMOKE_USER:-admin}
SMOKE_SKIP_REBOOT=${SMOKE_SKIP_REBOOT:-0}
PANEL=/www/panel/bin/panel
STATIC=smoke-static.test
PROXY=smoke-proxy.test
BACKEND_PORT=18081
BACKEND_DIR=/var/lib/smoke-backend
BACKEND_UNIT=smoke-backend.service
STATIC_MARK="static-$$-marker"
PROXY_MARK="proxy-$$-marker"
SITE_FILE=/www/panel/nginx/sites/$STATIC.conf

smoke_init
host=$(vm_host)

# site_body DOMAIN: what nginx answers for that Host, from the workstation.
site_body() {
	if [ "$SMOKE_DRY_RUN" = 1 ]; then return 0; fi
	curl -s --max-time 10 -H "Host: $1" "http://$host/" 2>/dev/null || true
}

# site_status DOMAIN: the HTTP status, 000 when nginx drops the connection.
site_status() {
	http_status "http://$host/" -H "Host: $1"
}

# both_serve WHEN: the two sites answer with their own content.
both_serve() {
	assert_contains "static site serves its page ($1)" "$STATIC_MARK" "$(site_body "$STATIC")"
	assert_contains "proxy site serves the backend's page ($1)" "$PROXY_MARK" "$(site_body "$PROXY")"
}

last_body() { cat "$SMOKE_TMP/last.json" 2>/dev/null; }

NGINX=/www/panel/nginx/current/sbin/nginx

nginx_running() {
	[ "$(vm 'systemctl is-active panel-nginx' 2>/dev/null)" = active ]
}

panel_healthy() {
	[ "$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 "https://$host:$port/api/health")" = 200 ]
}

# --- 1. the panel, and nginx under it -----------------------------------------
step 1 "the panel is up and has brought nginx up"
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

# The first start compiles nginx in a builder container, which takes several
# minutes on a fresh VM.
wait_until 900 "nginx to run" nginx_running
assert_equal "systemd reports panel-nginx active" active "$(vm 'systemctl is-active panel-nginx' 2>/dev/null)"
assert_contains "the master runs a binary from a build directory (readlink /proc/PID/exe)" "/www/panel/nginx/versions/" \
	"$(vm 'readlink /proc/$(systemctl show -p MainPID --value panel-nginx)/exe' 2>/dev/null)"
assert_equal "no panel-nginx container is left (docker ps)" none \
	"$(vm "docker ps -a --filter name=^panel-nginx\$ --format '{{.Names}}' | grep -q . && echo present || echo none" 2>/dev/null)"
assert_http "GET /api/services/nginx" 200 "$(api_call GET "$host" "$port" /api/services/nginx)"
assert_contains "the boot reconcile finished without an error" '"ok":true' "$(last_body)"
assert_http "a host no site claims gets the landing page" 200 "$(site_status unclaimed.test)"

# --- 2. a backend for the proxy site ------------------------------------------
step 2 "start a throwaway backend on 127.0.0.1:$BACKEND_PORT (a unit, so it survives the reboot)"
vm "mkdir -p $BACKEND_DIR && printf '%s\n' '$PROXY_MARK' > $BACKEND_DIR/index.html"
vm "printf '%s\n' '[Unit]' 'Description=smoke 1 backend' '[Service]' 'WorkingDirectory=$BACKEND_DIR' 'ExecStart=/usr/bin/python3 -m http.server $BACKEND_PORT --bind 127.0.0.1' '[Install]' 'WantedBy=multi-user.target' > /etc/systemd/system/$BACKEND_UNIT && systemctl daemon-reload && systemctl enable --now $BACKEND_UNIT"
wait_until 20 "the backend to answer on the VM" vm "curl -sf http://127.0.0.1:$BACKEND_PORT/ >/dev/null"
assert_contains "the backend answers on the VM" "$PROXY_MARK" "$(vm "curl -s http://127.0.0.1:$BACKEND_PORT/" 2>/dev/null)"

# --- 3. create both sites -----------------------------------------------------
step 3 "create a static site and a reverse-proxy site"
assert_http "POST /api/sites (static)" 201 \
	"$(api_call POST "$host" "$port" /api/sites "{\"domain\":\"$STATIC\",\"aliases\":[\"www.$STATIC\"],\"kind\":\"static\"}")"
assert_http "POST /api/sites (proxy)" 201 \
	"$(api_call POST "$host" "$port" /api/sites "{\"domain\":\"$PROXY\",\"kind\":\"proxy\",\"proxy_target\":\"http://127.0.0.1:$BACKEND_PORT\"}")"
assert_http "a duplicate domain is refused" 409 \
	"$(api_call POST "$host" "$port" /api/sites "{\"domain\":\"$STATIC\",\"kind\":\"static\"}")"

assert_equal "the web root belongs to www (stat)" www "$(vm "stat -c %U /www/services/sites/$STATIC" 2>/dev/null)"
assert_contains "the placeholder page is served" "$STATIC" "$(site_body "$STATIC")"
vm "printf '%s\n' '$STATIC_MARK' > /www/services/sites/$STATIC/index.html"
both_serve "after create"
assert_contains "the alias serves the same site" "$STATIC_MARK" "$(site_body "www.$STATIC")"

# --- 4. a broken site file is refused and rolled back -------------------------
step 4 "save a broken site file: nginx -t error returned, file unchanged, both sites serve"
before=$(vm "sha256sum < $SITE_FILE" 2>/dev/null)
assert_http "PUT a broken site file" 422 \
	"$(api_call PUT "$host" "$port" "/api/sites/$STATIC/file" '{"content":"server {\n    bogus_directive on;\n}\n"}')"
assert_contains "the error class is config_rejected" '"error":"config_rejected"' "$(last_body)"
assert_contains "the response carries nginx -t's own message" 'unknown directive' "$(last_body)"
assert_contains "nginx names the file it rejected" "$SITE_FILE" "$(last_body)"
assert_equal "the file on disk is byte-for-byte what it was (sha256sum)" "$before" "$(vm "sha256sum < $SITE_FILE" 2>/dev/null)"
assert_equal "nginx is still running (systemctl)" active "$(vm 'systemctl is-active panel-nginx' 2>/dev/null)"
both_serve "after the rejected edit"

assert_http "PUT a broken nginx.conf" 422 \
	"$(api_call PUT "$host" "$port" /api/nginx/conf '{"content":"bogus_directive on;\n"}')"
assert_ok "nginx.conf still passes nginx -t on the VM" vm "$NGINX -t -c /www/panel/nginx/nginx.conf"
both_serve "after the rejected nginx.conf"

# --- 5. detached and regenerate -----------------------------------------------
step 5 "remove a managed block by hand: the feature shows detached; regenerate reattaches it"
assert_http "GET the site before the edit" 200 "$(api_call GET "$host" "$port" "/api/sites/$STATIC")"
assert_contains "names is attached before the edit" '"feature":"names","include":"/www/panel/nginx/includes/'"$STATIC"'/names.conf","attached":true' "$(last_body)"

# The panel keeps each feature in a "# >>> pb:<name>" / "# <<< pb:<name>" block of
# the site file. An operator edit over SSH removes that block, behind the panel's
# back; nginx is not reloaded, so it keeps serving and the panel must notice from
# the file alone.
vm "sed -i '/# >>> pb:names\$/d; /# <<< pb:names\$/d' $SITE_FILE"
assert_http "GET the site after the edit" 200 "$(api_call GET "$host" "$port" "/api/sites/$STATIC")"
assert_contains "names shows detached" '"feature":"names","include":"/www/panel/nginx/includes/'"$STATIC"'/names.conf","attached":false' "$(last_body)"
assert_contains "listen is still attached" '"feature":"listen","include":"/www/panel/nginx/includes/'"$STATIC"'/listen.conf","attached":true' "$(last_body)"
assert_http "editing the domains of a detached site is refused" 409 \
	"$(api_call PUT "$host" "$port" "/api/sites/$STATIC" '{"aliases":["other.smoke-static.test"]}')"
assert_contains "the refusal says detached" '"error":"detached"' "$(last_body)"

assert_http "regenerate without confirm is refused" 400 \
	"$(api_call POST "$host" "$port" "/api/sites/$STATIC/regenerate")"
assert_http "regenerate with confirm" 200 \
	"$(api_call POST "$host" "$port" "/api/sites/$STATIC/regenerate?confirm=$STATIC")"
assert_http "GET the site after regenerate" 200 "$(api_call GET "$host" "$port" "/api/sites/$STATIC")"
assert_contains "names is attached again" '"feature":"names","include":"/www/panel/nginx/includes/'"$STATIC"'/names.conf","attached":true' "$(last_body)"
assert_http "the history of the site file is listed" 200 \
	"$(api_call GET "$host" "$port" "/api/config-history/sites%2F$STATIC.conf")"
assert_contains "the history has the regenerate entry" "regenerate site file $STATIC" "$(last_body)"
both_serve "after regenerate"

# --- 6. a setting from the form -----------------------------------------------
step 6 "change a common setting: written to panel-http.conf and live"
assert_http "PUT /api/nginx/settings" 200 \
	"$(api_call PUT "$host" "$port" /api/nginx/settings '{"worker_processes":"auto","client_max_body_size":"77m","gzip":true,"keepalive_timeout":65,"client_body_timeout":60,"client_header_timeout":60,"send_timeout":60,"server_tokens":false}')"
assert_contains "panel-http.conf carries the value (cat on the VM)" "client_max_body_size 77m;" \
	"$(vm "cat /www/panel/nginx/panel-http.conf" 2>/dev/null)"
assert_contains "nginx loaded it (nginx -T on the VM)" "client_max_body_size 77m;" \
	"$(vm "$NGINX -T -c /www/panel/nginx/nginx.conf 2>/dev/null" 2>/dev/null)"

# --- 7. nginx killed, then stopped by hand --------------------------------------
step 7 "kill nginx: systemd brings it back; stop it by hand: a daemon restart restores it"
old_pid=$(vm 'systemctl show -p MainPID --value panel-nginx' 2>/dev/null)
vm 'kill -9 $(systemctl show -p MainPID --value panel-nginx)'
wait_until 30 "systemd to restart nginx" nginx_running
new_pid=$(vm 'systemctl show -p MainPID --value panel-nginx' 2>/dev/null)
assert_not_equal "the master is a new process after the kill" "$old_pid" "$new_pid"
wait_until 30 "the static site to answer after the kill" sh -c "curl -s --max-time 5 -H 'Host: $STATIC' http://$host/ | grep -q '$STATIC_MARK'"
both_serve "after nginx was killed"

vm "systemctl stop panel-nginx"
assert_http "the site is down while nginx is stopped" 000 "$(site_status "$STATIC")"
vm "systemctl restart panel"
wait_until 60 "the panel to answer" panel_healthy
wait_until 120 "nginx to run again" nginx_running
assert_equal "systemd reports panel-nginx active again" active "$(vm 'systemctl is-active panel-nginx' 2>/dev/null)"
wait_until 30 "the static site to answer" sh -c "curl -s --max-time 5 -H 'Host: $STATIC' http://$host/ | grep -q '$STATIC_MARK'"
both_serve "after the daemon restart"

# --- 8. reboot ------------------------------------------------------------------
step 8 "reboot: both sites serve"
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
	wait_until 120 "the panel to answer" panel_healthy
	wait_until 120 "nginx to run" nginx_running
	wait_until 60 "the static site to answer" sh -c "curl -s --max-time 5 -H 'Host: $STATIC' http://$host/ | grep -q '$STATIC_MARK'"
	wait_until 60 "the proxy site to answer" sh -c "curl -s --max-time 5 -H 'Host: $PROXY' http://$host/ | grep -q '$PROXY_MARK'"
	both_serve "after the reboot"
	assert_http "login after the reboot" 200 "$(api_login "$host" "$port" "$SMOKE_USER" "$pass")"
fi

# --- 9. delete ------------------------------------------------------------------
step 9 "delete both sites: config gone, web root kept"
assert_http "DELETE without confirm is refused" 400 "$(api_call DELETE "$host" "$port" "/api/sites/$STATIC")"
assert_http "DELETE the static site" 200 "$(api_call DELETE "$host" "$port" "/api/sites/$STATIC?confirm=$STATIC")"
assert_contains "the response says where the kept files are" "/www/services/sites/$STATIC" "$(last_body)"
assert_http "DELETE the proxy site" 200 "$(api_call DELETE "$host" "$port" "/api/sites/$PROXY?confirm=$PROXY")"
# A host no site claims now gets the landing page, so the status is 200; what
# must be gone is the site's own content.
assert_not_contains "the static site no longer answers" "$STATIC_MARK" "$(site_body "$STATIC")"
assert_not_contains "the proxy site no longer answers" "$PROXY_MARK" "$(site_body "$PROXY")"
assert_equal "the site file is gone (test -e)" gone "$(vm "test -e $SITE_FILE && echo present || echo gone" 2>/dev/null)"
assert_equal "the web root is kept (test -d)" kept "$(vm "test -d /www/services/sites/$STATIC && echo kept || echo gone" 2>/dev/null)"
assert_ok "the remaining config passes nginx -t on the VM" vm "$NGINX -t -c /www/panel/nginx/nginx.conf"

# The backend unit and the kept web root are this script's own; remove them.
vm "systemctl disable --now $BACKEND_UNIT >/dev/null 2>&1; rm -f /etc/systemd/system/$BACKEND_UNIT; systemctl daemon-reload; rm -rf $BACKEND_DIR /www/services/sites/$STATIC"

smoke_finish
