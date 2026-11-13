# hsh forward

The `hsh forward` command makes a server inside a session reachable. The server
listens on a port in the session. The command maps the port to loopback on this
machine.

<!-- tape:output tools/terminal-demos/out/forward.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## List the ports a session listens on

Add the `--list` flag. The command prints the ports.

<!-- tape:run sleep=2s -->
```sh
hsh forward --list
```

## Forward a port

Type the command. Give the port. The command maps the port to `127.0.0.1`.

<!-- tape:run sleep=2s -->
```sh
hsh forward 8080
```
<!-- tape:wait /8080/ -->

<!-- tape END -->

## Reference

Usage:

```
hsh forward [FLAGS] [PORT | LOCAL:PORT]...
```

| Flag | Function |
|------|----------|
| `--session STRING` | Set the session to reach. Default: the workspace session. |
| `--bind STRING` | Set the local address. Default: `127.0.0.1`. |
| `--list` | List the ports the session listens on. Then exit. |
| `--managed` | Run detached as a managed forward from the monitor. |
