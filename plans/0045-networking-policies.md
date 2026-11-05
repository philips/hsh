# Network policies

> **Status:** draft. Phases 1, 2 and 3 implemented.

## Goal

- One place for a session's network decisions: a rules table in the session's
  `monitor.db`, written by the host, read by the proxy.
- An explicit deny, in the monitor and the CLI, that blocks a host in every
  mode, replacing "ignore".
- Reusable, user-written policy files that can be applied to a session.
- A connection log and a `report` mode, so a user can watch what an agent
  does before writing a policy (plan 0049 builds on it).

## Decisions

- Policy files are local config. There are no built-in policies; the only
  default is the mode's.
- Applying a policy copies its rules into the session. Editing or removing the
  policy file later does not change running sessions.
- Policies are applied to sessions only. Configs and image labels cannot name
  policies (no `network.policies` field).
- Approvals are no longer written into the session's `config.json`. The
  session's `network.allow` is copied into the rules table when the session is
  created; after that the rules table is the session's allowlist.
- Host patterns must be Punycode; non-ASCII input is rejected.
- `https` mode keeps its name; there is no `tls_passthrough` rename.

## Phases

Each phase ships on its own, in order.

1. Session rules and monitor deny
2. Host pattern grammar
3. Policy files and `hsh policy`
4. Connection log, `report` mode, `hsh proxy report`

# Rules

A rule answers two independent questions about a request: may it go through
(`access`), and how loudly is it reported (`log`). Both fields are optional; a
rule sets at least one.

```json
{"hosts": ["proxy.golang.org"], "access": "allow"}
{"hosts": ["gist.github.com"], "access": "deny"}
{"hosts": ["*.sentry.io"], "log": "record"}
```

Ports and schemes are not part of a rule. A rule matches a host on any port and
scheme.

## access

- `allow` | `deny`.
- Deny wins: a host matched by any `deny` rule is denied, whatever else
  matches it and in whatever order its rules were written. This includes TLS
  on port 443 in `https` mode, which today always goes through.
- A host no `access` rule matches gets the default of `network.mode`:

| mode | unmatched host |
| --- | --- |
| `https` | allowed for TLS on 443, denied otherwise |
| `allowlist` | denied |
| `report` (phase 4) | allowed |
| `none` | denied; rules are ignored |

## log

- `record`: the request is written to `proxy.db` (for a denial, the `denied`
  table; from phase 4, also the connection log). Nobody is interrupted; it is
  visible only when the user looks (`hsh proxy denied --all`, and from phase 4
  `hsh proxy report`).
- `review`: everything `record` does, plus a decision is asked for: the host
  is pending in the review queue, the status line turns blue, the monitor opens
  into the review list, and the request can be held
  (`network.approval_wait`). It stays pending until the user approves or
  denies it.
- `review` only affects denied requests. On an allowed request it acts as
  `record`.
- Defaults when no `log` rule matches: `review` for a request denied by the
  mode default, `record` for a request denied by a `deny` rule (the decision is
  already made), `record` for an allowed request.
- The quieter value wins when several `log` rules match: log rules exist to
  reduce noise, and a quieted denial is still blocked and still recorded.

Because the fields are independent, a host can be quieted without being
denied: in `allowlist` mode telemetry hosts are already denied, so a "quiet
telemetry" policy only needs `log: record`.

## Evaluation

For a request to `host:port`:

1. Mode `none`: deny.
2. Any matching rule with `access: deny`: deny.
3. Any matching rule with `access: allow`: allow.
4. Otherwise the mode default.
5. `log` from the matching `log` rules (quietest wins), else the defaults
   above.

One function in `internal/proxy` does this. `connectAllowed`,
`plaintextAllowed`, `allowListed` (the DNS gate in `dns.go` and
`netstack.go`) and the netstack paths all call it. The hold logic in
`hold.go` uses its `log` result: only `review` denials are held.

# Phase 1: Session rules and monitor deny

## Schema

`monitor.db` schema version 2 replaces `decisions(host, ignored)`:

