# Pre-built harness images

hsh ships pre-built workspace images for popular coding agents, each
with the agent pre-installed and a default hsh config baked in as a
container label. Published to ghcr.io by two workflows:

- `.github/workflows/release.yml` publishes `:latest` plus a version tag
  (matching the release tag) on every `v*` release.
- `.github/workflows/images.yml` republishes `:latest` on pushes to main
  that change `images/`, on a weekly schedule (so `fedora:latest` and the
  agent installers stay current), and build-checks PRs that change
  `images/`.

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
from the positional image argument). It provides the defaults the harness needs: the
provider API hosts in `network.allow`, and the `secrets` that reference
the [hsh secret store](config.md#the-hsh-secret-store) (`hsh://NAME`)
for the provider's API key.

It also declares the agent argv, in two forms:

```json
"agent": {"command": ["pi", "-p", "{prompt}"], "launch": ["pi"]}
```

- `command` is the headless template `hsh run --tasks` substitutes the
  task prompt into
- `launch` is the argv an **interactive session auto-launches from the
  shell**: bash starts, sources its rcfiles, and runs the harness as a
  normal foreground job — Ctrl-Z suspends it and drops you at the bash
  prompt (`jobs`, `fg`, `bg` all work), and exiting the harness leaves
  you in the shell. Piped/non-interactive sessions do not launch
  anything, so scripted use is unaffected.

```json
"agent": {"command": ["pi", "-p", "{prompt}"]}
```

`command` is an argv template; `{prompt}` is replaced by the composed
task prompt. `pi -p`, `vibe -p`, `claude -p`, and `codex exec` are the
headless invocations of the four harness images.

`hsh run IMAGE` in a directory without a `config.hsh` uses the
image's label as its config:

```
$ cd my-project
$ hsh run ghcr.io/ifup/hsh-vibe:latest
```

The current directory is mounted at `/workspace` (or whatever `mounts`
the label specifies, resolved against the current directory), the
label's allowlist and secrets are applied, and only the image's
declared provider endpoints are reachable. The harness images pin
`"mode":"allowlist"` in their labels to keep that promise; the
default mode for configs without one is `https` (TLS to any host —
see [network policy modes](config.md#network-policy-modes)).

Precedence: a `config.hsh` file (or `--config PATH`) always wins — the
label is only a fallback for when no config file exists. The image
also overrides the `image` field when a config file is present.

The label JSON must be quoted in the Containerfile so the Dockerfile
parser preserves its double quotes:

```dockerfile
LABEL org.ifup.hsh='{"shell":"/bin/bash","network":{"allow":["api.mistral.ai"]}}'
```

## Headless task runs

Give the agent a list of tasks and run it to completion:

```
$ hsh run ghcr.io/<owner>/hsh-pi --tasks setup-tasks.txt
```

`setup-tasks.txt` is newline-delimited (blank lines and `#` comments
skipped); the tasks are composed into one numbered prompt and given to
the image's agent (`agent.command` from the label) as a headless run.
hsh streams the output and exits with the agent's exit code. The task
file is read on the host — only the composed prompt enters the
sandbox. `--tasks -` reads the task list from stdin.

## Per-image defaults

- **pi** — multi-provider: `hsh://mistral-api-key` (Bearer) to
  `api.mistral.ai`, `hsh://anthropic-api-key` (`x-api-key`) to
  `api.anthropic.com`, `hsh://openai-api-key` (Bearer) to
  `api.openai.com`.
- **vibe** — `hsh://mistral-api-key` (Bearer) to `api.mistral.ai`.
- **claude-code** — `hsh://anthropic-api-key` (`x-api-key`) to
  `api.anthropic.com`; also allows `claude.ai` and
  `statsig.anthropic.com`.
- **codex** — `hsh://openai-api-key` (Bearer) to `api.openai.com`; also
  allows `auth.openai.com` and `chatgpt.com` (account login flows).

Secrets are resolved from the host-side hsh secret store at launch
time, so before the first run store the provider key under the image's
expected name:

```
hsh secret add anthropic-api-key
```

A missing store entry is a warning and the request is forwarded without
the header (the same fail-open behavior as an unset environment
variable). The container never sees the raw secret, only the injected
header on matching hosts.
