# Subcommand Cleanup

> **Status: implemented.** `hsh proxy` (denied/allow) and `hsh monitor`
> replace the old subcommands; the egress-proxy role is argv[0]-only;
> DNS denials share the single `denied` table.

Some of the subcommands on `hsh` need some cleanup.

## Monitor

Replace `proxy-watch` with `monitor`- the idea is that the user will be using this to monitor all of their sessions and we may add further access control rules beyond the proxy later.

## Proxy

Replace `proxy-denied` and `proxy-auto-allow` with subcommands under `proxy` like `secrets`. The old subcommands will be removed with no backwards compat.

User can list all denied requests

```
hsh proxy denied
SESSION ID      WORKSPACE       URL
3ba5a9ed451f    ex-project      https://example.com
3ba5a9ed451f    ex-project      https://foobar.example.com
022073c3fea9    ex-research     https://pdf.example.com
```

User can filter the requests to a single session

```
hsh proxy denied --session 3ba5a9ed451f
https://example.com
https://foobar.example.com
```

User can allow all denied requests for a session

```
hsh proxy allow --all --session 3ba5a9ed451f
added https://example.com
added https://foobar.example.com
```

(All of these commands take a `--watch` flag to continuously output new denied requests or approve them.)

User can add a single URL to a single session

```
hsh proxy allow --session 3ba5a9ed451f https://example.com
added https://example.com
```


## DNS Proxy

The DNS policy for the allowlist mode is that the DNS proxy blocks all lookups
unless the name is in the allowlist. (Port and protocol columns will be
designed in a separate follow-up plan.)

DNS denials are recorded in the same `denied` table as egress denials —
there is no separate `dns_denied` table and no separate DNS policy: a
refused lookup and a refused CONNECT are both "the agent tried to reach
X". One review queue, one approval per host (approving a host fixes the
lookup and the egress together). A merged row is keyed by host+session,
so `count` aggregates denial events of either kind and `target` keeps
the first-seen form (`host:443` from a CONNECT, `name:53` from a
lookup); if the event-kind distinction is ever needed, a kind bit can
be added to the row later.

Migration: merge existing `dns_denied` rows into `denied` (bumping
counts on collision) and drop the table. `proxy denied --dns` and the
separate-table behavior go away; the TUI and `allow --all` operate on
the single queue.

## Decisions (recorded)

**The egress-proxy role is argv[0]-only; the `proxy` subcommand alias is
deleted.** Like `hsh-workspace`, the proxy lives only under its role
name: new pods already invoke `/hsh-proxy` (argv[0] dispatch), so
freeing the `hsh proxy` subcommand turns `proxy` into a purely
host-facing command group with no flag collision and no
disambiguation. Standalone proxy use (e.g. the functional test) invokes
it the way the container does — via argv[0].

Consequence, accepted by design: **pre-migration sessions become
unresumable.** Old pods' proxy containers exec `/hsh proxy --listen
...`; with the alias gone they hit the command group and fail. Users
`hsh rm` old sessions and start fresh — consistent with the
no-backwards-compatibility stance.

**The dead code that existed only for those old sessions goes too:**

- the main.go pre-map and with it the `proxy`, `workspace`, and
  `workspace-inner` subcommand aliases — once `proxy` is reclaimed, a
  reattach needs both containers of an old pod to work, so the
  workspace aliases preserve nothing
- `RunParent`'s legacy re-exec fallback (`--inner-exe` empty: re-exec
  self with the `workspace-inner` subcommand) — only old containers
  took that path; `--inner-exe` becomes mandatory

main.go becomes: argv[0] role dispatch (hsh-proxy, hsh-workspace,
hsh-workspace-inner) plus the command tree, nothing else.

**One `denied` table.** Egress denials and DNS denials share the
single `denied` table — there is no `dns_denied` table. A refused lookup
and a refused CONNECT are both "the agent tried to reach X": one review
queue, one approval per host. The migration merges existing
`dns_denied` rows into `denied` (bumping counts on collision) and drops
the table; `proxy denied --dns` and the separate-table behavior go
away. Separate port and protocol columns — for denials and for
allowlist entries — will be designed in a separate plan.


