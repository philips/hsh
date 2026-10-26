Sessions are an instance of a hsh pod running on the host. 

Workspaces are a shared filesystem on the host machine. Generally this is going to be a folder with one or more git checkouts for the agent to use.

There may be multiple sessions per workspace.

```
mkdir example-workspace
git clone https://github.com/philips/ghar
hsh run ghcr.io/philips/hsh-pi
bash-5.3#
```

To help the user make decisions on proxy approvals and denials proxy watch should have the ability to filter by workspaces and/or sessions. 

```
cd
hsh proxy-watch --session a0e521513916
```

With `hsh run --resume` if there is only one session for the current workspace launch enter that session. Otherwise, list the sessions that exist.

```
cd example
hsh --resume
error: multiple sessions for workspace example/
ffdc21513916
d0e828113916
```
