# Shortnames for hsh run: `hsh run pi` with an overridable default registry

> **Status: not implemented.**

## Goal

`hsh run pi` starts the harness image by resolving the shortname to
`ghcr.io/philips/hsh-pi` by default. Users can point shortnames at
their own images via `~/.config/hsh/`, and can always bypass shortnames
entirely with an explicit transport reference (`oci://`, `docker://`,
`docker-archive:`, `dir:`, ... per
https://github.com/containers/image/blob/main/docs/containers-transports.5.md)
or a fully-qualified registry reference.

```
$ hsh run pi                        # ghcr.io/philips/hsh-pi (default)
$ hsh run ghcr.io/philips/hsh-pi    # fully-qualified: untouched
$ hsh run docker://registry.example.com/team/pi:dev   # transport: untouched
```

## Verified facts (podman 5.8.9 on this host)

1. `podman run` accepts transport references directly and pulls as
   needed (`podman run --rm docker://docker.io/library/busybox:latest`
   works) — transports can be passed through to every podman
   invocation hsh makes.
2. Podman's own shortname machinery does not help here: `podman run pi`
   fails with "short-name resolution enforced but cannot prompt without
   a TTY", and hsh sessions are frequently scripted. hsh must resolve
   shortnames itself, before podman sees the reference.
3. A transport reference is not in the local store until pulled, and
   hsh needs the image present before session creation to read its
   `org.ifup.hsh` label (`podman image inspect` does not accept
   transports). So transport references are pulled explicitly first
   (`podman pull <ref>`), then the reference is used as-is for the
   container create/run arguments.

## Design

### Argument classification (in this order)

1. **Transport reference** — matches a known transport prefix from
   containers-transports.5: `docker://`, `oci:`, `oci-archive:`,
   `docker-archive:`, `dir:`, `containers-storage:`, `docker-daemon:`,
   `sif:`. Pulled with `podman pull` if not already present, then used
   verbatim for the session's containers.
2. **Fully-qualified registry reference** — first path segment contains
   a `.` or `:` or is `localhost` (e.g. `ghcr.io/x/y`, `localhost/foo`).
   Used verbatim.
3. **Bare shortname** — no transport, no registry-looking first segment
   (e.g. `pi`, `mydev`). Resolved by the shortname map (below), and the
   result may itself be any of the three forms (a shortname may point
   at a transport reference).

The `--resume` positional is a session id, never an image; resolution
only applies to new sessions.

### The shortname map

`$XDG_CONFIG_HOME/hsh/shortnames.json` (default
`~/.config/hsh/shortnames.json` — the same directory as the secret
store; missing file is not an error):

```json
{
  "prefix": "ghcr.io/philips/hsh-",
  "shortnames": {
    "pi": "ghcr.io/philips/hsh-pi",
    "mydev": "docker://registry.example.com/team/pi:dev"
  }
}
```

- an explicit `shortnames` entry always wins
- otherwise a bare name `N` resolves to `prefix + N`
- the builtin default prefix is `ghcr.io/philips/hsh-`, so `hsh run pi`
  works with no configuration, and the four harness images
  (`pi`, `vibe`, `claude-code`, `codex`) are usable as shortnames out of
  the box
- `prefix` overrides the builtin default; a user shortname may be a
  fully-qualified or a transport reference

### Errors

An unresolvable or unpullable shortname must name what was tried:
`hsh: image "fedora" resolved to ghcr.io/philips/hsh-fedora: pull failed
(...); use a fully-qualified reference, a transport (oci://, docker://,
...), or add it to ~/.config/hsh/shortnames.json`.

### What stores the resolution

The session's config copy stores the **resolved** reference, so
`--resume` is independent of later shortname-config changes, and
`hsh ls`/session state shows the real image.

## Implementation sketch

- `internal/config/config.go` (or a new `internal/imageRef` package):
  `Classify(ref string) (kind, normalized)` — transport prefixes per
  containers-transports.5, the fully-qualified heuristic, shortname;
  `LoadShortnames(path)` with builtin defaults; `Resolve(ref string)
  (resolved string, err error)`.
- `internal/cli/run.go`: resolve the positional image after the
  `--resume` split, before the label lookup; for transport references
  run `podman pull` (skipping when the local store already has it via
  `podman image exists`), then proceed unchanged.
- `main.go`/help: `hsh run [FLAGS] [IMAGE]` stays; docs explain
  shortnames.
- Docs: `docs/images.md` — examples become `hsh run pi`; a shortnames
  section in `docs/config.md` (the file format, precedence, transports);
  the session doc notes the config copy stores the resolved image.
- Unit tests: classification (transport / fully-qualified / shortname,
  including `--resume` positional exemption), builtin defaults, map
  override, error messages.
- Functional: tag the fixture image as `ghcr.io/philips/hsh-pi` locally
  and assert `hsh run pi` resolves and starts (offline — no registry);
  save the fixture to an archive and assert `hsh run
  docker-archive:...` starts a session from it.

## Verification

1. `hsh run pi` with no user config starts ghcr.io/philips/hsh-pi
   (functional: local tag, no network).
2. A `shortnames.json` entry overrides a builtin shortname and can point
   at a transport reference; `prefix` override applies to unknown names.
3. Transport and fully-qualified references never go through the map
   (assert the session config copy holds the verbatim reference).
4. `--resume` positionals are never resolved as images.
5. An unpullable shortname produces the explanatory error.
6. Full `test/functional.sh`, `go vet`, static build unchanged.

## Decisions (resolved)

- **hsh owns shortname resolution** rather than leaning on podman's
  registries.conf shortnames: podman's enforcement cannot prompt in
  scripted sessions (fact 2), and the default namespace
  (`ghcr.io/philips/hsh-`) is hsh's product decision, not a
  registry-policy one.
- **Transports are pass-through with an explicit pull**: podman accepts
  them everywhere (fact 1), but hsh needs the image local before
  reading its label (fact 3).
- **One config file for the map and the prefix**, JSON, in
  `$XDG_CONFIG_HOME/hsh/` next to the secret store; missing file means
  builtin defaults.
- **Session copies store resolved references**, keeping resume stable
  across config edits.

## Risks

- The fully-qualified heuristic (first path segment with `.`/`:`) can
  misfire on single-segment registries — none are in practical use;
  the escape hatch is an explicit transport or a shortnames.json entry.
- New transports in containers/image (e.g. a future `sif:` variant)
  need the prefix list extended; unknown-but-transport-shaped
  references should still be passed through (classification treats
  `^[a-z0-9-]+:` conservatively and falls back to podman's own error).
- Shortname resolution must never apply to the `--resume` positional.
