# Secret store: `harnessh secret add` / `harnessh secret rm` and URL secret references

## Goal

Give harnessh a first-party secret store so users stop hand-editing
config files and stop exporting API keys into their shell environment:

- `harnessh secret add NAME` stores a secret value in a SQLite database
  under `$XDG_CONFIG_HOME/harnessh` (default `$HOME/.config/harnessh`).
- `harnessh secret rm NAME` removes it.
- The config file references secrets by URL: `env://VAR` for environment
  variables and `harnessh://NAME` for the harnessh secret store:

```json
"secrets": {
  "API_KEY": {
    "from": "harnessh://mistral-api-key",
    "hosts": ["api.mistral.ai"],
    "header": "Authorization",
    "prefix": "Bearer "
  }
}
```

The scheme makes the reference pluggable: other stores can be added over
time (`nono://`, `keyring://`, `dotenv://`, ...) without changing the
config format. nono.sh's credential-injection feature is the model for
this design — pluggable secret backends (macOS Keychain, Linux Secret
Service, 1Password, Bitwarden, `pass`, custom keyring services) with
host-scoped credential routes and proxy injection instead of environment
injection:
https://nono.sh/docs/cli/features/credential-injection

## Secret references

`secrets.<NAME>.from` becomes a reference URL:

| Reference | Meaning | Example |
|---|---|---|
| `env://VAR` | host process environment variable | `env://MISTRAL_API_KEY` |
| `harnessh://NAME` | the harnessh secret store (sqlite) | `harnessh://mistral-api-key` |
| bare `VAR` | treated as `env://VAR` (backwards compatibility) | `MISTRAL_API_KEY` |

A reference parser (`config.ParseSecretRef`) splits scheme and name,
rejects unknown schemes with a clear error, and resolution dispatches on
the scheme. Adding a store = registering a scheme plus a `Store`
implementation; the config format and the proxy do not change. The
`from` field keeps its name (only its value format changes).

## The secret store

- Location: `$XDG_CONFIG_HOME/harnessh/secrets.db` (default
  `$HOME/.config/harnessh/secrets.db`); a `--db` flag overrides it
  (tests, alternate stores).
- SQLite via modernc.org/sqlite (already a dependency, pure Go —
  `CGO_ENABLED=0` unaffected), WAL + busy timeout like the denial DB.
- Schema:

```sql
CREATE TABLE IF NOT EXISTS secrets (
    name       TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    created_at TEXT NOT NULL,   -- RFC 3339, UTC
    updated_at TEXT NOT NULL
);
```

- Permissions: the directory is created 0700 and the database file 0600
  — it holds plaintext secret values.
- Interface (new package `internal/secretstore`):

```go
type Entry struct {
	Name      string
	CreatedAt string
	UpdatedAt string
}

type Store interface {
	Get(name string) (string, error) // ErrNotFound if missing
	Set(name, value string, overwrite bool) error
	Delete(name string) error
	List() ([]Entry, error) // names + timestamps, never values
}
```

## Subcommands

```
harnessh secret add NAME [--db PATH] [--force] [--value-stdin]
                         [--config PATH] [--host H]... [--header H] [--prefix P]
harnessh secret rm NAME [--db PATH] [--config PATH]
harnessh secret ls [--db PATH]        # optional, see below
```

- `secret add NAME` prompts for the value with echo disabled (hidden
  input; never stored in shell history). `--value-stdin` reads the value
  from stdin for scripting (pipes, CI). There is deliberately no
  `--value` flag: secrets on the command line leak into shell history and
  the process list.
  - Without `--force`, adding an existing name fails; `--force` overwrites
    (bumps `updated_at`).
  - With `--host`/`--header`/`--prefix` (and `--config`), it also adds or
    updates the `secrets.<NAME>` entry in the config file, writing
    `from: "harnessh://NAME"` — the same RawMessage-preserving rewrite as
    `AddAllowedHosts`. Without those flags it only stores the value.
- `secret rm NAME` deletes it from the store; fails if missing. With
  `--config`, it also removes config entries whose `from` is
  `harnessh://NAME` and prints what it removed; without it, it prints a
  warning listing config entries that still reference the deleted secret
  (the proxy will report them missing).
- `secret ls` (optional, cheap): names + created/updated timestamps.
  Never prints values.

## Resolution changes

`config.ResolveSecrets` parses each `from` as a reference and resolves
through the scheme's store:

- `env://VAR` (and bare `VAR`): `os.Getenv`, as today.
- `harnessh://NAME`: `store.Get(NAME)`; a missing entry lands in the
  existing `missing` list (same UX as an unset env var today: the proxy
  logs it and skips the secret).
