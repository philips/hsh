# Pre-built harness images

hsh ships pre-built workspace images for popular coding agents, each
with the agent pre-installed and a default hsh config baked in as a
container label. Each image lives in `images/<name>/`:

- `Containerfile` — the build recipe.
- `hsh.json` — the pretty-printed `org.ifup.hsh` config (see below). It
  is version-controlled as readable JSON and compacted into the label
  at build time, so you never hand-edit a minified one-liner.
- any extra files the image copies in (e.g. `images/pi/auth.json`).

Published to ghcr.io by two workflows:

- `.github/workflows/release.yml` publishes `:latest` plus a version tag
  (matching the release tag) on every `v*` release.
- `.github/workflows/images.yml` republishes `:latest` on pushes to main
  that change `images/`, on a weekly schedule (so `fedora:latest` and the
  agent installers stay current), and build-checks PRs that change
  `images/`.

| Image | Harness | Installer |
|---|---|---|
| `ghcr.io/<owner>/hsh-pi` (`hsh run pi`) | [Pi](https://pi.dev) | `https://pi.dev/install.sh` |
| `ghcr.io/<owner>/hsh-vibe` | Mistral Vibe | `https://mistral.ai/vibe/install.sh` |
| `ghcr.io/<owner>/hsh-claude-code` | Claude Code | `https://claude.ai/install.sh` |
| `ghcr.io/<owner>/hsh-codex` | OpenAI Codex | `https://chatgpt.com/codex/install.sh` |
| `ghcr.io/<owner>/hsh-muse-code` | Meta Muse Code | `https://dev.meta.ai/install.sh` |

Build one locally with the helper, which reads `hsh.json`, compacts it
with `jq`, and passes it as the `HSH_LABEL` build arg:

```
$ images/build.sh vibe -t hsh-vibe
```

Extra arguments are forwarded to `docker`/`podman build`. Set
`CONTAINER_ENGINE` to force an engine. To build by hand, pass the arg
yourself:

```
$ podman build --build-arg "HSH_LABEL=$(jq -c . images/vibe/hsh.json)" images/vibe/
```

## The `org.ifup.hsh` label

Each image carries an `org.ifup.hsh` label whose value is a JSON config
in the [config file](config.md) format (without `image`, which comes
from the positional image argument). It starts with the config
`"version"`, which is required: an image whose label has another version, or
none, is refused (`rebuild the image`, or `update hsh` for a newer one) rather
than half-applied, and unknown fields in the label are errors too (see
[config versions](config.md#config-versions)). It provides the defaults the harness needs: the
provider API hosts in `network.allow`, and the `secrets` that reference
the [hsh secret store](config.md#the-hsh-secret-store) (`hsh://NAME`)
for the provider's API key.

It also declares the agent argv, in two forms:

```json
"agent": {
  "command": ["pi", "--append-system-prompt", "{network_policy}", "-p", "{prompt}"],
  "launch": ["pi", "--append-system-prompt", "{network_policy}"]
}
```

- `command` is the headless template `hsh run --tasks` substitutes the
  task prompt into
- `launch` is the argv an **interactive session auto-launches from the
  shell**: bash starts, sources its rcfiles, and runs the harness as a
  normal foreground job — Ctrl-Z suspends it and drops you at the bash
  prompt (`jobs`, `fg`, `bg` all work), and exiting the harness leaves
  you in the shell. Piped/non-interactive sessions do not launch
  anything, so scripted use is unaffected.

`{network_policy}` passes the [network notice](config.md#network-notice) to
the harness. The pi image appends it to pi's system prompt with
`--append-system-prompt`, which takes a file path. Images whose harness has no
such flag can list instruction files in `agent.context_files` instead (for
example `~/.codex/AGENTS.md`); the other images do not deliver the notice yet.

```json
"agent": {"command": ["pi", "-p", "{prompt}"]}
```

`command` is an argv template; `{prompt}` is replaced by the composed
task prompt. `pi -p`, `vibe -p`, `claude -p`, `codex exec`, and `muse exec`
are the headless invocations of the five harness images.

`hsh run IMAGE [WORKSPACE-DIR]` without `--config` uses the
image's label as its config:

```
$ hsh run ghcr.io/ifup/hsh-vibe:latest my-project
```

The workspace directory (the `WORKSPACE-DIR` positional, or the current
directory when omitted) is mounted at `/workspace` (or whatever `mounts`
the label specifies, resolved against the workspace directory), the
label's allowlist and secrets are applied, and only the image's
declared provider endpoints are reachable. The harness images pin
`"mode":"allowlist"` in their labels to keep that promise; the
default mode for configs without one is `https` (TLS to any host —
see [network policy modes](config.md#network-policy-modes)).

Precedence: `--config PATH` wins when given; otherwise the image label
is used. The image overrides the config's `image` field when a config
file is present.

The config is edited as `images/<name>/hsh.json` (pretty-printed) and
flows into the label through a build arg, so the Containerfile stays
agnostic to the config contents:

```dockerfile
ARG HSH_LABEL
LABEL org.ifup.hsh="${HSH_LABEL}"
```

The build (`images/build.sh` locally, the buildx step in CI) compacts
`hsh.json` with `jq -c` and passes it as `HSH_LABEL`. The value must
reach the builder as a shell-quoted `--build-arg`, never through
docker/build-push-action's `build-args` input: that input is parsed
as CSV with `"` as the quote character, which strips the JSON's double
quotes and produces a label hsh cannot parse.

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
  `api.openai.com`; also allows `ab.chatgpt.com`, `auth.openai.com`,
  and `chatgpt.com` and ships
  [OAuth capture providers](oauth.md#configuration) for both OpenAI
  subscription logins:
  - `openai` (`Sign in with ChatGPT`, browser-redirect): token endpoint
    `auth.openai.com/api/accounts/oauth/token`, api host
    `api.openai.com`. Deliver the browser callback with
    [`hsh oauth callback`](oauth.md#browser-redirect-logins).
  - `openai-codex` (`ChatGPT Plus/Pro`): token endpoint
    `auth.openai.com/oauth/token`, api hosts `chatgpt.com` and
    `ab.chatgpt.com`. The image preinstalls the
    `pi-openai-codex-device-login` extension, so this login defaults to
    a **device-code flow** - no loopback callback at all: open
    `https://auth.openai.com/codex/device` in any browser and enter the
    printed code. Only the `refresh_token` is captured (the client
    parses and resends the access token as-is, so it cannot be
    phantomized); the long-lived credential still never enters the
    workspace.

  The image bakes `images/pi/auth.json` into
  `/home/hsh/.pi/agent/auth.json` with a placeholder Mistral key (the
  proxy swaps in the store's real key on matching hosts — the
  [placeholder key pattern](config.md#secrets)).
- **vibe** — `hsh://mistral-api-key` (Bearer) to `api.mistral.ai`.
- **claude-code** — `hsh://anthropic-api-key` (`x-api-key`) to
  `api.anthropic.com`; also allows `claude.ai`,
  `downloads.claude.ai`, `github.com`,
  `http-intake.logs.us5.datadoghq.com`, `mcp-proxy.anthropic.com`,
  `platform.claude.com`, `raw.githubusercontent.com`, and
  `statsig.anthropic.com`.
- **codex** — `hsh://openai-api-key` (Bearer) to `api.openai.com`; also
  allows `ab.chatgpt.com`, `api.github.com`, `auth.openai.com`,
  `chatgpt.com`, `files.openai.com`, `github.com`, and
  `raw.githubusercontent.com` (account login and CLI flows).
- **muse-code** — `hsh://meta-api-key` (Bearer) to `api.meta.ai`; also
  allows `auth.meta.com`, `accountscenter.meta.com`, and `dev.meta.ai`
  (device-flow login and the installer). Muse Code refuses to start without
  a local credential, so the image bakes a placeholder `META_API_KEY`
  (muse sends it; the proxy replaces the header with the store's key —
  the [placeholder key pattern](config.md#secrets)).
  Set `MUSE_NO_AUTO_UPDATE=1` so the launcher's hourly self-update stays out
  of the allowlist (the weekly image rebuild keeps the binary current).
  For a Meta subscription login instead: `unset META_API_KEY`, then
  `muse login` (device flow through `auth.meta.com`).

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

## Nested sandboxes

hsh containers block nested user namespaces, so an agent's own
sandbox that needs them cannot run inside. The codex image therefore
ships `sandbox_mode = "danger-full-access"` in
`/home/hsh/.codex/config.toml`: codex's bwrap sandbox would fail with
`bwrap: setting up uid map: Operation not permitted`. This is safe
because the hsh sandbox already provides the isolation — network
allowlist, namespace confinement — that the nested sandbox would
attempt to add.
