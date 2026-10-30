# Sessions

`hsh` sessions start with a container images and launch a number of processes
together in a secure sandbox. The sandbox may be isolated from the host via
gVisor or Linux Containers.

## Session Lifecycle

Sessions have 3 common phases that are seen in `hsh ls`.

- Running: session is running and attached to a terminal
- Detached: session is running and detached from a terminal
- Exited: session processes are not running and can be restarted

In Detached and Exited states a user can resume the session with `hsh resume`.:

## Session Setup

A session begins like `hsh run pi .` which does a sequence of tasks:

- downloads a container image, in this case `ghcr.io/philips/hsh-pi`
- creates a session directory in `~/.local/state/hsh/sessions`
- unpacks the container image and creates a container filesystem (managed by podman)
- sets up a virtual network with one end connected to the `hsh-proxy` controlling egress
- create a pty (terminal) that is connected to a proxy for `hsh monitor`
- launches the agent process

At this point the session can be used to do agentic work in Pi.
