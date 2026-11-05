# Configuration reference

hsh reads a JSON config file when `hsh run --config PATH` is given. The
config file is a seed: every run copies it into the session's state
directory (`$XDG_STATE_HOME/hsh/sessions/<session>/config.json`) and the
copy - not the seed - is the live config, hot-reloaded by the proxy. Its
`network.allow` is copied into the session's [rules](#session-rules) when the
session is created; approvals and denials go to the rules, not to the config
copy. The seed file is never modified in place. Relative mount
sources are resolved against the workspace directory (the `WORKSPACE-DIR`
positional) and stored as absolute paths in the session copy. Without
`WORKSPACE-DIR` no mounts apply
([sessions without a workspace](#sessions-without-a-workspace)).

Without `--config`, `hsh run IMAGE` uses the image's `org.ifup.hsh` label
as the config - see [pre-built harness images](images.md). That built-in
config is untrusted until `hsh run --trust-image IMAGE` trusts that image;
until then its allowlist, network mode, OAuth providers, and mounts are
ignored ([image trust](images.md#image-trust)).

```json
{
  "version": 1,
  "image": "docker.io/library/alpine:latest",
  "shell": "/bin/sh",
  "mounts": [".:/workspace"],
  "secrets": {
    "API_KEY": {
      "from": "hsh://openai-api-key",
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
(`hsh proxy allow`, `hsh settings edit --session`, and the
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
| `mounts` | `[".:/workspace"]` | list of `src:dst[:opts]` bind mounts (podman volume syntax); relative sources resolve against `WORKSPACE-DIR`, and none apply without one ([sessions without a workspace](#sessions-without-a-workspace)) |
| `secrets` | `{}` | map of secret name -> secret definition (below) |
| `network.mode` | `https` | network policy: `https` (TLS to any host on 443), `allowlist` (only `network.allow`, any port), `none` (no egress) |
| `network.approval_wait` | *(unset)* | how long a denied request is held for the user's approval before it fails (for example `"30s"`, at most `5m`); `off` or `0s` fails it at once (see [holding denied requests](#holding-denied-requests)). Unset means the global `network_proxy_approval_wait` setting, then the builtin default `30s` |
| `network.suspend_delay` | *(unset)* | how long a hold must last before the sandbox is suspended to wait out the approval (for example `"2s"`, at most `1m`); `off` never suspends. Unset means the global `network_proxy_approval_suspend_delay` setting, then the builtin default `3s` |
| `network.notice` | `true` | tell the agent about the network restrictions ([network notice](#network-notice)); `false` disables it |
| `network.allow` | `[]` | egress allowlist, copied into the session's [rules](#session-rules) when the session is created (later edits of the session's config copy do not change it); in `https` mode it is an overlay (plaintext and non-443 ports), in `allowlist` mode it is the whole policy |
| `oauth` | `{}` | `oauth.providers`: OAuth provider descriptors to load by name; `oauth.api_hosts`: optional per-provider subset of their API hosts ([sandboxed OAuth logins](oauth.md)) |
| `detach_keys` | none | ignored; accepted so existing configs keep loading (no key detaches a session; use `hsh detach`) |
| `monitor` | `{"enabled": true, "status_line": true, "key": "ctrl-space"}` | inline proxy review in interactive sessions ([below](#inline-proxy-review)); `enabled: false` attaches the container directly |
| `agent` | `{}` | `agent.command`: argv template for headless runs; `{prompt}` is replaced with the task prompt ([headless task runs](images.md#headless-task-runs)); `agent.launch`: argv auto-launched from an interactive bash session; in both, `{network_policy}` is replaced with the [network notice](#network-notice) path; `agent.context_files`: instruction files that get the notice as a managed block |

The workspace starts in the first mount's target directory
(override with `hsh run --workdir PATH`).

## Secrets

| Field | Required | Description |
|---|---|---|
| `from` | yes | secret reference: `hsh://NAME`, an entry of the [hsh secret store](#the-hsh-secret-store) |
| `hosts` | yes (static secrets) | hosts the secret is injected for (same matching as `network.allow`), narrowed to the hosts approved for the secret in the store ([host binding](#host-binding)); for an OAuth login it defaults to the provider's API hosts and may only narrow them |
| `header` | no | header to set; defaults to the secret name with `_` -> `-` |
| `prefix` | no | string prepended to the value, e.g. `"Bearer "` |
| `seed` | no | for an OAuth login (`hsh://NAME` promoted with `hsh oauth promote`): seed templates from the provider descriptor to write into new sessions ([seeds](oauth.md#seeds)) |
| `optional` | no | `true`: a missing value is skipped silently instead of warning (the image labels use it for logins that may not be promoted yet) |

The scheme of `from` decides where the value comes from. The design
follows nono.sh's credential injection - pluggable secret backends with
host-scoped routes and proxy-side injection instead of environment
injection:
https://nono.sh/docs/cli/features/credential-injection

If the referenced store entry is missing, hsh prints a warning and the
secret is not injected (fail-open for availability; the request is still
forwarded, just without the header). An unknown scheme is a hard error at
config load, and so are environment references (`env://VAR` or a bare
`VAR`): they let any config or image label send any variable of the user's
environment to any host, so the error names the `hsh secret add` command
that replaces them. A session config with such a reference (from an older
hsh) does not load either; `hsh resume` names the file to change and
`hsh rm`.

Injection happens for every request to a matching host, **replacing** any
value the workspace sent for that header. Hosts with a secret are
TLS-intercepted; hosts without one are tunneled.

The common case is a bearer token for an LLM API - name the header and
prefix explicitly, because the default header name (the secret name with
`_` -> `-`, e.g. `Api-Key`) is not what APIs read:

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

With this, the proxy rewrites the request's `Authorization` header to
`Bearer <value>`, so tools inside the sandbox can be signed in with a
placeholder key (e.g. in pi's `auth.json`) and never see the real one.
Values are resolved on the **host** before the sandbox starts; hsh does
not read `.env` files, and keys should not live inside mounted
directories where the sandbox can read them.

### The hsh secret store

Store each key once in hsh's own secret store, together with the hosts it
may be sent to, and reference it by name:

```
hsh secret add --host api.mistral.ai mistral-api-key
```

`secret add` prompts for the value with echo disabled (never on the
command line, where it would leak into shell history); `--value-stdin`
reads it from stdin for scripting. It stores the value under `NAME`;
reference it from a config's `secrets.<NAME>` entry with
`from: "hsh://NAME"`.

```
hsh secret add --host HOST [--host HOST]... [--db PATH] [--force] [--value-stdin] NAME
hsh secret allow [--db PATH] NAME HOST...
hsh secret disallow [--db PATH] NAME HOST...
hsh secret edit [--db PATH] NAME
hsh secret rm [--db PATH] NAME
hsh secret ls [--db PATH]
```

- the store is a SQLite database at `$XDG_CONFIG_HOME/hsh/secrets.db`
  (default `~/.config/hsh/secrets.db`, `--db` overrides; used by
  `hsh run` via `--secret-db`)
- the directory is created `0700` and the database `0600`; values are
  stored **unencrypted**, readable only by your user - the same trust
  level as `~/.netrc` or `~/.aws/credentials` (a first-use note says so)
- `secret ls` prints names, timestamps and approved hosts, never values
- `secret rm` deletes the entry; it never edits the config
- adding an existing name fails without `--force`; `--force` replaces the
  value and keeps the approved hosts unless `--host` is given

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

### Host binding

A config route only says where a secret *should* go. Where it *may* go is
recorded with the secret, by the user, in the store: `hsh secret add`
requires at least one `--host` (repeatable or comma-separated; a hostname
such as `api.example.com`, or `*.example.com` for its subdomains; URLs and
ports are reduced to the host), `hsh secret allow`/`disallow` add or remove
hosts, and `hsh secret edit` opens the list in `$VISUAL`/`$EDITOR` (one host
per line, `#` comments; an invalid list is not saved).

At every resolution (launch, config reload, and the OAuth companion's
rewrites of `secrets.json`), a static secret is injected only for the route
hosts its approved hosts cover: an exact name, or a name or narrower
wildcard under an approved `*.` wildcard. Narrowing is fine; every other
route host is dropped with a warning that names the source and the fix:

```
hsh: warning: secret API_KEY (hsh://mistral-api-key) is not sent to evil.example: the org.ifup.hsh label of IMAGE asks for it, but you have not approved hsh://mistral-api-key for that host; allow it with: hsh secret allow mistral-api-key evil.example
```

The source is the image label or the `--config` file for a new session and
"the session config" on `hsh resume`. A secret whose route hosts are all
unapproved is not injected anywhere. When `hsh run` or `hsh resume` runs in a
terminal, it asks first, once per unapproved host
(`... asks to send hsh://NAME (as API_KEY) to HOST. Allow? [y/N]`), and
records a yes in the store; a secret with no approved hosts yet (one stored
before hosts were recorded) gets a one-line explanation first.
Non-interactive runs never ask. OAuth logins are not affected: their hosts
come from the user's provider descriptors.

## Network policy modes

`network.mode` selects the egress policy:

- `https` (default) - any host may be reached over TLS on port 443
  (tunneled blind; interception stays limited to hosts with configured
  secrets or OAuth endpoints). Plaintext HTTP and non-443 ports are
  denied unless an allow rule matches the host. DNS resolves every
  name.
- `allowlist` - only hosts an allow rule matches, any port; DNS is gated
  by the same rules (a refused lookup is logged, not queued for review).
  The pre-built harness images use this mode.
- `none` - no egress; DNS is refused; rules are not consulted.

A deny rule wins in every mode: a denied host is refused even for TLS on
port 443 in `https` mode.

## Session rules

Each session's decisions are rules in its `monitor.db`, written by the host
and read by the proxy (it rereads them within ~0.2 s). A rule has a host
pattern (`example.com` or `*.example.com`), a source, and an access:

| source | written by |
| --- | --- |
| `config` | session creation, one `allow` rule per `network.allow` entry |
| `user` | the monitor, the inline review screen, `hsh proxy allow` and `hsh proxy deny`; one rule per pattern, the latest decision replaces the earlier one |

- A host any `deny` rule matches is denied; otherwise a host any `allow`
  rule matches is allowed; otherwise the network mode decides.
- A denial the mode decides is queued for review and can be held. A denial
  a `deny` rule decides is recorded but not queued or held, and its 403 says
  the user decided and the agent should not ask again.
- Resuming a session copies nothing: the rules outlive later edits of the
  config copy's `network.allow`.
- A proxy that cannot read the rules logs it and uses no rules, so only the
  mode's defaults apply.
- The standalone proxy (`hsh-proxy` without `--monitor-db`) uses the config's
  `network.allow` as its allow rules.

`hsh proxy deny --session ID URL|PATTERN` denies a host, or every host under
`*.domain`. Approving it again (`hsh proxy allow`, or enter on the row in the
monitor's denied view) replaces the deny.

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

Holding is on by default (`30s`, from the global `network_proxy_approval_wait`
setting or the builtin default; set `network.approval_wait` per session to
change it — for example `"45s"`, at most `5m`, `off` to fail at once). A request the
policy would deny (a CONNECT, or a plaintext HTTP request) is *held* for
that long while you decide: if you approve the host in time the request just
succeeds, if you deny the host it fails at once, and if nobody decides it
fails with the same 403.

- It only holds while someone can approve: an inline-review relay is
  attached, or `hsh monitor` / `hsh proxy allow --watch` is covering the
  session. Otherwise denials are immediate.
- It does not apply in `none` mode, and in `https` mode only to what is
  denied (plaintext HTTP and non-443 ports).
- DNS lookups are never held, and neither are hosts a deny rule matches.
- Clients with short connect timeouts (Node's fetch gives up after 10 s) will
  see a timeout before a long wait ends.
- Turn it on in the seed config, an image label, or the session's
  `config.json`; `hsh settings edit --session ID` edits a running session's
  `approval_wait` (the proxy reloads within a second and releases requests
  already held when it goes off); in `hsh monitor` and the inline review
  screen press `w` to toggle the session of the selected row.
- Held hosts are listed first in the monitor.

## Allowlist matching

The same host patterns are used by `network.allow`, the session's
[rules](#session-rules), DNS gating, the monitor's "add new allowlist/deny
item", and `hsh proxy allow`/`deny` (grammar after
[smokescreen](https://github.com/stripe/smokescreen)). A pattern is a
hostname in Punycode with at most one `*`; matching is case-insensitive and
ignores a trailing dot:

- `example.com` matches that host only
- a leading `*.` matches one or more labels: `*.example.com` matches
  `api.example.com` and `a.b.example.com`, not `example.com`
- a `*` label elsewhere matches exactly one label:
  `access-analyzer.*.amazonaws.com` matches
  `access-analyzer.us-west-2.amazonaws.com`, not
  `access-analyzer.a.b.amazonaws.com`
- a `*` after a literal prefix inside a label matches one or more characters
  of that label: `api*.example.com` matches `api-east.example.com`, not
  `api.example.com` or `a.api-east.example.com`; `web*-canary.example.com`
  also works
- the wildcard label needs at least two literal labels after it, so
  `*.com`, `login.*.com`, and `example.*` are rejected (patterns under a
  public suffix such as `foo*.github.io` are accepted)
- IP addresses (IPv4, IPv6 with or without brackets) match exactly
- `*example.com`, `*-canary.example.com`, two wildcards, and non-ASCII names
  (`éxämple.com`; write `xn--...`) are rejected
- the port is not part of matching; any port on an allowed host is allowed

An invalid `network.allow` entry is a config load error that names the
entry. Secret hosts keep their own, narrower syntax (an exact name or `*.`;
[host binding](#host-binding)).

## Hot reload

`network.mode`, `network.approval_wait`, `network.suspend_delay`, and
everything under `secrets` are reloaded automatically when the config file
changes (the proxy polls once per second) - for a running session that file
is its config copy in the state directory. The session's
[rules](#session-rules) are reread separately, so approvals and denials
apply to a live session within a second. `image`, `shell`, and `mounts`
require restarting the sandbox.

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
monitor.db    the session's rules (approvals and denials)
ca.crt ca.key the session's MITM CA (stable across re-entries)
workspace     file with the absolute path of the workspace (empty without one)
```

- sessions persist when the shell exits: the pod is stopped (infra and
  proxy included) and the resolved `secrets.json` is scrubbed, so an idle
  session holds no key material; re-entry re-resolves secrets from the
  session config
- `hsh detach` (or **Session: detach session** in the
  [monitor](#the-monitor)) leaves an interactive session running in the
  background; closing the terminal does the same.
  `hsh resume` re-attaches to the same shell, and `hsh ls` shows
  `Attached`, `Detached`, or `Shell exited`. A small `hsh-reaper` process
  scrubs secrets and stops the pod if the shell exits while detached.
  `--rm` and `--tasks` sessions cannot detach
- only one terminal is attached to a session at a time: `hsh resume`
  refuses a session that is attached elsewhere. `hsh detach [<session id>]`
  detaches whatever terminal is attached (that `hsh run` or `hsh resume` restores its
  terminal and exits; the session keeps running), and
  `hsh resume --take-over <id>` does that and attaches here
- `hsh run --rm` makes a run temporary: pod and state are removed at exit
- `hsh resume [SESSION | WORKSPACE-DIR]` re-enters an existing session:
  the current directory's only session directly, or it lists the ids when
  there are several (`hsh resume <id>` picks one; an id prefix or the full
  pod name also works; a directory argument selects that workspace)
- sessions do not share approved proxy configuration on purpose: one
  session's rules are that session's policy
- `hsh rm` removes a session and its state; `hsh rm --all -f` sweeps
  every sandbox; `hsh rm --exited` removes exited sandboxes; `hsh rm
  --garbage` collects session state whose pod is gone

### Sessions without a workspace

`hsh run IMAGE` with no `WORKSPACE-DIR` mounts no host directory. Use
`hsh run IMAGE .` to mount the current directory. Without a workspace:

- the config's `mounts` are not applied, whether they come from the image
  label, a trusted image, or `--config`. A list other than the default
  `.:/workspace` is named in a warning. The session's config copy records
  `"mounts": []`, so resuming the session doesn't mount anything either
- the shell starts in `/workspace`, an ordinary directory in the
  workspace container's filesystem. hsh creates it if the image has none,
  and strips setuid bits there like everywhere else in the image
- `/workspace` keeps its contents across detach, exit, and `hsh resume`,
  because the workspace container is stopped, not removed. gVisor sessions
  run with `--overlay2=none`, so their writes land in the same container
  layer. The contents are lost when the session is removed (`hsh rm`,
  `--rm`) or when resume has to recreate the workspace container
- the pod has no `hsh.workspace` label and the session's `workspace` file
  is empty. `hsh ls` shows `-`. `hsh resume` with no argument, `hsh resume
  DIR`, and `hsh rm DIR` never match these sessions; use the session id

## Inline proxy review

Interactive `hsh run` sessions (not `--rm`, `--tasks`, or piped runs) pass the
session through a relay in the `hsh run` process, outside the sandbox — the
agent cannot trigger or forge any of it itself
([agent-terminal-isolation.md](agent-terminal-isolation.md)):

- the bottom terminal row is a status line: green with `proxy: nothing to
  review`, **blue** with `* N proxy requests need review | Ctrl+Space to
  review` (plus `(M held)` when requests are waiting on you) as the proxy
  records denials for the session, and red while the monitor is open
  (`"status_line": false` removes the reserved row). The line names the
  configured key. It also turns red with `login NAME failed: log in again,
  then hsh oauth promote --force PROVIDER NAME` while an OAuth login the
  session uses is marked failed ([oauth.md](oauth.md#host-held-logins))
- the monitor key turns the line red and takes over the whole screen with
  the [`hsh monitor`](#the-monitor) command palette, scoped to this session.
  When the session has proxy requests waiting for review it opens straight
  into the review list instead. The monitor key again closes it from any
  screen
- pressing the monitor key twice in a row sends the key itself to the
  program in the session, as tmux does with its prefix: the first press
  opens the monitor, the second closes it and forwards the key (in the
  encoding the terminal sent it). Choose another key if a program you use
  needs that key often
- while the monitor is up `hsh run` stops reading the session's output,
  so the container blocks in `write()` instead of printing; closing repaints
  the session screen from the emulator and delivers the held output with
  nothing lost
- the session's output is passed through unchanged, so the terminal's own
  scrollback, selection, and search keep working; the status line is part of
  the live screen only and is not in scrollback

Binaries built without `GOEXPERIMENT=simd` have no terminal emulator
(`hsh version` says so) and attach directly. The review list comes from the
proxy's monitor database, never from what the session prints.

## The monitor

`hsh monitor` (and the monitor key inside a session) opens a type-ahead
command palette. Typing filters the commands fuzzily, up/down (or
Ctrl+P/Ctrl+N, Tab/Shift+Tab) move, Enter runs the selected command, Esc
closes. The commands:

- **Proxy: review denied requests** — the review list: up/down move, enter
  approves the selected hosts, or the host under the cursor when none are
  selected (an allow [rule](#session-rules)), `d` denies the same way (a
  deny rule), space selects, `a`/`n` select all or none, `t` toggles the
  denied hosts (marked `(denied by you)`; enter on one approves it again),
  `w` toggles holding, `/` filters, `r` refreshes, `q` closes the monitor.
  Esc goes back to the palette, except when the monitor opened straight
  into the list: then Esc closes it, so approving a request is enter then
  Esc. `hsh monitor --all` opens with the denied hosts shown
- **Proxy: add new allowlist item** — type `example.com`, `*.example.com`,
  or a URL (reduced to its host) and press Enter. It becomes an allow rule
  and any pending denials it matches are approved; if a deny rule still
  matches the host, the status line says it stays denied. Inside a session
  (or with `hsh monitor --session ID`) it goes to that session; otherwise a
  fuzzy session picker (id and workspace) asks which one
- **Proxy: add new deny item** — the same, as a deny rule; pending denials
  it matches leave the review list
- **Global Settings: change monitor key** — pick any valid key
  (`ctrl-space`, `ctrl-a`..`ctrl-z` except Backspace, Tab, and Enter,
  `ctrl-\`, `ctrl-^`, `ctrl-_`). It is saved in the global settings file
  and, when changed from inside a session, takes effect there at once;
  other attached sessions keep their key until they are re-attached

- **Session: detach session** — inside a session, detaches this terminal
  (as `hsh detach` does; `hsh run` reports `requested by the monitor`) and
  the session keeps running. In `hsh monitor` it asks which attached session
  to detach, unless `--session` names one
- **Session: exit session** — asks for confirmation, then stops the session
  as if its shell had exited: the pod stops and `secrets.json` is scrubbed;
  the state is kept for `hsh resume`. Inside a session `hsh run` reports
  `exited session ID (requested by the monitor)` and exits 0. In
  `hsh monitor` it asks which session, unless `--session` names one

`hsh monitor` opens straight into the review list when something is
pending, like the in-session monitor. The review list's empty state is a
single `nothing to review` line. Ctrl+C closes it from any screen.

### Global settings

`$XDG_CONFIG_HOME/hsh/settings.json` (default `~/.config/hsh/settings.json`)
holds user preferences that apply to every session unless a session's config
overrides them; edit it with `hsh settings edit`:

```json
{
  "monitor_key": "ctrl-a",
  "network_proxy_approval_wait": "45s",
  "network_proxy_approval_suspend_delay": "2s"
}
```

`monitor_key`, when set, overrides `monitor.key` from the session config.
`network_proxy_approval_wait` and `network_proxy_approval_suspend_delay` are
the global defaults for the session config's `network.approval_wait` and
`network.suspend_delay` (how long a denied request is held for approval, and
how long a hold must last before the sandbox is suspended to wait it out;
builtin defaults `30s` and `3s`, `off` disables).
`hsh settings edit --session ID` edits the per-session overrides of the same
two knobs.
Unknown fields are an error; an unreadable file is ignored with a warning
and the builtin defaults are used.

## Managing the allowlist

Denied egress attempts are answered with `403 Forbidden` and recorded in the
session's own SQLite database (`$XDG_STATE_HOME/hsh/sessions/<id>/proxy/proxy.db`,
written by that session's proxy). The review commands read every session's
`proxy.db` and keep the user's decisions as rules in `sessions/<id>/monitor.db`
(see [architecture.md](architecture.md#denial-records-and-session-rules)):

```
hsh proxy denied [--session ID] [--watch]   # list the review queue
hsh proxy denied [--session ID] --all       # every recorded host and its state
hsh proxy allow --all --session ID          # allow a session's pending hosts
hsh proxy allow --session ID https://h      # allow one host
hsh proxy deny --session ID https://h       # deny one host (or *.domain)
hsh monitor                                 # command palette (TUI)
```

`hsh proxy denied --all` shows each host's state: `pending`, `allowed`,
or `denied by you`. `hsh proxy allow` on a host a deny rule still
matches says it stays denied.

The allow rules also gate DNS in `allowlist` mode: the workspace resolves
names through a forwarder in the proxy that refuses any name no allow rule
matches. A refused lookup is only logged: it is not recorded in the review
queue, so a host becomes reviewable when something connects to it.

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
    "API_KEY": {"from": "hsh://openai-api-key", "hosts": ["api.openai.com"]}
  },
  "network": {"allow": ["api.openai.com", "registry.npmjs.org", "github.com"]}
}
```

The injected header would be `Api-Key: <value>` for requests to
`api.openai.com`. To send `Authorization: Bearer <value>` instead, set
`"header": "Authorization", "prefix": "Bearer "`.
