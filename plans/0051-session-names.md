# Session names

> **Status:** implemented.

## Goal

A session is identified by a random 12-hex id. To come back to one a
user runs `hsh ls`, finds the row, and copies the id (or remembers the
workspace directory, which fails once a workspace has several sessions).
Let the user name a session when they start it:

```
hsh run --name auth-refactor pi .
<detach>
hsh resume auth-refactor
```

A name is an alias for the id. It is accepted anywhere a session id is,
so `hsh detach`, `hsh rm`, `hsh forward --session`, `hsh ssh`, and the
monitor all take it. The id stays the identity: pod `hsh-<id>`,
`sessions/<id>/`, container names, and the proxy state all keep using it,
so nothing about the sandbox changes.

The detach and exit messages print the name when there is one:
`detached from session auth-refactor; re-enter with: hsh resume auth-refactor`.

## Non-goals

- No auto-generated names. Without `--name` or `HSH_SESSION_NAME` a
  session is unnamed and behaves as before.
- No prefix matching on names; names match exactly. Id prefixes keep working.
- Names are not workspace-scoped. They are unique per user.

## Storage

The name lives in `sessions/<id>/name` (one line), beside the `workspace`
file. It is not a pod label:

- the monitor palette lists sessions by reading `sessions/` without
  calling podman (`listSessions` in `internal/tui/palette/model.go`), and
  must show names
- podman labels cannot be edited after `pod create`, so a label would
  rule out `hsh rename`

Helpers in a new `internal/cli/sessionname.go`:

- `readSessionName(sessionDir string) string` — like `readWorkspaceFile`
- `writeSessionName(sessionDir, name string) error` — write to a temp file
  and rename, so a reader never sees a partial name
- `validateSessionName(name string) error`
- `findSessionByName(name string) (id string, ok bool)` — scan
  `state.SessionsDir()`, read each `name` file. Session dirs without a pod
  are ignored by the callers, which already check `podExists`.

Name `internal/state` helpers if the palette needs the same read; the
palette must not import `internal/cli`, so put `ReadName(id)` in
`internal/state` and have the cli helpers call it.

## Name rules

`validateSessionName` accepts `[a-z0-9][a-z0-9._-]{0,62}` and rejects:

- anything that is exactly 12 lowercase hex characters (it would be
  indistinguishable from an id)
- a leading `hsh-` (it would collide with pod names)
- `.` and `..`, and names that contain a path separator (they are
  directories to `resume`/`rm`)
- a name already used by another session:
  `session name "X" is taken by session ID (re-enter it with: hsh resume X)`

The uniqueness check and the write are not atomic across two concurrent
`hsh run --name X`. Accept that: resolution reports an ambiguous name
(below) if it ever happens, rather than silently picking one.

## Resolution

One function replaces the ad hoc `TrimPrefix(..., "hsh-")` calls:

```go
func resolveSessionRef(arg string) (id string, err error)
```

Order:

1. exact name (`findSessionByName`) with an existing pod
2. exact id, `hsh-<id>`, or a unique id prefix (the existing `matchPods`
   logic)

Names win over id prefixes. A name cannot be 12 hex characters, but it can
be a hex-looking prefix (`ab`, `cafe`). A deliberate name should beat an
accidental prefix match.

A name that is also an existing directory (`hsh resume auth` with `./auth`
present) is ambiguous, because `resume` and `rm` already treat an existing
directory as a workspace. Do not guess:

```
"auth" is both a directory and a session name; use ./auth for the
workspace or the session id ID for the session
```

`hsh rm` is destructive, so this error matters most there. `hsh resume`
and `hsh rm` get it through `matchPods`. The no-argument form and `.` are
unaffected.

If a duplicate name is found on disk (the race above), resolution fails
with `session name "X" matches sessions ID1 and ID2; use an id`.

### Call sites to switch to `resolveSessionRef`

- `matchPods` in `internal/cli/rm.go` (covers `hsh rm` and
  `resolveResumeTarget`)
- `resolveSession` in `internal/cli/session_resolve.go` (`--session` for
  forward, ssh proxy, oauth callback, oauth promote)
- `resolveSessionArg` in `internal/cli/detach.go` (explicit argument)
- `--session` in `internal/cli/proxy_hold.go`, `hsh oauth status`
  (`oauth.go`), `proxy_group.go`, and `sessionFilterFlags` in `root.go`
  (`monitordb.Filter.Session` must receive the id, so resolve before
  building the filter)
- `hsh ssh` host argument. `hsh ssh config` should keep emitting
  `hsh-<id>` host entries; accepting names there is free once
  `ssh proxy` resolves through `resolveSessionRef`. Names cannot contain
  characters that break an ssh host pattern, which the rules above ensure.

Update each `--session` flag help from `session id` to `session name or id`.

## Commands

### `hsh run --name NAME`

Add `name` to `runOptions` and a `--name` flag on `runCommand` only.
`run()` validates the name before creating the pod (so a bad name fails
before any pull or pod work) and writes `sessions/<id>/name` next to the
`workspace` file. The `creating session` line becomes
`creating session NAME (ID, image ...)`.

