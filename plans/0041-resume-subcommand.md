# `hsh resume` subcommand

> **Status:** implemented.

## Goal

Replace `hsh run --resume` with a top-level `hsh resume` command. The
workflow is:

```
hsh run pi .
<exit or detach>
hsh resume .
```

`hsh run` only creates sessions. `hsh resume` only re-enters existing ones.
`hsh run --resume` is removed with no compatibility shim. ff reports
`--resume` as an unknown flag.

## Command surface

```
hsh resume [FLAGS] [SESSION | WORKSPACE-DIR]
```

ShortHelp: `re-enter an existing session (attach to its shell, or restart it if it exited)`

The positional argument is resolved like `hsh rm` resolves its arguments
(`matchPods` in `internal/cli/rm.go`):

- **No argument:** use the current directory as the workspace.
- **An existing directory** (`.`, `../proj`, an absolute path): match pods
  whose `hsh.workspace` label equals the absolute path. A directory wins over
  a session id with the same name, as in `rm`.
- **Anything else:** treat it as a session id. Accept the exact session id,
  the full pod name (`hsh-<id>`), or a unique prefix of either. An ambiguous
  prefix is an error.

Outcomes:

| matches | result |
|---|---|
| 0, workspace | `no session for workspace DIR; start one with: hsh run IMAGE DIR` |
| 0, id | `no session matching "X" (see: hsh ls)` |
| 1 | resume it |
| >1, workspace | `multiple sessions for workspace DIR:` followed by one line per session (id, status from `sessionStatus`, created time), then `pick one with: hsh resume <session id>` |
| >1, id prefix | the existing `ambiguous sandbox` error from `matchPods` |

Flags. Each one does something on resume:

| flag | meaning |
|---|---|
| `--take-over` | detach whatever terminal is attached first (same behavior as today) |
| `--forward` | same as `hsh run --forward` |
| `--monitor-db`, `--secret-db` | same defaults as `hsh run` |
| `--verbose` | same as `hsh run` |

These `hsh run` flags are not offered on resume: `--config`, `--workdir`,
`--tasks`, `--network`, `--pull`, `--rm`, `--runtime`, `--workspace`. A
resumed session keeps its stored config, runtime, and network mode.

`--fresh` is removed entirely and is not replaced. To reset a session, remove
it and start a new one: `hsh rm ID && hsh run IMAGE DIR`.

## Implementation

### 1. `internal/cli/resume.go` (new)

- Add `resumeCommand() *ff.Command`. Build it with `ff.NewFlagSet("resume")`,
  as `runCommand` does, using only the flags above. Allow at most one
  positional.
- Add `resolveResumeTarget(arg, cwd string, pods []podInfo) (string, error)`.
  It is a pure function so unit tests can cover it. It uses `matchPods` and
  produces the errors in the table above. Directory detection uses
  `os.Stat`, as `matchPods` does. Tests use `t.TempDir()`.
- The command's `Exec` calls `listSandboxes()`, resolves the session id, sets
  `ro.resume = true` and `ro.sessionID = id`, then calls `run(ctx, ro, "", "")`.
- Register `resumeCommand()` in `Root()` (`internal/cli/root.go`) directly
  after `runCommand()`.

### 2. `internal/cli/run.go`

- Remove the `resume`, `take-over`, `fresh`, and `workspace` flags from
  `runCommand`. Remove the `--resume` branch in `Exec`. Change ShortHelp to
  `start a new sandboxed session (re-enter it later with hsh resume)`.
- In `runOptions`, delete `workspaceSel` and `fresh`, and add
  `sessionID string`. Keep `resume` and `takeOver` as fields that only
  `resumeCommand` sets.
- In `run()`:
  - Delete the validations that can no longer trigger: `--workspace`
    requires `--resume`; `--runtime` with resume; `--rm` with resume;
    `--fresh` / `--take-over` require `--resume`.
  - Delete the session-lookup block (the `listSandboxes` switch under
    `if ro.resume`). Use `ro.sessionID` instead.
  - Set `workspaceRoot` from the positional only when `!ro.resume`.
  - Delete the `--fresh` code: the `if ro.fresh` block in the resume branch,
    the `freshImage` variable, and the `freshImage` fallback when choosing
    the image for a new session.
- Update the user-facing messages from `hsh run --resume` to `hsh resume`:
  - the exit message (`start it again with: hsh resume ID`)
  - the detach message (`re-enter with: hsh resume ID`)
  - the "attached from another terminal" error
    (`... or take it over with: hsh resume --take-over ID`)
  - the non-detachable "already running" error. It mentions
    `non-interactive, --rm, or --tasks`, which is still accurate.

### 3. Client detection: `internal/cli/detach.go`

`isHshRun` decides whether `client.pid` names a live attached client. It
checks for `"run"` in argv. An attached `hsh resume` must also count, so
accept `"run"` or `"resume"`. Rename the function to `isHshClient`. Without
this change, a second `hsh resume` would not see the first as attached, and
`hsh detach` would delete its pid file. Update the `signalling hsh run` and
`hsh run (pid …) did not detach` error strings to say `hsh client`. Update the
`hsh detach` success message to `re-enter with: hsh resume ID`.

