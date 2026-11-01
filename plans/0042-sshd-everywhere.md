# 0042: `/hsh-sshd` serves forwarding in both runtimes; the relay is removed

> **Status: implemented.** Supersedes
> plans/0034-sshd-everywhere.md. That plan was written before gVisor became the
> default runtime and before `hsh resume` replaced `hsh run --resume --fresh`.

## Goal

In both runtimes, `hsh forward`, `hsh run --forward` / `hsh resume
--forward`, `ssh hsh-<id>` (`hsh ssh proxy`), and `hsh oauth callback` all run
the same command:

```
podman exec -i hsh-<id>-ws /hsh-sshd [--deliver PORT --request REQ]
```

Today the `container` runtime uses a second path. It consists of the relay
on `10.77.0.2:7788` in the inner netns, plus `/hsh-workspace-inner`
`--ssh-stdio`, `--ssh-session`, `--callback-relay`, and `--oauth-deliver`.
This plan removes that path. One transport is left to test, the inner netns
loses a long-lived listener that it never needed, and the SSH server no longer
runs with `CAP_SYS_ADMIN`/`CAP_NET_ADMIN` in the pod netns while it parses
data from the sandbox.

`/hsh-sshd` never searches for the session's network namespace. The
workspace pins the namespace at setup where the agent cannot change it.
`/hsh-sshd` verifies the pin and aborts if anything does not match.

## Current code

| piece | where | runtime |
|---|---|---|
| choose the exec argv by session runtime | `sessionSSHArgs` in `internal/cli/ssh.go`; the `readSessionRuntime` branch in `internal/cli/oauth.go` | both |
| `/hsh-sshd` mount | `internal/cli/run.go`, only in the `gv != nil` branch | gvisor |
| `/hsh-sshd` entry | `cli.SSHD` in `internal/cli/sshd.go`: `RestrictPrivileges`, then `sshd.Serve` with `DialLoopbackPort`, `InnerListeners`, and `--session-shell` | gvisor |
| relay SSH server | `serveSSHStdio` and `spawnSSHSession` in `internal/cli/ssh.go` (`DialRelay`, `ListRelay`, `--ssh-session`). Never restricts its privileges. | container |
| relay | `internal/workspace/relay.go` (`ServeRelay`, `relayConn`, `DialRelay`, `ListRelay`, `allowPodSide`, `sourceAllowed`, `DialSandboxPort`, `Splice`) | container |
| relay start | `startCallbackRelay` in `internal/workspace/callback.go`, called from `RunInner` when `opts.ProxyAddr != ""` | container |
| relay-based OAuth delivery | `CallbackRelay` and `OAuthDeliver` in `internal/workspace/callback.go` | container |
| shell via `/proc` scan + setns | `RunSSHSession`, `innerNetnsPID`, and `runSessionShell` in `internal/workspace/sshsession.go` | container |
| relay constants | `CallbackRelayPort`, `CallbackRelayAddr` in `internal/netsetup` | container |
| flags | `--callback-relay`, `--oauth-deliver`, `--ssh-stdio`, `--ssh-session`, `--pty`, `--term`, and `--command` on `WorkspaceInner` | container |

## Design

### 1. Mount `/hsh-sshd` in every workspace container

In `internal/cli/run.go`, move `-v exe:/hsh-sshd:ro` out of the `gv != nil`
branch and into the common `wsArgs`. The container runtime keeps
`--cap-add NET_ADMIN --cap-add SYS_ADMIN`. `podman exec` processes get the
container's capability set, because the entrypoint drops capabilities only in
its own process tree. `/hsh-sshd` therefore starts with `CAP_SYS_ADMIN` and
can call `setns`.

### 2. Pin the session at setup (container runtime)

`RunParent` knows the inner child's PID (`cmd.Process.Pid`). The inner
process execs the shell in place, so that PID is the session shell's for the
whole session. After `MoveInnerVethToPID` and before `dropCaps()`, while it
still holds `CAP_SYS_ADMIN`, the parent runs `netsetup.PinSession(pid)`:

1. `mkdir -p /run/hsh`, then mount a tmpfs on it (`nosuid,nodev,noexec`,
   `mode=0755`, small size).
2. Write the PID to `/run/hsh/shell.pid`.
3. Create the empty file `/run/hsh/netns` and bind-mount
   `/proc/<pid>/ns/net` onto it, the way `ip netns add` does.
