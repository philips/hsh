# libghostty for attach/detach and approval popovers

> **Status: foundation and the passthrough relay are implemented**
> (`internal/vt`, `internal/relay`, plans/0026-detach-attach.md Phase 2).
> The research below is the record of the decisions; the prototype is in
> `plans/prototype/libghostty/` (`./test.sh` runs it).

## Question

TODO.md asked whether libghostty plus a custom escape sequence could let
sessions be attached and detached and let the monitor pop over the
session for firewall approvals ("an application specific tmux").

## What libghostty is

`libghostty-vt` is the terminal-emulation core extracted from Ghostty: a
C API (Zig implementation, needs Zig 0.17) that parses VT sequences and
maintains terminal state — screen, scrollback, alt screen, cursor, modes,
styles — plus a formatter (plain/VT/HTML), snapshot encode/restore, an
OSC parser, and key/mouse/paste encoders. It has no pty, no window, and
no renderer. Upstream marks the API incomplete and unstable.

That is exactly the missing piece. Today `hsh run` hands the terminal to
`podman start -ai`, so nothing on the host knows what is on the screen;
a popover or a reattach would have to ask the agent to redraw. With an
emulator in the relay path, the relay can draw over the screen and put
back exactly what was underneath.

## How it can be built into a static pure-Go hsh

hsh ships as `CGO_ENABLED=0` static binaries cross-compiled for
linux/amd64 and arm64 from one runner.

| Option | Pure Go | Notes |
|---|---|---|
| cgo + `libghostty-vt.a` | no | breaks `CGO_ENABLED=0`; Zig 0.17 and a C cross toolchain in CI |
| wasm + a Go wasm runtime (wazero) | yes | extra runtime dependency; interpreter/JIT overhead |
| wasm converted to Go source (`wasm2go`) | yes | what `code.selman.me/hauntty/libghostty` does: a committed 654 KB `.wasm` (sha256 pinned) turned into a 3.5 MB generated Go file, no runtime |
| pure-Go emulator (`charmbracelet/x/vt`) | yes | hsh already uses the charm libraries |

The prototype uses the third option. Measured on this machine:

| | libghostty (wasm2go) | `charmbracelet/x/vt` v0.1.0 |
|---|---|---|
| feeding 38 MB of colored, scrolling text into 200x50 | 2.4 s (about 17 MB/s) | 43.7 s (about 0.9 MB/s) |
| minimal binary | 7.1 MB | 4.5 MB |
| terminal creation | 3 ms | 1.4 ms |

These are rough microbenchmarks of one worst-case stream (a flood of
scrolling SGR text), not a verdict on either library. Interactive use is
far below either rate; a `cat` of a large file through the relay is the
case that would show the difference. The hsh binary is currently 21 MB.

Costs of the wasm2go route:

- **Toolchain**: the generated code needs Go 1.27 with
  `GOEXPERIMENT=simd`. hsh's `go.mod` says 1.26, so `test.yml`,
  `release.yml`, and DEVELOP.md would change. Both architectures built
  with the experiment in the prototype environment.
- **Supply chain**: the wasm is a prebuilt blob in a third-party module
  reached through a vanity import path. hsh would vendor or pin it and
  record which Ghostty commit it was built from. Memory-safety upside:
  the Zig parser runs inside wasm linear memory, which suits parsing
  untrusted agent output.
- **API stability**: upstream says it will break.

## What the prototype shows

`ptymux` is a pty relay with three client commands. `./test.sh` drives it
from a real pty and checks that:

1. The emulator tracks the screen while no client is attached (`dump`
   prints the plain-text screen).
2. Attaching restores the existing screen from emulator state
   (formatter VT output plus cursor), including alt-screen programs such
   as `less`.
3. Detaching leaves the session running; output produced while detached
   is on the screen at the next attach. Ctrl-] is the detach key.
4. `notify PROMPT` draws a popover bar over the attached client, takes
   over input (the answer key never reaches the shell), returns
   `approved`/`denied`, and the screen underneath is restored by redrawing
   from the emulator — the agent is not asked to repaint. The popover
   never enters the emulated screen.