### 4. Other messages

- `internal/cli/inline.go` (`monitorSessionOps`): change the detach and exit
  messages to `hsh resume ID`.
- `internal/tui/palette/model.go` (`viewConfirmExit`): change to
  `hsh resume ID starts a new shell.`
- `internal/cli/forward.go`, `internal/cli/ssh.go`: change
  `start or resume it with hsh run` to
  `start it with hsh resume SESSION`.
- Run `rg -n -- '--resume|--fresh' internal` and confirm no matches remain.

### 5. Tests

Go:
- `internal/cli/resume_test.go`. Test `resolveResumeTarget` for these cases:
  no arg with one, zero, and several pods for the cwd; `.`; another directory;
  exact id; `hsh-` pod name; unique prefix; ambiguous prefix; unknown id; a
  directory whose name equals a session id. Check that the multi-session
  error lists every id and suggests `hsh resume <session id>`.
- `internal/cli/help_test.go`: add `{"resume", "--help"}` and
  `{"resume", "-h"}`.
- Add a test that `hsh run --resume` is rejected by flag parsing. Parse with
  `Root().Parse([]string{"run", "--resume"})` and expect an error. Do the
  same for `run --fresh` and `resume --fresh`.
- A unit test that `isHshClient` accepts argv `hsh resume X` and `hsh run X`
  and rejects `hsh ls`. Factor the argv check out of the `/proc` read so it
  can be tested.

Integration (rewrite every `run --resume` call):
- `test/functional.sh`:
  - steps 3 and 5, and the calls near the hold, session.db, notice, and
    config-version checks (lines ~152, 283–349, 985, 1162, 1312, 1519–1523)
  - step 5 also covers: `hsh resume` with no session fails with
    `no session for workspace`; `hsh resume .` from inside the workspace;
    `hsh resume "$TMP/workspace"` from another cwd; `hsh resume <id-prefix>`
- `test/gvisor.sh` (lines ~119–147)
- `test/detach.py`: `spawn("resume", sid)` and
  `spawn("resume", "--take-over", sid)`
- `test/inline.py` (line ~184)

Run `go test ./...`, then `test/functional.sh`, `test/detach.py`,
`test/inline.py`, and `test/gvisor.sh` when runsc is available.

### 6. Docs (internal-docs/ only)

Do not edit `README.md` or `Documentation/`. `Documentation/sessions.md`
already describes `hsh resume`.

Change `run --resume` to `hsh resume` and `--workspace DIR` to the positional
directory in:
- `internal-docs/architecture.md`: lines ~27, 650 (the `hsh ls` comment), 689,
  717, 756, 760–797. Rewrite the bullet "`hsh run --resume` re-enters the
  current workspace's session (or the one named by `--workspace DIR`)" to
  describe the positional resolution above. Remove the `--fresh` bullet
  (~791) and change the closing note (~796) to say a reused sandbox is reset
  with `hsh rm ID` followed by `hsh run IMAGE DIR`.
- `internal-docs/config.md`: lines ~347–359, 434
- `internal-docs/gvisor.md`: lines ~8, 105
- `internal-docs/testing.md`: lines ~28–32, 60–65, 115
- `internal-docs/sandboxed-oauth.md`: lines ~14, 76, 283, 290

After the change, `rg -n -- '--resume|--fresh' internal-docs` should return nothing.

### 7. TODO.md

Remove the "hsh resume subcommand" section. Add a new section "gVisor
suspend/resume" that keeps only the checkpoint/restore idea (below).

## gVisor checkpoint/restore (out of scope, design constraint)

The TODO sketches a later `hsh suspend` (or a monitor command) that
checkpoints a gVisor sandbox. `hsh resume` would then restore it. Do not
build it now. Keep the code shaped so it can be added later:

- `hsh resume` decides what to do from the session's state, using the same
  logic `sessionStatus` already has for `Attached`, `Detached`, `Shell
  exited`, and `Exited`. A later `Suspended` state, meaning a checkpoint image
  exists in the session dir, would add one more case: run `runsc restore`
  instead of starting the container. Do not scatter state checks in a way
  that would block that.
- Known obstacles, to record in that future plan:
  - The embedded runsc payload drops `checkpointgofer`.
  - `podman container checkpoint` expects CRIU-style runtime flags.
  - The proxy's netstack socket is recreated on each start, so a restored
    sandbox must reconnect to it.

## Acceptance

- `hsh run pi .`, exit, `hsh resume .` re-enters the same container.
- `hsh run pi .`, detach, `hsh resume` (in the workspace) re-attaches to the
  same shell.
- `hsh resume <prefix>` works from any directory.
- A second `hsh resume` refuses a session that is attached elsewhere.
  `hsh resume --take-over` takes it over.
- `hsh run --resume`, `hsh run --fresh`, and `hsh resume --fresh` fail as
  unknown flags.
- No `--resume` or `--fresh` remains in `internal/`, `test/`, or
  `internal-docs/`.
