# A session state machine, and a forward lifecycle that follows it

A session is a podman pod (`hsh-<id>`) with a workspace container
(`hsh-<id>-ws`) and a proxy container. Its "state" — running, suspended,
shell exited, stopped, gone — is recomputed ad hoc wherever the code needs
it, from a handful of podman inspects and the `client.pid` file. The same
decision tree is spelled out in `sessionStatus` (for `hsh ls`), in
`containerRunning`/`containerPaused`/`containerStatus`/`podExists` (for
attach, resume, suspend, reaper, forward), and nowhere is it one thing.

`hsh forward` suffers from the absence of a single answer. A managed
forward (the monitor's "Forward: start a forward", a detached
`hsh forward --managed`) listens on a host port and relays over an SSH
connection carried by `podman exec … --ssh-stdio`. It only notices the
session is gone when that SSH connection *closes* (`client.Wait()`). When
the session is **suspended** (`hsh suspend` → `podman pod pause`), the exec'd
process is frozen, not closed: the connection stays open, the host port
keeps listening, and every connection to it hangs. When the shell exits or
the pod is stopped the connection usually does close, but nothing guarantees
the forward follows the session promptly. The user sees a listening port
that goes nowhere.

## Goal

1. One canonical session state, with named states and the transitions
   between them, used everywhere the code asks "what is this session doing?"
2. Make the lifecycle of every `hsh forward` process follow that state: a
   forward is alive only while the session is **reachable** (running). When
   the session suspends, its shell exits, its pod stops, or it is removed,
   the forward stops listening and exits, releasing its host port.

## The state machine

States (an `hsh` session, keyed by `<id>` / pod `hsh-<id>`):

| State         | Meaning                                                     |
|---------------|-------------------------------------------------------------|
| `Absent`      | no pod — the session does not exist (or was `hsh rm`'d)      |
| `Starting`    | pod/ws created but the workspace container is not yet running |
| `Attached`    | ws running, a live client recorded in `client.pid`          |
| `Detached`    | ws running, no client attached                              |
| `Suspended`   | pod paused (`hsh suspend`)                                   |
| `ShellExited` | ws exited while the pod (proxy) still runs                   |
| `Stopped`     | pod stopped, nothing running                                 |

Derived predicates:

- `Running()` = `Attached || Detached` (the workspace container is running).
- `Reachable()` = `Running()` — the only states where a server inside the
  session can be dialed, hence the only states in which a forward belongs.

Transitions (who drives them):

```
                 hsh run (create)
   Absent ───────────────────────────► Starting
      ▲                                    │ ws starts
      │ hsh rm                             ▼
      │                          Detached ◄──► Attached
      │                          │  ▲  │        │  ▲
      │          hsh suspend ────┘  │  │ attach │  │ detach / resume --take-over
      │                             │  │        │  │
      │              resume/unpause │  └────────┘  │
      │                             ▼              │
      │                         Suspended          │
      │                             ▲              │ shell exits
      │  hsh rm        hsh suspend  │              ▼
      └────────── Stopped ◄───── (reaper stops ShellExited
                     ▲             the pod)     │
                     └───────────────────────── ┘  reaper / hsh run idle cleanup
```

The transitions are not new behaviour — `hsh run`, `resume`, `suspend`,
`detach`, the reaper, and `rm` already cause them. The state machine only
names the states and gives one function that reports which one a session is
in.

## Implementation

### `internal/cli/sessionstate.go`

```go
type SessionState int

const (
    SessionAbsent SessionState = iota
    SessionStarting
    SessionAttached
    SessionDetached
    SessionSuspended
    SessionShellExited
    SessionStopped
)

func (s SessionState) Running() bool    // Attached || Detached
func (s SessionState) Reachable() bool   // == Running()
func (s SessionState) String() string    // display label
```

Two entry points onto the same enum:

- `sessionStateFromPod(p podInfo) SessionState` — for callers that already
  have a `podInfo` from `podman pod ls` (`hsh ls`, the monitor): reads the
  workspace container's status and `client.pid`. This is the logic currently
  inside `sessionStatus`.
- `resolveSessionState(id string) SessionState` — for single-session callers
  (the forward watcher): one `podman inspect {{.State.Status}}` of
  `hsh-<id>-ws` plus a pod-exists / `client.pid` check.

`sessionStatus(podInfo) string` is reimplemented as a thin wrapper over
`sessionStateFromPod`, preserving the exact strings `hsh ls` prints today
(`Attached`, `Detached`, `Suspended`, `Shell exited`, and the pod's own
status for the stopped/other cases) so `TestSessionStatus` is unchanged.

The scattered `containerRunning` / `containerPaused` / `containerStatus`
helpers stay (they are cheap single-fact probes), but the forward lifecycle
decision now goes through the state machine's `Reachable()`.

### Forward lifecycle

Add a watcher that cancels the forward's context as soon as the session
leaves `Reachable()`:

```go
func watchSessionReachable(ctx context.Context, cancel context.CancelFunc,
    sessionID string, onLeave func(SessionState))
```

It polls `resolveSessionState(sessionID)` (~1.5s). On the first non-reachable
state it calls `onLeave(state)` and `cancel()`, then returns. `forward.Run`
already returns cleanly when its context is cancelled and closes every
listener (`closeAll`), so the host port is released immediately.

Wire it into all three forward entry points:

1. **Foreground `hsh forward`** (`forwardCommand`): after `connectSession`,
   derive a cancellable context from the existing `signal.NotifyContext`,
   start the watcher, and on leave print
   `hsh: session <label> is <state>; stopped forwarding` before returning.
2. **Managed `hsh forward --managed`** (same handler): identical watcher; the
   existing `defer removeManagedForward(...)` then prunes the registry entry,
   so a suspended or exited session leaves no managed forward behind. The
   `onLeave` message goes to the forward's log.
3. **`hsh run --forward`** (`startRunForwards`): add the watcher to the
   background goroutine's context so an attached run's forwards also stop if
   the session stops out from under them (detach/suspend already cancel via
   `stopForwards`, so this is belt-and-braces).

The watcher is the single authority: it covers suspend (frozen SSH
connection), shell exit, pod stop, and removal with one mechanism, and it
needs no cooperation from `hsh suspend`/`rm`.

### Non-goals

- Restarting a managed forward when the session resumes. A forward that
  stopped because its session suspended or exited is gone; the user restarts
  it from the palette (or `hsh forward`) after `hsh resume`. The requirement
  is only that no host port listens while the session is not reachable.
- Changing `hsh ls` output or the monitor's session labels.

## Tests

- `sessionstate_test.go`: `sessionStateFromPod` over the matrix already in
  `TestSessionStatus` (no workspace container, running ± client, paused,
  exited-under-running-pod, fully stopped) plus `Running()`/`Reachable()`.
- Forward watcher: a fake `resolveSessionState` (injected like
  `forwardSessionRunning`) driven through Reachable→Suspended, asserting the
  context is cancelled and `onLeave` fires once.
- Existing `forwardmgmt_test.go` and `TestSessionStatus` keep passing.

## Docs

Update `internal-docs/architecture.md`'s session-lifecycle section to point
at the state machine as the source of truth for session state, and note that
forwards live only while a session is reachable.
