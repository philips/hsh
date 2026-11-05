#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: demo/encode.sh <source> <output>

Encode an already-captured take into another format WITHOUT re-running VHS.

  <source>   a frames directory (lossless, best) or an existing .gif
  <output>   target file; extension picks the codec (.webm .gif .mp4)

Env:
  FRAMERATE  fps to assume for a frames directory (default 24; match the take)

Examples:
  # one take -> many outputs, no re-record:
  demo/render.sh frames demo/tape-example.md
  demo/encode.sh demo/out/tape-example-frames demo/out/final.webm
  demo/encode.sh demo/out/tape-example-frames demo/out/preview.gif

  # salvage an old preview gif into a webm:
  demo/encode.sh demo/out/webserver.gif demo/out/webserver.webm
EOF
}

[ $# -eq 2 ] || { usage; exit 1; }
src="$1"; out="$2"
command -v ffmpeg >/dev/null || { echo "ffmpeg not found" >&2; exit 1; }
fps="${FRAMERATE:-24}"
ext="${out##*.}"
mkdir -p "$(dirname "$out")"

codec_args() {
  case "$1" in
    webm) printf '%s\0' -c:v libvpx-vp9 -pix_fmt yuv420p -b:v 0 -crf 30 -deadline good -cpu-used 2 -row-mt 1 ;;
    mp4)  printf '%s\0' -c:v libx264 -pix_fmt yuv420p -crf 20 ;;
    gif)  printf '%s\0' -filter_complex "split[s0][s1];[s0]palettegen[p];[s1][p]paletteuse" ;;
    *) echo "unsupported output extension: .$1" >&2; exit 1 ;;
  esac
}
mapfile -d '' CODEC < <(codec_args "$ext")

if [ "$ext" = mp4 ] && ! ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libx264; then
  echo "note: this ffmpeg has no libx264; use .webm or install an x264 build" >&2
fi

if [ -d "$src" ]; then
  glob=$(find "$src" -maxdepth 1 -name '*.png' | head -1)
  [ -n "$glob" ] || { echo "no PNG frames in $src" >&2; exit 1; }
  echo "==> encoding frames ($fps fps) -> $out"
  ffmpeg -y -framerate "$fps" -pattern_type glob -i "$src/*.png" "${CODEC[@]}" "$out"
else
  [ -f "$src" ] || { echo "no such source: $src" >&2; exit 1; }
  echo "==> transcoding $src -> $out"
  ffmpeg -y -i "$src" "${CODEC[@]}" "$out"
fi
echo "wrote $out"
