# gvx-spike

Phase 0 spike for plans/0055-gvx.md: embeds the gvx SDK
(`z.gfire.dev/gvx/pkg/gvx`) and exercises what hsh needs from it. Separate
module so it is not built by the repository's `./...`. Results are in the
plan under "Phase 0 results so far".

Build and run (Go 1.27.4 is fetched through `GOTOOLCHAIN`; the binary and
temporary build files go to `~/.cache/gvx-spike`, override with
`SPIKE_DIR`; on macOS the binary is signed with `entitlements.plist`):

    ./run.sh -image ghcr.io/philips/hsh-claude-code:latest -- claude --version

Sandbox flags: `-image`, `-pull`, `-net none|nat`, `-root-volume FILE`,
`-workspace DIR`, `-tty`, `-env K=V`, `-user`, `-workdir`, `-memory`,
`-control SOCKET`, `-checkpoint-dir DIR`, `-restore DIR`, `-cpuprofile FILE`
(needs `-no-lockdown`), `-no-lockdown`. Everything after `--` is the guest
argv.

Control clients against a sandbox started with `-control SOCKET` (the
socket's directory must be mode 0700):

    spike exec -control SOCKET [-tty] [-i] -- CMD...
    spike dial -control SOCKET -port N          stdin/stdout to a guest TCP port
    spike call -control SOCKET METHOD [JSON]    e.g. lifecycle.pause, sandbox.stats

Scripts:

- `tty_test.py [IMAGE]`: drives a guest bash through a pty and reports
  `tty`, initial size, resize, `SIGWINCH`, Ctrl-Z/jobs, Ctrl-C, `/dev/tty`,
  `TERM`, and exit status.
- `bench.sh`: runs the same commands under gvx, podman with runsc (the
  runsc that hsh installed, or `HSH_RUNSC`), and podman with crun.
