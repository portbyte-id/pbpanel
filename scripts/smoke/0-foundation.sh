#!/bin/sh
# Smoke 0: the M0 exit gate, end to end, against a fresh Ubuntu VM.
#
#   SMOKE_HOST=root@10.0.0.5 SMOKE_BINARY=./dist/panel sh scripts/smoke/0-foundation.sh
#   SMOKE_DRY_RUN=1 sh scripts/smoke/0-foundation.sh       # print the plan only
#
# Environment:
#   SMOKE_HOST       user@host of the VM; the user must be root
#   SMOKE_BINARY     path to the linux/amd64 release binary (default dist/panel)
#   SMOKE_NEW_PORT   port to move the panel to (default 9443)
#   SMOKE_SSH_OPTS   extra ssh options
#
# Needs a real Ubuntu VM: `panel install` installs Docker from the official
# repository and writes /www and a systemd unit. Never run it on a workstation.

set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

SMOKE_BINARY=${SMOKE_BINARY:-dist/panel}
SMOKE_NEW_PORT=${SMOKE_NEW_PORT:-9443}
PANEL=/www/panel/bin/panel

smoke_init
host=$(vm_host)

if [ "$SMOKE_DRY_RUN" != 1 ] && [ ! -f "$SMOKE_BINARY" ]; then
	log "binary not found: $SMOKE_BINARY (set SMOKE_BINARY)"
	exit 2
fi

# --- 1. copy the release binary ----------------------------------------------
step 1 "copy the release binary to the VM"
vm_copy "$SMOKE_BINARY" /tmp/panel
vm 'chmod 0755 /tmp/panel'
assert_ok "binary runs and reports a version" vm '/tmp/panel --version'

# --- 2. install --------------------------------------------------------------
step 2 "panel install"
# stdin is not a terminal, so the installer generates the password and prints
# it once; it is captured here and never written to disk.
install_out=$(vm '/tmp/panel install </dev/null' 2>&1)
install_rc=$?
if [ "$SMOKE_DRY_RUN" = 1 ]; then
	install_out="username  admin
password  DRYRUNPASSWORD
url       https://0.0.0.0:8443"
	install_rc=0
fi
assert_equal "panel install exits 0" 0 "$install_rc"
admin_user=$(printf '%s\n' "$install_out" | sed -n 's/^ *username  *//p' | head -n 1)
admin_pass=$(printf '%s\n' "$install_out" | sed -n 's/^ *password  *//p' | head -n 1)
assert_not_equal "install printed a username" "" "$admin_user"
assert_not_equal "install printed a password" "" "$admin_pass"

old_port=$(printf '%s\n' "$install_out" | sed -n 's/^ *url  *https:\/\/.*:\([0-9][0-9]*\)$/\1/p' | head -n 1)
[ "$SMOKE_DRY_RUN" = 1 ] && old_port=8443
assert_not_equal "install printed a URL with a port" "" "$old_port"

assert_equal "panel.service is active (systemctl)" active "$(vm 'systemctl is-active panel' 2>/dev/null)"
assert_equal "panel.service is enabled (systemctl)" enabled "$(vm 'systemctl is-enabled panel' 2>/dev/null)"

# A second install must repair, never overwrite the password.
reinstall=$(vm '/tmp/panel install </dev/null' 2>&1)
assert_contains "re-running install leaves the password unchanged" "password unchanged" "$reinstall"

# --- 3. log in over HTTPS ----------------------------------------------------
step 3 "log in over HTTPS (self-signed certificate accepted for this test)"
assert_http "login on the install port" 200 "$(api_login "$host" "$old_port" "$admin_user" "$admin_pass")"
assert_http "GET /api/me with the session" 200 "$(api_call GET "$host" "$old_port" /api/me)"
assert_http "POST without a CSRF token is refused" 403 \
	"$(curl -sk -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" -X POST "https://$host:$old_port/api/logout" 2>/dev/null)"

# --- 4. change panel.port ----------------------------------------------------
step 4 "change panel.port to $SMOKE_NEW_PORT through the API"
assert_http "PUT /api/settings panel.port" 200 \
	"$(api_call PUT "$host" "$old_port" /api/settings "{\"settings\":[{\"name\":\"panel.port\",\"value\":\"$SMOKE_NEW_PORT\"}]}")"

