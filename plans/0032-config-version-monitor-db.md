# Config versioning, and denied.db becomes monitor.db

> **Status: implemented** (phases 1 to 3; phase 4, rebuilding and publishing the
> harness images, happens in the release workflow). Replaces two entries in `TODO.md` ("config
> versioning" and "denied.db to monitor.db"), which are removed when this is
> done. There is no backwards compatibility: hsh has one user, so old configs,
> image labels, sessions, and `denied.db` are not migrated; they are rebuilt,
> recreated, or deleted.

## Goal

1. hsh never acts on a config it does not understand. A config, session
   config copy, or image label written for a different schema is refused with
   a message that says what to do, instead of having unknown fields silently
   ignored (today `json.Unmarshal` drops them, so a newer image's
   `network.notice`-style safety field is simply not applied).
2. The denial database is named `monitor.db`, matching `hsh monitor`, and is
   a general home for monitor state, not only the denial queue. It carries a
   schema version so a binary that does not understand it refuses it.

The two changes are in one plan because both are "refuse state written for a
schema this binary does not know", and because both touch the state-directory
helpers that live in the misnamed `internal/denieddb` package.

## Where versions matter today

- **Image labels** (`org.ifup.hsh`, from `images/*/hsh.json`): baked into
  images that outlive the hsh that built them. A newer image on an older hsh
  is the main case.
- **Session config copies** (`<session dir>/config.json`): written by the hsh
  that created the session and re-read by every later `hsh run --resume`,
  `hsh monitor`, `hsh proxy allow`, and by the session's proxy.
- **The proxy binary can be older than the host binary.** The proxy runs the
  hsh binary that was bind-mounted when the proxy container was created. A
  detached session that outlives an `hsh update` keeps reloading its config
  with the old code.
- **Seed config files** passed with `--config`.
- **Config editors** (`AddAllowedHosts`, `SetApprovalWait`, `AddSecret`)
  rewrite the file through a `map[string]json.RawMessage`, so they preserve
  unknown fields; they must not rewrite a file they do not understand.
- `monitor.db` is opened by `hsh run`, `hsh monitor`, `hsh proxy`, and by
  every session's proxy container at the same time.

## Design

### 1. `version` in the config

A required top-level integer, the same field in all three places:

```json
{ "version": 1, "image": "...", "network": { ... } }
```

- `config.Version` is the schema this binary reads and writes (1 today).
  `config.MinVersion` is the oldest it still reads (also 1: nothing is
  migrated). Keeping both lets a later bump keep reading the previous schema
  by adding a migration and leaving `MinVersion` where it is.
- `Config` gets `Version int` (`json:"version"`); `Default()` sets it, so the
  session copy `hsh run` writes carries it and `ParsedMounts` and friends are
  unaffected.
- **Required, not defaulted.** A missing version is an error. Defaulting to
  the current version would make a bump ambiguous for unversioned files.
  `images/*/hsh.json`, the Containerfile labels in `test/`, and the literals in
  tests and `test/functional.sh` gain `"version": 1`.

### 2. Reading: version first, then strict decoding

One internal function `decode(data []byte, source string) (*Config, error)`
behind `Load` and `FromImageLabel`:

1. Parse only `{"version": N}`. Errors, with `source` being the file path,
   `the org.ifup.hsh label of IMAGE`, or `session ID`:
   - missing: `"<source>: no \"version\" field (this hsh reads version 1)"`
   - `N > Version`: `"<source>: config version N is newer than this hsh
     reads (version V); update hsh (hsh update)"`
   - `N < MinVersion`: `"<source>: config version N is no longer supported
     (this hsh reads version V); regenerate it"` plus, for a label,
     `rebuild the image`, and for a session, `hsh rm` it
2. Decode into `Config` with `DisallowUnknownFields`. A typo such as
   `"approval_wiat"` or `"allw"` becomes an error naming the field instead of
   a silently open or closed policy. Nested structs are checked too
   (`network`, `agent`, `monitor`, `oauth`, `secrets` entries).
3. Run the existing validation (`ValidateMode`, `ValidateApprovalWait`,
   secret references).

`hsh run` already surfaces these errors for the seed and for the label, and
for a resumed session adds `(remove orphaned session state with: hsh gc)`;
that hint is replaced by the message above when the cause is the version.

### 3. Writing: editors check first

`AddAllowedHosts`, `SetApprovalWait`, and `AddSecret` call the same version
check on the file's bytes before editing, and return the same error. Their raw
map editing stays, so a file they accept round-trips. (Replacing config edits
with `session.db` tables is a separate TODO entry and out of scope.)

### 4. The proxy keeps running on a version it cannot read

`reload()` already logs `configuration reload failed` and keeps the previous
in-memory state when `config.Load` errors. That is the right behaviour for a
proxy older than its config (it keeps enforcing the last policy it
understood). Add a log line that says why in the version case (it comes with
the new error text), and a unit test that a config bumped to
`Version+1` leaves the allowlist unchanged and logs the failure. The startup
load in `hsh proxy` fails hard, as today.

### 5. Bump rule, and a test that enforces it

**Any change to the set of config fields bumps `config.Version`** (added,
removed, renamed, or changed meaning). With strict decoding an older hsh would
reject a config using a new field anyway; bumping makes the message say "newer
hsh" instead of "unknown field". The cost is that images and configs are
regenerated on each bump, which is cheap for one user.

`internal/config/schema_test.go` makes this hard to forget: it walks
`Config` with `reflect` (struct fields by their `json` tag; `map[K]V` as
`name[*]`; slices as `name[]`; pointers followed) and compares the sorted list
of paths to `testdata/schema-v<Version>.txt`. If the list differs, the test
fails with `config schema changed: bump config.Version and add
testdata/schema-v<N>.txt`. When the version is bumped the old golden file is
deleted (git has it).

### 6. `monitor.db`

Rename and generalise:

- **Package.** `internal/denieddb` becomes `internal/monitordb`. The state
  directory helpers that do not belong to a database (`StateDir`,
  `SessionsDir`, `SessionDir`) move to a new `internal/state` package, so
  `cli`, `tui`, and `attend` stop importing a database package to find the
  sessions directory. `monitordb.DefaultPath()` is `<state>/monitor.db`.
- **File and flags.** `denied.db` becomes `monitor.db`. Flags and options are
  renamed with no aliases: `hsh run --denied-db` and `hsh proxy --denied-db`
  become `--monitor-db`; `proxy.Options.DeniedDB` becomes `MonitorDB`; the
  `--db` flag help on `hsh monitor`, `hsh proxy denied`, and `hsh proxy allow`
  says "monitor database path". The container side is
  `/hsh-state/monitor.db`.
- **Tables.** The `denied` table keeps its name and columns (it is the denial
  queue). Other tables arrive with the features that need them (for example
  the report mode in `TODO.md`); the file name no longer implies denials only.
- **No migration.** The `migrate`, `rebuild`, `mergeLegacyDNS`, and
  `ensureHoldingColumn` code and their tests are deleted, and the schema is
  the single `CREATE TABLE` statement. An existing `denied.db` is left alone
  and unused; delete it by hand. Pending denials in it are lost.
- **Schema version.** `PRAGMA user_version` holds `monitordb.SchemaVersion`
  (1). `Open` calls the shared `dbschema.Apply`, which runs one
  `BEGIN IMMEDIATE` transaction (the unused read-only open was deleted):
  - `user_version = 0` (a new file): create the schema, set
    `user_version = SchemaVersion`.
  - `user_version = SchemaVersion`: continue.
  - greater: `monitor.db has schema version N, newer than this hsh reads
    (version V); update hsh`.
  - lower (no migrations exist yet): `monitor.db has schema version N, older
    than this hsh reads (version V); remove PATH`.

  A file with version 0 may already hold the tables (an existing
  `session.db` or `secrets.db`): the `CREATE TABLE IF NOT EXISTS` schema is a
  no-op for it and the version is just stamped, so existing data is adopted,
  not lost.

  The immediate transaction makes concurrent first opens (several session
  proxies and a monitor starting together) safe. A schema change to
  `monitor.db` bumps `SchemaVersion` and carries a migration, or says to delete
  the file, in the same way as the config bump rule.
- **Shared helper.** The check is `internal/dbschema.Apply(db, name, path,
  version, schema)`, used by `monitor.db`, `session.db`, and `secrets.db` (see
  Decisions). Each package owns its `SchemaVersion` constant and schema text.
- **Proxy.** A proxy that cannot open the database fails to start, as it does
  for other open errors today (`open monitor database: ...`); this is the
  case of an old proxy binary and a database a newer hsh upgraded, and its
  message tells the user what to do.

## Changes by area

- `internal/config`: `Version`, `MinVersion`, `VersionError`, `decode`, strict
  decoding, the editor checks, `schema_test.go` and its golden file (refresh
  with `go test ./internal/config -run TestSchemaMatchesVersion
  -update-schema`).
- `internal/dbschema` (new), `internal/secretstore`, `internal/sessiondb`: the
  shared `user_version` check.
- `images/*/hsh.json`, `images/build.sh` (unchanged; the label comes from the
  JSON), and the label and config literals in tests and `test/functional.sh`:
  add `"version": 1`.
- `internal/state` (new) and `internal/monitordb` (renamed): as above; callers
  in `internal/cli` (`run`, `monitor`, `proxy`, `proxy_group`, `sandbox`,
  `gc`, `detach`, `attended`, `inline`, `oauth`), `internal/proxy`, and
  `internal/tui/proxywatch`.
- `docs/config.md` (add `version` to the example and the fields table, the
  required-version and strict-field rules, the bump rule; rename the database
  paragraph), `docs/images.md` (the label carries `version`),
  `docs/architecture.md` (the "Denial database" section becomes the "Monitor
  database", drops the migration paragraph, gains the schema version; diagram
  node and mount names), `docs/testing.md`.
- `TODO.md`: remove the two entries.
- `test/measure/agent-trial.sh`: `--denied-db` and the `denied.db` path.

## Tests

- Config: a table for `decode` (missing, too new, too old, current, unknown
  top-level field, unknown nested field, wrong type) for the file, label, and
  session sources, with the exact messages; the schema golden test; each editor
  refuses a future-version file and leaves it byte-for-byte unchanged;
  `Default()` and the session copy written by `hsh run` carry `version`.
- Labelcheck: every `images/*/hsh.json` has the current version and decodes
  strictly.
- Proxy: reload with a config at `Version+1` keeps the old allowlist and logs
  the failure; startup with an unreadable config fails.
- `monitordb`: a new file gets `user_version = 1` and the table; a database
  with a newer version is refused with the update message, an older nonzero one
  with the remove message; two processes (goroutines with separate handles)
  opening a new path at once end with one schema and no error; the existing
  `denied` tests move with the package.
- Functional (step 26 and an addition to step 1): step 1's session writes
  `monitor.db` and no `denied.db`; a seed
  config without `version` and one with `version: 99` fail `hsh run` with the
  messages above and create no session; a session config edited to
  `version: 99` makes `hsh run --resume` and `hsh proxy allow` fail and leaves
  the file unchanged, while the running proxy keeps enforcing (a denied host
  stays denied after the edit); an image whose label has `version: 99` fails
  `hsh run IMAGE`.
- All existing functional steps change `--denied-db` to `--monitor-db` and
  their embedded labels and configs to carry `version`.

## Phases

1. **Config versioning**: `version`, `decode`, strict decoding, editor checks,
   the schema test, labels and test literals, config and proxy tests.
2. **Rename**: `internal/state`, `internal/monitordb` (with the migration code
   deleted and `user_version`), flags and options, callers, and the monitordb
   tests.
3. **Functional and docs**: the functional steps above, then
   `docs/config.md`, `docs/images.md`, `docs/architecture.md`,
   `docs/testing.md`, and `TODO.md`.
4. **Images**: rebuild the five harness images so their labels carry
   `version`; `images/build.sh` needs no change.

Phases 1 and 2 are independent and can land as separate commits; phase 3 needs
both.

## Security

Strict decoding and the version check fail closed: a config the binary does not
understand is never partially applied. A running proxy keeps its last readable
policy rather than dropping to a default. The version is advisory against a
hostile writer (the session directory is not writable from the workspace;
nothing about that changes), so the checks protect against skew and typos, not
against someone who can already edit the host's files.

## Decisions

- `hsh run` ignores a leftover `denied.db` next to `monitor.db`. There is only
  one user of this code so the leftover file doesn't matter.

- `session.db` (the event log) and the secret store get the same
  `user_version` check. A second database needed it, so the version handling
  is the shared `internal/dbschema` helper rather than code inside `monitordb`.
  An existing, unversioned `session.db` or `secrets.db` is adopted (stamped with
  version 1, data kept); this was checked against a copy of a real
  `secrets.db`.

- No migration framework (numbered upgrade steps run on open) is built before
  the first real bump. This plan keeps `MinVersion` and the structure for it and
  builds nothing else: an older version is refused with an instruction to
  regenerate or remove it.

## Implementation notes

- `config.VersionError` carries the kind (missing, newer, older), so callers
  add the right hint: `FromImageLabel` appends `rebuild the image` for a
  missing or older label, and `hsh run --resume` appends `remove the session
  with: hsh rm ID` for a stale session config instead of the `hsh gc` hint.
- The seed, session, and label share one `decode`; the version is checked
  before strict decoding so a newer file reports "newer", not "unknown field".
- The functional step is number 26 (25 was taken upstream).
