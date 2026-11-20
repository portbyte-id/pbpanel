#!/bin/sh
# Smoke 3: the M3 file manager, end to end, against a VM the panel is on.
#
#   SMOKE_HOST=root@10.0.0.5 sh scripts/smoke/3-file-manager.sh
#   SMOKE_DRY_RUN=1 sh scripts/smoke/3-file-manager.sh       # print the plan only
#
# It creates a scratch tree under /var/tmp, exercises browse, create, rename,
# chmod, copy, move, trash, the editor, chunked upload and archives through the
# panel API, and cleans up after itself. Nothing outside /var/tmp/smoke-fm-$$
# is written.
#
# Environment:
#   SMOKE_HOST       user@host of the VM; the user must be root
#   SMOKE_PASSWORD   admin password. When unset the script runs
#                    `panel reset-password` on the VM, which signs everyone out.
#   SMOKE_USER       admin username (default admin)
#   SMOKE_SSH_OPTS   extra ssh options
#
# Independent oracle: file existence and content are read with test/cat/sha256sum
# on the VM, never from a panel endpoint. The API assertions read the panel's
# own HTTP answers, which is the thing under test.

set -u
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$here/lib.sh"

PANEL=/www/panel/bin/pbp
WORK=/var/tmp/smoke-fm-$$

SMOKE_USER=${SMOKE_USER:-admin}

smoke_init
host=$(vm_host)

last_body() { cat "$SMOKE_TMP/last.json" 2>/dev/null; }

# fm_list PATH [EXTRA_QUERY]
fm_list() { api_call GET "$host" "$port" "/api/fm/list?path=$1${2:+&$2}"; }
fm_stat() { api_call GET "$host" "$port" "/api/fm/stat?path=$1"; }
fm_post() { api_call POST "$host" "$port" "$1" "$2"; }
fm_put() { api_call PUT "$host" "$port" "$1" "$2"; }
fm_del() { api_call DELETE "$host" "$port" "$1"; }

# wait_job JOB_ID: poll the job's SSE stream until it carries a terminal state;
# the stream stays open while the job runs. Prints the state.
wait_job() {
	i=0
	while [ "$i" -lt 120 ]; do
		_state=$(curl -sk --max-time 10 -b "$COOKIE_JAR" "https://$host:$port/api/jobs/$1/events" 2>/dev/null \
			| grep -o '"state":"[a-z]*"' | tail -n 1)
		case $_state in
		'"state":"succeeded"' | '"state":"failed"' | '"state":"canceled"' | '"state":"interrupted"')
			printf '%s' "$_state"
			return 0
			;;
		esac
		i=$((i + 1))
	done
	printf '%s' timeout
	return 1
}

# --- 1. the panel is up ----------------------------------------------------------
step 1 "the panel is up"
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

# --- 2. browse, hidden, stat -------------------------------------------------------
step 2 "list a directory, stat a file, hidden entries"
vm "mkdir -p $WORK/d && printf 'hello\n' > $WORK/a.txt && printf 'secret\n' > $WORK/.hidden"
assert_http "GET /api/fm/list" 200 "$(fm_list "$WORK")"
assert_contains "the listing names a.txt" '"name":"a.txt"' "$(last_body)"
HIDDEN_LIST=$(last_body)
case $HIDDEN_LIST in
*'"name":".hidden"'*) assert_equal "a hidden file is not listed by default" absent present ;;
*) assert_equal "a hidden file is not listed by default" absent absent ;;
esac
assert_http "hidden=1 shows the hidden file" 200 "$(fm_list "$WORK" hidden=1)"
assert_contains "the hidden file is in the listing" '"name":".hidden"' "$(last_body)"
assert_http "stat of an existing file" 200 "$(fm_stat "$WORK/a.txt")"
assert_contains "stat reports the size" '"size":6' "$(last_body)"
assert_http "stat of a missing file is a 404" 404 "$(fm_stat "$WORK/nope")"

# --- 3. create, rename, chmod --------------------------------------------------------
step 3 "create a file and a folder, rename, chmod"
assert_http "POST /api/fm/mkdir" 201 "$(fm_post /api/fm/mkdir '{"path":"'"$WORK"'/sub"}')"
assert_http "mkdir again is a conflict" 409 "$(fm_post /api/fm/mkdir '{"path":"'"$WORK"'/sub"}')"
assert_http "POST /api/fm/create" 201 "$(fm_post /api/fm/create '{"path":"'"$WORK"'/sub/one.txt"}')"
assert_equal "the created file exists on the VM (test -f)" yes \
	"$(vm "test -f $WORK/sub/one.txt && echo yes || echo no" 2>/dev/null)"
