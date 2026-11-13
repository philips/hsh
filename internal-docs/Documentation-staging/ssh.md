# hsh ssh

The `hsh ssh` command lets your ssh client reach a server inside a session. The
command prints an ssh config stanza. The command also acts as an ssh
`ProxyCommand`.

<!-- tape:output tools/terminal-demos/out/ssh.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## Print the ssh config stanza

Type the command. The command prints a stanza. Add the stanza to
`~/.ssh/config`. Then `ssh hsh-SESSION` reaches the session.

<!-- tape:run sleep=2s -->
```sh
hsh ssh config
```
<!-- tape:wait /Host/ -->

<!-- tape END -->

## Reference

Usage:

```
hsh ssh SUBCOMMAND
```

### Subcommands

| Subcommand | Function |
|------------|----------|
| `config` | Print the `~/.ssh/config` stanza for `ssh hsh-SESSION`. |
| `proxy` | Speak SSH on stdin and stdout. Use as an ssh `ProxyCommand`. |

### Flags for `config` and `proxy`

| Flag | Function |
|------|----------|
| `--session STRING` | Set the session to connect to. |
| `--host STRING` | Set the ssh destination of the machine that runs hsh. |
| `--hsh-path STRING` | Set the path of the hsh binary on that machine. |
