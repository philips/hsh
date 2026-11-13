# hsh suspend

The `hsh suspend` command suspends the processes in a session. A suspended
session uses no CPU. Re-enter the session later with `hsh resume`. The resume
command wakes the processes.

<!-- tape:output tools/terminal-demos/out/suspend.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## Suspend a session

Type the command. Give the session. The command pauses the pod.

<!-- tape:run sleep=2s -->
```sh
hsh suspend my-session
```
<!-- tape:wait /suspended session/ -->

<!-- tape END -->

## Reference

Usage:

```
hsh suspend [SESSION]
```

The command takes no flags other than `-h, --help`. If you give no session, the
command suspends the session for the current workspace.
