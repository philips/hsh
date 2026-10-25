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

Run it with:

```
./test/functional.sh
```

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
