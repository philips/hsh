#!/usr/bin/env bash
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=${SPIKE_DIR:-$HOME/.cache/gvx-spike}
mkdir -p "$work/tmp"
export GOTOOLCHAIN=go1.27.4 GOTMPDIR="$work/tmp" TMPDIR="$work/tmp"

bin=$work/spike
(cd "$here" && CGO_ENABLED=0 go build -o "$bin" .)
if [ "$(uname -s)" = Darwin ]; then
  codesign --sign - --entitlements "$here/entitlements.plist" --force "$bin"
fi
exec "$bin" "$@"
