#!/usr/bin/env bash
# Tyr releases (docs: DEVELOPER.md "Releasing").
#
#   scripts/release.sh notes    [BUMP|vX.Y.Z]  preview the next release's notes
#   scripts/release.sh snapshot                build the archives into dist/release, publish nothing
#   scripts/release.sh publish  [BUMP|vX.Y.Z]  check, write CHANGELOG.md, tag, push, publish
#   scripts/release.sh retry                   publish the tag at HEAD again (after a failed upload)
#
# BUMP is patch, minor or major; the first release is v1.0.0. Publishing
# needs GITHUB_TOKEN (a token allowed to create releases on the repository).
set -euo pipefail
cd "$(dirname "$0")/.."

GORELEASER=${GORELEASER:-go run github.com/goreleaser/goreleaser/v2@v2.20.0}
REPO=https://github.com/noahzmr/tyr
RELNOTES=(go run ./tools/relnotes -repo "$REPO")
mode=${1:-}
want=${2:-}

die() { echo "release: $*" >&2; exit 1; }

# GoReleaser refuses to run with more than one forge token in the
# environment; Tyr publishes to GitHub only.
gr() { env -u GITLAB_TOKEN -u GITEA_TOKEN $GORELEASER "$@"; }

last_tag() { git tag --merged HEAD --list 'v*' --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true; }

next_version() {
	local prev
	prev=$(last_tag)
	case "$want" in
	v*) echo "$want" ;;
	patch | minor | major) "${RELNOTES[@]}" -next "$want" ;;
	"")
		[[ -z $prev ]] || die "the last release is $prev: say which version comes next (BUMP=patch|minor|major or RELEASE=vX.Y.Z)"
		"${RELNOTES[@]}" -next patch # the first release: v1.0.0
		;;
	*) die "unknown version $want: use patch, minor, major or vX.Y.Z" ;;
	esac
}

checks() {
	echo "== checks"
	test -z "$(gofmt -l .)" || { gofmt -l .; die "gofmt needed"; }
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
	sh scripts/check-attribution.sh tree
	sh scripts/check-attribution.sh commits HEAD
	go test -count=1 ./...
}

case "$mode" in
notes)
	v=$(next_version)
	"${RELNOTES[@]}" -version "$v"
	;;

snapshot)
	gr release --snapshot --clean
	echo
	ls -l dist/release/*.tar.gz dist/release/SHA256SUMS
	;;

publish)
	[[ -n ${GITHUB_TOKEN:-} ]] || die "GITHUB_TOKEN is not set: create a token that may write releases (repo contents: write) and export it"
	[[ $(git rev-parse --abbrev-ref HEAD) == main ]] || die "releases are made from main"
	git diff --quiet && git diff --cached --quiet || die "uncommitted changes: commit or stash them first"
	untracked=$(git status --porcelain --untracked-files=normal | grep '^??' || true)
	[[ -z $untracked ]] || die "untracked files (GoReleaser refuses a dirty tree): add them to .gitignore or .git/info/exclude, or remove them:
$untracked"
	git fetch --quiet --tags origin
	[[ -z $(git rev-list HEAD..origin/main) ]] || die "origin/main has commits this checkout lacks: pull first"
	v=$(next_version)
	"${RELNOTES[@]}" -version "$v" -require-highlights >/dev/null # the version, the highlights, commits since the last tag
	! git rev-parse -q --verify "refs/tags/$v" >/dev/null || die "tag $v exists already"
	[[ -z $(git ls-remote --tags origin "refs/tags/$v") ]] || die "tag $v exists on origin already"
	prev=$(last_tag)
	echo "release $v (previous: ${prev:-none})"

	checks
	echo "== build (nothing published yet)"
	gr check
	gr build --snapshot --clean >/dev/null

	notes=dist/release-notes-$v.md
	cp CHANGELOG.md "dist/CHANGELOG.md.before-$v"
	# until the release commit exists, any way out puts CHANGELOG.md back
	trap 'cp "dist/CHANGELOG.md.before-$v" CHANGELOG.md; echo "release: stopped, CHANGELOG.md is as it was" >&2' EXIT
	"${RELNOTES[@]}" -version "$v" -require-highlights -write -notes "$notes"
	echo
	echo "────────────────────────── release notes of $v ──────────────────────────"
	cat "$notes"
	echo "─────────────────────────────────────────────────────────────────────────"
	if [[ ${YES:-} != 1 ]]; then
		answer=
		read -r -p "Commit CHANGELOG.md, tag $v, push main and the tag, publish the release? Type $v to go on: " answer || true
		[[ $answer == "$v" ]] || die "not published"
	fi

	git add CHANGELOG.md
	git commit -q -m "release: $v" -m "$(sed -n '1,/^### Changes/p' "$notes" | sed '$d')"
	trap - EXIT
	git tag -a "$v" -m "Tyr $v"
	git push --atomic origin main "refs/tags/$v"
	echo "== publishing $v"
	gr release --clean --release-notes "$notes" ||
		die "the tag $v is pushed, but publishing failed: fix the cause and run make publish-retry"
	echo "released $v: $REPO/releases/tag/$v"
	;;

retry)
	[[ -n ${GITHUB_TOKEN:-} ]] || die "GITHUB_TOKEN is not set"
	v=$(git describe --tags --exact-match HEAD 2>/dev/null) || die "HEAD is not a release tag"
	notes=dist/release-notes-$v.md
	if [[ ! -f $notes ]]; then # rebuild the notes from the changelog section
		awk -v h="## [${v#v}]" 'index($0,h)==1{f=1;next} f&&/^## /{exit} f' CHANGELOG.md | sed '1{/^$/d}' >"$notes"
	fi
	gr release --clean --release-notes "$notes"
	;;

*)
	sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
