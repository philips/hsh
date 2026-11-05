Primary goal: be able to suspend/resume a session using gVisor

Secondary goal: is it possible to checkpoint when the proxy gets a request that
would be denied and if the user approves it restore the sandbox back in time to
try and keep the agent from seeing time outs or other errors? This would save a
lot of tokens from retries.

## hsh considerations

Checkpoint triggers:

- `hsh suspend` or a monitor command similar to "Session: detach" if a user is
  trying to pause work to save resources, etcc/ `hsh resume` would continue work
- proxy integration for trying to avoid burning tokens on denials

`hsh resume` decides what to do from the session's state, using the same logic
`sessionStatus` already has for `Attached`, `Detached`, `Shell exited`, and
`Exited`. A later `Suspended` state, meaning a checkpoint image exists in the
session dir, would add one more case: run `runsc restore` instead of starting
the container. Do not scatter state checks in a way that would block that.

Potential obstacles:
 - The embedded runsc payload drops `checkpointgofer`.
 - `podman container checkpoint` expects CRIU-style runtime flags.
 - The proxy's netstack socket is recreated on each start, so a restored
   sandbox must reconnect to it.

## Prototype findings (2026-11-04)

Tested against the embedded runsc (release-20261026.0) under rootless
podman 5.8.9.

Two mechanisms, very different costs:

### 1. pause/resume (freeze CPU) -- works today, shipped as the prototype

`runsc pause` / `runsc resume` (equivalent to SIGSTOP/SIGCONT of the
sentry, but gVisor-native) work with the embedded payload, no extra
binaries:

- `podman pod pause hsh-<id>` freezes the sandbox. Status reports `paused`.
  `podman pod unpause` resumes it. Both go through the runsc runtime
  cleanly (needs `--security-opt label=disable`, which hsh already sets).
- Latency ~5-6 ms each way.
- A counter loop inside the guest stays frozen across the pause (4 -> 4,
  not 4 -> 8), confirming guest processes do not execute.
- Memory stays resident; only CPU is saved. Survives nothing (a reboot
  loses it).
- Caveat for the token-saving goal: the guest *clock is not rewound*.
  Guest wall-clock elapsed == host elapsed across an 8 s pause. So
  pausing during an approval hold stops the agent from running, but on
  resume its timers observe the full elapsed wall time. Good for short
  holds (seconds); a minutes-long hold can still trip an agent timeout.

Shipped prototype: `hsh suspend [SESSION]` (detaches any client, then
`podman pod pause`), a `Suspended` status in `sessionStatus`, and an early
`podman pod unpause` in the resume path so `hsh resume` wakes it. Pod-level
pause also freezes the proxy, which is why the unpause must run before the
proxy/ws start logic, not in the reattach block.

### 2. checkpoint/restore (free memory) -- needs one payload change

- `checkpointgofer` *is* shipped in the upstream release tarball
  (`gvisor-bin/checkpointgofer`, ~48 MB); `payload.sh` drops it. With it
  dropped, `runsc checkpoint` hangs indefinitely (no error).
- Adding it back to `FILES` in `payload.sh` makes checkpoint and restore
  work end to end: `runsc checkpoint --image-path=<dir> <ctr>` writes
  `checkpoint.img` + `pages*.img`, and `runsc restore --image-path=<dir>
  --bundle=<bundle> --detach <ctr>` brings the guest back with its
  process state intact. Cost: +~48 MB to the embedded binary (vs the
  current ~30 MB payload), so this wants to be optional/lazy.
- `runsc checkpoint` directly (not `podman container checkpoint`) avoids
  the CRIU-style flags podman expects; podman's checkpoint path assumes
  criu. Driving runsc against podman's runsc `--root`
  (`/run/user/<uid>/runsc`) and the stored OCI bundle is the way in.
- Clock caveat is worse than pause: on restore the guest realtime jumps
  to host wall-clock (observed a 10 s jump across an 8 s outage). gVisor
  does not rewind realtime, so "restore back in time to hide a denial"
  does not transparently stop realtime-based timeouts; monotonic-based
  timeouts fare better.
- Still open: the netstack socket must be re-dialed on restore. The proxy
  recreates the socket on each start and runsc dials it once at sandbox
  start; a restored sandbox re-runs that dial, so the proxy must be
  listening first (same ordering `startProxy`/`waitForSocket` already
  enforces). Not yet tested with a live netstack connection.

### Recommendation

- Ship pause/resume as `hsh suspend` now (done in this prototype): zero
  payload cost, instant, covers the "pause work to save CPU" use case and
  the short-hold proxy UX (freeze the agent while a fedora-mirror-style
  denial waits for approval, instead of letting it spin and burn tokens).
