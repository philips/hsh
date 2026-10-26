# Pre-built harness container images

> **Status: implemented.** One image per agent harness under `images/`,
> each with an `org.ifup.hsh` label providing the default config
> (hsh secret store references, network allowlist, headless agent
> command). Published to ghcr.io by `.github/workflows/release.yml`
> (version tags + latest) and `.github/workflows/images.yml` (latest on
> pushes to main, weekly refresh, PR build checks). See docs/images.md.

## Goal

Create pre-built container images for the most popular agent harnesses
with configurations set up for their providers.

## Images and installers

| Image | Harness | Installer |
|---|---|---|
| `images/pi/` | Pi (multi-provider) | `curl -fsSL https://pi.dev/install.sh \| sh` |
| `images/vibe/` | Mistral Vibe | `curl -LsSf https://mistral.ai/vibe/install.sh \| bash` |
| `images/claude-code/` | Claude Code | `curl -fsSL https://claude.ai/install.sh \| bash` |
| `images/codex/` | OpenAI Codex | `curl -fsSL https://chatgpt.com/codex/install.sh \| sh` |

## Endpoint configuration for the proxy

Provider endpoints and auth headers are taken from pi's provider
database: https://github.com/earendil-works/pi/tree/main/packages/ai/src/providers

A Containerfile label in the format `org.ifup.hsh` provides the default
config for each image: the provider API hosts in `network.allow`
(allowlist mode), the `secrets` mapping the hsh secret store to the
provider's API-key header, and the `agent` command template for
headless runs.