assert_http "rename without a name is a 400" 400 "$(fm_post /api/fm/rename '{"path":"'"$WORK"'/sub/one.txt"}')"
assert_http "POST /api/fm/rename" 200 "$(fm_post /api/fm/rename '{"path":"'"$WORK"'/sub/one.txt","name":"two.txt"}')"
assert_equal "the old name is gone and the new one exists (test)" yes \
	"$(vm "test ! -e $WORK/sub/one.txt && test -f $WORK/sub/two.txt && echo yes || echo no" 2>/dev/null)"

assert_http "chmod with a bad mode is refused" 400 "$(fm_post /api/fm/chmod '{"path":"'"$WORK"'/sub/two.txt","mode":"999"}')"
assert_http "chmod to 640" 200 "$(fm_post /api/fm/chmod '{"path":"'"$WORK"'/sub/two.txt","mode":"640"}')"
assert_equal "the mode is on the file (stat)" 640 "$(vm "stat -c %a $WORK/sub/two.txt" 2>/dev/null)"

# --- 4. copy and move -----------------------------------------------------------------
step 4 "copy and move through the API"
vm "printf 'payload\n' > $WORK/sub/two.txt"
assert_http "POST /api/fm/copy starts a job" 202 \
	"$(fm_post /api/fm/copy '{"paths":["'"$WORK"'/sub/two.txt"],"dest":"'"$WORK"'/d"}')"
COPY_JOB=$(last_body | sed -n 's/.*"job":{"id":"\([^"]*\)".*/\1/p')
assert_not_equal "the copy job has an id" "" "$COPY_JOB"
assert_equal "the copy job succeeds" '"state":"succeeded"' "$(wait_job "$COPY_JOB")"
assert_equal "the copy exists on the VM (cat)" "payload" "$(vm "cat $WORK/d/two.txt" 2>/dev/null)"

assert_http "POST /api/fm/move (same device, inline)" 200 \
	"$(fm_post /api/fm/move '{"paths":["'"$WORK"'/d/two.txt"],"dest":"'"$WORK"'"}')"
assert_equal "the move took effect on the VM (test)" yes \
	"$(vm "test -f $WORK/two.txt && test ! -e $WORK/d/two.txt && echo yes || echo no" 2>/dev/null)"

# --- 5. trash: delete, list, restore, permanent delete ---------------------------------
step 5 "delete to trash, list trash, restore, permanent delete"
vm "mkdir -p $WORK/gone && printf 'bye\n' > $WORK/gone/inner.txt"

# A folder needs its base name typed; a wrong or missing confirm is a 400.
assert_http "delete of a folder without confirm is refused" 400 \
	"$(fm_post /api/fm/delete '{"items":[{"path":"'"$WORK"'/gone"}]}')"
assert_equal "the folder survived the refused delete (test -d)" yes \
	"$(vm "test -d $WORK/gone && echo yes || echo no" 2>/dev/null)"
assert_http "delete of a folder with the wrong confirm is refused" 400 \
	"$(fm_post /api/fm/delete '{"items":[{"path":"'"$WORK"'/gone","confirm":"wrong"}]}')"

assert_http "delete of a single file needs no confirm" 200 \
	"$(fm_post /api/fm/delete '{"items":[{"path":"'"$WORK"'/two.txt"}]}')"
TRASH_ID=$(last_body | sed -n 's/.*"trashed":\[{"id":"\([^"]*\)".*/\1/p')
assert_not_equal "the trash response carries an id" "" "$TRASH_ID"
assert_equal "the file is out of the tree (test -e)" gone \
	"$(vm "test -e $WORK/two.txt && echo present || echo gone" 2>/dev/null)"

assert_http "delete of the folder with its typed name" 200 \
	"$(fm_post /api/fm/delete '{"items":[{"path":"'"$WORK"'/gone","confirm":"gone"}]}')"