5. A custom OSC (`ESC ] 7777 ; approve ; HOST BEL`) emitted inside the
   session is recognised, stripped before it reaches the emulator and the
   client's terminal, and raises the same popover; the decision is
   appended to a log file.
6. A prompt raised while nobody is attached waits and appears on the next
   attach; `--timeout` gives up cleanly.

## Findings on the custom escape sequence

The agent controls everything written to the pty, so it can emit the
sequence itself. The sequence is therefore only a hint ("something wants
your attention"), never authority:

- The text and the decision must come from state the agent cannot write
  (the host's `denied.db` and the host-side proxy watcher), not from the
  sequence payload. The prototype takes the host name from the payload
  only to demonstrate the mechanism.
- The popover is drawn by the relay on a screen region the agent does not
  own while it is active, and input is routed to the relay for as long as
  it is up. An agent can still *print* something that looks like a
  popover; it cannot make the relay treat a keystroke as an approval.
- Unrecognised OSC numbers pass through untouched; the filter handles
  sequences split across reads and both BEL and ST terminators.

## Where the emulator has to live

`hsh run` on the host exits when the user detaches, so persistence needs
a longer-lived owner of the pty and the emulator:

- **Host daemon per session**: owns the pty relay to the container,
  survives detach, and is the natural home for approval popovers because
  it sits outside the sandbox (plans/0024-process-approval.md, option B).
  Cost: a daemon lifecycle (start, adopt after reboot, cleanup in `hsh
  rm`/`gc`).

The emulator cannot live in the pods as then the popover logic then sit on the
agent's side of the boundary, which defeats the point for approvals.

It also resolves 0024's open work: the pty relay replaces `podman start -ai`,
and detach/reattach plus popovers come from the same code.

## Decisions

0. Build a simple prototype for a user to test of a single "status line" for `hsh` that has notification of proxy items needing review- then change that status line from green to red when the user presses Ctrl+Space and pops over a fake approval dialog list.
1. Use Go 1.27 / `GOEXPERIMENT=simd` and bump go.mod
2. Attempt to build the wasm from a pinned Ghostty commit ourselves.
3. Use libghostty for the emulator, behind a small interface (`Write`,
   `Resize`, `Snapshot`) so `charmbracelet/x/vt` or a later cgo build can be
   swapped in.
4. Build the host-side pty relay first (no emulator: raw relay, resize, detach
   key) since both plans depend on it and it is independently useful; add the
   emulator for restore and popovers once the relay is in.
5. Do not ship the in-band OSC trigger without the rule above that payloads
   carry no authority.
6. **Inline dialog handling for the monitor uses passthrough mode, not render
   mode.** The relay forwards the session's bytes unchanged so the user's
   terminal keeps its scrollback, selection, and search. Ctrl+Space pauses by
   not reading the pty and pops the review dialog; closing repaints the rows
   it covered and flushes the held output. The reserved status row is
   optional (`monitor.status_line`, default on) because it is the part with
   the known gaps; the dialog alone needs no reserved row. The status line is
   not part of scrollback, so it is not visible while scrolled back. The
   relay lives in `hsh run` itself (plans/0026-detach-attach.md); a host daemon is an optional later phase there.
   Render mode stays in the prototype for comparison only.

## Status line prototype (decision 0)

`ptymux demo` in `plans/prototype/libghostty/` is the thing to try by
hand (see its README). It reserves the bottom terminal row for an `hsh`
status line, shrinks the session's pty and emulator by one row, and
simulates proxy denials arriving. The line is green and announces
`N proxy requests need review`; Ctrl+Space turns it red and pops over a
list where each item can be approved or denied (up/down, `a`, `d`, `A`,
`D`, Esc to close). The data is fake; nothing reads `denied.db`.

What building it showed:

- **A reserved row forces the relay to render, not pass through.** A
  passthrough client would let the session scroll into the status row
  (a child at its last line writes over it) or let the child reset the
  scroll region. The prototype therefore sends the client a full frame
  built from emulator state (coalesced to one per 25 ms, wrapped in
  synchronized-output mode `?2026`) instead of raw child bytes. This is
  how tmux works, and it makes popovers and status trivial to draw, but
  the user's own terminal scrollback no longer sees session output; the
  relay would need its own scrollback view.
- **Ctrl+Space arrives as a NUL byte** on most terminals. It is a fine
  hsh key but shadows Emacs' set-mark inside the session; the key should
  be configurable. Terminals using the kitty keyboard protocol send it
  differently, which the "Tests" item on keyboard protocol passthrough
  has to cover.
- **Red means hsh holds the keyboard.** The line turns red for the review
  dialog and for the y/n prompt bar, so the user can tell input is not
  going to the agent.
- The screen under the dialog keeps updating (the agent is not paused);
  closing the dialog needs no repaint request to the agent.
- `render_test.go` feeds the relay's frames into a second emulator that
  stands in for the user's terminal and asserts the visible rows: status
  text, dialog contents, session content preserved around the dialog, and
  restoration after close.

## Passthrough prototype

`ptymux demo --mode passthrough` (same README) forwards the child's bytes
to the user's terminal unchanged and keeps the status line and dialog.
The pieces that make that work, and what they cost:

- **Pause is "stop reading the pty".** While the review dialog or a prompt
  bar is up the relay does not read the pty master; the child blocks in
  `write()` once the kernel buffer fills. Nothing is dropped, and the
  emulator, which is fed only what was forwarded, is exactly what the
  user's terminal shows. Bytes that arrive between the keypress and the
  pause are held and delivered on resume. Closing repaints only the rows
  the overlay covered, one grid-ref selection per row, then flushes. A
  detach while paused releases the pause.
- **Only inject at ground state.** A read can end mid-escape-sequence;
  writing a status line or dialog into the middle of it corrupts the
  output. libghostty's `VTGround()` says whether the stream is between
  sequences. Overlays and status redraws wait for it.
- **The reserved row is a scroll region.** The pty is sized one row short
  and the client terminal gets `DECSTBM 1;rows-1`. Children that reset the
  region (`ESC[r`) would hand the status row back to the scroller, so the
  relay rewrites DECSTBM to stay inside the region. `clear` and entering
  the alternate screen wipe the row; it is repainted within one 25 ms
  tick.
- **Scrollback survives a partial region in Ghostty's terminal core.**
  Tested by feeding scrolling output into libghostty with a top-anchored
  region and counting history lines: all lines were kept. That is one
  terminal's behavior; xterm-family terminals agree as far as I know, but
  kitty, iTerm2, Terminal.app, tmux, and others must be checked before
  relying on it.
- **Attach restores scrollback.** The formatter output includes history,
  so replaying it fills a fresh terminal's scrollback; the visible rows
  are then repainted in place.

Known gaps, all inherent to injecting into a live stream:

- The relay uses DECSC/DECRC (`ESC 7` / `ESC 8`) around its drawing, which
  clobbers a child's own saved cursor if it saves before a pause and
  restores after.
- Cursor visibility is tracked by scanning for `?25h/l`, not read from
  the emulator, so combined mode sequences are missed.
- Child terminal resets (`RIS`, `DECSTR`), other margin sequences, and
  mouse or keyboard mode changes while paused are not modelled.
- The status row flickers for up to one tick after the child clears the
  screen.

Compared with render mode, the same dialog, status line, and decision
flow work in both. Render mode needs none of the above and has none of
the gaps, but owns scrollback and selection; passthrough keeps the user's
terminal behaviour and carries the gaps. The tests that back this are
`TestPassthroughPauseLeavesTheClientTerminalIdenticalToTheEmulator` (the
client terminal, a second libghostty fed only the relay's output, equals
the emulator after a pause with 30 held lines) and
`TestPassthroughStatusRowSurvivesScrollClearAndAltScreen`.

## Implementation order

This plan's deliverables are the toolchain bump (decision 1), the pinned
wasm build (decision 2), and the emulator package behind a small interface
(decision 3). The relay, status row, and dialog (decisions 4 and 6) are
built in plans/0026-detach-attach.md Phase 2 on top of them; 0026 Phase 1
does not depend on this plan and can go first.

## Foundation (implemented)

- `go.mod` is on Go 1.27; the CI workflows (`test.yml`, `release.yml`)
  set `GOEXPERIMENT=simd`, and DEVELOP.md documents it.
- `internal/vt` is the emulator interface (`Write`, `Resize`, `Ground`,
  `Cursor`, `Plain`, `Replay`, `Row`, `Close`). The libghostty backend
  (`ghostty.go`, `code.selman.me/hauntty/libghostty`) is compiled only
  with `GOEXPERIMENT=simd`; without it `none.go` returns
  `ErrUnavailable`, so `go build`, `go vet`, and `go test` keep working
  everywhere and the emulator tests skip. Nothing links the package into
  the binary yet; the relay (0026 Phase 2) will.
- **Provenance of the wasm.** The blob in that module is byte-identical
  to the official Ghostty release-tip artifact for revision
  `7f62fe70a288c5d35ebd3097e75c46017950bcc7`
  (`tip.files.ghostty.org/<rev>/ghostty-vt-small.wasm`, sha256
  `98ed3473d125460daea8602cebe2cd0e5a63209c908b72f054be712c2ccdcf6c`).
  `internal/vt/provenance_test.go` pins both, so a dependency bump that
  changes the blob fails a test and has to be a conscious decision.
- **Reproducing it ourselves (decision 2): attempted, not bit-exact.**
  Ghostty builds the artifact with `zig build -Demit-lib-vt
  -Dtarget=wasm32-freestanding -Doptimize=ReleaseSmall` followed by
  binaryen's `wasm-opt -O3` with simd, bulk-memory, sign-ext,
  nontrapping-float-to-int, multivalue and reference-types enabled, inside
  a nix environment, and signs it with minisign. At that revision
  (minimum Zig 0.16.0) the Zig build takes about a minute and yields a
  730 KB module; wasm-opt from binaryen version_133 turns it into 654,413
  bytes against the official 654,354, so the output is not identical. The
  difference is expected to be the binaryen version pinned by Ghostty's
  nix lock, which I could not match (older binaryen release tarballs were
  not fetchable and the disk filled up). Bit-exact reproduction therefore
  remains open; the supply-chain control today is the pinned revision and
  hash above plus the official artifact's minisign signature, which this
  repository does not yet verify.

## Tests

- Paste, mouse, focus reports, and bracketed paste while the review screen is
  open: implemented and tested. Pasted text reaches the monitor model as one
  pasted value (so it fills the filter and never triggers a command), mouse
  and focus reports are discarded, and the relay turns bracketed paste on for
  the user's terminal while the screen is open and restores the session's own
  setting on close. Lines the model draws are truncated to the screen width.
- Terminal compatibility is checked by hand: see `docs/terminal-checks.md`.

## Future Work (out of scope)

- Multiple simultaneous clients: resolved in 0026 (one at a time, with
  `hsh detach` and `--take-over`); read-only observers are possible later.
- Scrollback restore on reattach (only the active screen is replayed);
  relevant only with the optional host daemon of 0026 Phase 4.
- The remaining passthrough gaps: the relay's DECSC/DECRC use can overwrite a
  program's own saved cursor; cursor visibility is tracked by scanning for
  `?25h`/`?25l` rather than read from the emulator; terminal resets (`RIS`,
  `DECSTR`) and other margin sequences are not modelled; mouse or keyboard
  mode changes made while the output is paused are not modelled; the status
  row flickers for up to one tick after the child clears the screen.
- The in-band OSC hint (`ESC ] 7777 ; ... BEL`) is recognised and stripped,
  but nothing in the sandbox emits it (the proxy cannot write to the
  session's pty); the two-second poll does the work. It becomes useful if a
  channel from the proxy to the pty is ever added.
