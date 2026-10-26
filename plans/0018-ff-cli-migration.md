# Migrate to peterbourgon/ff (v4 beta), remove --image, one positional contract

> **Status: implemented.**

## Goal

Replace the stdlib `flag` package and `main.go`'s hand-rolled dispatch with
`github.com/peterbourgon/ff/v4` (beta) `ff.Command` trees, adopt a single
positional-argument contract for the whole CLI, and remove `hsh run --image`
so that `hsh run` has exactly one positional whose meaning is unambiguous —
the workspace image normally, the session id with `--resume`
(plans/0014-workspaces.md).

## Verified ff v4 facts (drives the design)

Checked against `v4.0.0-beta.1`:

- `ff.Command{Name, Usage, ShortHelp, LongHelp, Flags, Subcommands, Exec}`
  composes a command tree; `ParseAndRun(ctx, args)` parses, selects the
  terminal command by matching the first leftover token against each level's
  subcommand names (case-insensitive), and runs `Exec(ctx, args)` with the
  leftovers.
- `ff.FlagSet` is ff's own flag set (`ff.NewFlagSet`), with
  `String/Bool/Int/StringVar/...` registration and `ffval.List` for
  repeatable values (replaces `multiFlag`).
- **Parsing stops at the first non-flag argument**, exactly like stdlib
  `flag`. Flags after a positional are NOT parsed; there is no interspersed
  option. `hsh secret add NAME --force` will not work under ff.
- `ffhelp` generates help text (`ffhelp.Command(cmd)`) from the tree;
  `ffhelp.WrapFlag` provides a `-h/--help` flag.

## Decision: flags before positionals

ff does not fix flags-after-positional; nothing in the stdlib-adjacent
ecosystem does without switching to pflag. Instead of another bespoke
extraction hack, the CLI adopts **one contract, uniformly**:

> `hsh COMMAND [FLAGS] [POSITIONAL...]` — flags precede positionals.

Consequences:

- `hsh run --config x IMAGE` works; `hsh run IMAGE --config x` becomes an
  error with a message that explains the ordering ("flags must come before
  positional arguments").
- `hsh secret add --force NAME` works; `hsh secret add NAME --force`
  becomes the same class of error.
- The per-command patches (`extractPositional` in secret.go, the
  pre-parse image extraction in run.go) are deleted; leftover-args
  validation in each Exec enforces the contract with a clear message.
- Positionals still work in the natural position (nothing is lost for
  `hsh rm POD...`, `hsh run IMAGE`, `hsh secret add NAME`).

## Remove --image

`hsh run` currently accepts the image twice: as `--image` and as a
positional alias. plans/0014-workspaces.md wants the positional to mean a
session id under `--resume`, which collides with the image alias. Fix:

- `--image` is removed everywhere (flag, usage, docs, functional test).
- The positional is the image: `hsh run [FLAGS] IMAGE`.
- With `--resume`, the positional is the session id; until 0014 lands,
  `hsh run --resume <id>` fails with "session selection arrives with
  plans/0014-workspaces.md" (the slot is reserved and unambiguous).
- `--resume` with no positional keeps today's behavior (reattach the
  workspace's sandbox / create if missing — or 0014's error semantics
  once implemented).

## Design

### Command tree

```
hsh
├── run          [FLAGS] [IMAGE]
├── secret
│   ├── add      [FLAGS] NAME
│   ├── rm       [FLAGS] NAME
│   └── ls       [FLAGS]
├── proxy-denied [FLAGS]
├── proxy-auto-allow [FLAGS]
├── proxy-watch  [FLAGS]
├── ls           [FLAGS]
└── rm           [FLAGS] SANDBOX...
```

- Bare `hsh` (no args) keeps printing help. With ff, root gets no
  `Exec`, so an unmatched first token selects root; give root an `Exec`
  that prints help and returns a "unknown command %q" error (exit 2).
- `hsh help` / `-h` / `--help` come from a help command/flag built on
  `ffhelp`; the hand-written `usage` const in main.go is retired — the
  current usage prose moves into `Usage`/`ShortHelp`/`LongHelp` fields.
- The container roles (`hsh-proxy`, `hsh-workspace`,
  `hsh-workspace-inner`) keep their argv[0] dispatch in main.go,
  **before** the tree. The legacy subcommand names (`proxy`,
  `workspace`, `workspace-inner`) are pre-mapped in main.go to direct
  cli calls — they stay out of the ff tree so generated help shows only
  user-facing commands, while pre-migration `--resume` pods (whose
  containers exec `/hsh workspace`) keep working.

### Per-command changes

