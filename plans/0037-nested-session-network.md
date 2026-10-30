# 0037: Network egress for nested sessions (proxy chaining)

Status: not started. Prerequisite: plan 0036 (HSH_NESTING) is done —
nested `hsh run` works, but the nested session's shell has **no egress**
(its pod is created with `--network none` and the nested proxy has no
upstream). This plan gives nested sessions real network access by
chaining each nested proxy to its parent session's proxy.

## Current architecture (verified)

A session has three netns levels; all containers of a pod share the pod
netns, which is owned by the container's user namespace (so processes
with the kept NET_ADMIN cap can configure interfaces in it):

```
host ── netavark bridge ── POD NETNS (infra + proxy + ws entrypoint, pid 1)
                             hsh0 = 10.77.0.1 (proxy listens :8080 and :53)
                               │ veth pair created by the ws entrypoint
                             hsh1 = 10.77.0.2 (moved into ...)
         SESSION SHELL NETNS ──┘ (fresh netns, only hsh1, route 10.77.0.0/24)
```

- The session shell reaches only 10.77.0.1:8080 (HTTP proxy) and :53
  (DNS forwarder). The proxy egresses via the netavark bridge.
- The proxy MITMs TLS (secret injection) and forwards DNS based on the
  upstream resolvers in its own /etc/resolv.conf.