```
rules(
  pattern    TEXT NOT NULL,
  source     TEXT NOT NULL,   -- config | user | policy:<name>
  access     TEXT,            -- allow | deny | NULL (not set by this rule)
  log        TEXT,            -- record | review | NULL
  decided_at TEXT NOT NULL,
  PRIMARY KEY (pattern, source)
)
```

- `config`: copied from the session's `network.allow` at session creation.
- `user`: decisions from the monitor, the inline review screen, and
  `hsh proxy allow|deny`. One row per pattern: the latest user decision
  replaces the earlier one.
- `policy:<name>`: rules copied by `hsh policy apply` (phase 3).
- The host writes `monitor.db` (`monitordb.Store`); the proxy reads it
  read-only (`monitordb.Decisions`) and reloads all rules on its one-second
  tick, as it reloads `config.json`. Patterns mean lookups match against every
  rule, not a keyed query.
- A proxy that cannot read the rules table logs it
  and uses no rules, so it falls back to the mode default. In `https` mode
  that means 443 still goes through and every allowlisted plaintext host is
  denied.
- The standalone proxy (`hsh-proxy` without `--monitor-db`) has no rules
  table; it uses the config's `network.allow` as `config` allow rules.

## Session creation

- `monitordb.InitSession(sessionDir, allow)` creates the schema and inserts
  each `network.allow` entry of the session's `config.json` as `{pattern,
  access: allow, source: config}`. It runs once, at creation; resume copies
  nothing.

## Who writes what

| action | writes |
| --- | --- |
| approve in the review list (enter) | `{host, access: allow, source: user}` |
| deny in the review list (`d`) | `{host, access: deny, source: user}` |
| "Proxy: add new allowlist item" | `{pattern, access: allow, source: user}` |
| "Proxy: add new deny item" (new) | `{pattern, access: deny, source: user}` |
| `hsh proxy allow [--all \| URL]` | `{host, access: allow, source: user}` |
| `hsh proxy deny URL\|PATTERN` (new) | `{pattern, access: deny, source: user}` |

`hsh proxy deny` and "add new deny item" accept wildcard patterns, as "add
new allowlist item" does for allows. In this phase patterns are today's
(exact names and a leading `*.`); phase 2 widens the grammar.

`config.AddAllowedHosts` and its three callers (`internal/cli/proxy_group.go`,
`internal/tui/proxywatch/model.go`, `internal/tui/palette/model.go`) are
removed. `hsh settings edit --session` still edits `config.json` for
`approval_wait` and `suspend_delay`; the proxy still hot-reloads `config.json`
for the mode, secrets and holds, but no longer reads `network.allow` from it.

`hsh proxy denied --all` lists every recorded host with its state (`pending`,
`allowed`, `denied by you`, `quiet`).

## Review queue

A row in the `denied` table is:

- **pending** when the host's effective `log` is `review` (no `access` rule
  matches it and no `log: record` rule quiets it)
- **allowed** when an `allow` rule matches and no `deny` rule does
- **denied** when a `deny` rule matches; the row shows its source:
  `(denied by you)` or `(denied by policy NAME)`
- **quiet** when only a `log: record` rule matches

`monitordb.DeniedHost` replaces `Allowed`/`Ignored`/`AllowedAt` with
`State` (one of the four above), `Source`, and `DecidedAt`.
`monitordb.Decision` becomes `{Access, Log, Source}`. `Store.Ignored` becomes
`Store.Denied`, `MarkAllowed`/`MarkIgnored` become `Allow`/`Deny`.

## Monitor

- `d` denies the selected hosts (or the host under the cursor). `i` is
  removed.
- `t` toggles the denied view (`hsh monitor --all` opens with it on).
- In the denied view, enter on a row denied by you approves it: the `user`
  row becomes `allow`. A row denied by a policy cannot be approved there; the
  status line says `denied by policy NAME; hsh policy unapply NAME to undo`.
- "Add new allowlist item" on a pattern a `deny` rule matches adds the rule
  and says the host stays denied, naming the deny's source.
- Held requests for a host fail at once when it is denied, as ignore does
  today. A host already denied by a rule is not held at all.

## Proxy messages

- Log lines: `denied (denied by you)`, `denied (denied by policy NAME)`,
  `denied while held (denied by you)`.
- The 403 for a request denied by a rule says the user decided:

  ```
  hsh: egress to %s is denied by the user's policy for this session.
  Do not ask for it again or try other tools, mirrors, proxies, or DNS tricks.
  ```

  A request denied by the mode default keeps today's message.

## Renames

`keys.Ignore`/`ToggleIgnored`, `actionIgnore`, `showIgnored`/`ignoredRows`,
`palette.Options.Ignored`/`ShowIgnored`, `Store.Ignored`, `MarkIgnored`,
`DeniedHost.Ignored`, `Decision.Ignored`, the `hsh monitor --all` help text
("show denied hosts"), and the proxy log lines above.

## Docs and tests

- `internal-docs/config.md` (approval wait, the monitor, the session config
  no longer being edited by approvals), `internal-docs/architecture.md` (the
  `monitor.db` schema and the queue description), `internal-docs/testing.md`,
  `internal-docs/terminal-checks.md`.
- Unit: evaluation order (deny beats allow across sources, `user` last write
  wins, mode defaults, `log` defaults and quietest-wins), `InitSession` copying `network.allow`, queue states, a deny
  blocking 443 in `https` mode, held requests failing on deny, the 403 text.
  Update `hold_test.go`, `proxywatch/model_test.go`, `monitordb_test.go`.
- `test/functional.sh`: approve and deny from the CLI, a wildcard deny, deny
  then approve again, a denied host refused on 443 in `https` mode.

# Phase 2: Host pattern grammar

(lifted from https://github.com/stripe/smokescreen/blob/master/README.md)

A pattern is a hostname with at most one wildcard, in Punycode, lowercase,
trailing dot removed.

- A leading `*.` matches one or more subdomain labels: `*.example.com`
  matches `api.example.com` and `a.b.example.com`, but not `example.com`.
- A `*` label other than the leftmost matches exactly one label:
  `access-analyzer.*.amazonaws.com` matches
  `access-analyzer.us-west-2.amazonaws.com`, but not
  `access-analyzer.a.b.amazonaws.com`.
- A `*` after a literal prefix within a label matches one or more characters
  in that label: `api*.example.com` matches `api-east.example.com`, but not
  `api.example.com`, `api.a.example.com`, or `a.api-east.example.com`.
- The wildcard label needs at least two literal labels after it. This rejects
  `login.*.com` and `example.*` without the public suffix list; patterns
  under a public suffix such as `foo*.github.io` are accepted.
- IP literals (IPv4, or IPv6 in brackets or bare) are exact only, and a
  wildcard pattern cannot end in a numeric label (`*.0.0.1`).
- Patterns are stored in canonical form (`config.CanonicalPattern`):
  lowercase, no trailing dot, IPv6 without brackets.

| pattern                           | valid |
| --------------------------------- | ----- |
| `example.com`                     | yes   |
| `*.example.com`                   | yes   |
| `access-analyzer.*.amazonaws.com` | yes   |
| `api*.example.com`                | yes   |
| `web*-canary.example.com`         | yes   |
| `web.qa-*.internal.example.com`   | yes   |
| `foo*.github.io`                  | yes   |
| `10.0.0.1`                        | yes   |
| `*`                               | no    |
| `*.com`                           | no    |
| `login.*.com`                     | no    |
| `*.*.example.com`                 | no    |
| `*.api*.example.com`              | no    |
| `*example.com`                    | no    |
| `*-canary.example.com`            | no    |
| `ex*ample.com`                    | no    |
| `éxämple.com`                     | no    |
| `example.*`                       | no    |

- One validator and matcher in `internal/config/host.go`, used by
  `network.allow` at config load, policy rules, the rules table, DNS gating,
  the monitor's "add new allowlist item", `hsh proxy allow|deny`, and
  `monitordb.ValidHost`.
- `network.allow` is not validated today. An invalid entry becomes a load
  error naming the entry. The leading `*.` semantics match today's, so valid
  existing entries keep their meaning.
- Secret host binding (`HostCovered`) stays exact names plus `*.`: deciding
  whether `*.example.com` covers `api*.example.com` gets complicated.
- Tests: the table above, matching cases from the three bullets, IP literals,
  case and trailing dot normalization, the load error.

# Phase 3: Policy files and `hsh policy`

## Files

- `~/.config/hsh/policy/network/NAME.json`. The policy name is the file name
  without `.json`; names match `^[a-z0-9][a-z0-9-]{0,62}$`.
- Format version 1; unknown fields are an error, as in the config:

  ```json
  {
    "version": 1,
    "description": "Go toolchain and module proxy",
    "rules": [
      {"hosts": ["go.dev", "proxy.golang.org", "sum.golang.org", "dl.google.com"], "access": "allow"}
    ]
  }
  ```

- `description`: optional string.
- `recorded`: optional object written by plan 0049 (`image`, `command`,
  `runs`, `at`, `hsh`). Only `hsh policy verify` reads it; v1 accepts it so
  0049 needs no format change.
- `rules`: non-empty list. Each rule has a non-empty `hosts` list of valid
  patterns and at least one of `access`, `log`.

## Commands

| command | does |
| --- | --- |
| `hsh policy ls` | name and description of each policy |
| `hsh policy show NAME` | the file, pretty-printed |
| `hsh policy add NAME FILE\|-` | validates and writes the file; refuses an existing name without `--force`; prints the rules it adds |
| `hsh policy rm NAME` | removes the file; running sessions keep their copies |
| `hsh policy apply [--session ID] NAME` | copies the rules into the session as `source: policy:NAME`, replacing rows from an earlier apply of the same name |
| `hsh policy unapply [--session ID] NAME` | deletes the session's `policy:NAME` rows |
| `hsh proxy rules [--session ID]` | lists the session's rules with their source |

- `--session` defaults to this workspace's attached or only session
  (`resolveSession`).
- A policy rule with several hosts becomes one row per pattern.
- Docs: a "Network policies" section in `internal-docs/config.md`.
- Tests: format validation (unknown fields, empty rules, bad names, bad
  patterns), apply/unapply/reapply, deny from a policy beating a user allow,
  `functional.sh` add, apply and a denied request.

# Phase 4: Connection log, `report` mode, `hsh proxy report`

## Connection log

`proxy.db` schema version 2 adds:

```
connections(
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  time        TEXT NOT NULL,
  host        TEXT NOT NULL,
  port        INTEGER NOT NULL,
  scheme      TEXT NOT NULL,   -- tls | http
  access      TEXT NOT NULL,   -- allow | deny
  log         TEXT NOT NULL,   -- record | review
  pattern     TEXT,            -- the matching rule, NULL for the mode default
  source      TEXT,            -- config | user | policy:<name>
  held_ms     INTEGER NOT NULL,
  bytes_up    INTEGER NOT NULL,
  bytes_down  INTEGER NOT NULL,
  duration_ms INTEGER NOT NULL
)
```

- `held_ms`: how long the request waited for approval (0 when not held).
- `duration_ms`: from the request's arrival to its end, including `held_ms`.
- `id` only increases (`AUTOINCREMENT`), so it is a cursor for `--wait`.
- One row per connection or plaintext request. An allowed one is written when
  it ends. A denied one is written at once, before any hold, with zero bytes;
  when a hold ends the proxy updates its `access` (allow if approved while
  held), `held_ms` and `duration_ms`. Both the HTTP proxy and the netstack
  paths write it. DNS lookups are not logged.
- Capped at 50,000 rows; the oldest are deleted.
- Host-side readers treat it as untrusted, as they treat the `denied` table:
  hosts are validated with the phase 2 validator and invalid rows skipped.

## `report` mode

- `network.mode: report` and `hsh run --network report`: every host is
  allowed on every port and scheme unless a `deny` rule matches. Unmatched
  hosts get `log: record`, so nothing is queued or held.
- Bumps `config.Version`: an older hsh would otherwise reject the mode with a
  misleading "invalid network mode".
- The network notice is not written in `report` mode (nothing is blocked
  unless the user denied it).

## `hsh proxy report`

```
hsh proxy report [--session ID] [--format text|policy]
hsh proxy report --wait [--session ID] [--since ID] [--denied] [--timeout DUR] [--format text|json]
```

- `text` (default): one line per host with connection count, ports, schemes,
  bytes up and down, last seen, and whether it was allowed or denied.
- `policy`: a draft policy on stdout with one `access: allow` rule of the
  allowed hosts, sorted and deduplicated, for the user to edit before
  `hsh policy add`. Piping it straight into `hsh policy add` turns everything
  the agent contacted into a permanent allowlist; the docs say to review it
  first.

### `--wait`

For prototyping automated judges: block until the connection log has a new
entry, print the new entries, and exit.

- New means `id` greater than `--since ID`, or than the newest `id` when the
  command started. Each printed entry carries its `id`, so a judge loop passes
  the last one back as `--since` and misses nothing between calls. A `--since`
  older than the oldest kept row prints from the oldest.
- `--denied`: only entries with `access: deny` count and are printed.
- Polls `proxy.db` every 250 ms. Prints every new entry found in the poll
  that ends the wait, not only the first.
- `--timeout DUR`: exit status 2 with nothing on stdout when nothing new
  arrives in time. Without it, waits until interrupted (exit status 0, nothing
  printed).
- `--format text` (default): one line per entry with `id`, time, host, port,
  scheme, access, log, and the matching pattern and source. `--format json`:
  one JSON object per line with the `connections` columns and `session`. `--format policy`
  is an error with `--wait`.
- While waiting, it marks the session attended (`watchSessions`, as
  `hsh proxy allow --watch` does), so denied `review` requests are held
  (`network.approval_wait`) while a judge decides. Attendance stays fresh for
  `attend.MaxAge` (20 s), which covers the gap between calls in a loop.
- A judge acts on an entry with `access: deny` and `log: review` by running
  `hsh proxy allow` or `hsh proxy deny` for its host; a held request is then
  released or failed within the proxy's 250 ms poll.

A judge loop:

```
since=()
while out=$(hsh proxy report --wait --denied --session "$S" --format json "${since[@]}"); do
  # judge each line, then: hsh proxy allow|deny --session "$S" HOST
  since=(--since "$(tail -1 <<<"$out" | jq .id)")
