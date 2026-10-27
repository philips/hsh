# Process control for firewall approvals

> **Status: research, with a working prototype** in
> `plans/prototype/process-approval/` (`./test.sh` runs it). Nothing in
> `internal/` changes yet.

## Question

Can the egress proxy pop the user out of the agent harness and into an
admin prompt to approve a denied request, with the agent paused while
they decide? The idea (from TODO.md): the proxy signals the agent
(SIGTSTP, like Ctrl-Z), bash returns to its prompt, and the user runs an
approval command that talks to the proxy over a unix socket the agent
cannot open.

Today a denial is recorded in `denied.db` and reviewed out of band
(`hsh monitor`, `hsh proxy allow --watch`) in a second host terminal.

## Findings

### 1. The proxy cannot signal the agent directly

The proxy and the workspace are separate containers with separate PID
namespaces. Sharing one (`podman pod create --share pid`) would let the
agent see and signal the proxy, and read `/proc/<proxy>/` — same uid
inside the rootless user namespace — which exposes the injected secrets
and the MITM CA key. Rejected.

The signal has to be sent from inside the workspace container, by a
process the proxy can reach but the agent cannot. The workspace parent
(`/hsh-workspace`, outside the inner netns, caps already dropped) fits:
it supervises the inner process, shares the PID namespace with the shell,
and is reachable from the proxy over the pod's loopback. The inner netns
only routes to `10.77.0.1` (the proxy), so a parent listener on the pod
loopback is unreachable from the agent. No filesystem object is needed.

### 2. The pause itself works with plain job control

The broker reads `/proc/<shell>/stat`, takes the terminal's foreground
process group (`tpgid`), and sends `SIGTSTP` to `-tpgid`; bash reports
`Stopped` and redraws its prompt, and `fg` resumes. This needs no
cooperation from the harness beyond the SIGTSTP behaviour every TUI
already has for Ctrl-Z (restoring terminal modes). The broker falls back
to SIGSTOP if the group is not stopped after one second. SIGSTOP cannot
be ignored but leaves a raw-mode TUI's terminal state un-restored, so it
is a last resort.

If the shell is at its prompt (`tpgid == pgrp`) there is nothing to
pause; the denial stays in the queue.

### 3. Landlock can fence the approval socket off from the agent

Landlock ABI 9 adds `LANDLOCK_ACCESS_FS_RESOLVE_UNIX`: a domain that
handles it and grants no rule cannot `connect()` to any pathname unix
socket. `LANDLOCK_SCOPE_SIGNAL` and `SCOPE_ABSTRACT_UNIX_SOCKET` stop the
domain from signalling processes or reaching abstract sockets outside it.
`pa guard -- CMD` applies both plus NO_NEW_PRIVS and execs CMD; domains
are inherited, so the harness's own children (tool calls) stay inside.
The prototype verifies from an interactive bash that a guarded process
gets `Permission denied` on the admin socket and `Operation not
permitted` when it tries to kill the shell, while the unguarded shell
can approve. This host reports ABI 10. A kernel without ABI 9 has no way
to restrict pathname sockets, so the feature must refuse to start (fail
closed) rather than degrade.

### 4. The weak point is the shell, not the socket

The shell the user types the approval into is agent-influenced state: it
sources `$HOME/.bashrc` from the persistent session home, inherits `PATH`
and `PROMPT_COMMAND`, and the agent can write files the shell later
executes. An agent that plants an `approve` alias, a `DEBUG` trap, or an
earlier-in-`PATH` `hsh` wrapper turns the user's own unguarded approval
into a confused-deputy approval of everything pending. Absolute-path
invocation (`/hsh-workspace-inner approve ...`) defeats aliases and
functions, but not `trap DEBUG`, `PROMPT_COMMAND`, or a prompt that
lies about what is pending. The agent can also simply ask the user to
approve. An approval surface inside the sandbox can be made hard to
subvert but never as sound as one outside it.

## Options

| | Approval surface | Pause mechanism | Agent-subvertible | Cost |
|---|---|---|---|---|
| A. In-sandbox admin shell (TODO idea, prototyped) | the session bash, over a Landlock-fenced socket | SIGTSTP from the workspace parent | partially (finding 4) | supervisor listener, guarded launch, `approve` subcommand, kernel ABI 9 |
| B. Host-owned terminal | `hsh run` on the host draws the prompt over the session | none required; optionally `podman pause` | no | `hsh run` must interpose a pty relay instead of `podman start -ai`; resize, raw mode, and output interleaving to get right |
| C. Hold the request | existing host surfaces (`hsh monitor`, `proxy allow --watch`) | none: the proxy holds the denied request for N seconds and retries the policy check | no | proxy change only; the agent sees latency rather than failure |

## Recommendation

1. **Do C first.** It needs no process control and removes the main
   annoyance (the agent seeing a hard failure for something the user is
   about to approve). Hold a denied CONNECT/DNS lookup for a configurable
   window (default off, `network.approval_wait`), re-checking the policy
   against the session config, which `hsh proxy allow` already rewrites.
2. **Then B** as the interactive surface: `hsh run` notices a pending
   denial for its own session (it already knows the session id and
   `denied.db`), and presents the approval without any sandbox-side
   approval command. If B needs the agent quiesced, the workspace parent
   exposes a pause/resume the proxy never sees, using the same
   foreground-group SIGTSTP code the prototype proves.
3. **Keep A in reserve** for environments where the host terminal is not
   available (detached or remote sessions). If it ships, require the
   mitigations from finding 4: absolute-path approve command printed in
   the stop banner with the exact host list being approved, no sourcing
   of agent-writable rc files by the admin prompt, and the Landlock-ABI
   check above.

## Prototype

`plans/prototype/process-approval/` is a separate Go module (stdlib only,
not part of `./...`). `pa broker` stands in for the workspace supervisor
plus the proxy's denial hook, with separate proxy and admin sockets;
`pa guard` is the Landlock wrapper; `pa agent` is the workload.
`test.sh` drives an interactive bash on a pty and checks: the foreground
job runs, a denial stops it with SIGTSTP and returns the prompt, the
shell lists pending hosts, a guarded process cannot reach the admin
socket or signal the shell, `approve` + `fg` resumes the job to
completion, and the approved host is then allowed.

Checked by hand: the static `pa guard` binary runs inside a rootless
podman container with the default seccomp profile
(`podman run -v pa:/pa:ro localhost/hsh-example /pa guard -- sh -c ...`),
so the `landlock_*` syscalls are not blocked there.

Not covered by the prototype: the proxy-to-supervisor hop over pod
loopback, a real raw-mode TUI harness handling SIGTSTP, and a host
kernel that lacks ABI 9.

## Open questions

- Should denials queue while the shell sits at a prompt, or notify only
  while a job is in the foreground?
- Per-host vs per-session approval granularity in the stop banner.
