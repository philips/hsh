#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: tools/terminal-demos/encode.sh <source> <output>

Encode an already-captured take into another format WITHOUT re-running VHS.

  <source>   a frames directory (lossless, best) or an existing .gif
  <output>   target file; extension picks the codec (.webm .gif .mp4)

VHS frame directories are handled automatically: the frame-text / frame-cursor
layers are composited, and any nested vhs<random>/ subdir is found for you.

GIF uses a two-pass palette so memory stays flat; the one-pass
palettegen/paletteuse split buffers the whole video in RAM and can OOM-kill
your session.

Env:
  FRAMERATE  fps to assume for a frames directory (default 24; match the take)
  SCALE      output width in px for gif previews (e.g. SCALE=640); keeps
             preview files small and encoding fast

Examples:
  tools/terminal-demos/render.sh frames tools/terminal-demos/tape-example.md
  tools/terminal-demos/encode.sh tools/terminal-demos/out/tape-example-frames tools/terminal-demos/out/final.webm
  SCALE=640 tools/terminal-demos/encode.sh tools/terminal-demos/out/tape-example-frames tools/terminal-demos/out/preview.gif
  tools/terminal-demos/encode.sh tools/terminal-demos/out/webserver.gif tools/terminal-demos/out/webserver.webm
EOF
}

[ $# -eq 2 ] || { usage; exit 1; }
src="$1"; out="$2"
command -v ffmpeg >/dev/null || { echo "ffmpeg not found" >&2; exit 1; }
fps="${FRAMERATE:-24}"
ext="${out##*.}"
mkdir -p "$(dirname "$out")"

scale_chain() { # $1 = incoming chain label
  if [ -n "${SCALE:-}" ]; then
    echo "${1},scale=${SCALE}:-1:flags=lanczos"
  else
    echo "$1"
  fi
}

case "$ext" in
  webm) VCODEC=(-c:v libvpx-vp9 -pix_fmt yuv420p -b:v 0 -crf 30 -deadline good -cpu-used 2 -row-mt 1) ;;
  mp4)  VCODEC=(-c:v libx264 -pix_fmt yuv420p -crf 20)
        ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libx264 || \
          echo "note: this ffmpeg has no libx264; use .webm or install an x264 build" >&2 ;;
  gif)  VCODEC=() ;;
  *) echo "unsupported output extension: .$ext" >&2; exit 1 ;;
esac

if [ -d "$src" ]; then
  text=$(find "$src" -name 'frame-text-*.png' | sort | head -1 || true)
  if [ -n "$text" ]; then
    dir=$(dirname "$text")
    num=$(basename "$text" .png); num=${num##*-}
    w=${#num}; start=$((10#$num))
    tpat="$dir/frame-text-%0${w}d.png"
    cpat="$dir/frame-cursor-%0${w}d.png"
    frame_args=(-framerate "$fps" -start_number "$start" -i "$tpat"
                -framerate "$fps" -start_number "$start" -i "$cpat")
    overlay="[0][1]overlay"
  else
    any=$(find "$src" -name '*.png' | sort | head -1 || true)
    [ -n "$any" ] || { echo "no PNG frames under $src" >&2; exit 1; }
    dir=$(dirname "$any")
    frame_args=(-framerate "$fps" -pattern_type glob -i "$dir/*.png")
    overlay="[0:v]"
  fi
  echo "==> encoding frames ($fps fps) -> $out"
  if [ "$ext" = gif ]; then
    pal=$(mktemp -t pal.XXXXXX.png)
    trap 'rm -f "$pal"' EXIT
    echo "    pass 1/2: palette"
    ffmpeg -hide_banner -loglevel error -y "${frame_args[@]}" \
      -filter_complex "$(scale_chain "$overlay"),palettegen=stats_mode=diff" \
      -frames:v 1 "$pal"
    echo "    pass 2/2: frames"
    ffmpeg -hide_banner -loglevel error -y "${frame_args[@]}" -i "$pal" \
      -filter_complex "$(scale_chain "$overlay")[v];[v][2:v]paletteuse=dither=bayer:bayer_scale=5" \
      "$out"
  else
    ffmpeg -hide_banner -loglevel error -y "${frame_args[@]}" \
      -filter_complex "$(scale_chain "$overlay")" "${VCODEC[@]}" "$out"
  fi
else
  [ -f "$src" ] || { echo "no such source: $src" >&2; exit 1; }
  echo "==> transcoding $src -> $out"
  if [ "$ext" = gif ]; then
    pal=$(mktemp -t pal.XXXXXX.png)
    trap 'rm -f "$pal"' EXIT
    echo "    pass 1/2: palette"
    ffmpeg -hide_banner -loglevel error -y -i "$src" \
      -vf "$(scale_chain "palettegen=stats_mode=diff")" -frames:v 1 "$pal"
    echo "    pass 2/2: frames"
    ffmpeg -hide_banner -loglevel error -y -i "$src" -i "$pal" \
      -filter_complex "$(scale_chain "[0:v]")[v];[v][1:v]paletteuse=dither=bayer:bayer_scale=5" \
      "$out"
  else
    vf=()
    sc=$(scale_chain "")
    [ -n "$sc" ] && vf=(-vf "$sc")
    ffmpeg -hide_banner -loglevel error -y -i "$src" \
      "${vf[@]}" "${VCODEC[@]}" "$out"
  fi
fi
echo "wrote $out"
