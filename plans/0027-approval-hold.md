# Hold denied requests for approval instead of failing them

> **Status: implemented** (core, monitor toggle and held markers, the
> attended marker; see Decisions). Desktop notifications are deferred. Builds on the inline review screen
> (plans/0026-detach-attach.md Phase 2) and the denial database
> (plans/0003-proxy-denied.md). Implements option C of
> plans/0024-process-approval.md.

## Goal

Agents that hit an egress denial immediately start working around it:
other mirrors, other tools, rewriting commands, asking the user — each
attempt costs tokens, and the workaround is often worse than the thing
that was denied. A request that is **held** until the user decides looks
like a slow network instead of a refusal. If the user approves within the
hold window the original request just succeeds and the agent never knows;
if not, the request fails as it does today.

## Where it applies

The default network mode is `https` (CONNECT :443 to any host is already
allowed), so holds matter where denials happen:

- `allowlist` mode: every unlisted host, at DNS and again at CONNECT
- `https` mode: plaintext HTTP proxy requests and non-443 CONNECTs to
  unlisted hosts
- `none` mode: never held — nothing can be approved there (the mode forbids
  all egress regardless of `network.allow`), so a hold would only add delay

## Design

### Config

`network.approval_wait`: a duration string, **default `"30s"`**. `"0s"`,
`"0"`, or `"off"` disable holding. Values above `"5m"` are rejected at load
time. `config.Load` validates it; `Network.ApprovalWait()` returns the
parsed duration. It is a per-session setting like the rest of the session
config copy (seed config, image label, or the session's `config.json`) and
is hot-reloaded with the proxy's existing one-second config watch, so
changing it affects the next request and releases current holds (below).

### Holding in the proxy

A single helper decides what to do with a request the policy would deny:

```go
func (p *Proxy) awaitApproval(ctx context.Context, host, target, kind string,
    allowed func() bool, maxWait time.Duration) verdict
```

- It records the denial first (`denieddb.Record`, as `deny()` does today),
  so the host is visible in `hsh monitor` and the status line at once.
- It then polls every 250 ms until one of:
  - `allowed()` becomes true (the config reload picked up an approval) →
    `verdictAllow`, and the caller proceeds exactly as if the host had been
    listed
  - the denial database says the user ignored the host
    (`allowed = 1 AND ignored = 1` for this host and session) →
    `verdictDeny` immediately
  - `maxWait` elapses, the wait is disabled by a config reload, or the
    client goes away (`ctx.Done()`) → `verdictDeny` / abandon
- Concurrent holds are capped (default 64 across the proxy, a constant to
  start). Over the cap the request is denied at once, as today.
- Callers:
  - `handleConnect`: before hijacking, with `allowed = connectAllowed`
  - `ServeHTTP` plaintext path: before forwarding, with
    `allowed = plaintextAllowed`
  - `serveDNS` (allowlist mode only): before answering REFUSED, with
    `allowed = allowListed(name)`; if allowed, fall through to the upstream
    exchange
- `deny()` keeps its current role as the immediate-failure path and the
  post-timeout path.

### Per-protocol hold limits

Clients give up on their own schedule; a hold longer than the client's
patience just becomes a timeout, which is better than a refusal but no
better than needed.

| Path | Typical client patience | Hold used |
|---|---|---|
| HTTP CONNECT / plain HTTP | curl: minutes; Go, reqwest: 30 s+; Node (undici) connect: 10 s | `approval_wait` |
| DNS | glibc and Go resolvers: 5 s per try, 2 tries, retransmitting the same query | `min(approval_wait, 9s)` |

The DNS hold stays under two resolver tries so the answer, when approved,
reaches a resolver that is still listening. Retransmissions of the same
query are held independently (each counts against the cap and the denial
count) and all released together on approval. These figures are the
starting point; the tests measure them against curl, Node fetch, Python
requests, and Go.

### The denial message

Independent of holding, the 407 body changes from "not in network.allow"
to text aimed at an agent: the host, that the sandbox requires the user's
approval, that the user can approve it with `hsh monitor`, and an explicit
instruction to retry later and not to look for workarounds. DNS REFUSED
cannot carry text, so the DNS side relies on the hold.

### Opt-out and toggle

- **Config**: `network.approval_wait: "off"` (or `"0s"`) in the seed config,
  an image's `org.ifup.hsh` label, or a session's `config.json`.
- **`hsh proxy hold [--session ID] on|off|DURATION`**: scriptable
  equivalent. It rewrites `network.approval_wait` in the session config
  (`config.SetApprovalWait`, next to `AddAllowedHosts`) with the same
  atomic-write behavior; `on` removes an explicit override so the default
  applies.
- **Monitor (both `hsh monitor` and the inline review screen, which share
  one model)**: key `w` toggles holding for the session of the selected
  row; the title line shows the state (`holding: 30s` / `holding: off`) for
  the session in view, and the status message confirms the change
  ("holding off for session X — requests fail immediately"). The model
  reads the session config on each refresh, so changes made elsewhere show
  up. In the unscoped `hsh monitor` with several sessions, `w` acts on the
  selected row's session and says which; with no rows it does nothing.
  Toggling takes effect within the proxy's one-second reload; holds in
  flight when it is turned off are released as denials on the next poll.

### Showing what is held (phase 2)

`denied.db` gets `holding INTEGER NOT NULL DEFAULT 0`: the number of
requests currently held for that host and session (incremented when a hold
starts, decremented when it ends; the proxy zeroes its session's rows at
start so a crash cannot leave stale counts). The monitor lists held hosts
first with a marker, and the inline status line becomes
`* 2 proxy requests need review (1 held)`. This tells the user which
approvals unblock a waiting agent.

## Security

Holding does not widen policy. Allow decisions still come only from the
allowlist in the session config (or, in `none` mode, never); a hold ends in
the same denial as today if nobody approves. The caps bound the cost of an
agent that opens many held connections. The monitor toggle edits the host
session config exactly as approvals do, from outside the sandbox; the
sandbox cannot write it (mounted read-only into the proxy only). Nothing
in the held request's content is inspected or stored beyond host, target,
and the count.

## Phases

1. **Core**: config field and validation, `awaitApproval`, CONNECT, HTTP and
   DNS callers, the concurrency cap, the new denial message,
   `denieddb` decision lookup, `hsh proxy hold`.
2. **Monitor**: the `w` toggle and title state in `proxywatch`, the
   `holding` column, held markers and the status-line count.
3. **Docs and tuning**: measure real clients, adjust the DNS limit and the
   default if needed.

## Tests

- Config: parsing (`"30s"`, `"off"`, `"0"`, invalid, over the 5-minute
  limit), default when absent, round trip through the session-config
  rewrite.
- Proxy unit tests (extending `modes_test.go`): a held CONNECT and a held
  plain-HTTP request are released by an approval written to the config;
  an ignore decision in the database releases with a denial before the
  timeout; the timeout produces the 407 with the new message; disabling
  during a hold releases it; a client disconnect abandons the hold and
  frees its slot; the cap denies immediately; `none` mode never holds;
  `https` mode still tunnels :443 without holding.
- DNS: a held query is answered normally after approval; REFUSED after the
  DNS limit; retransmitted queries are all released.
- `denieddb`: the decision lookup states, `holding` increments and the
  startup reset.
- `proxywatch`: `w` toggles and writes the session config, the title
  reflects it, a no-row toggle does nothing.
- `hsh proxy hold`: on, off, a duration, and a missing session.
- Functional (`test/functional.sh`): in an allowlist session, a `curl` to an
  unlisted host started in the background is still pending after a few
  seconds, completes after `hsh proxy allow`, and a second host fails after
  `hsh proxy hold` off immediately with the new message; an `ignore`
  decision fails a held request fast.

## Docs

`docs/config.md` (the `network.approval_wait` field, the toggle, holds per
mode, client-timeout guidance), `docs/architecture.md` (the hold in the
proxy section and the DNS limit), `docs/testing.md`, and the monitor key
help.

## Open questions

All resolved; see Decisions below.

## Decisions

- **Unattended sessions.** Yes, implement a liveness file for the proxy to read to know if someone might be watching. Implemented: the `attended` file (see docs/architecture.md); holds apply only while it is fresh, so a proxy with no marker never holds.
- **Per-host overrides** No, do not do this.
- **30 s as the default.** OK
- **Desktop notifications** Yes, hold on this (deferred; not built).
- **Added during implementation.** The attended marker is also touched by
  `hsh monitor` and `hsh proxy allow --watch`, not only by the relay, and the
  relay creates it before the container starts so the first requests of a
  session are held. A proxy started without `--attended` never holds, which
  keeps the existing proxy tests and standalone `hsh proxy` immediate.
