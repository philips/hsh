# hsh run --pull: podman's pull policy, defaulting to latest

> **Status: implemented.** Changes `resolveImage` and its call sites in
> internal/cli/run.go; touches functional step 15 and the shortnames
> section of docs/config.md.

## Goal

`hsh run` uses a locally cached image whenever one exists and only
pulls when the local store is empty. The harness images
(`ghcr.io/philips/hsh-pi`, ...) are republished on pushes to main and
weekly (plans/0010-container-images.md), so a user who ran `hsh run pi`
once keeps using the stale local image forever. `hsh run` must default
to getting the latest image for the requested tag, while keeping
offline runs and never-pushed local dev images working.

## Design: adopt podman's `--pull` policy vocabulary

New flag on `hsh run`: `--pull <always|missing|newer|never>`
(`StringVar`, validated against the four values), **default `newer`**.
The values keep podman's semantics (podman-run man page):

| policy | behavior |
|---|---|
| `always` | pull every run; error if the pull fails |
| `missing` | pull only when the image is not local (today's behavior); error if the pull fails |
| `newer` | pull (registry digest compare, downloads only new layers); on pull failure fall back to the local image with a one-line warning, error only if no local image exists |
| `never` | use the local image; error if it is not local |

`newer` is the default: that is the "always the latest" requirement,
with podman's built-in tolerance for offline use ("pull errors are
suppressed if a local image was found" — hsh additionally prints one
warning line, which podman does not).

## Why not pass `--pull` through to the podman create commands

The session containers are created with `podman create` + `podman
start`, and `podman create` accepts `--pull`. A pure pass-through
cannot satisfy the requirement because of the `org.ifup.hsh` label
read: with no config file, hsh inspects the *local* image for its
label before creating anything, and the label is the config (mounts,
allowlist, secrets, agent) that the create arguments are built from.
A create-time pull fires after that read, so

- a never-pulled image could not be used for a label-based run at all
  (`image inspect` fails: "does not exist locally"), and
- a refreshed image would run with the *stale* image's label config.

The label flow is the primary one (`hsh run pi` with no config file),
so the pull must happen before the label read.

A second gap: config-file images never pass through `resolveImage`
today (only the positional / `--fresh` image does; run.go resolves
`positional` and then `cfg.Image = image`), so a pass-through on
create would silently give the two flows different pull behavior.

## Implementation

One place, `resolveImage` (internal/cli/run.go), which grows a policy
argument; the four policies map onto it directly:

1. Reference resolution is unchanged (transport verbatim,
   registry-qualified verbatim, shortname via shortnames.conf,
   `docker://` stripped — the stripped form is what the session config
   copy stores and what functional step 15 asserts).
2. `newer` / `always`: `podman pull -q <ref>` (prints the image id).
   `newer` on failure → if `podman image exists <ref>`, warn
   (`hsh: warning: pull of <ref> failed (<reason>); using the local
   image`) and use it; otherwise the existing pull-failed error.
   `always` on failure → the existing pull-failed error.
3. `missing`: `podman image exists <ref>` → return it; else pull,
   error on failure (today's code path).
4. `never`: `podman image exists <ref>` → return it; else error
   (`image <ref> is not in the local storage; build or pull it first,
   or use --pull=missing`).
5. Transport references (`docker-archive:`, `oci:`, ...): the source
   *is* the local file/directory, so `missing`/`newer`/`always` all
   mean "copy it" (a `podman pull -q` of the transport), and `never`
   is an error explaining that transport references cannot come from
   the local storage. The existing normalization to a local
   repository name (image id from the pull +
   `podman image ls --filter id=`, because transport refs are not
   locally inspectable) applies after the copy in all cases.

Call sites — `resolveImage` runs for every image a fresh session uses:

- the positional / `--fresh` image (existing call site, now with the
  policy; still before the label read, so label config comes from the
  latest image), and
- `cfg.Image` from the config file when no positional image is given
  (new call site, replacing the implicit reliance on podman's default
  `missing` policy at create time); a positional image keeps winning
  over the config's image field, as today.

No `--pull` is added to the `podman create` arguments: by create time
the image is guaranteed local (resolveImage pulled it or confirmed it),
so podman's default `missing` policy at create is exactly right, and
each session pays at most one registry round-trip instead of one per
create (proxy + workspace).

Unchanged:

- `--resume` re-enters a live pod whose containers already run the
  image; no pull happens on resume.
- `--fresh` re-resolves the stored image reference, so it pulls like a
  fresh run.
- the session config copy stores the resolved reference, so resume
  stays independent of later config edits.

## Podman behavior to verify (in a podman environment)

- `podman pull -q IMG` with an up-to-date local image returns quickly
  without re-downloading and leaves the digest unchanged
- `podman pull -q IMG` after the tag's digest changed moves the local
  name to the new digest
- `podman pull -q` prints the image id on stdout
- pulling a locally built, never-pushed reference (e.g.
  `localhost/hsh-example:latest`) fails — the `newer` fallback path
- `podman pull -q docker-archive:img.tar` re-copies when the archive's
  image changed (new digest) and is a no-op when unchanged
- `podman create` accepts `--pull` (recorded for the record only —
  the design does not use it)

## Tests (test/functional.sh)

1. Default (`newer`) re-pull picks up new content, registry-free:
   build a marked image, `podman save` it to `img.tar`, run a session
   from `docker-archive:img.tar` and assert the marker; rebuild the
   same name with the marker changed, re-save over the same tar, run a
   second session from it, assert the new marker.
2. `newer` falls back to the local copy: run a built-locally,
   never-pushed image (`localhost/hsh-example:latest`) and assert the
   "using the local image" warning on stderr and a working session.
3. `--pull=never` errors clearly when the image is not local, and
   `--pull=always` errors when the pull of a never-pushed reference
   fails (no fallback).
4. Step 15's hermetic shortname run switches to
   `hsh run --pull=never pi` against the locally tagged
   `ghcr.io/philips/hsh-pi` (with network access, a default run would
   pull the real public image over the local tag); the session config
   still stores the resolved reference.
5. Config-file image honors the policy: a config whose `image` points
   at `docker-archive:img.tar`, run twice around a re-save with the
   changed marker, second session sees the new marker (covers the new
   resolveImage call site).
6. Unknown shortname with no local copy still fails naming what it
   resolved to and pointing at shortnames.conf (existing step 15
   check, now via the `newer` pull-failure path).

## Docs

docs/config.md, shortnames section: replace "a resolved reference is
pulled into the local store if not already there" with the `--pull`
policy table (default `newer`) and the local-copy fallback warning.
