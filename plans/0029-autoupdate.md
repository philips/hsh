# `hsh update --auto`: background version check + inline self-update

> **Status: implemented.**

## Goal

`hsh update --auto` opts the install into automatic updates. From then
on, hsh periodically checks GitHub for a newer release in the
background — no daemon, throttled to roughly one check every few
hours across invocations — and the next time the user runs any hsh
command after a newer release has been seen, hsh performs the update
inline at command startup, printing exactly what `hsh update` would
have printed. No separate `hsh update` run is needed; `hsh update
--no-auto` (or `HSH_NO_UPDATE_CHECK=1`) turns it off.

Each session records which hsh versions created and re-entered it in
a sqlite database (`session.db`) in its session state directory, so
version-related behavior can be debugged against the exact binary
that ran — and so session-scoped data has a place to grow.

## Background: what replacing the binary actually does to running sessions

`hsh run` bind-mounts the host binary into the pod
(`-v <exe>:/hsh-proxy:ro`, `/hsh-workspace`, `/hsh-workspace-inner`)
and uses those mounts as container entrypoints. Two facts drive the
design:

- A file bind mount pins the **inode**, and `hsh update` installs by
  `os.Rename` (a directory-entry swap, not an in-place write). A
  running pod therefore keeps executing the old inode; the rename
  cannot break a live session, its proxy, or its reaper.
- A **stopped** pod stores the mount *source path*, not the inode.
  When it is resumed, podman re-binds from the path, so the resumed
  session runs the **new** binary against a pod whose state directory
  and config were written by the old version.

Consequences for this feature:

- `hsh update` needs no session handling at all. It neither blocks,
  prompts, nor stops anything: live pods keep the old inode and are
  untouched, stopped pods pick up the new binary on resume (resume
  compatibility is a maintained goal, plan 0026), and new sessions
  get the new version from the start.
- Sessions may therefore span versions (an old session resumed by a
  new binary, or a session created just before an update). To make
  that debuggable after the fact, each session records the versions
  that created and re-entered it in `session.db`, a per-session
  sqlite database (below) chosen over another flat file so it can
  carry more session facts later without new file formats.

## Design

### No daemon: check on invocation, throttled

hsh has no long-lived daemon process, so "every few hours" is
approximated across invocations. Every **host-side** command (`hsh
run`, `ls`, `rm`, `secret`, ... — anything dispatched through
`cli.Run`; the container roles `hsh-proxy`/`hsh-workspace`/`hsh-
reaper` never reach this code) does two things at startup:

1. **Update.** Read the last recorded latest release tag from state;
   if it is newer than the running binary, run the update inline
   before the command's own work: print the notice, then perform the
   exact flow `hsh update` performs — download, verify, self-replace,
   metrics report (plan 0028) — so the output is what `hsh update`
   would have printed. Comparing at print time against the *current*
   binary means a release applied out-of-band (install.sh, a package
   manager) self-clears. A failed inline update (network, checksum,
   unwritable install dir) prints the error as a warning and the
   command proceeds unchanged; best-effort, never a failed command.
2. **Check.** If the last check is older than the interval (default
   **4h**, minimum 1h, override with `HSH_UPDATE_CHECK_INTERVAL`,
   e.g. `2h`), spawn a **detached child** to perform the check. The
   child is `setsid`-detached exactly like the reaper, so short-lived
   parents (`hsh ls`) do not cancel it and long check latency (10s
   timeout) never delays the user's command.

Both steps run only when autoupdate is enabled — `hsh update --auto`
sets a flag row in state (below) — and are skipped entirely when
`HSH_NO_UPDATE_CHECK=1`. Both fail silent: state that cannot be read,
a DB that cannot be opened, a check child that fails to spawn — none
of it affects the command the user actually ran.

The child writes `last_check` immediately on start (before the network
call), so concurrent invocations cannot spawn a child per command for
the duration of one slow check; two racing children are harmless
(sqlite serializes the writes, same DB discipline as the session
counter).

### The check child: `hsh update --autoupdate-check`

