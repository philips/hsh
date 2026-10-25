# `harnessh proxy-watch` — interactive TUI for proxy denials

## Goal

A terminal UI (bubbletea + bubbles) that lists all proxy domain failures
recorded in the denial database and lets the user choose which ones to
approve. This is the interactive counterpart of the existing
`harnessh proxy-denied` (plain table) and `harnessh proxy-auto-allow`
(approve everything, no choice): the TUI gives per-host selection.

Libraries: `github.com/charmbracelet/bubbletea` (framework),
`github.com/charmbracelet/bubbles` (list, help, key components),
`github.com/charmbracelet/lipgloss` (styling). All pure Go, so the
`CGO_ENABLED=0` static build is preserved (binary grows by a few MB).

## Command

```
harnessh proxy-watch [--config PATH] [--db PATH] [--all]
```

- Host-side subcommand (like `proxy-denied` / `proxy-auto-allow`). It edits
  the config file and reads/writes the denial database, so it must run on
  the host — the workspace container does not see either.
- `--config` (default `config.harnessh`) — where approved hosts are added
  (`network.allow`), same as `proxy-auto-allow`.
- `--db` (default `$XDG_STATE_HOME/harnessh/denied.db`).
- `--all` — start with "show ignored" on (see below).
- Requires a TTY; if stdout is not a terminal, fail with a hint to use
  `harnessh proxy-denied`.

## UX

```
 denied hosts awaiting review (12)          2 selected

  [x] api.mistral.ai           443   14x  10-25 06:31  10-25 06:12
  [ ] telemetry.example.com    443    3x  10-25 06:29  10-25 06:20
   ▸ storage.googleapis.com    443    1x  10-25 06:28  10-25 06:28
  · old.example.net            443    2x  10-24 18:02  10-24 18:02  ignored

  ↑/k ↓/j move   space select   a all   n none   / filter
  enter approve   i ignore   t show ignored   r refresh   ? help   q quit
```

- Multi-select list: `space` toggles, `a`/`n` select/clear **all** items
  (ignoring any active filter), `↑↓`/jk move, `/` opens the bubbles
  built-in fuzzy filter, `esc` clears it.
- Columns: checkbox, host, port (from `target`), count, last seen, first
  seen. Order: **most recently requested first** (`last_seen DESC`, then
  host); no sort toggle — the list is re-sorted on every refresh, so newly
  denied hosts appear at the top.
- `enter` (or `o`) — **approve** selected hosts.
- `i` — **ignore** selected hosts: mark them handled permanently without
  adding them to the allowlist. Ignored hosts never reappear in the default
  view; `t` toggles a "show ignored" view that lists them dimmed, labeled
  `ignored`, and non-selectable (`--all` starts in that view).
- `r` — refresh now; the list also polls the DB every ~2s so new denials
  appear live at the top while the TUI is open.
- Status line shows feedback: "approved 3 host(s) — proxies reload within
  ~1s", "ignored 1 host(s)", errors from config/DB writes, selection
  counts.
- Empty state: "no denied hosts awaiting review".
- `?` toggles the full help; `q`/`esc`/`ctrl+c` quit.

## Semantics

Approving a selection (mirrors `proxy-auto-allow`):

1. `config.AddAllowedHosts(configPath, hosts)` — appends the hosts to
   `network.allow` in the config file, preserving the rest of the file.
   Running proxies watch the config and hot-reload within ~1s.
2. `denieddb.MarkAllowed(db, hosts)` — marks the rows handled (both the
   `denied` and `dns_denied` tables, as `MarkAllowed` already does).
3. Remove the approved items from the list and show the count.

Ignoring a selection:

1. `denieddb.MarkIgnored(db, hosts)` — sets `allowed = 1, ignored = 1` on
   the `denied` rows (DNS audit rows are untouched). Ignoring is permanent:
   by existing `denieddb` semantics a later denial of the same host does
   not reset `allowed`, so it never re-enters the review list.
2. Remove the ignored items from the list and show the count.

