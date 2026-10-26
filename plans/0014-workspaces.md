# Definitions 

> **Status: implemented.**

Sessions are an instance of a hsh pod running on the host. 

```
ls $XDG_STATE_HOME/hsh/sessions/<session-id>/
config.json # the session's config copy — the only writable config
ca.crt # keys for mitm proxy
ca.key
secrets.json # resolved secrets for the running proxy
workspace # file with absolute path to workspace for session
```

Workspaces are a shared filesystem on the host machine. Generally this is going to be a folder in the users home directory with one or more git checkouts for the agent to use.

```
mkdir example-workspace
git clone https://github.com/philips/ghar
hsh run ghcr.io/philips/hsh-pi
bash-5.3#
```

There may be multiple sessions per workspace.

## Multiple Sessions for workspace

With `hsh run --resume` if there is only one session for the current workspace launch enter that session. Otherwise, list the sessions that exist.

```
cd example
hsh run --resume
error: multiple sessions for workspace example/
ffdc21513916
d0e828113916
```

User selects a session and can re-enter the container

```
cd example
hsh run --resume ffdc21513916
```

Note: the podman name hsh-ffdc21513916 is also acceptable

If no session exists in the current workspace just exit with an error

```
cd example
hsh run --resume
error: no session to resume, try hsh run IMAGE 
```
Sessions do not share approved proxy configuration on purpose. You may have a "web search" agent that does not have access to the Github credentials for example.

# Proxy Watch Coordination

To help the user make decisions on proxy approvals and denials proxy watch should have the ability to filter by workspaces and/or sessions. 

The denied.db should exist globally for all sessions and workspaces but display the workspace and session next to the request for the user to make the decision. 

```
cd
hsh proxy-watch --session a0e521513916 # watch for one session
hsh proxy-watch --workspace $CWD # all containers in this workspace
hsh proxy-watch # watch all workspaces and all sessions
```

This will require adding workspace + session columns on both denied and dns_denied.

`hsh proxy-auto-allow` will stop taking a config and take a session instead `hsh proxy-auto-allow --session <session_id>`
`hsh proxy-denied` will take `--session` or `--workspace` filters or list all with the new session/workspace columns
`hsh proxy-watch` will remove the `--config` flag and take `--session` or `--workspace` filters

# Lifecycle

`hsh` sessions started with `hsh run`, by default, persist. This is so users can re-enter the filesystem between reboots or other interruptions to the process. 

`hsh` sessions can be made temporary with `hsh run --rm` flag.

`hsh` sessions that are "idle" due to the primary process exiting will also stop the proxy process and the infra process to save resources.

Sessions can be removed manually by the user via `hsh rm` if the pods are stopped. Sessions that need cleanup due to pods missing can be removed with a new `hsh gc` command to garbage collect.

# Identity

`hsh run` passes the session id and workspace path to the proxy container so denials are tagged; rows display session + workspace

# Configuration

Configuration should be stored in the state directory for a session and edited there. If the session is started from a container with a org.ifup.hsh label that config should be copied to the session directory for modification.

# Breaking Change Decisions

These are all OK changes. Do not implement backwards compatiblity.

- Multi-session per workspace breaks the existing state layout. The state layout will need to change to implement this. 
- proxy-auto-allow writes to the state files instead of a configuration file in the cwd
- a local or flag provided config.hsh is copied to the state directory and never modified in place
- use the podname suffix as the session id
- hsh run --resume will error instead of creating as it does today
