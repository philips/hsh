# Reaching servers inside a session (`hsh forward`)

> **Status: implemented** (`hsh forward`, `hsh forward --list`, auto mode,
> `hsh run --forward`; documented in docs/architecture.md, "Reaching servers
> inside the session"). The design changed from exec-per-connection to a
> host-side SSH client over the stdio SSH server of plans/0031-ssh.md.

## Goal

An agent builds a prototype and starts a server on `localhost:8000` inside
the sandbox. The user opens it from their own machine:

```
laptop$ ssh -L 8000:localhost:8000 server      # only if hsh runs on a remote host
server$ hsh forward 8000                       # leaves the shell/agent running
laptop$ open http://localhost:8000
```

Requirements:

- works for a session that is already running, with a port nobody knew about
  when `hsh run` started (the agent picks the port)
- works whether the session is attached, detached, or idle-but-running
- inbound only: it never gives the sandbox a new way out, and the egress
  policy is untouched
- no root, no changes to the pod, no republishing of ports

## Why the sandbox is unreachable today

The shell runs in the **inner** netns (`10.77.0.2/24` on `hsh1` plus its own
`lo`). A server bound to `127.0.0.1:8000` lives on that private loopback.
Nothing outside the inner netns can reach it:

- the host cannot, and neither can the pod netns (proxy container, `podman
  exec` shells), because the loopback is per-netns
- `podman pod create -p` only publishes into the *pod* netns, one hop short,
  and cannot be added to an existing pod

Plan 0022 already built the missing last hop: a relay inside the inner netns
(`/hsh-workspace-inner --callback-relay`, listening on `10.77.0.2:7788`,
started by the workspace parent). It is reachable from the pod netns, i.e.
from `podman exec <pod>-ws`, and dials `127.0.0.1:<port>` inside the inner
netns. `hsh oauth callback` uses it today for one HTTP request. This plan
generalizes it to long-lived, bidirectional TCP and puts a listener on the
host side.

## Design

`hsh forward` is a host-side SSH client over the session's stdio SSH server
(plan 0031). One `podman exec -i <pod>-ws /hsh-workspace-inner --ssh-stdio`
carries every connection as an SSH channel, so there is no per-connection
exec, no framing protocol of our own, and no port is published anywhere.

```
browser ──► 127.0.0.1:8000 (hsh forward, host netns)
              └─ direct-tcpip "localhost:8000" over one ssh connection
                   └─ podman exec -i <pod>-ws /hsh-workspace-inner --ssh-stdio
                        └─ TCP 10.77.0.2:7788  "DIAL 8000"   (veth)
                             └─ relay in inner netns ──► 127.0.0.1:8000 (the server)
```

### 1. Relay protocol (`internal/workspace/relay.go`)

`DIAL <port>` (ack `OK` or `ERR <reason>`, then a raw stream), `LIST` (the
inner netns's listening TCP ports from `/proc/net/tcp{,6}`, minus the relay's
own), the source check (`10.77.0.1` only), and correct half-close. `PORT <n>`
stays for `hsh oauth callback`. Implemented under plan 0031.

### 2. The SSH server

`internal/sshd` accepts `direct-tcpip` to the sandbox's loopback only and a
`hsh-list@ifup.org` global request that returns the `LIST` result. Implemented
under plan 0031.

### 3. The host command (`internal/cli/forward.go`, `internal/forward`)

```
hsh forward [--session ID] [--bind ADDR] [PORT | LOCAL:PORT]...
hsh forward --list [--session ID]
```

- Session resolution is `resolveSession` (workspace-directory label,
  `--session` to pick one).
- For each spec, listen on `ADDR:LOCAL` (default `127.0.0.1`; `LOCAL` defaults
  to `PORT`). A taken local port fails up front, naming the port and
  suggesting `LOCAL:PORT`.
- Each accepted connection opens a `direct-tcpip` channel to
  `localhost:PORT` and splices both ways with half-close. A failed dial closes
  that connection and prints the sandbox's reason once per 5 seconds per
  port; the ssh connection and the other forwards keep running.
- With no specs (auto mode), poll `hsh-list@ifup.org` every 2 seconds. A port
  the sandbox starts listening on gets a local listener on the same number; a
  port that stops listening is released at the next poll. A port that is busy
  locally is reported once, with the `LOCAL:PORT` hint, and skipped. This is
  the "the agent says it is on 8000" case with nothing to name.
- `--list` prints the sandbox's listeners and exits.
- Foreground like `ssh -L`, one announcement line per forward, exits when the
  session's workspace container stops or on Ctrl-C. Nothing is installed in
  the pod.
- When `SSH_CONNECTION` is set, each announcement is followed by the matching
  `ssh -N -L PORT:localhost:PORT SERVER-ADDRESS` line for the user's
  computer. The server address is the one from `SSH_CONNECTION`; the user
  substitutes the name they use if they have one.

### 4. `hsh run --forward LOCAL:PORT` (repeatable)

The same forwarder runs inside the `hsh run` process for as long as it is
attached; a detached session needs `hsh forward` from another terminal, and
the announcement printed at start says so. The local ports are checked before
the session is created, so a busy port fails the command without leaving a
session behind. There is deliberately no config or image-label field: an image
should not be able to open listeners on the user's machine; the flag is the
user's explicit act.

### 5. OAuth callbacks

`hsh forward 1455` with `ssh -L 1455:localhost:1455 server` delivers the
browser redirect straight into the waiting login, and `ssh -N -L
1455:localhost:1455 hsh-<id>` (plan 0031) does the same in one hop. This
supersedes 0022's published-port design. `hsh oauth callback` stays as the
paste fallback.

## Security

- Listeners bind `127.0.0.1` only. `--bind 0.0.0.0` is allowed but prints a
  warning: anything that can reach the host can then reach the sandbox's
  service. On a shared server, every local user can reach `127.0.0.1:PORT`
  too; the docs say so and suggest a distinct `LOCAL` port or a unix socket
  (not planned) for shared hosts.
- The data path is a TCP stream into one port the user named (or that the
  sandbox is already listening on). The sandbox cannot use it to initiate
  anything: the host side only dials into the sandbox, and the relay only
  dials loopback and `10.77.0.2` in the inner netns, never the proxy or the
  pod netns. `DIAL` is restricted to ports 1 to 65535 on those addresses; the
  relay refuses sources other than `10.77.0.1`.
- Opening a forwarded port makes the user's browser fetch content served by
  the agent's code. That is the same exposure as running the dev server
  locally, but note that a page from `localhost:8000` can probe other
  `localhost` ports in that browser. Worth a sentence in the docs; not
  something hsh can fix.
- The sandbox's own processes can already talk to the relay (it listens on
  the veth address), so the relay must not grow an ability the shell does not
  have. `DIAL` and `LIST` satisfy this: they only reach the sandbox's own
  loopback and veth address.