4. Remount `/run/hsh` read-only.

The agent has no `CAP_SYS_ADMIN` in the container's user namespace. It cannot
unmount, remount, or mount over `/run/hsh`, and the read-only mount stops it
from writing there even with `CAP_DAC_OVERRIDE`. A namespace the agent
creates with `unshare -Urm` is a private copy, while `podman exec` joins the
container's original mount namespace. Each container start creates a new
mount namespace, so `hsh resume` pins again.

Under gVisor there is no second netns. `/hsh-workspace --netstack` execs the
shell in place as PID 1, so nothing needs to be pinned.

### 3. Verify, enter, restrict, re-exec (`cli.SSHD`)

`setns`, `capset`, and `PR_SET_NO_NEW_PRIVS` each act on one thread, and a
Go server dials and forks from any thread. So `/hsh-sshd` does all of its
privileged work on one locked thread and then execs itself from that
thread, exactly once. The new image inherits that thread's namespace and
credentials on every thread.

1. **Mode.** If the current netns has an `hsh0` link, this is the pod side of
   the container runtime, and the pin is required. Otherwise this is gVisor:
   the target PID is 1, so skip to step 4. The agent cannot create or remove
   `hsh0` in this netns in either runtime. There is never a fallback to
   serving from the pod netns. There, the interface-address dial (step 5)
   would reach the proxy's `10.77.0.1` listeners.
2. **Wait for the pin.** Poll for up to 10 s until `/run/hsh` is a read-only
   tmpfs (the remount is the last setup step), because `hsh run --forward`
   connects as soon as the container is running. Then fail with `the
   session network namespace is not pinned`.
3. **Verify the pin.** Each check below aborts with its own error.
   - `/run/hsh/netns` is on nsfs (`fstatfs` gives `NSFS_MAGIC`) and is a
     network namespace (`ioctl(NS_GET_NSTYPE) == CLONE_NEWNET`).
   - Its inode differs from `/proc/self/ns/net`.
   - The PID in `/run/hsh/shell.pid` exists, and `/proc/<pid>/ns/net` has
     the pinned inode. If it does not, the shell exited and the PID was
     reused, so `/hsh-sshd` reports that the session shell is not running.
4. **Enter.** `runtime.LockOSThread()`, then `setns(pinFd, CLONE_NEWNET)`.
5. **Snapshot.** Still privileged, read `/proc/<PID>/environ` and
   `/proc/<PID>/cwd`, and write them as JSON to a sealed memfd. This has to
   happen before the restriction. In nesting sessions the shell keeps
   `CAP_SYS_ADMIN` and other capabilities. A restricted `/hsh-sshd` holding
   only `CAP_DAC_OVERRIDE` would fail the kernel's ptrace-access check on
   those files. A memfd keeps the shell's environment (which the agent can
   rewrite in its own memory) out of the server's own environment.
6. **Restrict this thread.** Drop every capability except
   `CAP_DAC_OVERRIDE`, then set `NO_NEW_PRIVS`.
7. **Re-exec.** `execve("/proc/self/exe", ["/hsh-sshd", "--serve", FD,
   args...])` from the locked thread. `argv[0]` stays `/hsh-sshd` so that
   `main.go` dispatches to the same role. stdin and stdout (the SSH
   transport) carry over. With `NO_NEW_PRIVS` already set and an empty
   permitted set apart from `CAP_DAC_OVERRIDE`, a uid-0 `execve` cannot regain
   the bounding set.
8. **Serve.** With `--serve`: refuse to run unless `NO_NEW_PRIVS` is set and
   the permitted and inheritable sets hold nothing beyond
   `CAP_DAC_OVERRIDE`. Set `PR_SET_DUMPABLE=0` (`execve` resets it), read the
   snapshot from `FD`, then deliver or run the SSH server on stdin/stdout. No
   SSH bytes are read before this step.

An agent that runs `/hsh-sshd --serve` itself is already restricted, so the
check passes, and it gets a server in its own namespace with a snapshot it
supplied. It gains nothing.

### 4. Why non-dumpable

