# `hsh update`: self-update + anonymous metrics report

> **Status: implemented.**

## Goal

`hsh update` replaces the running `hsh` binary in place with the latest
release from GitHub (`github.com/philips/hsh/releases`), using the same
download/verify/install rules as install.sh. As part of an update, hsh
posts an anonymous metrics report so the project can see how much the
released binary is used. The user is told, before anything is sent,
exactly what is uploaded: version, platform, and the number of sessions
created since the last update — and that no identifiable information is
uploaded. Downloads come from GitHub releases; the only other network
call is the metrics webhook.

## Design

### Discovery, download, and verification: same rules as install.sh

install.sh resolves the newest tag by following the `releases/latest`
redirect (no API call, no rate limit), downloads
`hsh-linux-<amd64|arm64>` plus `SHA256SUMS`, verifies the checksum, and
installs to `~/.local/bin`. `hsh update` mirrors all of it:

- Base URL: `https://github.com/philips/hsh/releases`, overridable with
  `HSH_RELEASE_BASE` (for testing against a staging bucket).
- Version: newest tag from the `latest` redirect, or `--version v1.2.3` /
  `HSH_VERSION` to pin (flag wins).
- Asset: `hsh-linux-<GOARCH>` where GOARCH is `amd64` or `arm64`; any
  other arch errors out (`hsh releases cover linux/amd64 and
  linux/arm64`).
- SHA-256: download `SHA256SUMS` from the same release, find the line
  for the asset, compare against the local file's digest; mismatch is a
  hard error, and the temp download is removed.

### Install location: where the running binary lives

install.sh owns the location, `hsh update` inherits it: the binary
updates itself where it already is, discovered with `os.Executable()`
followed by `filepath.EvalSymlinks` (a symlink on PATH updates the real
target). This means a binary installed by install.sh updates
`~/.local/bin/hsh` without any configuration.

Self-replace sequence:

1. Download and verify into a temp file **in the same directory** as
   the target (same filesystem, so the final step is an atomic rename).
2. `chmod 0755` the temp file.
3. `os.Rename` over the running binary. On Linux this is allowed even
   while the old process runs; the running process keeps the old inode.
