# hsh

`hsh` (pronounced hush) is a developer focused Linux container sandbox to keep the agents under your care from chatting with systems you don't want them to using a MitM proxy HTTPS proxy with secret injection and containerized filesystem access.

To use an analogy: `hsh` is the exam proctor keeping the room quiet so your little agents [don't cheat](https://en.wikipedia.org/wiki/OpenAI_rogue_agent_breach_of_Hugging_Face).

<p align="right">
    <img src="docs/hsh-logo.jpg" alt="emoji whisper face in water color and inked lines" width="300">
</p>

## Overview

The goals of `hsh` isolation include:

- Single install binary
- Rootless Linux containers
- Dynamic egress network isolation

The goal is to make it easy to hack on software alongside an agent harness like
`pi`, `codex`, `claude-code`, etc while protecting you from moderate dangers of
agents like credential exfiltration to the open internet OR local filesystem
damage.

If you are dealing with a potentially persistent attacker or hostile inference
provider consider additional protection like a virtual machine (which may be
supported via libkrun later).

Install `hsh`

```
curl -fsSL https://raw.githubusercontent.com/philips/hsh/main/install.sh | sh
```

<!--- install.sh START -->

Create a workspace for your agent to work under.
This will be mounted to `/workspace` in container.

```
mkdir agent-pen
cd agent-pen
```

Setup your mistral credentials (or openai/claude/etc).

The egress proxy will inject the correct Authorization header.

```
hsh secret add mistral-api-key
```

Run pi and configure it to use mistral

```
hsh run pi
/login mistral
<type foo or anything as the API key>
```

By default `hsh run pi` will have an allowlist egress network policy.

In a separate window run `hsh monitor`. This TUI enables you to filter and
approve domain names quickly without editing JSON.

`monitor` views all hsh sessions on the host. Type `/` to filter.

```
hsh monitor
```

Start vibing

<!--- install.sh END -->

## Inspiration

These projects inspired some of the ideas behind hsh

- https://github.com/agent-substrate/substrate/blob/main/docs/egress-credential-injection.md
- https://github.com/superhq-ai/shuru
- https://nono.sh/
- https://github.com/EddyPronk/podman-agent-sandbox

## TODO

See [TODO.md](TODO.md)
