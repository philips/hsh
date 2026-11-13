# hsh oauth

The `hsh oauth` command manages OAuth logins in the sandbox. The command
delivers a loopback callback. The command shows the captured tokens. The command
moves a login into the secret store. The command never prints a token value.

<!-- tape:output tools/terminal-demos/out/oauth.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## Show the captured logins

Type the command. The command prints the providers for each session.

<!-- tape:run sleep=2s -->
```sh
hsh oauth status
```

## Promote a login to the secret store

Type the command. Give the provider and a name. The host refreshes the login
now. Every session can use the login.

<!-- tape:run sleep=2s -->
```sh
hsh oauth promote github my-github-login
```
<!-- tape:wait /promote/ -->

<!-- tape END -->

## Reference

Usage:

```
hsh oauth SUBCOMMAND
```

### Subcommands

| Subcommand | Function |
|------------|----------|
| `callback` | Replay a loopback callback URL into the waiting session. |
| `status` | Show the captured providers and the phantom counts per session. |
| `promote` | Move a captured login into the secret store. |
| `refresh` | Refresh a stored login now. Push the new token into the sessions. |
| `provider` | List, show, and override the OAuth provider descriptors. |

### `hsh oauth provider` subcommands

| Subcommand | Function |
|------------|----------|
| `ls` | List the providers: built in, overridden, or added by you. |
| `cat` | Show a provider descriptor and where each layer came from. |
| `edit` | Override a provider with a drop-in, or a full copy with `--full`. |
| `add` | Review the descriptors an image suggests. Copy the ones you accept. |

### Flags for `callback`

| Flag | Function |
|------|----------|
| `--session STRING` | Set the session to deliver to. |

### Flags for `status`

| Flag | Function |
|------|----------|
| `--session STRING` | Show one session only. |
| `--store STRING` | Read this OAuth token store file instead. |

### Flags for `promote`

| Flag | Function |
|------|----------|
| `--session STRING` | Set the session that holds the login. |
| `--force` | Replace an existing login with this name. |

### Flags for `provider cat`, `edit`, `add`

| Flag | Function |
|------|----------|
| `--shipped` | Show the built-in descriptor. Ignore your overrides. (`cat`) |
| `--full` | Edit a full copy instead of a drop-in. (`edit`) |
| `--drop-in STRING` | Set the drop-in file name. (`edit`) |
| `--from-image STRING` | Set the image whose label suggests descriptors. (`add`) |
| `-y, --yes` | Accept without asking. (`add`) |
| `--force` | Replace an existing descriptor file. (`add`) |
