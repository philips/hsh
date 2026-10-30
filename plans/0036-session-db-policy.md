# Session policy in session.db, not config.json

> **Status:** plan, awaiting review.

## Goal

Nothing but session creation writes a session's `config.json`. Everything the
user changes while a session runs (approved hosts, the approval-hold setting,
later secrets, report mode, imported policies) lives in
`$XDG_STATE_HOME/hsh/sessions/<id>/session.db`, which already exists as the
version event log.

## Why

Today `hsh proxy allow`, the monitor TUI, the inline review screen and
`hsh proxy hold` all rewrite the session's `config.json`
(`config.AddAllowedHosts`, `config.SetApprovalWait`). Problems:

- **Canonicalisation.** Config versioning (plan 0032) makes the editors refuse
  a file whose version they do not read, which closes the worst of the schema
  drift. Each mutation still reads the file as `map[string]json.RawMessage`,
  edits one key and re-marshals, so every approval rewrites the whole file
  (keys re-sorted, re-indented) for the sake of one host.
- **Lost updates.** The monitor TUI, an inline review relay and
  `hsh proxy allow --watch` are separate processes doing read-modify-write on
  one file with no lock. Two approvals landing together can drop one.
- **Change detection by bytes.** The proxy and `hsh run` both poll the file and
  compare bytes; any rewrite triggers a full reload, including a secrets
  re-resolve in `hsh run`.
- Every new feature on the TODO list (report mode, policies, report to policy)
  wants structured, queryable, per-session state that a JSON document edited in
  place is bad at.

## Design

### Effective configuration = base + overlay

- `config.json` is the base: written once at session creation from the seed
  (`--config` or the image label), never rewritten. Sessions created by older
  versions keep whatever allow entries they already have in it; no migration
  is needed because the overlay is purely additive.
- `session.db` holds the overlay. The effective config the proxy enforces is
  computed by one function, `config.Effective(base *Config, overlay Overlay)`,
  used by the proxy, `hsh run`, the TUI and the CLI so no caller merges by hand.

### Tables

Added next to the existing `events` table in `internal/sessiondb`. The schema
version lives in `PRAGMA user_version` (managed by `internal/dbschema`), not in
a table:

```
allow(host TEXT PRIMARY KEY, added_at TEXT NOT NULL, source TEXT NOT NULL)
  -- source: monitor | inline | cli | policy:<name>
settings(key TEXT PRIMARY KEY, value TEXT NOT NULL)
  -- approval_wait (row absent = use the base config)
```

Adding tables bumps `sessiondb.SchemaVersion` from 1 to 2, and that is
deliberate: an hsh that predates the overlay refuses a version 2 `session.db`
(`session.db has schema version 2, newer than this hsh reads; update hsh`)
instead of silently ignoring approvals. This is the first real schema bump, so
it needs the smallest possible migration step, see "Schema bump" below.
`config.Version` does not change: the `Config` struct is untouched.

Secrets and report tables are not part of this plan's first delivery (see
"Later"), but the layout leaves room: `secrets(name, from_ref, header, prefix)`
plus `secret_hosts(name, host)`, and a `report` table for the report-mode TODO.

Removing a host from the overlay is a `DELETE`; hosts that come from the base
config cannot be removed by the overlay (unchanged from today, where they could
only be removed by hand-editing).

### Package API

`internal/sessiondb` grows from `Append` into a small store:

```go
type Store struct{ db *sql.DB }
func Open(sessionDir string) (*Store, error)        // read-write, creates schema
func OpenReadOnly(path string) (*Store, error)      // for the proxy
func (s *Store) AddAllow(hosts []string, source string) (added []string, err error)
func (s *Store) Allow() ([]string, error)
func (s *Store) SetSetting(key, value string) error
func (s *Store) DeleteSetting(key string) error
func (s *Store) Settings() (map[string]string, error)
func (s *Store) Overlay() (Overlay, error)          // one transaction, consistent snapshot
```

`AddAllow` is a single transaction (`INSERT OR IGNORE`), so concurrent writers
cannot lose each other's rows, and the return value (rows actually inserted)
replaces the pre-check `applyAllow` does today. `Append` keeps its retry on
`SQLITE_BUSY`.

### Schema bump

`dbschema.Apply` today creates the schema for a new file and refuses any other
version than its own. Existing sessions have `session.db` at version 1 and must
keep resuming, so this plan adds the minimum:

