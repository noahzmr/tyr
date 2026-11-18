#!/usr/bin/env bash
# Tyr releases (docs: DEVELOPER.md "Releasing").
#
#   scripts/release.sh notes    [BUMP|vX.Y.Z]  preview the next release's notes
#   scripts/release.sh snapshot                build the archives into dist/release, publish nothing
#   scripts/release.sh prepare  [BUMP|vX.Y.Z]  check, write CHANGELOG.md on a release/vX.Y.Z
#                                              branch and push it for a pull request
#   scripts/release.sh publish                 after that pull request is merged: tag the
#                                              merged commit on main, push the tag, publish
#   scripts/release.sh retry                   publish the tag at HEAD again (after a failed upload)
#   scripts/release.sh update-notes [vX.Y.Z]   set a published release's text from CHANGELOG.md
#                                              (default: the latest tag; nothing is rebuilt)
#
# BUMP is patch, minor or major; the first release is v1.0.0. Publishing
# needs GITHUB_TOKEN (a token allowed to create releases on the repository).
# main is protected: nothing here pushes to it; the release commit reaches it
# through the pull request, publishing only pushes a tag.
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

clean_main() {
	[[ $(git rev-parse --abbrev-ref HEAD) == main ]] || die "run this on main"
	git diff --quiet && git diff --cached --quiet || die "uncommitted changes: commit or stash them first"
	untracked=$(git status --porcelain --untracked-files=normal | grep '^??' || true)
	[[ -z $untracked ]] || die "untracked files (GoReleaser refuses a dirty tree): add them to .gitignore or .git/info/exclude, or remove them:
$untracked"
	git fetch --quiet --tags origin
	[[ $(git rev-parse HEAD) == $(git rev-parse origin/main) ]] || die "main differs from origin/main: pull (or push your commits through a pull request) first"
}

confirm() {
	[[ ${YES:-} == 1 ]] && return 0
	local answer=
	read -r -p "$1 Type $2 to go on: " answer || true
	[[ $answer == "$2" ]]
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

prepare)
	clean_main
	v=$(next_version)
	"${RELNOTES[@]}" -version "$v" -require-highlights >/dev/null # the version, the highlights, commits since the last tag
	! git rev-parse -q --verify "refs/tags/$v" >/dev/null || die "tag $v exists already"
	[[ -z $(git ls-remote --tags origin "refs/tags/$v") ]] || die "tag $v exists on origin already"
	branch=release/$v
	[[ -z $(git ls-remote --heads origin "$branch") ]] || die "branch $branch exists on origin already: merge or delete it"
	echo "prepare $v (previous: $(last_tag || true))"

	checks
	echo "== build (nothing published)"
	gr check
	gr build --snapshot --clean >/dev/null

	mkdir -p dist
	notes=dist/release-notes-$v.md
	cp CHANGELOG.md "dist/CHANGELOG.md.before-$v"
	# until the release commit exists, any way out puts CHANGELOG.md back
	trap 'cp "dist/CHANGELOG.md.before-$v" CHANGELOG.md; echo "release: stopped, CHANGELOG.md is as it was" >&2' EXIT
	"${RELNOTES[@]}" -version "$v" -require-highlights -write -notes "$notes"
	echo
	echo "────────────────────────── release notes of $v ──────────────────────────"
	cat "$notes"
	echo "─────────────────────────────────────────────────────────────────────────"
	confirm "Commit CHANGELOG.md to $branch and push it for a pull request?" "$v" || die "not prepared"

	git switch -q -c "$branch"
	git add CHANGELOG.md
	git commit -q -m "release: $v" -m "$(sed -n '1,/^### Changes/p' "$notes" | sed '$d')"
	trap - EXIT
	git push -q -u origin "$branch"
	git switch -q main
	echo
	echo "Pushed $branch. Open the pull request, let CI pass and merge it:"
	echo "  $REPO/compare/main...$branch?expand=1&title=release:+$v"
	echo "Then: git pull && make publish"
	;;

publish)
	[[ -n ${GITHUB_TOKEN:-} ]] || die "GITHUB_TOKEN is not set: create a token that may write releases (repo contents: write) and export it"
	clean_main
	v=$("${RELNOTES[@]}" -pending) || exit 1
	! git rev-parse -q --verify "refs/tags/$v" >/dev/null || die "tag $v exists already"
	[[ -z $(git ls-remote --tags origin "refs/tags/$v") ]] || die "tag $v exists on origin already"
	# the release commit, merged as is or squashed ("release: vX.Y.Z (#12)")
	git log --format=%s HEAD | grep -E "^release: ${v//./\\.}( \\(#[0-9]+\\))?$" >/dev/null ||
		die "main has no \"release: $v\" commit: merge the release pull request first"
	late=$("${RELNOTES[@]}" -missing "$v")
	if [[ -n $late ]]; then
		echo "These commits are on main but not in the notes of $v (they landed after it was prepared):"
		echo "$late" | sed 's/^/  /'
		confirm "Publish $v without them? (Otherwise prepare it again.)" "$v" || die "not published"
	fi
	echo "== build (nothing published yet)"
	gr check
	gr build --snapshot --clean >/dev/null

	mkdir -p dist
	notes=dist/release-notes-$v.md
	"${RELNOTES[@]}" -section "$v" >"$notes"
	[[ -s $notes ]] || die "the release notes of $v are empty"
	echo
	echo "────────────────────────── release notes of $v ──────────────────────────"
	cat "$notes"
	echo "─────────────────────────────────────────────────────────────────────────"
	confirm "Tag $(git rev-parse --short HEAD) as $v, push the tag and publish the release?" "$v" || die "not published"

	git tag -a "$v" -m "Tyr $v"
	git push -q origin "refs/tags/$v"
	echo "== publishing $v"
	gr release --clean --release-notes "$notes" ||
		die "the tag $v is pushed, but publishing failed: fix the cause and run make publish-retry"
	# the text once more, straight from CHANGELOG.md: a release never stays without notes
	"${RELNOTES[@]}" -update-release "$v"
	echo "released $v: $REPO/releases/tag/$v"
	;;

retry)
	[[ -n ${GITHUB_TOKEN:-} ]] || die "GITHUB_TOKEN is not set"
	v=$(git describe --tags --exact-match HEAD 2>/dev/null) || die "HEAD is not a release tag"
	mkdir -p dist
	notes=dist/release-notes-$v.md
	"${RELNOTES[@]}" -section "$v" >"$notes"
	gr release --clean --release-notes "$notes"
	"${RELNOTES[@]}" -update-release "$v"
	;;

update-notes)
	[[ -n ${GITHUB_TOKEN:-} ]] || die "GITHUB_TOKEN is not set"
	v=${want:-$(last_tag)}
	[[ -n $v ]] || die "no release tag yet"
	"${RELNOTES[@]}" -update-release "$v"
	;;

*)
	sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
