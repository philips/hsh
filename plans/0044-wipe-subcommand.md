# hsh wipe

During the alpha period config/state schemas can change between releases;
`hsh wipe` gives users a one-command clean slate.

## Behavior

- `hsh wipe` (no flags, no arguments) removes, in full:
  - the config dir `$XDG_CONFIG_HOME/hsh` (default `~/.config/hsh`):
    settings.json, secrets.db, oauth-providers/, shortnames.conf
  - the state dir `$XDG_STATE_HOME/hsh` (default `~/.local/state/hsh`):
    sessions/, monitor.db, metrics.db, crash.log
- Before deleting it prints both paths and requires the user to type
  `YES` (exact, case-sensitive) at a prompt. Anything else aborts with
  nothing removed.
- If hsh pods exist it refuses and points at `hsh rm --all --force` (wiping
  state under a live sandbox breaks the pod). If the podman check itself
  fails (broken podman is one reason to wipe) it prints a warning and
  continues after the YES.
- `hsh wipe` skips the auto-update hooks in `Run`, so the detached
  update-check child cannot recreate the state dir after the wipe.

## Implementation

- `internal/cli/wipe.go`: `wipeCommand()` + `runWipe(in, out, pods, podsErr)`;
  the pods parameters keep the function testable without podman.
- registered in `Root()` after `update`.
- `internal/cli/wipe_test.go`: temp XDG dirs, YES wipes, wrong answer keeps
  everything, existing pods refuse.
- document in `internal-docs/architecture.md` (lifecycle/cleanup); remove
  the TODO entry when done.
