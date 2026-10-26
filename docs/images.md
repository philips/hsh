# Pre-built harness images

hsh ships pre-built workspace images for popular coding agents, each
with the agent pre-installed and a default hsh config baked in as a
container label. Published per release to ghcr.io (see
`.github/workflows/release.yml`):

| Image | Harness | Installer |
|---|---|---|
| `ghcr.io/<owner>/hsh-pi` | [Pi](https://pi.dev) | `https://pi.dev/install.sh` |
| `ghcr.io/<owner>/hsh-vibe` | Mistral Vibe | `https://mistral.ai/vibe/install.sh` |
| `ghcr.io/<owner>/hsh-claude-code` | Claude Code | `https://claude.ai/install.sh` |
| `ghcr.io/<owner>/hsh-codex` | OpenAI Codex | `https://chatgpt.com/codex/install.sh` |

Build one locally with `podman build -t hsh-vibe images/vibe/`.

## The `org.ifup.hsh` label

Each image carries an `org.ifup.hsh` label whose value is a JSON config
in the [config file](config.md) format (without `image`, which comes
from `--image`). It provides the defaults the harness needs: the
provider API hosts in `network.allow`, and the `secrets` that map host
environment variables to the provider's API-key header.

`hsh run --image IMAGE` in a directory without a `config.hsh` uses the
image's label as its config:

```
$ cd my-project
$ hsh run --image ghcr.io/ifup/hsh-vibe:latest
```

The current directory is mounted at `/workspace` (or whatever `mounts`
the label specifies, resolved against the current directory), the
label's allowlist and secrets are applied, and only the image's
declared provider endpoints are reachable.

Precedence: a `config.hsh` file (or `--config PATH`) always wins — the
label is only a fallback for when no config file exists. `--image`
also overrides the `image` field when a config file is present.

The label JSON must be quoted in the Containerfile so the Dockerfile
parser preserves its double quotes:

```dockerfile
LABEL org.ifup.hsh='{"shell":"/bin/bash","network":{"allow":["api.mistral.ai"]}}'
```

## Per-image defaults

- **pi** — multi-provider: `MISTRAL_API_KEY` (Bearer) to
  `api.mistral.ai`, `ANTHROPIC_API_KEY` (`x-api-key`) to
  `api.anthropic.com`, `OPENAI_API_KEY` (Bearer) to `api.openai.com`.
- **vibe** — `MISTRAL_API_KEY` (Bearer) to `api.mistral.ai`.
- **claude-code** — `ANTHROPIC_API_KEY` (`x-api-key`) to
  `api.anthropic.com`; also allows `claude.ai` and
  `statsig.anthropic.com`.
- **codex** — `OPENAI_API_KEY` (Bearer) to `api.openai.com`; also
  allows `auth.openai.com` and `chatgpt.com` (account login flows).

Secrets are resolved from the host environment at launch time; the
container never sees the raw environment variable, only the injected
header on matching hosts.
