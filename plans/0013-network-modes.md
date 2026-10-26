# Network policy modes: allow all HTTPS by default, switchable

## Goal

Change the default egress policy from "deny everything not allowlisted"
to "all HTTPS traffic is allowed", with a flag (and config field) to
switch between modes:

```
hsh run ghcr.io/<owner>/hsh-pi --tasks setup-tasks.txt            # https default
hsh run ghcr.io/<owner>/hsh-pi --network allowlist --tasks ...     # old behavior
```

Today the only mode is an allowlist (`network.allow`, empty = no
egress at all). That is the most secure posture but makes the first
hour with hsh painful: every host the agent touches has to be
allowlisted (or trusted via `proxy-auto-allow`) before anything works.
Agents routinely reach dozens of hosts (package registries, docs,
git remotes, CDNs). TLS itself provides the meaningful boundary for a
first run: the sandbox cannot serve plaintext traffic, and the proxy
still sits on every connection.

## Modes

`network.mode`, one of:

- **`https`** (new default) — CONNECT to any host on port 443 is
  allowed and tunneled (blind TLS; interception stays limited to
  hosts with configured secrets or OAuth endpoints). Plain-HTTP
  proxy requests and non-443 CONNECTs remain denied unless the host
  is in `network.allow`. DNS resolves every name (no DNS denials
  recorded). `network.allow` entries still apply as an overlay
  (plaintext/other ports) and secrets/OAuth matching is unchanged.
- **`allowlist`** — exactly today's behavior: only `network.allow`
  hosts on any port, DNS gated by the same list, denials recorded.
- **`none`** — no egress at all, DNS refused.

Precedence: the `hsh run --network MODE` flag over the
`network.mode` in the config file or `org.ifup.hsh` label, over the
built-in default (`https`).

## Configuration and CLI

```json
"network": {"mode": "allowlist", "allow": ["api.mistral.ai"]}
```

- `hsh run --network https|allowlist|none` overrides the mode from
  the config/label. The override is passed to the proxy as a new
  `hsh proxy --network-mode` flag (config hot-reload of the user's
  config file keeps working unchanged).
- An empty `mode` means `https` (the new default).
- The shipped harness images must set `"mode": "allowlist"` in their
  `org.ifup.hsh` labels: their labels promise provider-scoped egress,
  and the default change must not silently open them to all of the
  internet.

## Security implications (document in docs/architecture.md)

- In `https` mode a compromised agent can exfiltrate data to any
  TLS endpoint; the allowlist no longer constrains destinations.
  Secret injection and OAuth capture stay host-scoped; the MITM CA
  still only presents itself for configured hosts.
- `proxy-denied` / `proxy-auto-allow` are only meaningful in
  `allowlist` mode; in `https`/`none` mode the denial database stays
  empty.
- The recommendation for untrusted code stays `--network allowlist`.

## Implementation sketch

- `internal/config`: `Network.Mode` field; validate on load
  (`"https"`, `"allowlist"`, `"none"`, empty = `https`).
- `internal/cli/run.go`: `--network` flag; pass `--network-mode` to
  the proxy container.
- `internal/cli/proxy.go`: `--network-mode` flag into
  `proxy.Options`.
- `internal/proxy`:
  - `state.mode`; `allowed(host)` splits into a mode-aware policy:
    `httpsMode` = CONNECT/:443 always allowed; `allowlistMode` =
    today's matching; `noneMode` = nothing. The plaintext/overlay
    check stays `HostMatches(allow, host)`.
  - `handleConnect`: enforce the port rule (443 for the open TLS
    leg; other ports fall back to the allowlist overlay).
  - `serveDNS`: refuse-all in `none`, gate as today in `allowlist`,
    forward everything in `https`.
  - mode printed at startup next to the allowlist line count.
- `examples/config.hsh`, the four `images/*/Containerfile` labels,
  and the functional-test configs: set `"mode": "allowlist"`
  explicitly so existing guarantees and tests are unchanged by the
  new default.
- `main.go` usage, `docs/config.md` (mode field + updated semantics),
  `docs/architecture.md` (security model), `docs/images.md` note.

## Verification

1. Unit tests (`internal/proxy`): mode matrix — `https` allows
   CONNECT to an arbitrary host:443 and denies plaintext/other ports
   not in the allowlist; `allowlist` denies an arbitrary host;
   `none` denies everything; DNS refusal per mode; `--network-mode`
   override beats the config.
2. Functional: new step — run with no `allow` and mode `https`:
   `curl https://<non-allowlisted-host>` succeeds through the proxy,
   `curl http://<same-host>` is denied 407, a DNS lookup of an
   arbitrary name resolves; `--network allowlist` restores 407 for
   both; existing steps (which pin `allowlist`) are unchanged.
3. The shipped images keep their provider-scoped egress
   (`podman image inspect` shows `"mode":"allowlist"`).

## Open questions

- Should `https` also cover 8443 and other common TLS ports? Proposed
  no for now — 443 only; unusual ports go through the allowlist.
- Should there be an `open` mode (plaintext HTTP anywhere)? Proposed
  no: plaintext egress has no acceptable default.
- Whether `https` mode should still record DNS queries for audit
  without refusing them; proposed no (quiet by default), audit trails
  can come back as a separate feature.
