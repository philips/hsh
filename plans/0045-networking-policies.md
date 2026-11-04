## create policy config files

Policy files are local, user-written files. There are no built-in policies and
no fetching policies from a URL.

Policies could be managed with the `hsh policy` subcommand like

```
hsh policy add golang ./golang-policy.json
```

Which would add a file to `~/.config/hsh/policy/network/golang.json`

This is an example policy that might be used for Golang access:

```
{"hosts":["go.dev"]},
{"hosts":["dl.google.com"]},
{"hosts":["proxy.golang.org"]},
```

Path rules (`paths: ["/go/*"]`) and methods need TLS interception and are in
[0048-intercept-proxy.md](0048-intercept-proxy.md).

Then a user could do something like `hsh policy list` and `hsh session apply-policy <session-id> <policy-id>`. Policies could also be referenced from config.json files.

## Hostname matching

(lifted from https://github.com/stripe/smokescreen/blob/master/README.md)

A host can be specified with or without a wildcard, and a glob may contain at most one wildcard. The host must be in Punycode to prevent ambiguity.

A leading *. matches one or more subdomain labels: *.example.com matches api.example.com and a.b.example.com, but not example.com.
A * spanning a label other than the leftmost one matches exactly one label: access-analyzer.*.amazonaws.com matches access-analyzer.us-west-2.amazonaws.com, but not access-analyzer.a.b.amazonaws.com.
A * after a literal prefix within a label matches one or more characters in that label: api*.example.com matches api-east.example.com, but not api.example.com, api.a.example.com, or a.api-east.example.com.

| host                              | valid   |
| --------------------------------- | ------- |
| `example.com`                     | yes     |
| `*.example.com`                   | yes     |
| `access-analyzer.*.amazonaws.com` | yes     |
| `api*.example.com`                | yes     |
| `web*-canary.example.com`         | yes     |
| `web.qa-*.internal.example.com`   | yes     |
| `*.*.example.com`                 | no      |
| `*.api*.example.com`              | no      |
| `*example.com`                    | no      |
| `*-canary.example.com`            | no      |
| `ex*ample.com`                    | no      |
| `login.*.com`                     | no      |
| `foo*.github.io`                  | no      |
| `éxämple.com`                     | no      |
| `example.*`                       | hell no |

## rename https mode to tls_passthrough



https://github.com/agent-substrate/substrate/blob/75a061c290952b432de1131397bdb9a222a33e3e/docs/guides/egress-trust-bundle.md?plain=1#L22

## add a "report" network policy

Users may want to monitor what their agent is doing for awhile. Add a
connection log to proxy.db: what the proxy already knows without interception
(time, host, port, scheme, decision, matching rule or policy, bytes, duration).
A HAR-like request log needs interception and is in
[0048-intercept-proxy.md](0048-intercept-proxy.md).

Also, we need to ammend the schema with a transformations log like iron-proxy

https://github.com/paradigmxyz/iron-proxy#audit-log-format

```
  "request_transforms": [
    {
      "name": "allowlist",
      "action": "continue"
    },
    {
      "name": "secrets",
      "action": "continue",
      "annotations": {
        "swapped": [{ "secret": "OPENAI_API_KEY", "locations": ["header:Authorization"] }]
      }
    }
  ],
```

# Rules

A rule answers two independent questions about a request: may it go through
(`access`), and how loudly is it reported (`log`). Both fields are optional; a
rule sets at least one.

```json
{"hosts": ["proxy.golang.org"], "access": "allow"}
{"hosts": ["gist.github.com"], "access": "deny"}
{"hosts": ["*.sentry.io"], "log": "record"}
```

## access

- `allow` | `deny`.
- Deny wins: a host matched by any `deny` rule is denied, whatever else
  matches it and in whatever order policies were applied.
- A host no `access` rule matches gets the default of `network.mode`.

## log

- `record`: the request is written to `proxy.db` (the connection log and, for
  a denial, the `denied` table). Nobody is interrupted; it is visible only
  when the user looks (`hsh proxy denied --all`, `hsh proxy report`).
- `review`: `record`, plus a decision is asked for: the host is pending in the
  review queue, the status line turns blue, the monitor opens into the review
  list, and the request can be held (`network.approval_wait`). It stays
  pending until the user approves or denies it.
- There is no `off`: every request leaves a record.
- A host no `log` rule matches gets `review` when it is denied and `record`
  when it is allowed. A host matched by a `deny` rule gets `record` (the user
  already decided).
- The quieter value wins when several `log` rules match: log rules exist to
  reduce noise, and a quieted denial is still blocked and still recorded.

Because the fields are independent, a host can be quieted without being
denied: in `allowlist` mode telemetry hosts are already denied, so a "quiet
telemetry" policy only needs `log: record`.

## Session rules

The session's `monitor.db` holds the rules the user makes from the monitor and
the CLI:

```
rules(host TEXT PRIMARY KEY, access TEXT, log TEXT, source TEXT NOT NULL, decided_at TEXT NOT NULL)
  -- access, log: NULL = not set by this rule
  -- source: monitor | inline | cli | policy:<name>
```

| monitor action | writes |
| --- | --- |
| approve (enter) | `{host, access: allow}` |
| deny (`d`) | `{host, access: deny}` |

The table replaces `decisions(host, ignored)`; the proxy reads it read-only as
it reads `decisions` today.

# UX of Adding/Removing Policies

## Add a "report to network policy" command

It should be easy for a user to do something like `hsh proxy report | hsh proxy add` or `hsh proxy report | hsh policy add my-project`.

## Monitor: deny instead of ignore

"Ignore" only makes sense if the user knows hsh denies by default. Replace it
with an explicit deny.

- `d` denies the selected hosts (or the host under the cursor): writes
  `{host, access: deny}` to the session's `monitor.db` rules with source
  `monitor`. The host leaves the review queue because a deny rule's reporting
  is `record`. `i` is removed.
- `t` toggles the denied view; rows are marked `(denied)`.
- Held requests for the host fail at once, as ignore does today.
- A deny is a real rule, not a queue state: it still blocks the host when the
  session's mode allows it (`tls_passthrough`, `report`, or a later
  report/allowlist toggle).
