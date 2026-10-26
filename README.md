# hsh

`hsh` (pronounced hush) is a developer focused Linux container sandbox to keep the agents under your care from chatting with systems you don't want them to using a MitM proxy HTTPS proxy with secret injection and containerized filesystem access. 

<p align="right">
    <img src="docs/hsh-logo.jpg" alt="emoji whisper face in water color and inked lines" width="300">
</p>

## Overview

The goals of `hsh` isolation include:

- Single install binary
- Standard isolation via Linux containers (podman)
- Dynamic and easy egress network isolation

The goal is to make it easy to hack on software with an agent harness like pi,
codex, claude, etc while protecting you from moderate dangers of agents like
credential exfiltration to the open internet OR local filesystem damage.

If you are dealing with a potentially persistent attacker or hostile inference
provider consider additional protection like a virtual machine (which may be
supported via libkrun later).

## Getting Started

Build the project:

```
git clone https://github.com/philips/hsh
cd hsh
CGO_ENABLED=0 go build -o hsh .
```

Build the example pi container

```
podman build -t hsh-example examples/
cd examples/ 
../hsh run
```

This example is designed for use with the [Mistral API](https://console.mistral.ai/api-keys). Grab an API key and export it:

```
export MISTRAL_API_KEY=mstrl_12345
```

Now run and configure thee `pi` agent harness. 

```
pi
/login mistral
```

And type in `foo` or something. The correct Authorization header will be injected with the request to `api.mistral.ai`. See [config.hsh](https://github.com/philips/hsh/blob/main/examples/config.hsh) for how the proxy was configured.

The container will have access to the examples/ directory on your host via the /workspace folder in the container. 

## Technical Design

- A single Go binary for entire system
- Assume Podman is installed (do not use docker or docker-engine)

## Inspiration

These projects documentation can be used as inpiration:

- https://github.com/EddyPronk/podman-agent-sandbox
- https://github.com/superhq-ai/shuru
- https://nono.sh/
- https://github.com/agent-substrate/substrate/blob/main/docs/egress-credential-injection.md

## TODO

See [TODO.md](TODO.md)