The detached child is `hsh update --autoupdate-check` — a visible
flag, so the background step is debuggable by hand. It resolves the
latest tag with `update.Available()` (HEAD to
`<HSH_RELEASE_BASE>/latest`, same discovery as install.sh and `hsh
update`, 10s timeout, one attempt), records the result in state, and
exits without installing. Run by hand it prints what it found:

```
hsh v0.2.0 is available (this is v0.1.0): it installs on the next hsh command
hsh v0.1.0 is up to date
```

Run as the detached child (spawned via `os.Executable()` with argv
`["hsh-autoupdate", "update", "--autoupdate-check"]`), it prints
nothing; its stderr is discarded. A failed check (offline, 4xx/5xx,
malformed redirect) still records `last_check` — the interval, not
success, paces retry — and leaves the previously recorded tag alone,
so a transient failure never erases a pending release.

**What the child never does:** download an asset, verify a checksum,
touch the running binary, or POST the metrics report. A detached
background process must not replace the binary invisibly — the
install always happens inline, at a host command the user ran, with
the full `hsh update` output. The child's only network call is one
HEAD to github.com (or `HSH_RELEASE_BASE`), the same request
install.sh makes; at one per 4 hours it is far below any rate limit.

### Version comparison

Tags are `vX.Y.Z` (the release workflow stamps exactly this shape).
`Newer(current, latest)` compares numerically after stripping `v`;
anything malformed, or carrying a prerelease suffix, is never
"newer". A release build with a newer recorded tag updates inline as
described above. A **dev build** (`version.Tag == ""`) never updates
inline — replacing a binary a developer built on purpose would be a
surprise — it only gets the notice, every invocation until resolved:

```
hsh v0.2.0 is available (this is a dev build): run `hsh update` to upgrade
```

The one-line notice is the only recurring output: a pending release
triggers the inline update (its several lines) exactly once, and
nothing further prints until the next release. Deliberately not
capped at "once per version" — a user who ignores the dev notice for
a week should still see it the day they come back, and the
release-build case resolves itself on that next run.

### State: the existing metrics.db

Update-check state lives in the existing
`$XDG_STATE_HOME/hsh/metrics.db` (opened per operation, WAL +
busy_timeout, exactly like the counters). One new table alongside
`counters`:

```sql
CREATE TABLE IF NOT EXISTS kv (
  name  TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
```

Rows: `update_last_check` (unix seconds, written by the check child
before its network call), `update_latest_tag` (the tag, or empty),
and `update_auto` (`1` when `hsh update --auto` ran, removed by
`--no-auto`). `hsh update` — manual or inline — clears
`update_latest_tag` after a successful install, mainly so `hsh
version` and future tooling see a settled state; the print-time
comparison is what actually gates the inline update.

### Session database: `session.db`

`hsh run` maintains a sqlite database,
`$XDG_STATE_HOME/hsh/sessions/<session-id>/session.db`, next to
`config.json` and the CA files. It uses the same driver and
open-per-operation pattern as `denied.db` and `metrics.db`
(`modernc.org/sqlite`, WAL, busy timeout), so there is no
long-lived connection and no new machinery. The first table is an
append-only event log; version entries are the first events:

```sql
CREATE TABLE IF NOT EXISTS events (
  id      INTEGER PRIMARY KEY,
  time    TEXT NOT NULL,
  kind    TEXT NOT NULL,
  version TEXT NOT NULL
);
```

- One row per entry, creation or resume: `time` is RFC3339 UTC,
  `kind` is `created` or `resumed`, and `version` is
  `version.Get().String()` (tag, commit, Go, platform — exactly
  what `hsh version` prints).

  | time | kind | version |
  |---|---|---|
  | `2026-11-12T10:31:05Z` | `created` | `hsh v0.1.0 (commit abc123, go1.24.4, linux/amd64)` |
  | `2026-11-13T09:02:11Z` | `resumed` | `hsh v0.2.0 (commit def456, go1.25.0, linux/amd64)` |

- The log answers both "which version created this session" and
  "which versions have since re-entered it" — the version-skew
  questions that an in-place update makes possible.
- Future session facts (timings, pod id, exit status) become new
  tables or new `kind`s in the same database instead of more
  single-purpose files.
- Best-effort: a write failure (unwritable state dir) is a one-line
  warning, never a failed session. The database is removed with the
  rest of the session directory by `hsh rm` — no extra cleanup.