4. On a permission error writing that directory, fail with the
   install.sh-style guidance ("could not write to <dir>; hsh update
   only manages an existing install — re-run install.sh, or set
   HSH_INSTALL_DIR for a writable location").

`hsh update` never rewrites shell rc files or PATH — that is
install.sh's job on first install, and an existing install already has
it.

### Metrics report

After a successful update, hsh POSTs a single JSON document to the
project webhook:

```
POST https://infinitelogic.app/o/hsh-home/hooks/metrics_report
Content-Type: application/json

{"product": "hsh", "version": "0.1.0", "platform": "linux/amd64", "sessions_since_last_update": 38}
```

Fields:

| field | value |
|---|---|
| `product` | `"hsh"` |
| `version` | the version that was running **before** the update (the version those sessions were used with) |
| `platform` | `GOOS/GOARCH` (e.g. `linux/amd64`), same string as `hsh version` |
| `sessions_since_last_update` | sessions created since the previous update's report, as an integer |

Rules:

- **Notice first.** Before the download starts, print one block:
  `hsh update reports anonymous usage metrics to the project:
  version, platform (linux/amd64), and the number of sessions created
  since the last update. No identifiable information is uploaded.
  Skip with --no-metrics or HSH_NO_METRICS=1.` Nothing is sent when the
  flag/env is set, and the notice is not printed either.
- No other data is attached: no hostname, no user name, no workspace
  paths, no session ids, no timestamps.
- Upload failures (offline, 4xx/5xx, timeout) print a one-line warning
  and do not fail the update; the session counter is still reset —
  the report is best-effort, not a transaction.
- HTTP client timeout: 10s for metrics, 30s for GitHub downloads.

### Session counter: "sessions created since the last update"

A sqlite database in the existing state directory (`~/.local/state/hsh`,
respecting `XDG_STATE_HOME`, same convention as the denied-hosts DB):
`~/.local/state/hsh/metrics.db`, opened with the same
`modernc.org/sqlite` driver. Unlike a JSON file it needs no
read-modify-write of the whole document, and later counters/facts
about hsh usage grow the same file instead of adding more single-
purpose files:

```sql
CREATE TABLE IF NOT EXISTS counters (
  name  TEXT PRIMARY KEY,
  value INTEGER NOT NULL DEFAULT 0
);
```

- `hsh run` increments the `sessions_since_last_update` row whenever
  a new session is created — the two `randHex(6)` call sites in run.go
  are factored into one `newSessionID()` helper that bumps the
  counter; `--resume` re-entries do not count.
- `hsh update` reads the count, resets the row to zero after the
  report, and the report carries the pre-reset value.
- Counter read/write is best-effort: a missing row counts as zero
  (an insert-on-bump creates it), a database that cannot be opened or
  is corrupt is treated as zero with no error.
- `Bump()` uses a single `INSERT ... ON CONFLICT(name) DO UPDATE SET
  value = value + 1` statement so concurrent sessions (two `hsh run`
  in parallel shells) cannot lose increments; sqlite's locking also
  serializes concurrent writers safely.

### Version of a dev build

`version.Get()` yields `dev` for a local `go build`. `hsh update` on a
dev build still installs the latest release (updating over a
development binary is a user choice; the command reports both
`current: dev` and `target: v1.2.3` so it is never silent about it).
The metrics `version` field is the real tag when there is one, `dev`
otherwise.

## Implementation

New package `internal/update` (download/verify/replace + metrics +
counter, testable without the CLI):

- `update.go`: `Available(baseURL) (tag, error)` (latest-redirect tag),
  `Do(opts) (Result, error)` — download asset + SHA256SUMS, verify,
  self-replace via `os.Executable`/EvalSymlinks; `opts` carries the
  base URL, version pin, and current `version.Info`.
- `metrics.go`: `Report(baseURL version, platform string, sessions int)
  error` — the webhook POST with a 10s client; `HSH_NO_METRICS=1`
  check lives in the CLI, not here.
- `counter.go`: `Bump()`, `Take() (int, error)` over
  `XDG_STATE_HOME/hsh/metrics.db` — the sqlite counters table above,
  opened per-operation like denieddb does for its DB (no long-lived
  connection); `Take` reads and zeroes `sessions_since_last_update`
  in one transaction.

New command `internal/cli/update.go`, registered in `Root()`:

- `hsh update [--version vTAG] [--no-metrics]`
- env overrides honored: `HSH_RELEASE_BASE`, `HSH_VERSION`,
  `HSH_NO_METRICS`.
- Flow: print notice (unless skipped) → resolve latest tag → if equal
  to current tag, print `hsh <tag> is up to date` and exit without
  downloading or reporting (no update means nothing to report; the
  counter keeps accumulating) → download/verify/replace → POST metrics
  with the pre-update version and taken counter → print
  `✓ hsh <tag> installed (was <old>)`.

run.go: replace the two `sessionID = randHex(6)` sites with
`sessionID = newSessionID()` which calls `update.Bump()` (ignoring its
error) before returning the id.

## Tests

Unit (go test):

- `update.Available` parses the tag out of a `.../tag/v1.2.3` redirect
  (httptest server).
- checksum verification: matching and mismatching SHA256SUMS lines,
  missing entry.
- `Report` posts the exact JSON shape to a httptest endpoint
  (`product`, integer `sessions_since_last_update`, `platform` from
  GOOS/GOARCH); server error → non-nil error that the CLI downgrades to
  a warning.
- counter: bump/take/reset round trip; missing DB/row → 0; corrupt DB
  file → 0 without error; parallel `Bump()` calls all land (no lost
  increments).

Functional (test/functional.sh, network-dependent steps already exist
there):

1. `HSH_RELEASE_BASE` pointed at a local file:// or test server
   hosting a fake release (two tags, SHA256SUMS): `hsh update
   --version v1.2.3` on a copy of the binary in a temp dir replaces it
   (assert the new file's checksum) and the metrics.db counter row
   resets.
2. `--no-metrics` makes no POST (assert via a metrics test server
   counting hits; `HSH_RELEASE_BASE` for the update, the metrics URL
   needs its own override for tests — add `HSH_METRICS_URL` for this).
3. Update when target == current prints "up to date", no download, no
   metrics POST, counter untouched.
4. Unwritable install dir → the guidance error, nothing replaced.
5. Run two sessions, then `hsh update`: the metrics POST body carries
   `sessions_since_last_update: 2`.

## Docs

- docs/config.md: new "update" section — command, env overrides
  (`HSH_VERSION`, `HSH_RELEASE_BASE`, `HSH_NO_METRICS`,
  `HSH_METRICS_URL`), the metrics report (exact JSON, what is sent and
  what is not, when it fires, `--no-metrics`), and the
  `~/.local/state/hsh/metrics.db` counter database.
- Remove the `hsh update` TODO entry from TODO.md once implemented.
- README already tells users about install.sh; it is not edited.
