# hsh

`hsh` (pronounced hush) keeps your agents quiet: no open internet, no filesystem access

![](docs/hsh-logo.jpg)

## Overview

`hsh` is a developer focused Linux container based sandbox for agent harness isolation. The goals include:

- Single install binary
- Standard isolation via Linux containers (podman)
- Dynamic and easy egress network isolation

The goal is to make it easy to hack on software with an agent harness like pi,
codex, claude, etc while protecting you from moderate dangers of agents like
credential exfiltration to the open internet OR local filesystem damage.

If you are dealing with a potentially persistent attacker or hostile inference
provider consider additional protection like a virtual machine (which may be
supported via libkrun later).

## Technical Design

- A single Go binary for entire system
- Assume Podman is installed (do not use docker or docker-engine)

## Inspiration

These projects documentation can be used as inpiration:

- https://github.com/EddyPronk/podman-agent-sandbox
- https://github.com/superhq-ai/shuru
- https://nono.sh/

## TODO

See [TODO.md](TODO.md)
