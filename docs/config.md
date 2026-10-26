# Configuration reference

hsh reads a JSON config file (default: `./config.hsh`, override
with `hsh run --config PATH`). Relative mount sources are resolved
against the config file's directory.

Without a config file, `hsh run --image IMAGE` falls back to the
image's `org.ifup.hsh` label as the config — see
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
| `secrets` | `{}` | map of secret name → secret definition (below) |
| `network.mode` | `https` | network policy: `https` (TLS to any host on 443), `allowlist` (only `network.allow`, any port), `none` (no egress) |
| `network.allow` | `[]` | egress allowlist; in `https` mode it is an overlay (plaintext and non-443 ports), in `allowlist` mode it is the whole policy |
| `oauth` | `{}` | map of provider name → OAuth capture definition ([sandboxed OAuth logins](oauth.md)) |
| `agent` | `{}` | `agent.command`: argv template for headless runs; `{prompt}` is replaced with the task prompt ([headless task runs](images.md#headless-task-runs)) |

The workspace starts in the first mount's target directory
(override with `hsh run --workspace PATH`).

## Secrets

| Field | Required | Description |
|---|---|---|
| `from` | yes | secret reference: `env://VAR`, `hsh://NAME`, or a bare `VAR` (treated as `env://VAR`) |
| `hosts` | yes | hosts the secret is injected for (same matching as `network.allow`) |
| `header` | no | header to set; defaults to the secret name with `_` → `-` |
| `prefix` | no | string prepended to the value, e.g. `"Bearer "` |

The scheme of `from` decides where the value comes from. The design
follows nono.sh's credential injection — pluggable secret backends with
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

The common case is a bearer token for an LLM API — name the header and
prefix explicitly, because the default header name (the secret name with
`_` → `-`, e.g. `Api-Key`) is not what APIs read:

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
  stored **unencrypted**, readable only by your user — the same trust
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

- `https` (default) — any host may be reached over TLS on port 443
  (tunneled blind; interception stays limited to hosts with configured
  secrets or OAuth endpoints). Plaintext HTTP and non-443 ports are
  denied unless the host is in `network.allow`. DNS resolves every
  name.
- `allowlist` — only hosts in `network.allow`, any port; DNS is gated
  by the same list. The pre-built harness images use this mode.
- `none` — no egress; DNS is refused.

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
when the config file changes (the proxy polls once per second). `image`,
`shell`, and `mounts` require restarting the sandbox.

## Managing the allowlist

Denied egress attempts are answered with `407` and recorded in a SQLite
database (`$XDG_STATE_HOME/hsh/denied.db`, default
`~/.local/state/hsh/denied.db`). Two subcommands manage it:

```
hsh proxy-denied [--all]              # list hosts awaiting review
hsh proxy-auto-allow [--config PATH]  # add them to network.allow
```

`proxy-auto-allow` rewrites the config file as canonical JSON (keys
sorted, 2-space indent); only `network.allow` changes.

`network.allow` also gates DNS: the workspace resolves names through a
forwarder in the proxy that refuses any name not on the allowlist. DNS
denials are recorded separately (`hsh proxy-denied --dns`) and are
never auto-allowed.

## Tools that self-download

Some tools download helper binaries on first run instead of using system
packages. Pi (the coding agent), for example, downloads `fd` and `ripgrep`
from GitHub when they are not already on `PATH`. Inside the sandbox those
downloads are denied unless the hosts are allowlisted:

- `github.com` — release version lookup and download URLs
- `release-assets.githubusercontent.com` — where GitHub release assets redirect
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
