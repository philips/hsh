#!/usr/bin/env bash
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
MD2TAPE="$HERE/md2tape.py"
WRAPDIR="$HERE/bin"

usage() {
  cat <<'EOF'
Usage: demo/render.sh <mode> <file.md> [outdir]

Modes:
  preview   GIF output, 24fps, real sleeps -- quick visual check
  fast      full quality but VP9 encoded with realtime flags (5-10x faster)
  frames    skip ffmpeg entirely; export lossless PNG sequence for editing
  final     full quality, default settings, no shortcuts
  debug     inspect a broken run: PNG frames + final-screen text dump +
            a runnable live-repro script you can step through by hand

Args:
  file.md   annotated markdown (e.g. Documentation/sessions.md, demo/tape-example.md)
  outdir    where tape/output land (default: demo/out)

Env:
  FRAMERATE   override framerate for fast/frames/final (default: tape's own)
  HUMAN       set to 1 for human-like typing (varied speed, pauses, typos)
  PROMPT      shell prompt for the recorded terminal (default: vhs's purple '> ')

Examples:
  demo/render.sh preview demo/tape-example.md
  demo/render.sh fast    demo/tape-example.md
  demo/render.sh frames  demo/tape-example.md
  demo/render.sh final   Documentation/sessions.md
EOF
}

[ $# -ge 2 ] || { usage; exit 1; }

MODE="$1"
MD="$2"
OUT="${3:-$HERE/out}"
[ -f "$MD" ] || { echo "no such file: $MD" >&2; exit 1; }
command -v vhs >/dev/null || { echo "vhs not found on PATH" >&2; exit 1; }

mkdir -p "$OUT"
base="$(basename "${MD%.*}")"
tape="$OUT/$base.tape"

[ -n "${PROMPT:-}" ] && export PS1="$PROMPT"
hu=()
[ -n "${HUMAN:-}" ] && hu=(--human)
gen() { python3 "$MD2TAPE" "$MD" "${hu[@]}" "$@" -o "$tape"; }
fr=()
[ -n "${FRAMERATE:-}" ] && fr=(--framerate "$FRAMERATE")

case "$MODE" in
  preview)
    gen --format gif --framerate "${FRAMERATE:-24}"
    echo "==> preview (GIF, real sleeps)"
    vhs "$tape"
    ;;
  fast)
    gen "${fr[@]}"
    echo "==> fast VP9 encode"
    PATH="$WRAPDIR:$PATH" vhs "$tape"
    ;;
  frames)
    gen "${fr[@]}"
    echo "==> frame export (no encode)"
    DEMO_FRAMES="$OUT/${base}-frames" PATH="$WRAPDIR:$PATH" vhs "$tape"
    echo "frames in: $OUT/${base}-frames"
    ;;
  final)
    gen "${fr[@]}"
    echo "==> final render"
    vhs "$tape"
    ;;
  debug)
    gen "${fr[@]}"
    script="$OUT/${base}.live.sh"
    python3 "$MD2TAPE" "$MD" --script -o "$script"
    txt="$OUT/${base}-final.txt"
    if grep -qiE "^Output .*\\.txt" "$tape"; then :; else
      awk -v t="$txt" 'NR==1&&/^Output /{print; print "Output \"" t "\""; next} {print}' "$tape" > "$tape.tmp" && mv "$tape.tmp" "$tape"
    fi
    echo "==> debug: frames + final-screen text; no encode"
    DEMO_FRAMES="$OUT/${base}-frames" PATH="$WRAPDIR:$PATH" vhs "$tape" || true
    echo
    echo "live-repro script : $script   (run in a real terminal to watch it fail)"
    echo "final screen text : $txt"
    echo "frames            : $OUT/${base}-frames"
    ;;
  -h|--help|help)
    usage
    ;;
  *)
    echo "unknown mode: $MODE" >&2
    usage
    exit 1
    ;;
esac
