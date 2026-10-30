# 0035: Running hsh (and podman) nested inside an hsh session

Status: done (default runtime, real kernel). `HSH_NESTING=1` allows
`hsh run` inside an hsh session; the full nesting chain was verified:
host -> session -> nested podman -> nested session shell (uid 0, working
uid_map). gVisor nesting stays blocked upstream.

## Final design (all patches in the working tree, built as ./hsh-nested)

1. `internal/cli/run.go`, `wsArgs`: when `HSH_NESTING=1`, pass
   `-e HSH_NESTING=1` to the workspace container (podman does not
   inherit the host env; the entrypoint reads the variable inside).
2. `internal/workspace/workspace.go`, `enterSession`: when
   `HSH_NESTING=1`, keep these caps alongside `CAP_DAC_OVERRIDE`:
   `SETUID, SETGID, SETFCAP, SETPCAP, SYS_ADMIN, NET_ADMIN, CHOWN,
   FOWNER, FSETID`, and first trim the capability bounding set to the
   same list (`dropBoundingSetKeep`, PR_CAPBSET_DROP).

No `--userns host` anywhere: the session's default per-container user
namespace works. `PR_SET_NO_NEW_PRIVS` and the default seccomp profile
stay in effect.

3. `internal/workspace/workspace.go`, `enterSession`: when
   `HSH_NESTING=1`, the entrypoint also writes
   `/tmp/podman-nest/storage.conf` (vfs) + exports
   `CONTAINERS_STORAGE_CONF`, and writes
   `/tmp/podman-nest/containers.conf` (`cgroups = "disabled"`,
   `cgroupns = "host"`, `netns = "host"`, `cgroup_manager = "cgroupfs"`)
   + exports `CONTAINERS_CONF_OVERRIDE`, and sets `HSH_NESTED=1` for
   the session shell. Without the storage conf the inner podman uses
   the default overlay driver and layer unpack fails with `after
   fallback to chroot: operation not permitted` (reported when running
   `./hsh-nested run pi .` inside a session).
4. `internal/cli/run.go`, `podCreateArgs`: when `HSH_NESTED=1` (only set
   inside a session), create the pod with `--network none` and
   `--infra-image <session image> --infra-command "sleep infinity"`.

The additional pieces fix what users hit running `./hsh-nested run pi .`
inside a session with an image whose podman is not
`quay.io/podman/stable` (which pre-configures all of this):

- without `cgroups = "disabled"`: `pod create` fails at
  `creating cgroup path /libpod_parent/...: read-only file system`
  (the cgroupfs manager of a rootful-acting podman cannot mkdir under
  the read-only `/sys/fs/cgroup` of the session container).
- without the custom infra: the default pause container's rootfs is a
  native overlay mount (`createInitRootfs` sets `RootfsOverlay`), which
  fails nested (`invalid argument`); `podman/stable` avoids it only by
  shipping fuse-overlayfs.
- without `netns = "host"`: `podman run` inside the session fails at
  netavark sysctl writes on the read-only `/proc/sys`.
- the nested session's own pod uses `--network none` instead, so its
  veth setup cannot clobber the outer session's `hsh0`/`hsh1` (fixed
  names and 10.77.0.1/2 addresses): with a shared network namespace
  the nested proxy deletes the outer session's veth pair and kills its
  egress. The nested session is network-isolated; image pulls still
  work because the nested hsh pulls from the outer session's shell
  before creating the pod.
- `HSH_NESTED` (set only inside a session) gates the inner-only pod
  flags so a host-level `HSH_NESTING=1 hsh run` keeps its normal pod
  (netavark network, pause infra, DNS egress).

## Why each piece is needed (all verified experimentally)

- `SETUID`/`SETGID`/`SETFCAP`: podman's startup re-exec into a fresh
  user namespace writes multi-line uid_map/gid_map through the
  session's procfs. With only DAC_OVERRIDE+SETUID+SETGID the write
  fails; SETFCAP is additionally required (bisected by cap-add).
