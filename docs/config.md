# Configuration reference

hsh reads a JSON config file (default: `./config.hsh`, override
with `hsh run --config PATH`). The config file is a seed: every run
copies it into the session's state directory
(`$XDG_STATE_HOME/hsh/sessions/<session>/config.json`) and the copy -
not the seed - is the live config, hot-reloaded and edited by the
review commands. The seed file is never modified in place. Relative
mount sources are resolved against the seed config's directory and
stored as absolute paths in the session copy.

Without a config file, `hsh run IMAGE` falls back to the
image's `org.ifup.hsh` label as the config - see
[pre-built harness images](images.md).

```json
{
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

## Fields

| Field | Default | Description |
|---|---|---|
| `image` | `docker.io/library/alpine:latest` | container image for both the proxy and workspace containers |
| `shell` | `/bin/sh` | shell to exec in the workspace |
| `mounts` | `[".:/workspace"]` | list of `src:dst[:opts]` bind mounts (podman volume syntax) |
| `secrets` | `{}` | map of secret name -> secret definition (below) |
| `network.mode` | `https` | network policy: `https` (TLS to any host on 443), `allowlist` (only `network.allow`, any port), `none` (no egress) |
| `network.allow` | `[]` | egress allowlist; in `https` mode it is an overlay (plaintext and non-443 ports), in `allowlist` mode it is the whole policy |
| `oauth` | `{}` | map of provider name -> OAuth capture definition ([sandboxed OAuth logins](oauth.md)) |
| `detach_keys` | `ctrl-]` | podman key sequence that detaches an interactive session (`ctrl-a,d` style); empty disables detaching |
| `monitor` | `{"enabled": true, "status_line": true, "key": "ctrl-space"}` | inline proxy review in interactive sessions ([below](#inline-proxy-review)); `enabled: false` attaches the container directly |
| `agent` | `{}` | `agent.command`: argv template for headless runs; `{prompt}` is replaced with the task prompt ([headless task runs](images.md#headless-task-runs)); `agent.launch`: argv auto-launched from an interactive bash session |

The workspace starts in the first mount's target directory
(override with `hsh run --workspace PATH`).

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

- the bottom terminal row is a status line: green, `proxy: nothing to
  review`, or `* N proxy requests need review | Ctrl+Space to review` as the
  proxy records denials for the session (`"status_line": false` removes the
  reserved row)
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
proxy's denial database, never from what the session prints.

## Managing the allowlist

Denied egress attempts are answered with `407` and recorded in a SQLite
database (`$XDG_STATE_HOME/hsh/denied.db`, default
`~/.local/state/hsh/denied.db`). Two subcommands manage it:

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

## Examples

Minimal dev sandbox with an API key:

```json
{
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
