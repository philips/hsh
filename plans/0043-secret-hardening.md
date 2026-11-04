# Secret hardening

> **Status: sections 1, 2 and 5 implemented, and section 3 as deny by
> default with `--trust-image` (see "How it landed"); the interactive
> review and section 4 (keyring backend) are tracked in TODO.md.**

## Goal

Tighten who can read and steer hsh's credentials: static secrets, OAuth
logins (plan 0042), and the per-session MITM CA keys. The sandboxed
workspace is already kept away from all of them. This plan is about the
next ring out:

- **a compromised proxy.** The proxy parses untrusted traffic from the
  sandbox (HTTP, TLS interception, token bodies). A bug there must not
  hand over every session's credentials.
- **an untrusted config source.** An image label (anyone can publish
  an image) or an agent-writable config file must not decide where a
  user's secrets are sent.
- **secrets at rest** on the host.

The rule from plan 0042 applies throughout: a session writes only into
its own session directory, and anything that has to leave it is pulled
out by a host-side process.

## Findings (current state)

F1. **Every proxy mounts the whole state directory read-write**
   (`$XDG_STATE_HOME/hsh` at `/hsh-state`, run.go). From any session's
   proxy container that exposes, for every session:
   - `sessions/*/secrets.json`: resolved static secret values and OAuth
     login access tokens,
   - `sessions/*/ca.key`: other sessions' MITM CA private keys,
   - `sessions/*/proxy/oauth-tokens.json`: captured **real** access and
     refresh tokens,
   - `sessions/*/config.json` and `oauth-phantoms.json`, **writable**. A
     compromised proxy could add an allowlist entry or a secret route to
     another session's config, and the host would act on it at the next
     reload or launch,
   - the shared `monitor.db` (denials, holds, approval decisions), which
     the proxy writes directly.

   The proxy needs exactly its own session directory (read-only), its own
   `proxy/` directory (read-write), and a way to report denials and
   receive approvals.