- `run`: flags `--config --tasks --network --workspace --denied-db
  --secret-db --verbose --resume --fresh`; `--image` and its pre-parse
  extraction removed; positional IMAGE validated once; `--resume` +
  positional rejected (reserved for 0014). The `hsh run` label-fallback
  error text updates ("no config file and no image given: hsh run IMAGE").
- `secret add`: `--host` becomes `ffval.List[string]`; NAME is the single
  positional; `extractPositional` and `multiFlag` are deleted.
- `secret rm/ls`, `proxy-denied`, `proxy-auto-allow`, `proxy-watch`,
  `ls`, `rm`: mechanical flag-set swaps; `rm` keeps multiple positionals.
- `proxy`, `workspace`, `workspace-inner` (internal role entrypoints):
  keep their existing `flag.FlagSet` parsing — they are invoked by
  container entrypoints with machine-generated, flags-first argv, never
  by users, so they gain nothing from ff and stay on stdlib.

### main.go

```
switch basename(argv[0]) {
hsh-proxy | hsh-workspace | hsh-workspace-inner: role dispatch (unchanged)
default: pre-map legacy names, then cli.Root().ParseAndRun(ctx, args)
}
```

Errors: parse/usage → "hsh: %v" + exit 2; runtime → exit 1 (ff returns
errors; it does not exit).

## Migration steps

1. `go.mod`: pin `github.com/peterbourgon/ff/v4 v4.0.0-beta.1` (beta;
   keep the pin exact, not `@latest`).
2. `internal/cli/root.go` (new): `Root() *ff.Command` composing the
   tree; per-file builders (`runCommand()`, `secretAddCommand()`, ...)
   own their `ff.FlagSet` and `Exec`; existing `Run(args)`-style
   entry points fold into the `Exec`s.
3. `main.go`: argv[0] role dispatch + legacy pre-map + `ParseAndRun`;
   remove the `usage` const (prose moves to command fields); help via
   `ffhelp`.
4. `internal/cli/secret.go`: delete `extractPositional`/`multiFlag`;
   `ffval.List` for `--host`.
5. `internal/cli/run.go`: delete `--image` and the pre-parse positional
   extraction; add the flag-ordering error and the reserved
   `--resume <positional>` rejection.
6. Tests: `internal/cli` tests drive the command tree
   (`Root().ParseAndRun` in a test context); add parsing-contract cases
   (flags-before-positional accepted, positional-then-flag rejected with
   the ordering message, `run IMAGE` positional, `--resume IMAGE`
   rejected, `secret add --force NAME` accepted).
7. `test/functional.sh`: `--image hsh-label-test:latest` and
   `--image localhost/hsh-vibe:latest` become positionals; the
   "without a config file or --image" wording updates.
8. Docs: `docs/config.md` and `docs/images.md` switch
   `hsh run --image IMAGE` to `hsh run IMAGE`; usage/help screenshots of
   command lists re-derived from ffhelp output. README.md still shows
   `--image` — owner edits (assistant is not allowed to).

## Verification

1. Unit: parsing contract (orderings, ambiguities, `--` terminator),
   unknown command → exit 2 with help, bare `hsh` → help, `ffval.List`
   `--host` accumulation, `--resume` positional rejection.
2. Unit: existing cli tests (secret add/rm/ls, config rewrites) pass via
   the tree.
3. Functional: full `test/functional.sh` pass, including a pre-migration
   `--resume` pod re-attached with the new binary (legacy
   `/hsh workspace` entrypoint → argv[0] default → pre-map alias).
4. `go vet`, `gofmt`, `CGO_ENABLED=0 go build` unchanged; static binary
   size delta acceptable (ff adds no non-stdlib dependencies).
5. Manual: `hsh help` renders the tree; `hsh run IMAGE` starts a
   sandbox; `hsh run IMAGE --config x` explains the flag ordering.

## Decisions (resolved)

- **ff v4 beta, ff.Command** per owner request; version pinned. The
  beta risk is accepted for the declarative tree and ffhelp; revisit at
  v4 final.
- **Flags-before-positionals contract** instead of interspersed
  parsing: consistent, expressible in ff, and enforced with a clear
  error message everywhere.
- **--image removed**: one positional, one meaning (image; session id
  under `--resume` per 0014).
- **Container role entrypoints stay on stdlib flag** — machine-invoked,
  flags-only argv, no benefit from ff.
- **Legacy subcommand aliases live in main.go's pre-map**, outside the
  user-facing tree and help.
- **Help is generated by ffhelp** from the command tree; the usage
  const is retired so help cannot drift from the tree.

## Risks

- v4 beta API churn between beta.1 and final (mitigated by the pin).
- Behavior change for existing users: flags-after-positional invocations
  now error. Mitigation: the error names the flag and states the
  ordering; docs show the canonical form.
- ffhelp's format differs from today's hand-aligned usage table; the
  prose is preserved, the alignment is not.
