# Host session daemon: detach, attach, and inline approvals

> **Status: plan.** Builds on plans/0025-libghostty.md (emulator,
> passthrough mode, decisions 1-6) and replaces the `podman start -ai`
> attach path in `internal/cli/run.go`. Prototype of the relay half:
> `plans/prototype/libghostty/`.

## Goal

1. A session's shell and agent keep running when the user detaches or
   closes the terminal, and `hsh run --resume` re-attaches to the *same*
   running shell with the screen and scrollback restored.
2. Proxy denials for the session surface inline: a status line and a
   Ctrl+Space review dialog drawn by a host process, outside the sandbox
   (plans/0024-process-approval.md option B).

## Where things stand

`hsh run` hands its own terminal to `podman run -it` / `podman start -ai`.
When the workspace container exits, `hsh run` removes `secrets.json` and
stops the pod. "Resume" starts a *new* shell in the same pod; the old
shell, the agent, and the screen are gone. Closing the terminal kills the
session.

## Design

### One daemon per session

`hsh run` starts `hsh-sessiond` for the session and then becomes a client
of it. A daemon per session keeps lifecycles and crash blast radius small
(one pod, one pty, one emulator, one `denied.db` filter) and needs no
global registry; `hsh ls` already enumerates sessions through pods. A
single global daemon would centralise the monitor but adds a long-lived
privileged-ish process and cross-session state; revisit only if a
cross-session dialog is wanted.

The daemon is the same binary re-executed with `argv[0]` `hsh-sessiond`
(the existing role dispatch in `main.go`), detached with `setsid` so it
survives the terminal and the client.

### What the daemon owns

- **The pty.** It allocates a pty pair and runs the existing podman
  command (`podman run -it` for a new session, `podman start -ai` for a
  restarted one) with the pty slave as its terminal. Verified: a
  `podman run -it` whose terminal is a pty slave runs normally and
  propagates resizes (`TIOCSWINSZ` on the master reaches the container's
  `stty size`). The podman client must be the slave's foreground process
  group (`Setsid` + `Setctty`). podman's default detach keys (Ctrl-P
  Ctrl-Q) would detach the *podman client*; pass `--detach-keys=` empty
  (to be verified) so only hsh's detach key applies.
- **The emulator** (libghostty-vt, behind the `Write/Resize/Snapshot`
  interface of 0025 decision 3), fed exactly the bytes forwarded to the
  client.
- **The review state** for its session: it watches `denied.db` filtered
  by the session id and applies approvals through the same code as
  `hsh proxy allow` (`allowHosts`: edit the session config copy, mark the
  rows allowed). That makes `hsh proxy allow --watch` unnecessary for
  attached sessions; the global `hsh monitor` TUI keeps working because
  both write the same database idempotently.
- **The container lifecycle.** When the podman client exits (shell
  exited), the daemon runs today's cleanup (`rm secrets.json`,
  `pod stop --time 2`, or `pod rm` for `--rm` sessions), tells any
  attached client the exit code, and exits.

### Client and protocol

`hsh run` and `hsh run --resume` connect to the daemon's unix socket,
send the terminal size, put the terminal in raw mode, and relay frames.
The framing is the prototype's (`input`, `output`, `resize`, `end`,
`mode`) plus a version byte in the handshake and the exit code in `end`.
Detach key: Ctrl-] by default, configurable. One client at a time; a new
attach detaches the previous one (like `tmux attach -d`).

On attach the daemon replays emulator history into the new terminal
(so its scrollback fills) and repaints the visible rows in place. This is
the passthrough sequence from the prototype (`attachPassLocked`).

Non-interactive uses (`hsh run` with stdin not a terminal, `--tasks`
headless runs) do not start a daemon and keep the current direct path.

### Inline dialog (passthrough, 0025 decision 6)

While the review dialog or a prompt bar is up the daemon stops reading
the pty, so the container blocks in `write()`; closing repaints the
covered rows from the emulator and flushes held bytes. Overlays and the
status row are written only when the emulator reports the stream is at
ground state. The status row is a scroll region on a pty one row short and
is optional (`monitor.status_line`); Ctrl+Space is configurable. The
dialog text and the approval targets come from `denied.db`, never from
the byte stream: an in-band OSC from the agent can at most say "look at
the queue".

