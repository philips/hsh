# Detach/attach with podman's own flow, and an in-process relay for inline approvals

> **Status: plan.** Builds on plans/0025-libghostty.md (emulator and
> passthrough decisions) and changes how `internal/cli/run.go` attaches to
> the workspace container. Relay prototype: `plans/prototype/libghostty/`.

## Goal

1. A session's shell and agent keep running when the user detaches or
   closes the terminal, and `hsh run --resume` re-attaches to the *same*
   running shell.
2. Proxy denials for the session surface inline: a status line and a
   Ctrl+Space review dialog drawn by a host process, outside the sandbox
   (plans/0024-process-approval.md option B).

## Where things stand

`hsh run` hands its terminal to `podman run -it` / `podman start -ai`.
When the workspace container exits, `hsh run` removes `secrets.json` and
stops the pod. "Resume" starts a *new* shell in the same pod; the old
shell and agent are gone. Closing the terminal kills the session (the
podman client proxies SIGHUP into the container). podman's default detach
keys, Ctrl-P Ctrl-Q, are also live: they detach the podman client, `hsh
run` sees it exit, and stops the pod.

## Decision: use podman's detach/attach, not a daemon

podman already keeps the container's pty (conmon holds the master) and has
`podman attach`, so a daemon, socket, and protocol in front of it are not
needed for goal 1. Measured with podman 5.8.9 and the hsh example image,
podman's own flow covers it:

| Behaviour | Result |
|---|---|
| `podman run -it --detach-keys=ctrl-]` then Ctrl-] | client exits 0, container keeps running |
| `podman attach` afterwards | same shell (a variable set before detaching was still set) |
| default Ctrl-P Ctrl-Q with `--detach-keys=ctrl-]` | no longer detaches |
| `--detach-keys=` (empty) | disables detaching entirely |
| terminal closed while attached, default `--sig-proxy` | container dies (SIGHUP proxied in) |
| terminal closed with `--sig-proxy=false` | container keeps running, acting as a detach |
| window size change after attach | reaches the container; the shell redraws its prompt |
| 2 MB of output written to the pty while detached | writer does not block (conmon drains and discards); nothing is delivered on attach |

So detaching needs no new process, socket, or protocol. What podman's flow
does **not** give, and what this plan accepts:

- No history replay: output produced while detached is gone, and there is
  no scrollback restore. The user's terminal keeps its own scrollback if
  they reattach from the same window.
- No screen repaint on attach: the shell prompt does not reappear until a
  redraw (Ctrl-L, or a window-size change, which Phase 2 does for the
  user).
- Nothing watches the container after `hsh run` exits (see the reaper).

A host daemon with an emulator would restore the screen and history. It is
the optional Phase 4 below, not the baseline.

## Phase 1: podman-native detach and attach

- Interactive `hsh run` and `--resume` pass `-it --sig-proxy=false
  --detach-keys=<key>` to the podman client. Default key Ctrl-] (config
  `detach_keys`, podman's syntax). `--rm` sessions pass `--detach-keys=`
  (empty): a temporary session must not outlive its `hsh run`, because
  nothing would remove it.
- When the podman client returns, inspect the workspace container. If it
  is still running the user detached: print `detached; hsh run --resume to
  return`, keep the pod, proxy, and `secrets.json`, and exit 0 without the
  stop-pod cleanup. If it exited, run today's cleanup and propagate the
  exit code.
- `hsh run --resume` on a running workspace container attaches with
  `podman attach` (same flags) instead of failing with "session is
  already running". A stopped container starts a fresh shell as today.
- **The reaper.** For every interactive session `hsh run` starts a tiny
  setsid'd watcher (`hsh-reaper`, the existing `argv[0]` role dispatch in
  `main.go`) when it creates the workspace container, not when the user
  detaches: closing the terminal sends SIGHUP to `hsh run` as well, so
  anything started at detach time would never exist. The reaper blocks in
  `podman wait <workspace>` and then runs the same idempotent cleanup
  (`rm secrets.json`, `pod stop --time 2`); `hsh run` still runs it itself
  when it sees the container exit, so an attached exit is immediate. The
  reaper has no socket and no protocol, and exits with the pod. Without it
  a shell that exits while detached leaves the proxy running with resolved
  secrets until the next `hsh` command. `hsh gc` also stops pods whose
  workspace container has exited, as a backstop if the reaper died.
- `hsh ls` shows attached versus detached (the container is running with
  no attached client) and "shell exited" for the leftover case. `hsh rm`
  stops the reaper by removing the pod.

Phase 1 is a small change to `run.go` and needs no new dependencies.

## Phase 2: in-process relay for the inline monitor

Detach does not need a relay; the inline dialog does, because something
host-side must see Ctrl+Space and be able to pause the output. `hsh run`
becomes the relay, in the foreground, with no daemon:

- `hsh run` allocates a pty pair, runs the podman client with the slave
  as its terminal (verified: podman behaves normally on a pty slave and
  propagates resizes; the client must be the slave's foreground process
  group, so `Setsid` + `Setctty`), and relays between the user's raw
  terminal and the master.