- From inside the session shell, `/proc/1/ns/net` is the pod netns (the
  entrypoint's netns); the CLI inside a session has NET_ADMIN/SYS_ADMIN
  in the container user namespace, which owns both the shell netns and
  the pod netns — interface moves between them are permitted
  (MoveInnerVethToPID already does exactly this class of operation).

A nested session (level 2) today: the nested hsh CLI runs in the outer
session shell; it creates a fresh pod with `--network none` (netavark is
impossible: sysctl writes hit the read-only /proc/sys; the pause infra
rootfs overlay is impossible too, so the infra is taken from the session
image). The nested proxy binds 10.77.0.1:8080 in the nested pod netns
and serves the nested shell through its own hsh0/hsh1 pair — but has no
route anywhere, so every upstream dial fails ("offline").

## Why the obvious approaches do not work

- **netavark bridge for the nested pod**: sets sysctls via the
  read-only /proc/sys (`net/ipv4/conf/podman0/route_localnet: Read-only
  file system`). /proc/sys cannot be remounted rw (MNT_LOCK_READONLY)
  and a fresh /proc mount is refused because the session's pid namespace
  is owned by an ancestor user namespace.
- **pasta/slirp4netrs as general egress**: user-mode networking relays
  sockets from the *parent* netns — the outer session shell — which can
  only reach the outer proxy (10.77.0.1:8080) anyway. Raw dials to the
  internet fail by design. User-mode networking is therefore only
  useful as *reachability to the parent proxy*, not as egress itself.
- **Transit routing through the outer shell netns** (veth into the outer
  shell, forward to hsh1): needs ip_forward — a sysctl — read-only.
  Any design that forwards packets *through* a sandbox netns is out;
  only point-to-point links that terminate in a netns that owns the
  parent proxy address work.

## Goal

The nested session's shell gets egress by **chaining the nested proxy to
the parent proxy**:

- the nested proxy dials upstream by issuing CONNECT to the parent proxy
  (address taken from the session shell's HTTP_PROXY env, always
  http://10.77.0.1:8080), piping bytes through;
- the parent proxy applies ITS session policy (allowlist, secrets,
  monitor.db) to the nested traffic, then egresses normally — so nested
  egress is subject to both sessions' policies, outer secrets are
  injected by the outer proxy layer, and denials land in the outer
  monitor;
- DNS in the nested shell resolves via the nested proxy, whose upstream
  resolver is the parent proxy's DNS listener (10.77.0.1:53);
- recursion works: level 3 chains to level 2's proxy (each pod netns
  sees the parent proxy at the same 10.77.0.1 address);
- if any wiring step fails, the nested session degrades to today's
  offline behavior instead of breaking;
- the outer session must be unaffected (no fixed-name interface
  deletion in shared netns — the clobbering lesson from 0036).

## Implementation

### 1. Proxy chaining (internal/proxy)

- New proxy option `--upstream-proxy http://10.77.0.1:8080`, set only by
  a nested `hsh run` (the CLI reads it from its own HTTP_PROXY env and
  forwards it explicitly).
- In the dial path (proxy.go:310, `net.DialTimeout(r.Host, ...)` for
  CONNECT; the plain-HTTP forward path similarly), when an upstream is
  configured: dial the upstream, send `CONNECT <host>:<port>`, check the
  200, then pipe. Keep the existing gate/allowlist/intercept logic in
  front — the parent proxy re-applies its own policy anyway.
- TLS interception stacks: nested shell ↔ nested proxy (nested CA) ↔
  parent proxy (parent CA, parent secrets) ↔ internet.
- DNS: the nested proxy's upstream resolver list comes from
  /etc/resolv.conf (dns.go); when chained, pass the parent proxy's DNS
  (10.77.0.1) explicitly (e.g. reuse the upstream host with :53, or a
  --dns-upstream flag) instead of relying on the nested pod's
  resolv.conf, which is empty under `--network none`.
- Denials/monitoring: nested denials are recorded by the nested proxy
  in the nested monitor.db as today; parent-side denials of nested
  traffic appear in the parent monitor with the parent session id.
  Consider attributing them in the parent monitor UI later (open
  question below).

### 2. Uplink reachability — pick one via a feasibility spike

**Option A (preferred): pasta uplink.**
- Add `--device /dev/net/tun` to the ws container when HSH_NESTING=1
  (the node does not exist in the session container today; podman
  creates it for the container).
- Nested pod create uses `--network=pasta` instead of `--network=none`.
- Pasta gives the nested pod netns a default route whose sockets relay
  from the outer shell netns; only the parent proxy is reachable there,
  which is all the chain needs.
- Spike risks, in order: pasta may try to set sysctls in the nested
  netns (mitigate via containers.conf `[network] pasta_options`, e.g.
  disabling the offending options; pasta generally warns and continues);
  pasta may need capabilities it lacks; /dev/net/tun propagation through
  two levels.
- Test inside a session first with plain podman:
  `podman create --network=pasta ... ; start; exec curl 10.77.0.1:8080`
  before touching hsh.

**Option B (fallback): explicit uplink veth pair.**
- The nested CLI (in the outer session shell, NET_ADMIN kept) creates a
  veth pair: one end moved into the nested pod netns (via the infra
  container pid, known after pod create), the other end moved into the
  outer pod netns via `LinkSetNsFd` with the fd of `/proc/1/ns/net`
  (verified: the pod netns is owned by the container userns, so the move
  is permitted).
- The nested entrypoint (RunParent, already in the nested pod netns with
  NET_ADMIN) assigns the nested-side address and route
  (`10.77.0.0/24 via <gw> dev hshup`) when HSH_NESTED is set, mirroring
  how ConfigureInner configures hsh1.
- The outer pod netns side needs its address configured by the CLI with
  setns into /proc/1/ns/net (runtime.LockOSThread discipline).
- Naming: interfaces land in shared netns, so the outer-side end must be
  per-session unique (`hshdn-<8 chars of session id>`, ≤15 chars); a
  level-1 pod netns can host several concurrent nested sessions. Take
  the /30 for each pair from a level-indexed pool (10.77.16+<level>.0/24
  carved into /30s), derived from the session id with collision probing.
- No clobbering by construction: hsh0/hsh1 keeps its fixed names, new
  names are unique, and CreateHostVeth's stale-name deletion only ever
  runs inside the fresh nested pod netns.

### 3. Wiring in the CLI (internal/cli/run.go)

- Nested runs (HSH_NESTED=1): pass `--upstream-proxy` to the nested
  proxy, switch the pod to the chosen uplink (pasta or the veth), keep
  `--infra-image`/`--infra-command` as today.
- On any uplink failure: log a warning, keep the pod at `--network
  none`, and start the nested proxy without the upstream (today's
  offline behavior), rather than failing the session.

### 4. Docs

- docs/testing.md nesting section: nested sessions have egress through
  the parent session's proxy; parent policy applies to nested traffic;
  offline fallback.
- docs/architecture.md (or the proxy doc): one paragraph on proxy
  chaining and the netns diagram above.

## Test matrix

1. Pasta spike (no hsh changes): in a session, start a container with
   `--network=pasta` and confirm it can open a TCP connection to
   10.77.0.1:8080 and nothing else. If this fails, switch to option B.
2. Chained proxy, https mode: nested session shell
   `curl https://example.com` succeeds through nested → parent proxy;
   parent monitor.db records the flows.
3. Allowlist enforcement at both levels: nested mode allowlist denies
   before the parent sees traffic; parent allowlist denies produce the
   parent's 403 in the nested shell and land in the parent monitor.
4. DNS: nested shell resolves and reaches a hostname (chain carries
   hostnames via CONNECT; nested proxy DNS forwards to 10.77.0.1:53).
5. Secrets: a parent-session secret (e.g. a header for an allowlisted
   host) is present on requests from the nested session.
6. Recursion: level 3 session egresses through two chained proxies.
7. No clobbering: run nested sessions, then confirm the outer session's
   egress and DNS still work (repeat after nested teardown).
8. Fallback: break the uplink (e.g. missing tun) and confirm the nested
   session starts offline with a warning instead of failing.
9. Regression: non-nested sessions unchanged (no new flags, no tun
   device, normal netavark pod networking).

## Open questions

- Should parent-side denials of nested traffic be attributed visibly in
  the parent monitor (e.g. a marker that the request came from a nested
  session), or is the plain host entry enough?
- `hsh forward` / `ssh hsh-<id>` inside a nested session: ingress
  forwarding through two levels is not covered; declare unsupported for
  now and fail clearly, or revisit later.
- Option A dependency: if pasta proves unusable inside a sandbox, option
  B is the design; estimate it at roughly: netsetup uplink helpers
  (~150 lines), CLI wiring, entrypoint route setup.