### Security

- The socket lives in `$XDG_RUNTIME_DIR/hsh/<session-id>.sock` (mode 0600,
  parent 0700), falling back to a 0700 directory under the state dir only
  if there is no runtime dir. **Never under a directory that is bind
  mounted into a container.** The proxy container mounts the entire
  state directory read-write (`stateDir:/hsh-state`) and the session
  directory read-only, so a socket in either is reachable from the proxy.
- The daemon checks `SO_PEERCRED` uid on every connection.
- The daemon runs as the user on the host, outside the sandbox. It parses
  untrusted pty output, which is why the emulator is the wasm build: the
  parser runs in wasm linear memory. It strips the custom OSC and never
  forwards it to the user's terminal.
- Emulator history holds whatever the agent printed, including anything
  it echoed from secrets. It stays in daemon memory only and is gone when
  the daemon exits.
- The proxy's all-sessions state mount is a separate concern and is not
  changed here.

### Behaviour changes

- `hsh run --resume` on a session whose daemon is alive attaches to it;
  today it errors with "session is already running". With no daemon
  (pod stopped, reboot, daemon crash) it starts a fresh shell as today.
- `hsh ls` gains an attached/detached distinction in STATUS; `hsh rm`
  stops the daemon before removing the pod; `hsh gc` removes stale
  sockets and sessiond pid files.
- Detaching leaves the pod, proxy, and agent running with resolved
  `secrets.json` present, which is the same exposure as a running
  session today. The idle-scrub guarantee of plan 0019 applies once the
  shell exits.
- If the daemon is killed, its podman client receives SIGHUP and the
  container's shell dies, the same outcome as closing the terminal today.
  The pod is then idle-but-running; `--resume` and `gc` treat a running
  pod without a daemon as needing cleanup.

## Phases

1. **Raw relay** (no emulator): the daemon, socket, client, resize,
   detach key, exit-code propagation, `--resume` attach. This is 0025
   decision 4 and is useful alone.
2. **Lifecycle**: `ls`/`rm`/`gc` integration, stale-socket handling,
   crash behaviour, headless paths unchanged.
3. **Emulator**: history replay and visible-row repaint on attach.
4. **Inline monitor**: `denied.db` watcher, status row, Ctrl+Space dialog,
   pause-by-not-reading, in-band OSC hint.
5. Remove the direct `podman start -ai` attach path for interactive runs.

Phase 3 depends on the Go 1.27 / `GOEXPERIMENT=simd` bump and the pinned
wasm build (0025 decisions 1 and 2); phases 1-2 do not.

## Tests

Extend `test/functional.sh` (it already drives interactive sessions
through a python pty, step 14):

- Start a session, detach with the detach key, confirm the pod and
  workspace container are still running and `hsh ls` shows detached.
- Run a command in the shell, detach, wait, `hsh run --resume`: the same
  shell (a shell variable set before detaching is still set) and the
  earlier output on the restored screen.
- Output produced while detached is present after reattach.
- Resize in the client reaches the container.
- Shell exit stops the pod, removes `secrets.json`, and the client exits
  with the shell's exit code; daemon and socket are gone.
- Kill the daemon: the session ends, `--resume` starts fresh, `gc` is
  clean.
- Socket and directory modes; a second uid cannot connect.
- Piped and `--tasks` runs start no daemon.
- Inline monitor: a denial appears in the status row; Ctrl+Space pauses
  (a long-running `seq` stops advancing), `a` approves and the host is
  added to the session config, closing resumes with no lost output.
- Unit tests for the framing, the pause/restore equivalence (the client
  terminal equals the emulator), and the status-row region rewrite,
  carried over from the prototype.

## Open questions

- Per-session vs one global daemon (above; per-session recommended).
- Multiple simultaneous clients, including read-only observers.
- Whether history should survive a daemon restart (persisting it means
  agent output on disk).
- History size cap and replay cost for long sessions.
- Keys: Ctrl+Space and Ctrl-] as defaults, how they are configured, and
  how the kitty keyboard protocol changes what arrives.
- Terminals other than Ghostty's core: partial-region scrollback behaviour
  must be checked (kitty, iTerm2, Terminal.app, tmux) before the status row
  defaults on.
- Whether `hsh run` should keep an in-process fallback when the daemon
  cannot start.