- The relay is the passthrough design of 0025 decision 6: the emulator is
  fed exactly the forwarded bytes; the review dialog and prompt bar pause
  the output by not reading the pty master and repaint the covered rows on
  close; overlays and the status row are written only at the emulator's
  ground state; the reserved status row is a scroll region on a pty one
  row short and is optional (`monitor.status_line`); Ctrl+Space is
  configurable.
- The dialog and approvals come from `denied.db` filtered by the session
  id and apply through `allowHosts` (the code behind `hsh proxy allow`),
  never from the byte stream; an in-band OSC from the agent can at most
  say "look at the queue" and is stripped.
- On attach the relay briefly changes the pty window size by one column
  and back, which makes the shell (and full-screen programs) repaint. That
  gives a usable screen after reattach without an emulator that outlives
  `hsh run`.
- Denials that arrive while detached accumulate in `denied.db` as today;
  the status row and the existing `hsh monitor` show them on the next
  attach.

Headless runs (`--tasks`) and non-terminal stdin skip the relay and keep
the direct path.

## Security

- No new listening surface: no socket, no daemon, no protocol. The relay
  runs in the user's own `hsh run` process on the host, outside the
  sandbox, and the reaper is a user process with no inputs.
- The relay parses untrusted pty output. The emulator is the wasm build so
  the parser runs in wasm linear memory, and the custom OSC is stripped
  before it can reach the user's terminal.
- A detached session keeps its pod, proxy, agent, and resolved
  `secrets.json` running, the same exposure as an attached one; the
  idle-scrub guarantee of plan 0019 applies once the shell exits (via the
  reaper or `gc`).
- Emulator contents live only in the `hsh run` process.

## Phase 3: tests

Extend `test/functional.sh` (its python pty driver already runs
interactive sessions, step 14):

- Detach with the detach key: workspace container still running, `hsh ls`
  shows detached, `secrets.json` still present.
- Set a variable, detach, `hsh run --resume`: same shell (variable set),
  prompt repainted.
- Close the terminal (master closed) while attached: container keeps
  running.
- Ctrl-P Ctrl-Q no longer detaches.
- `--rm` sessions cannot detach and are removed on exit.
- Shell exits while detached: the reaper stops the pod and removes
  `secrets.json`; kill the reaper first and `gc` does it.
- Exit code propagates from an attached shell; piped and `--tasks` runs
  start no reaper.
- Phase 2: a denial raises the status row; Ctrl+Space pauses a running
  `seq` (it stops advancing), `a` adds the host to the session config,
  closing resumes with no lost output; the pause/restore equivalence and
  region-rewrite unit tests carry over from the prototype.

## Phase 4 (optional): history and screen restore

Only if the resize-nudge repaint and lost detached output are not good
enough: a per-session host daemon that owns the pty and runs podman's
client against it, keeps the relay and emulator alive across detaches, and
replays history on attach. It would also absorb the reaper: one supervisor
owns the pty, the cleanup, and the denial watcher, and knows exactly who
is attached (which answers the `hsh ls` question below). The reaper is its
seed: it is already a per-session process that outlives the terminal.
The costs are the double pty layer (host pty, podman client, conmon's
container pty), a failure mode podman-native does not have (if the daemon
dies its podman client dies and the shell gets SIGHUP, whereas conmon keeps
the container's pty alive across `hsh` crashes and upgrades), and the
protocol and process lifecycle below. That reintroduces a
socket (it must live under `$XDG_RUNTIME_DIR/hsh/`, never under the state
or session directories, which are bind-mounted into the proxy container),
`SO_PEERCRED` checks, a protocol, and a process lifecycle. Decide after
using Phases 1 and 2.

Phase 2 depends on the Go 1.27 / `GOEXPERIMENT=simd` bump and the pinned
wasm build (0025 decisions 1 and 2); Phase 1 does not.

## Implementation order

1. **Phase 1 first or in parallel.** It has no dependency on 0025 (no
   emulator, no toolchain change) and on its own fixes the Ctrl-P Ctrl-Q
   session loss and the closed-terminal kill.
2. **0025's foundation**: the Go 1.27 / `GOEXPERIMENT=simd` bump, the
   pinned wasm build, and the `Write/Resize/Snapshot` emulator interface.
   0025 delivers these and the decisions; the relay and dialog it
   describes are built here.
3. **Phase 2**: the raw pty relay in `hsh run` (resize, detach key,
   exit codes) can land before the emulator exists; the emulator, status
   row, and dialog follow once step 2 is done.
4. Phase 4 only if wanted.

## Open questions

- Default detach key (Ctrl-] vs something less likely to clash) and
  whether `--detach-keys=` empty should be an option for users who want
  no detach.
- Whether the reaper should survive `hsh` upgrades (it re-execs the same
  binary path) or exit and be re-spawned by the next `hsh` command.
- Multiple clients: a second `podman attach` shares the pty; decide
  whether `--resume` refuses when another client is attached.
- Whether `hsh ls` can tell attached from detached cheaply (podman does
  not expose attached clients directly).
- Terminals other than Ghostty's core: partial-region scrollback
  behaviour must be checked before the status row defaults on.