- Treat checkpoint/restore as a later, heavier feature gated on shipping
  `checkpointgofer` and solving the netstack reconnect + clock questions.
  The `Suspended` state the plan wants already exists for the pause case
  and can grow a second "checkpoint image on disk" case without
  scattering status checks.

### 3. freeze-on-hold for the proxy (prototype, opt-in)

The secondary goal (avoid burning tokens on a denial the user will
approve) is prototyped with pause, not checkpoint-to-disk, because the
agent's in-flight connection must survive the wait. Killing the sandbox
for a disk checkpoint drops the proxy's per-connection netstack stack and
with it the held TCP connection, so the approved request could not
complete. Pause keeps the connection open.

How it works (`HSH_FREEZE_ON_HOLD=1`, opt-in):

- The proxy already bumps a `holding` count in `proxy.db` for each request
  it is holding for approval (`monitordb.AdjustHolding`).
- A host-side watcher in `hsh run`/`resume` (`watchHoldFreeze`) polls
  `monitordb.HoldingCount(sessionDir)` every 150 ms. When a hold has been
  active for longer than `network.freeze_delay` (grace period, default
  1000 ms) it `podman pause`es the *workspace container only* (the proxy
  keeps running to serve the approval); when the count returns to zero it
  `podman unpause`s and resets the timer.
- The grace period keeps a burst of quick holds -- e.g. dnf/apt fanning
  out across mirrors -- from flapping the agent: holds that resolve within
  `freeze_delay` never freeze, and the denials they record can be
  approved together with `hsh proxy allow --all`. Set `freeze_delay` to
  `off` to freeze immediately, or a duration up to 1m.
- `approval_wait` and `freeze_delay` resolve session over global over
  builtin: an explicit value in the session's `network` config wins,
  then `$XDG_CONFIG_HOME/hsh/settings.json` (`approval_wait`,
  `freeze_delay`; edit with `hsh settings edit`), then the built-in
  defaults (30s and 3s). Holding is therefore on by default when someone
  is attending. `hsh run` passes the resolved global wait to the proxy
  container as `--approval-wait-default` (the proxy cannot read the
  host's settings file); a session config with `approval_wait` set
  overrides it live. Adding `freeze_delay` bumped the config schema to
  version 2 (v1 configs still load; the field is optional).
- Enable the whole behavior with `HSH_FREEZE_ON_HOLD=1`.

- Approving with `hsh proxy allow` (or the monitor) releases the proxy's
  hold, the count drops, the watcher thaws, and the *same held request*
  proceeds.
- Freeze/thaw feedback goes to the relay's status line, not stderr: the
  watcher and the inline monitor share a `freezeState`; when a relay is
  attached it polls a `FreezeNote` callback (like `Notice`) and paints
  "SUSPENDED: proxy approval | * N to review (M held)" in the status
  line (yellow, like the red MONITOR mode) instead of scribbling over
  the session text. The status
  line reads "hsh <session>" with no separator pipe between them.
  Without a status line (monitor disabled, non-tty, relay unavailable)
  it falls back to stderr lines.

`freeze_delay` is a grace period *before* freezing, not a hard cap -- once
frozen, the hold still runs its normal `approval_wait`.

Verified end to end (allowlist mode, holding on, attended fresh): an agent
`curl https://example.org` hit the gate, the watcher froze the workspace
(status `Suspended`), `hsh proxy allow` thawed it, and the original curl
completed with HTTP 200 -- the agent saw no error and no timeout.

Config/database sync (fixed during prototyping): the review UI used to treat
any decision row in monitor.db (written by `hsh proxy allow` or the monitor)
as "allowed" forever, so manually removing a host from the session config
made the proxy deny and hold it again while the review list still said
"nothing to review" -- the agent froze with no visible reason. The config is
now the authority: `rows()` marks a denial Allowed only when the session's
current `network.allow` matches the host (`config.HostMatches`), keeps the
decision row for the approval timestamp and the ignore flag, and `Pending`
excludes ignored hosts explicitly instead of via the overloaded Allowed
flag. Removing a host from the config puts it back in the review list with
its live holding count; re-adding it (by hand or with `hsh proxy allow`)
hides it again.

Limitations of this prototype:

- The watcher runs only in the attached `hsh run`/`resume` process, so it
  covers the attached experience. A detached session would need the same
  loop in the reaper.
- Freezing is coarse: any hold freezes the whole workspace, including
  unrelated work the agent might be doing concurrently. Fine for a single
  agent; a shell doing other things would stall too.
- The guest clock still advances while frozen (see finding 1), so a hold
  longer than the agent's own request timeout can still surface an error
  on thaw. The 30 s default hold is well within typical timeouts.
- Pause keeps memory resident; this saves tokens/retries, not RAM.