`--name` with `--rm` and `--tasks` is fine.

`HSH_SESSION_NAME` seeds the name for scripted runs. The precedence is the
`--name` flag, then `HSH_SESSION_NAME`, then no name; an empty variable
counts as unset. The variable goes through the same validation, and a bad
value fails with an error that names the variable. Only `hsh run` reads
it. `resume`, `rename`, and the other commands ignore it, and it is not
passed into the sandbox. Because names are unique, a shell that exports it
can start only one session at a time with that name; the second `hsh run`
fails with the usual `session name "X" is taken by session ID` error.

### `hsh rename SESSION NAME`

New command, `internal/cli/rename.go`. Resolves `SESSION` with
`resolveSessionRef`, validates `NAME` (excluding the session's own current
name from the uniqueness check), and writes the file. `hsh rename SESSION
""` is an error; to clear a name use `hsh rename --clear SESSION`. Works on
running sessions, since only the file changes. Register in `Root()` after
`resumeCommand()`.

### `hsh resume`

No flag changes; the positional help becomes
`[SESSION | WORKSPACE-DIR]` where SESSION is a name or id. The
"multiple sessions for workspace" error lists names:

```
multiple sessions for workspace /w:
  auth-refactor  53bd3fc7e5ba  Detached  2024-01-01 00:00
  -              53cc00000000  Exited    2024-01-02 00:00
pick one with: hsh resume <name or session id>
```

The not-found error becomes `no session matching "X" (see: hsh ls)`
unchanged.

### `hsh ls`

Add a NAME column after SESSION (`-` when unnamed). `-q` stays ids only so
scripts keep working. The podman passthrough path (`--format`, `--ctr-*`)
is unchanged: those flags go to podman, which does not know names.

### Messages

Where a message shows `hsh resume ID`, `hsh detach ID`, or
`hsh resume --take-over ID`, show the name when the session has one. Add
`sessionLabel(id string) string` that returns the name or the id, and use
it in these places:

- `run.go`: exit, detach, and "attached from another terminal" messages
  (lines ~654, 670, 680)
- `detach.go`: `detached session ...; re-enter with: hsh resume ...`
- `inline.go` (`monitorSessionOps`) detach and exit messages
- `forward.go`, `ssh.go`: `start it with hsh resume SESSION`
- `internal/tui/palette/model.go` `viewConfirmExit`

Messages keep printing the id as well in the one place a user might need it
for a script: the `creating session` line.

### Monitor palette

`listSessions` shows `name  id  workspace` for named sessions and
`id  workspace` otherwise, sorted by that text. Item ids stay the
session id.

## Implementation order

1. `internal/state`: `ReadName`, `WriteName`; unit tests
2. `internal/cli/sessionname.go`: validation, `findSessionByName`,
   `resolveSessionRef`, `sessionLabel`; unit tests
3. `run --name`, and the `creating session` message
4. switch call sites to `resolveSessionRef` (`matchPods` first, which
   brings `rm` and `resume` along)
5. `hsh ls` NAME column, resume ambiguity listing
6. `hsh rename`
7. messages and palette
8. docs and tests

## Tests

Go:

- `internal/state`: round trip, missing file, trailing newline, atomic
  replace
- `run` option resolution: flag beats `HSH_SESSION_NAME`, empty variable
  is unset, invalid variable value errors naming the variable
- `sessionname_test.go`: `validateSessionName` table (valid, uppercase,
  12 hex, `hsh-` prefix, `..`, slash, too long, empty, taken, own name on
  rename); `resolveSessionRef` with a fake sessions dir (`t.TempDir()` as
  `XDG_STATE_HOME`) and a stubbed pod existence check: name hit, name beats
  id prefix, id fallback, duplicate on disk, name with no pod
- `resume_test.go`: name resolves; name plus same-named directory errors
  and mentions `./NAME`; multi-session listing includes the name column
- `rm_test.go`: `hsh rm NAME` matches one pod; the directory collision is
  an error and removes nothing
- `help_test.go`: `{"rename", "--help"}`, `{"rename", "-h"}`
- palette test for the `name  id  workspace` row

Integration:

- `test/functional.sh` step 5: `hsh rename`, `hsh ls` shows the name,
  `hsh resume NAME` from an unrelated directory, `--name` reuse fails and
  names the holder, `hsh rm NAME`, `hsh rename --clear`
- `test/functional.sh` also covers `HSH_SESSION_NAME=NAME hsh run ...`,
  that `--name` overrides the variable, and that an invalid variable value
  fails before a pod is created

Run `go test ./...` and `test/functional.sh`.

## Docs

User-facing docs (`README.md`, `Documentation/`) are not edited here; add
a TODO.md entry to document `--name`, `hsh rename`, and the NAME column in
`Documentation/sessions.md`.

Design docs in `internal-docs/`:

- `architecture.md`: document `HSH_SESSION_NAME` beside `--name`; add `name` to the session directory contents, and
  describe name resolution beside the `hsh resume` bullets, including the
  name-beats-prefix rule and the directory collision error
- `testing.md`: mention the new functional step
