# 0034: `/hsh-sshd` for every session; drop the relay and `--ssh-stdio`

> **Status: plan.** Depends on the `/hsh-sshd` role from 0033 (branch
> `gvisor-prototype`).

## Goal

Serve `hsh forward`, `hsh run --forward`, `ssh hsh-<id>` (`hsh ssh proxy`),
and `hsh oauth callback` the same way in both runtimes: one `podman exec -i
<pod>-ws /hsh-sshd`, which runs in the session shell's network namespace and
dials the sandbox's loopback directly. Remove the second path that the
default runtime uses today:

- the in-sandbox relay (`10.77.0.2:7788`, the `DIAL`/`LIST`/`PORT` protocol)
  and the process the inner workspace starts for it
- `/hsh-workspace-inner --ssh-stdio`, `--ssh-session`, `--callback-relay`,
  and `--oauth-deliver`, with their `--pty`/`--term`/`--command` flags
- the host side choosing between the two by session runtime
  (`sessionSSHArgs`, the branch in `hsh oauth callback`)

This leaves one code path to test and reason about. It also removes a
listener the agent's namespace never needed (the relay accepted only
`10.77.0.1`, but it was a long-lived process inside the sandbox, and it is
why `LIST` filters out port 7788).

## Today

| | default runtime (crun) | gVisor |
|---|---|---|
| entry | `podman exec -i ws /hsh-workspace-inner --ssh-stdio` | `podman exec -i ws /hsh-sshd` |
| exec lands in | pod netns (outer) | the shell's netns (there is only one) |
| dial | `DIAL n` to the relay at `10.77.0.2:7788` in the inner netns, which dials `127.0.0.1`, `10.77.0.2`, `::1` | `127.0.0.1`, `::1` directly |
| list | `LIST` through the relay (`/proc/net/tcp{,6}` read in the inner netns) | `/proc/net/tcp{,6}` directly |
| shell | `--ssh-session` scans `/proc/*/ns/net` for the inner netns, `setns`, copies its env/cwd | `--session-shell` copies PID 1's env/cwd |
| oauth callback | `--oauth-deliver` → relay `PORT n` | `/hsh-sshd --deliver` |

## Design

`/hsh-sshd` enters the session's network namespace itself, then behaves
exactly as it does under gVisor.

1. **Find the session shell.** The session shell is the lowest PID whose
   network namespace differs from `/hsh-sshd`'s own (the existing
   `innerNetnsPID` scan). Under gVisor no process differs, so the shell is
   PID 1. The PID is resolved once, here.
2. **Enter its netns by re-exec.** `setns` changes only the calling thread,
   and a Go server dials from any thread. So `/hsh-sshd` locks its thread,
   `setns(CLONE_NEWNET)` into `/proc/<pid>/ns/net`, and `execve`s itself with
   an internal `--entered <pid>` flag. The new process starts entirely in the
   inner netns. This needs `CAP_SYS_ADMIN`, which `podman exec` processes
   have in the default runtime (the workspace container is created with
   `--cap-add NET_ADMIN --cap-add SYS_ADMIN`; the workspace entrypoint drops
   them only for its own process tree). Under gVisor this step is skipped.
3. **Restrict, then serve.** With `--entered` (or under gVisor), drop every
   capability but `CAP_DAC_OVERRIDE` and set `NO_NEW_PRIVS`, as today, then
   run the SSH server. Restricting after the exec is required: a root
   process regains its bounding set across `execve` unless `NO_NEW_PRIVS` is
   already set. Every shell, dial, and delivery afterwards inherits both the
   inner netns and the restrictions.
4. **Dial.** `127.0.0.1`, then `::1`, then any other address assigned to an
   interface in the current netns (in the default runtime that is
   `10.77.0.2` on `hsh1`, which keeps today's behavior for servers bound
   only to the veth address). Never an address that is not local: under
   gVisor a non-local address would leave through the netstack to the proxy.
   `internal/sshd` keeps refusing non-loopback `direct-tcpip` destinations.