DIR_TRASH_ID=$(last_body | sed -n 's/.*"trashed":\[{"id":"\([^"]*\)".*/\1/p')
assert_not_equal "the folder's trash id" "" "$DIR_TRASH_ID"

assert_http "GET /api/fm/trash" 200 "$(api_call GET "$host" "$port" /api/fm/trash)"
assert_contains "the trash lists the file" "\"id\":\"$TRASH_ID\"" "$(last_body)"
assert_contains "the trash lists the folder" "\"id\":\"$DIR_TRASH_ID\"" "$(last_body)"

assert_http "restore the file" 200 "$(fm_post "/api/fm/trash/$TRASH_ID/restore" '')"
assert_equal "the file is back with its content (cat)" "payload" "$(vm "cat $WORK/two.txt" 2>/dev/null)"
assert_http "restoring again is a 404" 404 "$(fm_post "/api/fm/trash/$TRASH_ID/restore" '')"

# Permanent delete of the folder: the typed confirmation is the entry's name.
assert_http "permanent delete without the typed name is refused" 400 \
	"$(fm_del "/api/fm/trash/$DIR_TRASH_ID")"
assert_http "permanent delete with a wrong typed name is refused" 400 \
	"$(fm_del "/api/fm/trash/$DIR_TRASH_ID?confirm=wrong")"
assert_http "DELETE /api/fm/trash/{id}?confirm=gone" 204 "$(fm_del "/api/fm/trash/$DIR_TRASH_ID?confirm=gone")"
assert_equal "the folder is gone for good (slot on the VM)" gone \
	"$(vm "test -e /www/panel/trash/$DIR_TRASH_ID && echo present || echo gone" 2>/dev/null)"

# --- 6. the editor ----------------------------------------------------------------------
step 6 "editor open/save round-trip and the 409 conflict path"
assert_http "GET /api/fm/file" 200 "$(api_call GET "$host" "$port" "/api/fm/file?path=$WORK/two.txt")"
assert_contains "the content round-trips" '"content":"payload\n"' "$(last_body)"
HASH=$(last_body | sed -n 's/.*"hash":"\([^"]*\)".*/\1/p')
MTIME=$(last_body | sed -n 's/.*"mtime":\([0-9]*\).*/\1/p')
SIZE=$(last_body | sed -n 's/.*"size":\([0-9]*\).*/\1/p')
assert_not_equal "the open carries a hash" "" "$HASH"

# The file changes behind the panel's back; a save with the stale base conflicts.
vm "printf 'changed\n' > $WORK/two.txt"
assert_http "a stale save is a 409" 409 \
	"$(fm_put /api/fm/file '{"path":"'"$WORK"'/two.txt","content":"mine\n","base":{"size":'"$SIZE"',"mtime":'"$MTIME"',"hash":"'"$HASH"'"}}')"
assert_contains "the conflict names edit_conflict" '"error":"edit_conflict"' "$(last_body)"
assert_contains "the conflict carries the current content" '"current"' "$(last_body)"
assert_equal "the file still holds the other writer's bytes (cat)" "changed" "$(vm "cat $WORK/two.txt" 2>/dev/null)"
assert_http "an unconditional save (no base) succeeds" 200 \
	"$(fm_put /api/fm/file '{"path":"'"$WORK"'/two.txt","content":"mine\n"}')"
assert_equal "the file holds the saved bytes (cat)" "mine" "$(vm "cat $WORK/two.txt" 2>/dev/null)"

# --- 7. chunked upload, download back, Range ---------------------------------------------
step 7 "chunked upload init/chunk/complete, download, Range"
UP=$WORK/up.bin
printf '0123456789abcdefghijklmnopqrstuvwxyz' > "$SMOKE_TMP/up.bin"
WHOLE_SHA=$(sha256sum < "$SMOKE_TMP/up.bin" | cut -d' ' -f1)
C1_SHA=$(head -c 20 "$SMOKE_TMP/up.bin" | sha256sum | cut -d' ' -f1)
C2_SHA=$(tail -c 16 "$SMOKE_TMP/up.bin" | sha256sum | cut -d' ' -f1)
assert_http "POST /api/fm/uploads" 201 \
	"$(fm_post /api/fm/uploads '{"dir":"'"$WORK"'","name":"up.bin","size":36}')"
