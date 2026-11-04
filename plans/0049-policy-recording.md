# Policy recording, verification, and suggestions

> **Status:** draft. Follows plan 0045.

## Goal

Give the 0045 policy design a way to be exercised end to end:

1. **Record:** run a command in a container, capture every host it connected
   to, and turn that into a draft policy.
2. **Verify:** rerun the command with only that policy allowed and confirm it
   still succeeds.
3. **Suggest:** when a session's review queue has a host that a stored policy
   allows, the monitor offers to apply that policy.

The Fedora walk-through:

```
hsh policy record fedora --image registry.fedoraproject.org/fedora:42 -- dnf -y install neovim > fedora.json
$EDITOR fedora.json
hsh policy add fedora fedora.json
hsh policy verify fedora
```

Later, in a session on a Fedora image, a review request for
`mirrors.fedoraproject.org` shows `in policy: fedora`, and one key applies it.

## Depends on

- 0045: the policy file format, `report` mode, the connection log in
  `proxy.db`, session rules in `monitor.db`, `hsh policy add` / `apply`.
- 0047 (no-workspace runs): recording should not mount the current
  directory. Until 0047 lands, the recorder uses an empty temporary directory
  as the workspace.

## Recording

```
hsh policy record NAME --image IMAGE [--runs N] [--runtime gvisor|container] -- COMMAND...
```

