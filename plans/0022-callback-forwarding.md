# OAuth browser-redirect callback forwarding (the phase-2 callback bridge)

> **Status: partially implemented.** The paste variant is done:
> `hsh oauth callback '<url>'` delivers a browser callback URL into
> the waiting session via the per-session callback relay
> (10.77.0.2:7788, started by the workspace-inner, reached through
> `podman exec` into the pod netns). The `--forward` published-port
> bridge for fully automated redirects remains future work, now
> planned as `hsh forward` in plans/0030-forward-ports.md, which
> supersedes the published-port design below.

## Problem

Browser-redirect OAuth logins (codex/ChatGPT on `127.0.0.1:1455`,
Claude Code, and other CLIs that follow RFC 8252 loopback redirects)
listen on the **workspace's** loopback. That loopback lives in the
workspace's private network namespace and is unreachable from
anywhere else — not from the server's host network, and not from the
user's laptop. Over SSH the browser runs on the laptop, the provider
redirects it to `http://127.0.0.1:<port>/auth/callback?code=...`, and
the callback dies: nothing on the laptop (or, with plain `ssh -L`, on
the server) is listening at that port.

Device-code flows already work (the agent prints a code, the user
completes login in a host browser) — this plan is only about
browser-redirect flows, which need the callback to travel **into** the
sandbox.

## The four legs of a sandboxed browser-redirect login

```
laptop$ ssh -L 1455:localhost:1455 server          leg 1: laptop browser → server
server$ hsh run --forward 1455 <image>              leg 2: server loopback → sandbox netns
sandbox$ codex login                               leg 3: callback arrives; CLI exchanges the code
                                                   leg 4: token exchange via the egress proxy →
                                                   the oauth capture swaps real tokens for phantoms
```

Legs 1 and 4 exist today (SSH local forwarding; plan 0011's capture).
Leg 2 is the missing bridge.

## Design: `hsh run --forward HOST_PORT[:WORKSPACE_PORT]`

Docker-style `-p` semantics, scoped to one session, repeatable:

```
hsh run --forward 1455 ghcr.io/philips/hsh-codex
```

The bridge has three parts, all within hsh's existing topology:

1. **Published port** — `podman pod create -p 127.0.0.1:HOST_PORT:...`
   publishes the port on the server's loopback via pasta, landing in
   the pod's network namespace. Loopback-only by default: the callback
   endpoint is for this user, not the network. (Podman publishes into
   the pod netns; the workspace's private netns is one hop deeper.)
2. **Proxy-side listener** — the egress proxy container (already in the
   pod netns, already running the hsh binary) listens on the published
   port and relays TCP to `10.77.0.2:WORKSPACE_PORT` — the workspace's
   hsh1 address, reachable from the pod netns over the existing veth.
3. **Workspace-side forwarder** — the workspace-inner, before exec'ing
   the shell, forks a small TCP relay (the mounted hsh binary in a
   relay mode) that listens on `10.77.0.2:WORKSPACE_PORT` and forwards
   to `127.0.0.1:WORKSPACE_PORT`, where the CLI's callback listener
   actually is.

`--forward 1455` defaults `WORKSPACE_PORT` to the same number, so the
common case is one flag. The port a CLI will pick is only known to the
user (codex: 1455), so the flag is explicit rather than magic.

### Security

- Published on `127.0.0.1` only; combined with `ssh -L`, the callback
  path is end-to-end private (laptop loopback → ssh → server loopback
  → pod → workspace loopback).
- The bridge carries the callback (and only the callback): the
  authorization code crosses it once, exactly as it would on a
  non-sandboxed machine. The code exchange still goes through the
  egress proxy, where the plan-0011 capture replaces the real tokens
  with phantoms — the bridge never brings real tokens into the
  sandbox.
- `--forward` opens inbound into the workspace netns on exactly the
  named port, on loopback-derived addresses only; it does not change
  the egress policy.

## Documentation shape (the answer to "how should this work")

For harnesses with browser-redirect logins, document:

```
ssh -L 1455:localhost:1455 server
hsh run --forward 1455 ghcr.io/philips/hsh-codex
codex login        # open the printed URL on the laptop; the callback
                   # lands back in the sandbox through both forwards
```

plus, for providers with token-endpoint capture configured, the note
that the exchange is intercepted and only phantom tokens persist
inside the workspace.

## Interim guidance (before the bridge exists)

- Prefer **device-code login** where the harness offers it.
- If the harness supports pasting the authorization code (some CLIs
  accept the code or the full callback URL manually), copy it from the
  browser's failed-callback URL and paste it into the waiting CLI.
- Logging in **outside hsh** on the server and copying the CLI's
  credentials into the workspace works but places *real* tokens inside
  the sandbox — acceptable only when the user considers the workspace
  trusted; it forfeits the phantom-token property.
- A failed callback URL contains a live authorization code — treat it
  as sensitive and do not share it (it is single-use, but still).

## Why a proxy-side MITM cannot replace the bridge

The egress proxy only ever sees the sandbox's own outbound traffic.
The two legs that hurt are browser legs, and they happen on the
user's laptop:

- **the authorize request** (browser → provider) — never traverses
  the sandbox's egress; the CLI only prints the URL, it does not
  fetch it
- **the redirect** (provider → browser → `127.0.0.1:1455`) — again
  laptop-side; and the `redirect_uri` is pinned by the provider to
  the CLI's loopback (RFC 8252; providers do not allow registering a
  proxy-hosted redirect URI), so the proxy cannot host the callback
  endpoint itself

Three MITM-shaped alternatives and why they lose to the bridge:

1. **Route the user's browser through the hsh proxy** (publish the
   proxy, `ssh -L` it, point the laptop browser at it): the proxy
   would then see the redirect and could absorb the code — but it
   requires installing hsh's MITM CA into the user's personal browser
   and routing their browsing through the sandbox's policy engine.
   Strictly worse than forwarding one loopback port, and it widens
   the blast radius of the MITM CA from "this sandbox" to "my
   laptop's browser". Rejected.
