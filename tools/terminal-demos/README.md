# terminal-demos — markdown-driven screen recordings

`md2tape.py` converts an annotated markdown file into a
[VHS](https://github.com/charmbracelet/vhs) `.tape` file. The same markdown is
your blog post, your narration script, and your recording source — all under
version control.

## Usage

The quickest path is `render.sh`, which wraps tape generation + vhs + the
encode options below:

```sh
tools/terminal-demos/render.sh preview tools/terminal-demos/tape-example.md   # fast GIF, half size, sleeps/3
tools/terminal-demos/render.sh fast    tools/terminal-demos/tape-example.md   # full quality, fast VP9 encode
tools/terminal-demos/render.sh frames  tools/terminal-demos/tape-example.md   # skip ffmpeg, export PNG frames
tools/terminal-demos/render.sh final   Documentation/sessions.md  # full quality, no shortcuts
tools/terminal-demos/render.sh debug   tools/terminal-demos/tape-example.md       # inspect a broken run
```

Set `FRAMERATE=24 tools/terminal-demos/render.sh ...` to cut frames for any encoded mode.

To drive the steps by hand instead:

```sh
python3 tools/terminal-demos/md2tape.py Documentation/sessions.md -o tools/terminal-demos/out/sessions.tape
vhs tools/terminal-demos/out/sessions.tape        # produces the .mp4/.gif set by tape:output
```

Pipe to stdout to preview without writing a file:

```sh
python3 tools/terminal-demos/md2tape.py tools/terminal-demos/example.md
```

## Fast iteration

Rendering full-res VP9 WebM is slow, and VHS waits out `Sleep`s in real time.
Use these flags to tighten the edit loop, then drop them for the final render:

```sh
# dev preview: half size, 24fps, sleeps /3, GIF output (skips VP9)
python3 tools/terminal-demos/md2tape.py tools/terminal-demos/tape-example.md --fast -o tools/terminal-demos/out/preview.tape
vhs tools/terminal-demos/out/preview.tape
```

| Flag | Effect |
|------|--------|
| `--fast` | half dimensions, `Framerate 24`, sleeps /3, output as GIF |
| `--speed N` | divide every `Sleep` by N (min 50ms) without other changes |
| `--framerate N` | framerate used with `--fast` (default 24) |
| `--format gif\|webm\|mp4` | override the `tape:output` extension |

Other speedups: `vhs validate file.tape` checks syntax with no render, and a
`Screenshot tools/terminal-demos/out/frame.png` line captures a single frame to inspect layout
without producing a video.

## Faster WebM encode (when sleeps are fixed)

If your `Sleep`s are real API waits you can't shorten, cut the *encode* instead:

1. **Lower the framerate** (VHS defaults to 50fps). Fewer frames to encode,
   zero effect on sleeps. Works standalone:
   ```sh
   python3 tools/terminal-demos/md2tape.py tools/terminal-demos/tape-example.md --framerate 24 -o tools/terminal-demos/out/webserver.tape
   ```

2. **Speed up libvpx-vp9.** VHS calls `ffmpeg` from `PATH`; `tools/terminal-demos/bin/ffmpeg`
   is a wrapper that injects `-deadline realtime -cpu-used 8 -row-mt 1` for VP9
   (often 5-10x faster, fine quality for a screencast). Put it first on PATH
   only for the render:
   ```sh
   PATH="$PWD/tools/terminal-demos/bin:$PATH" vhs tools/terminal-demos/out/webserver.tape
   ```
   The wrapper passes everything else through to the real ffmpeg untouched.

Combine both for the biggest win without changing a single `Sleep`.

## Re-encode a take without re-recording

The slow/variable part is the *take* (VHS replays the whole timed session). Once
captured, switching formats is just an ffmpeg transcode. `tools/terminal-demos/encode.sh` turns
an existing frames directory (best) or GIF into any format:

```sh
# capture once, lossless (no video file is produced):
tools/terminal-demos/render.sh frames tools/terminal-demos/tape-example.md
# then produce outputs from the SAME take, no re-record:
tools/terminal-demos/encode.sh tools/terminal-demos/out/tape-example-frames tools/terminal-demos/out/final.webm
tools/terminal-demos/encode.sh tools/terminal-demos/out/tape-example-frames tools/terminal-demos/out/preview.gif

# or salvage an old preview GIF into a webm:
tools/terminal-demos/encode.sh tools/terminal-demos/out/webserver.gif tools/terminal-demos/out/webserver.webm
```

In `frames` mode the tape's `tape:output` is retargeted to a temporary stub
inside the frames directory and removed after the run, so no fake
`webserver.webm` appears next to the frames.

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
it into a video. Set `DEMO_FRAMES` and the `tools/terminal-demos/bin/ffmpeg` wrapper copies the
frames out and skips the encode completely:

```sh
DEMO_FRAMES="$PWD/tools/terminal-demos/out/frames" PATH="$PWD/tools/terminal-demos/bin:$PATH" vhs tools/terminal-demos/out/webserver.tape
```

Result: `tools/terminal-demos/out/frames/*.png` (visually lossless, instant). Import the
sequence into your editor (DaVinci Resolve auto-detects image sequences;
Kdenlive: "Add Image Sequence") and set the clip fps to match `Set Framerate`.
Encode to H.264/VP9 just once, on final export.

The real timings/quality still live in the markdown, so the final build is just
`md2tape.py` + `vhs` with no flags.

## Seeing what went wrong

VHS runs headless (a hidden ttyd terminal inside headless Chromium), so there
is no live window. To inspect a failing run, `tools/terminal-demos/render.sh debug <file.md>`
gives you three views at once:

- **`<base>.live.sh`** — the typed commands as a runnable bash script. Run it
  in a real terminal to watch the failure happen live. Generate it directly
  with `python3 tools/terminal-demos/md2tape.py <file.md> --script`.
- **`<base>-final.txt`** — VHS writes the final terminal screen as plain text
  (readable, greppable error messages). Add more `Output *.txt` or
  `Screenshot *.png` lines via `<!-- tape:output ... -->` / `<!-- tape:raw
  Screenshot ... -->` for intermediate checkpoints.
- **`<base>-frames/`** — the full lossless PNG sequence; scrub it to find the
  exact frame where things broke.

Also useful: `vhs validate <tape>` catches syntax errors before any render.

## Waiting on events instead of sleeping

`Sleep` guesses how long something takes; `tape:wait` blocks until the event
actually happens. Two forms.

**Regex form (preferred, works anywhere including inside an agent TUI):**

```
<!-- tape:wait /SUSPENDED: proxy approval/ -->
```

emits `Wait+ Screen /SUSPENDED: proxy approval/` — VHS pauses the tape until
the regex shows up on the rendered terminal. This is the right tool for the
hsh status line (`hsh <id> | SUSPENDED: proxy approval | * 1 to review (1
held) | Ctrl+Space`) and for agent output. Caveat: the regex also matches text
that was *typed* on screen, so pick a pattern that only appears in the output
you are waiting for. Append `line` (`/pattern/ line`) to match new output
lines instead of the composited screen.

`Wait` has a built-in timeout (the default is short); raise it for slow agent
work with a `tape:set`:

```
<!-- tape:set WaitTimeout 3m -->
```

### Waiting for a command to complete

VHS has no wait-for-exit primitive, and often you do not need one: commands
typed while the shell is busy are buffered by the pty and run in order
(type-ahead). A wait only matters when the next line must be typed at a fresh
prompt for visual cleanliness.

Then wait on the command's completion output — either its real final line
(`hsh rm` prints `removed <name>`) or a marker you append. Because `Wait`
also matches typed text, build the marker so the typed line does not contain
it — split it with quotes:

```
<!-- tape:run sleep=none -->
```sh
hsh rm -f $HSH_SESSION_NAME && echo CLEANUP-FI""NISHED
```
<!-- tape:wait /CLEANUP-FINISHED/ -->
```

The screen shows the typed `CLEANUP-FI""NISHED` but the output is
`CLEANUP-FINISHED`, so the wait matches only completion. Do not wait on
`removed` in pre-take cleanup — `hsh rm -f` prints nothing when the session
does not exist, and the wait would time out and fail the render.

Notes: `Wait+ Line` does not work reliably in this vhs build (it misses real
output lines — verified), so stick to the `Screen` form. Waiting on the bare
prompt also does not work (the screen matcher never sees it cleanly).

**Shell form (needs a shell prompt):** VHS (v0.11) has no host-side command
execution, so the wait is typed into the terminal: it prefixes an
`until <cmd>; do sleep 0.5; done; ` loop onto the next `tape:run` command,
which then executes only after the event fires:

```
<!-- tape:wait timeout=180s hsh proxy denied | grep -q go.dev -->
<!-- tape:run sleep=3s -->
```sh
hsh resume
```
```

The wait gates the shell, not the tape: keep a generous `sleep=` after the
command (its output appears only once the event fired; trim idle time in the
editor). `timeout=` (default 60s) breaks the loop instead of hanging forever.

The vhs tape parser cannot represent double quotes in typed text, so shell-form
waits and `tape:run` blocks must use single quotes. The `--script` debug output
includes the same wait loop, so live repro matches.


## Markup

All directives are HTML comments, so they are invisible in rendered markdown.
Regular prose is ignored by the recorder and serves as blog text / voiceover.

| Directive | VHS output | Notes |
|-----------|------------|-------|
| `<!-- tape:output tools/terminal-demos/out/x.mp4 -->` | `Output tools/terminal-demos/out/x.mp4` | always applied, even outside a region |
| `<!-- tape:set FontSize 22 -->` | `Set FontSize 22` | any VHS `Set` key |
| `<!-- tape:require hsh -->` | `Require hsh` | fail early if binary missing |
| `<!-- tape:run -->` + code block | types each line + `Enter` | records the next fenced block |
| `<!-- tape:run sleep=4s -->` | pause after the block | default `sleep=2s`; `sleep=none` to skip |
| `<!-- tape:run typingspeed=90ms -->` | slows typing for that block | resets to 60ms after |
| `<!-- tape:run noenter -->` | type without pressing Enter | for interactive prompts |
| `<!-- tape:type Thanks! -->` | `Type "Thanks!"` | literal text, no Enter |
| `<!-- tape:key Ctrl+C -->` | `Ctrl+C` | any key: `Enter`, `Tab`, `Ctrl+L`, ... |
| `<!-- tape:wait /regex/ -->` | `Wait+ Screen /regex/` | block until the regex appears on the terminal (e.g. the hsh suspend status line); add `line` before the closing `/` to match new output lines instead |
| `<!-- tape:wait <shell cmd> -->` | typed `until <cmd>; do sleep 0.5; done; ` prefixed onto the next command | block until cmd exits 0 (needs a shell prompt); `timeout=` (default 60s) caps it |
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
python3 tools/terminal-demos/md2tape.py tools/terminal-demos/tape-example.md --human -o tools/terminal-demos/out/webserver.tape
# or via render.sh:
HUMAN=1 tools/terminal-demos/render.sh final tools/terminal-demos/tape-example.md
```

Per-block overrides: `<!-- tape:run human -->` forces it on for one block,
`<!-- tape:run nohuman -->` forces it off. The base speed comes from the
block's `typingspeed=` (default 60ms) and is jittered around that.

## The `> ` prompt

VHS injects its own prompt, `PS1=\[\e[38;2;90;86;224m\]> \[\e[0m\]` (a purple
`> `), into the recorded shell's environment, and runs bash with `--noprofile
--norc` so no rc file overrides it. Every typed line therefore renders prefixed
with `> ` — that is VHS's built-in look, not a shell error.

To use your own prompt, export `PS1` before vhs runs (VHS only injects its
default when none is set). `render.sh` exposes this as `PROMPT`:

```sh
PROMPT='philips $ ' tools/terminal-demos/render.sh frames tools/terminal-demos/tape-example.md
```

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
