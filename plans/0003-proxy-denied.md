# Proxy denial responses, denial log, and TOFU auto-allow

## Goals

1. The egress proxy answers every attempt to reach a non-allowlisted host
   with HTTP `407 Proxy Authentication Required` (instead of the current
   403), so a policy denial is distinguishable from an upstream 403 and
   reads as "the proxy refuses to carry this".
2. Every denial is recorded in a SQLite database that persists across
   sessions on the host, using `modernc.org/sqlite` (pure Go — keeps the
   `CGO_ENABLED=0` static build; `mattn/go-sqlite3` would break it).
3. `harnessh proxy-denied` lists the hosts denied and awaiting review.
4. `harnessh proxy-auto-allow` adds the denied hosts to a config file's
   `network.allow` and prints which host was added — trust on first use
   for containers: the first attempt is denied and recorded, review
   allows it.

## Usage flow (TOFU)

```
$ harnessh                          # inside the sandbox: curl https://foo.com → 407
$ harnessh proxy-denied
HOST      COUNT  FIRST SEEN            LAST SEEN
foo.com   1      2026-10-24T08:00:00Z  2026-10-24T08:00:00Z
$ harnessh proxy-auto-allow --config examples/config.harnessh
added foo.com
1 host(s) added, 0 already allowed
$ harnessh                          # curl https://foo.com now works
```

## Design decisions

### 407 for denials

- All egress denials — `CONNECT` and absolute-URI plain HTTP — return 407
  with a `Proxy-Authenticate: Harnessh realm="egress-allowlist"` header
  (RFC 7235) and a short body naming the host.
- curl surfaces it as `CONNECT tunnel failed, response 407`.
- The relative-URI "not a proxy request" case stays 400: it is not an
  egress attempt.

### Denial database

- Default location: `$XDG_STATE_HOME/harnessh/denied.db`, falling back to
  `~/.local/state/harnessh/denied.db`. Overridable with `--db` (CLI
  subcommands) / `--denied-db` (`run` and `proxy`).
- Opened in WAL mode with a busy timeout, so the proxy (writer) and the
  CLI (reader) can use it concurrently, including across simultaneous
  sandbox sessions.
- Schema:

  ```sql
  CREATE TABLE IF NOT EXISTS denied (
      host       TEXT PRIMARY KEY,
      target     TEXT NOT NULL,   -- host:port as requested
      count      INTEGER NOT NULL,
      first_seen TEXT NOT NULL,   -- RFC 3339, UTC
      last_seen  TEXT NOT NULL,
      allowed    INTEGER NOT NULL DEFAULT 0,
      allowed_at TEXT
  );
  ```

- Recording is an upsert on `host`: increment `count`, bump `last_seen`.
  Full history is kept; `allowed` marks hosts already handled.
- The database lives in the host state directory, bind-mounted read-write
  into the proxy container only. The workspace never sees the mount, so
  the agent cannot erase or forge its own denial record.
- Database errors are logged to stderr and never change the response: a
  denial is a denial with or without the log.

### `harnessh proxy-denied`

- Host-side, read-only open of the database. A missing database is not an
  error: it prints `no denied hosts recorded`.
- Lists pending rows (`allowed = 0`) as a table: host, count, first seen,
  last seen; sorted by count descending, then host.
- Flags: `--db PATH` (default as above), `--all` (include already-allowed
  hosts in the listing).

### `harnessh proxy-auto-allow`

- Flags: `--config PATH` (default `config.harnessh`), `--db PATH`.
- For every pending denied host:
  - not in the config's `network.allow` → append it and print
    `added <host>`;
  - already in the allowlist (allowed manually) → print
    `already allowed <host>`;
  - either way, mark the row `allowed = 1`, `allowed_at = now` so it
    leaves the pending review list.
- Prints a summary line: `N host(s) added, M already allowed`.
- The config file is rewritten as canonical JSON (2-space indent, keys
  sorted). Only `network.allow` changes semantically; other fields are
  carried through as raw JSON. The file's formatting and key order are
  normalized — a surgical text edit of the allow array was considered and
  rejected as fragile.
- Errors: missing config file or missing/empty database → clear message,
  nothing written.

### Wiring (`run` / `proxy`)

- `harnessh run --denied-db PATH` (default: XDG path). The host creates
  the state directory and bind-mounts it read-write into the proxy
  container at `/harnessh-state`, passing
  `--denied-db /harnessh-state/denied.db`.
- `harnessh proxy --denied-db PATH` — empty disables recording (the 407 is
  still returned).
- The proxy records in `deny()` for both CONNECT and plain-HTTP denials,
  storing the bare host (allowlist matching is host-based) and the full
  `host:port` target.

## File changes

- `internal/denieddb/denieddb.go` (new): default path resolution, schema,
  open (rw/ro, WAL), `Record(host, target)`, `Pending()`, `MarkAllowed(hosts)`.
- `internal/proxy/proxy.go`: 407 + `Proxy-Authenticate`, record on deny,
  new `DeniedDB` option.
- `internal/cli/proxy.go`: `--denied-db` flag.
- `internal/cli/run.go`: `--denied-db` flag, state dir creation + bind
  mount, pass-through to the proxy container.
- `internal/cli/proxy_denied.go` (new): `proxy-denied` subcommand.
- `internal/cli/proxy_auto_allow.go` (new): `proxy-auto-allow` subcommand.
- `internal/config/config.go`: `AddAllowedHosts(path, hosts)` helper
  (raw-JSON-preserving rewrite of `network.allow`).
- `main.go`: register the subcommands, extend usage.
- `go.mod` / `go.sum`: add `modernc.org/sqlite`.
- `docs/architecture.md`: 407 behavior, denial database (location,
  workspace isolation, schema), the two subcommands.
- `docs/config.md`: pointer to the subcommands for managing `network.allow`.

## Verification

1. `CGO_ENABLED=0 go build` still produces a static binary (modernc.org/sqlite
   is pure Go).
2. Sandbox run: `curl https://google.com` → 407, recorded.
3. `harnessh proxy-denied` → google.com listed with count 1.
4. Repeat the curl → count 2 (upsert works).
5. `harnessh proxy-auto-allow --config <test config>` → `added google.com`;
   the config contains google.com; the DB row is marked allowed.
6. `harnessh proxy-denied` → no pending hosts.
7. Sandbox rerun: `curl https://google.com` → allowed (real response).
8. Regression: allowlisted host + secret injection still work; the
   workspace cannot see or modify the database (no mount); concurrent
   sessions do not corrupt the DB (WAL).
9. Update docs; commit.

## Open questions

- 407 was chosen per the request; it is distinguishable from an upstream
  403 and matches "the proxy refuses to carry this". Changing it later is
  a one-line edit in `deny()`.
- `proxy-auto-allow` is non-interactive (allows all pending hosts). An
  `--interactive` per-host prompt could be a follow-up.
- History growth: rows are kept forever; a prune/VACUUM option can be a
  follow-up if it matters.
