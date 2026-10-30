# 0033: gVisor (runsc) as an optional sandbox runtime

Status: prototype on branch `gvisor-prototype`, implementing the external network proxy layout (Plan steps 1–4, 6, 7). Design documentation: `docs/gvisor.md`. `hsh forward`, `hsh ssh`, and `hsh oauth callback` are served by a separate `/hsh-sshd` role inside the sandbox, with no access to secrets. Remaining: the netns single-interface question, and whether this stays opt-in.

gVisor gives a stronger boundary than a plain Linux container (a userspace kernel intercepts syscalls) without needing `/dev/kvm`, and unlike krun (see 0020) it keeps network namespaces meaningful, so the egress model survives. The spike shows rootless podman + runsc can run the sandbox, but not with the current two-container pod and inner-namespace design. runsc's external network proxy lets the egress proxy stay outside the sandbox in its own container (see below).

## Spike results

Environment: Fedora, x86_64, rootless podman 5.8, runsc release-20261026.0 installed from the upstream tarball into a scratch directory (`https://storage.googleapis.com/gvisor/releases/release/latest/x86_64/gvisor.tar.zstd`; `runsc` and `gvisor-bin/` must stay together). Podman is pointed at it with `CONTAINERS_CONF_OVERRIDE`:

```
[engine]
runtime="runsc"
cgroup_manager="cgroupfs"
[engine.runtimes]
runsc=["/path/to/runsc-wrap"]
```

where `runsc-wrap` is `exec runsc --ignore-cgroups "$@"`. The platform is the default `systrap`; no KVM needed.

Needed to make it start:

- `--ignore-cgroups`: without it runsc fails on `cgroup.subtree_control` (cgroupfs manager) or on a systemd D-Bus authorization error (systemd manager). Podman refuses `--cgroups=disabled` with runsc. Consequence: no cgroup resource limits.
- `--security-opt label=disable` on every container, including the pod infra container (runsc rejects SELinux labels: `SELinux is not supported`). hsh already does this for its own containers.

Works:

- Rootless `podman run` boots a gVisor sentry (`uname -r` is `4.19.0-gvisor`, `id` is root in the userns).
- Networking through pasta uses runsc's netstack: the sandbox sees the pasta interface and reaches the internet.
- With `--cap-add NET_ADMIN,SYS_ADMIN`: `ip link add type veth`, `unshare -n`, netlink all work. `unshare -n` yields a namespace with only `lo` and a direct `curl https://1.1.1.1` from it fails, i.e. the same isolation as crun. (Under krun/TSI the same test succeeded, so the egress boundary was broken.)
- `LinkSetNsPid` (move veth into a child netns by PID) returns `operation not supported`; `LinkSetNsFd` with `/proc/PID/ns/net` works. `internal/netsetup/netsetup.go` now uses the fd form, which also works under crun (verified).
- The real binaries in one container under runsc: `/hsh-workspace` starts `/hsh-proxy` in the outer namespace, the veth is created, the inner namespace is configured, the proxy listens on `10.77.0.1:8080`, `curl` through the proxy returns 200, direct egress is blocked, and the proxy log shows `CONNECT example.com:443 -> allowed`. Output matches the crun run of the same script.
- `podman exec` into a running runsc container, `podman pod stop` / `pod start` restart, and a pod created with `--infra=false` all work.
- `podman run -it`: the window size propagates (`stty size` correct, `TERM` passed).
- Inside the sandbox, `setuid(1000)` works with the rootless userns's subuid mapping, and a uid-1000 process cannot read `/proc/PID/environ`, open `/proc/PID/mem`, or read a mode-0600 root file of a root process. So the workspace can be separated from the proxy by uid inside one sandbox.
- SQLite in WAL mode written by the proxy in the sandbox onto a bind-mounted directory (`monitor.db`) is readable from the host (`hsh proxy denied --db ...` listed the denial).

Does not work:

