# gvx-spike

Phase 0 spike for plans/0055-gvx.md: embeds the gvx SDK
(`z.gfire.dev/gvx/pkg/gvx`) and exercises what hsh needs from it. Separate
module so it is not built by the repository's `./...`. Results are in the
plan under "Phase 0 results so far".

The spike builds against a patched gvx. `fork.sh` clones the gvx version that
`go.mod` requires into `~/.cache/gvx-spike/gvx-fork`, applies
`patches/*.patch`, and writes `go.fork.mod` with a `replace` to the clone.
`run.sh` runs `fork.sh`, then builds with that module file. A plain
`go build` here fails, because the spike uses the patched API.

- `patches/0001-link-network-mode.patch`: `gvx.NetModeLink`
  (`Config.LinkConn`, `Config.LinkMTU`) and `netbackend.PacketLink`. eth0 is
  attached to a connected byte stream that carries IP packets, each framed
  as a 4-byte big-endian length followed by the packet. It covers lockdown,
  checkpoint quiescing, and restore onto a new connection.

Build and run (Go 1.27.4 is fetched through `GOTOOLCHAIN`; the binary, fork,
and temporary build files go to `~/.cache/gvx-spike`, override with
`SPIKE_DIR`; on macOS the binary is signed with `entitlements.plist`):

    ./run.sh -image ghcr.io/philips/hsh-claude-code:latest -- claude --version

Sandbox flags: `-image`, `-pull`, `-net none|nat|link`, `-link SOCKET`
(dial a packet link peer; implies `-net link`), `-link-mtu`,
`-root-volume FILE`, `-workspace DIR`, `-tty`, `-env K=V`, `-user`,
`-workdir`, `-memory`, `-control SOCKET`, `-checkpoint-dir DIR`,
`-restore DIR`, `-cpuprofile FILE` (needs `-no-lockdown`), `-no-lockdown`.
Everything after `--` is the guest argv.

Subcommands, run with `~/.cache/gvx-spike/spike` after a `run.sh` build:

    spike exec -control SOCKET [-tty] [-i] -- CMD...
    spike dial -control SOCKET -port N          stdin/stdout to a guest TCP port
    spike call -control SOCKET METHOD [JSON]    e.g. lifecycle.pause, sandbox.stats
    spike peer -listen SOCKET [-mtu N]          packet link peer

The control socket's directory must be mode 0700. The peer is a
promiscuous gVisor netstack, shaped like hsh's proxy netstack. It logs
every flow and relays TCP to the real destination and UDP 53 to the host
resolver, and drops other UDP. Port 81 on any address sends 256 MiB, and
port 82 discards what it receives; both are for throughput.

Scripts:

- `tty_test.py [IMAGE]`: drives a guest bash through a pty and reports
  `tty`, initial size, resize, `SIGWINCH`, Ctrl-Z/jobs, Ctrl-C, `/dev/tty`,
  `TERM`, and exit status.
- `link_test.sh`: link mode end to end. It checks the lockdown, DNS and
  HTTPS through the peer, throughput both ways, checkpoint and restore onto
  a new peer connection, and losing the peer.
- `bench.sh`: runs the same commands under gvx, podman with runsc (the
  runsc that hsh installed, or `HSH_RUNSC`), and podman with crun; it skips
  what is not installed.
