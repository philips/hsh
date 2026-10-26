# Idle sessions: stop the whole pod on exit, scrub secrets while idle

> **Status: implemented.** Depends on the session model and state
> layout in plans/0014-workspaces.md; land after it (and after
> plans/0018-ff-cli-migration.md, which reserves the `--resume`
> positional the session model uses).

## Goal

Under plans/0014-workspaces.md, `hsh run` sessions persist by default:
the pod survives exit so the workspace filesystem can be re-entered.
That makes the idle state the common case, so it must be cheap and hold
no secrets:

- an idle session stops its **entire pod** — the proxy container and
  the pod's infra container (verified: podman auto-starts the infra
  container when any container in the pod is started, so reattach needs
  no changes)
- an idle session holds **no plaintext secret material**: the resolved
  `secrets.json` is scrubbed at exit and re-resolved on reattach from
  the session's config copy in the state directory

## Verified behavior (podman 5.8.9)

- `podman pod stop` stops the proxy and the infra container; the pod
  survives in `Exited` state and `hsh ls`/`hsh rm` keep working on it.
- `podman start <container>` on a container in a stopped pod
  **auto-starts the infra container first**. hsh's reattach path already
  does `podman start <proxy>` then `podman start -ai <ws>`, so the
  stopped-infra case exercises existing branches only.
- Stopping the pod kills the per-pod **pasta** process (one per pod
  network namespace). Full pod stop saves two processes per idle
  session: pause (infra) + pasta.
- The pod network namespace is already per-launch state for hsh: the
  workspace entrypoint deletes the stale veth pair on re-entry and
  rewires it (`CreateHostVeth` + `waitForProxy`), and pasta recreates
  the outbound path and `host.containers.internal` on start. Nothing
  that survives idle lives in the pod netns.

## Design

### Exit cleanup (persistent sessions)

When the workspace container exits (normal exit, error exit, or the
SIGINT/SIGTERM handler), `hsh run`:

1. unlinks `sessions/<session-id>/secrets.json`
2. runs `podman pod stop --time 2 <pod>`

The `--time 2` grace lets the proxy close its sqlite WAL cleanly on
SIGTERM before SIGKILL (an immediate `--time 0` relies on crash recovery
every session). The existing ephemeral (`--rm` / non-persistent) path
keeps its `pod rm -f` cleanup.

A SIGKILL of `hsh run` skips cleanup entirely: the pod is left fully
running. That state is tolerated — reattach starts the already-running
proxy via the normal path, and `hsh gc` correctly ignores it (the pod
exists, so it is not garbage).

### Reattach

Unchanged: `podman start <proxy>` (auto-starts the infra), then
`podman start -ai <ws>`. Reattach additionally re-resolves secrets
from the session config copy and rewrites `secrets.json` before starting
the proxy — so scrub-at-exit and reattach compose.

### Idle session state

```
sessions/<session-id>/
    config.json     intact (the writable config)
    ca.crt ca.key   intact (stable trust anchor for the session)
    secrets.json    ABSENT while idle (scrubbed at exit, recreated on reattach)
    workspace       intact
```

Key material exists only while the session has a running proxy.

## Implementation sketch

- `internal/cli/run.go`: exit cleanup for persistent sessions calls the
  scrub + `podman pod stop --time 2`; same for the signal handler; the
  reattach path re-resolves secrets before starting the proxy.
- Functional test: between `--resume` launches, assert the pod is
  `Exited`, the pasta process count drops, and `secrets.json` is absent
  from the session dir; assert re-entry still works end to end
  (workspace marker, proxied request, DNS).

## Verification

1. `hsh run` (persistent) exits → pod `Exited`, no hsh pasta process,
   no `secrets.json` in the session dir.
2. `hsh run --resume` re-enters: infra auto-starts, proxy starts,
   secrets re-resolve, workspace marker intact, proxied request and DNS
   work.
3. SIGKILL the host-side `hsh run` mid-session → pod left running;
   reattach tolerates it; no double-start errors.
4. `hsh ls` lists the idle (Exited) session; `hsh rm` removes pod +
   session dir.
5. Full `test/functional.sh` pass.

## Decisions (resolved)

- Idle = whole pod stopped (infra included), not just the proxy: two
  processes saved per session, reattach verified to auto-start the
  infra.
- `secrets.json` is scrubbed at exit and re-resolved on reattach —
  idle sessions hold no key material.
- `podman pod stop --time 2` for the grace period; `--time 0` rejected
  (sqlite WAL crash recovery every session).
- Crash-orphaned running pods are tolerated by design; `hsh gc` only
  collects sessions whose pods are gone.