- `dbschema.Apply` takes an optional ordered list of upgrade steps
  (`[]Step{{From: 1, SQL: "CREATE TABLE ..."}}`), run inside the same
  `BEGIN IMMEDIATE` transaction, each followed by the `user_version` bump. A
  version with no step to the current one is refused as today. `monitor.db` and
  `secrets.db` pass no steps, so their behaviour is unchanged.
- `dbschema.Check(db, name, version)`: read-only. Reads `user_version` and
  returns the newer/older error without taking a write lock. The proxy opens
  `session.db` through `OpenReadOnly`, which only calls `Check` (it cannot
  migrate on a `:ro` mount).
- A session whose `session.db` is still version 1 when the proxy starts: `hsh
  run` always opens it read-write first (`recordSessionVersion` on every
  entry), so it is upgraded before the proxy container is created. A detached
  session's already-running old proxy never reads the new tables and keeps
  enforcing the base config; approvals made after the upgrade start applying
  at the next `--resume`. Document that.

### Change detection

The proxy and `hsh run` keep a 1 s poll. Instead of comparing file bytes they
read `Overlay()` (tiny) and compare it to the last value, so an event-log
append does not trigger a reload. `config.WatchFile` stays for the base file
only if still needed, otherwise it is deleted.

### Extracting the config: `hsh config`

With the live policy split across two places, users need one command that
turns a session back into a config file:

```
hsh config [--session ID] [--base] [-o FILE]
```

- Prints the session's **effective** config (`config.Effective` of the base and
  the overlay) as indented JSON on stdout, or to `-o FILE` (mode 0600, refuses
  to overwrite an existing file without `--force`).
- The output is a valid seed: `hsh config --session ID > config.hsh` then
  `hsh run --config config.hsh` starts a new session with the approved hosts
  and the hold setting baked in. Mounts are already absolute and `image` is the
  resolved reference, so it is portable on the same host. Provenance
  (`allow.source`) is not kept; approved hosts are merged into `network.allow`
  in sorted order, base entries first.
- `--base` prints `config.json` exactly as created, without the overlay.
- Secrets appear as references (`env://X`, `hsh://NAME`) only. Values are never
  in the config, so the output is safe to commit.
- `--session` is resolved like `hsh proxy hold`: the flag, else the workspace's
  attached or only session. It reads state files only, so it works on stopped
  sessions and does not need podman beyond the session lookup.
- The command is read-only and opens `session.db` read-only, so it is safe to run while
  the session is live.
- Not to be confused with `hsh ssh config`, which prints an ssh_config
  snippet; the top-level name is free.

### Proxy access to session.db (the one real risk)

The proxy container mounts the session directory read-only at `/hsh-session`.
A WAL database cannot be read from a read-only mount (readers need to write the
`-shm` file), and `session.db` is WAL today. Phase 0 is a spike to pick one:

1. **Rollback journal for session.db** (`journal_mode=DELETE`). Readers on a ro
   mount work; the host writers create the journal in the writable directory
   and the proxy's `busy_timeout` rides out commits. Preferred: keeps the
   proxy's access read-only. Cost: switch the event-log opener too, and measure
   `SQLITE_BUSY` under the monitor TUI plus the proxy poll.
2. Mount the session directory rw for the proxy. Simple, but gives the
   proxy container (which parses untrusted agent traffic) write access to its
   own policy and CA material. Fallback only.
3. A separate `policy.db` file that the proxy gets rw. Rejected: the TODO asks
   for one `session.db`.

The spike result goes in this plan before phase 1 starts.

## Phases

### Phase 0 - spike

Verify option 1 on a real session: proxy container reading a DELETE-mode
`session.db` from a `:ro` bind mount while the host writes rows; no stale reads,
no sustained `SQLITE_BUSY`. Record the result here.

### Phase 1 - store and effective config

- `internal/dbschema`: upgrade steps and `Check`, with tests.
- `internal/sessiondb`: schema (`allow`, `settings`), `SchemaVersion = 2`, `Store`, tests
  (concurrent `AddAllow` from several goroutines and processes, idempotence,
  ro open via `dbschema.Check`, a version 1 file upgraded to 2 with its events
  kept, a version 3 file refused).
- `internal/config`: `Overlay` type and `Effective`; unit tests for merge order
  (base allow + overlay allow, `approval_wait` overlay beats base, absent
  setting falls through).
- `session.db` journal mode per phase 0.

### Phase 1b - `hsh config`

- `internal/cli/config.go`: `configCommand()` registered in `Root()`; flags
  `--session`, `--base`, `-o`, `--force`. Marshals with the same
  `json.MarshalIndent(cfg, "", "  ")` that `hsh run` uses to write the base.
