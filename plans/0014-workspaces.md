# Definitions 

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

# Lifecycle

Sessions are tied to the lifecycle of the pod 

# Configuration

Configuration should be stored in the state directory for a session and edited there. If the session is started from a container with a org.ifup.hsh label that config should be copied to the session directory for modification.

# Breaking Change Decisions

These are all OK changes. Do not implement backwards compatiblity.

- Multi-session per workspace breaks the existing state layout. The state layout will need to change to implement this. 
- proxy-auto-allow writes to the state files instead of a configuration file in the cwd
- a local or flag provided config.hsh is copied to the state directory and never modified in place
- use the podname suffix as the session idname
- hsh run --resume will error instead of creating as it does today
