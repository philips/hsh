## harnessh

harnessh is a developer focused container based sandbox for agent harness isolation. The goals include:

- Single install binary
- Standard isolation via Linux containers (podman)
- Optional configurable egress network isolation

The goal is to make it easy to hack on software while protecting you from moderate dangers of agents like credential exposure to inference providers OR local filesystem damage. If you are dealing with a potentially persistent attacker or hostile inference provider consider additional protection like a virtual machine.

## Technical Design

- A single Go binary for entire system
- Assume Podman is installed (do not use docker or docker-engine)

## Egress Proxy Design

Copy the design and/or implementation of agent-substrate's egress credential proxy but make it run without all of the Kubernetes dependencies. 

https://raw.githubusercontent.com/agent-substrate/substrate/refs/heads/main/docs/egress-credential-injection.md

## Inspiration

These projects documentation can be used as inpiration:

- https://github.com/EddyPronk/podman-agent-sandbox
- https://github.com/superhq-ai/shuru

TODO:
- Research if we can use HTTPS_PROXY instead of just MITM
