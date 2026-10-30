# Testing

## Functional test

`test/functional.sh` runs an end-to-end test of the sandbox, the egress
proxy, the monitor database, and the example image:

- builds `hsh` and the dummy OpenAI-compatible endpoint
  (`test/dummyopenai`)
- ensures the fixture image exists (builds it with
  `podman build -t hsh-example test/fixtures/` if missing)
- **step 1**: a curl to a not-yet-allowlisted host (`api.mistral.ai`) is
  denied with 403 and recorded in a throwaway `monitor.db` (which carries a
  schema version, with no `denied.db` beside it), attributed
  with the session id and workspace; the session state directory holds
  the config copy and workspace file
- **step 2**: `hsh proxy allow --all --session ID` adds the denied host to
  the session's config copy (trust on first use; the seed config is
  never modified)
- **step 3**: resuming that session, the endpoint is allowed (real
  upstream response through the intercepting proxy), the workspace mount
  works, and `echo hi | pi` returns `hello world` from the dummy endpoint
  that pi is configured against (`~/.pi/agent/models.json` +
  `~/.pi/agent/settings.json` inside the sandbox)
- **step 4**: a standalone `hsh proxy` forwards to an allowlisted host,
  injects the configured secret header (observed in the dummy endpoint's
  request log), and denies a non-allowlisted host with 403
- **step 5**: the session model — resume without a session fails clearly;
  plain runs persist their pod; idle pods are `Exited` with `secrets.json`
  scrubbed; bare `--resume` re-enters the workspace's only session with
  state, proxy, and DNS intact; `--rm` runs remove themselves; a second
  session makes bare `--resume` list the ids; `--resume <id>` picks one;
  `hsh gc` collects orphaned session state
- **step 6**: `hsh ls` / `hsh rm` list and clean up sandboxes
- **step 7**: `hsh monitor` refuses to run without a terminal, and the
  old subcommand names are gone
- **step 8**: `hsh run IMAGE` uses the image's `org.ifup.hsh` label
  config (mount, allowlist, and an `hsh://` secret resolved from the
  store, matching the harness images' labels)
- **step 9**: a sandboxed OAuth login (`test/oauthsrv`): the sandbox
  sees only phantom tokens, API calls and refresh flows redeem them at
  the proxy, and the real tokens stay in the host-side store
- **step 10**: the same OAuth flow inside the `hsh-vibe` image (skipped
  if that image is not built locally)
- **step 11**: `hsh run IMAGE --tasks` — the image's agent command
  receives the numbered task prompt, agent exit codes propagate, and a
  missing agent command fails clearly
- **step 12**: network policy modes — the `https` default allows TLS to
  an arbitrary host but denies plaintext there, and resolves all DNS
  names; `--network allowlist` and `--network none` deny it
- **step 13**: the secret store — `hsh secret add` stores values (0600
  file, 0700 directory), refuses duplicates without `--force`, `secret
  ls` shows names but never values, an `hsh://` config reference resolves
  through the store and is injected (observed in the dummy endpoint's
  request log), and after `secret rm` the entry goes missing: a warning,
  no injection, the request still forwarded, and a dangling-reference
  warning without editing the config
- **step 17** (`test/detach.py`, a pty driver): Ctrl-] detaches and leaves
  the shell, secrets, and a reaper running; podman's Ctrl-P Ctrl-Q does
  nothing; `hsh run --resume` re-enters the same shell; closing the
  terminal detaches; the reaper scrubs secrets and stops the pod when a
  detached shell exits; without a reaper `hsh ls` shows `Shell exited`
  and `hsh gc` cleans up; `hsh detach` force-detaches an attached
  terminal; `--resume` refuses a session attached elsewhere and
  `--resume --take-over` takes it over; `--rm` sessions cannot detach
- **step 19**: held requests — in an `allowlist` session with
  `approval_wait` set and someone watching (the `attended` marker), a denied
  CONNECT stays pending until `hsh proxy allow` approves it and then
  succeeds; with no setting (the default) a denied request fails at once with
  the agent-directed message; `hsh proxy hold 4s` turns holding on live and an
  unapproved request is held about 4 s, then denied. The proxy unit tests
  (`hold_test.go`) cover release by approval, release by ignore, timeout,
  disabling during a hold, unattended and stale markers, `none` mode, the
  default being off, the concurrency cap, client disconnects, the `holding`
  counts, and held DNS queries
