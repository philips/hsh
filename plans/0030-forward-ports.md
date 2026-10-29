# Reaching servers inside a session (`hsh forward`)

> **Status: plan, awaiting review.** Nothing here is implemented.

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

```
browser ──► 127.0.0.1:8000 (hsh forward, host netns)
              └─ per connection: podman exec -i <pod>-ws /hsh-workspace-inner
                   --forward-conn 8000          (pod netns, stdio = the stream)
                    └─ TCP 10.77.0.2:7788  "DIAL 8000"   (veth)
                         └─ relay in inner netns ──► 127.0.0.1:8000 (the server)
```

### 1. Relay protocol (`internal/workspace/callback.go`)

The relay's current protocol is one line, `PORT <n>`, followed by raw bytes
(used by `hsh oauth callback`). On success it writes no acknowledgement, so a
failure (`ERR ...`) is indistinguishable from data. Add two verbs and keep
`PORT` working:

- `DIAL <port>` — dial the target; reply `OK\n` or `ERR <reason>\n`; after
  `OK` the connection is a raw bidirectional stream. Target resolution: try
  `127.0.0.1:<port>`, then `10.77.0.2:<port>` (servers bound only to the
  veth address), then `[::1]:<port>`.
- `LIST` — reply with the listening TCP sockets of the inner netns, one
  `port addr` per line, read from `/proc/net/tcp` and `/proc/net/tcp6`
  (the relay runs in the inner netns, so it sees exactly the sandbox's
  listeners). Excludes the relay's own port and the proxy-related ports.

Relay hardening while we are in there: only accept connections whose source
address is `10.77.0.1` (the pod side of the veth); handle half-close
correctly (`CloseWrite` on both directions, wait for both copies) so HTTP/1.0
and `Connection: close` responses are not truncated. The current
"first copy done wins" shortcut is wrong for general streams.

### 2. Stream helper (`internal/workspace`)

`/hsh-workspace-inner --forward-conn PORT`: connect to the relay, send `DIAL
PORT`, read the ack, then copy stdin→conn and conn→stdout with half-close.
Exit non-zero with the `ERR` text on stderr when the dial fails. Same
internal-flag pattern as `--oauth-deliver`, and the binary is already mounted
in the workspace container.

### 3. Host command (`internal/cli/forward.go`)

```
hsh forward [--session ID] [--bind ADDR] [PORT | LOCAL:PORT]...
hsh forward --list [--session ID]
```

- Session resolution: reuse `resolveCallbackSession` (workspace-directory
  label, `--session` to pick one). Rename it to something neutral and move it
  out of `oauth.go`.
- For each spec, listen on `ADDR:LOCAL` (default `127.0.0.1`, `LOCAL` defaults
  to `PORT`). Fail up front, naming the port, if the local port is taken;
  `LOCAL:PORT` is the answer to a clash.
- For each accepted connection, run `podman exec -i <pod>-ws
  /hsh-workspace-inner --forward-conn PORT` with stdin/stdout wired to the
  accepted socket. Close the socket when the exec exits; close exec stdin on
  client half-close.
- No-argument form (`hsh forward`): ask the relay with `LIST`, forward every
  listening port it reports, and keep polling every few seconds so a server
  the agent starts later gets a listener too (and one that exits is released
  on its next failed dial, not eagerly). This is the "agent just told me it is
  on 8000" case without the user having to name anything. `--list` prints the
  sandbox's listeners and exits.
- Runs in the foreground like `ssh -L`, prints one line per forwarded port,
  exits when the session's workspace container stops or on Ctrl-C. Nothing is
  installed in the pod, so there is nothing to clean up.
- When `SSH_CONNECTION` is set, print the matching `ssh -L` line so the user
  does not have to work it out. Do not guess otherwise.

The exec-per-connection shape costs one `podman exec` (on the order of 100 ms)
per TCP connection. That is acceptable for testing a prototype, and it needs
no framing protocol, no extra dependency, and no netns tricks. Phase 1
measures it against a real page (HTML plus a dozen assets, and a websocket).
If it is too slow, the fallback is one long-lived `podman exec -i` running a
multiplexer (yamux over stdio; the helper dials via the same relay), with the
host listener unchanged.

### 4. `hsh run --forward` (convenience, phase 3)

`hsh run --forward 8000` (repeatable, also `forward = [8000]` in the config)
starts the same listeners inside the `hsh run` process for as long as it is
attached. It is syntax over `hsh forward`, not a second mechanism. A detached
session needs `hsh forward` from another terminal, which the docs say plainly.

### 5. OAuth callbacks

`hsh forward 1455` plus `ssh -L 1455:localhost:1455` delivers the browser
redirect straight into the waiting login, replacing the paste step of
`hsh oauth callback` with the fully automatic flow that plan 0022 left as
future work. This design supersedes 0022's published-port and proxy-side
listener design (no pod changes, works on running sessions). `hsh oauth
callback` stays as the fallback when the user cannot forward. Update 0022's
status note when this lands.

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

1. **Core.** Relay `DIAL`/`LIST` with the source check and half-close fixes,
   `--forward-conn`, `hsh forward PORT`, `--list`, measurement against a real
   page and a websocket. Decide exec-per-connection vs multiplexer.
2. **Auto mode.** No-argument `hsh forward` with polling, ssh hint.
3. **`hsh run --forward`** and the config field; update plan 0022's status;
   `docs/architecture.md` and `docs/config.md`.

## Verification

Unit tests:

- relay: `DIAL` ack/err, large transfers in both directions, half-close in
  each direction, refused source address, `LIST` parsing of `/proc/net/tcp`
  and `tcp6` (fixtures), `PORT` compatibility with `hsh oauth callback`
- spec parsing: `8000`, `9000:8000`, bad ports, duplicates

Functional (`test/functional.sh`, new step):

1. start a server in the sandbox on `127.0.0.1:8000`; `hsh forward 8000`
   from the host; `curl http://127.0.0.1:8000/` returns the body, including a
   multi-megabyte response and a keep-alive sequence
2. `LOCAL:PORT` maps; a taken local port fails with a clear message
3. server bound to `0.0.0.0` and to `::1`; server not running gives a failed
   connection (closed socket) and a message on the forwarder's stderr, and a
   later server start works without restarting `hsh forward`
4. detached session: forward works after Ctrl-] and after the workspace
   container goes idle-but-running; forwarder exits cleanly when the session
   stops
5. no-argument mode picks up a server started after `hsh forward`
6. egress is unchanged: with a forward open, the denial database and the
   proxy log show no new entries and the sandbox still cannot reach anything
   outside the allowlist
7. nothing listens on the host without `hsh forward` (no ports opened by
   `hsh run`)

## Future Work

- Does `podman exec -i` propagate stdin EOF promptly enough for half-close on
  every supported podman version? Phase 1 checks; the multiplexer avoids the
  question.
- UDP is out of scope (servers under test are almost always TCP).

## Decisions

- Should `hsh forward` be callable from inside the sandbox by the agent
  (e.g. an in-sandbox command that asks the host to open a port)? No: that
  would let the sandbox choose host listeners. The user keeps the control.
