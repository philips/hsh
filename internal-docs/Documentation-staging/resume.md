# hsh resume

The `hsh resume` command re-enters a session that exists. The command attaches
your terminal to the session shell. If the session exited, the command restarts
it. Give a name, a session id, or a workspace directory.

<!-- tape:output tools/terminal-demos/out/resume.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## List the sessions first

Type the command to see the sessions. Find the session to resume.

<!-- tape:run sleep=2s -->
```sh
hsh ls
```
<!-- tape:wait /SESSION/ -->

## Resume a session

Type the command. Give the session name or id.

<!-- tape:run sleep=3s -->
```sh
hsh resume my-session
```

<!-- tape END -->

## Reference

Usage:

```
hsh resume [FLAGS] [NAME | SESSION | WORKSPACE-DIR]
```

| Flag | Function |
|------|----------|
| `--secret-db STRING` | Use this secret store for `hsh://` references. |
| `--verbose` | Print progress to stderr. |
| `--take-over` | Detach the terminal that is attached to the session first. |
| `--forward STRINGARRAY` | Forward a session port to loopback. Repeatable. See `hsh forward`. |
