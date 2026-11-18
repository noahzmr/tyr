#!/bin/sh
# Fails when version control holds executables or packaged binaries: build
# them (make build, make cross, make release-snapshot), never commit them.
#
#   check-binaries.sh tree     every tracked file
#   check-binaries.sh staged   the files a commit adds or changes
set -eu
export LC_ALL=C
cd "$(git rev-parse --show-toplevel)"

case "${1:-tree}" in
tree) files=$(git ls-files -s | awk '$1 != "120000" {print $4}') ;;
staged) files=$(git diff --cached --name-only --diff-filter=ACMR) ;;
*) echo "usage: $0 tree|staged" >&2; exit 2 ;;
esac

bad=""
for f in $files; do
	[ -f "$f" ] && [ ! -L "$f" ] || continue
	case "$f" in
	*.exe | *.dll | *.so | *.so.* | *.dylib | *.a | *.o | *.jar | *.war | *.whl | *.deb | *.rpm | *.apk | *.iso | *.img | *.msi)
		bad="$bad$f (packaged binary)
"
		continue
		;;
	esac
	magic=$(head -c 4 "$f" | od -An -tx1 | tr -d ' \n')
	case "$magic" in
	7f454c46) bad="$bad$f (ELF executable)
" ;;
	4d5a*) bad="$bad$f (Windows executable)
" ;;
	feedface | feedfacf | cefaedfe | cffaedfe | cafebabe) bad="$bad$f (Mach-O executable / class file)
" ;;
	esac
done
if [ -n "$bad" ]; then
	echo "binaries in version control (build them, do not commit them):" >&2
	printf '%s' "$bad" >&2
	exit 1
fi