## Telling the agent

Agents will not know this exists. Once plan 0028 lands an instruction-file
channel, add a line there: "a server you start on any port is reachable by the
user through `hsh forward`; tell them the port." Until then the harness image
docs mention it. Binding to `0.0.0.0` or `127.0.0.1` both work; the agent does
not need to change anything.

## Phases

1. **Core** (done): relay `DIAL`/`LIST`, `hsh forward PORT`, `--list`.
2. **Auto mode** (done): no-argument `hsh forward` with polling and the ssh
   hint.
3. **`hsh run --forward`** (done), plan 0022's status, and
   `docs/architecture.md`.

## Verification

Unit tests:

- `internal/workspace`: relay `DIAL` ack/err, large transfers with half-close,
  refused source address, `LIST` parsing of `/proc/net/tcp` and `tcp6`,
  `PORT` compatibility
- `internal/forward`: spec parsing and duplicate local ports; explicit forwards
  with a 3 MiB transfer; a busy local port failing up front with the
  `LOCAL:PORT` hint; the session closing ending `Run` with an error; auto
  mode picking up, releasing, and re-picking a listener; one warning per busy
  local port; one warning per burst of dial failures with recovery afterward

Functional (`test/functional.sh` step 25):

1. `hsh run --forward` with a busy local port fails before any session exists
2. `hsh run --forward 18100:18200` reaches a node server in the sandbox
3. `hsh forward --list` shows the server and not the relay's port
4. `hsh forward LOCAL:PORT` works on a session that is already running
5. `hsh forward` with no ports forwards the running server and picks up a
   second server started afterward
6. listeners disappear with the process that owns them

## Future Work

- Functional coverage of a detached session (forward after Ctrl-], and the
  forwarder exiting when the session stops), servers bound to `0.0.0.0` or
  `::1`, and a check that the proxy log and denial database show no new
  entries while a forward is open.
- UDP is out of scope (servers under test are almost always TCP).
- An in-sandbox hint to the agent (see "Telling the agent") once plan 0028's
  instruction-file channel exists.

## Decisions

- Should `hsh forward` be callable from inside the sandbox by the agent
  (e.g. an in-sandbox command that asks the host to open a port)? No: that
  would let the sandbox choose host listeners. The user keeps the control.
