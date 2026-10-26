# Initial tasks: headless agent runs with `hsh run --tasks`

## Goal

Run an agent harness non-interactively against a list of tasks:

```
hsh run ghcr.io/philips/hsh-pi --tasks setup-tasks.txt
```

where `setup-tasks.txt` is a newline-delimited list of tasks:

```
Install gh for github access and clone https://github.com/philips/hsh
Setup a Golang environment using the latest stable release
```

Inspired by [google/ax](https://github.com/google/ax): declare an
agent workload (image + tasks) and have the platform sandbox it, wire
up the workspace, and run it to completion. Unlike ax there is no
cluster or control plane — the sandbox is the existing hsh pod, and
"running the workload" means starting the harness headless with the
tasks as its initial prompt, streaming output, and propagating the
exit code.

## UX

- `hsh run [IMAGE] --tasks PATH` — `IMAGE` is a new optional positional
  argument, equivalent to `--image` (the example above needs it). A
  positional argument is treated as the image; `--config` and other
  flags are unchanged.
- `--tasks -` reads the task list from stdin.
- Task file syntax: one task per line; blank lines and lines starting
  with `#` are skipped. Every task is kept verbatim (no rewrapping).
- The tasks are composed into a single numbered prompt:

  ```
  Complete the following tasks:

  1. Install gh for github access and clone https://github.com/philips/hsh
  2. Setup a Golang environment using the latest stable release
  ```

  One agent session receives all tasks — harnesses handle multi-task
  prompts, and it mirrors what a user would type at an interactive
  session. (Per-task agent invocations are a possible follow-up; see
  open questions.)
- `--tasks` implies a headless run: stdin is not a terminal, output
  streams to stdout/stderr, and hsh exits with the agent's exit code.
  Default lifecycle is unchanged (pod removed on exit; `--resume`
  keeps it, and a later `--resume --tasks` re-runs the agent in the
  persisted sandbox).

## Agent invocation

Each harness starts headless differently (`pi -p`, `vibe -p`,
`claude -p`, `codex exec`). The image declares its invocation in the
existing `org.ifup.hsh` label so `hsh run` stays harness-agnostic:

```json
"agent": {"command": ["pi", "-p", "{prompt}"]}
```

- `command` is an argv template; `{prompt}` (exactly one element or
  several) is replaced with the composed prompt. Substitution happens
  at the argv level — no shell quoting of the prompt is needed.
- Works with or without a config file (label fallback already exists).
- `hsh run --tasks` without an `agent.command` in the effective config
  fails with a message pointing at the label.

## Mechanics

- `hsh run` reads the task file on the host, composes the prompt, and
  passes the agent argv to the workspace container (not mounted; the
  prompt is argv, so the task file never needs to be inside the
  sandbox).
- The workspace entrypoint (`hsh workspace`) gains an exec mode:
  `--exec ARGV...` execs the given argv directly instead of the
  interactive shell, with the same environment (proxy vars, workspace
  dir, HOME) the shell gets. `hsh run` passes `--exec <agent argv>`
  when `--tasks` is given. No new container wiring is needed beyond
  this flag.
- Exit-code propagation and cleanup reuse the existing `hsh run`
  paths (`podman run` for the default flow, attach for `--resume`).

## Implementation sketch

- `internal/config`: `Agent struct { Command []string }`, field
  `"agent"` in the config/label.
- `internal/cli/run.go`:
  - positional image argument; `--tasks PATH` flag; task-file parsing
    and prompt composition (share the parser as a small function so it
    is unit-testable);
  - when tasks are given, append `--exec <argv...>` to the workspace
    container args instead of the plain `--shell` invocation;
  - error when no `agent.command` is configured.
- `internal/cli/workspace.go` / `internal/workspace`: `--exec` mode;
    exec argv with the built environment.
- `images/{pi,vibe,claude-code,codex}/Containerfile`: add
  `"agent": {"command": [...]}` to the `org.ifup.hsh` label:
  - pi: `["pi", "-p", "{prompt}"]`
  - vibe: `["vibe", "-p", "{prompt}"]`
  - claude-code: `["claude", "-p", "{prompt}"]`
  - codex: `["codex", "exec", "{prompt}"]`
- `docs/images.md` (label schema + example), `docs/config.md`
  (`agent` field), usage in `main.go`.
- Unit test: task-file parsing (blank lines, comments, numbering).
- `test/functional.sh`: a label-test image with
  `"agent": {"command": ["/bin/sh", "-c", "echo TASKS:{prompt}"]}`
  and a two-line tasks file; assert the output contains both numbered
  tasks; assert non-zero exit propagation (a second run with
  `exit 3`); assert `hsh run` without an agent command fails cleanly.

## Verification

1. `hsh run localhost/hsh-pi --tasks setup-tasks.txt` runs pi
   headless with the numbered prompt in the workspace; the pod is
   removed on exit and hsh exits with pi's exit code.
2. A missing or empty (all comments) tasks file fails with a clear
   error; `--tasks -` reads stdin.
3. Task files with blanks and `#` comments produce the expected
   numbered prompt (unit test).
4. Exit codes propagate (0 and non-zero).
5. `--tasks` works with `--resume` (agent re-runs in the persisted
   sandbox).
6. Images carry the agent label; `podman image inspect` shows valid
   JSON. Functional test passes.

## Open questions

- Per-task invocations (run the agent once per task, stop at first
  failure)? Adds resumability semantics (`ax`-like task phase
  tracking) but also state; proposed no for now — one session, one
  prompt.
- Whether to also pass the task list as a file into the workspace
  (e.g. `/workspace/.hsh-tasks.md`) so the agent can re-read it;
  proposed no: the prompt carries it, and files in the workspace
  persist in the user's directory.
- Resource/turn/cost limits on headless runs (ax's `maxTurns`-style
  controls): the harnesses' own flags can be embedded in the agent
  command template per image; a first-class `hsh` limit is out of
  scope.
- Concurrent headless runs of the same image: already works (random
  pod names); only `--resume` is keyed to the workspace.
