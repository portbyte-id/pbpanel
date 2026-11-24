#!/bin/sh
# PB Panel: install or upgrade from https://github.com/portbyte-id/pbpanel
#
# One script for both. It installs when no panel binary is present and upgrades
# when one is; the real work is `pbp install` and `pbp upgrade` in the binary,
# which this script only fetches and runs.
#
#   sudo -E sh install.sh                      latest release
#   sudo -E sh install.sh --version v0.2.0     a specific release
#   sudo -E sh install.sh --source --ref main  build a branch, tag or commit here
#   sudo -E sh install.sh --clone --ref main   same, fetching the source with git
#   sudo -E sh install.sh --force              reinstall even if already current
#
# The repository is private, so a token with read access to it is required:
#
#   export GITHUB_TOKEN=...                 (or PB_GITHUB_TOKEN)
#
# Where the binary comes from:
#   1. The release's pbp-linux-<arch> asset, checked against SHA256SUMS.
#   2. If the repository has no release yet (or --source or --clone is given),
#      the source at --ref is built on this host with a Go and a Node toolchain
#      that are downloaded into a temp directory and removed afterwards.
#      --source downloads the source as a tarball from the GitHub API. --clone
#      runs a shallow `git fetch` of --ref instead (needs git on the host); it
#      is the one to use when the API download is slow, since git moves the
#      same bytes over a different transfer path. The token, if any, is passed
#      to git through its environment, not written to disk or argv.
#
# Environment: GITHUB_TOKEN / PB_GITHUB_TOKEN, PB_REPO (owner/name),
#              PB_VERSION, PB_REF, PB_PANEL_DATA_DIR (non-default install root).
#
# Everything runs inside main(), called on the last line, so a truncated
# download of this file never executes a half-read command.

set -eu

REPO=${PB_REPO:-portbyte-id/pbpanel}
TOKEN=${PB_GITHUB_TOKEN:-${GITHUB_TOKEN:-}}
VERSION=${PB_VERSION:-latest}
REF=${PB_REF:-main}
SOURCE=0
CLONE=0
FORCE=0
NODE_MAJOR=22 # keep in step with .github/workflows/ci.yml

API=https://api.github.com/repos/$REPO
DATA_DIR=${PB_PANEL_DATA_DIR:-/www/panel/data}
ROOT=${DATA_DIR%/panel/data}
# The binary is `pbp`. `panel` is the name it had before; a host that still has
# one is upgraded rather than installed over.
LIVE=$ROOT/panel/bin/pbp
LEGACY=$ROOT/panel/bin/panel

WORK=
CURLRC=
ARCH=
GOARCH=
NODEARCH=
SHA7=

say() { printf '%s\n' "$*"; }
die() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}

usage() {
	sed -n '2,/^# Everything runs/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
	exit 0
}

cleanup() {
	[ -n "$WORK" ] && rm -rf "$WORK"
	return 0
}

# ver_ge A B: true when version A >= version B.
ver_ge() {
	[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n 1)" = "$2" ]
}

# http_get URL OUT [ACCEPT]: GitHub API call with the token; prints the status.
# The token goes through a mode-0600 curl config, never argv, so it is not
# visible in `ps`.
http_get() {
	curl -sS -L -o "$2" -w '%{http_code}' -K "$CURLRC" \
		-H "Accept: ${3:-application/vnd.github+json}" \
		-H 'X-GitHub-Api-Version: 2022-11-28' "$1"
}

# plain_get URL OUT: a download from a third party; no token is sent.
plain_get() {
	curl -fsSL -o "$2" "$1" || die "download failed: $1"
}

