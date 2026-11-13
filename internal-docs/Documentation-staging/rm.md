# hsh rm

The `hsh rm` command removes hsh sandboxes. The command works like
`podman pod rm`. Caution: the command removes the pod and its state.

<!-- tape:output tools/terminal-demos/out/rm.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## Remove a sandbox

Type the command. Give the sandbox id or name.

<!-- tape:run sleep=2s -->
```sh
hsh rm my-session
```

## Remove the exited sandboxes

Add the `--exited` flag. The command removes each sandbox whose shell exited.

<!-- tape:run sleep=2s -->
```sh
hsh rm --exited
```

<!-- tape END -->

## Reference

Usage:

```
hsh rm [FLAGS] SANDBOX...
```

| Flag | Function |
|------|----------|
| `-a, --all` | Remove all the hsh sandboxes. |
| `-f, --force` | Stop the running containers first. Then remove them. |
| `-i, --ignore` | Do not fail when a sandbox is missing. |
| `-t, --time INT` | Wait this many seconds for the pod to stop. Then kill it. |
| `-l, --latest` | Act on the most recent sandbox. |
| `--exited` | Remove each exited sandbox. |
| `--garbage` | Remove the session state directories whose pods are gone. |
