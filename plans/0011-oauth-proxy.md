# Sandboxed OAuth logins (OAuth capture proxy)

## Goal

Some agent harnesses authenticate with OAuth instead of a static API
key: the harness runs an interactive login (`codex login`,
`claude setup-token`, an MCP connector login in vibe, a device-code
flow), receives access/refresh tokens, and stores them locally for
later API calls. Letting those tokens land inside the sandbox defeats
its purpose: a compromised agent could exfiltrate or reuse them.

Implement a sandboxed OAuth login like nono.sh's "sandboxed OAuth
logins": the login flow runs **inside** the sandbox, but the hsh egress
proxy intercepts the provider's token-endpoint response, keeps the real
tokens on the host, and hands the sandbox **phantom tokens**
(`hsh_<64 hex>`). The harness stores and reuses phantoms; when it later
calls the provider's API through the proxy, the proxy swaps the phantom
back for the real token on egress. A phantom only works through the
matching proxy route — it is worthless if exfiltrated.

The mechanism is provider-agnostic: any token endpoint declared in the
config is captured (e.g. Codex's device flow at `auth.openai.com`,
Claude Code's OAuth at `console.anthropic.com`). Testing: Mistral Vibe
itself uses API-key auth (covered by secret injection), so the flow is
verified inside the `hsh-vibe` sandbox with a fake OAuth server driven
by curl — the same code path a real harness OAuth client would take. A
real end-to-end login with a harness's own OAuth client (e.g. a vibe
MCP connector login) is a follow-up once a user has an OAuth server to
connect to.

## Config

A provider is declared in the hsh config (file or `org.ifup.hsh`
label). Token endpoints and API hosts are taken from the pi provider
database (https://github.com/earendil-works/pi) and published endpoint
docs.

```json
"oauth": {
  "mistral": {
    "token_endpoint": {"host": "auth.example.com", "path": "/oauth/token"},
    "response_fields": ["access_token", "refresh_token"],
    "request_nonce_fields": ["refresh_token"],
    "api_hosts": ["api.example.com"]
  }
}
```

- `token_endpoint`: the HTTPS endpoint whose JSON response carries the
  tokens. Only responses to POSTs at this host+path are rewritten.
- `response_fields`: JSON paths in the token response to capture and
  replace with phantoms (default: `access_token`, `refresh_token`).
- `request_nonce_fields`: request body fields (form or JSON encoded)
  that carry a phantom during refresh flows; the proxy swaps in the
  real value before forwarding, then captures the response again.
- `api_hosts`: hosts whose requests get phantom redemption in the
  `Authorization: Bearer` header.

## Mechanics

1. **Intercept**: the proxy already MITMs TLS for hosts with configured
   secrets; it must also MITM for hosts involved in OAuth (token
   endpoint and API hosts) even when no static secret is configured.
2. **Capture**: on a response from a token endpoint, parse the JSON
   body; for each `response_field`, generate a phantom
   (`hsh_` + 32 random bytes hex), store `phantom -> {provider, field,
   value}` in the token store, and rewrite the body delivered to the
   sandbox.
3. **Redeem**: on a request to an `api_host` whose
   `Authorization: Bearer` value is a known phantom, replace it with
   the real token before forwarding upstream.
4. **Refresh**: on a POST to the token endpoint whose body contains a
   phantom in a `request_nonce_field`, replace it with the real value
   before forwarding; the response is captured like an initial login.
5. **Store**: `oauth-tokens.json` next to the denial database
   (`$XDG_STATE_HOME/hsh/`), mounted only into the proxy container —
   the workspace never sees it. Mode 0600, atomic writes. `hsh rm`
   session-state cleanup does not touch it; it survives sandboxes.

Both plain-HTTP (absolute-URI proxy requests) and HTTPS (CONNECT +
MITM) flows are handled.

## Implementation sketch

- `internal/config`: parse `oauth` providers from config/label.
- `internal/proxy/oauth.go`: provider matching (token endpoint,
  api hosts), token store (load/save/lookup), phantom generation,
  request-body redemption (form + JSON), response capture and rewrite.
- `internal/proxy/proxy.go`: intercept when an OAuth host is involved;
  route token-endpoint requests through the capture path (buffered
  response) in both the CONNECT and plain-HTTP paths.
- `internal/cli/proxy.go`: `--oauth-store` flag (default: next to the
  denial database).
- `docs/oauth.md`: design + configuration reference.
- Go unit tests (`internal/proxy`): the exchange over both the plain
  HTTP and the TLS MITM (CONNECT) path against an `httptest` upstream.
- `test/functional.sh`: fake OAuth server; in-sandbox curl login shows
  phantoms only; API call with a phantom succeeds; refresh rotates
  through the proxy; the store on the host holds the real tokens; the
  flow also runs inside the `hsh-vibe` image sandbox.

## Verification

1. Login inside the sandbox: the response the sandbox sees contains
   `hsh_<64hex>` phantoms, never the real tokens.
2. API call from the sandbox with a phantom Bearer succeeds upstream
   (real token substituted); the same phantom used directly against
   the provider (bypassing the proxy) fails.
3. Refresh flow with the phantom refresh token works and captures the
   new tokens.
4. Real tokens exist only in `$XDG_STATE_HOME/hsh/oauth-tokens.json`
   on the host; the sandbox filesystem and the harness keyring contain
   phantoms.
5. The same flow works inside the `hsh-vibe` sandbox image.
6. Unit and functional tests pass.

## Open questions

- Phantom shapes for clients that sniff token formats (e.g. Claude's
  `sk-ant-oat01-...` prefix): add a per-field format template when a
  real client needs it.
- id_token JWT-shaped phantoms: same — only if a client parses it
  locally.
- Whether the token store should be per-sandbox (sessions dir) instead
  of shared per-host state; shared matches the "the sandbox is
  disposable, the host credential is not" model.
