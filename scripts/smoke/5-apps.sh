#!/bin/sh
# Smoke 5: the M5 exit gate, end to end, against a VM the panel is installed on.
#
#   SMOKE_HOST=root@10.0.0.5 sh scripts/smoke/5-apps.sh
#   SMOKE_DRY_RUN=1 sh scripts/smoke/5-apps.sh           # print the plan only
#
# Run it after smoke 0 and smoke 1 (nginx must be up for the proxy-site check). It
# installs a Node.js version through the panel, creates a Node app and a Go binary
# app, checks both answer, kills each with SIGKILL and waits for supervisord to
# bring it back, restarts the panel and checks both keep their PIDs, and puts a
# reverse-proxy site in front of one. It also reads the first real supervisord
# answers of the project: until this script has passed, the RPC subset in
# internal/apps/doc.go is read from source and not run.
#
# Environment:
#   SMOKE_HOST        user@host of the VM; the user must be root
#   SMOKE_PASSWORD    admin password. When unset the script runs
#                     `panel reset-password` on the VM, which signs everyone out.
#   SMOKE_USER        admin username (default admin)
#   SMOKE_SSH_OPTS    extra ssh options
#   SMOKE_NODE        Node.js version to install (default 22)
#   SMOKE_NODE_PORT   port of the Node app (default 18091)
#   SMOKE_GO_PORT     port of the Go app (default 18092)
#   SMOKE_GO_BINARY   a linux binary that answers "go-ok" on $PORT. When unset the
#                     script cross-compiles scripts/smoke/testdata/apps-hello, which
#                     needs `go` on the workstation.
#   SMOKE_REINSTALL=1 also re-run `panel install` and check it did not restart the apps
#
# Independent oracle: "is it serving" is `curl` run on the VM against the app's
# port; "which process" is `ss -ltnp` (the PID that owns the listening socket);
# "is supervisord up, and who can open its socket" is systemctl and stat; "is the
# binary the pinned one" is sha256sum. The proxy site is checked by a request to
# nginx on port 80 with the site's Host header. None of them is a panel endpoint,
# and the SIGKILL is sent with kill(1), not through the panel.
#
# The VM needs git and internet access for the nvm download.

set -u
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

SMOKE_USER=${SMOKE_USER:-admin}
NODE_VER=${SMOKE_NODE:-22}
NP=${SMOKE_NODE_PORT:-18091}
GP=${SMOKE_GO_PORT:-18092}
SMOKE_REINSTALL=${SMOKE_REINSTALL:-0}
PANEL=/www/panel/bin/panel
NODE_APP=smoke-node
GO_APP=smoke-go
SITE=smoke-app.test

# The pinned supervisord checksums, repeated here on purpose: the script checks the
# installed binary against what the source says, not against the source's own copy.
SUP_SHA_AMD64=4a9bd84e902f605bfb47fe0aa67b2cc98f6fea3ee36a2907415c641bdb3fb9b5
SUP_SHA_ARM64=337cd0be0f0bffa3bf0dbdb53464b1aac5922f2c29a667c45aede215b3932c18

smoke_init
host=$(vm_host)

last_body() { cat "$SMOKE_TMP/last.json" 2>/dev/null; }

# listen_pid PORT: the PID that owns the listening socket on the VM, or empty.
listen_pid() {
	vm "ss -ltnpH 'sport = :$1'" 2>/dev/null | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | head -n 1
}

# answer PORT: what the app on that port says to a request made on the VM.
answer() { vm "curl -s --max-time 5 http://127.0.0.1:$1/" 2>/dev/null; }

serves() { [ "$(answer "$1")" = "$2" ]; }