- Ships before the writers move: with an empty overlay it prints the base, so
  it is useful immediately and its output stays correct as the overlay fills.
- Tests: round trip (extract, `config.Load` the result, equal to `Effective`);
  `--base` ignores overlay rows; overlay allow and `approval_wait` appear in
  the default output; `-o` refuses to clobber; no secret value can appear
  (assert against a session whose `secrets.json` holds a known value).

### Phase 2 - writers move to the store

- `internal/cli/proxy_group.go`: `applyAllow` / `allowHosts` use
  `Store.AddAllow`; `sessionConfigPath` becomes a session-dir check.
  "already allowed" is computed against `Effective`, so hosts in the base
  config still report as already allowed.
- `internal/tui/proxywatch/model.go`: `actionCmd` (approve) and `toggleHold`
  use the store; `loadHolds` uses `Effective`.
- `internal/cli/proxy_hold.go`: `SetApprovalWait` becomes
  `SetSetting("approval_wait", ...)`; `default` deletes the row.
- Delete `config.AddAllowedHosts` and `config.SetApprovalWait`.
- Update `internal/proxy/hold_test.go` helpers that call them.

### Phase 3 - readers move to Effective

- `internal/proxy/proxy.go` `reload` / `watch`: load the base config, read the
  overlay through `OpenReadOnly`, apply `Effective`. Add `--session-db` to the
  proxy flags and pass `/hsh-session/session.db` from `internal/cli/run.go`.
- `internal/cli/run.go`: `resume` loads the effective config; the secrets
  `WatchFile` goroutine watches the overlay for secret changes only (none until
  the secrets follow-up), so it keeps watching `config.json` for now and
  drops the allow-driven reloads. `hsh run` no longer logs "configuration
  reloaded" for approvals.
- `internal/tui/proxywatch` and `internal/cli/inline.go` pass the session dir
  rather than a config path where they only needed it for writes.

### Phase 4 - docs and tests

- `docs/config.md`: document `hsh config` (extract a session's effective
  config, `--base`, using the output as a seed) in "Sessions"; the session copy is write-once; approvals and `hsh proxy
  hold` are stored in `session.db`; fix the "Hot reload", "Holding denied
  requests", "Sessions" and "Managing the allowlist" sections (the last says
  `hsh proxy allow` rewrites the config copy as canonical JSON). The detach-key
  note about editing the session `config.json` stays accurate and stays.
- `docs/architecture.md`: the state-directory listing and the `session.db`
  paragraph (no longer append-only; list the tables); the `cfgcopy` diagram
  node labelled "the only writable config" becomes read-only after creation and
  the db gets the proxy mount.
- `test/functional.sh`: step 22 greps `session.db` for event text and is
  journal-mode sensitive (it reads `-wal`); extend it to assert that after
  `hsh proxy allow` the session's `config.json` is byte-identical and the host
  is in the `allow` table and enforced by the proxy within ~1 s. Existing
  steps that approve a host and then expect egress keep passing unchanged,
  which is the main regression check. Add a step that approves a host, runs
  `hsh config --session ID`, and asserts the host is in `network.allow` while
  `hsh config --base` does not list it.

## Later (separate plans)

- **Secrets**: `hsh secret add` currently edits the config file named by
  `--config` (default `./config.hsh`, the user's own seed file), which has the
  same drift problem. Move to a `secrets` overlay table and a `--session` flag,
  and make the seed file strictly user-owned. Needs a decision on whether
  `hsh secret add` without a session should still write a seed file.
- **Report mode, report to policy, toggle report/allowlist**: tables in the same
  db (see TODO).
- **Report mode tables** may land in `monitor.db` (global, per-session
  attributed rows) rather than `session.db`; the TODO says `session.db`, to be
  settled when that plan is written.

## Open questions

1. Is overlay-only removal acceptable (a base-config host cannot be revoked
   from the monitor), or should the overlay also carry denies?
2. Should `hsh config` also be able to print the overlay alone (a diff-like
   `--overlay`), which is what `hsh policy add` from the TODO would consume?
   Left out until the policy work needs it.
3. Should `hsh proxy allow` on a session created before this change copy its
   existing config.json allow list into the table, or just leave it in the base
   as planned? The plan leaves it; the cost is that `hsh proxy allowed` (if we
   add a listing) must read both.
4. An already-running proxy from before the upgrade keeps the base policy until
   the session is resumed (see "Schema bump"). Acceptable, or should `hsh run`
   refuse to attach to a session whose proxy predates the overlay?
