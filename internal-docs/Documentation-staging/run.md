# hsh run

The `hsh run` command starts a new sandboxed session. The command puts an agent
harness inside a container. The container has a network policy and a secret
store. Re-enter the session later with `hsh resume`.

<!-- tape:output tools/terminal-demos/out/run.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## Start a session

Type the command. Give an image and a workspace directory. The agent starts in
the workspace.

<!-- tape:run typingspeed=90ms sleep=4s -->
```sh
hsh run pi .
```
<!-- tape:wait /hsh/ -->

## Start a temporary session

Add the `--rm` flag. The command removes the pod and the state at exit.

<!-- tape:run sleep=3s -->
```sh
hsh run --rm pi .
```

<!-- tape END -->

## Reference

Usage:

```
hsh run [FLAGS] IMAGE [WORKSPACE-DIR]
```

| Flag | Function |
|------|----------|
| `--config STRING` | Seed the session config from this file. Default: the image label. |
| `--workdir STRING` | Set the start directory in the container. Default: the workspace mount. |
| `--secret-db STRING` | Use this secret store for `hsh://` references. |
| `--tasks STRING` | Read tasks from a file (or `-` for stdin). Run the agent headless. |
| `--network STRING` | Set the network policy: `https`, `allowlist`, `report`, or `none`. |
| `--pull STRING` | Set the image pull policy: `always`, `missing`, `newer`, or `never`. |
| `--verbose` | Print progress to stderr. |
| `--rm` | Remove the pod and the state at exit. |
| `--trust-image` | Apply the hsh config built into the image. Remember the image. |
| `--runtime STRING` | Set the runtime: `gvisor` or `container`. |
| `--forward STRINGARRAY` | Forward a session port to loopback. Repeatable. See `hsh forward`. |
