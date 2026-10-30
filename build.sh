#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat <<'USAGE'
usage: ./build.sh [--no-gvisor] [-o OUTPUT] [GO BUILD FLAGS...]

Builds hsh the way releases are built: GOEXPERIMENT=simd, CGO_ENABLED=0,
GOOS=linux, and gVisor's runsc embedded for `hsh run --runtime gvisor`.
The gVisor payload for GOARCH (default: go env GOARCH) is downloaded and
verified on first use and reused afterwards.

  --no-gvisor   build without the embedded runsc (about 30 MB smaller)
  -o OUTPUT     output path (default: ./hsh)

Extra flags go to go build, e.g. ./build.sh -trimpath -ldflags="-s -w".
Cross-compile with GOARCH=arm64 ./build.sh -o hsh-arm64.
USAGE
}

cd "$(dirname "${BASH_SOURCE[0]}")"

gvisor=1
out=./hsh
args=()
while [ $# -gt 0 ]; do
	case "$1" in
	--no-gvisor) gvisor=0 ;;
	-o)
		[ $# -ge 2 ] || { usage >&2; exit 2; }
		out="$2"
		shift
		;;
	-o=*) out="${1#-o=}" ;;
	-h | --help)
		usage
		exit 0
		;;
	*) args+=("$1") ;;
	esac
	shift
done

case ",${GOEXPERIMENT:-}," in
*,simd,*) ;;
,,) export GOEXPERIMENT=simd ;;
*) export GOEXPERIMENT="$GOEXPERIMENT,simd" ;;
esac
export CGO_ENABLED=0
export GOOS="${GOOS:-linux}"
export GOARCH="${GOARCH:-$(go env GOARCH)}"

tags=()
if [ "$gvisor" = 1 ]; then
	if ! bash internal/gvisor/payload.sh "$GOARCH"; then
		echo "build.sh: could not prepare the gVisor payload; rerun with --no-gvisor to build without it" >&2
		exit 1
	fi
	tags=(-tags gvisor_embed)
fi

go build "${tags[@]}" "${args[@]}" -o "$out" .

what="without gVisor"
if [ "$gvisor" = 1 ]; then
	what="with gVisor $(cat internal/gvisor/payload.version)"
fi
echo "built $out ($GOOS/$GOARCH, GOEXPERIMENT=$GOEXPERIMENT, $what, $(du -h "$out" | cut -f1))"
