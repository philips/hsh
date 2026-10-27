# Auto-launch the agent harness from the session shell

> **Status: implemented.** The `org.ifup.hsh` label's new
> `agent.launch` argv runs from a bash rcfile with job control enabled,
> so interactive sessions start in the harness and Ctrl-Z suspends it
> into the bash session; piped sessions launch nothing.

## Goal

Container images drop into bash; the agent harness (e.g. pi) should be
launched automatically from bash, with the bash session still there so
the user can Ctrl-Z the harness and muck around without leaving the
container session.

## Design

- `agent.launch` (config field, image label): argv to launch in
  interactive sessions.
- The workspace-inner writes an rcfile into the session home and execs
  bash with `--rcfile`. The rcfile: `set -m` (job control during
  rcfile execution — without it Ctrl-Z stops the whole session, not
  the job), source `$HOME/.bashrc`, then run the launch argv as a
  foreground job.
- bash reads the rcfile only when interactive, so piped/scripted
  sessions are unaffected. Headless `--tasks` runs keep using
  `agent.command`.
- The harness images' labels declare `launch` (pi, vibe, claude,
  codex); a labelcheck test pins it.

## Verification (all in test/functional.sh, step 14)

1. A piped session with a `launch` label does not auto-launch.
2. A pty-driven interactive session auto-launches the harness, Ctrl-Z
   suspends it to the bash prompt, `jobs` lists the stopped job, and
   the shell remains usable afterwards.