- Container-role entrypoints never write it; it records host-side
  `hsh run` versions, which is the binary the pods were built from.

### Long-running sessions

A multi-hour `hsh run` is a single invocation, so the
check-on-invocation rule alone would never fire during it. `hsh run`
adds one ticker goroutine alongside its existing config-watcher: it
re-evaluates "is a check due?" every 15 minutes and spawns the same
detached `hsh update --autoupdate-check` child when it is. No update or
notification ever happens mid-session — the inline update only runs
at command startup — so a long session is never disturbed, and its
pods never swap binaries underneath it; the update lands at the
next invocation, which is exactly the requested behavior.

## Implementation

- `internal/update/check.go`:
  - `Newer(current, latest string) bool` — strict `vX.Y.Z` numeric
    compare; anything else → false.
  - `CheckDue(path string, interval time.Duration) bool` — reads
    `update_last_check`, true when absent or older than `interval`.
  - `MarkCheck(path)` — writes `update_last_check` (now), called
    before the network call so the throttle window closes during a
    slow check.
  - `SetLatest(path, tag)` / `KnownLatest(path) string` — write and
    read `update_latest_tag` (missing DB/row/corrupt → `""`, no
    error — same best-effort contract as the counter);
    `ClearLatest(path)` removes the row after an install.
  - `AutoEnabled(path) bool` / `SetAuto(path, on bool)` — read and
    set/clear the `update_auto` row.
- `internal/update/counter.go`: the schema exec grows the `kv` table;
  `openCounter` stays, `RecordCheck`/`KnownLatest` share its open
  pattern.
- `internal/cli/update.go` — the update command grows three flags,
  and its exec body is factored into `runUpdate(opts) error`
  (resolve tag, notice, download/verify/replace, metrics report,
  clear `update_latest_tag`) shared by the plain `hsh update` path
  and the inline auto path:
  - `--auto` / `--no-auto` — set/remove the `update_auto` row,
    print the new state, and return without updating (an auto flag
    never triggers an install in the same invocation).
  - `--autoupdate-check` — the child's mode: read env
    (`HSH_RELEASE_BASE`), `update.Available`, `RecordCheck`, print
    the result line, exit 0 — exit 0 even when the check fails
    (print the error; the child must not produce nonzero exits for an
    offline host).
- `internal/cli/autoupdate.go` (helpers only; no new subcommand):
  - `spawnUpdateCheck()` — exec `os.Executable()` with argv
    `["hsh-autoupdate", "update", "--autoupdate-check"]`, `Setsid:
    true`, stdout/stderr to `/dev/null`, `Start` without `Wait`
    (reaper-style), errors swallowed.
  - `maybeAutoUpdate()` — only if `AutoEnabled` and not
    `HSH_NO_UPDATE_CHECK=1`: read `KnownLatest`, compare with
    `version.Get()`; newer + release build → print the notice, call
    `runUpdate`, downgrade any error to a warning line; newer + dev
    build → the dev notice only.
  - `maybeSpawnUpdateCheck()` — same gating, read interval from
    `HSH_UPDATE_CHECK_INTERVAL` (default 4h, values below 1h
    clamped), `CheckDue` → `spawnUpdateCheck()`.
- `internal/cli/root.go` `Run()`: split `ParseAndRun` into
  `Parse` + run so the selected command is known before execution;
  after a successful parse, unless the selected command is `update`
  itself (it already covers installing and checking), call
  `maybeAutoUpdate()` then `maybeSpawnUpdateCheck()` (both
  best-effort, never fail the command), then execute.
- `internal/cli/run.go`:
  - the ticker goroutine — every 15m call
    `maybeSpawnUpdateCheck()`; started next to the config watcher,
    stopped with it.
  - `sessiondb.Append(sessionDir, kind string) error` — new package
    `internal/sessiondb` (schema + one INSERT, `modernc.org/sqlite`,
    open-per-operation like denieddb); called from both the
    new-session and `--resume` paths with `kind` `created`/`resumed`,
    recording `version.Get().String()`; errors → one warning line.

## Tests

Unit (go test):