2. **Proxy-hosted login broker**: the CLI prints the provider's
   authorize URL directly; hsh cannot change that URL, and providers
   pin `redirect_uri`. There is nothing for the proxy to host.
   Rejected — structurally impossible.
3. **MITM the token exchange** — the one leg the proxy does see, and
   it is already implemented (plan 0011): the exchange POST carries
   the code and PKCE verifier through the egress proxy, which
   captures the real tokens and returns phantoms. This is why the
   bridge is the *only* missing piece: once the callback delivers the
   code to the CLI, the rest of the login is already sandbox-safe.

A **paste variant** of the bridge is worth recording: the user's
browser lands on the failed callback URL (which contains
`code`+`state`), and a host-side command replays it into the sandbox —
`hsh oauth callback '<url>'`. It reuses exactly the same relay parts
(pod-netns relay + workspace forwarder) without the published port,
trades automation for not opening a listener, and works when the
code/state are still valid (they are single-use but short-lived, so
this is time-sensitive). It shares the bridge's security properties
because the delivery path is the same.

The paste command resolves its target session the same way
`hsh run --resume` does: run from the workspace directory, the
`hsh.workspace` pod label identifies the session — the only one is
used, several produce resume's list-the-ids error, and `--session`
picks explicitly. The target port comes from the callback URL itself
(`127.0.0.1:1455`). Mis-delivery fails safe: the OAuth `state` in the
pasted URL does not match the state the target session's CLI
generated, so a wrong-session delivery is rejected by the CLI — the
state parameter is the protection. If no login is in progress, the
connection to the replayed loopback port simply refuses. Contrast
`--forward`, which is unambiguous by construction: the flag is on
`hsh run`, so the published port belongs to that session.

## Implementation sketch

- `internal/cli/run.go`: parse `--forward` (repeatable), pass to the
  pod (`-p 127.0.0.1:P:P`), the proxy container (relay listen port),
  and the workspace container (relay args).
- `internal/proxy`: a TCP relay mode (or a spawned child) in the pod
  netns: listen on `0.0.0.0:P`, dial `10.77.0.2:P`.
- `internal/workspace`: fork the relay child in the inner netns before
  exec: listen on `10.77.0.2:P`, forward to `127.0.0.1:P`.
- Reattach (`--resume`) must re-establish both relay ends (the pod's
  published ports persist with the pod; the relays restart with the
  containers).
- Functional test: a fake loopback-OAuth harness inside the sandbox
  (extend `test/oauthsrv` to redirect to `127.0.0.1:<port>`), plus a
  host-side listener asserting the callback arrives end-to-end through
  the bridge.

## Verification

1. Over a real SSH `-L` (or a pty stand-in): `codex`-style login
   completes; the callback's `code` arrives at the in-sandbox listener;
   the exchange response is phantom-rewritten when the provider's token
   endpoint is configured for capture.
2. The bridge is inert without `--forward`; no listener appears on the
   server without the flag.
3. Unit tests for both relays; functional coverage as above.