- Prints a draft policy to stdout. It never writes into
  `~/.config/hsh/policy/network/`: the user reads it, edits it, and adds it
  with `hsh policy add` (the same review step as 0045's report-to-policy pipe).
- Each run is a fresh session created by the recorder, not the image's config:
  - image `IMAGE`, mode `report`, reporting `record` (nothing is queued or held)
  - no secrets, no OAuth providers, no mounts, so nothing is intercepted and no
    credential is exposed to the recorded code
  - the image's `org.ifup.hsh` label is ignored even if the image is trusted
- `COMMAND` runs non-interactively, as a piped `hsh run` does today (stdin to
  the shell, exit code propagated). A run counts only if `COMMAND` exits 0; a
  failed run is an error and prints its output.
- After each run the recorder reads the session's connection log, then
  removes the session.
- `--runs N` (default 1) records N separate sessions and takes the union of
  their hosts. Hosts seen in fewer than N runs are reported on stderr as
  unstable (`unstable (1/3 runs): fedora.mirror.example.edu`).
- `report` mode allows every host and port for the recorded command. Print one
  line on stderr saying so before the first run.

### Draft output

```json
{
  "version": 1,
  "description": "recorded: dnf -y install neovim",
  "recorded": {
    "image": "registry.fedoraproject.org/fedora:42@sha256:...",
    "command": ["dnf", "-y", "install", "neovim"],
    "runs": 3,
    "at": "2025-06-01T12:00:00Z",
    "hsh": "v0.1.0"
  },
  "rules": [
    {"hosts": ["mirrors.fedoraproject.org", "..."], "access": "allow"}
  ]
}
```

- One `access: allow` rule, hosts sorted and deduplicated. Ports and schemes are
  not part of matching (0045), so they are not kept.
- Only connection attempts count; DNS lookups are not in the connection log.
- IP-literal destinations are kept and listed on stderr: they usually mean a
  hard-coded address that the user should check.
- Wildcards are never generated. When several hosts share a parent domain the
  recorder prints a hint on stderr (`4 hosts under fedoraproject.org; consider
  *.fedoraproject.org`); widening is the user's edit.
- `recorded` is a new optional field of the 0045 format. Add it to 0045 before
  that format ships, so the format stays version 1. Only `verify` reads it.

## Verification

```
hsh policy verify NAME [--image IMAGE] [-- COMMAND...]
```

- Without `--image` and `COMMAND`, replays the policy's `recorded` block. A
  policy without one needs both.
- Runs a fresh session in `allowlist` mode with exactly the policy's rules and
  nothing else, reporting `record` and no holds.
- Passes if `COMMAND` exits 0. On failure it lists the hosts that were denied
  (from the session's `denied` table) as missing from the policy, and exits
  non-zero.
- The image is the recorded digest when replaying, so a failure means the
  policy or the network changed, not the image. `--image` with a tag tests the
  policy against a newer image.

## Suggestions in the monitor

- For each pending row in the review list, the host side looks up the stored
  policies (`~/.config/hsh/policy/network/*.json`) that are not yet applied to
  the session and have an `access: allow` rule matching the host. The row shows
  `in policy: fedora` (up to three names, then `+N`).
- `p` on a row with a suggestion opens a confirmation listing every host the
  policy would allow. Enter applies it: its rules are copied into the
  session's `monitor.db` rules with source `policy:fedora`, and the pending
  rows they cover are approved. With several matching policies, `p` asks
  which one first.
- Only `allow` rules produce suggestions; a policy whose `deny` rule matches the
  host does not.
- `hsh proxy denied` gets a `POLICIES` column with the same names, and
  `hsh policy apply --session ID NAME` does what `p` does.
- Suggestions are computed on the host from the user's own policy files; the
  proxy and the sandbox are not involved.

## The mirror problem

Fedora is the hard case, and should be the first one exercised for that reason:

- dnf asks `mirrors.fedoraproject.org` for a metalink and then downloads from
  mirrors chosen per run and per location, on hosts with unrelated domains.
- A recorded policy holds the mirrors that one recording saw. `--runs N` widens
  it, but a user elsewhere gets different mirrors.
- The suggestion still works for the first request (`mirrors.fedoraproject.org`
  is stable), but after the policy is applied, a mirror that was never recorded
  shows up as a new review request with no suggestion.

Options, to decide while exercising it:

1. Accept it: the policy covers the stable hosts, unknown mirrors are reviewed
   one at a time.
2. Recipe-level pinning: record against an image or dnf configuration that
   uses a fixed `baseurl`, and document that the policy only works with it.
3. A policy rule that allows the hosts a metalink response names. That needs the
   proxy to read the metalink body, which is interception
   ([0048-intercept-proxy.md](0048-intercept-proxy.md)), so not here.

Golang (`go mod download`, `proxy.golang.org`) and npm are deterministic and
make good second and third cases.

## Phases

1. **Draft from a session:** `hsh proxy report --session ID --format policy`
   (from 0045) with the sorting, deduplication, IP and wildcard hints above. Unit
   tests feed a fixture `proxy.db` and compare the draft.
2. **`hsh policy record`:** session creation with the recorder's config, the
   piped command run, `--runs`, unstable hosts, session cleanup (also on
   failure and Ctrl+C).
3. **`hsh policy verify`:** the allowlist-only session and the missing-hosts
   report.
4. **Suggestions:** the policy lookup shared by the monitor and
   `hsh proxy denied`, the `p` key and its confirmation, and `hsh policy apply`.

## Tests

- Unit: draft generation (fixture connection logs: duplicate hosts, mixed
  ports, IP literals, several runs with unstable hosts); suggestion lookup
  (exact, wildcard, applied policies excluded, deny-only policies excluded);
  applying a policy copies its rules with the right `source` and approves the
  covered pending rows.
- Functional, needs real network, opt-in like the egress tests:
  record `go mod download` for a small module, verify it, then verify it with
  one host removed from the policy and assert it fails and names that host.
- Manual: the Fedora walk-through above, recording the mirror behaviour in
  this plan.

## Open questions

- Should recording also support an agent run (`--tasks`) instead of a command?
  The agent's own API hosts would be recorded too and would need excluding.
  Proposed: command only for now.
- Where do recipes for the Fedora, Golang and npm cases live: as test fixtures
  in `test/`, or only as the `recorded` blocks of policies the user keeps?
- Should `verify` also run in CI for a set of fixture policies, given that it
  depends on third-party hosts staying the same?
