# Configuration reference

harnessh reads a JSON config file (default: `./config.harnessh`, override
with `harnessh run --config PATH`). Relative mount sources are resolved
against the config file's directory.

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
(override with `harnessh run --workspace PATH`).

## Secrets

| Field | Required | Description |
|---|---|---|
| `from` | yes | host environment variable to read the value from |
| `hosts` | yes | hosts the secret is injected for (same matching as `network.allow`) |
| `header` | no | header to set; defaults to the secret name with `_` → `-` |
| `prefix` | no | string prepended to the value, e.g. `"Bearer "` |

If the environment variable named by `from` is not set, harnessh prints a
warning and the secret is not injected (fail-open for availability; the
request is still forwarded, just without the header).

Injection happens for every request to a matching host, replacing any value
the workspace sent. Hosts with a secret are TLS-intercepted; hosts without
one are tunneled.

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
database (`$XDG_STATE_HOME/harnessh/denied.db`, default
`~/.local/state/harnessh/denied.db`). Two subcommands manage it:

```
harnessh proxy-denied [--all]              # list hosts awaiting review
harnessh proxy-auto-allow [--config PATH]  # add them to network.allow
```

`proxy-auto-allow` rewrites the config file as canonical JSON (keys
sorted, 2-space indent); only `network.allow` changes.

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
