# OAuth browser-redirect callback forwarding (the phase-2 callback bridge)

> **Status: not implemented.** This is the "browser-redirect callback
> bridge" that docs/sandboxed-oauth.md defers to phase 2.

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
