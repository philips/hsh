# Subcommand Cleanup

Some of the subcommands on `hsh` need some cleanup.

## Monitor

Replace `proxy-watch` with `monitor`- the idea is that the user will be using this to monitor all of their sessions and we may add further access control rules beyond the proxy later.

## Proxy

Replace `proxy-denied` and `proxy-auto-allow` with subcommands under `proxy` like `secrets`.

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

User can add a single URL to a single session

```
hsh proxy allow --session 3ba5a9ed451f https://example.com
added https://example.com
```

All of these commands take a `--watch` flag to continuously output new denied requests or approve them.

## DNS Proxy

The DNS policy for the allowlist mode is that the DNS proxy blocks all lookups
unless the name is in the allowlist. (Which will be expanded to other protocols
and ports later).

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