- **step 23**: ssh into a session — `ssh -N -L` through the `hsh
  ssh config` ProxyCommand reaches a node server on the sandbox's loopback;
  a 5 MiB response and eight concurrent connections cross one ssh
  connection; a closed sandbox port fails only that connection; a forward to
  `example.com` and `ssh -R` are refused and nothing reaches the egress
  proxy; `ssh hsh-<id> CMD` runs commands in the session's shell environment
  (HOME, the egress proxy in the environment, `/proc/net/dev` showing the
  inner netns's `hsh1`, exit status propagation) and `ssh -tt` runs them on
  a pty; no listeners outlive the ssh client. Skipped without an `ssh`
  client. `internal/sshd` has Go tests (in-process client over loopback TCP)
  for the destination restriction, every rejected channel and request type,
  large transfers with half-close, concurrency, recovery after a failed
  dial, and session channels (exec output and exit status, stderr as
  extended data, pty winsize via `stty size`, signals, stdin EOF, rejection
  without a spawn handler); `internal/workspace` tests the relay (`DIAL`,
  `LIST`, `PORT`, source refusal, half-close) and the `/proc/net/tcp`
  parser
- **step 24**: the network notice — a `--tasks` run whose agent command
  contains `{network_policy}` gets the read-only notice path; the managed block
  is created in a missing `~/` instruction file and, in a pre-existing file in
  the workspace, keeps the user's text with exactly one block across a resume;
  `network.notice: false` and `--network none` write no notice, drop the flag
  with its placeholder, and remove the existing block. `internal/notice` and
  `internal/workspace` have unit tests for the text, the argv substitution, and
  the block upsert and removal
- **step 25**: `hsh forward` and `hsh run --forward` — a busy `--forward` port
  fails before a session exists; `--forward 18100:18200` reaches a node server
  in the sandbox; `hsh forward --list` shows the server and not the relay's
  port; `hsh forward LOCAL:PORT` works on a running session; `hsh forward`
  with no ports forwards the running server and a second one started later;
  listeners go away with their process. `internal/forward` has Go tests for
  spec parsing, explicit and automatic forwarding (including release and
  re-pickup), busy local ports, dial-failure warnings, and a closing session
- **step 26**: schema versions — a seed config with no version, a newer one,
  or an unknown field, and an image label with no version or a newer one,
  are refused with the documented message and create no session; `secrets.db`
  and `monitor.db` with a newer `user_version` are refused; in a live session
  the session config is replaced by a newer-version file, `hsh proxy allow`
  refuses and leaves it unchanged, the running proxy keeps denying a host the
  new file would allow and logs the failed reload, `--resume` explains and
  names `hsh rm`, and `hsh rm` still removes the session
- **step 18** (`test/inline.py`): through the relay, the status line is
  drawn, a proxy denial raises the notification, Ctrl+Space takes over the
  screen with the `hsh monitor` view and pauses the session's output,
  space+enter adds the host to the session config, `q` closes and delivers
  the held output, and `monitor.enabled=false` attaches directly. `internal/relay` has Go tests for the pause/restore
  equivalence (the user's terminal, a second emulator fed only the
  relay's output, equals the relay's emulator), the status row under
  scroll, `clear`, the alternate screen and scroll-region resets, a
  full-screen overlay driven by a fake bubbletea model (keys, commands,
  size, close and restore), the in-band hint, kitty-encoded keys, and
  resize; `internal/cli` checks that the overlay is the real monitor model
  scoped to the session and that approving reaches the session config; they
  skip without `GOEXPERIMENT=simd`
- **agent-to-monitor isolation** (`test/security.py` plus
  `internal/relay/security_test.go` and the fuzz targets in
  `internal/relay/fuzz_test.go` and `internal/vt`): terminal replies to
  agent-provoked queries (DSR, DA, DECRPM, kitty flags, mouse, focus,
  paste markers, key-release events) never match the monitor key for any
  valid `monitor.key`, the agent's output can never open, close, or feed
  the review screen, forged `OSC 7777` hints are inert (they poll the real
  database and carry no text), scroll-region writes can never address the
  status row — including a split-write smuggle that once reassembled a
  live DECSTBM past the overlong-param give-up (fixed by dropping the
  introducer byte instead of emitting it) and a region reset smuggled
  behind an aborted CSI (`ESC[ESC[r`, fixed by aborting and re-parsing
  at the embedded escape) — and the same properties are
  fuzz-enforced on arbitrary split byte streams (`Key.find` only accepts
  the genuine encodings, the input parser and both filters are reassembly-safe and bounded)

Run it with:

```
./test/functional.sh
```

`internal/proxy/oauth_test.go` covers the OAuth capture over both the
plain-HTTP and the TLS MITM path as Go unit tests.
`internal/monitordb` covers the single-table record lifecycle, the schema
version, and concurrent first opens; `internal/dbschema` covers the
`user_version` check (new, adopted, newer, older, concurrent) shared by
`monitor.db`, `session.db`, and `secrets.db`; `internal/secretstore` covers the
store CRUD, overwrite semantics, file/directory permissions, and adopting an
unversioned store with its secrets; `internal/config` covers secret reference
parsing (`env://`, `hsh://`, bare, unknown schemes), resolution through
a fake store, the config rewrite for `secret add`, the version rules (missing,
newer, older, unknown fields, editors refusing unreadable versions), and a
schema test (`testdata/schema-v<N>.txt`) that fails when the config fields
change without a `Version` bump (update it with `go test ./internal/config
-run TestSchemaMatchesVersion -update-schema`); `internal/proxy` covers a
reload with a newer config keeping the old policy; `internal/cli`
covers `secret add`/`rm`/`ls` behavior via `--value-stdin`.

Requirements: Go, rootless podman, network access (for `api.mistral.ai` and
the image build), and port 8899 free on the host. The test is hermetic: it
uses a temporary config, workspace, and monitor database, and cleans up after
itself. When a step fails, the artifacts are kept in a temp directory (the
path is printed) for debugging.

The dummy endpoint listens on `0.0.0.0:8899` because connections from the
sandbox reach the host through the pasta gateway, which does not forward to
loopback-only listeners. The endpoint also serves its instance id at `/id`
(and as the `X-Dummy-Id` header) so the test can detect another process
already squatting on the port.

## Manual terminal checks

The relay writes escape sequences to the user's real terminal, which the
automated tests only model. `docs/terminal-checks.md` is the checklist to run
by hand per terminal (scroll region and scrollback, the review screen,
paste, mouse, and keyboard protocols).

## Measurement tools

`test/measure/` holds manual tools used to design how agents are told about
network restrictions (plans/0028-network-notice.md): `clients.sh` runs curl,
wget, Python, Node, npm, git, and Go against a stand-in denying proxy
(`test/measure/denyproxy`, with configurable status, reason phrase, body, and
headers) and records what each client shows and whether it retries;
`agent-trial.sh` runs pi with a Mistral model through an hsh allowlist session
for several wordings of a notice and counts tool calls, tokens, and recorded
denials (it spends tokens and needs the `hsh://mistral-api-key` secret).
`VARIANTS="v5 c0 c1"` runs the shipped notice through `{network_policy}` on a
blocked host, and the over-caution pair: an `https`-mode task on a reachable
host without (`c0`) and with (`c1`) the notice.

## Nesting tests (hsh inside an hsh sandbox)

Running hsh (and podman) inside an existing sandbox fails by design: the
session entrypoint drops all capabilities except `CAP_DAC_OVERRIDE`, and
podman inside a user namespace needs more than that to re-execute itself
into a fresh user namespace, apply image layers, and create containers.

`HSH_NESTING=1 hsh run ...` is a test-only escape hatch. The workspace
container receives the variable, and the session entrypoint keeps this
capability set alongside `CAP_DAC_OVERRIDE` (and trims the capability
bounding set to the same set, so a nested podman prunes its container specs
to what the session can grant):

```
CAP_SETUID CAP_SETGID CAP_SETFCAP CAP_SETPCAP
CAP_SYS_ADMIN CAP_NET_ADMIN CAP_CHOWN CAP_FOWNER CAP_FSETID
```

The requirements behind each capability: `SETUID`/`SETGID`/`SETFCAP` for
podman's user-namespace re-exec and `uid_map`/`gid_map` writes,
`SYS_ADMIN`/`NET_ADMIN` for the inner runtime's mounts and networking,
`CHOWN`/`FOWNER`/`FSETID` for applying image layers with the `vfs` driver,
`SETPCAP` for the inner runtime's capability filtering. `NO_NEW_PRIVS` and
the default seccomp profile stay in effect; no `--userns` changes are
needed (the per-container user namespace of the session works).

The session entrypoint also configures nested podman automatically, so a
nested `hsh run` works without further setup:

- a `vfs` storage config at `/tmp/podman-nest/storage.conf`, exported as
  `CONTAINERS_STORAGE_CONF` (the default overlay driver cannot unpack
  layers inside a sandbox: `after fallback to chroot: operation not
  permitted`)
- a `containers.conf` override at `/tmp/podman-nest/containers.conf`,
  exported as `CONTAINERS_CONF_OVERRIDE`, with `cgroups = "disabled"`,
  `cgroupns = "host"`, `netns = "host"`, and `cgroup_manager = "cgroupfs"`
  (the cgroup filesystem and `/proc/sys` are read-only inside a sandbox:
  without it, `podman pod create` fails at `creating cgroup path
  /libpod_parent/...` and `podman run` fails at netavark sysctl writes)

```
podman pull docker.io/library/busybox
podman run --rm docker.io/library/busybox echo ok
```

Inside a session the entrypoint also sets `HSH_NESTED=1`. A nested
`hsh run` sees it and creates its pod with `--network none` and an infra
container taken from the session image instead of the pause image (the
pause image's rootfs is a native overlay mount, and netavark cannot set
up a pod network on the read-only `/proc/sys` of a sandbox). The nested
session is therefore network-isolated: its shell has no egress, but the
nested `hsh run` can still pull images, since the pull happens in the
outer session's shell before the pod is created.

For nested `hsh run`, the session image must contain podman; any image with
an `org.ifup.hsh` label works, e.g. build one from `quay.io/podman/stable`
and run the inner hsh with the binary available in the workspace mount:

```
cat > nest.Containerfile <<'EOF'
FROM quay.io/podman/stable:latest
LABEL org.ifup.hsh='{"version":1,"shell":"/bin/bash"}'
EOF
podman build -t nest-podman nest.Containerfile
HSH_NESTING=1 hsh run localhost/nest-podman .
```

Prefer testing nesting with a pre-pulled image (`hsh run --pull never`)
to keep the inner environment off the network. Nesting is only supported
on the default runtime and a real kernel; inside a gVisor sandbox the
nested `uid_map` write is refused by the sentry regardless of capabilities
(see docs/gvisor.md).
