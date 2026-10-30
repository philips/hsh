#!/usr/bin/env bash
set -euo pipefail

VERSION=20261026.0
SHA512_X86_64=e6aa030fb8379fe92b53e9b8b696419e0dce8a3bbcc7d4c1cf76b2ea3d6569eabd7360e694deda6ccbdcd77095b3e82ca0f90a3d8fd13a3e5d6b11cd29fe4649
SHA512_AARCH64=88d6194a71a73e3934556028e2168c7bd1051aea28fe190cfd97226ff08e2697550927bc98a56270c90f3452401e3b1081b4d75d9a1283228e86e62f60ebdf0d
LICENSE_SHA256=0fbab5c58efbdf6d31e8085214f2dd821659c03d73cff3ed2b08e98826ea1cd9
FILES="runsc gvisor-bin/gvisor_sentry gvisor-bin/gvisor-sentry-prewarmer gvisor-bin/gvisor-proc-umounter gvisor-bin/runsc-fd-parking"

GOARCH="${1:-$(go env GOARCH)}"
case "$GOARCH" in
amd64) ARCH=x86_64 SHA512=$SHA512_X86_64 ;;
arm64) ARCH=aarch64 SHA512=$SHA512_AARCH64 ;;
*)
	echo "payload.sh: no gVisor release for GOARCH=$GOARCH" >&2
	exit 1
	;;
esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

URL="https://storage.googleapis.com/gvisor/releases/release/$VERSION/$ARCH/gvisor.tar.zstd"
curl -fsSL -o "$WORK/gvisor.tar.zstd" "$URL"
echo "$SHA512  $WORK/gvisor.tar.zstd" | sha512sum -c --quiet -
curl -fsSL -o "$WORK/LICENSE" "https://raw.githubusercontent.com/google/gvisor/release-$VERSION/LICENSE"
echo "$LICENSE_SHA256  $WORK/LICENSE" | sha256sum -c --quiet -

mkdir -p "$WORK/out"
tar --zstd -xf "$WORK/gvisor.tar.zstd" -C "$WORK/out" $FILES
cp "$WORK/LICENSE" "$WORK/out/LICENSE"
echo "release-$VERSION" >"$WORK/out/VERSION"
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
	-C "$WORK/out" -cf - VERSION LICENSE $FILES | gzip -n -9 >"$HERE/payload.tar.gz"
printf 'release-%s' "$VERSION" >"$HERE/payload.version"
sha256sum "$HERE/payload.tar.gz" | cut -d' ' -f1 >"$HERE/payload.sha256"
echo "wrote $HERE/payload.tar.gz (gVisor release-$VERSION, $ARCH, $(wc -c <"$HERE/payload.tar.gz") bytes)"
