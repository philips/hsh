# `hsh run IMAGE WORKSPACE-DIR`: workspace as an explicit argument

> **Status: implemented.** Breaking change to `hsh run`'s positional
> arguments and to the meaning of `cwd`.

## Goal

Let the user name the workspace directory on the command line instead of
having to `cd` into it first:

```
hsh run ghcr.io/philips/hsh-pi ~/work/ghar
```

Today the workspace is implicit: `hsh run` mounts the current directory
(`.:/workspace` default mount, resolved against `cwd`), labels the pod
with `hsh.workspace=<cwd>`, and `--resume` finds the session by matching
`cwd` again. So the user must stand in the workspace directory to start
*and* to resume. That is the UX we want to fix.

## Current behavior (what changes)

`internal/cli/run.go`:

- `cwd, _ := os.Getwd()` is the anchor for everything workspace-related.
- Fresh run: `mountBase = cwd` when the config comes from the image
  label; mounts resolve relative paths against it. The default mount
  `.:/workspace` therefore means "mount cwd at /workspace".
  `workspaceSource = mounts[0].Source`.
- The config path defaults to `config.hsh`, so a `config.hsh` in cwd is
  read implicitly. When a seed file is used, `mountBase =
  filepath.Dir(seed)` (not cwd), so the config file's own directory
  anchors relative mounts.
- `--resume` with no session id: `listSandboxes("label=hsh.workspace=" +
  cwd)`.
- The pod is labeled `hsh.workspace=<workspaceSource>`.
- The single positional argument is IMAGE (or a session id with
  `--resume`).

## Proposed CLI

```
hsh run [FLAGS] IMAGE [WORKSPACE-DIR]
hsh run [FLAGS] --resume [SESSION-ID]
```

- `WORKSPACE-DIR` is the host directory to use as the workspace. It is
  optional; when omitted it defaults to `cwd` (preserving the common
  "cd in, run" flow). The path is resolved to an absolute path with
  `filepath.Abs` against `cwd` and must exist and be a directory.
- `WORKSPACE-DIR` replaces `cwd` as the anchor for *all* workspace
  logic: mount resolution base, the `hsh.workspace` label, the
  `workspace` session file, and `--resume` lookup.
- The default mount stays `.:/workspace`; `.` now resolves against
  WORKSPACE-DIR, so `hsh run IMAGE ~/work/ghar` mounts `~/work/ghar` at
  `/workspace`.

### Config must be explicit; drop the seed dir anchor

Remove the implicit `config.hsh`. The `--config` default changes from
`config.hsh` to empty; a seed file is loaded only when `--config` is
given. With no `--config`, config comes from the image label (and the
image must be present/pullable).

With config always explicit, relative mounts no longer anchor to the
config file's directory. They anchor to WORKSPACE-DIR (or `cwd` when
WORKSPACE-DIR is omitted) in every case. This removes the "seed dir"
concept entirely: a `--config` file placed
anywhere (e.g. `~/.config/hsh/foo.hsh`) describes mounts relative to the
chosen workspace, not to wherever the file happens to live.

Breaking change: users who relied on dropping a `config.hsh` in cwd must
now pass `--config config.hsh` (or `--config ./config.hsh`). Call it out
in the changelog.

### Resume keeps a single positional

`--resume` still takes an optional session id, not a workspace dir. To
resume by workspace without cd-ing, reuse the freed-up `--workspace`
flag:

```
hsh run --resume --workspace ~/work/ghar
```

With no session id and no `--workspace`, resume falls back to `cwd`.

## Flag rename

The existing `--workspace` flag means "the workspace directory *inside*
the container" (default: first mount target). It does not define a mount;
its only effect is `os.Chdir` into that directory when the shell starts
(`internal/workspace/workspace.go:182`). The mount target is set by the
config `mounts`. So:

- `--workspace` → `--workdir` (the in-container working directory the
  shell starts in), matching podman/OCI `WORKDIR`. Help text: "directory
  the shell starts in (default: the workspace mount point)."
- `--workspace` is then freed and reused on the resume path to select
  the host workspace for lookup (see above).

Breaking flag change; acceptable per the goal. Call it out loudly in the
changelog: `--workspace` changes meaning, so scripts must switch to
`--workdir`. The host workspace on a fresh run is the positional
WORKSPACE-DIR, not a flag.

## Implementation sketch

In `internal/cli/run.go`:

1. Parse a second positional for the fresh-run path. Update
   `runCommand`'s arg check:
   - Non-resume: allow up to two positionals (IMAGE, WORKSPACE-DIR).
   - Resume: still one positional (SESSION-ID); the host workspace comes
     from `--workspace`.
   - Update `Usage` and `ShortHelp`.
2. Add `workspaceRoot` resolved early:
   ```go
   workspaceRoot := cwd
   if workspaceArg != "" {
       workspaceRoot, err = filepath.Abs(workspaceArg)
       // stat: must exist and be a dir
   }
   ```
3. Change the `--config` default from `config.hsh` to empty. Load a seed
   file only when `--config` is non-empty; otherwise take config from
   the image label. Drop the `filepath.Dir(absSeed)` mount anchor.
4. Replace every workspace use of `cwd` with `workspaceRoot`, and set
   `mountBase = workspaceRoot` on *both* paths (image-label and seed
   file), since mounts now always anchor to the workspace:
   - `--resume` lookup: `listSandboxes("label=hsh.workspace=" +
     workspaceRoot)` (line ~148).
5. Rename the host-facing flag: field `workspaceDir` → `workdir`, flag
   name `--workdir`; `wsDir` derivation (line ~289) unchanged otherwise.
   Then add a new `--workspace` flag (host workspace dir) used only on
   the resume path. The internal proxy/ws `--workspace` CLI args to the
   inner binaries (lines ~421, ~505) describe the host workspace source
   and can keep their names — they are not user-facing.
6. Messages referencing `cwd` ("multiple sessions for workspace %s",
   "no session to resume") now print `workspaceRoot`.

## Docs

- Update `docs/architecture.md` workspace description and the mermaid
  note that says `hsh run` uses cwd.
- Update any `hsh run` usage examples in `docs/` that assume cd-first.
- Do not edit `README.md`.

## Tests

- `test/` end-to-end: add a case that runs `hsh run IMAGE <dir>` from an
  unrelated cwd and asserts the workspace mount and `hsh.workspace`
  label point at `<dir>`.
- Resume-by-workspace from an unrelated cwd.
- Backward path: `hsh run IMAGE` with no dir still uses cwd.
- `hsh run IMAGE` with a `config.hsh` in cwd no longer reads it
  implicitly; `--config config.hsh` is required.

## Open questions

1. Resume selector: positional WORKSPACE-DIR vs. `--workspace` flag.
   Positional is more consistent but overloads the resume positional
   (currently SESSION-ID). Propose: keep SESSION-ID positional for
   resume, use the `--workspace` flag for the dir path.

(Seed-dir mount anchoring is no longer an open question: dropping the
implicit `config.hsh` means mounts always anchor to WORKSPACE-DIR, and
the seed dir concept is removed.)

## Decisions

1. Resume works with `cd my-workspace; hsh run --resume` `hsh run --resume <session-id>` or `hsh run --resume --workspace my-workspace`