- Unknown scheme: a hard error at config load (fail fast, not a silent
  skip).

The resolved values still flow exactly as today: `harnessh run` writes
them to a 0600 secrets file bind-mounted into the proxy container only,
and the config watcher re-resolves on change (a running proxy picks up a
new or updated secret within ~1s). Only the host-side `harnessh run`
process and the `secret` subcommands touch the store; the proxy keeps
reading the resolved secrets file and does not change. The store lives on
the host and is never mounted into the workspace.

## Security

- The database holds plaintext values: 0700 directory, 0600 file, and a
  first-use warning ("secrets are stored unencrypted in
  ~/.config/harnessh/secrets.db, readable only by your user — the same
  trust level as ~/.netrc or ~/.aws/credentials"). Encryption at rest (OS
  keyring, passphrase) is a possible follow-up, not in scope.
- Values never enter the workspace's environment, filesystem, or
  snapshots; they reach only the proxy container's 0600 secrets file.
- `secret add` never echoes the value; `secret ls` never prints values.
- If the user mounts a directory containing the store into the sandbox
  (e.g. mounting `$HOME`), the agent could read it — the same caveat as
  any host credential file. The default store location is outside the
  usual workspace mounts.

## Implementation sketch

- `internal/secretstore/secretstore.go` — `Store` interface, `Entry`,
  `ErrNotFound`, and the sqlite implementation (WAL + busy timeout,
  0700 directory, 0600 file, schema).
- `internal/config/config.go` — `ParseSecretRef(from string) (scheme,
  name string, err error)`; `ResolveSecrets` gains a store lookup by
  scheme; `AddSecret(path, name, Secret)` and `RemoveSecretRefs(path,
  scheme, name)` as RawMessage-preserving rewrites like
  `AddAllowedHosts`.
- `internal/cli/secret.go` — the `secret` command with `add` / `rm` /
  `ls` subcommands; hidden prompt via `golang.org/x/term`
  (`term.ReadPassword`, small pure-Go addition).
- `internal/cli/run.go` — open the store at the default path (or
  `--secret-db`) and pass it into resolution.
- `main.go` — register `secret`, add the usage lines.
- `examples/config.harnessh` — `from` becomes `env://MISTRAL_API_KEY`
  (or a `harnessh://` example).
- `docs/config.md` + `docs/architecture.md` — the reference schemes, the
  store location and permissions, and the nono.sh credential-injection
  docs link (https://nono.sh/docs/cli/features/credential-injection) in
  the secret-injection section.
- Tests: `internal/secretstore` unit tests (CRUD, `ErrNotFound`,
  overwrite semantics, 0600/0700 permissions); `internal/config` tests
  for `ParseSecretRef` (env / harnessh / bare / unknown / malformed) and
  `ResolveSecrets` with a fake store; a cli-level test for add/rm via
  `--value-stdin`; functional test coverage as below.

## Verification

1. `secret add foo --value-stdin` stores the value; `secret ls` shows the
   name (never the value); `secret rm foo` deletes it; a second
   `secret rm foo` fails.
2. File permissions: directory 0700, database 0600.
3. A config with `from: "harnessh://foo"` resolves through the store; a
   missing store entry lands in the `missing` list (same UX as an unset
   env var); `from: "env://FOO"` and bare `FOO` behave identically; an
   unknown scheme fails at config load.
4. `secret add NAME --host api.mistral.ai --header Authorization
   --prefix "Bearer "` writes the config entry with
   `from: "harnessh://NAME"`; `secret rm NAME --config ...` removes it.
5. Functional: add a secret, run a sandbox, the dummy endpoint receives
   the injected header (same assertion as the existing secret test);
   after `secret rm`, the proxy logs the secret as missing and does not
   inject it.
6. `CGO_ENABLED=0 go build` unchanged; `go vet` clean; the full
   functional test passes.

## Open questions

- `secret ls`: include (proposed) or add/rm only?
- Should `secret rm` auto-remove config references (with `--config`) or
  only warn? Proposed: remove with `--config`, warn without.
- Encryption at rest (OS keyring / passphrase)? Out of scope; plaintext
  0600 like `~/.netrc`, with a first-use warning.
- Should the store path respect `XDG_CONFIG_HOME` (proposed) or be
  literally `$HOME/.config/harnessh`?
- A `dotenv://` scheme would subsume plans/0006-env-file.md (marked not
  implemented) — worth adding later as another pluggable store?