preflight() {
	[ "$(uname -s)" = Linux ] || die "this installer targets Linux; this host is $(uname -s)"
	[ "$(id -u)" -eq 0 ] || die "run as root, for example: sudo -E sh install.sh"

	case $(uname -m) in
	x86_64 | amd64)
		ARCH=amd64 GOARCH=amd64 NODEARCH=x64
		;;
	aarch64 | arm64)
		ARCH=arm64 GOARCH=arm64 NODEARCH=arm64
		;;
	*) die "unsupported architecture $(uname -m); the panel supports amd64 and arm64" ;;
	esac

	# The panel's OS layer implements Ubuntu only; refuse early rather than fail
	# halfway through `panel install`.
	if [ -r /etc/os-release ]; then
		# shellcheck disable=SC1091
		os_id=$(. /etc/os-release && printf '%s' "${ID:-}")
	else
		os_id=
	fi
	[ "$os_id" = ubuntu ] || die "unsupported distribution (ID=${os_id:-unknown}); the panel supports Ubuntu"

	for tool in curl tar awk sort sha256sum; do
		command -v "$tool" >/dev/null 2>&1 || die "$tool is required (apt-get install -y curl ca-certificates)"
	done
	if [ "$CLONE" -eq 1 ]; then
		command -v git >/dev/null 2>&1 || die "git is required for --clone (apt-get install -y git)"
		command -v base64 >/dev/null 2>&1 || die "base64 is required for --clone (coreutils)"
	fi

	[ -n "$TOKEN" ] || say "note: no GITHUB_TOKEN set; this works only if $REPO is public"
}

# asset_url JSON NAME: the API URL of the release asset called NAME. GitHub
# pretty-prints, and an asset object lists "url" before "name".
asset_url() {
	awk -v want="$2" '
		/"url": *"[^"]*\/releases\/assets\/[0-9]+"/ {
			match($0, /https:[^"]*/); u = substr($0, RSTART, RLENGTH)
		}
		/"name": *"/ {
			n = $0
			sub(/^[^"]*"name": *"/, "", n); sub(/".*$/, "", n)
			if (n == want) { print u; exit }
		}
	' "$1"
}

# fetch_release: download and verify the release asset into $WORK/panel.
# Returns 0 on success, 2 when the repository has no such release.
fetch_release() {
	if [ "$VERSION" = latest ]; then
		url=$API/releases/latest
	else
		url=$API/releases/tags/$VERSION
	fi

	code=$(http_get "$url" "$WORK/release.json") || die "could not reach api.github.com"
	case $code in
	200) ;;
	404) return 2 ;;
	401 | 403) die "GitHub refused the request ($code); check that the token can read $REPO" ;;
	*) die "GitHub answered $code for $url" ;;
	esac

	tag=$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$WORK/release.json" | head -n 1)
	[ -n "$tag" ] || die "the release response has no tag_name"

	bin_name=pbp-linux-$ARCH
	bin_url=$(asset_url "$WORK/release.json" "$bin_name")
	sum_url=$(asset_url "$WORK/release.json" SHA256SUMS)
	[ -n "$bin_url" ] || die "release $tag has no $bin_name asset"
	[ -n "$sum_url" ] || die "release $tag has no SHA256SUMS asset; refusing to install an unverified binary"

	say "downloading $bin_name ($tag)"
	code=$(http_get "$bin_url" "$WORK/panel" application/octet-stream) || die "download failed"
	[ "$code" = 200 ] || die "downloading $bin_name answered $code"
	code=$(http_get "$sum_url" "$WORK/SHA256SUMS" application/octet-stream) || die "download failed"
	[ "$code" = 200 ] || die "downloading SHA256SUMS answered $code"

	want=$(awk -v n="$bin_name" '$2 == n || $2 == "*" n { print $1; exit }' "$WORK/SHA256SUMS")
	[ -n "$want" ] || die "SHA256SUMS has no entry for $bin_name"
	have=$(sha256sum "$WORK/panel" | awk '{ print $1 }')
	[ "$want" = "$have" ] || die "checksum mismatch for $bin_name (expected $want, got $have)"
	say "checksum ok"
	return 0
}

# ensure_go VERSION: puts a Go >= VERSION on PATH.
ensure_go() {
	need=$1
	if command -v go >/dev/null 2>&1; then
		have=$(go env GOVERSION 2>/dev/null | sed 's/^go//')
		if [ -n "$have" ] && ver_ge "$have" "$need"; then
			return 0
		fi
	fi
	say "fetching Go $need into a temp directory"
	tgz=go$need.linux-$GOARCH.tar.gz
	plain_get "https://dl.google.com/go/$tgz" "$WORK/$tgz"
	plain_get "https://dl.google.com/go/$tgz.sha256" "$WORK/$tgz.sha256"
	[ "$(awk '{ print $1 }' "$WORK/$tgz.sha256")" = "$(sha256sum "$WORK/$tgz" | awk '{ print $1 }')" ] ||
		die "checksum mismatch for $tgz"
	mkdir "$WORK/go-toolchain"
	tar -xzf "$WORK/$tgz" -C "$WORK/go-toolchain"
	PATH=$WORK/go-toolchain/go/bin:$PATH
	export PATH
}

