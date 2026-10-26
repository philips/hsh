Sessions are an instance of a hsh pod running on the host. 

Workspaces are a shared filesystem on the host machine. Generally this is going to be a folder with one or more git checkouts for the agent to use.

There may be multiple sessions per workspace.

## Mutliple Sessions for workspace

```
mkdir example-workspace
git clone https://github.com/philips/ghar
hsh run ghcr.io/philips/hsh-pi
bash-5.3#
```
With `hsh run --resume` if there is only one session for the current workspace launch enter that session. Otherwise, list the sessions that exist.

```
cd example
hsh --resume
error: multiple sessions for workspace example/
ffdc21513916
d0e828113916
```

User selects a session and can re-enter the container

```
cd example
hsh --resume ffdc21513916
```

If no session exists in the current workspace just exit with an error

```
cd example
hsh --resume
error: no session to resume, try removing --resume flag and providing an image name
```
# Proxy Watch Coordination

To help the user make decisions on proxy approvals and denials proxy watch should have the ability to filter by workspaces and/or sessions. 

The denied.db should exist globally for all sessions and workspaces but display the workspace and session next to the request for the user to make the decision. 

```
cd
hsh proxy-watch --session a0e521513916 # watch for one session
hsh proxy-watch --workspace $CWD # all containers in this workspace
hsh proxy-watch # watch all workspaces and all sessions
```

# Configuration

Configuration should be stored in the state directory for a session and edited there. If the session is started from a container with a org.ifup.hsh label that config should be copied to the session directory for modification.