- **Two containers in one pod.** Pods need shared namespaces and runsc rejects joining them (`joining UTS namespace ... is not supported`). With `--share net` the containers start but each runsc sandbox runs its own netstack: the second container only has `lo` (not even internet), and the first container's `127.0.0.1` listener is unreachable. The current proxy-container + workspace-container layout cannot work.
- **Host networking does not rescue the pod layout.** The UTS join error goes away with `podman pod create --share net`, but each runsc sandbox then has its own netstack. With `runsc --network=host` the sandboxes share the pod's netns and the workspace reaches a proxy on `127.0.0.1` in the other container, but hostinet has no netlink veth (`RTNETLINK answers: Operation not supported`) and a fresh `unshare -n` namespace has no `AF_INET` at all (`Address family not supported by protocol`), so the inner-namespace isolation cannot be built.
- `runsc --rootless` (method 1 in the gVisor docs) is for `runsc do`; not used, podman's rootless userns path (method 2) is.

Not verified:

- Interactive TTY semantics: `tty` printed `ttyname error: Inappropriate ioctl for device` under `podman run -it` (fd 0 works). hsh's workspace and the vt/attach code may depend on a controlling terminal, `/dev/tty`, or job control.
- `podman attach` with detach keys, the detach/attach/reaper flow, `hsh forward` and the ssh server over `podman exec -i`, and the OAuth callback relay.
- Behaviour of the host monitor reading `monitor.db` while the sandbox is actively writing (coherency of gofer bind mounts under concurrent access).
- Performance, memory overhead, and syscall compatibility of the shipped harness images (pi, claude-code, codex, vibe, muse-code, nvim AppImage).
- The functional test suite under runsc.

## External network proxy (spike)

runsc's experimental `--network-proxy-path=SOCK` attaches the sandbox's primary interface to a `SOCK_SEQPACKET` Unix socket owned by an external process. The sentry's netstack writes raw IP packets to it, and the peer is responsible for forwarding and filtering (https://gvisor.dev/docs/user_guide/networking/#external-network-proxy-experimental). The peer must be listening when the sandbox starts (runsc dials once, 5 s timeout) and the flag takes a host path.