panel_healthy() {
	[ "$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 "https://$host:$port/api/health")" = 200 ]
}

# app_state NAME: the state the panel reports (an oracle only for the panel's own claim).
app_state() {
	api_call GET "$host" "$port" "/api/apps/$1" >/dev/null
	last_body | sed -n 's/.*"state":"\([A-Za-z ]*\)".*/\1/p' | head -n 1
}

node_running() { [ "$(app_state "$NODE_APP")" = RUNNING ]; }

# port_closed PORT: nothing listens on it on the VM.
port_closed() { [ -z "$(listen_pid "$1")" ]; }

site_answer() { curl -s --max-time 10 -H "Host: $SITE" "http://$host/"; }
site_serves() { [ "$(site_answer)" = node-ok ]; }

node_installed() {
	api_call GET "$host" "$port" /api/runtimes >/dev/null
	last_body | grep -q "\"version\":\"$NODE_VER\."
}

cleanup_vm() {
	api_call DELETE "$host" "$port" "/api/sites/$SITE?confirm=$SITE" >/dev/null 2>&1 || true
	api_call DELETE "$host" "$port" "/api/apps/$NODE_APP" >/dev/null 2>&1 || true
	api_call DELETE "$host" "$port" "/api/apps/$GO_APP" >/dev/null 2>&1 || true
	api_call DELETE "$host" "$port" /api/apps/smoke-crash >/dev/null 2>&1 || true
}

# --- 1. the panel and supervisord ---------------------------------------------
step 1 "the panel and supervisord are up, and supervisord is what the source pins"
assert_equal "panel.service is active (systemctl)" active "$(vm 'systemctl is-active panel' 2>/dev/null)"
assert_equal "panel-supervisord.service is active (systemctl)" active "$(vm 'systemctl is-active panel-supervisord' 2>/dev/null)"
assert_equal "panel-supervisord.service is enabled (systemctl)" enabled "$(vm 'systemctl is-enabled panel-supervisord' 2>/dev/null)"
# The Go supervisord ignores chmod=0700 and leaves the socket 755 (seen on a live
# VM), so the mode is not the property; connecting is. Only an owner or group
# member can write to a socket to connect to it, and www must be refused.
assert_equal "supervisord's socket belongs to root (stat)" root "$(vm "stat -c %U /www/panel/supervisor/supervisord.sock" 2>/dev/null)"
assert_contains "the www account cannot connect to supervisord's socket" "Permission denied" \
	"$(vm "su -s /bin/sh www -c 'python3 -c \"import socket;s=socket.socket(socket.AF_UNIX);s.connect(\\\"/www/panel/supervisor/supervisord.sock\\\")\"'" 2>&1)"
arch=$(vm 'uname -m' 2>/dev/null)
case $arch in
aarch64 | arm64) goarch=arm64 sup_sha=$SUP_SHA_ARM64 ;;
*) goarch=amd64 sup_sha=$SUP_SHA_AMD64 ;;
esac
[ "$SMOKE_DRY_RUN" = 1 ] && sup_sha=dry
assert_equal "the installed supervisord is the pinned release (sha256sum)" "$sup_sha" \
	"$([ "$SMOKE_DRY_RUN" = 1 ] && printf dry || vm 'sha256sum /www/panel/bin/supervisord' 2>/dev/null | cut -d' ' -f1)"
assert_equal "supervisord listens on no TCP port (ss)" 0 "$(vm "ss -ltnpH | grep -c supervisord" 2>/dev/null || true)"

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
vm "rm -rf /www/services/apps/$NODE_APP /www/services/apps/$GO_APP" >/dev/null 2>&1 || true

# --- 2. a Node.js version -----------------------------------------------------
step 2 "install Node.js $NODE_VER through the panel"
assert_http "POST /api/runtimes/node" 202 "$(api_call POST "$host" "$port" /api/runtimes/node "{\"version\":\"$NODE_VER\"}")"
wait_until 900 "Node.js $NODE_VER to appear in /api/runtimes" node_installed
assert_ok "the panel lists Node.js $NODE_VER" node_installed
node_bin=$(vm "ls -d /www/panel/nvm/versions/node/v$NODE_VER.*/bin/node 2>/dev/null | tail -n 1")
assert_not_equal "nvm put a node binary on disk (ls)" "" "$node_bin"
assert_contains "that binary runs (node --version)" "v$NODE_VER." "$(vm "$node_bin --version" 2>/dev/null)"
assert_http "an invalid version is refused before any job exists" 422 \
	"$(api_call POST "$host" "$port" /api/runtimes/node '{"version":"22;id"}')"

# --- 3. a Node app and a Go app -----------------------------------------------
step 3 "create a Node app and a Go binary app"
if [ -n "${SMOKE_GO_BINARY:-}" ]; then
	gobin=$SMOKE_GO_BINARY
else
	gobin=$SMOKE_TMP/apps-hello
	if [ "$SMOKE_DRY_RUN" != 1 ]; then
		(cd "$repo" && GOOS=linux GOARCH=$goarch CGO_ENABLED=0 go build -o "$gobin" ./scripts/smoke/testdata/apps-hello) ||
			{ log "could not build the Go test app; set SMOKE_GO_BINARY"; exit 2; }
	fi
fi