# ensure_node: puts a Node >= NODE_MAJOR (with npm) on PATH.
ensure_node() {
	if command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then
		have=$(node -p 'process.versions.node.split(".")[0]' 2>/dev/null || echo 0)
		if [ "$have" -ge "$NODE_MAJOR" ] 2>/dev/null; then
			return 0
		fi
	fi
	say "fetching Node $NODE_MAJOR into a temp directory"
	base=https://nodejs.org/dist/latest-v$NODE_MAJOR.x
	plain_get "$base/SHASUMS256.txt" "$WORK/SHASUMS256.txt"
	line=$(grep " node-v[0-9.]*-linux-$NODEARCH\.tar\.gz\$" "$WORK/SHASUMS256.txt" | head -n 1)
	[ -n "$line" ] || die "no Node $NODE_MAJOR build for linux-$NODEARCH"
	sum=$(printf '%s' "$line" | awk '{ print $1 }')
	file=$(printf '%s' "$line" | awk '{ print $2 }')
	plain_get "$base/$file" "$WORK/$file"
	[ "$sum" = "$(sha256sum "$WORK/$file" | awk '{ print $1 }')" ] || die "checksum mismatch for $file"
	mkdir "$WORK/node-toolchain"
	tar -xzf "$WORK/$file" -C "$WORK/node-toolchain" --strip-components=1
	PATH=$WORK/node-toolchain/bin:$PATH
	export PATH
}

# tarball_source: unpack $REF from the GitHub API into $WORK/src.
tarball_source() {
	code=$(http_get "$API/tarball/$REF" "$WORK/src.tar.gz") || die "could not reach api.github.com"
	case $code in
	200) ;;
	404) die "$REPO@$REF not found; if the repository is private, set GITHUB_TOKEN" ;;
	401 | 403) die "GitHub refused the request ($code); check that the token can read $REPO" ;;
	*) die "GitHub answered $code for the source tarball" ;;
	esac

	mkdir "$WORK/src"
	# The tarball's one top-level directory is named <owner>-<repo>-<sha7>.
	top=$(tar -tzf "$WORK/src.tar.gz" | head -n 1 | cut -d/ -f1)
	SHA7=${top##*-}
	tar -xzf "$WORK/src.tar.gz" -C "$WORK/src" --strip-components=1
}

# clone_source: shallow-fetch $REF into $WORK/src. Works for a branch, a tag or
# a commit: `git fetch <sha>` is allowed by GitHub for any reachable commit, so
# there is no need to tell the kinds of ref apart. The token goes to git as a
# Basic-auth header in the environment of one subshell, so it is never in argv,
# never in .git/config, and not exported to the build that follows.
clone_source() {
	say "cloning $REPO@$REF (shallow)"
	mkdir "$WORK/src"
	git -C "$WORK/src" init -q || die "git init failed"
	git -C "$WORK/src" remote add origin "https://github.com/$REPO.git" || die "git remote add failed"
	(
		if [ -n "$TOKEN" ]; then
			auth=$(printf 'x-access-token:%s' "$TOKEN" | base64 | tr -d '\n')
			GIT_CONFIG_COUNT=1
			GIT_CONFIG_KEY_0=http.extraHeader
			GIT_CONFIG_VALUE_0="Authorization: Basic $auth"
			export GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0
		fi
		GIT_TERMINAL_PROMPT=0
		export GIT_TERMINAL_PROMPT
		git -C "$WORK/src" fetch -q --depth 1 origin "$REF"
	) || die "could not fetch $REF from $REPO (if it is private, set GITHUB_TOKEN)"
	git -C "$WORK/src" -c advice.detachedHead=false checkout -q FETCH_HEAD || die "checkout of $REF failed"
	SHA7=$(git -C "$WORK/src" rev-parse --short=7 HEAD) || die "cannot read the commit of $REF"
	# Drop the history: the tarball path has none, and go would otherwise try to
	# stamp VCS info from it.
	rm -rf "$WORK/src/.git"
}