- `SYS_ADMIN`: the inner runtime (crun) mounts a fresh procfs for the
  nested container. The kernel gates this on CAP_SYS_ADMIN in the user
  namespace that owns the pid namespace being mounted for
  (fs/proc/root.c sets fc->user_ns to the pid ns owner and
  mount_capable checks CAP_SYS_ADMIN there; a new pid ns created in the
  session's userns is owned by it, so caps suffice).
- `CHOWN`/`FOWNER`/`FSETID`: with SYS_ADMIN present, containers/storage
  takes the privileged layer-apply path (real lchown/chmod on unpack).
- `SETPCAP`: crun filters the nested container's bounding set with
  PR_CAPBSET_DROP, which needs CAP_SETPCAP.
- **Bounding-set trim** (the subtle one): podman prunes a container
  spec's requested caps to the caller's bounding set. Without the trim,
  the session's bounding set still contains podman's 13-cap default, so
  the inner podman requests caps (KILL, NET_BIND_SERVICE, SYS_CHROOT)
  that are in the bounding set but not in the session's permitted set;
  crun's capset then tries to grow the permitted set, which the kernel
  always refuses (cap_capset: new permitted must be a subset of old
  permitted) -> `crun: capset: Operation not permitted`. Trimming the
  bounding set makes podman prune, and every later capset is a subset
  operation. The same effect is why mimic containers created with
  `--cap-drop ALL --cap-add ...` worked while real sessions failed.
- The inner podman needs a `vfs` storage conf (nested overlay mounts are
  refused inside the sandbox); see docs/testing.md.

## Corrections to the earlier analysis

- The plan's earlier conclusion that `--userns host` on pod and
  workspace container was required was wrong: it came from the
  procfs-ownership theory. On this host, rootless podman's re-exec
  userns has the same two-line uid_map as a per-container userns, so
  the uid_map contents cannot distinguish the two; with the right caps
  kept, the default per-container userns passes every test. The
  speculative gVisor `--userns host` additions from the handoff were
  reverted (unverified; gVisor nesting is blocked upstream anyway).
- "Two-line uid_map = patch not applied" was a broken test: on this
  rootless host a `--userns host` container shows the same two-line
  map as a default container. Test what matters: `unshare -Ur id`,
  `podman pull`, `podman run`, nested `hsh run`.
- "The uid_map write is capability-checked against the userns owning
  the procfs" was only part of the story; the missing piece was
  SETFCAP (for the map write) and, for nested containers, the
  bounding-set/pruning interplay above.

## Verified test matrix (kernel 7.2.11, Fedora 44, rootless podman 5.8.9)

Session image: any image with podman and an `org.ifup.hsh` label; the
test used `localhost/nest-podman` built from `quay.io/podman/stable`:

```
FROM quay.io/podman/stable:latest
LABEL org.ifup.hsh='{"version":1,"shell":"/bin/bash"}'
```

With `HSH_NESTING=1 ./hsh-nested run --pull never localhost/nest-podman /tmp/nest-ws`
(the workspace dir must contain the hsh-nested binary for the inner run):

1. Session checks: `cat /proc/self/uid_map` (two lines, per-container,
   expected), `CapEff` = the 10-cap keep set, `unshare -Ur id` prints
   uid=0. PASS.
2. Nested podman with `CONTAINERS_STORAGE_CONF=/tmp/storage.conf` (vfs):
   `podman pull busybox`, `podman run --rm busybox echo ok`. PASS.
3. Nested hsh: pre-pull `ghcr.io/philips/hsh-pi` into the session's
   podman, then
   `(echo "echo INNER_HSH_OK; id; exit") | HSH_NESTING=1 /workspace/hsh-nested run --pull never pi /workspace/inner-ws`
   — the inner session starts, its shell runs as uid 0 with a correct
   per-container uid_map, fully off the network. PASS. (With
   `--pull newer` the inner hsh pulled the image through the outer
   session's egress proxy, which also works.)
4. gVisor: not tested; blocked upstream (runsc sentry refuses the
   nested uid_map write regardless of caps). Documented in
   docs/gvisor.md.

Inner sessions created inside the session's podman stay inside the
session (vfs storage under /tmp); they do not leak to the host podman.

## Docs updated

- docs/testing.md: nesting section rewritten with the final recipe
  (kept caps, bounding-set trim, vfs storage conf, podman-carrying
  test image, pre-pull preference, default-runtime-only note).
- docs/gvisor.md: "Not supported yet" notes that podman/hsh inside a
  gVisor sandbox fails at the nested uid_map write.

## Upstream issues worth filing

- podman 5.8: `_CONTAINERS_USERNS_CONFIGURED=1` skips the startup
  re-exec but `podman pull` still re-execs and then nil-panics in
  `libimage.(*Runtime).Pull` when the re-exec fails (seen earlier in
  this work as "failed to re-exec podman into user namespace" followed
  by a nil pointer dereference).
- gVisor: nested user namespace uid_map writes are refused even with
  CAP_SETUID/SETGID (`unshare -U` succeeds, `unshare -Ur` fails with
  EPERM on the map write).
