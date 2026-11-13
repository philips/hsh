# hsh proxy

The `hsh proxy` command reviews the records of the egress proxy. The proxy
blocks a host that the network policy does not allow. Use this command to see
the denied hosts. Use this command to decide a host.

<!-- tape:output tools/terminal-demos/out/proxy.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## List the denied hosts

Type the command. The command prints each host the proxy denied.

<!-- tape:run sleep=2s -->
```sh
hsh proxy denied
```

## Allow a denied host

Type the command. Give the host. The command adds the host to the session
config. The agent can reach the host now.

<!-- tape:run sleep=2s -->
```sh
hsh proxy allow api.example.com
```

## List the session rules

Type the command. The command prints the allow, deny, and log rules.

<!-- tape:run sleep=2s -->
```sh
hsh proxy rules
```
<!-- tape:wait /PATTERN/ -->

<!-- tape END -->

## Reference

Usage:

```
hsh proxy SUBCOMMAND
```

### Subcommands

| Subcommand | Function |
|------------|----------|
| `denied` | List the hosts the proxy denied. |
| `allow` | Allow the denied hosts of a session (TOFU). |
| `deny` | Deny a host for a session, in every network mode. |
| `rules` | List the allow, deny, and log rules of a session. |
| `report` | Summarize the connection log of a session, or wait for new entries. |

### Flags for `denied`

| Flag | Function |
|------|----------|
| `--watch` | Output each new denied request. |
| `--all` | Also list the hosts you decided or quieted, with their state. |

### Flags for `allow`

| Flag | Function |
|------|----------|
| `--session STRING` | Set the session the hosts are allowed in. |
| `--all` | Allow every host now denied for the session. |
| `--watch` | Approve each new denied request. |

### Flags for `deny`

| Flag | Function |
|------|----------|
| `--session STRING` | Set the session the host is denied in. |

### Flags for `rules`

| Flag | Function |
|------|----------|
| `--session STRING` | Set the session. Default: the workspace session. |

### Flags for `report`

| Flag | Function |
|------|----------|
| `--session STRING` | Set the session. Default: the workspace session. |
| `--format STRING` | Set the format: `text`, `policy`, or `json` (with `--wait`). |
| `--wait` | Block until the log has new entries. Print them. Then exit. |
| `--since INT` | With `--wait`: show entries after this id. |
| `--denied` | With `--wait`: show the denied entries only. |
| `--timeout DUR` | With `--wait`: give up after this time. Exit status 2. |
