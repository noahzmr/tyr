#!/usr/bin/env bash
# Builds the Tyr live ISO: boots, runs `tyr accept` and shows the verdict.
#
#   iso/build.sh [amd64|arm64]      → dist/tyr-live-<version>-<arch>.iso
#
# Needs Docker. arm64 on an amd64 host also needs qemu-user binfmt
# (Debian/Ubuntu: sudo apt install qemu-user-binfmt); on an arm64 host it
# builds natively. Vendor tools: put their .deb/.rpm into iso/vendor/.
# Limits: iso/accept.json (optional) becomes /etc/tyr/accept.json.
set -euo pipefail

arch=${1:-amd64}
case "$arch" in
amd64 | arm64) ;;
*)
	echo "usage: $0 [amd64|arm64]" >&2
	exit 2
	;;
esac
cd "$(dirname "$0")/.."

host=$(uname -m)
if [[ $arch == arm64 && $host != aarch64 && ! -e /proc/sys/fs/binfmt_misc/qemu-aarch64 ]]; then
	echo "building arm64 on $host needs qemu-user binfmt: sudo apt install qemu-user-binfmt (or build on an arm64 host)" >&2
	exit 1
fi
if [[ $arch == amd64 && $host != x86_64 && ! -e /proc/sys/fs/binfmt_misc/qemu-x86_64 ]]; then
	echo "building amd64 on $host needs qemu-user binfmt: sudo apt install qemu-user-binfmt" >&2
	exit 1
fi

echo "== tyr binary (linux/$arch)"
version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
	-ldflags "-s -w -X github.com/noahzmr/tyr/internal/buildinfo.Version=$version -X github.com/noahzmr/tyr/internal/buildinfo.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
	-o "dist/tyr-linux-$arch" ./cmd/tyr

echo "== build environment"
docker build --platform "linux/$arch" -t "tyr-iso-builder:$arch" iso

echo "== image"
docker run --rm --privileged --platform "linux/$arch" \
	-e VERSION="$version" -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" -e MIRROR="${MIRROR:-http://deb.debian.org/debian}" \
	-v "$PWD:/src" -w /src "tyr-iso-builder:$arch" iso/builder.sh "$arch"