# build_source: build $REF on this host into $WORK/panel.
build_source() {
	if [ "$CLONE" -eq 1 ]; then
		say "building $REPO@$REF from source (git clone)"
		clone_source
	else
		say "building $REPO@$REF from source"
		tarball_source
	fi
	built=$REF
	[ -n "$SHA7" ] && built=$REF-$SHA7

	need_go=$(awk '/^go [0-9]/ { print $2; exit }' "$WORK/src/go.mod")
	[ -n "$need_go" ] || die "cannot read the Go version from go.mod"
	ensure_go "$need_go"
	ensure_node

	GOPATH=$WORK/gopath
	GOCACHE=$WORK/gocache
	GOTOOLCHAIN=local
	npm_config_cache=$WORK/npm
	export GOPATH GOCACHE GOTOOLCHAIN npm_config_cache

	say "building the web UI"
	(cd "$WORK/src/web" && npm ci --no-audit --no-fund && npm run build) || die "web build failed"

	say "building the panel binary"
	(cd "$WORK/src" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -trimpath \
		-ldflags "-s -w -X github.com/portbyte-id/server-panel/internal/version.Version=$built" \
		-o "$WORK/panel" ./cmd/panel) || die "go build failed"
}

# run_panel CMD...: the panel asks for the admin password on a terminal. Under
# `curl | sh` stdin is the pipe, so hand it /dev/tty when there is one.
run_panel() {
	if [ -r /dev/tty ] && (: </dev/tty) 2>/dev/null; then
		"$@" </dev/tty
	else
		"$@" </dev/null
	fi
}

main() {
	while [ $# -gt 0 ]; do
		case $1 in
		--version)
			[ $# -ge 2 ] || die "--version needs a value"
			VERSION=$2
			shift
			;;
		--ref)
			[ $# -ge 2 ] || die "--ref needs a value"
			REF=$2
			SOURCE=1
			shift
			;;
		--source) SOURCE=1 ;;
		--clone)
			CLONE=1
			SOURCE=1
			;;
		--force) FORCE=1 ;;
		-h | --help) usage ;;
		*) die "unknown option $1 (see --help)" ;;
		esac
		shift
	done

	preflight

	WORK=$(mktemp -d "${TMPDIR:-/var/tmp}/pbpanel-install.XXXXXX") || die "cannot create a temp directory"
	trap cleanup EXIT
	trap 'exit 130' INT TERM

	CURLRC=$WORK/curlrc
	: >"$CURLRC"
	chmod 600 "$CURLRC"
	if [ -n "$TOKEN" ]; then
		printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" >"$CURLRC"
	fi

	if [ "$SOURCE" -eq 0 ]; then
		status=0
		fetch_release || status=$?
		if [ "$status" -eq 2 ]; then
			[ "$VERSION" = latest ] ||
				die "release $VERSION not found in $REPO (if it is private, set GITHUB_TOKEN)"
			say "no release found in $REPO; building $REF from source instead"
			SOURCE=1
		fi
	fi
	[ "$SOURCE" -eq 0 ] || build_source

	chmod 755 "$WORK/panel"
	new_version=$("$WORK/panel" version 2>/dev/null) || die "the downloaded binary does not run on this host"
	say "new binary: $new_version"

	# A host installed before the rename has the old binary and no `pbp`. Both
	# mean "already installed, take the upgrade path": installing over a live
	# database would be the wrong call, and upgrade is what moves the unit and
	# the PATH entry onto the new name.
	installed=$LIVE
	[ -x "$installed" ] || installed=$LEGACY

	if [ -x "$installed" ]; then
		old_version=$("$installed" version 2>/dev/null || true)
		say "installed:  ${old_version:-unknown}"
		if [ "$FORCE" -eq 0 ] && [ -n "$old_version" ] && [ "$old_version" = "$new_version" ]; then
			say "already up to date (use --force to reinstall the same version)"
			exit 0
		fi
		say "upgrading"
		run_panel "$WORK/panel" upgrade
	else
		say "installing"
		run_panel "$WORK/panel" install
	fi
}

main "$@"
