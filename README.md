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
- Optional gVisor support for VM-like isolation
- Dynamic egress network isolation
- Simple OCI (docker) agent packaging

The goal is to make it easy to hack on software alongside an agent harness like
`pi`, `codex`, `claude-code`, etc while protecting you from moderate dangers of
agent credential exfiltration to the open internet OR local filesystem damage.

Install `hsh`

```
curl -fsSL https://raw.githubusercontent.com/philips/hsh/main/install.sh | sh
```

<!--- install.sh START -->

Create a workspace for your agent to work under.
This will be mounted to `/workspace` in container.

```
mkdir my-project
cd my-project
```

Setup your mistral.ai credentials and the egress proxy will inject the correct
Authorization header.

```
hsh secret add mistral-api-key
```

Run pi and configure it to use mistral

```
hsh run pi .
```

`hsh run pi` will configure a egress network policy allowing the agent to
connect to OpenAI, Anthropic, and Mistral LLM domains. If the agent tries to
access something else it will fail to connect.

For example, tell the agent to install golang. Then watch hsh's egress monitor
by pressing Ctrl+<Space>.

Press `a` to approve all of the requested hostnames and press enter to add them
to this session's allowlist.

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
