# hsh policy

The `hsh policy` command manages the network policy files. A policy is a JSON
file. The file holds host rules. The command applies a policy to a session. The
apply step copies the rules into the session. A later edit of the file does not
change a session that applied the policy.

<!-- tape:output tools/terminal-demos/out/policy.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## List the policies

Type the command. The command prints each policy and its rule count.

<!-- tape:run sleep=2s -->
```sh
hsh policy ls
```

## Add a policy

Type the command. Give a name and a file. The command validates the file. The
command stores the file under the name.

<!-- tape:run sleep=2s -->
```sh
hsh policy add ci policies/ci.json
```
<!-- tape:wait /added policy/ -->

## Apply a policy to a session

Type the command. Give the name. The command copies the rules into the session.

<!-- tape:run sleep=2s -->
```sh
hsh policy apply ci
```
<!-- tape:wait /applied policy/ -->

<!-- tape END -->

## Reference

Usage:

```
hsh policy SUBCOMMAND
```

### Subcommands

| Subcommand | Function |
|------------|----------|
| `ls` | List the network policies. |
| `show` | Print a network policy. |
| `add` | Validate a policy file and store it under a name. |
| `rm` | Remove a policy. A session that applied it keeps its copy. |
| `apply` | Copy a policy's rules into a session. |
| `unapply` | Remove a policy's rules from a session. |

### Flags for `add`

| Flag | Function |
|------|----------|
| `--force` | Replace an existing policy of the same name. |

### Flags for `apply` and `unapply`

| Flag | Function |
|------|----------|
| `--session STRING` | Set the session. Default: the workspace session. |
