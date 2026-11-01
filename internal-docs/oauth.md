# Sandboxed OAuth logins

Some agent harnesses authenticate with an OAuth login instead of a
static API key: the harness runs an interactive login (or a device-code
flow), receives access and refresh tokens, and stores them locally for
later API calls. Letting those tokens land inside the sandbox defeats
its purpose — a compromised agent could exfiltrate and reuse them.

hsh solves this the same way as nono.sh's sandboxed OAuth logins
(https://nono.sh/docs/cli/features/sandboxed-oauth-logins): the login
flow runs **inside** the sandbox, but the egress proxy intercepts the
provider's token-endpoint response, keeps the real tokens outside the
workspace, and hands the sandbox **phantom tokens**. The harness stores
and reuses the phantoms; when it later calls the provider's API through
the proxy, the proxy swaps the phantom back for the real token on
egress. A phantom only works through the proxy — exfiltrated, it is
worthless.

```
OAuth provider ──► hsh proxy ──► sandboxed agent
real tokens        stores real    sees phantoms
                   returns phantom
```

## Configuration

A config (file or `org.ifup.hsh` label) **names** providers; it never
defines them:

```json
"oauth": {
  "providers": ["openai-codex", "anthropic"],
  "api_hosts": {"openai-codex": ["chatgpt.com"]}
}
```

- `providers`: provider descriptors to load (see below). An unknown
  name is an error at launch.
- `api_hosts` (optional): narrow a provider's API hosts to a subset.
  Widening is an error.
- The token endpoint and API hosts must also be in `network.allow`
  (the harness image labels are checked for this in the unit tests).
- Inline definitions (`"oauth": {"name": {"token_endpoint": …}}`) are a
  load error. Where a refresh token is sent is decided only by files the
  user owns, so an image or a workspace config (which an agent can write)
  cannot point a token endpoint or API host at a host it controls.

## Provider descriptors

Everything vendor-specific is data in a provider descriptor; the
binary holds only the generic capture/redeem machinery.

| Layer | Location |
|---|---|
| shipped | built into the hsh binary (`internal/oauthprov/providers/*.json`), never written to disk |
| full override or new provider | `$XDG_CONFIG_HOME/hsh/oauth-providers/NAME.json` |
| partial override (drop-ins) | `$XDG_CONFIG_HOME/hsh/oauth-providers/NAME.d/*.json` |

Resolution: the user's `NAME.json` if present, otherwise the shipped
descriptor; then every `NAME.d/*.json` in lexical order as a JSON merge
patch (RFC 7396: objects merge, `null` deletes, everything else
replaces). This mirrors systemd units and their `unit.d/*.conf`
drop-ins: a drop-in changes one field and keeps inheriting everything
else from later hsh releases.

A full override goes stale when a release changes the shipped
descriptor. `hsh oauth provider edit --full` therefore records
`"base": "sha256:…"` of the shipped file in the copy, and `hsh run` and
`hsh oauth provider ls` warn when the shipped descriptor no longer
matches it (or when a full override of a shipped provider has no
`base`).

Shipped: `openai` (pi's Sign in with ChatGPT), `openai-codex` (Codex CLI
and pi's ChatGPT Plus/Pro), `anthropic` (Claude Code and pi's Claude
Pro/Max).

```json
{
  "version": 1,
  "token_endpoint": {"host": "platform.claude.com", "path": "/v1/oauth/token"},
  "response_fields": [
    {"path": "access_token", "format": "sk-ant-oat01-{phantom}"},
    {"path": "refresh_token", "format": "sk-ant-ort01-{phantom}"}
  ],
  "request_nonce_fields": ["refresh_token"],
  "api": {"hosts": ["api.anthropic.com"]}
}
```

- `version`: descriptor format version (1). A newer version is rejected
  as a whole, never half-applied.
- `token_endpoint`: responses to POSTs at this host + exact path are
  captured.
- `response_fields`: top-level JSON fields of the token response to
  capture. A plain string is an opaque field; an object can set
  - `kind: "jwt"` — a claims-preserving JWT phantom (below);
  - `format` — a template containing `{phantom}` exactly once, for
    clients that classify tokens by prefix.
- `request_nonce_fields`: request body fields (form or JSON) that may
  carry a phantom back to the token endpoint (refresh tokens, Codex's
  `subject_token` id-token exchange); the proxy swaps in the real value
  before forwarding, then captures the response again.
- `api.hosts`: where phantoms are redeemed. `api.header` (default
  `Authorization`) and `api.prefix` (default `Bearer ` for
  `Authorization`, empty otherwise) say where the token travels.
- `base`: only in full overrides, written by `edit --full`.

### Commands

```
hsh oauth provider ls                    # name, source (shipped / override / user), drop-ins, stale warnings
hsh oauth provider cat NAME              # merged result, with the file each layer came from
hsh oauth provider cat --shipped NAME    # the descriptor built into hsh, with its hash
hsh oauth provider edit NAME             # $VISUAL/$EDITOR on NAME.d/50-local.json (--drop-in FILE)
hsh oauth provider edit --full NAME      # full copy with "base" recorded; a skeleton for a new name
hsh oauth provider add --from-image IMG  # review descriptors an image suggests, copy accepted ones
```

`edit` validates the result after the editor exits. A running session
picks up provider changes at its next configuration reload (the
resolved providers travel in the session's `secrets.json`).

An image can suggest descriptors for providers hsh does not ship in an
`org.ifup.hsh.oauth-providers` label (a JSON object of name ->
descriptor). They are never used directly: `add --from-image` shows each
one and copies it into `oauth-providers/` only after confirmation
(`--yes` to skip the prompt, `--force` to replace an existing file).

## Phantom shapes

| Kind | Sandbox sees | For |
|---|---|---|
| opaque | `hsh_<64 hex>` | refresh tokens, API keys, any token the client treats as a string |
| `format` | the template with the opaque phantom inside, e.g. `sk-ant-oat01-hsh_<64 hex>` | Claude Code (`startsWith("sk-ant-oat")` switches it to OAuth mode) |
| `jwt` | the real token's header and payload, with the signature segment replaced by `hsh_<64 hex>` | Codex and pi, which decode `id_token`/`access_token` claims (account id, plan, `exp`) but never verify signatures |

A `jwt` field whose real value is not a JWT (Codex's id-token exchange
returns an API key under `access_token`) gets an opaque phantom. A JWT
phantom exposes the token's claims (email, account id, expiry) to the
sandbox: identity, not a credential. Because `exp` is kept, clients
that refresh proactively from the claim keep their real schedule.

The store keys every mapping by the full value the sandbox holds, so
redemption is a plain lookup of whatever follows the configured prefix.

## Mechanics

- The proxy MITMs TLS for OAuth-involved hosts even when no static
  secret is configured for them.
- **Capture**: a JSON response from a token endpoint is rewritten field
  by field; each real value is stored under a fresh phantom.
- **Redeem**: on a request to a provider's API host, a phantom in the
  configured header is replaced with the real token — only if the
  phantom was captured for **that** provider. A phantom presented to
  another provider's host passes through unresolved (and fails
  upstream). A redeemed phantom wins over a static `secrets` injection
  on the same host: the static secret is injected only when the request
  carries no phantom, so a subscription login and a static API key can
  share one host.
- **Refresh**: a phantom sent back in a `request_nonce_fields` field is
  redeemed in the request body (form or JSON, detected from the
  `Content-Type` or a leading `{`), and the rotated response is captured
  again.
- Both plain-HTTP and HTTPS flows are handled.

## Browser-redirect logins

Prefer the harness's headless login where it has one — the browser leg
then stays on whatever machine you like and nothing needs to reach the
sandbox:

```
sandbox$ codex login --device-auth   # open https://auth.openai.com/codex/device anywhere
sandbox$ claude auth login           # paste the code#state the copy-code page shows
```

A browser-redirect login (the harness runs a loopback listener and the
provider redirects the browser to it) cannot complete on its own: the
listener's loopback is inside the sandbox's private network namespace,
unreachable from the browser. Either forward the port
(`ssh -L 1455:localhost:1455 server` plus `hsh run --forward 1455`), or
replay the failed redirect:

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

## Debugging a login

The egress proxy log is the primary diagnostic - the login's HTTP traffic
all crosses it. While the session runs:

```
server$ hsh ls -q                      # find the session id
server$ podman logs -f hsh-<session>-proxy
```

What a healthy login looks like, in order:

- `CONNECT auth.openai.com:443 ... will intercept for oauth` - the
  provider reached this session's proxy. If you instead see
  `denied auth.openai.com (not in network.allow)` or
  `dns: refused auth.openai.com`, the session is running a stale image or
  config - the allowlist and the `oauth` providers must both be present.
- `POST auth.openai.com/oauth/token -> oauth token exchange (openai-codex)`
- `oauth: openai-codex exchange request: grant_type=authorization_code
  code=<12 hex>(len 96) code_verifier(len 43)
  redirect_uri=... client_id=app_...` - the request shape the provider
  received (codes and verifiers are fingerprinted, never logged; form
  and JSON bodies are both decoded).
- `oauth: openai-codex token exchange: upstream 200 OK (application/json, N bytes)`
- `oauth: captured openai-codex access_token (sandbox sees a phantom)` -
  one line per captured field.
- Later API calls: `oauth: redeemed openai-codex access_token token for chatgpt.com`.

`hsh oauth callback` prints the same code fingerprint it delivered
(`(code 9f2a41c77b1e, 96 chars)`), so the delivery and the exchange can
be correlated: the same fingerprint in the exchange means the harness
sent the code you delivered; a repeated fingerprint across two
exchanges means a retried (already consumed) code; a fresh fingerprint
that still fails means the provider rejected it on its own merits.

Failure signatures:

- `oauth: token endpoint refused the exchange: invalid_grant` (with the
  upstream status line just above it) - the provider refused the code:
  expired or already redeemed (see
  [Browser-redirect logins](#browser-redirect-logins)). If the code was
  fresh and promptly delivered, check whether the browser showed a
  **success** page instead of the expected connection-refused error:
  that means something on the browsing machine consumed the callback
  (an unfinished `pi` or Codex CLI login holding `127.0.0.1:1455`) and
  exchanged the single-use code first - kill it (`lsof -i :1455`) and
  start a fresh login.
- `token response is not JSON` / `contained none of the declared fields` -
  the descriptor's `token_endpoint` host+path does not match what the
  client actually calls; check the path against the CONNECT/POST lines
  above it, and fix it with a drop-in (`hsh oauth provider edit NAME`).
- `declared as a JWT but the value is not one` - informational; the
  field got an opaque phantom.
- `TLS handshake with client for auth.openai.com: ...` - the client does
  not trust the session CA bundle (check the `*_CA_*` env vars inside the
  workspace).
- `unknown phantom presented` - the client sent a phantom this session's
  store has no mapping for (a phantom from another session, or the
  session's `proxy/` directory was removed).
- `writing the token store ... :` - the capture could not be persisted;
  phantoms stop resolving after a restart.

`hsh oauth status` lists what each session has captured - providers,
phantom counts, and capture times, never token values:

```
server$ hsh oauth status
session 0fc8116fa399
~/.local/state/hsh/sessions/0fc8116fa399/proxy/oauth-tokens.json: 2 phantom(s)
  openai-codex:
    access_token   1  newest captured 2025-06-11T18:02:14Z
    refresh_token  1  newest captured 2025-06-11T18:02:14Z
```

`--session ID` limits it to one session; `--store PATH` reads a store
file directly.

Inside the sandbox, `curl -sS https://auth.openai.com/` checks the full
path (DNS, proxy env, CA trust) independently of the client.

## Token store

Captured tokens live in the session's own writable directory,
`$XDG_STATE_HOME/hsh/sessions/<id>/proxy/oauth-tokens.json` (directory
0700, file 0600). `hsh run` bind-mounts `sessions/<id>/proxy/`
read-write into that session's proxy container only (at
`/hsh-proxy-state`); the rest of the session directory stays read-only
to the proxy, and the workspace never sees any of it. This follows the
data-flow rule from plans/0042-remote-oauth-logins.md: a session writes
only into its own directory, and anything that has to leave it is
pulled out by a host-side process.

Captures therefore belong to one session: they survive `hsh resume`
(the harness keeps its phantoms in the same workspace container) and
are deleted with the session by `hsh rm`.

## Security model

1. **Real token responses are rewritten before the sandbox sees them.**
   The workspace only ever receives phantoms.
2. **Phantoms resolve only through their own provider's routes.** A
   phantom sent anywhere else — another host, another provider's API
   host, a third party — is an inert string. A successful login does not
   give the agent a transferable bearer token.
3. **Only user-owned files decide where tokens go.** Images and
   workspace configs name providers; descriptors come from the hsh
   binary and `$XDG_CONFIG_HOME/hsh/oauth-providers/`.

Not protected (same as the rest of hsh — use a VM for persistent
attackers or hostile providers):

- the capture store at rest (plaintext, owner-only; same as `~/.netrc`),
- a compromised host or proxy process (memory/ptrace),
- the agent *using* its login through the proxy (that is the point),
  including calling any of the provider's API hosts with it,
- the authorization code (and, for device code, the PKCE verifier),
  which passes through the sandbox during an in-sandbox login: an agent
  that is already compromised at that moment could race the harness to
  exchange it elsewhere,
- phantoms leaked to third parties (useless there, but they do reveal
  that a login exists, and JWT phantoms reveal the token's claims).

## Limitations

- Only top-level JSON fields of the token response are captured, and
  only JSON token responses are rewritten.
- Redemption covers one header per provider (`api.header`/`api.prefix`).
  Tokens sent as query parameters or in other headers are not redeemed.
- A token-bearing response the descriptor does not describe (a wrong
  path, an unexpected field) passes through unchanged; there is no
  fail-closed backstop yet.
