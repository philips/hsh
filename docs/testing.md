# Testing

## Functional test

`test/functional.sh` runs an end-to-end test of the sandbox, the egress
proxy, the denial database, and the example image:

- builds `hsh` and the dummy OpenAI-compatible endpoint
  (`test/dummyopenai`)
- ensures the example image exists (builds it with
  `podman build -t hsh-example examples/` if missing)
- **step 1**: a curl to a not-yet-allowlisted host (`api.mistral.ai`) is
  denied with 407 and recorded in a throwaway denial database
- **step 2**: `hsh proxy-auto-allow` adds the denied host to the config
  (trust on first use)
- **step 3**: in a fresh sandbox session, the endpoint is allowed (real
  upstream response through the intercepting proxy), the workspace mount
  works, and `echo hi | pi` returns `hello world` from the dummy endpoint
  that pi is configured against (`~/.pi/agent/models.json` +
  `~/.pi/agent/settings.json` inside the sandbox)
- **step 4**: a standalone `hsh proxy` forwards to an allowlisted host,
  injects the configured secret header (observed in the dummy endpoint's
  request log), and denies a non-allowlisted host with 407
- **step 5**: `--resume` re-enters the same container, with state
  persisted and the proxy/DNS working after re-entry
- **step 6**: `hsh ls` / `hsh rm` list and clean up sandboxes
- **step 7**: `proxy-watch` refuses to run without a terminal
- **step 8**: `hsh run --image` uses the image's `org.ifup.hsh` label
  config (mount, allowlist, and an `hsh://` secret resolved from the
  store, matching the harness images' labels)
- **step 9**: a sandboxed OAuth login (`test/oauthsrv`): the sandbox
  sees only phantom tokens, API calls and refresh flows redeem them at
  the proxy, and the real tokens stay in the host-side store
- **step 10**: the same OAuth flow inside the `hsh-vibe` image (skipped
  if that image is not built locally)
- **step 11**: `hsh run IMAGE --tasks` — the image's agent command
  receives the numbered task prompt, agent exit codes propagate, and a
  missing agent command fails clearly
- **step 12**: network policy modes — the `https` default allows TLS to
  an arbitrary host but denies plaintext there, and resolves all DNS
  names; `--network allowlist` and `--network none` deny it
- **step 13**: the secret store — `hsh secret add` stores values (0600
  file, 0700 directory), refuses duplicates without `--force`, `secret
  ls` shows names but never values, an `hsh://` config reference resolves
  through the store and is injected (observed in the dummy endpoint's
  request log), and after `secret rm` the entry goes missing: a warning,
  no injection, the request still forwarded, and a dangling-reference
  warning without editing the config

Run it with:

```
./test/functional.sh
```

`internal/proxy/oauth_test.go` covers the OAuth capture over both the
plain-HTTP and the TLS MITM path as Go unit tests.
`internal/secretstore` covers the store CRUD, overwrite semantics, and
file/directory permissions; `internal/config` covers secret reference
parsing (`env://`, `hsh://`, bare, unknown schemes), resolution through
a fake store, and the config rewrite for `secret add`; `internal/cli`
covers `secret add`/`rm`/`ls` behavior via `--value-stdin`.

Requirements: Go, rootless podman, network access (for `api.mistral.ai` and
the image build), and port 8899 free on the host. The test is hermetic: it
uses a temporary config, workspace, and denial database, and cleans up after
itself. When a step fails, the artifacts are kept in a temp directory (the
path is printed) for debugging.

The dummy endpoint listens on `0.0.0.0:8899` because connections from the
sandbox reach the host through the pasta gateway, which does not forward to
loopback-only listeners. The endpoint also serves its instance id at `/id`
(and as the `X-Dummy-Id` header) so the test can detect another process
already squatting on the port.
