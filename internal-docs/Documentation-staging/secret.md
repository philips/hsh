# hsh secret

The `hsh secret` command manages the hsh secret store. The store holds secrets.
The store also holds the hosts that each secret may go to. A config references a
secret as `hsh://NAME`. hsh sends a secret only to the hosts you approved.

The store keeps the secrets unencrypted. The file mode is `0600`. Only your user
can read the file. This is the same trust level as `~/.netrc`.

<!-- tape:output tools/terminal-demos/out/secret.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## Add a secret

Type the command. Give a name and one host or more. The command asks for the
value with a hidden prompt.

<!-- tape:run noenter -->
```sh
hsh secret add GITHUB_TOKEN --host api.github.com
```
<!-- tape:key Enter -->
<!-- tape:sleep 1s -->
<!-- tape:type ghp_example -->
<!-- tape:key Enter -->
<!-- tape:sleep 2s -->

## List the secrets

Type the command. The command prints the names. The command never prints the
values.

<!-- tape:run sleep=2s -->
```sh
hsh secret ls
```
<!-- tape:wait /GITHUB_TOKEN/ -->

## Allow a secret for more hosts

Type the command. Give the name and the hosts.

<!-- tape:run sleep=2s -->
```sh
hsh secret allow GITHUB_TOKEN uploads.github.com
```

<!-- tape END -->

## Reference

Usage:

```
hsh secret add NAME --host HOST [--host HOST]... [--db PATH] [--force] [--value-stdin]
hsh secret allow NAME HOST... [--db PATH]
hsh secret disallow NAME HOST... [--db PATH]
hsh secret edit NAME [--db PATH]
hsh secret rm NAME [--db PATH]
hsh secret ls [--db PATH]
```

### Subcommands

| Subcommand | Function |
|------------|----------|
| `add` | Store a secret and the hosts it may go to. |
| `allow` | Let a secret go to more hosts. |
| `disallow` | Stop a secret from going to some hosts. |
| `edit` | Edit the hosts for a secret in `$EDITOR`. |
| `rm` | Remove a secret from the store. |
| `ls` | List the stored secret names. Never the values. |

### Flags for `add`

| Flag | Function |
|------|----------|
| `--db STRING` | Set the secret store path. |
| `--host STRING` | Add a host the secret may go to. Repeatable, or comma-separated. |
| `--force` | Overwrite an existing value. Keep the hosts unless `--host` is given. |
| `--value-stdin` | Read the value from stdin. |

### Flags for `allow`, `disallow`, `edit`, `rm`, `ls`

| Flag | Function |
|------|----------|
| `--db STRING` | Set the secret store path. |
