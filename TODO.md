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

## Session listings

`hsh ls` doesn't have a session id header that can be copied and pasted into other tools. SESSION should come first and POD ID and INFRA ID are probably not necessary on the default output
```
POD ID        NAME              STATUS   CREATED       INFRA ID      # OF CONTAINERS  WORKSPACE
b75203c46672  hsh-29aff84d4f2e  Running  2 hours ago   e71a1beb37f4  3                /home/philips/roost/roost
6a73045df01b  hsh-91a75a47a298  Running  13 hours ago  6cd28dc88eaa  3                /home/philips/hsh
```

## hsh proxy allow

Investigate crash in `hsh proxy allow`
```
$ hsh proxy allow --watch --session 29aff84d4f2e --all
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x20 pc=0xa1bbac]

goroutine 1 [running]:
hsh/internal/cli.proxyAllowCommand.func2({0x0, 0x0}, {0x2eeb082fc140, 0x0, 0x58?})
	hsh/internal/cli/proxy_group.go:264 +0x6ec
hsh/internal/cli.newCommand.func1({0x0?, 0x0?}, {0x2eeb082fc140?, 0x0?, 0x2eeb08393de8?})
	hsh/internal/cli/root.go:33 +0x84
github.com/peterbourgon/ff/v4.(*Command).Run(0x0?, {0x0?, 0x0?})
	github.com/peterbourgon/ff/v4@v4.0.0-beta.1/command.go:146 +0x129
github.com/peterbourgon/ff/v4.(*Command).Run(0xb8e646?, {0x0?, 0x0?})
	github.com/peterbourgon/ff/v4@v4.0.0-beta.1/command.go:148 +0x138
github.com/peterbourgon/ff/v4.(*Command).Run(0x2eeb08369220?, {0x0?, 0x0?})
	github.com/peterbourgon/ff/v4@v4.0.0-beta.1/command.go:148 +0x138
github.com/peterbourgon/ff/v4.(*Command).ParseAndRun(0x2eeb08369220, {0x0, 0x0}, {0x2eeb082fc0f0?, 0x42abd6?, 0x119f000?}, {0x0?, 0x4211be?, 0x7ff1c709a108?})
	github.com/peterbourgon/ff/v4@v4.0.0-beta.1/command.go:158 +0x59
main.hostCommand({0x2eeb082fc0f0, 0x6, 0x6})
	hsh/main.go:38 +0x5e
main.main()
	hsh/main.go:25 +0x125
```

