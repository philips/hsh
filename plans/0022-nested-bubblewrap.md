# Nesting bubblewrap inside hsh workspaces

> **Status: research.** Codex's bwrap sandbox currently fails in hsh
> workspaces with `bwrap: setting up uid map: Operation not
> permitted`. The codex image ships
> `sandbox_mode = "danger-full-access"` as a workaround; this plan is
> the path to allowing the nested sandbox to run for real.

## Why it fails today

A nested bwrap needs three things the hsh inner process does not have:

1. **Podman's default seccomp profile** is applied to the workspace
   container (`run.go` `wsArgs` sets `--security-opt label=disable`
   but nothing about seccomp). The default OCI profile denies
   `unshare`/`clone` with `CLONE_NEWUSER` unless the caller holds
   `CAP_SYS_ADMIN`, so `unshare(CLONE_NEWUSER)` returns `EPERM`.
2. **The inner process drops all capabilities** (keeps only
   `CAP_DAC_OVERRIDE`) and sets `PR_SET_NO_NEW_PRIVS`
   (`workspace.go`), so even if the syscall were allowed it cannot
   create the mapped uid it needs: writing `/proc/self/uid_map`
   requires `CAP_SETUID` in the parent userns or an unprivileged
   mapping restricted to the caller's own single uid.
3. **Rootless single mapping**: the workspace container itself runs
   under rootless podman, and when the host has no `/etc/subuid`
   entry podman falls back to a single-id mapping. A nested userns
   then has no subuid range to map, so bwrap's `--uid`/`--gid`
   remapping fails even after the userns is created.

Note `NO_NEW_PRIVS` itself does not block `unshare`; it only blocks
privilege escalation. The blockers are seccomp + capability drops.

## Research questions

1. **Which layer actually returns EPERM?** Instrument a workspace
   container: run `unshare -Ur true`, `unshare -m true`,
   `cat /proc/self/status | grep -E 'CapEff|Seccomp|NoNewPrivs'`,
   and check `sysctl user.max_user_namespaces` and
   `/proc/sys/kernel/unprivileged_userns_clone`. Determine whether
   the deny comes from podman seccomp, the cap drop, or the host
   sysctl.
2. **Does the host have subuid ranges?** Confirm
   `/etc/subuid`/`/etc/subgid` on the machine running `hsh run`; CI
   installs them in `test.yml` (`passt uidmap` + subuid entries) so
   the functional test can be extended to answer this on a known-good
   host.
3. **What does bwrap need exactly?** bwrap has two modes: unprivileged
   (creates its own userns, needs a subuid-less single-uid map to
   work) and setuid-root (opens `/dev/bwrap`/does the setup before
   dropping). Which mode does the codex-shipped bwrap use, and is
   there a flag (`--unshare-user`, `--die-with-parent`, or the
   setuid helper) that works under a single-id nested userns?
4. **What does codex actually sandbox?** Codex's bwrap profile
   isolates the filesystem (bind mounts, tmpfs /tmp, read-only /) and
   optionally the network. hsh already provides network isolation;
   if only the filesystem part matters, a bwrap invocation without
   `--unshare-user` may be enough.

## Options to evaluate, cheapest first

1. **Document + keep the workaround.** hsh is already a sandbox;
   codex nested sandboxing is defense-in-depth. Cost: zero. Risk:
   none. This is the current state.
2. **`--security-opt seccomp=unconfined` (or a custom profile that
   allows `clone`/`unshare`) on the workspace container**, gated
   behind a config flag (e.g. `workspace.nestedUserns: true` in
   config.hsh or the image label). This removes blocker 1 only; the
   inner cap drop must also keep `CAP_SETUID`/`CAP_SETGID` (or pass
   through unprivileged) for uid_map writes. Evaluate how much
   attack surface the single-purpose seccomp relaxation adds: the
   proxy allowlist still constrains egress.
3. **Setuid bwrap in the image.** If bwrap is installed setuid-root
   it performs the userns setup itself before dropping privileges;
   inside the container uid 0 maps to the invoking host user, so
   nothing new escapes the container. Blocked by blocker 1 as well
   (setuid bwrap still calls unshare) but sidesteps blocker 2/3.
   Fedora ships bwrap non-setuid; the image would need a custom
   package or `chmod u+s` — plus `NO_NEW_PRIVS` on the inner process
   neutralizes setuid binaries, so this only works if the inner exe
   skips `PR_SET_NO_NEW_PRIVS` when the flag is enabled.
4. **Run the agent's sandbox at a different layer.** Instead of
   nesting, give codex an IPC socket out of the container and let
   hsh's proxy mediate — e.g., a `hsh bwrap` helper on the host that
   the container talks to. Largest change; treat as last resort.

## Success criteria

- `unshare -Ur true` succeeds inside a workspace container with the
  flag enabled and fails cleanly without it.
- `bwrap --ro-bind / / --dev /dev --proc /proc true` succeeds in the
  codex image, and codex runs without
  `sandbox_mode = "danger-full-access"`.
- `test/functional.sh` gains a nested-namespace test (CI host has
  subuid entries already) and the codex image builds in
  `images.yml`.
- No change in egress behavior: a sandboxed codex still cannot reach
  anything outside the allowlist.

## Deliverables

1. Findings for the research questions (run them in a workspace and
   on a bare host).
2. A decision: implement option 2/3 or keep option 1 and close this
   plan with a "will not implement" note.
3. If implementing: config flag, `workspace.go`/`run.go` changes,
   functional test, updated docs (docs/images.md "Nested sandboxes"
   section is rewritten to describe the flag).
