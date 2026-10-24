# Post-MVP follow-ups

The MVP (plans/0000-mvp.md) is implemented and verified end to end: sandboxed
shell, workspace mount, allowlisted egress with secret injection, denied
egress for everything else, and a pre-installed Pi in the example image
(`pi --version` → 1.4.0 with the plan's original allowlist).

Open items, in rough priority order:

1. **README TODO: `HTTPS_PROXY` instead of MITM.** The proxy already tunnels
   allowlisted hosts that have no secret configured (end-to-end TLS via
   `HTTPS_PROXY`). Remaining question: whether interception can be avoided
   entirely for secret-bearing hosts (e.g. per-host `CONNECT` to a dedicated
   injecting upstream, or a sidecar per secret host). Interception is
   currently required to inject headers into HTTPS requests.
2. **Non-HTTP CONNECT to intercepted hosts.** Hosts with a secret are
   TLS-intercepted, which breaks non-HTTP protocols (ssh, etc.) to those
   hosts. Could fall back to tunnel + separate injection path.
3. **Robustness:** stale pods from crashed runs are not reaped automatically
   (`podman pod ls | grep harnessh`); container start races if the image is
   not present locally (podman auto-pulls, but the proxy bind-retry window
   is 60s).
4. **Workspace ergonomics:** the shell runs as container uid 0 (mapped to
   the host user) with zero capabilities. Some tools expect a non-root user;
   consider a `--uid` option that maps to a dedicated workspace user.
5. **Testing:** the end-to-end verification is currently a manual script
   (see docs/architecture.md). Add an automated test that runs the MVP
   scenario against a local echo server instead of real internet hosts.
