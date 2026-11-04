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

The claude-code image ships `/home/hsh/.claude/settings.json` with
`{"tui":"fullscreen"}`. Claude Code picks its renderer once at startup, and
without the setting a fresh home (every new session) starts with the classic
main-screen renderer, which has no in-app scrollback; only later launches,
after remote flags are cached in `~/.claude.json`, would get the fullscreen
renderer. Users can switch with `/tui`.

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

### Image trust

The label is a config written by whoever built the image, and anyone can
publish an image. So hsh applies an image's built-in config only once the
user trusts that exact image. Until then `hsh run IMAGE` ignores the parts
that reach outside the sandbox:

- `network.allow` (the session starts with an empty allowlist),
- `network.mode` (forced to `allowlist`),
- `oauth` (no providers),
- `mounts` (reset to the default `.:/workspace`; the image could otherwise
  mount any host path, such as `~/.ssh`),

and warns with what it dropped, how to show the built-in config
(`podman image inspect --format '{{index .Labels "org.ifup.hsh"}}' IMAGE`),
and how to trust it: `hsh run --trust-image IMAGE` applies the whole config
and records the image id in `$XDG_CONFIG_HOME/hsh/trusted-images` (one line
per image: id, the reference it was trusted under, time). Later runs of the
same image id apply the config without the flag. The image id is the digest
of the image config, which holds the label, so a rebuilt or re-pulled image
with any change is untrusted again. Hosts denied in a session can still be
approved one by one through the monitor.

The rest of the built-in config applies either way: its secret routes
(already limited to the hosts approved for each secret,
[host binding](config.md#host-binding)), the agent argv, the shell, context
files, the network notice, and `approval_wait`, which only act inside the
sandbox or within the user's approvals. Images built locally
(`localhost/...`) and configs passed with `--config` are not restricted;
the shipped harness images are, until trusted once. A resumed session keeps
the config it was created with.

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
  and `chatgpt.com` and names the shipped
  [OAuth providers](oauth.md#provider-descriptors) for both OpenAI
  subscription logins (`"oauth": {"providers": ["openai", "openai-codex"]}`):
  - `openai` (`Sign in with ChatGPT`, browser-redirect): token endpoint
    `auth.openai.com/api/accounts/oauth/token`, api host
    `api.openai.com`. Deliver the browser callback with
    [`hsh oauth callback`](oauth.md#browser-redirect-logins).
  - `openai-codex` (`ChatGPT Plus/Pro`): token endpoint
    `auth.openai.com/oauth/token`, api hosts `chatgpt.com`,
    `ab.chatgpt.com` and `api.openai.com`. The image preinstalls the
    `pi-openai-codex-device-login` extension, so this login defaults to
    a **device-code flow** - no loopback callback at all: open
    `https://auth.openai.com/codex/device` in any browser and enter the
    printed code. The access and id tokens become claims-preserving JWT
    phantoms (pi reads the account id from the claims), the refresh
    token an opaque phantom.

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
  `statsig.anthropic.com`. Names the shipped `anthropic` OAuth provider,
  so a Claude Pro/Max login works over SSH: run `claude auth login`,
  open the URL on any machine, and paste the `code#state` the copy-code
  page shows. The sandbox stores `sk-ant-oat01-hsh_…` /
  `sk-ant-ort01-hsh_…` phantoms; the real tokens stay in the session's
  proxy directory. Promote that login once (`hsh oauth promote anthropic`) and later
  sessions start logged in: the label's optional `CLAUDE_LOGIN` secret
  (`hsh://anthropic-login`, seed `claude-code`) writes a phantom
  `~/.claude/.credentials.json` and the host keeps the login refreshed
  ([host-held logins](oauth.md#host-held-logins)).
- **codex** — `hsh://openai-api-key` (Bearer) to `api.openai.com`; also
  allows `ab.chatgpt.com`, `api.github.com`, `auth.openai.com`,
  `chatgpt.com`, `files.openai.com`, `github.com`, and
  `raw.githubusercontent.com` (account login and CLI flows). Names the
  shipped `openai-codex` OAuth provider: `codex login --device-auth`
  works over SSH (open `https://auth.openai.com/codex/device` anywhere
  and enter the code), and `auth.json` holds only claims-preserving JWT
  phantoms and an opaque refresh phantom. The browser login also works
  with `ssh -L 1455:localhost:1455` plus `hsh run --forward 1455`, or
  [`hsh oauth callback`](oauth.md#browser-redirect-logins). Promote the login (`hsh oauth promote openai-codex`) and later
  sessions start logged in through the label's optional `CODEX_LOGIN`
  secret (`hsh://openai-codex-login`, seed `codex`).
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
hsh secret add --host api.anthropic.com anthropic-api-key
```

The `--host` is the user's approval: a label can ask to send a stored
secret anywhere, but it is injected only for hosts approved for it
([host binding](config.md#host-binding)). A missing store entry is a
warning and the request is forwarded without the header. The container never sees the raw secret, only the injected
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
