# Env file for config secrets

> **Status: not implemented.** Deferred in favor of the secret store and
> the `secret add` / `secret rm` subcommands (plans/0009-secret-store.md).
> The env-file idea maps onto that design as a future `dotenv://`
> reference scheme — one more pluggable secret store.

## Goal

`secrets.<NAME>.from` currently reads only the host process environment.
Users naturally keep API keys in env files (dotenv-style `.env`), and the
repo already has one (`examples/.env` — which also demonstrates the
problem, see Security below). Add support for reading secret values from
an env file so `harnessh` can be pointed at one.

## Design decisions

### Where the file is specified

A top-level config field, resolved relative to the config file's
directory:

```json
{
  "envFile": ".env",
  "secrets": { ... }
}
```

Default: `.env` in the config directory, loaded **if present** and
silently skipped if absent (no config change needed for the common case).
An explicit path (e.g. `"envFile": "../secrets.env"` or an absolute path)
points elsewhere. A `--env-file PATH` flag on `harnessh run` overrides the
config (optional; can be added later if wanted).

### Format

Minimal dotenv:

- `KEY=VALUE` lines; whitespace around key/value trimmed
- `#` comments and blank lines skipped
- optional `export ` prefix accepted
- optional matching single/double quotes stripped from the value
- **no** variable interpolation, no multi-line values (documented)

### Precedence

`from` resolves in this order: **process environment first, then the env
file** (dotenv convention: the file fills in what the environment does not
provide). An explicitly exported variable always wins.

### Flow (unchanged guarantees)

env file → harnessh (host) → resolved into `secrets.json` → bind-mounted
(read-only, run directory) into the **proxy container only**. The workspace
never sees the values — same guarantee as environment-provided secrets.
File permissions of the env file are the user's responsibility (docs
recommend `chmod 600`).

### Hot reload

The host-side watcher currently re-resolves secrets when the config file
changes. It must also watch the env file: a change to either re-resolves
secrets and rewrites `secrets.json`, which the proxy picks up within a
second (directory bind mount, so file replacement is visible).

### Security — the important part

An env file that lives **inside a mounted directory is readable by the
sandboxed agent** — loading it does not change that; it only makes the
host side convenient. So:

- harnessh prints a warning when the resolved env file path is inside any
  mount source (the key is exposed to the sandbox regardless of injection).
- Docs recommend keeping the env file **outside** the mounts, e.g.
  `~/.config/harnessh/<project>.env`, referenced from the config with a
  relative or absolute path.
- The repo's own `examples/.env` (which sits inside the mounted
  `examples/` workspace) should move out of the mount, or the example
  should stop mounting the directory that contains it.

A hard refusal to load mounted env files was considered and rejected: it
would break the convenient layout for users who accept the exposure, and
the warning plus docs carry the point. (Could be revisited as a strict
mode later.)

## File changes

- `internal/config/config.go`: `EnvFile` field (default `.env`),
  `LoadEnvFile(path) (map[string]string, error)` dotenv parser,
  `ResolveSecrets` takes a lookup (process env first, then file values).
- `internal/cli/run.go`: resolve + load the env file, pass values into
  `ResolveSecrets`, warn if inside a mount, watch the env file alongside
  the config for hot reload.
- `docs/config.md`: `envFile` field, format, precedence, security note.
- `docs/architecture.md`: one paragraph in the secret-injection section.
- `examples/`: move/demonstrate the env file outside the mounted directory.
- `test/functional.sh`: a step that provides the secret only via an env
  file (not the process env) and asserts injection; a precedence check
  (process env wins over the file).

## Verification

1. Secret value present only in the env file → injected (proxy log +
   dummy endpoint header log).
2. Value in both process env and file → the process env value is used.
3. Missing env file → no error, secrets just unresolved (warning as today).
4. Env file edited mid-session → secrets re-resolve within ~1s (hot
   reload, both watchers).
5. Env file inside a mount → warning printed.
6. Malformed lines (no `=`, comments, quotes, `export `) → parsed or
   skipped per the documented rules.
7. Functional test extended and passing.

## Open questions

- Strict mode that refuses mounted env files (vs warning)? Later.
- Variable interpolation in values (`${OTHER}`)? Not in v1.
- A `harnessh proxy-auto-allow`-style helper for rotating keys? Out of
  scope.