# The apps are created stopped, so the files they run can be put in place first;
# the panel creates each working directory and gives it to www.
node_body="{\"name\":\"$NODE_APP\",\"type\":\"node\",\"command\":\"server.js\",\"node_version\":\"$NODE_VER\",\"port\":$NP,\"autostart\":false}"
assert_http "create the Node app (stopped)" 201 "$(api_call POST "$host" "$port" /api/apps "$node_body")"
go_body="{\"name\":\"$GO_APP\",\"type\":\"go\",\"command\":\"/www/services/apps/$GO_APP/$GO_APP\",\"port\":$GP,\"autostart\":false}"
assert_http "create the Go app (stopped)" 201 "$(api_call POST "$host" "$port" /api/apps "$go_body")"
assert_equal "the working directory is owned by www (stat)" www "$(vm "stat -c %U /www/services/apps/$NODE_APP" 2>/dev/null)"
assert_contains "the program file exists (cat)" "[program:$NODE_APP]" "$(vm "cat /www/panel/supervisor/conf.d/$NODE_APP.conf" 2>/dev/null)"

vm "cat > /www/services/apps/$NODE_APP/server.js" <<'JS'
require('http').createServer((q, r) => r.end('node-ok')).listen(process.env.PORT, '0.0.0.0')
JS
vm_copy "$gobin" "/www/services/apps/$GO_APP/$GO_APP"
vm "chown -R www:www /www/services/apps/$NODE_APP /www/services/apps/$GO_APP && chmod 755 /www/services/apps/$GO_APP/$GO_APP"

assert_http "start the Node app" 200 "$(api_call POST "$host" "$port" "/api/apps/$NODE_APP/start")"
assert_http "start the Go app" 200 "$(api_call POST "$host" "$port" "/api/apps/$GO_APP/start")"
wait_until 20 "the Node app to answer" serves "$NP" node-ok
wait_until 20 "the Go app to answer" serves "$GP" go-ok
assert_equal "the Node app answers (curl on the VM)" node-ok "$(answer "$NP")"
assert_equal "the Go app answers (curl on the VM)" go-ok "$(answer "$GP")"
node_pid=$(listen_pid "$NP")
go_pid=$(listen_pid "$GP")
assert_not_equal "the Node app has a listening process (ss)" "" "$node_pid"
assert_not_equal "the Go app has a listening process (ss)" "" "$go_pid"
assert_equal "the Node app runs as www (ps)" www "$(vm "ps -o user= -p $node_pid" 2>/dev/null | tr -d ' ')"
assert_equal "the panel reports the Node app RUNNING" RUNNING "$(app_state "$NODE_APP")"

# --- 4. kill -9 ---------------------------------------------------------------
step 4 "kill -9 each app: supervisord starts it again"
vm "kill -9 $node_pid $go_pid"
wait_until 40 "the Node app to answer again" serves "$NP" node-ok
wait_until 40 "the Go app to answer again" serves "$GP" go-ok
new_node=$(listen_pid "$NP")
new_go=$(listen_pid "$GP")
assert_not_equal "the Node app has a new PID after SIGKILL" "$node_pid" "$new_node"
assert_not_equal "the Go app has a new PID after SIGKILL" "$go_pid" "$new_go"
assert_equal "the Node app answers again" node-ok "$(answer "$NP")"
assert_equal "the Go app answers again" go-ok "$(answer "$GP")"
node_pid=$new_node
go_pid=$new_go

# --- 5. the panel restarts, the apps do not -----------------------------------
step 5 "systemctl restart panel: the apps keep their PIDs"
vm "systemctl restart panel"
wait_until 60 "the panel to answer /api/health" panel_healthy
assert_equal "the Node app kept its PID (ss)" "$node_pid" "$(listen_pid "$NP")"
assert_equal "the Go app kept its PID (ss)" "$go_pid" "$(listen_pid "$GP")"
assert_equal "the Node app still answers" node-ok "$(answer "$NP")"
assert_equal "the Go app still answers" go-ok "$(answer "$GP")"
assert_http "log in again after the restart" 200 "$(api_login "$host" "$port" "$SMOKE_USER" "$pass")"
wait_until 20 "the panel to report the Node app RUNNING again" node_running
assert_equal "the panel reports the Node app RUNNING after its restart" RUNNING "$(app_state "$NODE_APP")"

