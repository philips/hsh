# No Workspace Runs

> **Status: implemented.** Breaking change: `hsh run IMAGE` with no
> `WORKSPACE-DIR` stops mounting the current directory.

## Goal

`hsh run pi` starts a session with **no host bind mounts** from the
config. `/workspace` exists but is a plain directory in the workspace
container's own filesystem. The agent builds its own hermetic
environment (clone repos, install tools) and nothing on the host
filesystem is visible or writable to it.

To mount a directory, the user names it:

```
hsh run pi .            # mount cwd at /workspace (today's `hsh run pi`)
hsh run pi ~/work/ghar  # mount that directory
hsh run pi              # no mounts; /workspace is private to the sandbox
```

This matches what the README and `Documentation/sessions.md` already
show (`hsh run pi .`). It is also useful for tests that do not need a
workspace.

## Current behavior

`internal/cli/run.go`:

- `workspaceRoot` defaults to `cwd` when `workspaceArg == ""`.
- `cfg.ParsedMounts(workspaceRoot)` resolves the config's mounts (the
  default `.:/workspace`) against it.
- `workspaceSource = mounts[0].Source` goes into the `workspace`
  session file and the `hsh.workspace` pod label.
- `wsDir` (the shell's start directory, `--workspace` for
  `/hsh-workspace`) defaults to `mounts[0].Target`. If there are no
  mounts, this panics.
- `workspace.enterSession` calls `stripSetuid(opts.Workspace)`, which
  skips the workspace dir, and then `os.Chdir(opts.Workspace)`, which
  fails if the image has no `/workspace`.

## Design

### CLI

- `hsh run [FLAGS] IMAGE [WORKSPACE-DIR]` keeps its usage line.
  Omitting `WORKSPACE-DIR` now means "no workspace", not "cwd".
- Validation of a given `WORKSPACE-DIR` (must exist and be a directory)
  stays the same.
- Update the `--workdir` help text: "(default: the workspace mount
  point, or /workspace when there is no workspace)".
- On a fresh run without `WORKSPACE-DIR`, always print this line to
  stderr (not only with `--verbose`), because the change can surprise
  people:
  `hsh: no workspace directory given; /workspace is private to the sandbox (to mount the current directory: hsh run IMAGE .)`

### Config mounts with no workspace

Config mounts are resolved against the workspace directory, so they
have nothing to anchor to without one. To keep the guarantee simple
("no positional, no host filesystem"), a run without `WORKSPACE-DIR`
uses **zero** config mounts, wherever the config came from (label,
`--trust-image`, or `--config`).

- If the effective config's `mounts` is the default `[".:/workspace"]`,
  drop it silently.
- If it declares anything else, drop it too and warn:
  `hsh: warning: ignoring mounts (a, b, ...) from <configSource>: no workspace directory given`
  Use `listOf` for the list.
- Store `"mounts": []` in the session's `config.json` (set
  `cfg.Mounts = []string{}`, not nil). `decode` starts from `Default()`,
  so a missing key would bring `.:/workspace` back on resume and
  resolve it against the session dir. An explicit empty list survives
  the round trip. Add a unit test for this.

hsh's own mounts (binary, CA, notice, seeds, gVisor bits) stay. They
are not workspace mounts.

### Session identity

- `workspaceSource = ""` when there are no mounts. The `workspace` file
  is still written, with empty contents, so readers behave the same.
- Leave out `--label hsh.workspace=...` when `workspaceSource` is
  empty. `podInfo.Workspace()` already returns `""`, and `hsh ls`
  already prints `-`.
- `hsh resume` with no argument still matches on cwd, so it never picks
  a workspace-less session. Those are resumed by session id. The "no
  session for workspace" error stays.
- `hsh rm DIR` and `hsh ls --workspace DIR` don't match them either.
  That is correct.
- Resume log line: `resuming session %s` plus ` for workspace %s` only
  when there is a workspace.
- Monitor/palette (`internal/tui/palette/model.go`) and `monitordb`
  read the `workspace` file. Check they render an empty value as `-`
  (or skip it) and don't show a blank label.

### Inside the sandbox