- Undo: approving a host in the denied view replaces its session rule (one
  rule per host, the last decision wins). A deny from a policy still wins over
  a session approval; the monitor shows `denied by policy NAME`.
- Renames that follow: `keys.Ignore`/`ToggleIgnored`, `monitordb.MarkIgnored`,
  the proxy log line `denied (ignored by the user)`, `internal-docs/config.md`,
  `internal-docs/architecture.md`, and `test/functional.sh`.

# Out of Scope

For the initial work I don't want the proxy to become too crazy to configure- that can turn into a distraction to getting things done.

- Path and method rules, full request logs: [0048-intercept-proxy.md](0048-intercept-proxy.md)
- Built-in policies for popular services
- Fetching policies from a URL
- Global defaults in `settings.json` (for example a reporting level for every
  session)

# Open questions and inconsistencies

## Undecided

- Session rules (approvals, denials, applied policies) go in the session's
  `monitor.db`: it is host-written and the proxy already reads it read-only
  (`monitordb.Decisions`). The `decisions(host, ignored)` table grows into a
  rules table. Plan 0036 (session.db overlay) is superseded for this.
- Copy or link: does applying a policy copy its hosts into the session
  (editing the policy file later does not change running sessions), or does
  the session reference the policy by name and follow edits? config.md says
  sessions do not share approved configuration on purpose, which argues for
  copy.
- Report: with reporting split into `log` (see Rules), `report` is only an
  access default: a fourth `network.mode` that allows unmatched hosts, which
  are then `record`ed. Confirm, and decide whether a config field
  (`network.review: queue|log`) sets the `log` default for unmatched hosts.
- Approvals today are also written into the session's `config.json`. With
  session rules in `monitor.db`, that rewrite goes away and the proxy merges
  `config.json` and the rules table.

## Policy format

- The Golang example is not valid JSON: trailing commas, no enclosing array or
  object, no `version`.
- Proposed shape, a `rules` list (see Rules) so `paths`/`methods` from 0048
  can be added without a format change; unknown fields are an error, as in the
  config:

  ```json
  {
    "version": 1,
    "description": "Go toolchain and module proxy",
    "rules": [
      {"hosts": ["go.dev", "proxy.golang.org", "sum.golang.org", "dl.google.com"], "access": "allow"}
    ]
  }
  ```

- The Golang policy is missing `sum.golang.org` (the checksum database
  `go mod download` verifies against by default).
- The policy id is the file name (`golang.json` -> `golang`); say so.
- Plan 0049 adds an optional `recorded` field (image, command, runs, time, hsh
  version) for `hsh policy verify`; include it in the v1 format.

## Commands

- There is no `hsh session` group; session-scoped commands today are
  `hsh proxy ... --session ID`. Pick one of `hsh policy apply [--session ID]
  NAME` or `hsh proxy allow --policy NAME`.
- `hsh proxy add` does not exist; the command is `hsh proxy allow`.
- Proposed set: `hsh policy ls`, `hsh policy show NAME`,
  `hsh policy add NAME FILE|-`, `hsh policy rm NAME`,
  `hsh policy apply [--session ID] NAME`.

## Config references

- `network.policies: ["golang"]` is a new config field: `config.Version` bumps
  (the schema test enforces it). The tls_passthrough rename also bumps it; ship
  both in one bump.
- A config naming a policy that is missing (on resume, or on another machine):
  error or warning?
- Image labels naming policies depend on files in the user's config
  directory. Allow it, or only `--config` seeds?

## Hostname matching

- `login.*.com` and `foo*.github.io` are only rejectable with the public
  suffix list (`golang.org/x/net/publicsuffix`). Take the dependency or drop
  those rows.
- Where the grammar applies: `network.allow`, policy hosts, DNS gating, the
  monitor's "add new allowlist item", and the `monitordb.ValidHost` regex. One
  validator and matcher in `internal/config/host.go` for all of them.
- Secret host binding (`HostCovered`) should stay exact names plus `*.`:
  deciding whether `*.example.com` covers `api*.example.com` gets complicated.
- The leading `*.` semantics already match today's (one or more labels), so
  existing configs are unaffected.
- Non-Punycode input (`éxämple.com`): reject as the table says, or convert with
  `idna` and store the Punycode?
- `example.*` "hell no" is just "no".

## Rename https to tls_passthrough

- State the reason here; the substrate link alone does not explain it.
- `tls_passthrough` is not quite accurate: hosts with secrets or OAuth
  endpoints are still intercepted in that mode.
- Touches the `--network` flag values, `network.mode` validation, and docs.
  The shipped images pin `allowlist`, so mostly the default changes.

## Report

- Secret annotations in the transforms log can only appear for hosts that are
  already intercepted (secret or OAuth hosts), and record secret names only,
  never values.
- Retention and size caps for the connection log (the `denied` table caps at
  `MaxHosts`).
- `hsh proxy report | hsh policy add` turns everything the agent contacted into
  a permanent allowlist, so the agent effectively writes its own policy. Make
  `hsh proxy report --format policy` emit policy JSON the user edits, have
  `hsh policy add` print what it adds, and validate hosts with the config
  validator (readers treat proxy.db as untrusted).
