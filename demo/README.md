# demo — markdown-driven screen recordings

`md2tape.py` converts an annotated markdown file into a
[VHS](https://github.com/charmbracelet/vhs) `.tape` file. The same markdown is
your blog post, your narration script, and your recording source — all under
version control.

## Usage

The quickest path is `render.sh`, which wraps tape generation + vhs + the
encode options below:

```sh
demo/render.sh preview demo/tape-example.md   # fast GIF, half size, sleeps/3
demo/render.sh fast    demo/tape-example.md   # full quality, fast VP9 encode
demo/render.sh frames  demo/tape-example.md   # skip ffmpeg, export PNG frames
demo/render.sh final   Documentation/sessions.md  # full quality, no shortcuts
demo/render.sh debug   demo/tape-example.md       # inspect a broken run
```

Set `FRAMERATE=24 demo/render.sh ...` to cut frames for any encoded mode.

To drive the steps by hand instead:

```sh
python3 demo/md2tape.py Documentation/sessions.md -o demo/out/sessions.tape
vhs demo/out/sessions.tape        # produces the .mp4/.gif set by tape:output
```

Pipe to stdout to preview without writing a file:

```sh
python3 demo/md2tape.py demo/example.md
```

## Fast iteration

Rendering full-res VP9 WebM is slow, and VHS waits out `Sleep`s in real time.
Use these flags to tighten the edit loop, then drop them for the final render:

```sh
# dev preview: half size, 24fps, sleeps /3, GIF output (skips VP9)
python3 demo/md2tape.py demo/tape-example.md --fast -o demo/out/preview.tape
vhs demo/out/preview.tape
```

| Flag | Effect |
|------|--------|
| `--fast` | half dimensions, `Framerate 24`, sleeps /3, output as GIF |
| `--speed N` | divide every `Sleep` by N (min 50ms) without other changes |
| `--framerate N` | framerate used with `--fast` (default 24) |
| `--format gif\|webm\|mp4` | override the `tape:output` extension |

Other speedups: `vhs validate file.tape` checks syntax with no render, and a
`Screenshot demo/out/frame.png` line captures a single frame to inspect layout
without producing a video.

## Faster WebM encode (when sleeps are fixed)

If your `Sleep`s are real API waits you can't shorten, cut the *encode* instead:

1. **Lower the framerate** (VHS defaults to 50fps). Fewer frames to encode,
   zero effect on sleeps. Works standalone:
   ```sh
   python3 demo/md2tape.py demo/tape-example.md --framerate 24 -o demo/out/webserver.tape
   ```

2. **Speed up libvpx-vp9.** VHS calls `ffmpeg` from `PATH`; `demo/bin/ffmpeg`
   is a wrapper that injects `-deadline realtime -cpu-used 8 -row-mt 1` for VP9
   (often 5-10x faster, fine quality for a screencast). Put it first on PATH
   only for the render:
   ```sh
   PATH="$PWD/demo/bin:$PATH" vhs demo/out/webserver.tape
   ```
   The wrapper passes everything else through to the real ffmpeg untouched.

Combine both for the biggest win without changing a single `Sleep`.

## Re-encode a take without re-recording

The slow/variable part is the *take* (VHS replays the whole timed session). Once
captured, switching formats is just an ffmpeg transcode. `demo/encode.sh` turns
an existing frames directory (best) or GIF into any format:

```sh
# capture once, lossless:
demo/render.sh frames demo/tape-example.md
# then produce outputs from the SAME take, no re-record:
demo/encode.sh demo/out/tape-example-frames demo/out/final.webm
demo/encode.sh demo/out/tape-example-frames demo/out/preview.gif

# or salvage an old preview GIF into a webm:
demo/encode.sh demo/out/webserver.gif demo/out/webserver.webm
```

Set `FRAMERATE=` to match the take's framerate when encoding from frames. If
the tape has no `Set Framerate`, VHS captured at 50fps, so use `FRAMERATE=50`
for real-time playback. `SCALE=640` shrinks a GIF preview. Frames give the best
quality; a GIF source is capped by its 256-color palette.

GIF encoding uses a two-pass palette to keep memory flat. Do not switch it to
the common one-pass `palettegen/paletteuse` split filter — it buffers the whole
video in RAM (a ~4900-frame take at 1134x570 needs ~12GB) and the kernel OOM
killer will take down the entire session, tmux included.

## Skip ffmpeg entirely — export raw frames

VHS renders a lossless PNG frame sequence first, then only uses ffmpeg to pack
it into a video. Set `DEMO_FRAMES` and the `demo/bin/ffmpeg` wrapper copies the
frames out and skips the encode completely:

```sh
DEMO_FRAMES="$PWD/demo/out/frames" PATH="$PWD/demo/bin:$PATH" vhs demo/out/webserver.tape
```

Result: `demo/out/frames/*.png` (visually lossless, instant). Import the
sequence into your editor (DaVinci Resolve auto-detects image sequences;
Kdenlive: "Add Image Sequence") and set the clip fps to match `Set Framerate`.
Encode to H.264/VP9 just once, on final export.

The real timings/quality still live in the markdown, so the final build is just
`md2tape.py` + `vhs` with no flags.

## Seeing what went wrong

VHS runs headless (a hidden ttyd terminal inside headless Chromium), so there
is no live window. To inspect a failing run, `demo/render.sh debug <file.md>`
gives you three views at once:

- **`<base>.live.sh`** — the typed commands as a runnable bash script. Run it
  in a real terminal to watch the failure happen live. Generate it directly
  with `python3 demo/md2tape.py <file.md> --script`.
- **`<base>-final.txt`** — VHS writes the final terminal screen as plain text
  (readable, greppable error messages). Add more `Output *.txt` or
  `Screenshot *.png` lines via `<!-- tape:output ... -->` / `<!-- tape:raw
  Screenshot ... -->` for intermediate checkpoints.
- **`<base>-frames/`** — the full lossless PNG sequence; scrub it to find the
  exact frame where things broke.

Also useful: `vhs validate <tape>` catches syntax errors before any render.


## Markup

All directives are HTML comments, so they are invisible in rendered markdown.
Regular prose is ignored by the recorder and serves as blog text / voiceover.

| Directive | VHS output | Notes |
|-----------|------------|-------|
| `<!-- tape:output demo/out/x.mp4 -->` | `Output demo/out/x.mp4` | always applied, even outside a region |
| `<!-- tape:set FontSize 22 -->` | `Set FontSize 22` | any VHS `Set` key |
| `<!-- tape:require hsh -->` | `Require hsh` | fail early if binary missing |
| `<!-- tape:run -->` + code block | types each line + `Enter` | records the next fenced block |
| `<!-- tape:run sleep=4s -->` | pause after the block | default `sleep=2s`; `sleep=none` to skip |
| `<!-- tape:run typingspeed=90ms -->` | slows typing for that block | resets to 60ms after |
| `<!-- tape:run noenter -->` | type without pressing Enter | for interactive prompts |
| `<!-- tape:type Thanks! -->` | `Type "Thanks!"` | literal text, no Enter |
| `<!-- tape:key Ctrl+C -->` | `Ctrl+C` | any key: `Enter`, `Tab`, `Ctrl+L`, ... |
| `<!-- tape:sleep 3s -->` | `Sleep 3s` | |
| `<!-- tape:monitor -->` | `Sleep 1s` + `Ctrl+Space` + `Sleep 1s` | opens the hsh monitor; tune with `sleep=`/`after=` (or `none`) |
| `<!-- tape:hide -->` / `<!-- tape:show -->` | `Hide` / `Show` | run setup off-camera |
| `<!-- tape:raw Ctrl+L -->` | passthrough | emit any raw VHS line |
| `<!-- tape:say ... -->` | nothing | voiceover-only note, ignored by VHS |

## Human-like typing

VHS types at a constant robotic rate. `--human` rewrites typed commands with
varied per-word speed, short thinking pauses, and the occasional typo that gets
backspaced and corrected. It is seeded from the command text, so the output is
deterministic and diff-friendly in git.

```sh
python3 demo/md2tape.py demo/tape-example.md --human -o demo/out/webserver.tape
# or via render.sh:
HUMAN=1 demo/render.sh final demo/tape-example.md
```

Per-block overrides: `<!-- tape:run human -->` forces it on for one block,
`<!-- tape:run nohuman -->` forces it off. The base speed comes from the
block's `typingspeed=` (default 60ms) and is jittered around that.

## Light terminal (white background)

VHS defaults to a dark theme. Set a light one with a `tape:set Theme`
directive. Either a built-in theme:

```
<!-- tape:set Theme "Catppuccin Latte" -->
```

(run `vhs themes` to list them; other light options: `Github`, `Solarized
Light`, `One Light`), or an inline JSON palette on one line for an exact
white-background / black-text look (see `tape-example.md`):

```
<!-- tape:set Theme { "background": "#ffffff", "foreground": "#000000", ... } -->
```

## Regions

If a file contains any `<!-- tape START -->` markers, only content between
`START` and `<!-- tape END -->` is recorded (config directives still apply
anywhere). This lets you annotate a real `Documentation/*.md` page and record
just the relevant section. With no region markers, the whole file is processed.

## Why this workflow

- One source of truth in git: the demo is pinned to a tool version per commit.
- Tool output changed? Edit one line, re-run `md2tape.py` + `vhs`, done — no reshoot.
- Deterministic terminal visuals; record voiceover separately and composite for
  a natural YouTube feel.

See `example.md` for a complete annotated example.