if [ "$SMOKE_REINSTALL" = 1 ]; then
	vm "$PANEL install" >/dev/null 2>&1
	assert_equal "panel install re-run left the Node app's PID alone" "$node_pid" "$(listen_pid "$NP")"
	assert_equal "panel install re-run left the Go app's PID alone" "$go_pid" "$(listen_pid "$GP")"
fi

# --- 6. controls and logs -----------------------------------------------------
step 6 "stop, start, logs"
assert_http "stop the Go app" 200 "$(api_call POST "$host" "$port" "/api/apps/$GO_APP/stop")"
wait_until 15 "the Go app's port to close" port_closed "$GP"
assert_equal "the Go app no longer listens (ss)" "" "$(listen_pid "$GP")"
assert_http "start the Go app again" 200 "$(api_call POST "$host" "$port" "/api/apps/$GO_APP/start")"
wait_until 20 "the Go app to answer" serves "$GP" go-ok
assert_http "GET the Go app's log" 200 "$(api_call GET "$host" "$port" "/api/apps/$GO_APP/logs")"
assert_contains "the log holds the program's output" "apps-hello listening on $GP" "$(last_body)"
assert_contains "the log file is on disk (cat)" "apps-hello listening" "$(vm "cat /www/panel/logs/apps/$GO_APP/out.log*" 2>/dev/null)"

# --- 6b. edit a running app, and an app that dies at start ---------------------
step 6b "edit a running app; save one that cannot stay up"
before_edit=$(listen_pid "$GP")
edit_body="{\"type\":\"go\",\"command\":\"/www/services/apps/$GO_APP/$GO_APP\",\"port\":$GP,\"env\":{\"SMOKE_EDIT\":\"1\"}}"
assert_http "edit the running Go app (PUT)" 200 "$(api_call PUT "$host" "$port" "/api/apps/$GO_APP" "$edit_body")"
assert_contains "the program file carries the edit (cat)" 'SMOKE_EDIT="1"' "$(vm "cat /www/panel/supervisor/conf.d/$GO_APP.conf" 2>/dev/null)"
wait_until 30 "the edited Go app to answer" serves "$GP" go-ok
after_edit=$(listen_pid "$GP")
assert_not_equal "supervisord saw the edit as a change and restarted the app (new PID)" "$before_edit" "$after_edit"
assert_equal "the edited app answers" go-ok "$(answer "$GP")"
assert_equal "the edited app is RUNNING again (panel)" RUNNING "$(app_state "$GO_APP")"

crash_body='{"name":"smoke-crash","type":"command","command":"/bin/false","restart":"never"}'
assert_http "an app that exits at once is still saved" 201 "$(api_call POST "$host" "$port" /api/apps "$crash_body")"
assert_contains "and the response says it did not start" "did not start" "$(last_body)"
assert_not_equal "the panel does not report it RUNNING" RUNNING "$(app_state smoke-crash)"
assert_ok "the panel is still healthy" panel_healthy
assert_http "delete the crashing app" 204 "$(api_call DELETE "$host" "$port" /api/apps/smoke-crash)"

# --- 7. a proxy site ----------------------------------------------------------
step 7 "a reverse-proxy site in front of the Node app"
site_body="{\"domain\":\"$SITE\",\"kind\":\"proxy\",\"proxy_target\":\"http://127.0.0.1:$NP\"}"
assert_http "create the proxy site" 201 "$(api_call POST "$host" "$port" /api/sites "$site_body")"
wait_until 15 "the site to answer" site_serves
assert_equal "nginx proxies the site's Host to the Node app (curl from the workstation)" node-ok \
	"$([ "$SMOKE_DRY_RUN" = 1 ] && printf node-ok || site_answer)"

# --- 8. clean up --------------------------------------------------------------
step 8 "remove what the script made"
assert_http "delete the proxy site" 200 "$(api_call DELETE "$host" "$port" "/api/sites/$SITE?confirm=$SITE")"
assert_http "delete the Node app" 204 "$(api_call DELETE "$host" "$port" "/api/apps/$NODE_APP")"
assert_http "delete the Go app" 204 "$(api_call DELETE "$host" "$port" "/api/apps/$GO_APP")"
wait_until 15 "the Node app's port to close" port_closed "$NP"
assert_equal "deleting an app stopped it (ss)" "" "$(listen_pid "$NP")"
assert_equal "the program file is gone (ls)" "" "$(vm "ls /www/panel/supervisor/conf.d/$NODE_APP.conf 2>/dev/null")"
vm "rm -rf /www/services/apps/$NODE_APP /www/services/apps/$GO_APP"
smoke_finish
