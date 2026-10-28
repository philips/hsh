# Network egress tests with a stock python http.server origin

> **Status: not implemented.**

## Goal

Pin the sandbox's network policy end to end with the simplest possible
origin — `python3 -m http.server` — whose own access log is the ground
truth for "did this request actually reach the network?". Today egress
behavior is covered indirectly (the dummy OpenAI endpoint, live
`example.com` in step 12, curl exit codes). This plan adds a direct
matrix: each network mode, each path out of the sandbox, observed from
both sides (what the sandbox saw, what the origin saw).

## Why python's http.server

- stdlib only, one line to start, plain HTTP, and it logs every request
  it serves with the peer address — a zero-dependency oracle for
  "reached the origin". A denied request must leave **no log line**.
- python3 is absent from the fixture image (verified: `command -v
  python3` fails inside `localhost/hsh-example`), so the origin runs on
  the **host** and the sandbox reaches it as `host.containers.internal`
  — the same path the dummy endpoint already uses.
- Bind `0.0.0.0`, not loopback: pasta does not forward sandbox
  connections to loopback-only host listeners (documented in
  docs/testing.md for the dummy endpoint).
- Use its own port (8898), distinct from the dummy endpoint's 8899.

## Verified behavior (this host, observed — the baseline the test pins)

Empty allowlist, request to `http://host.containers.internal:8898/`:

| mode | via proxy | direct (`--noproxy`) | DNS for an unknown name | origin saw |
|---|---|---|---|---|
| `https` | 407 | connect fails (curl rc 7) | resolves | nothing |
| `allowlist` | 407 | connect fails | REFUSED | nothing |
| `none` | 407 | connect fails | REFUSED | nothing |

With `network.allow: ["host.containers.internal"]` in allowlist mode:
200, body returned, and the origin logs the request. The origin sees the
**host's LAN address** as the peer (pasta masquerades), never the
sandbox's internal `10.77.0.2`.

## Test pitfalls found while probing (the plan encodes the fixes)

1. **`getent hosts host.containers.internal` proves nothing about DNS
   policy**: podman injects that name into `/etc/hosts`, so it resolves
   even in allowlist mode with an empty list. DNS assertions must use a
   name only DNS can answer (e.g. `example.com`, or a throwaway name
   expected to be REFUSED).
2. **Never truncate the origin's log under a running server**: `: > log`
   leaves the server writing at its old offset, producing a sparse file
   of NULs that `grep` calls binary. Give each assertion window a fresh
   log path (restart the server per case) or read the server's stdout
   through a pipe; use `grep -a` defensively.
3. **Port 443 cannot be used for an unprivileged test origin**, so the
   "https mode tunnels TLS to any host on 443" rule stays covered by
   step 12 (live `example.com`); this plan covers the *non-443* and
   plaintext sides of that rule, which are the denials.
4. **Don't wait on the whole job table**: any helper the test starts in
   the background is waited on by pid, never bare `wait` (a bare wait
   blocks on the suite's long-lived dummy endpoint — a 17-minute hang
   already happened once).

## Test matrix (new functional step, "egress")

Origin: `python3 -m http.server 8898 --bind 0.0.0.0` serving a temp
directory with `hello.txt`; a helper starts it with a fresh log per
case and returns the count of served `GET`s.

1. **Deny by default, per mode** — for `https`, `allowlist`, `none` with
   an empty allowlist: plaintext request via the proxy is 407 and the
   origin log has zero requests (the table above).
2. **Allowlisted host passes** — `allow: ["host.containers.internal"]`:
   200, expected body, exactly one origin log line, and the logged peer
   address is **not** in `10.77.0.0/24` (internal addressing does not
   leak).
3. **Direct bypass fails** — `curl --noproxy '*'` cannot connect, in
   every mode, and the origin log stays empty even when the host *is*
   allowlisted (the allowlist applies to the proxy path only; there is
   no other path).
4. **Port-agnostic matching** — with the host allowlisted, a second
   origin on another port (8897) is also reachable; allowing a host
   allows all its ports (documented matching rule).
5. **Hot reload, end to end** — start denied (407), then
   `hsh proxy allow --session <id> <host>` mid-session, and the very
   next request from the *same running shell* succeeds within ~2s, with
   no restart. This is the TOFU loop observed at the origin.
6. **Denials are recorded** — after the denied case, `hsh proxy denied
   --session <id>` lists the host; after `proxy allow` it is gone.
7. **DNS gate** — in `allowlist` mode an unlisted real name (not
   `/etc/hosts`-injected) is REFUSED and shows up in the single denied
   queue; in `https` mode it resolves (the documented difference).
8. **Optional TLS-on-a-non-443-port** — a tiny python script wraps
   `http.server` in TLS (self-signed) on 8896: via the proxy in `https`
   mode it is denied unless allowlisted (non-443 rule), pinning the
   rule step 12 cannot reach.

## Out of scope

- Real-internet egress and TLS interception behavior (steps 3/12/9 and
  the oauth tests cover those).
- Secret injection headers (python's server does not log headers; the
  dummy endpoint already asserts injection).
- Wildcard host matching (`*.example.com`) — needs resolvable fake
  names; the matching is unit-tested in `internal/proxy` instead.

## Implementation sketch

- `test/functional.sh`: a `start_origin PORT LOG` helper (python3
  http.server in a temp dir, `--bind 0.0.0.0`, stdout/stderr to a fresh
  log, pid recorded and killed by the exit trap) and `origin_hits LOG`
  (count of `GET` lines, `grep -a`); a new step after step 16 using
  `hsh run --rm` sessions per mode.
- Skip with a clear `SKIP:` when `python3` is unavailable (same
  convention as the pty driver in step 14).
- docs/testing.md: a step entry and the pitfalls above (they are
  exactly the traps the next person to write network tests will hit).

## Verification

1. The matrix assertions pass on this host with the observed values
   above; each denied case shows zero origin log lines.
2. Flip the policy (e.g. temporarily make `allowlist` allow everything)
   and confirm the matrix fails — the test must be able to fail on the
   thing it guards.
3. Full `test/functional.sh` stays green and its runtime grows by under
   a minute (one `--rm` session per case, no image builds).
