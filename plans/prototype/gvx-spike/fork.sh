#!/usr/bin/env bash
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=${SPIKE_DIR:-$HOME/.cache/gvx-spike}
fork=$work/gvx-fork
mkdir -p "$work/tmp"
export GOTOOLCHAIN=go1.27.4 GOTMPDIR="$work/tmp"

version=$(awk '$1 == "require" && $2 == "z.gfire.dev/gvx" { print $3 }' "$here/go.mod")
[ -n "$version" ] || { echo "fork.sh: no z.gfire.dev/gvx version in go.mod" >&2; exit 1; }

if [ -d "$fork/.git" ] && [ "$(git -C "$fork" describe --tags --exact-match 2>/dev/null || true)" != "$version" ]; then
  rm -rf "$fork"
fi
if [ ! -d "$fork/.git" ]; then
  if ! git -c advice.detachedHead=false clone -q --depth 1 --branch "$version" https://github.com/gfire-sigs/gvx "$fork" 2>"$work/clone.log"; then
    cat "$work/clone.log" >&2
    exit 1
  fi
fi

git -C "$fork" checkout -q -- .
git -C "$fork" clean -fdq
for p in "$here"/patches/*.patch; do
  git -C "$fork" apply "$p"
done

cp "$here/go.mod" "$work/go.fork.mod"
cp "$here/go.sum" "$work/go.fork.sum"
(cd "$here" && go mod edit -modfile="$work/go.fork.mod" -replace "z.gfire.dev/gvx=$fork")