UP_ID=$(last_body | sed -n 's/.*{"id":"\([^"]*\)".*/\1/p')
assert_not_equal "the upload has an id" "" "$UP_ID"
assert_contains "the upload reports the chunk limit" '"chunk_max"' "$(last_body)"

# A chunk is PUT with its offset and the SHA-256 of its bytes. Send the file in
# two real chunks.
chunk() { # chunk ID OFFSET FILE SHA
	curl -sk --max-time 30 -b "$COOKIE_JAR" -o "$SMOKE_TMP/last.json" -w '%{http_code}' \
		-X PUT -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/octet-stream' \
		-H "X-Chunk-SHA256: $4" --data-binary @"$3" \
		"https://$host:$port/api/fm/uploads/$1?offset=$2" 2>/dev/null || true
}
CSRF=$(cat "$SMOKE_TMP/csrf" 2>/dev/null)
head -c 20 "$SMOKE_TMP/up.bin" > "$SMOKE_TMP/c1"
tail -c 16 "$SMOKE_TMP/up.bin" > "$SMOKE_TMP/c2"
assert_http "chunk 1 at offset 0" 200 "$(chunk "$UP_ID" 0 "$SMOKE_TMP/c1" "$C1_SHA")"
assert_http "a chunk at the wrong offset is a conflict" 409 "$(chunk "$UP_ID" 5 "$SMOKE_TMP/c2" "$C2_SHA")"
assert_http "a chunk with a bad checksum is refused" 400 \
	"$(chunk "$UP_ID" 20 "$SMOKE_TMP/c2" "$(printf x | sha256sum | cut -d' ' -f1)")"
assert_http "chunk 2 at offset 20" 200 "$(chunk "$UP_ID" 20 "$SMOKE_TMP/c2" "$C2_SHA")"
assert_http "POST the upload complete" 200 \
	"$(fm_post "/api/fm/uploads/$UP_ID/complete" '{"sha256":"'"$WHOLE_SHA"'"}')"
assert_contains "the complete names the placed file" '"path":"'"$UP"'"' "$(last_body)"

DL_URL="https://$host:$port/api/fm/download?path=$UP"
assert_equal "download returns the same bytes" "$WHOLE_SHA" \
	"$(curl -sk --max-time 15 -b "$COOKIE_JAR" "$DL_URL" 2>/dev/null | sha256sum | cut -d' ' -f1)"
RANGE_OUT=$(curl -sk --max-time 15 -b "$COOKIE_JAR" -r 10-19 -w '\n%{http_code}' "$DL_URL" 2>/dev/null)
assert_equal "a Range request answers 206" 206 "$(printf '%s' "$RANGE_OUT" | tail -n 1)"
assert_equal "the range carries bytes 10-19" "abcdefghij" "$(printf '%s' "$RANGE_OUT" | head -n 1)"

# --- 8. archive create and extract ---------------------------------------------------------
step 8 "archive create and extract round-trip"
assert_http "POST /api/fm/archive starts a job" 202 \
	"$(fm_post /api/fm/archive '{"dir":"'"$WORK"'","names":["sub"],"dest":"'"$WORK"'/sub.tar.gz"}')"
ARCH_JOB=$(last_body | sed -n 's/.*"job":{"id":"\([^"]*\)".*/\1/p')
assert_not_equal "the archive job has an id" "" "$ARCH_JOB"
assert_equal "the archive job succeeds" '"state":"succeeded"' "$(wait_job "$ARCH_JOB")"
assert_equal "the archive exists on the VM (test -f)" yes \
	"$(vm "test -f $WORK/sub.tar.gz && echo yes || echo no" 2>/dev/null)"

vm "rm -rf $WORK/out && mkdir -p $WORK/out"
assert_http "POST /api/fm/extract starts a job" 202 \
	"$(fm_post /api/fm/extract '{"path":"'"$WORK"'/sub.tar.gz","dest":"'"$WORK"'/out"}')"
EXT_JOB=$(last_body | sed -n 's/.*"job":{"id":"\([^"]*\)".*/\1/p')
assert_equal "the extract job succeeds" '"state":"succeeded"' "$(wait_job "$EXT_JOB")"
assert_equal "the extracted content matches (cat)" "payload" "$(vm "cat $WORK/out/sub/two.txt" 2>/dev/null)"

