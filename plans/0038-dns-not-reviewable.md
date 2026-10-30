# 0038: DNS lookups are filtered, not reviewed

Status: implemented.

## Goal

The egress policy gates DNS by the same allowlist as every other egress
(resolve only what `network.allow` permits), but a refused lookup is no longer
a *denial event*: it is not recorded in `monitor.db`, never shows up in
`hsh monitor` / the inline review / `hsh proxy denied`, and is never held for
approval. Only connection attempts (CONNECT, plaintext HTTP, transparent TLS/
TCP in the gVisor runtime) create review items.

## Why

Filtering DNS on its own has no review use case:

- The workspace has no route except to the proxy, so a resolved name is never
  directly reachable. The only thing the lookup gate protects is DNS
  tunnelling, and that is protected by refusing the query, not by asking a
  human about it.
- A lookup is not "the agent tried to reach X". Resolvers, libc, curl and
  browsers issue speculative queries (AAAA, HTTPS/SVCB type 65, TXT, search
  domain expansions, names that are looked up and then reached through the
  HTTP proxy instead). Each one in `allowlist` mode becomes a monitor row
  (target `name:53`), so test and real sessions fill the queue with hosts
  nobody tried to connect to, and a row keyed host+session whose `target`
  stays `name:53` even when a real connection follows.
- DNS holds make a plain `getent` block for up to 9 s on an unapproved name,
  for a result the caller cannot use anyway.
- The gVisor runtime already works this way for A/AAAA (placeholder answers,
  only the connection is recorded — internal-docs/gvisor.md). The veth runtime and the
  non-A/AAAA queries in the gVisor runtime are the odd ones out.

## Current behaviour (verified)

`internal/proxy/dns.go` `serveDNS` is the single DNS policy point; it is
reached from the veth forwarder (`startDNS`, 10.77.0.1:53) and from the gVisor
netstack (`serveTransparentDNS`, after `answerSynthetic` has handled A/AAAA of
non-allowed names in `allowlist`/`none` mode).

In `allowlist` mode, for a name not on the allowlist it either calls
`awaitApproval(..., "dns", ..., min(approvalWait, dnsHoldLimit))` (holds
enabled; this records the denial and bumps `holding`) or `recordDenial(name,
name+":53")`, then answers REFUSED. `none` mode refuses without recording.
`https` mode forwards every name.

## Design

Policy for a query name, evaluated in `serveDNS`:

| mode | name allowlisted | result |
|---|---|---|
| `none` | – | REFUSED, log only |
| `allowlist` | yes | forwarded upstream |
| `allowlist` | no | REFUSED immediately, log only |
| `https` | – | forwarded upstream (unchanged: any name can be reached on 443, so resolving it is not a separate decision) |

A refused lookup: one log line (`dns: refused <name> (not in network.allow)`),
no `recordDenial`, no `awaitApproval`, no hold, no change to `holding` counts.

Unchanged on purpose:

- gVisor `answerSynthetic`: A/AAAA for non-allowed names still get the
  placeholder from `198.18.0.0/15`, so the *connection* reaches the proxy and
  is recorded/held/denied by name. That answer is synthesised locally and
  never leaves the proxy, so it is not a tunnelling channel. Without it a
  gVisor session could never request approval in `allowlist` mode, because
  the lookup would no longer be reviewable.
- Approving a host (allowlist change) makes both the lookup and the
  connection work; nothing in the approval path changes.
- In the veth runtime, tools reach the proxy through `HTTP(S)_PROXY` and send
  the hostname in CONNECT, so their denial is still recorded by the HTTP
  path. A tool that resolves locally and then tries to connect directly has
  no route anyway and leaves no review item; that is accepted (the
  agent notice already tells it to ask the user for the host).

### Code changes

- `internal/proxy/dns.go`: in `serveDNS`, replace the `allowlist` branch
  (hold / `recordDenial`) with a log and REFUSED. Drop the `context` import
  if unused.
- `internal/proxy/hold.go`: delete `dnsHoldLimit`. Keep `awaitApproval`'s
  `kind` parameter (still used for `TLS`, `TCP`, HTTP methods).
- `internal/cli/proxy_group.go`: `denied` description drops "(DNS lookups
  share this queue)".
- `internal/notice/notice.go`: keep "a refused DNS lookup" in the description
  of what a block looks like (it is still true); no change expected, confirm
  wording when implementing.

### Tests

- `internal/proxy/hold_test.go`: delete
  `TestHeldDNSQueryIsAnsweredAfterApprovalAndRefusedOtherwise` and
  `TestDNSHoldIsCappedBelowResolverPatience`. Add, using `startHoldProxy`
  with `allowlist`, holds enabled and the attended file fresh:
  - a query for a non-allowlisted name is REFUSED in well under the hold
    time, `monitordb` has no row for it, and `activeHolds()` stays 0;
  - the same for a TXT / HTTPS-type query (the gVisor non-A/AAAA path);
  - an allowlisted name (`localhost`) is answered (not REFUSED);
  - after `e.allow(name)` and the one-second reload, the same query is
    answered.
- Add a `none`-mode case: REFUSED, no row.
- `test/functional.sh` (steps around lines 192-243):
  - keep: allowlisted name resolves; non-allowlisted `getent` fails; the
    direct `curl --noproxy '*'` fails at TCP, not at DNS;
  - invert "DNS denial is not in the single review queue": assert
    `dns-denied.example.net` is **absent** from `hsh proxy denied`;
  - the "expected the refused DNS lookup to be pending in the single queue"
    check (lines ~221-225) relied on that row. Replace it with an assertion
    on something the test really denies by connection (add one `curl` through
    the proxy to a non-allowlisted host if nothing else is pending), so
    "approved hosts leave the queue" still tests a non-empty queue.
- Check `test/security.py`, `test/inline.py`, `test/gvisor.sh` and the
  `blocked*.example.test` curls in `functional.sh` (lines ~968, ~986): they
  drive denials through `curl` and the proxy env, so they should still
  produce their row from the CONNECT/HTTP path; run them to confirm the
  expected host still appears (and, for the gVisor script, from the
  connection).
- Run `go test ./internal/proxy/...` and the functional suite, then grep the
  monitor DB the suites leave behind for any `:53` target.

### Docs

Design docs live in `internal-docs/`; update there, no
history prose:

- `internal-docs/architecture.md`: the DNS section (lines ~321-340: replace "DNS
  denials are recorded in the same `denied` table" with: refused lookups are
  logged only, review items come from connection attempts); the hold section
  (~299: holds apply to CONNECT and plain HTTP, drop the DNS sentence and the
  9 s cap); the monitor DB section (~394: drop "and DNS denials"); the
  threat-model lines (~839, ~854) to say DNS is gated but not reviewable.
- `internal-docs/config.md`: network modes (~188-192), `approval_wait` (~244 drop "or
  a DNS lookup", ~254 drop the 9 s bullet), and the `network.allow` DNS
  paragraph (~430: lookups of non-allowlisted names are refused and not
  recorded; hosts become reviewable when something connects to them).
- `internal-docs/gvisor.md` (~150): generalise the last paragraph — a lookup of a
  denied name is never recorded in either runtime; only the connection is.
- `internal-docs/testing.md` (~75): drop "and held DNS queries".
- `internal-docs/oauth.md` (~106): remove the `dns: denied auth.openai.com` log hint
  (the log line becomes `dns: refused ...`; update it).

## Out of scope

- Changing what `https` mode resolves.
- Rate-limiting or logging DNS tunnelling attempts beyond the log line.
- Any new config knob.
