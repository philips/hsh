# hsh ls

The `hsh ls` command lists the hsh sandboxes. A sandbox is a pod. The command
works like `podman pod ls`.

<!-- tape:output tools/terminal-demos/out/ls.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## List the sandboxes

Type the command. The command prints one row for each sandbox.

<!-- tape:run sleep=2s -->
```sh
hsh ls
```
<!-- tape:wait /SESSION/ -->

## List the session ids only

Add the `--quiet` flag. The command prints the ids. Scripts use these ids.

<!-- tape:run sleep=2s -->
```sh
hsh ls --quiet
```

<!-- tape END -->

## Reference

Usage:

```
hsh ls [FLAGS]
```

| Flag | Function |
|------|----------|
| `-q, --quiet` | Print the session ids only. |
| `-n, --noheading` | Do not print the headers. |
| `--no-trunc` | Do not truncate the pod and container ids. |
| `--sort STRING` | Sort by `created`, `id`, `name`, or `number`. |
| `-f, --filter STRING` | Filter the output. Repeatable. |
| `--format STRING` | Format the output with a Go template. |
| `--ctr-ids` | Show the container ids. |
| `--ctr-names` | Show the container names. |
| `--ctr-status` | Show the container status. |
| `--ns` | Show the namespace information. |
| `-l, --latest` | Show the most recent sandbox only. |
| `--workspace STRING` | Show the sandbox for this workspace directory only. |