To tell ignored rows apart from approved ones, the schema gains an
`ignored` column (see Implementation). Error handling: if the config write
fails, do not mark the DB and show the error in the status line.
Approving/ignoring an empty selection is a no-op with a hint ("select
hosts with space"). The DB is opened read-write; it is WAL with a busy
timeout, so the running proxy can keep writing concurrently.

DNS denials (`dns_denied`) are not part of this TUI: they are audit-only,
and approving one would also allow HTTP egress to that host (the allowlist
is shared between the proxy and the DNS forwarder). They stay with
`harnessh proxy-denied --dns`.

## Implementation sketch

- `internal/denieddb/denieddb.go`:
  - schema: add `ignored INTEGER NOT NULL DEFAULT 0` to `denied` (and
    `dns_denied` for symmetry); migration for existing databases: check
    `PRAGMA table_info(...)` and `ALTER TABLE ... ADD COLUMN` if missing.
  - `MarkIgnored(db, hosts)` — `UPDATE denied SET allowed = 1, ignored = 1,
    allowed_at = ? WHERE host = ?`.
  - `Ignored(db)` — `SELECT ... FROM denied WHERE ignored = 1 ORDER BY
    last_seen DESC` for the show-ignored view.
  - `Pending`, `All`, `MarkAllowed` unchanged (existing commands keep
    their behavior).
- `internal/cli/proxy_watch.go` — `ProxyWatch(args []string) error`:
  flags, TTY check, open DB (`denieddb.Open`), initial `Pending` (and
  `Ignored` when `--all`), build the model, `tea.NewProgram(model).Run()`.
- `internal/tui/proxywatch/` (new package, keeps `cli` thin):
  - `model.go` — the bubbletea `Model`: items, selection set, filter,
    show-ignored state, status message, tick-based refresh + re-sort,
    approve/ignore commands.
  - `item.go` — `list.Item` (host row) + custom `list.DefaultDelegate`
    rendering the `[x]`/`[ ]` checkbox, dimmed style for ignored rows.
  - `keys.go` — `bubbles/key` bindings + `bubbles/help` short/full help.
  - `messages.go` — `tickMsg` (2s poll), `refreshDoneMsg`, `actionDoneMsg`.
- `main.go` — register `proxy-watch`, add the usage line.
- `docs/architecture.md` — denial review section gains
  `harnessh proxy-watch` (one paragraph + the key bindings).
- Dependencies: `go get github.com/charmbracelet/bubbletea
  github.com/charmbracelet/bubbles github.com/charmbracelet/lipgloss`.
  Verify `CGO_ENABLED=0 go build -o harnessh .` still produces a static
  binary.

## Verification

1. **Unit test** (headless — bubbletea models are testable without a TTY):
   `internal/tui/proxywatch/model_test.go` drives `Update` with synthetic
   key messages against a temp DB + temp config:
   - select + approve → hosts appear in the config's `network.allow`, DB
     rows marked allowed (ignored=0), items removed from the list
   - ignore → DB rows marked allowed with ignored=1, config unchanged,
     items removed; the show-ignored toggle lists them dimmed and
     non-selectable
   - list is sorted most-recently-requested first; a refresh re-sorts and
     puts new denials at the top
   - `a`/`n` act on all items even with an active filter
   - filter narrows the visible list
   - approve with empty selection is a no-op
   - config write failure → error surfaced, DB not marked
   - migration: an old DB without the `ignored` column is upgraded on open
2. **Manual**: start a sandbox, trigger denials, run
   `harnessh proxy-watch`, approve one host; the next request to that host
   succeeds (config hot reload), `harnessh proxy-denied` no longer lists
   it. Ignore another host; it disappears and only reappears under
   `t`/`--all`.
3. **Live refresh**: with the TUI open, trigger a new denial in the
   sandbox — it appears at the top within ~2s.
4. `CGO_ENABLED=0 go build` succeeds; `go vet ./...` clean; existing
   functional test still passes (the TUI is not exercised there — it needs
   a TTY; optionally add a `script(1)`-based pty smoke test later).

## Decisions (resolved)

- Command name: `proxy-watch`.
- Ignore action: included — permanent dismissal (never shown again by
  default); a "show ignored" toggle (`t`, or `--all` at startup) reveals
  ignored rows dimmed and non-selectable. Distinguished from approved via
  a new `ignored` column.
- DNS denials: not in the TUI. A read-only DNS view would be a pure audit
  listing — identical to `harnessh proxy-denied --dns` — and approving a
  DNS denial would also allow HTTP egress to that host (shared allowlist),
  so it cannot be actionable. DNS stays audit-only.
- Non-TTY: hard error with a hint to use `harnessh proxy-denied`.
- Sort: no toggle — always most recently requested (`last_seen DESC`) at
  the top; select-all/none (`a`/`n`) ignore the active filter.