After the restriction, `/hsh-sshd` and the agent are both uid 0 with only
`CAP_DAC_OVERRIDE`. The kernel's ptrace check would then let the agent attach
to `/hsh-sshd` (the `/hsh-sshd` capabilities are a subset of the agent's;
Yama `ptrace_scope=0` is the Fedora default). An attached agent controls the
server side of the SSH connection to the host's `hsh forward` or OpenSSH
client. It could fake listings, which make auto-forward open loopback
listeners on the host, or ask for agent-forwarding channels. A non-dumpable
process can only be ptraced with `CAP_SYS_PTRACE`. Today's container-runtime
server is protected by holding capabilities the agent lacks. Before this
change gVisor's `/hsh-sshd` was exposed in this way. Session shells exec a new image, so they
are dumpable again, as intended: they are equivalent to the agent.

### 5. Dial and list

Replace `DialLoopbackPort` and `DialSandboxPort` with
`workspace.DialLocalPort(port)`. It tries `127.0.0.1`, then `::1`, then every
other non-link-local address on an interface in the current netns (from
`net.InterfaceAddrs()`). In the container runtime that list includes
`10.77.0.2`, so servers bound only to the veth address stay reachable. A
local address never leaves the netns. Under gVisor it never goes through the
netstack to the proxy. The agent cannot add addresses, because it has no
`NET_ADMIN`. `internal/sshd` keeps refusing non-loopback `direct-tcpip`
destinations. `DeliverLocal` uses `DialLocalPort`.

Listing stays `InnerListeners()` (`/proc/net/tcp{,6}` of the current netns).
The 7788 filter goes away along with the relay.

### 6. Session shells

`spawnSession` execs the shell directly. Today it re-execs `/hsh-sshd
--session-shell`. Now it uses `HSH_SHELL` from the snapshot (or `/bin/sh`),
`-c COMMAND` when one is given, the snapshot environment with `TERM` from the
request, `Dir` from the snapshot cwd, and `SysProcAttr{Setsid: true, Setctty:
pty, Ctty: 0}`. The children inherit the netns, the dropped capabilities, and
`NO_NEW_PRIVS`. This removes `--session-shell`, `--pty`, `--term`, and
`--command` from `/hsh-sshd`, together with `RunDirectSSHSession` and
`runSessionShell`.

### 7. Host side

In `internal/cli/ssh.go`, replace `sessionSSHArgs` with
`sessionSSHArgs(sessionID string) ([]string, error)`, backed by `checkSSHDMount(sessionID)`:

- Check the container mounts with `podman container inspect --format
  '{{range .Mounts}}{{.Destination}}{{"\n"}}{{end}}' hsh-<id>-ws`. If
  `/hsh-sshd` is missing, return the recreate error described below.
- Return `exec -i hsh-<id>-ws /hsh-sshd extra...`.

`connectSession` (forward, and `--forward` on run/resume) and `hsh ssh proxy`
call `sessionSSHArgs`. `hsh oauth callback` calls `checkSSHDMount`, then runs
`podman exec ws /hsh-sshd --deliver PORT --request REQ`.
`readSessionRuntime` stays for `run`/`resume` but is no longer used here.

Also:
- `hsh ssh config` adds `ForwardAgent no` and `ForwardX11 no` to the stanza.
- `hsh oauth callback` strips control characters from the status line before
  printing it. The line comes from a server inside the sandbox.

### 8. Version skew

Each container start re-resolves the bind mount, so `/hsh-sshd` is the binary
that was on disk when the session last started. A session that stays up
through `hsh update` keeps its older binary. Security fixes in `/hsh-sshd`
take effect when the session restarts. The interface between the host and
`/hsh-sshd` must stay backward compatible: the stdio SSH server, the list
request, and `--deliver`/`--request`. `/hsh-sshd` is the only thing that
passes `--serve` to itself.

## Security notes

- **Nesting sessions** (`HSH_NESTING=1`): the agent keeps `CAP_SYS_ADMIN` in
  the container's user namespace. It can therefore unmount `/run/hsh` and
  pin a namespace of its choosing. In this mode the pin is advisory. The
  checks still reject the pod netns (the inode differs from our own) and a
  pin that does not match `shell.pid`. An agent with these capabilities can
  already `setns` itself, so this does not weaken the mode further.
- **The agent can run `/hsh-sshd` itself.** It gains nothing. It has no
  `CAP_SYS_ADMIN` to `setns`, it is already restricted, and the same binary
  is already mounted as `/hsh-workspace-inner`.
