Create pre-built container images for the most popular agent harnesses with configurations setup for their providers.


# Installer Locations

## Pi install

```
curl -fsSL https://pi.dev/install.sh
```

Providers: TODO

## Mistral Vibe

```
curl -LsSf https://mistral.ai/vibe/install.sh | bash
```

## Claude Code

```
curl -fsSL https://claude.ai/install.sh | bash
```

## OpenAI Codex

```
curl -fsSL https://chatgpt.com/codex/install.sh | sh
```

# Endpoint Configuration for Proxy

To find the endpoints and other facts about the provider use the database from `pi`: https://github.com/earendil-works/pi/tree/d5b3de22d0e24a3e9c38a9410d3329b099717283/packages/ai/src/providers

A Containerfile Label in the format org.ifup.hsh should be used to provide a default config with configuration for the hsh secrets and network to access the provider.
