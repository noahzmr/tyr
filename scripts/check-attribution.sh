#!/bin/sh
# Rejects tooling attribution: commit messages, pull request texts and the
# tracked files describe the work and nothing else; trailers or notes that
# credit a generator are not allowed.
#
#   check-attribution.sh text FILE     any text (a commit message, a PR title and description)
#   check-attribution.sh commits [REV] every commit message reachable from REV (default HEAD)
#   check-attribution.sh tree          tracked file contents and names
#
# The hooks in scripts/hooks call this; so does the CI workflow.
set -eu

pattern='claude|anthropic|co-authored-by|generated with'
root=$(git rev-parse --show-toplevel)

case "${1:-}" in
text)
	if grep -inE "$pattern" "$2" >&2; then
		echo "check-attribution: text matches /$pattern/i (lines above)." >&2
		exit 1
	fi
	;;
commits)
	bad=0
	for c in $(git rev-list "${2:-HEAD}"); do
		if git log -1 --format=%B "$c" | grep -inE "$pattern" >&2; then
			echo "check-attribution: commit $c matches /$pattern/i (lines above)." >&2
			bad=1
		fi
	done
	exit "$bad"
	;;
tree)
	cd "$root"
	# This script and the hooks spell the pattern out and are exempt.
	hits=$(git grep -I -n -i -E "$pattern" -- . ':(exclude)scripts/check-attribution.sh' ':(exclude)scripts/hooks' || true)
	names=$(git ls-files | grep -iE "$pattern" || true)
	if [ -n "$hits$names" ]; then
		echo "check-attribution: tracked files match /$pattern/i:" >&2
		[ -z "$hits" ] || echo "$hits" >&2
		[ -z "$names" ] || echo "$names" >&2
		exit 1
	fi
	;;
*)
	echo "usage: check-attribution.sh text FILE | commits [REV] | tree" >&2
	exit 2
	;;
esac