- **Removing the relay** removes the one in-sandbox dialer that the proxy
  container could reach: the relay accepted connections from `10.77.0.1`.

## Sessions created before this change

- **gvisor sessions** already mount `/hsh-sshd`, and the old binary serves the
  same protocol. They keep working.
- **container sessions** have no `/hsh-sshd` mount, and a container's mounts
  cannot change after creation. `checkSSHDMount` fails with:

  ```
  session <id> was created by an older hsh and cannot serve hsh forward, ssh, or oauth callback;
  remove it with: hsh rm <id>   then start a new one with: hsh run
  ```

  `hsh resume`, `hsh ls`, `hsh monitor`, and everything else keep working
  for these sessions.

hsh is pre-1.0 and sessions are cheap to recreate, so this ships in one
release with no transition period.

## Removal

- `internal/workspace/relay.go`: delete the file. Move `parsePort` and
  `DialLocalPort`/`dialPort` to `internal/workspace/dial.go`. `Splice` has no
  other user and is deleted.
- `internal/workspace/callback.go`: delete `CallbackRelay`, `OAuthDeliver`,
  and `startCallbackRelay`. Keep `DeliverLocal` and `HTTPEcho`.
- `internal/workspace/workspace.go`: delete the `startCallbackRelay()` call in
  `RunInner`.
- `internal/workspace/sshsession.go`: delete `RunSSHSession`,
  `RunDirectSSHSession`, `runSessionShell`, and `innerNetnsPID`. Keep the
  environ helpers for the snapshot.
- `internal/netsetup`: delete `CallbackRelayPort` and `CallbackRelayAddr`.
- `internal/cli/workspace.go`: on `WorkspaceInner`, delete `--callback-relay`,
  `--oauth-deliver`, `--ssh-stdio`, `--ssh-session`, `--pty`, `--term`, and
  `--command`. Keep `--http-echo`, which the tests use.
- `internal/cli/sshd.go`: delete `--session-shell`, `--pty`, `--term`, and
  `--command`.
- `internal/cli/ssh.go`: delete `serveSSHStdio`, `spawnSSHSession`, and
  `sessionSSHArgs`.
- `internal/cli/oauth.go`: delete the runtime branch and the `netsetup`
  import.

## Tests

### Unit

- Move `TestParseListeners` and `TestParseListenersMalformed` from
  `relay_test.go` to `listeners_test.go`. Delete `relay_test.go` and the
  relay cases in `callback_test.go`.
- `callback_test.go`: `DeliverLocal` delivers to a local listener, rejects an
  invalid port, and returns `ERR` when nothing listens.
- `dial_test.go`: `DialLocalPort` reaches servers bound to `127.0.0.1`,
  `::1`, and a non-loopback interface address. It fails on a closed port.
- Pin verification takes the pin directory as a parameter. The tests cover
  a pin that is not ready, a regular file, a namespace of the wrong type, our
  own netns, and a missing, garbage, or mismatched `shell.pid`. A netns from
  `unshare -Urn` serves as the valid pin, and the test skips when `unshare`
  is unavailable.
- The snapshot: a sealed memfd round trip, rejection of incomplete
  snapshots, reading a child's environment and cwd, and `CheckRestricted`
  rejecting an unrestricted process.
- `internal/cli`: parse the mount list from inspect output. Return the
  recreate error when `/hsh-sshd` is missing. Check the `hsh ssh config`
  stanza for the new options.

### Functional (`test/functional.sh`, both runtimes)

Steps 16 (oauth callback), 23 (ssh, `-L`, large and concurrent forwards,
non-loopback refusal), and 25 (forward, `--forward`) should pass unchanged.
Additions:

- Step 23, both runtimes: the `ssh hsh-<id>` shell reports `NoNewPrivs: 1`
  and `CapEff: 0000000000000002` (`DAC_OVERRIDE` only). While a forward is
  open, `/proc/<sshd pid>/status` inside the sandbox does not show the
  dumpable flag (check with `grep -l hsh-sshd /proc/*/cmdline` and `cat
  /proc/<pid>/environ`, which fails).
- Step 23, `only_runtime container`:
  - `podman exec hsh-<id>-ws /hsh-workspace-inner --ssh-stdio` fails with
    `flag provided but not defined`.
  - `ssh hsh-<id> cat /proc/net/tcp` shows no listener on port `1E6C`
    (7788).
  - A `node server.js` bound only to `10.77.0.2` is reachable through `hsh
    forward`.
  - Inside the session, `umount /run/hsh/netns`, `umount /run/hsh`, and
    `touch /run/hsh/x` all fail.
