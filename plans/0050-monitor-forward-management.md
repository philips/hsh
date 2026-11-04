# Managed forwards from the monitor palette

The agent starts a webserver inside the sandbox on port 8000. Today the
user must dedicate a terminal to `hsh forward 8000` to open it
locally. Add palette commands so a user in the monitor (`hsh monitor` or
ctrl-space) can start and stop forwards that outlive the TUI — start
one, browse `http://localhost:8000`, stop it when done.

## Behavior

- Palette gains two commands (next to the Proxy/Session/Settings ones):
  - `Forward: start a forward` — pick a session, see its listening
    ports, pick one, get a managed `hsh forward` running detached.
  - `Forward: stop a forward` — pick an active managed forward, stop it.
- Start flow: session picker (skip it when the monitor was opened with
  a `--session` filter or from inside a session, like the allow
  command), then a picker of the session's listening ports (the
  `hsh-list` result, fetched in a tea.Cmd since the podman exec + SSH
  dial takes a moment — status line "listing ports…" until the message
  arrives). Enter starts the forward; the local port equals the
  remote port. A local port already in use is an error status naming
  the port and pointing at `hsh forward <other>:<port>` for a custom
  local port.
- Stop flow: picker over active managed forwards, `session  port ->
  remote  started <age>`. Enter sends SIGTERM, waits, and removes the
  entry; the status line names what stopped. Esc goes back.
- The command items' notes show `n active`.
- Managed forwards bind `127.0.0.1` by default (the `--bind` escape hatch
  stays on the `hsh forward` CLI).
- Forward start fails with the usual "no running workspace container"
  error when the session is down; the status line shows it.

## Managed forward lifecycle

- Registry: one directory per forward under
  `state.Dir()/forwards/<id>/` with `meta.json`
  (id, session, bind, local, remote, pid, started RFC3339) and the
  child's `log`. Directory create with `MkdirAll` (last-write-wins for
  a restart of the same id). ID is `<session>-<local>`, so one managed
  forward per session+local port; starting the same one again is a
  no-op reported as "already forwarding".
- Start (host side, injected into the palette as `ForwardOps.Start`):
  1. check the local port is free (reuse `forward.CheckLocal`);
  2. re-exec `os.Executable()` as
     `hsh forward --managed --session <id> 127.0.0.1 <local>:<remote>`
     with `Setsid`, stdout/stderr to the log file, `Process.Release()`
     (same detached-child pattern as `spawnUpdateCheck`);
  3. write `meta.json`.
- The `--managed` child resolves the session, connects, and runs
  `forward.Run` with the same signal.NotifyContext handling as the
  foreground path, then removes its registry directory on any exit
  (workspace container stopped, SIGTERM, error). The registry therefore
  needs no lock: entries are directories, and a dead child's exit
  handler (or the prune below) is the only remover.
- Prune: every registry read (list, stop, start pre-check) drops
  entries whose pid is dead (`syscall.Kill(pid, 0)` ESRCH) — covers a
  killed child that could not clean up.
- `hsh run --forward` attachments and managed forwards collide only on
  the local port, which the pre-check catches with the error above.
- `hsh wipe` already removes the state dir; `wipe` refuses while pods
  exist, so managed forwards are normally gone (or their sessions are)
  before a wipe. Document the registry in architecture.md's monitor
  section; remove the TODO entry when done.

## Implementation

- `internal/cli/forwardmgmt.go`:
  - `type ForwardOps` (matching `palette.SessionOps`'s injected-func
    style): `List() ([]Managed, error)`, `Start(session string, remote
    int) (Managed, error)`, `Stop(id string) (string, error)`,
    `Listening(session string) ([]sshd.Listener, error)` (wraps
    `connectSession` + `forward.ListPorts`).
  - Registry helpers (read/prune/start/stop) against a configurable
    base dir (`state.Dir()` default) for testability; the spawn
    function is a package var so tests can stub the child.
- `internal/cli/forward.go`: `--managed` flag on `hsh forward`
  (unexported use, like update's `--autoupdate-check`): the child
  removes its registry dir on exit; everything else reuses the
  foreground path.
- `internal/tui/palette`:
  - `Options.Forwards *ForwardOps` (nil keeps the commands hidden);
    wired in `monitor.go` and `inline.go` next to
    `monitorSessionOps()`.
  - New views `viewPortPick` (listening ports) and `viewForwardPick`
    (active forwards), reusing the picker and the session-pick
    pattern (`pickFor` gains a kind); the async port list arrives as a
    tea.Msg from a `ForwardOps.Listening` cmd.
  - Command items in `refreshCommands` with the active count note.
- `internal/cli/monitor.go` / `inline.go`: wire
  `monitorForwardOps()`.

## Tests

- `internal/cli/forwardmgmt_test.go`: registry against a temp dir —
  start records meta (stubbed spawn), duplicate id is a no-op error,
  busy local port error, prune drops dead pids, Stop kills (fake child
  via a stubbed stop func or a real `sleep` process) and removes the
  dir, JSON round-trip.
- `internal/tui/palette/model_test.go`: forward commands appear with
  ops wired and hidden when nil; start flow reaches the port picker
  after the listening msg; stop flow stops the picked id via fake ops;
  esc behavior.
- `hsh forward --managed` removal on exit: covered by exercising the
  registry helper in the child's code path (the full podman round
  trip is not unit-testable).
- Manual e2e: `hsh run` a session, start a server inside, ctrl-space →
  start forward, `curl localhost:<port>`, stop, confirm the local port
  is released.