F2. **Image labels choose where static secrets go.** A label can declare
   `"from": "hsh://anthropic-api-key", "hosts": ["evil.example"]` and the
   proxy injects the key there. A label can also declare
   `"from": "env://AWS_SECRET_ACCESS_KEY"` and send any variable from the
   user's environment to any host. The shipped labels are checked in the
   unit tests (hsh:// only), but third-party images are not. OAuth logins
   are already safe from this: their routes come only from user-owned
   provider descriptors.

F3. **Workspace config files have the same power.** `hsh run --config
   ./config.hsh` reads a file an agent in a previous session could have
   edited. The session's own `config.json` lives in the state directory,
   out of the workspace's reach (but see F1).

F4. **`secrets.db` is plaintext at rest** (0600, like `~/.netrc`), as are
   the capture stores and `secrets.json` while a session runs.

F5. **A failed OAuth login is only visible** in the proxy's 401 body and
   `hsh secret ls`. No monitor notice.

F6. **Fail-closed covers configured token endpoints only.** A
   token-bearing response from another path on an auth host (a
   descriptor with a wrong path) passes through.

Already in place (keep, and test): values are never logged (proxy,
companion); `secrets.json` is removed when a session goes idle; OAuth
refresh tokens never leave the host process; seeds and the workspace only
ever see phantoms.

## Plan

### 1. Each proxy sees only its own session (F1)

Mounts for the proxy container:

| Host path | Proxy path | Mode |
|---|---|---|
| `sessions/<id>/` | `/hsh-session` | ro (as today) |
| `sessions/<id>/proxy/` | `/hsh-proxy-state` | rw (as today) |
| the state directory | — | **no longer mounted** |

What used the state directory mount:

- **Denials and holds** (`monitordb.Record`, `AdjustHolding`): the proxy
  writes them into a per-session database in its own directory,
  `sessions/<id>/proxy/monitor.db`, using the existing monitordb schema.
  Host readers (`hsh proxy denied`, the monitor TUI, inline review) read
  across sessions. Either they union the per-session databases, or a
  host-side process copies them up into the shared `monitor.db`. Decide
  in the implementation; copying up keeps the readers unchanged and fits
  the data-flow rule.
- **Approval decisions** (`LookupDecision`): written by the host into a
  host-owned file in the session directory (read-only to the proxy), for
  example `sessions/<id>/decisions.json`, which the proxy watches like
  `secrets.json`. Holding requests keep working because the proxy polls
  it.
- The gVisor netstack socket directory stays as its own mount.

Verification: a functional test runs two sessions, then `podman exec`
into one proxy and checks that it cannot see the other session's
`secrets.json`, `ca.key` or `proxy/oauth-tokens.json`, and cannot write
any file outside its own `proxy/`. Denial review and approval holds keep
passing (existing steps 18 and 19).

### 2. Static secrets are bound to the hosts the user approved (F2, F3)

- `secrets.db` records approved hosts per static secret (a `hosts`
  column, migration to schema version 3). `hsh secret add NAME --host H
  ...` sets them, and `hsh secret allow NAME HOST` / `hsh secret disallow`
  edit them.
- At launch, a config route `secrets.X = {from: hsh://NAME, hosts: [...]}`
  is injected only for hosts in NAME's approved set. Narrowing is fine;
  any other host is dropped with a warning that names the source (image
  label or config file) and the `hsh secret allow` command.
- **Interactive first use:** when a route names a host not yet approved,
  `hsh run` asks once (`allow hsh://anthropic-api-key to be sent to
  api.anthropic.com? [y/N]`) and records the answer. Non-interactive runs
  refuse.
- **`env://` from image labels is refused** at load. Labels must use
  `hsh://` (all shipped images already do). User config files may still
  use `env://`, and those references are covered by the same per-host
  approval (keyed by the variable name).
- Existing secrets without recorded hosts get them from their first
  approved use, so nothing breaks silently. The prompt explains the
  change once.

Verification: a test label that routes a stored secret to an unapproved
host gets no injection and a clear warning. Approving the host makes it
work. A label with `env://` fails to load.

### 3. Image label review (F2, later)

On the first `hsh run` of an image digest, show what its label asks for
(allowlist, secret routes, OAuth providers, agent command) and remember
the approval per digest in `$XDG_CONFIG_HOME/hsh/`. With (2) in place
this is defence in depth, not the main control. Decide after (2) whether
it is worth the prompt.

### 4. Secrets at rest (F4, optional)

Options, all keeping `hsh://NAME` references unchanged:

- a `keyring://NAME` backend (Secret Service / macOS Keychain) for static
  secrets. Unattended OAuth refresh (the companion) then depends on an
  unlocked keyring, which headless servers often lack;
- encrypting the value columns of `secrets.db` with a key held in the
  OS keyring, with the same unlock caveat;
- documenting that `secrets.db` and the state directory should live on
  an encrypted home directory.

Proposed: document the trade-off now, and add `keyring://` only on
demand.

### 5. Smaller items (F5, F6)

- A monitor notice (status line) when a login the session uses is marked
  failed, next to the existing 401 body.
- Extend the fail-closed scan to every 2xx JSON response from a
  provider's token-endpoint **host** (not only the exact path), so a
  wrong descriptor path blocks instead of leaking.
- A test that greps proxy and companion logs from the functional run for
  the fake providers' real token strings.

## Order

1. Proxy mounts (1). It closes the largest exposure, needs no new user
   interaction, and is mostly plumbing.
2. Host binding for static secrets and refusing `env://` in labels (2).
3. Smaller items (5).
4. Label review (3) and secrets at rest (4) when there is demand.

## Open questions

- Per-session monitor databases: union them in the readers, or copy them
  up into the shared `monitor.db` with a host-side process?
- Should `hsh secret add` require at least one `--host`, or keep relying
  on the first-use prompt?
- `env://` in user config files: keep it with per-host approval, or
  deprecate it in favour of `hsh secret add`?

### Decisions

- Union the monitors in the readers
- hsh secret add should take a host or list of hosts. we should also have a hsh secret edit that opens an editor for easier UX with long lists
- yes, deprecate env:// in user config files and images- too dangerous

## How it landed

### 1. Proxy mounts

- The proxy no longer mounts the state directory. Its host paths are the
  session directory (ro, `/hsh-session`), its own `proxy/` (rw,
  `/hsh-proxy-state`), and the gVisor `netstack/` socket directory.
- Two SQLite databases per session, one writer each:
  `sessions/<id>/proxy/proxy.db` (the proxy's denials and hold counts) and
  `sessions/<id>/monitor.db` (the host's approve/ignore decisions, read by
  the proxy read-only for "ignored"). Both use the rollback journal, not
  WAL: with one writer there is no need for WAL, and a read-only reader
  then creates no files (a WAL reader opens `-shm` read-write, which a
  compromised proxy could symlink to any host file).
- Readers union `proxy.db` and `monitor.db` across `sessions/*`; session and
  workspace come from the directory name and the `workspace` file, not
  from the proxy. SQLite never opens `proxy.db` by name: the host opens it
  with `O_NOFOLLOW|O_NONBLOCK` (no symlink swap, no FIFO), takes SQLite's
  shared `fcntl` lock on that descriptor with a short timeout, skips it
  while an interrupted write's journal is present, reads at most 8 MiB,
  and loads the bytes into an in-memory database. Hosts are validated.
  The companion's `refresh-wanted` markers are resolved relative to an
  `O_NOFOLLOW` directory descriptor. The shared
  `monitor.db`, `hsh run/resume --monitor-db`, the `--db` flags of
  `hsh monitor`/`proxy denied`/`proxy allow`, and `hsh-proxy
  --session/--workspace` are gone.
- `hsh resume` recreates a stopped proxy container that still has the old
  `/hsh-state` mount and warns when it is running.
- Host reads of `proxy/oauth-tokens.json` (`hsh oauth status`, `promote`)
  use the same untrusted-file reader.
- Functional step 19b checks the isolation with `podman exec`.

### 2. Host binding

- `secrets.db` schema version 3 adds `hosts` (JSON array). `hsh secret add`
  requires `--host` (repeatable, comma-separated) for a new secret;
  `--force` keeps the hosts unless `--host` is given. `hsh secret allow`,
  `disallow`, and `edit` (`$VISUAL`/`$EDITOR`, one host per line) change
  them; `secret ls` shows them.
- `config.ResolveSecrets` injects a static secret only for route hosts its
  approved hosts cover (exact, or under an approved wildcard) and reports
  the rest; `hsh run` warns with the config source and the `hsh secret
  allow` command, and asks in a terminal before resolving.
- Environment references (`env://`, bare names) are refused at config load
  everywhere: labels, `--config` files, and session configs (resume names
  the file to change).

### 5. Smaller items

- The inline status line turns red with `login NAME failed: ...` while a
  login in the session's `secrets.json` is marked failed.
- Successful responses of up to 1 MiB from any path on a provider's token
  host are scanned for token-like fields (`Accept-Encoding` narrowed to
  gzip so the body can be decoded); a hit is a 502.
- Functional step 9b ends by searching proxy, companion, and hsh logs for
  the fake providers' real tokens.

### 3. Image trust (deny by default)

- An image's built-in config (its label) is untrusted until
  `hsh run --trust-image IMAGE` records the image id in
  `$XDG_CONFIG_HOME/hsh/trusted-images`. Until then its
  `network.allow`, `network.mode` (forced to `allowlist`), OAuth providers,
  and mounts are dropped with a warning; secret routes, the agent argv, and
  other in-sandbox settings apply. `localhost/` images and `--config` files
  are exempt. This breaks the shipped images until they are trusted once,
  which is accepted.
- Mounts were added to the list: an image could otherwise mount any host
  path into the sandbox.
