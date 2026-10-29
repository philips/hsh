# Configuration reference

hsh reads a JSON config file when `hsh run --config PATH` is given. The
config file is a seed: every run copies it into the session's state
directory (`$XDG_STATE_HOME/hsh/sessions/<session>/config.json`) and the
copy - not the seed - is the live config, hot-reloaded and edited by the
review commands. The seed file is never modified in place. Relative mount
sources are resolved against the workspace directory (the `WORKSPACE-DIR`
positional, or the current directory when omitted) and stored as absolute
paths in the session copy.

Without `--config`, `hsh run IMAGE` uses the image's `org.ifup.hsh` label
as the config - see [pre-built harness images](images.md).

```json
{
  "version": 1,
  "image": "docker.io/library/alpine:latest",
  "shell": "/bin/sh",
  "mounts": [".:/workspace"],
  "secrets": {
    "API_KEY": {
      "from": "env://OPENAI_API_KEY",
      "hosts": ["api.openai.com"],
      "header": "Authorization",
      "prefix": "Bearer "
    }
  },
  "network": {
    "allow": ["api.openai.com", "*.example.com"]
  }
}
```

## Config versions

Every config has a top-level `"version"` integer: the seed file, the session's
config copy, and the `org.ifup.hsh` image label. It is required. An hsh reads
only the versions it understands (today, exactly `1`) and refuses anything
else with a message that says what to do:

- no `version`: `no "version" field (this hsh reads version 1)`
- a newer version (an image or session from a newer hsh):
  `config version N is newer than this hsh reads (version 1); update hsh (hsh update)`
- an older, unsupported version: `config version N is no longer supported
  (this hsh reads version 1); regenerate it`, plus `rebuild the image` for a
  label and `remove the session with: hsh rm ID` for a session. Nothing is
  migrated: configs are rewritten, images rebuilt, sessions recreated.

After the version check the config is decoded strictly: a field hsh does not
know is an error that names it (a typo such as `"approval_wiat"` never silently
changes the policy). The commands that edit a session's config
(`hsh proxy allow`, `hsh proxy hold`, `hsh secret add --config`, and the
monitor) apply the same version check first and leave a file they cannot read
untouched.

A proxy that is running when its session config becomes unreadable (for
example a newer hsh rewrote it) keeps enforcing the last config it could read
and logs `configuration reload failed` with the reason.

`internal/config`'s `Version` changes whenever the set of config fields
changes (added, removed, renamed, or changed meaning); a test fails if the
schema changes without a bump, so images and configs are regenerated on each
bump.

## Fields