done
```

- Tests: rows written for allowed and denied requests on both paths, the cap,
  untrusted rows skipped, `report` mode allowing everything but denies, all
  output formats; `--wait` returning on a new entry, `--since` with no gaps
  across calls, `--denied` skipping allowed entries, `--timeout` exit status,
  a held request visible to `--wait` before the hold ends, and a judge's
  `hsh proxy allow` releasing it.

# Future work

- Path and method rules (`paths: ["/go/*"]`) and a HAR-like request log need
  TLS interception: [0048-intercept-proxy.md](0048-intercept-proxy.md).
- A transforms log like iron-proxy's
  (https://github.com/paradigmxyz/iron-proxy#audit-log-format), recording
  for each request which steps ran (allowlist, secrets). Secret annotations
  only appear for hosts that are already intercepted and record secret names,
  never values:

  ```
  "request_transforms": [
    {"name": "allowlist", "action": "continue"},
    {"name": "secrets", "action": "continue",
     "annotations": {"swapped": [{"secret": "OPENAI_API_KEY", "locations": ["header:Authorization"]}]}}
  ]
  ```

# Out of scope

For the initial work the proxy should not become hard to configure.

- Built-in policies for popular services
- Fetching policies from a URL
- Global defaults in `settings.json` (for example a reporting level for every
  session)
- Referencing policies from configs or image labels
