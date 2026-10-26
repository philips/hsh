# Post-MVP follow-ups

The MVP (plans/0000-mvp.md) is implemented and verified end to end: sandboxed
shell, workspace mount, allowlisted egress with secret injection, denied
egress for everything else, and a pre-installed Pi in the example image
(`pi --version` → 1.4.0 with the plan's original allowlist).

Items, in rough priority order (closed items keep their resolution):

1. **Closed — MITM for secret hosts: the MITM certificate is the correct
   solution.** The question was whether TLS interception could be avoided
   entirely for secret-bearing hosts (the proxy already tunnels hosts
   without secrets, end-to-end TLS via `HTTPS_PROXY`). It cannot: injecting
   a header into an HTTPS request requires terminating TLS, and the
   secret must never enter the workspace (having the client send it
   defeats the purpose). Per-host `CONNECT` to a dedicated injecting
   upstream, or a sidecar per secret host, only relocate the termination.
   The per-sandbox CA — certificate appended to the workspace's trust
   bundle, private key mounted into the proxy container only — is the
   design (docs/architecture.md, "TLS interception"). Related direction:
   phantom tokens (docs/sandboxed-oauth.md) keep the workspace free of
   anything credential-shaped while the proxy still terminates TLS.
2. **Non-HTTP CONNECT to intercepted hosts.** Hosts with a secret are
   TLS-intercepted, which breaks non-HTTP protocols (ssh, etc.) to those
   hosts. Could fall back to tunnel + separate injection path.
3. **Robustness:** stale pods from crashed runs are not reaped automatically
   (`podman pod ls | grep hsh`); container start races if the image is
   not present locally (podman auto-pulls, but the proxy bind-retry window
   is 60s). Cleanup subcommands are planned in plans/0007-ls-rm.md;
   `--resume` leaves pods behind by design.
4. **Workspace ergonomics:** the shell runs as container uid 0 (mapped to
   the host user) with zero capabilities. Some tools expect a non-root user;
   consider a `--uid` option that maps to a dedicated workspace user.
5. **Done — testing:** `test/functional.sh` runs the end-to-end scenario
   against a local dummy endpoint (plus proxy, DNS, `--resume`, and TUI
   checks); see docs/testing.md.
