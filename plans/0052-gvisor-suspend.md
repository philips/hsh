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
