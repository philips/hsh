# Shortnames for hsh run: `hsh run pi` with an overridable default registry

> **Status: implemented.**

## Goal

`hsh run pi` retrieves `ghcr.io/philips/hsh-pi` by default. Shortnames
are assumed unless the reference is a transport (`docker://`, `oci://`,
anything with `://`) or registry-qualified (first path segment has a
`.`, a port, or is `localhost`). Aliases and the default prefix live in
a user config file, `key=value` like podman's registries.conf:

```
shortname-prefix=docker://quay.io/example-corp-agents/
pi=docker://ghcr.io/philips/hsh-pi
```

Someone setting `shortname-prefix=docker://quay.io/example-corp-agents/`
gets `hsh run pi` retrieving `quay.io/example-corp-agents/pi`.

## Resolution (internal/imageRef + cli.resolveImage)

1. transport (`contains "://"`) → verbatim; `docker://` refs are
   stripped to their registry name so the local store can satisfy
   repeat runs without a registry round-trip
2. registry-qualified (first path segment has `.`/`:` or is
   `localhost`; tags do not count — `pi:latest` is a shortname) →
   verbatim
3. otherwise → alias lookup, else `shortname-prefix + name`; the
   result re-enters the same flow (aliases may point at transports)

The resolved reference is pulled into the local store when missing
(hsh needs the image local to read its `org.ifup.hsh` label; transport
references that cannot be looked up locally are normalized to their
local repository name via `podman image ls --filter id=`), and the
session's config copy stores the resolved reference so `--resume` is
independent of later config edits.

## Config

`$XDG_CONFIG_HOME/hsh/shortnames.conf` (missing file = builtin
defaults): `shortname-prefix=<ref>` plus `name=<ref>` alias lines,
`#` comments, one per line; malformed lines are an error. The builtin
prefix is `docker://ghcr.io/philips/hsh-`.

## Verified podman behavior

- `podman run`/`podman create` accept transport references directly
- `podman pull -q` prints the image ID; `podman image ls --filter id=`
  yields the normalized local name (transport refs are never
  inspectable, hence the pull-then-normalize step)
- podman's own shortname machinery cannot prompt in scripted sessions
  ("short-name resolution enforced but cannot prompt without a TTY"),
  which is why hsh resolves shortnames itself

## Verification (functional step 15)

1. `hsh run pi` starts ghcr.io/philips/hsh-pi (local tag, offline) and
   the session copy stores the resolved reference
2. an unknown shortname fails naming what it resolved to and pointing
   at shortnames.conf
3. `docker-archive:` references (registry-qualified via the colon in
   the first segment) are pulled and run
4. unit tests: transport/qualified/shortname classification, tag
   handling, builtin defaults, alias precedence, prefix override,
   malformed lines
