# Fix pi's fd/ripgrep download warnings inside the sandbox

## Problem

Running pi inside the harnessh sandbox (example image) prints:

```
Warning: Failed to download fd: fetch failed: Request was cancelled.: Proxy response (403) !== 200 when HTTP Tunneling
Warning: Failed to download ripgrep: fetch failed: Request was cancelled.: Proxy response (403) !== 200 when HTTP Tunneling
```

The warnings are non-fatal, but pi's `find` and `grep` tools then fail at use
time ("fd is not available and could not be downloaded").

## Root cause (verified against the installed pi 1.4.0 source)

Interactive pi calls `ensureTool("fd")` and `ensureTool("rg")` at startup
(`dist/modes/interactive/interactive-mode.js:778-779`). `ensureTool`
(`dist/utils/tools-manager.js`) resolves a tool in this order:

1. pi's own tools dir (`~/.pi/agent/bin/fd`, `rg`)
2. **system PATH** — `fd` or `fdfind` for fd; `rg` for ripgrep
3. download: version lookup at `https://github.com/<repo>/releases/latest`
   (same-origin 302, no `api.github.com`), then the binary from
   `https://github.com/<repo>/releases/download/<tag>/<asset>`, which 302s
   to `release-assets.githubusercontent.com` (verified; the legacy target is
   `objects.githubusercontent.com`)

The example image has neither the tools in pi's dir nor `fdfind`/`rg` on
PATH (verified: `command -v fdfind fd rg` finds nothing), so pi downloads.
The sandbox proxy denies both hosts (verified: `curl https://github.com/...`
and `https://release-assets.githubusercontent.com/` through the sandbox fail
with the proxy's denial — 403 today, 407 per plans/0003-proxy-denied.md),
undici's ProxyAgent surfaces "Proxy response (403) !== 200 when HTTP
Tunneling", and pi warns.

Note: pi's managed downloads land in `~/.pi/agent/bin`, which is the
container's ephemeral home (`/home/harnessh`) — they would be re-downloaded
every session even if the egress were allowed.

## Fix options

### A. Pre-install fd and ripgrep in the example image (recommended)

Add to `examples/Containerfile`:

```
RUN dnf install -y nodejs npm curl fd-find ripgrep && dnf clean all
```

Fedora 44 provides `fd-find` 10.4.2 (binary `fdfind`, which pi checks) and
`ripgrep` 15.2.0 (binary `rg`) — verified available. pi's `getToolPath`
then finds both on system PATH and `ensureTool` returns without any network
access: no warnings, no new allowlist hosts, tools persist in the image.
This matches the established pattern of pre-installing pi itself.

### B. Allowlist the download hosts (alternative)

Add to `examples/config.harnessh` `network.allow`:

```
"github.com",
"release-assets.githubusercontent.com",
"objects.githubusercontent.com"
```

(`release-assets.githubusercontent.com` is the current redirect target;
`objects.githubusercontent.com` is the legacy one — include both for
robustness.) pi then downloads its pinned tool versions through the proxy,
into the ephemeral container home. Costs: wider allowlist, per-session
re-downloads, and the downloads must succeed for the tools to work.

### C. `PI_OFFLINE=1` in the image (recommended together with A)

`ENV PI_OFFLINE=1` makes pi skip tool downloads and its background update /
model-catalog checks entirely. Combined with A this guarantees pi never
attempts tool-download egress inside the sandbox, even if a future pi
version changes its PATH lookup. Trade-off: pi won't refresh model
catalogs or check for self-updates inside the sandbox (updates happen by
rebuilding the image). If tools are somehow missing, pi warns
"Offline mode enabled, skipping download" instead — so C should ship
with A, not alone.

### D. Synergy with plans/0003-proxy-denied.md

Once the denial log and TOFU auto-allow land, option B becomes a review
flow instead of a permanent allowlist entry: pi's attempts get recorded,
`harnessh proxy-denied` shows `github.com` /
`release-assets.githubusercontent.com`, and `harnessh proxy-auto-allow` adds
them on approval.

## Recommended change set

A + C: pre-install `fd-find` + `ripgrep`, set `PI_OFFLINE=1` in
`examples/Containerfile`. The example sandbox then runs pi with working
find/grep tools and the minimal MVP allowlist unchanged. Document B (with
the exact hosts) in `docs/config.md` for users who prefer pi's pinned
tool versions.

## File changes

- `examples/Containerfile`: add `fd-find ripgrep` to the dnf install line;
  add `ENV PI_OFFLINE=1`.
- `docs/config.md`: short section "Tools that self-download" listing the
  hosts pi needs if fd/rg are not pre-installed (option B), and the
  `PI_OFFLINE` env var.
- `docs/architecture.md`: one line in the example-image section noting the
  image pre-installs fd/ripgrep so pi's find/grep tools work offline.

## Verification

1. Rebuild: `podman build -t harnessh-example examples/`.
2. In the sandbox (example config, unchanged allowlist):
   `command -v fdfind rg` finds both; `fdfind --version`, `rg --version`
   work (this is exactly pi's `commandExists` check).
3. Start interactive pi under a pty and confirm no "Failed to download"
   warnings at startup (needs a pty — `script(1)` or a real terminal; note
   `util-linux`'s `script` is not in the minimal image, so verify with a
   terminal or add `util-linux` temporarily for the test).
4. Confirm the proxy log shows no denied github.com /
   release-assets.githubusercontent.com requests during the pi session.
5. Regression: `pi --version` still 1.4.0; allowlisted egress, secret
   injection, and denials unchanged.

## Open questions

- Ship pi's pinned tool versions instead of Fedora's (option B) for
  reproducibility across image rebuilds? Fedora's versions are close to
  current (fd 10.4.2, rg 15.2.0) and the tools' interfaces pi uses are
  stable.
- Should `PI_OFFLINE=1` be opt-in (documented) rather than baked into the
  example image, for users who want pi's update checks? Current
  recommendation: bake it in; the image is the update mechanism.
