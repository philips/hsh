# libghostty for attach/detach and approval popovers

> **Status: research, with a working prototype** in
> `plans/prototype/libghostty/` (`./test.sh` runs it). Nothing in
> `internal/` changes yet.

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
- **Inside the workspace parent**: persists as long as the pod runs, no
  new host process, but the emulator and popover logic then sit on the
  agent's side of the boundary, which defeats the point for approvals.

Prefer the host daemon. It also resolves 0024's open work: the pty relay
replaces `podman start -ai`, and detach/reattach plus popovers come from
the same code.

## Recommendation

1. Keep libghostty as the leading candidate for the emulator, behind a
   small interface (`Write`, `Resize`, `Snapshot`) so `charmbracelet/x/vt`
   or a later cgo build can be swapped in.
2. Build the host-side pty relay first (no emulator: raw relay,
   resize, detach key) since both plans depend on it and it is
   independently useful; add the emulator for restore and popovers once
   the relay is in.
3. Decide the Go 1.27 / `GOEXPERIMENT=simd` bump deliberately; it is the
   biggest non-technical cost.
4. Do not ship the in-band OSC trigger without the rule above that
   payloads carry no authority.

## Not covered

- Throughput under a realistic burst through the relay (the numbers above
  are the emulator alone).
- Mouse, bracketed paste, and kitty keyboard protocol passthrough while a
  popover is up.
- Multiple simultaneous clients (the prototype detaches the previous
  client, like `tmux attach -d`).
- Scrollback restore on reattach (only the active screen is replayed).
- Building the wasm from a pinned Ghostty commit ourselves.