# --- 5. new port answers, old port does not ----------------------------------
step 5 "the panel answers on the new port and not the old one"
wait_until 30 "the new port to answer" \
	sh -c "[ \"\$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 https://$host:$SMOKE_NEW_PORT/api/health)\" = 200 ]"
assert_http "new port serves the health probe" 200 "$(http_status "https://$host:$SMOKE_NEW_PORT/api/health")"
# The old listener closes after a grace period.
wait_until 40 "the old port to close" \
	sh -c "[ \"\$(curl -sk -o /dev/null -w '%{http_code}' --max-time 3 https://$host:$old_port/api/health)\" = 000 ]"
assert_http "old port no longer answers" 000 "$(http_status "https://$host:$old_port/api/health")"
# Independent oracle on the VM itself: listening sockets, not the panel's word.
listeners=$(vm "ss -ltnH" 2>/dev/null)
assert_contains "ss shows a listener on the new port" ":$SMOKE_NEW_PORT " "$listeners"
if [ "$SMOKE_DRY_RUN" = 1 ]; then
	log "  [dry-run] assert ss shows no listener on the old port"
else
	case $listeners in
	*":$old_port "*) _fail "ss shows no listener on the old port" "port $old_port is still listening" "$listeners" ;;
	*) _pass "ss shows no listener on the old port" ;;
	esac
fi

# --- 6. reset-password -------------------------------------------------------
step 6 "panel reset-password: old session rejected, new password works"
assert_http "old session still valid before the reset" 200 "$(api_call GET "$host" "$SMOKE_NEW_PORT" /api/me)"
reset_out=$(vm "$PANEL reset-password" 2>&1)
if [ "$SMOKE_DRY_RUN" = 1 ]; then reset_out="    DRYRUNNEWPASSWORD"; fi
new_pass=$(printf '%s\n' "$reset_out" | sed -n 's/^ *\([A-Za-z0-9]\{12,\}\) *$/\1/p' | head -n 1)
assert_not_equal "reset-password printed a new password" "" "$new_pass"
assert_not_equal "the new password differs from the old one" "$admin_pass" "$new_pass"
assert_http "the old session is rejected" 401 "$(api_call GET "$host" "$SMOKE_NEW_PORT" /api/me)"
assert_http "the old password no longer logs in" 401 "$(api_login "$host" "$SMOKE_NEW_PORT" "$admin_user" "$admin_pass")"
assert_http "the new password logs in" 200 "$(api_login "$host" "$SMOKE_NEW_PORT" "$admin_user" "$new_pass")"

# --- 7. panel info -----------------------------------------------------------
step 7 "panel info shows the new port"
info_out=$(vm "$PANEL info" 2>&1)
assert_contains "panel info reports the new port" "port      $SMOKE_NEW_PORT" "$info_out"
assert_contains "panel info URL carries the new port" ":$SMOKE_NEW_PORT" "$info_out"

# --- 8. reboot ---------------------------------------------------------------
step 8 "reboot: the panel comes back on the new port"
if [ "$SMOKE_DRY_RUN" = 1 ]; then
	log "  [dry-run] ssh \$SMOKE_HOST: reboot; wait for ssh down, then up"
else
	vm 'systemctl reboot' >/dev/null 2>&1 || true
	# Wait for the VM to go away first, or the "up" check passes on the old boot.
	wait_until 60 "the VM to go down" sh -c "! ssh $_ssh_base $SMOKE_SSH_OPTS -o ConnectTimeout=3 $SMOKE_HOST true"
fi
wait_until 300 "ssh to come back" vm true
wait_until 120 "the panel to answer on the new port" \
	sh -c "[ \"\$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 https://$host:$SMOKE_NEW_PORT/api/health)\" = 200 ]"
assert_equal "panel.service is active after reboot (systemctl)" active "$(vm 'systemctl is-active panel' 2>/dev/null)"
assert_http "the new port serves after reboot" 200 "$(http_status "https://$host:$SMOKE_NEW_PORT/api/health")"
assert_http "the old port stays closed after reboot" 000 "$(http_status "https://$host:$old_port/api/health")"
assert_http "the new password still logs in after reboot" 200 "$(api_login "$host" "$SMOKE_NEW_PORT" "$admin_user" "$new_pass")"

smoke_finish