- `Newer`: `v0.2.0` > `v0.1.9`, equal tags → false, malformed /
  prerelease latest → false, empty current (dev) → true.
- `MarkCheck`/`SetLatest`/`KnownLatest`/`CheckDue`/`SetAuto`/
  `AutoEnabled` round trip against a temp state dir; missing DB →
  zero values; corrupt DB → zero values without error; clamped
  interval floor.
- `maybeAutoUpdate`: enabled + pending newer tag + release build →
  notice on captured stderr and `runUpdate` invoked; equal tags or
  not enabled → silence and no update; dev current → the dev notice
  and no update; a `runUpdate` error → warning, not a failed
  command; `HSH_NO_UPDATE_CHECK=1` → silence and no update.
- `SetAuto`/`AutoEnabled`: row set and removed; missing DB → false.
- `sessiondb`: two `Append` calls → two `events` rows in id order
  with the right `kind` and `version` values and parseable RFC3339
  `time`; a fresh session dir gets the schema created on first
  write; an unwritable directory → error (the CLI downgrades it to a
  warning); a corrupt `session.db` file surfaces as an error, not a
  panic.
- functional-shape test for the check path: point
  `HSH_RELEASE_BASE` at an httptest server, run the
  `--autoupdate-check` exec function directly (not via fork) and
  assert the server saw the HEAD and the kv rows landed; a failing
  server → exit 0 with the error printed.

Functional (test/functional.sh, extending the fake-release harness
from plan 0028):

1. Opt-in: by default nothing happens — no spawn, no notice, no
   inline update. `hsh update --auto` sets `update_auto` and prints
   the confirmation; `hsh update --no-auto` clears it.
2. Full flow: `HSH_RELEASE_BASE` at a server with tags v1.0.0 and
   v2.0.0; binary built as v1.0.0, autoupdate enabled: `hsh update
   --autoupdate-check` records v2.0.0 and prints the available
   line; the next host command (`hsh version`) prints the notice
   and installs inline — the installed binary's checksum matches
   the fake v2.0.0 asset, `✓ hsh v2.0.0 installed (was v1.0.0)`
   appears on stderr, the metrics POST (at the
   `HSH_METRICS_URL` test endpoint) fired with the taken session
   counter, `update_latest_tag` is empty, and a further command
   prints nothing.
3. Throttle: server counts HEAD hits; two immediate invocations
   spawn one child; with `HSH_UPDATE_CHECK_INTERVAL=1h` nothing
   spawns.
4. `HSH_NO_UPDATE_CHECK=1`: no spawn, and a recorded newer tag does
   not trigger the inline update.
5. Inline failure: record v2.0.0, then take the release server
   down; the next host command prints the warning line, succeeds,
   and the binary is unchanged; a later successful check lets the
   inline update proceed.
6. Session database: a new session's directory contains
   `session.db` whose `events` table has one `created` row matching
   the binary's version output; `hsh run --resume` on that session
   appends a `resumed` row; `hsh rm` removes it with the rest of the
   session directory. Run one session, let the inline update to the
   fake v2.0.0 land, then resume: the appended row records the new
   version — the debuggability this database exists for.
7. Up-to-date path: latest == current → `hsh update
   --autoupdate-check` prints the up-to-date line, no notice and no
   inline update on the next invocation.

## Docs

- docs/config.md, "update" section: add the autoupdate behavior —
  `hsh update --auto`/`--no-auto` as the opt-in/out, the background
  check (what it sends: one HEAD per interval, and only when
  enabled), the inline update at the next command startup (full
  `hsh update` semantics including the metrics notice and report,
  best-effort on failure), `--autoupdate-check`, `HSH_NO_UPDATE_CHECK`,
  `HSH_UPDATE_CHECK_INTERVAL`, and the `kv` rows in metrics.db.
- docs/architecture.md: the session-state-directory inventory gains
  `session.db` (the schema, what it records, and when rows are
  appended); the lifecycle section gets one paragraph on the
  background check and inline install — host commands (when
  autoupdate is enabled) spawn the detached check child and apply
  pending updates at startup, container roles never do, and the
  rename-vs-bind-mount semantics above, since they explain what an
  update can and cannot disturb.
- TODO.md: add an entry for this feature; remove it when implemented.
