## Integrated update command

`hsh update` download latest release

## config versioning

version config schema so that `hsh` doesn't try to use container schema it doesn't understand

## version subcommand

include git commit and tag

## Investigate using libghostty

Investigate using libghostty and a custom escape sequence so that sessions can be attached/detached and the monitor can pop-over the session for firewall approvals. Essentially an application specific tmux.

## Investigate using process control

Another way to have the pop-over firewall and usage approvals would be that the container has access to a unix domain socket to the proxy process. This unix domain socket is only ever given access to the agents controlling shell. The proxy would then send a SIGPAUSE (like ctrl+z) to the agent dropping the user to the admin terminal where they could run a command to approve/deny requests. 

We would need to protect the agent process from reading the unix socket- maybe using landlock LSM or something else???
