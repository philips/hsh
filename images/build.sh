#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: images/build.sh <name> [extra docker/podman build args...]" >&2
  echo "  name: one of the image directories under images/ (e.g. pi, vibe, codex)" >&2
  exit 2
}

[ $# -ge 1 ] || usage

name=$1
shift

here=$(cd "$(dirname "$0")" && pwd)
dir=$here/$name
label_file=$dir/hsh.json

[ -d "$dir" ] || { echo "no such image: $name ($dir)" >&2; exit 1; }
[ -f "$label_file" ] || { echo "missing label file: $label_file" >&2; exit 1; }

engine=${CONTAINER_ENGINE:-}
if [ -z "$engine" ]; then
  if command -v docker >/dev/null 2>&1; then engine=docker
  elif command -v podman >/dev/null 2>&1; then engine=podman
  else echo "no docker or podman found" >&2; exit 1; fi
fi

label=$(jq -c . "$label_file")

exec "$engine" build \
  --build-arg "HSH_LABEL=$label" \
  -f "$dir/Containerfile" \
  "$@" \
  "$dir"
