# hsh detach

The `hsh detach` command detaches the terminal from a session. The session keeps
running. Re-enter the session later with `hsh resume`.

<!-- tape:output tools/terminal-demos/out/detach.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## Detach a session

Type the command. Give the session. The session continues to run.

<!-- tape:run sleep=2s -->
```sh
hsh detach my-session
```

<!-- tape END -->

## Reference

Usage:

```
hsh detach [SESSION]
```

The command takes no flags other than `-h, --help`. If you give no session, the
command detaches the session for the current workspace.