- `run.go`: `wsDir` falls back to `/workspace` when `ro.workdir == ""`
  and `len(mounts) == 0`. Never index `mounts[0]` without checking the
  length (both `wsDir` and `workspaceSource`).
- `workspace.enterSession`: `os.MkdirAll(opts.Workspace, 0o755)` before
  `Chdir`, so images without `/workspace` work.
- `stripSetuid` skips the workspace dir only because it is a host bind
  mount. With no mount, `/workspace` is image content and must be
  scanned like everything else. `run.go` passes `--private-workspace`
  (`InnerOptions.PrivateWorkspace`) when the session has no mounts, and
  only then is the skip dropped. The flag is opt-in rather than a
  `--workspace-mounted` opt-out. Existing containers keep their creation
  args but run the updated, bind-mounted binary, and an opt-out would
  make them strip setuid bits from host files in their workspace.
- Persistence: `/workspace` lives in the workspace container's writable
  layer. That layer survives detach, exit, and `hsh resume`, because
  the container is stopped, not removed. gVisor sessions use
  `--overlay2=none`, so writes land there too. It is lost on `hsh rm`
  and on `--rm`, and also whenever the resume path recreates the
  workspace container (`!containerExists(wsName)`). Note that last case
  in the docs.

### Code shape

Pull the mount and workspace decision out of `run()` into a helper so
it can be unit tested without podman:

```go
func sessionMounts(cfg *config.Config, workspaceRoot, configSource string) (mounts []config.Mount, workspaceSource string, warning string, err error)
```

`workspaceRoot == ""` means no workspace. Without one, the helper
returns no mounts, sets `cfg.Mounts = []string{}`, and builds the
warning text. With one, it does today's resolution and rewrites
`cfg.Mounts` as absolute paths.

## Tests

Unit (`internal/cli`):

- `sessionMounts` with no workspace and the default mounts: no mounts,
  no warning, `cfg.Mounts` empty and non-nil.
- No workspace and custom mounts: no mounts, a warning that lists them.
- With a workspace: same results as today, absolute sources.
- Session config round trip: marshal a config with `Mounts: []string{}`,
  `config.Load` it, and `ParsedMounts` returns none.

Unit (`internal/workspace`):

- `stripSetuid` honors the skip only when the workspace is mounted (or
  test the option plumbing, if the walk is hard to test).

Functional (`test/functional.sh`):

- Many steps rely on the implicit cwd mount (`cd "$TMP/labelws" &&
  hsh run IMAGE`, the shortname/pull/transport/launch/tasks steps, and
  `test/detach.py`, `test/inline.py`, `test/security.py`). Add `.` to
  each one that checks workspace contents. Leave steps that don't need
  a mount as they are, so they also cover the no-workspace path.
- New step: in a dir holding a marker file, run `hsh run --rm IMAGE`
  and check, inside the sandbox, that:
  - `pwd` is `/workspace`;
  - the marker is absent;
  - `/proc/self/mountinfo` has no `/workspace` entry;
  - a file written to `/workspace` does not show up on the host.

  Also check that the startup notice was printed.
- New step: a run without `--rm`. Write `/workspace/x`, exit, then
  `hsh resume <id>`. `x` must still be there, and `hsh ls` shows `-`
  for the workspace.
- New step: with a trusted label image or a `--config` that declares
  extra mounts, run without `WORKSPACE-DIR`. The warning must name the
  mounts and none of them may be mounted.
- Run under both runtimes (`HSH_TEST_RUNTIME=container` and gVisor).

## Docs

- `internal-docs/images.md` (the "workspace directory ... or the
  current directory when omitted" paragraph), `internal-docs/config.md`
  (the `mounts` row: mounts apply only when `WORKSPACE-DIR` is given),
  and `internal-docs/architecture.md` (the boundary/mounts section).
  Describe the no-workspace mode and where its data lives.
- `README.md` and `Documentation/` already use `hsh run pi .`. I won't
  edit them. The owner should check them for any text that says the
  cwd is mounted by default.

## Decisions

- Custom config mounts without `WORKSPACE-DIR` are dropped with a
  warning, not rejected. The config format is due for a rethink, so
  this stays minimal.
- No `--no-workspace` flag. Omitting `WORKSPACE-DIR` is the only way
  to get a session without a workspace.
