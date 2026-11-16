#!/bin/sh
# Enforces docs/development-plan.md §1.4:
#   1. no shell strings      — `sh -c` / `bash -c` outside the allowlist file
#   2. distro logic only in `internal/osdep` — apt, dnf, ufw, firewalld, semanage
#
# Exits non-zero and prints `file:line: reason` for every violation.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
allowlist="scripts/convention-allowlist.txt"

status=0

is_allowlisted() {
	[ -f "$allowlist" ] || return 1
	while IFS= read -r entry || [ -n "$entry" ]; do
		case $entry in
		'' | '#'*) continue ;;
		esac
		[ "$entry" = "$1" ] && return 0
	done <"$allowlist"
	return 1
}

# Prefer the index, but fall back to a walk: on a fresh checkout with nothing
# staged `git ls-files` is empty and the script would check zero files.
# shellcheck disable=SC2046 # word splitting over newline-separated paths is intended
files=$(git ls-files 2>/dev/null || true)
if [ -z "$files" ]; then
	files=$(find . -type f \
		-not -path './.git/*' -not -path './web/node_modules/*' \
		-not -path './web/dist/*' -not -path './bin/*')
fi

# Two shapes: the argv form `("sh", "-c", ...)` and the literal form `sh -c`.
shell_re='((^|[^[:alnum:]_./-])(/bin/)?(sh|bash)"?[[:space:]]*,[[:space:]]*"-c"|(^|[^[:alnum:]_./-])(/bin/)?(sh|bash)[[:space:]]+-c([[:space:]]|$))'
distro_re='(^|[^[:alnum:]_./-])(apt|apt-get|dnf|yum|ufw|firewalld|semanage)([^[:alnum:]_-]|$)'

# A comment that mentions the forbidden thing is documentation, not a use.
is_comment() {
	case $(printf '%s' "$1" | sed 's/^[[:space:]]*//') in
	'//'* | '#'* | '*'* | '/*'*) return 0 ;;
	esac
	return 1
}

report() {
	# $1 file, $2 line number, $3 message
	printf '%s:%s: %s\n' "$1" "$2" "$3" >&2
}

for file in $files; do
	# `git ls-files` prints bare paths, `find` prints them with a `./` prefix.
	file=${file#./}
	case $file in
	*.go | *.ts | *.tsx | *.js | *.jsx | *.mjs | *.cjs) ;;
	*) continue ;;
	esac

	if ! is_allowlisted "$file"; then
		hits=$(grep -nE "$shell_re" "$file" 2>/dev/null || true)
		if [ -n "$hits" ]; then
			printf '%s\n' "$hits" | while IFS= read -r hit; do
				text=${hit#*:}
				is_comment "$text" && continue
				report "$file" "${hit%%:*}" \
					"shell string (no \`sh -c\` / \`bash -c\`): $(printf '%s' "$text" | sed 's/^[[:space:]]*//')"
			done
			status=1
		fi
	fi

	case $file in
	internal/osdep/*) continue ;;
	esac
	case $file in
	*.go) ;;
	*) continue ;;
	esac

	hits=$(grep -nE "$distro_re" "$file" 2>/dev/null || true)
	if [ -n "$hits" ]; then
		printf '%s\n' "$hits" | while IFS= read -r hit; do
			text=${hit#*:}
			is_comment "$text" && continue
			report "$file" "${hit%%:*}" \
				"distro command outside internal/osdep: $(printf '%s' "$text" | sed 's/^[[:space:]]*//')"
		done
		status=1
	fi
done

if [ "$status" -eq 0 ]; then
	echo "lint-conventions: ok"
fi
exit "$status"