Spike peer (a throwaway Go program, not in the repo): `net.Listen("unixpacket", path)`, then for each connection a gVisor `pkg/tcpip` stack (`gvisor.dev/gvisor@go`, builds with the repo's toolchain) with an `fdbased` link endpoint (no Ethernet header, MTU 1500) on the accepted fd, promiscuous mode and spoofing on, default routes on the NIC, and `tcp.NewForwarder` / `udp.NewForwarder` handlers that log the original destination and relay with `net.Dial`.

Results:

- A runsc container with the flag, no capabilities and no netns tricks, runs `curl https://example.com` (200) and `curl https://1.1.1.1` (301) through the peer. The peer sees every flow with its original destination: UDP DNS to the resolvers from `/etc/resolv.conf` (`169.254.1.1:53`, `100.100.100.100:53`) and TCP SYNs to the resolved IPs on 443.
- The same works with the peer in a normal (crun) container: `podman pod create --infra=false --share ""`, proxy container with a host directory bind-mounted at `/sock` holding the socket, workspace container run with `podman --runtime PATH-TO-WRAPPER` where the wrapper adds `--ignore-cgroups --network-proxy-path=HOST/PATH/sock`. runsc dials the host path, which is the bind-mounted socket, so the proxy can stay in its own container with its own egress.
- Only the primary interface is proxied. Anything else in the container netns would bypass it, so the netns must have a single interface.
- The sandbox needs neither `NET_ADMIN` nor `SYS_ADMIN`; there is no veth, no inner namespace, no `HTTP_PROXY`, and no `resolv.conf` rewrite.

This keeps the two-container, proxy-outside-the-workspace layout and removes most of `internal/netsetup`, the relay, and the capability drop for this runtime.

## Plan

Add an opt-in runtime, e.g. `hsh run --runtime=gvisor` (config key `runtime`), leaving crun/runc as the default.

1. **Transparent listener in the proxy.** A new `/hsh-proxy` option (e.g. `--netproxy-socket PATH`) listens on a `SOCK_SEQPACKET` socket and, per connection, runs a `pkg/tcpip` stack like the spike peer. The accept loop must handle reconnects (runsc dials again on each workspace container start, so resume works) and remove a stale socket at startup.
   - TCP: the forwarder hands each accepted connection to the existing proxy logic as if it were a `CONNECT` to the original destination. For port 443 read the ClientHello SNI and apply the same allowlist/denial/hold/monitor policy, MITM with the session CA, and inject secrets. For plain HTTP use the `Host` header. Other ports and protocols follow the network mode (deny in `https` and `allowlist` modes; none in `none`).
   - IP literals and missing SNI: allow only an IP that the proxy's own DNS handler returned for an allowed name (needs a short-lived name-to-IP map); otherwise deny and record the denial.
   - UDP: answer port 53 to any destination with the existing DNS handler (`internal/proxy/dns.go`), so denied names are recorded exactly as today; drop other UDP. Drop ICMP.
2. **Pod and runtime wiring in `run.go`.** In gVisor mode: `podman pod create --infra=false --share ""`; the proxy container keeps crun and gets a bind mount of a per-session directory for the socket; the workspace container is created with `--runtime WRAPPER`, where the wrapper is a small script written into the session state directory (`exec runsc --ignore-cgroups --network-proxy-path=<session>/netproxy/sock "$@"`). The proxy must be running and listening before the workspace container starts. The recorded per-container runtime was verified to apply to later `pod start`, `exec`, and `pod stop`. Locate `runsc` on `PATH` or via `HSH_RUNSC`, and explain how to install it when missing (upstream tarball; no Fedora package).
3. **Workspace entrypoint in gVisor mode.** `/hsh-workspace` skips `CreateHostVeth`, `waitForProxy`, the inner re-exec, and the capability drop, and goes straight to the shell/agent setup (CA install, notice, home, launch). Drop `--cap-add NET_ADMIN/SYS_ADMIN`, always `--security-opt label=disable`, and leave the proxy environment variables unset (traffic is intercepted below the socket layer). The CA still has to be trusted inside the sandbox.
4. **Single-interface netns.** The workspace container's netns must expose exactly one interface (the default pasta setup does). Decide whether `--network=none` with a synthetic interface is possible, to avoid leaving an unused pasta uplink in the netns; check whether runsc requires a default route to choose the primary interface.
5. **Verify the unchecked behaviors** below, in this order: TTY/controlling terminal and the detach/attach flow; `hsh forward`/ssh/OAuth callbacks (the callback relay and port forwarding currently use the inner netns and `podman exec`, so they need a gVisor-specific path); the monitor database; the harness images.
6. **Tests.** A gVisor leg in `test/functional.sh` (skipped without runsc): allowed host via MITM, denied host recorded in `monitor.db`, secret injection, IP literal denied, DNS denial recorded, resume reconnects, and a check that killing the proxy container cuts all egress from the workspace.
7. **Docs.** Document the runtime option, the transparent-proxy layout, the missing cgroup limits, and the install steps in `docs/`; update `docs/architecture.md`.

## Fallback layout: one container for proxy and workspace

If the external network proxy is rejected (it is marked experimental), put `/hsh-proxy` and `/hsh-workspace` in a single runsc container and keep the veth/inner-namespace design: `/hsh-workspace` starts the proxy in the outer namespace, and the inner process drops to an unprivileged uid before exec (verified: that uid cannot read the proxy's `/proc/PID/environ`, `/proc/PID/mem`, or 0600 root files). This weakens the proxy/workspace separation to uid isolation inside one sandbox.

## Alternative: split containers over a Unix socket

Workspace sandbox with `--network none`, a TCP-to-Unix-socket relay inside it, runsc `--host-uds`, and the proxy serving a Unix socket. Superseded by the external network proxy, which does the same at L3/L4 without relays or `host-uds`.

## Risks

- `--network-proxy-path` is experimental and may change; it is pinned to the runsc release installed by the user.
- The proxy now terminates arbitrary TCP flows from a userspace netstack: more parsing surface (TCP forwarder, SNI sniffing) in the component that holds secrets. It runs in its own container, as today.
- Proxy gets original-destination IPs only; hostname policy depends on SNI, the Host header, and DNS correlation.
- gVisor's `pkg/tcpip` becomes a dependency of the hsh binary.
- No resource limits (`--ignore-cgroups`).
- gVisor's syscall coverage may break some agents or tooling; compatibility is unmeasured.
- `runsc` has no distro package on Fedora, so users install and update it themselves.
