# Sandboxed OAuth logins

Some agent harnesses authenticate with an OAuth login instead of a
static API key: the harness runs an interactive login (or a device-code
flow), receives access and refresh tokens, and stores them locally for
later API calls. Letting those tokens land inside the sandbox defeats
its purpose — a compromised agent could exfiltrate and reuse them.

hsh solves this the same way as nono.sh's sandboxed OAuth logins: the
login flow runs **inside** the sandbox, but the egress proxy intercepts
the provider's token-endpoint response, keeps the real tokens on the
host, and hands the sandbox **phantom tokens**
(`hsh_<64 hex>`). The harness stores and reuses the phantoms; when it
later calls the provider's API through the proxy, the proxy swaps the
phantom back for the real token on egress. A phantom only works
through the proxy — exfiltrated, it is worthless.

## Configuration

Declare the provider's token endpoint and API hosts in the config
(file or `org.ifup.hsh` label):

```json
"oauth": {
  "myprovider": {
    "token_endpoint": {"host": "auth.example.com", "path": "/oauth/token"},
    "response_fields": ["access_token", "refresh_token"],
    "request_nonce_fields": ["refresh_token"],
    "api_hosts": ["api.example.com"]
  }
}
```

- `token_endpoint`: responses to POSTs at this host+path are rewritten.
  `response_fields` defaults to `access_token` + `refresh_token`.
- `request_nonce_fields`: request body fields (form or JSON encoded)
  that may carry a phantom during a refresh flow; the proxy swaps in
  the real value before forwarding, then captures the response again.
- `api_hosts`: hosts whose `Authorization: Bearer` phantoms are
  redeemed for the real token on egress.
- The token endpoint and API hosts must also be in `network.allow`.

## Mechanics

- The proxy MITMs TLS for OAuth-involved hosts even when no static
  secret is configured for them (otherwise it would tunnel and could
  not rewrite the exchange).
- **Capture**: a JSON response from a token endpoint is rewritten
  field by field; each real value is stored under a fresh phantom and
  the sandbox sees only `hsh_<64 hex>` values.
- **Redeem**: requests to an `api_host` carrying
  `Authorization: Bearer hsh_...` get the real token substituted
  upstream. The phantom never leaves the sandbox; the real token never
  enters it. A redeemed phantom wins over a static `secrets` injection
  on the same host: the static secret is injected only when the request
  carries no phantom, so a subscription login and a static API key can
  share one `api_host`.
- **Refresh**: a phantom sent back in a refresh flow is redeemed in
  the request body, and the rotated response is captured again.
- Both plain-HTTP and HTTPS flows are handled.

## Browser-redirect logins

A browser-redirect login (the harness runs a loopback listener and the
provider redirects the browser to it) cannot complete on its own: the
listener's loopback is inside the sandbox's private network namespace,
unreachable from the browser. Bridge it by replaying the failed redirect:

```
sandbox$ pi            # starts a login; prints the authorize URL
laptop-browser: open the URL, complete the login - the redirect to
            http://127.0.0.1:PORT/... fails, but the URL is in the
            address bar
laptop$ ssh server
server$ hsh oauth callback [--session ID] 'http://127.0.0.1:PORT/auth/callback?code=...&state=...'
```

Run it promptly: authorization codes are short-lived and single-use, so
a code minted before an egress hiccup (a hold, a denied host) is stale
by the time the exchange finally goes through - the provider answers
`invalid_grant`. Deliver the callback right after the browser login,
and start a fresh login (new browser round-trip) rather than replaying
an old callback URL. The harness's `state` check makes mis-delivery
fail safe, and non-loopback URLs are refused.

## Token store

Captured tokens live in `oauth-tokens.json` next to the denial
database (`$XDG_STATE_HOME/hsh/oauth-tokens.json`), mode 0600. The
directory is mounted only into the proxy container; the workspace
never sees it. The store is shared across sandboxes and survives them
— the sandbox is disposable, the host credential is not. Remove it to
revoke all phantoms.

## Limitations

- Phantoms are opaque `hsh_<64 hex>` values. A client that sniffs a
  specific token format (e.g. a required `sk-...` prefix) would need a
  per-field format template; not implemented yet.
- Redemption covers the `Authorization: Bearer` header only. Tokens
  sent as query parameters or in other headers are not redeemed.
- Only JSON token responses are rewritten.
