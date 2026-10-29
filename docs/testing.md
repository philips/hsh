# Testing

## Functional test

`test/functional.sh` runs an end-to-end test of the sandbox, the egress
proxy, the denial database, and the example image:

- builds `hsh` and the dummy OpenAI-compatible endpoint
  (`test/dummyopenai`)
- ensures the fixture image exists (builds it with
  `podman build -t hsh-example test/fixtures/` if missing)
- **step 1**: a curl to a not-yet-allowlisted host (`api.mistral.ai`) is
  denied with 403 and recorded in a throwaway denial database, attributed
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

Run it with:

```
./test/functional.sh
```

`internal/proxy/oauth_test.go` covers the OAuth capture over both the
plain-HTTP and the TLS MITM path as Go unit tests.
`internal/denieddb` covers the single-table record lifecycle and the
`dns_denied` merge migration; `internal/secretstore` covers the store CRUD, overwrite semantics, and
file/directory permissions; `internal/config` covers secret reference
parsing (`env://`, `hsh://`, bare, unknown schemes), resolution through
a fake store, and the config rewrite for `secret add`; `internal/cli`
covers `secret add`/`rm`/`ls` behavior via `--value-stdin`.

Requirements: Go, rootless podman, network access (for `api.mistral.ai` and
the image build), and port 8899 free on the host. The test is hermetic: it
uses a temporary config, workspace, and denial database, and cleans up after
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
