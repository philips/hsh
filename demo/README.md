# demo — markdown-driven screen recordings

`md2tape.py` converts an annotated markdown file into a
[VHS](https://github.com/charmbracelet/vhs) `.tape` file. The same markdown is
your blog post, your narration script, and your recording source — all under
version control.

## Usage

```sh
python3 demo/md2tape.py Documentation/sessions.md -o demo/out/sessions.tape
vhs demo/out/sessions.tape        # produces the .mp4/.gif set by tape:output
```

Pipe to stdout to preview without writing a file:

```sh
python3 demo/md2tape.py demo/example.md
```

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
| `<!-- tape:hide -->` / `<!-- tape:show -->` | `Hide` / `Show` | run setup off-camera |
| `<!-- tape:raw Ctrl+L -->` | passthrough | emit any raw VHS line |
| `<!-- tape:say ... -->` | nothing | voiceover-only note, ignored by VHS |

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
