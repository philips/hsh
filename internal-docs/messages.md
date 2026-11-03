# Console and TUI messages

hsh talks to the user through a handful of distinct surfaces. When writing
or reviewing messages, pick the right surface for the content, then keep
the wording terse and accurate.

`./internal-docs/find-messages.sh` prints every message on every surface
below, grouped by surface, with `file:line`. The list goes to stdout in
quickfix format (`file:line:message`); the section headers go to stderr,
so a redirected run is a pure jump list:

```sh
./internal-docs/find-messages.sh > /tmp/hsh-messages.txt   # sections still visible on screen
nvim -q /tmp/hsh-messages.txt                               # opens on the first message
```

In nvim: `:copen` shows the list, `:cnext`/`:cprev` (or `]q`/`[q` with
vim-unimpaired-style maps) walk it, `<cr>` on a quickfix line jumps to
the message. Each section of the list corresponds to one surface below,
in the same order — the stderr headers printed during the run tell you
which is which.

## Surfaces

### CLI prints — `internal/cli/`, `internal/crashlog/`

Direct `fmt.Print*` output from host-side subcommands.

- **stdout** carries results and prompts (e.g. `hsh ls`, `hsh proxy
  denied`, the oauth-provider `[y/N]` prompts). It must stay clean enough
  to pipe: no warnings, no progress chatter.
- **stderr** carries warnings and errors.
- Commands that need to be testable take an `io.Writer` for output and
  read answers from an `io.Reader` (`runWipe`, `addSuggestedProviders`);
  prefer that pattern for new interactive flows.

### Error strings — `fmt.Errorf` / `errors.New` in `internal/cli/`

Every error returned from a command is rendered by `main.go` as
`hsh: <message>` on stderr. These are console messages: write them for
the user (what happened, what to do), not for a stack trace. They are
also matched in tests, so wording changes can break tests — run the
suite after rewording.

### Verbose progress — `infof` (`internal/cli/root.go`)

Progress chatter (pod creation, workspace start, config reloads) is
suppressed by default and only shown with `--verbose` or `HSH_VERBOSE=1`.
When a message feels too chatty for everyday use, the fix is usually to
route it through `infof` rather than delete it.

### Monitor TUI

Three bubbletea programs render the monitor:

- `internal/tui/palette` — the `hsh monitor` command palette. Status
  lines go through `Model.setStatus` (empty string clears the line);
  views and help text are inlined in `model.go`.
- `internal/tui/proxywatch` — the inline denial-review view opened with
  ctrl-space during `hsh run`. Status lines are assigned to `m.status`;
  help lines are rendered from key bindings.
- `internal/relay` — the relay draws the status bar over the running
  session (` hsh | MONITOR | ...`) and the overlay chrome for the
  inline review and crash notices (`overlay.go`). This text interrupts
  the user's terminal; keep it to one line and unambiguous.

Container roles (`/hsh-proxy`, `/hsh-workspace`, `hsh-reaper`) also print,
but that output goes to `podman logs` and crash logs, not the user's
console; it is out of scope for message reviews.

## Review checklist

When auditing messages (the script output, top to bottom):

1. **Terse** — one line, no filler ("error: ", "failed to", trailing
   punctuation). A message the user must read mid-session should be
   readable in one glance.
2. **Accurate** — does it still describe what the code does? Counts,
   key names, and suggested commands are the usual drift.
3. **Consistent voice** — lowercase, no `hsh:` prefix inside messages
   (the prefix comes from `main.go` for errors, or the status bar for
   the relay), consistent pluralization (`host(s)` vs `hosts`).
4. **Right surface** — warnings must not be on stdout; progress belongs
   in `infof`; anything testable should come from an injected writer.

Wording-only changes do not need documentation updates; changes to what a
message *tells* the user to do (a suggested command, a key name) must be
reflected in `internal-docs/` and README-facing docs.