- Step 25: replace the `^7788` check with a check that `--list` shows only
  the test's servers.
- New step, legacy session: run `podman pod create --name hsh-legacy$$`,
  then `podman run -d --pod hsh-legacy$$ --name hsh-legacy$$-ws
  localhost/hsh-example:latest sleep 300`. `hsh forward --session legacy$$
  --list`, `hsh ssh proxy`, and `hsh oauth callback --session` each fail with
  the recreate message. Remove the pod afterwards.

### `test/gvisor.sh`

No changes. Step 4 already covers `/hsh-sshd` under gVisor.

### Nesting

Run the nesting tests in the container runtime, and check that `ssh
hsh-<id>` inside a nesting session still gets the shell's environment. This
exercises the privileged snapshot in design step 3.5.

## Docs

- `internal-docs/architecture.md`:
  - In the roles table, change the `/hsh-sshd` row to "workspace container,
    both runtimes".
  - Add `/run/hsh` (tmpfs, read-only after setup) to the network isolation
    steps.
  - Rewrite "Reaching servers inside the session" around `/hsh-sshd`:
    - the diagram (host → `podman exec` → `/hsh-sshd` → verify pin → setns +
      re-exec → dial/list/shell/deliver)
    - the pin and its checks
    - the dial order
    - restrict-after-exec and non-dumpable

    Delete the relay bullet and the `--ssh-session` shell path.
- `internal-docs/gvisor.md` "Reaching servers inside the session": cut this
  down to the gVisor-specific point (no second netns, so no pin, no setns,
  and the shell is PID 1) and link to architecture.md.
- `internal-docs/sandboxed-oauth.md`: replace the `callback relay
  (10.77.0.2:7788)` hops (the sequence diagram near line 102 and the
  paragraph near line 287) with `/hsh-sshd --deliver`.
- `internal-docs/testing.md`: replace the description of the relay unit
  tests, and update the step 25 note about the relay's port.
- TODO.md: remove the item once this lands.

## Findings

Results from manual runs and the test suites with rootless podman 5.8 on
kernel 7.2:

- **Pin.** In the container's user namespace the parent can mount the tmpfs,
  bind the nsfs, and remount read-only. Without capabilities the agent gets
  `must be superuser` from `umount`, and `Read-only file system` when it
  writes `shell.pid` or creates files. `podman exec` sees the pins and can
  `setns` into them. After `hsh resume`, a new pin is created and
  `--forward` connects through the pin wait.
- **`/run`.** Podman always creates `/run` in the container (for
  `.containerenv` and `secrets`), even when the image has none.
  `PinSession` also runs `mkdir -p`.
- **Restriction is per-thread.** A first version called plain `capset` and
  `prctl` in a running Go server. The `/hsh-sshd` main thread showed
  `CapEff: 2` and `NoNewPrivs: 1`, but `ssh hsh-<id>` shells, forked from
  other threads, got the full exec capability set with `NoNewPrivs: 0`.
  `syscall.AllThreadsSyscall` fixed that but returns `ENOTSUP` in cgo builds
  (a plain `go build .`). The final design restricts one locked thread and
  execs from it. The result: every shell has `CapEff: 0000000000000002` and
  `NoNewPrivs: 1` in both runtimes and in a cgo build. Before this change,
  the gVisor `/hsh-sshd` restricted only one thread of its server. Its
  shells were still restricted because they went through a
  `--session-shell` re-exec.
- **Non-dumpable.** From an ssh shell, `/proc/<sshd>/environ` and
  `/proc/<sshd>/fd` return permission denied.
- **`--serve` misuse.** `podman exec ws /hsh-sshd --serve 0` (unrestricted)
  fails with `refusing to serve: not restricted`.
- **Direct spawn.** `Setctty` works under runc and gVisor (`ssh -tt` gets
  `/dev/pts/0`).
- **Dial.** A server bound only to `10.77.0.2` is reachable through `hsh
  forward`. `hsh oauth callback` delivers in both runtimes. A fake legacy
  pod gets the recreate message from `hsh forward` and from `hsh oauth
  callback`.
