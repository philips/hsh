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
      "from": "OPENAI_API_KEY",
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
| `network.allow` | `[]` | egress allowlist; empty means no egress at all |

The workspace starts in the first mount's target directory
(override with `hsh run --workspace PATH`).

## Secrets

| Field | Required | Description |
|---|---|---|
| `from` | yes | host environment variable to read the value from |
| `hosts` | yes | hosts the secret is injected for (same matching as `network.allow`) |
| `header` | no | header to set; defaults to the secret name with `_` → `-` |
| `prefix` | no | string prepended to the value, e.g. `"Bearer "` |

If the environment variable named by `from` is not set, hsh prints a
warning and the secret is not injected (fail-open for availability; the
request is still forwarded, just without the header).

Injection happens for every request to a matching host, **replacing** any
value the workspace sent for that header. Hosts with a secret are
TLS-intercepted; hosts without one are tunneled.

The common case is a bearer token for an LLM API — name the header and
prefix explicitly, because the default header name (the secret name with
`_` → `-`, e.g. `Api-Key`) is not what APIs read:

```json
"secrets": {
  "API_KEY": {
    "from": "MISTRAL_API_KEY",
    "hosts": ["api.mistral.ai"],
    "header": "Authorization",
    "prefix": "Bearer "
  }
}
```

With this, the proxy rewrites the request's `Authorization` header to
`Bearer <value>`, so tools inside the sandbox can be signed in with a
placeholder key (e.g. in pi's `auth.json`) and never see the real one.
`from` reads the **host** environment (`export MISTRAL_API_KEY=...` before
launching); hsh does not read `.env` files, and keys should not live
inside mounted directories where the sandbox can read them.

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
    "API_KEY": {"from": "OPENAI_API_KEY", "hosts": ["api.openai.com"]}
  },
  "network": {"allow": ["api.openai.com", "registry.npmjs.org", "github.com"]}
}
```

The injected header would be `Api-Key: <value>` for requests to
`api.openai.com`. To send `Authorization: Bearer <value>` instead, set
`"header": "Authorization", "prefix": "Bearer "`.