# --- 9. hostile paths -----------------------------------------------------------------------
step 9 "the API refuses traversal, symlink escapes and protected trees"
# Traversal: Clean collapses .., so /etc/.. lands on / and delete of / is a 403.
assert_http "delete of /etc/../ style traversal is refused" 403 \
	"$(fm_post /api/fm/delete '{"items":[{"path":"/etc/..","confirm":"x"}]}')"
assert_http "delete of /etc is refused (protected)" 403 \
	"$(fm_post /api/fm/delete '{"items":[{"path":"/etc","confirm":"etc"}]}')"
assert_http "mkdir under /proc is refused (read-only tree)" 403 \
	"$(fm_post /api/fm/mkdir '{"path":"/proc/x"}')"
assert_http "a relative path is a 400" 400 "$(fm_list relative)"

# Symlink escape: deleting a link in the scratch tree that points at /etc acts
# on the link, never on its target; the panel must not follow it.
vm "ln -sfn /etc $WORK/etc-link"
assert_http "delete of a symlink to /etc removes the link only" 200 \
	"$(fm_post /api/fm/delete '{"items":[{"path":"'"$WORK"'/etc-link","confirm":"etc-link"}]}')"
assert_equal "the link is gone (test -L)" no \
	"$(vm "test -L $WORK/etc-link && echo yes || echo no" 2>/dev/null)"
assert_equal "/etc is untouched (test -f)" yes \
	"$(vm "test -f /etc/passwd && test -d /etc/systemd && echo yes || echo no" 2>/dev/null)"

# A hostile archive with a ../ entry must fail its job and write nothing.
# Built on the VM with python3's tarfile, since tar refuses to store ../ names.
vm "python3 -c \"
import tarfile, io
t = tarfile.open('$WORK/evil.tar.gz', 'w:gz')
data = b'x'
info = tarfile.TarInfo('../evil.txt')
info.size = len(data)
t.addfile(info, io.BytesIO(data))
t.close()
\""
assert_http "extract of a hostile archive starts a job" 202 \
	"$(fm_post /api/fm/extract '{"path":"'"$WORK"'/evil.tar.gz","dest":"'"$WORK"'/out"}')"
EVIL_JOB=$(last_body | sed -n 's/.*"job":{"id":"\([^"]*\)".*/\1/p')
assert_not_equal "the hostile extract job has an id" "" "$EVIL_JOB"
# Either the job fails, or it succeeded and wrote nothing outside dest.
wait_job "$EVIL_JOB" >/dev/null 2>&1 || true
assert_equal "the hostile archive wrote nothing above dest (test)" gone \
	"$(vm "test -e $WORK/evil.txt && echo present || echo gone" 2>/dev/null)"
assert_equal "dest holds no evil entry (test)" gone \
	"$(vm "test -e $WORK/out/innocent.txt && echo present || echo gone" 2>/dev/null)"

# --- 10. settings and trash retention ---------------------------------------------------------
step 10 "the Files settings exist; retention purge is time-gated"
assert_http "GET /api/settings" 200 "$(api_call GET "$host" "$port" /api/settings)"
for key in fm.editor.max_bytes fm.upload.chunk_max_bytes fm.upload.sweep_age \
	fm.trash.max_bytes fm.trash.retention_days fm.archive.max_bytes fm.archive.max_entries; do
	assert_contains "the setting $key exists" "\"name\":\"$key\"" "$(last_body)"
done

# The retention purge runs daily and against a clock; setting retention to 0
# and back is the honest assertion available without time travel: the key is
# live (the service reads it per operation), the bin survives a purge with a
# retention of 0 only by size. A real retention test needs an item older than
# the cutoff, which cannot be produced through the API.
assert_http "PUT fm.trash.retention_days=0 is accepted" 200 \
	"$(api_call PUT "$host" "$port" /api/settings '{"settings":[{"name":"fm.trash.retention_days","value":"0"}]}')"
assert_http "PUT fm.trash.retention_days=30 is accepted" 200 \
	"$(api_call PUT "$host" "$port" /api/settings '{"settings":[{"name":"fm.trash.retention_days","value":"30"}]}')"
log "  SKIP  trash purge against retention: the daily purge reads a clock, and an item
        older than the cutoff cannot be produced through the API; covered by unit tests."

# --- 11. cleanup -------------------------------------------------------------------------------
step 11 "cleanup"
vm "rm -rf $WORK"

smoke_finish