5. **Shell.** `--session-shell --pid <pid>` copies the environment
   (`HSH_SHELL`, proxy variables, CA bundle, home) and working directory of
   the PID from step 1, makes fd 0 the controlling terminal for pty sessions,
   and execs the shell. It no longer scans `/proc`: after step 2 the scan
   would find the outer workspace process instead of the shell.
6. **OAuth callback.** `hsh oauth callback` always runs `podman exec ws
   /hsh-sshd --deliver PORT --request REQ`, through the same steps 1–4.
7. **Mount.** `hsh run` bind-mounts the binary at `/hsh-sshd` in every
   workspace container, not only gVisor ones.

## Removal

- `internal/workspace/relay.go`: `ServeRelay`, `relayConn`, `DialRelay`,
  `ListRelay`, `allowPodSide`, `sourceAllowed`, and the 7788 filter in
  `LIST`. `Splice` moves to its remaining user or goes away. `DialSandboxPort`
  is replaced by the local-address dial in step 4.
- `internal/workspace/callback.go`: `CallbackRelay`, `OAuthDeliver`,
  `startCallbackRelay`, and its call in `enterSession`. `HTTPEcho` stays (a
  test helper) and `DeliverLocal` stays.
- `internal/workspace/sshsession.go`: `RunSSHSession` (the scan-and-setns
  shell); `runSessionShell` keeps only the copy-env-and-exec part.
- `internal/netsetup`: `CallbackRelayPort`, `CallbackRelayAddr`.
- `internal/cli/workspace.go`: the `--callback-relay`, `--oauth-deliver`,
  `--ssh-stdio`, `--ssh-session`, `--pty`, `--term`, and `--command` flags.
  `--http-echo` stays.
- `internal/cli/ssh.go`: `serveSSHStdio`, `spawnSSHSession`, and
  `sessionSSHArgs` (both callers use `/hsh-sshd` unconditionally).
- Tests: `relay_test.go` and the relay cases in `callback_test.go` go;
  dial-local and `--session-shell --pid` get unit tests.

## Sessions created before this change

Their workspace containers have no `/hsh-sshd` mount, and a container's
mounts cannot be changed after creation. Their mounted binary is also the
old hsh, which does not know the `hsh-sshd` role. After removal they cannot
be served, so the host commands detect this explicitly. `podman container
inspect` shows no mount at `/hsh-sshd`, and the commands fail with: `session
<id> was created by an older hsh and does not support forwarding/ssh;
recreate it with: hsh run --resume --fresh <id>` (or `hsh rm`). Everything
else about the session keeps working.

hsh is pre-1.0 and sessions are cheap to recreate, so this does it in one
release. If that is too abrupt: ship step 7 (mount `/hsh-sshd` everywhere,
prefer it when mounted, fall back to the old path otherwise) one release
before the removal, so sessions created in between already work.

## Tests

- `test/functional.sh` steps 16 (oauth callback) and 23 (ssh, `-L`, large and
  concurrent forwards, refusal of non-loopback destinations) and the forward
  steps must pass unchanged. They exercise the CLI, not the transport.
- New functional checks:
  - in the default runtime, nothing listens on 7788 in the inner netns
  - `/hsh-workspace-inner --ssh-stdio` is an unknown flag
  - an `ssh hsh-<id>` shell has `NoNewPrivs: 1` and `CapEff` limited to
    `DAC_OVERRIDE`
  - a server bound only to `10.77.0.2` is reachable through `hsh forward`
  - a pre-change session (a workspace container without the `/hsh-sshd`
    mount) gets the recreate message
- `test/gvisor.sh` unchanged.

## Docs

Rewrite "Reaching servers inside the session" in `docs/architecture.md`
around `/hsh-sshd` (the diagram, the relay bullet, and the shell path).
Move the `/hsh-sshd` row in the roles table from "gVisor sessions only" to
both runtimes. Collapse the corresponding section of `docs/gvisor.md` to
the one gVisor-specific point (step 2 is skipped). Plan 0031 remains as
history.