| Field | Default | Description |
|---|---|---|
| `version` | required | config schema version, `1` ([versions](#config-versions)) |
| `image` | `docker.io/library/alpine:latest` | container image for both the proxy and workspace containers |
| `shell` | `/bin/sh` | shell to exec in the workspace |
| `mounts` | `[".:/workspace"]` | list of `src:dst[:opts]` bind mounts (podman volume syntax) |
| `secrets` | `{}` | map of secret name -> secret definition (below) |
| `network.mode` | `https` | network policy: `https` (TLS to any host on 443), `allowlist` (only `network.allow`, any port), `none` (no egress) |
| `network.approval_wait` | `off` | opt-in: how long a denied request is held for the user's approval before it fails (for example `"30s"`, at most `5m`); `off` or `0s` fails it at once (see [holding denied requests](#holding-denied-requests)) |
| `network.notice` | `true` | tell the agent about the network restrictions ([network notice](#network-notice)); `false` disables it |
| `network.allow` | `[]` | egress allowlist; in `https` mode it is an overlay (plaintext and non-443 ports), in `allowlist` mode it is the whole policy |
| `oauth` | `{}` | map of provider name -> OAuth capture definition ([sandboxed OAuth logins](oauth.md)) |
| `detach_keys` | `ctrl-]` | podman key sequence that detaches an interactive session (`ctrl-a,d` style); empty disables detaching |
| `monitor` | `{"enabled": true, "status_line": true, "key": "ctrl-space"}` | inline proxy review in interactive sessions ([below](#inline-proxy-review)); `enabled: false` attaches the container directly |
| `agent` | `{}` | `agent.command`: argv template for headless runs; `{prompt}` is replaced with the task prompt ([headless task runs](images.md#headless-task-runs)); `agent.launch`: argv auto-launched from an interactive bash session; in both, `{network_policy}` is replaced with the [network notice](#network-notice) path; `agent.context_files`: instruction files that get the notice as a managed block |

The workspace starts in the first mount's target directory
(override with `hsh run --workdir PATH`).

## Secrets

| Field | Required | Description |
|---|---|---|
| `from` | yes | secret reference: `env://VAR`, `hsh://NAME`, or a bare `VAR` (treated as `env://VAR`) |
| `hosts` | yes | hosts the secret is injected for (same matching as `network.allow`) |
| `header` | no | header to set; defaults to the secret name with `_` -> `-` |
| `prefix` | no | string prepended to the value, e.g. `"Bearer "` |

The scheme of `from` decides where the value comes from. The design
follows nono.sh's credential injection - pluggable secret backends with
host-scoped routes and proxy-side injection instead of environment
injection:
https://nono.sh/docs/cli/features/credential-injection

If the referenced value is missing (an unset environment variable or an
absent store entry), hsh prints a warning and the secret is not injected
(fail-open for availability; the request is still forwarded, just without
the header). An unknown scheme is a hard error at config load.

Injection happens for every request to a matching host, **replacing** any
value the workspace sent for that header. Hosts with a secret are
TLS-intercepted; hosts without one are tunneled.

The common case is a bearer token for an LLM API - name the header and
prefix explicitly, because the default header name (the secret name with
`_` -> `-`, e.g. `Api-Key`) is not what APIs read:

```json
"secrets": {
  "API_KEY": {
    "from": "env://MISTRAL_API_KEY",
    "hosts": ["api.mistral.ai"],
    "header": "Authorization",
    "prefix": "Bearer "
  }
}
```

With this, the proxy rewrites the request's `Authorization` header to
`Bearer <value>`, so tools inside the sandbox can be signed in with a
placeholder key (e.g. in pi's `auth.json`) and never see the real one.
Values are resolved on the **host** before the sandbox starts; hsh does
not read `.env` files, and keys should not live inside mounted
directories where the sandbox can read them.

### The hsh secret store

Instead of exporting keys into your shell environment, store them once
in hsh's own secret store and reference them by name:

```
hsh secret add mistral-api-key --host api.mistral.ai \
  --header Authorization --prefix "Bearer "
```

`secret add` prompts for the value with echo disabled (never on the
command line, where it would leak into shell history); `--value-stdin`
reads it from stdin for scripting. With `--host`/`--header`/`--prefix`
it also writes the `secrets.<NAME>` entry into the config file
(default `config.hsh`, override with `--config PATH`), using
`from: "hsh://NAME"`. Without those flags it only stores the value.

```
hsh secret add NAME [--db PATH] [--force] [--value-stdin]
                     [--config PATH] [--host HOST]... [--header HEADER] [--prefix PREFIX]
hsh secret rm NAME [--db PATH] [--config PATH]
hsh secret ls [--db PATH]
```

- the store is a SQLite database at `$XDG_CONFIG_HOME/hsh/secrets.db`
  (default `~/.config/hsh/secrets.db`, `--db` overrides; used by
  `hsh run` via `--secret-db`)
- the directory is created `0700` and the database `0600`; values are
  stored **unencrypted**, readable only by your user - the same trust
  level as `~/.netrc` or `~/.aws/credentials` (a first-use note says so)
- `secret ls` prints names and timestamps, never values
- `secret rm` deletes the entry and warns if the config still references
  `hsh://NAME`; it never edits the config
- adding an existing name fails without `--force`

A config entry referencing the store:

```json
"secrets": {
  "API_KEY": {
    "from": "hsh://mistral-api-key",
    "hosts": ["api.mistral.ai"],
    "header": "Authorization",
    "prefix": "Bearer "
  }
}
```

## Network policy modes

`network.mode` selects the egress policy:

- `https` (default) - any host may be reached over TLS on port 443
  (tunneled blind; interception stays limited to hosts with configured
  secrets or OAuth endpoints). Plaintext HTTP and non-443 ports are
  denied unless the host is in `network.allow`. DNS resolves every
  name.
- `allowlist` - only hosts in `network.allow`, any port; DNS is gated
  by the same list. The pre-built harness images use this mode.
- `none` - no egress; DNS is refused.

`hsh run --network MODE` overrides the mode from the command line.
In `https` mode a compromised agent can exfiltrate data to any TLS
endpoint; for untrusted code use `--network allowlist`.

## Network notice

Agents that hit a blocked request without explanation hunt for workarounds
(other tools, proxy settings, mirrors, DNS tricks) and burn tokens. hsh
therefore tells the agent up front, in its system prompt or instruction
files, that some requests are blocked on purpose, what a block looks like
(`403 Forbidden` or a refused DNS lookup), that you are notified, and that it
should tell you which host it needs and why instead of working around it.

The notice deliberately does not list the reachable hosts: in measurements an
agent told which hosts are allowed refused to try any other, so the request
that would have asked you for approval was never made.

When a session is created, hsh writes the notice to
`notice/network-policy.md` in the session directory, and the workspace sees
it read-only at `/hsh-run/notice/network-policy.md`. There is no notice in
`none` mode (nothing can be approved there) or with `network.notice: false`.
The text is fixed for the life of the session.

The notice reaches the agent in one of two ways, chosen by the image label:

- **`{network_policy}` in `agent.launch` or `agent.command`.** Each start
  replaces an argument that is exactly `{network_policy}`, or ends in
  `={network_policy}`, with the notice path. Without a notice, that argument
  is dropped, together with the flag before it (an argument starting with
  `-`), so `["pi", "--append-system-prompt", "{network_policy}"]` becomes
  plain `["pi"]`.
- **`agent.context_files`**, a list of paths in the container (`~/` is the
  sandbox home). Each start writes the notice into each file between
  `<!-- hsh:network-policy begin -->` and `<!-- hsh:network-policy end -->`,
  creating the file if needed and leaving everything outside the markers
  alone. Without a notice, an existing block is removed. The agent can edit
  the block; it is rewritten on the next start, and it is only advice:
  enforcement stays in the proxy.

hsh never writes into your project's own instruction files unless a path in
`agent.context_files` points there.

## Holding denied requests

By default a denied request fails at once with a `403 Forbidden` whose message
tells the agent that the block is on purpose, that you have been notified, and
to tell you which host it needs and why instead of looking for workarounds.

If you would rather the request wait for you, set `network.approval_wait` (for
example `"30s"`, at most `5m`). Then a request the policy would deny (a CONNECT,
a plaintext HTTP request, or a DNS lookup in `allowlist` mode) is *held* for
that long while you decide: if you approve the host in time the request just
succeeds, if you ignore the host it fails at once, and if nobody decides it
fails with the same 403.

- It only holds while someone can approve: an inline-review relay is
  attached, or `hsh monitor` / `hsh proxy allow --watch` is covering the
  session. Otherwise denials are immediate.
- It does not apply in `none` mode, and in `https` mode only to what is
  denied (plaintext HTTP and non-443 ports).
- DNS lookups are held for at most 9 s (resolvers give up after about 10 s).
- Clients with short connect timeouts (Node's fetch gives up after 10 s) will
  see a timeout before a long wait ends.
- Turn it on in the seed config, an image label, or the session's
  `config.json`; `hsh proxy hold [--session ID] on|off|default|45s` changes a
  running session (`on` means 30 s; the proxy reloads within a second and
  releases requests already held when it goes off); in `hsh monitor` and the
  inline review screen press `w` to toggle the session of the selected row.
- Held hosts are listed first in the monitor (`HELD x2`) and the inline
  status line shows `(M held)`.

## Allowlist matching

- exact hostname match, case-insensitive, trailing dot ignored
- `*.example.com` matches any subdomain (one or more labels)
- IP addresses match literally
- the port is not part of matching; any port on an allowed host is allowed

## Hot reload

`network.allow` and everything under `secrets` are reloaded automatically
when the config file changes (the proxy polls once per second) - for a
running session that file is its config copy in the state directory, so
`hsh proxy allow` approvals apply to a live session within ~1s. `image`,
`shell`, and `mounts` require restarting the sandbox.

## Image shortnames

The `hsh run` positional image is a **shortname** unless it is a
transport reference (`docker://...`, `oci://...` - anything containing
`://`) or a registry-qualified reference (first path segment contains
a `.`, has a port, or is `localhost`, like `ghcr.io/x/y`,
`localhost/my-dev:latest`, or `registry:5000/x`).

A shortname resolves through
`$XDG_CONFIG_HOME/hsh/shortnames.conf` (default
`~/.config/hsh/shortnames.conf`, the same directory as the secret
store):

```
# override where shortnames point by default
shortname-prefix=docker://quay.io/example-corp-agents/

# aliases for specific shortnames
pi=docker://ghcr.io/philips/hsh-pi
mydev=docker://registry.example.com/team/pi:dev
```

- an explicit alias always wins; otherwise a shortname `N` resolves to
  `shortname-prefix` + `N`
- the builtin default prefix is `docker://ghcr.io/philips/hsh-`, so
  `hsh run pi` retrieves `ghcr.io/philips/hsh-pi` with no
  configuration, and `hsh run vibe`, `hsh run claude-code`, and
  `hsh run codex` work the same way
- unknown shortnames resolve from the prefix and fail with an error
  that names the reference tried and points at the config file

The image is fetched for every new session according to `hsh run`'s
`--pull` policy (same names and meanings as podman's pull policies):

- `newer` (default) - pull every run, so the session gets the latest
  image for the tag; if the pull fails and a local image exists, hsh
  warns and uses the local image (offline use, locally built images)
- `always` - pull every run; a failed pull is an error
- `missing` - pull only when the image is not in the local store
- `never` - use the local image; an error if it is not local

The pull happens before hsh reads the image's `org.ifup.hsh` label,
so a label-based config always comes from the latest image. The
session's config copy stores the *resolved* reference (a transport
reference normalized to its local repository name), so resuming is
unaffected by later config edits.

## Sessions

Every `hsh run` creates a persistent session: a podman pod plus a state
directory at `$XDG_STATE_HOME/hsh/sessions/<session-id>/` (the session
id is the pod name suffix):

```
config.json   the session's config copy - the only writable config
ca.crt ca.key the session's MITM CA (stable across re-entries)
workspace     file with the absolute path of the workspace
```

- sessions persist when the shell exits: the pod is stopped (infra and
  proxy included) and the resolved `secrets.json` is scrubbed, so an idle
  session holds no key material; re-entry re-resolves secrets from the
  session config
- the detach key (`detach_keys`, default Ctrl-], see below) leaves an
  interactive session running in the background; closing the terminal does
  the same.
  `hsh run --resume` re-attaches to the same shell, and `hsh ls` shows
  `Attached`, `Detached`, or `Shell exited`. A small `hsh-reaper` process
  scrubs secrets and stops the pod if the shell exits while detached.
  `--rm` and `--tasks` sessions cannot detach
- only one terminal is attached to a session at a time: `hsh run --resume`
  refuses a session that is attached elsewhere. `hsh detach [<session id>]`
  detaches whatever terminal is attached (that `hsh run` restores its
  terminal and exits; the session keeps running), and
  `hsh run --resume --take-over <id>` does that and attaches here
- `hsh run --rm` makes a run temporary: pod and state are removed at exit
- `hsh run --resume` re-enters the current workspace's session - the only
  one directly, or lists the ids when there are several
  (`hsh run --resume <id>` picks one; the full pod name also works)
- sessions do not share approved proxy configuration on purpose: one
  session's allowlist is that session's policy
- `hsh rm` removes a session and its state; `hsh rm --all -f` sweeps
  every sandbox; `hsh gc` collects session state whose pod is gone

### Detach key

`detach_keys` takes podman's syntax: comma-separated keys, each `ctrl-` plus
a letter or one of `@ \ ] ^ _` (for example `ctrl-a,d` is Ctrl-A then d), and an
empty string disables detaching. Set it in the seed config (`config.hsh`), in
an image's `org.ifup.hsh` label, or, for a session that already exists, in
`$XDG_STATE_HOME/hsh/sessions/<id>/config.json` before `hsh run --resume <id>`
(a session keeps its own copy of the config).

If the key seems to do nothing, check what your terminal sends: run `cat -v`
in the session and press it. `^]` is the plain control byte. Something like
`^[[93;5u` or `^[[27;5;93~` means a program in the session switched the
terminal to an enhanced keyboard protocol (kitty or xterm modifyOtherKeys),
and podman does not understand those encodings. The inline review relay
(below) translates such encodings back for the configured detach key; without
it (a build without `GOEXPERIMENT=simd`, or `monitor.enabled: false`) pick a
key that your programs do not remap, or avoid the protocol.

## Inline proxy review

Interactive `hsh run` sessions (not `--rm`, `--tasks`, or piped runs) pass the
session through a relay in the `hsh run` process, outside the sandbox:

- the bottom terminal row is a status line: green with `proxy: nothing to
  review`, **blue** with `* N proxy requests need review | Ctrl+Space to
  review` (plus `(M held)` when requests are waiting on you) as the proxy
  records denials for the session, and red while the review screen is open
  (`"status_line": false` removes the reserved row)
- `monitor.key` (default Ctrl+Space; `ctrl-a`..`ctrl-z`, `ctrl-\`, `ctrl-]`,
  `ctrl-^`, `ctrl-_`) turns the line red and takes over the whole screen with
  the same view and keys as `hsh monitor`, scoped to this session: up/down
  select, space selects, enter approves the selected hosts (adding them to
  the session config's `network.allow`), `i` ignores, `a`/`n` select all or
  none, `t` toggles ignored hosts, `/` filters, `r` refreshes, `q` or Esc
  closes (as does the review key again)
- while the review screen is up `hsh run` stops reading the session's output,
  so the container blocks in `write()` instead of printing; closing repaints
  the session screen from the emulator and delivers the held output with
  nothing lost
- the session's output is passed through unchanged, so the terminal's own
  scrollback, selection, and search keep working; the status line is part of
  the live screen only and is not in scrollback

Binaries built without `GOEXPERIMENT=simd` have no terminal emulator
(`hsh version` says so) and attach directly. The review list comes from the
proxy's monitor database, never from what the session prints.

## Managing the allowlist

Denied egress attempts are answered with `403 Forbidden` and recorded in the
monitor database, a SQLite file (`$XDG_STATE_HOME/hsh/monitor.db`, default
`~/.local/state/hsh/monitor.db`, override with `hsh run --monitor-db PATH`).
Two subcommands manage it:

```
hsh proxy denied [--session ID] [--watch]   # list the review queue
hsh proxy allow --all --session ID          # allow a session's pending hosts
hsh proxy allow --session ID https://h      # allow one host
hsh monitor                                 # interactive review (TUI)
```

`hsh proxy allow` rewrites the session's config copy as canonical JSON (keys
sorted, 2-space indent); only `network.allow` changes.

`network.allow` also gates DNS: the workspace resolves names through a
forwarder in the proxy that refuses any name not on the allowlist. DNS
DNS denials share the single review queue and are
never auto-allowed.

## Tools that self-download

Some tools download helper binaries on first run instead of using system
packages. Pi (the coding agent), for example, downloads `fd` and `ripgrep`
from GitHub when they are not already on `PATH`. Inside the sandbox those
downloads are denied unless the hosts are allowlisted:

- `github.com` - release version lookup and download URLs
- `release-assets.githubusercontent.com` - where GitHub release assets redirect
  to (the legacy target is `objects.githubusercontent.com`; allowlist both)

The example image avoids this entirely by pre-installing `fd-find` and
`ripgrep` and setting `PI_OFFLINE=1`, so Pi's tools work with a minimal
allowlist and Pi never attempts downloads or update checks inside the
sandbox. If you prefer Pi's pinned tool versions, allowlist the hosts above
instead and omit `PI_OFFLINE`.

## Updating hsh

`hsh update` replaces the running binary in place with a release from
GitHub (`github.com/philips/hsh/releases`), using the same rules as
install.sh: it resolves the newest tag by following the `releases/latest`
redirect, downloads the `hsh-linux-<amd64|arm64>` asset, verifies its
SHA-256 checksum against the release's `SHA256SUMS`, and renames it over
the running binary (found via `os.Executable`, symlinks resolved). The
binary updates itself wherever it is installed — no install location is
configured or remembered.

```
usage: hsh update [--version vTAG] [--no-metrics]
       hsh update --auto | --no-auto
       hsh update --autoupdate-check
```

Environment overrides (matching install.sh):

| variable | purpose |
|---|---|
| `HSH_VERSION` | update to a specific tag instead of latest (the `--version` flag wins) |
| `HSH_RELEASE_BASE` | release base URL for testing (default `https://github.com/philips/hsh/releases`) |

If the installed directory is not writable, the update fails with
instructions to re-run install.sh.

### Usage metrics

When an update installs a new version, hsh posts one anonymous report so
the project can see how much a released binary is used. Before anything is
sent, `hsh update` prints:

```
hsh update reports anonymous usage metrics to the project:
version, platform (linux/amd64), and the number of sessions created since the
last update. No identifiable information is uploaded.
Skip with --no-metrics or HSH_NO_METRICS=1.
```

The POST:

```
POST https://infinitelogic.app/o/hsh-home/hooks/metrics_report
Content-Type: application/json

{"product": "hsh", "version": "0.1.0", "platform": "linux/amd64", "sessions_since_last_update": 38}
```

`version` is the version that was running before the update, and
`sessions_since_last_update` counts the sessions created by `hsh run` since
the previous update's report. The count lives in a sqlite database at
`~/.local/state/hsh/metrics.db` (`$XDG_STATE_HOME/hsh/metrics.db`) and is
reset to zero after each report. Nothing else is sent — no hostname, user
name, workspace paths, session ids, or timestamps — and a report that
cannot be delivered (offline, server error) is a warning, not a failed
update.

To skip the report entirely, pass `--no-metrics` or set `HSH_NO_METRICS=1`
(the notice is not printed either); the counter still resets. An install
that is already up to date downloads nothing and reports nothing.
`HSH_METRICS_URL` overrides the webhook URL for testing.

### Automatic updates

`hsh update --auto` opts the install into automatic updates;
`hsh update --no-auto` turns them back off. While enabled:

- Every hsh command starts a **background check** in a detached child
  process — one HEAD request to the release host (`HSH_RELEASE_BASE`),
  at most once every 4 hours. The child only *records* what it finds;
  it never downloads anything or touches the binary. A long `hsh run`
  re-evaluates the check every 15 minutes in the background, but never
  disturbs the session.
- The **next hsh command** after a newer release has been recorded
  performs the update inline, at command startup, printing exactly what
  `hsh update` would have printed — including the metrics notice and
  report above. A failed inline update (offline, checksum, unwritable
  install dir) prints a warning and the command proceeds normally.
- A **dev build** (a local `go build`) is never auto-updated; it only
  prints a one-line notice pointing at `hsh update`.

Updating in place never disturbs running sessions: the binary is
bind-mounted into each pod, and the update installs by rename, so live
sessions keep executing the old binary until they stop; a session
resumed after an update runs the new binary. Each session records
the versions that created and re-entered it (see
[architecture](architecture.md)).

Off by default: without `--auto`, hsh makes no background requests and
prints no notices. `HSH_NO_UPDATE_CHECK=1` silences both the background
check and the inline update for a single run (useful in scripts);
`HSH_UPDATE_CHECK_INTERVAL` overrides the check interval (minimum 1h,
e.g. `2h`). The check state (`update_last_check`, `update_latest_tag`,
`update_auto`) lives in the `kv` table of the same metrics database
above. `hsh update --autoupdate-check` runs the check by hand and
prints what it found without updating.

## Examples

Minimal dev sandbox with an API key:

```json
{
  "version": 1,
  "mounts": [".:/workspace"],
  "secrets": {
    "API_KEY": {"from": "env://OPENAI_API_KEY", "hosts": ["api.openai.com"]}
  },
  "network": {"allow": ["api.openai.com", "registry.npmjs.org", "github.com"]}
}
```

The injected header would be `Api-Key: <value>` for requests to
`api.openai.com`. To send `Authorization: Bearer <value>` instead, set
`"header": "Authorization", "prefix": "Bearer "`.
