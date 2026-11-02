# Subscription logins for Codex and Claude Code on a remote hsh host

> **Status: Phase 0 done, Phase 1 implemented, Phase 2 not started.**
> Phases at the end. Current behavior is documented in
> internal-docs/oauth.md.

## Problem

`hsh secret` handles static API keys well. Subscription auth (ChatGPT
Plus/Pro for Codex, Claude Pro/Max for Claude Code) is OAuth: an
interactive login, short-lived access tokens, rotating refresh tokens.
On a remote host (laptop browser, `ssh server`, `hsh run`) each of these
causes friction:

| | Codex CLI | Claude Code |
|---|---|---|
| default login | browser redirect to `127.0.0.1:1455` inside the sandbox netns | browser redirect to `localhost:<random>`; falls back to a copy-code page (`platform.claude.com/oauth/code/callback`) |
| headless option | `codex login --device-auth` (device code) | copy-code paste |
| token endpoint | `auth.openai.com/oauth/token` (code exchange: form; refresh: JSON) | `platform.claude.com/v1/oauth/token` (JSON) |
| stored at | `~/.codex/auth.json` | `~/.claude/.credentials.json` |
| token shape the client inspects | `id_token` and `access_token` are JWTs whose claims (`https://api.openai.com/auth` → account id, plan) the client decodes | `sk-ant-oat01-…` / `sk-ant-ort01-…` prefixes |
| hsh image today | no `oauth` section, so a login drops **real** tokens into the sandbox | no `oauth` section, same |

Cross-cutting pain:

1. **The browser leg is on the laptop, the listener is in the sandbox.**
   `hsh oauth callback` works but needs a URL copied from the laptop
   browser and pasted into a second server shell, fast.
2. **Opaque phantoms break JWT-decoding clients.** The pi `openai-codex`
   provider already leaves `access_token` unphantomed for this reason, so
   a live access token sits in the sandbox.
3. **Logins are per workspace container.** Phantoms live in the
   client's credential file inside the container. A new session (anything
   other than `hsh resume`) means logging in again.
4. **Refresh-token rotation.** Two holders of one single-use refresh
   token (two sessions, or a laptop and a sandbox) log each other out:
   whichever refreshes first wins.
5. **No way to put a subscription login in `hsh secret`.** The store holds
   static strings, and the proxy cannot refresh.
6. **Images define OAuth endpoints.** Today an image label's `oauth`
   section says where tokens are captured and redeemed. Once logins
   become long-lived host secrets (D), letting an image decide where a
   refresh token is sent is not acceptable.

## Prior art

- **pi 1.4.0** (`@earendil-works/pi-ai/dist/auth/oauth/`) already ships
  both headless flows: OpenAI device code (`deviceauth/usercode` →
  `deviceauth/token` → `oauth/token` with
  `redirect_uri=https://auth.openai.com/deviceauth/callback`) and the
  Anthropic copy-code redirect (`platform.claude.com/oauth/code/callback`,
  paste `code#state`).
- **MorseWayne/pi-openai-codex-device-login**: the device-code flow as a
  pi extension. Its useful detail is that `deviceauth/token` returns
  `authorization_code` + `code_verifier` in JSON, and these then go to
  `oauth/token`. So the device poll response is code material, not a
  token, and capture can still happen at `oauth/token`.
- **gotgenes/pi-anthropic-auth**: shows that the access-token prefix
  `sk-ant-oat` is what clients key on to switch into OAuth mode. Its
  retro 0027 also covers why the paste input (`usesCallbackServer`) is
  required in Docker/SSH: the callback server is unreachable there.

## Provider descriptors

Everything vendor-specific (endpoints, client ids, body encodings, token
shapes, credential-file templates) is data in a provider descriptor, not
Go code. The binary holds only the generic machinery: capture, redeem,
refresh, phantom kinds, and a small template engine. A new provider or a
vendor endpoint change then needs a JSON file, not an hsh release.

### Layering

| Layer | Location | Owner |
|---|---|---|
| shipped | embedded in the hsh binary (`go:embed`), never written to disk | hsh release |
| full override / new provider | `$XDG_CONFIG_HOME/hsh/oauth-providers/NAME.json` | user |
| partial override | `$XDG_CONFIG_HOME/hsh/oauth-providers/NAME.d/*.json` | user |

Resolution for `NAME`: take the user's `NAME.json` if present, otherwise
the shipped descriptor. Then apply each `NAME.d/*.json` in lexical order
as a JSON merge patch (RFC 7396). This mirrors systemd's unit +
`unit.d/*.conf` drop-ins. A drop-in changes one field and inherits
everything else, including later release updates.

**Stale full overrides.** A full override goes stale once a release
changes the shipped file (Anthropic's move from `console.anthropic.com`
to `platform.claude.com` is the example). `hsh oauth provider edit
--full` therefore writes `"base": "sha256:<shipped descriptor>"` into the
copy, and `hsh run` and `hsh oauth provider ls` warn when the shipped
descriptor no longer matches `base`, like `systemd-delta`.

**Versioned format.** Each descriptor carries `"version": 1`. A
descriptor that needs a newer mechanism than this hsh supports is
rejected as a whole, never half-applied.

### Commands

```
hsh oauth provider ls                 # name, source (shipped / override / drop-ins), stale warnings
hsh oauth provider cat NAME           # merged result, with the file each layer came from
hsh oauth provider edit NAME          # $EDITOR on a new NAME.d/50-local.json drop-in
hsh oauth provider edit NAME --full   # copy shipped → NAME.json with "base" recorded
hsh oauth provider add --from-image IMAGE NAME
                                      # show a descriptor an image suggests; copy into
                                      # oauth-providers/ on confirmation
```

### Who may define a provider

Only the two host layers above: the hsh binary and files the user owns
under `$XDG_CONFIG_HOME/hsh`. Image labels and workspace `config.hsh`
files (an agent can write the latter) **name** providers but never
define them:

```json
"oauth": {"providers": ["openai-codex"]}
```

A config may narrow a provider (a subset of its API hosts), never widen
it. So a malicious image cannot point a token endpoint or API host at a
host it controls. An image that needs a provider hsh does not ship can
carry a suggested descriptor under a separate label, which goes through
`hsh oauth provider add --from-image`.

This replaces today's inline `oauth` definitions in labels. The pi image's
`openai` and `openai-codex` entries move into shipped descriptors. The
format changes in place: no config `version` bump, no deprecation period.
Inline definitions become a load error.

### Descriptor shape (sketch)

```json
{
  "version": 1,
  "token_endpoint": {"host": "platform.claude.com", "path": "/v1/oauth/token"},
  "request_body": "json",
  "refresh": {
    "grant_type": "refresh_token",
    "client_id": "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
    "send_fields": ["refresh_token"]
  },
  "response": {
    "expires_in": "expires_in",
    "fields": [
      {"path": "access_token", "format": "sk-ant-oat01-{phantom}"},
      {"path": "refresh_token", "format": "sk-ant-ort01-{phantom}"}
    ]
  },
  "request_nonce_fields": ["refresh_token"],
  "api": {"hosts": ["api.anthropic.com"], "header": "Authorization", "prefix": "Bearer "},
  "seed": [
    {
      "path": "/home/hsh/.claude/.credentials.json",
      "mode": "0600",
      "template": "{\"claudeAiOauth\":{\"accessToken\":\"{{.access_token}}\",\"refreshToken\":\"{{.refresh_token}}\",\"expiresAt\":{{.expires_at_millis}}}}"
    }
  ]
}
```

Templates use Go `text/template` with a fixed helper set (`phantom`,
`now`, `nowMillis`, `jwtClaim`) and the session's values for the secret
(phantoms, `expires_at`, `expires_at_millis`). There are no loops
over network calls and no scripting. Flows that need procedure (device-code
polling) are out of scope for descriptors.

`hsh run` resolves the named descriptors and hands them to the proxy the
same way it hands over resolved secrets. The proxy never reads the
descriptor directories.

## Data flow: sessions never push upward

A session writes only into its own session directory. Anything that has
to leave a session is **pulled** out by a host-side process, and anything
that has to reach a session is **pushed down** by a host-side process.
No proxy or workspace writes into `secrets.db`, another session's
directory, or other shared host state.

```
                  secrets.db  (host only, never mounted)
                    ▲       │
    promote / pull  │       │  resolve / push down
    (host process)  │       ▼  (host process)
  sessions/<id>/proxy/oauth-tokens.json    sessions/<id>/secrets.json
  written by this session's proxy (rw)     written by the host, read by the proxy (ro)
```

- **Session-owned, writable:** `sessions/<id>/proxy/`, mounted read-write
  into that session's proxy only. It holds `oauth-tokens.json`, the
  capture store from plan 0011, unchanged in format and role. Its location
  moves from the shared state directory into the session.
- **Host-owned, read-only to the session:** the rest of `sessions/<id>/`
  (`config.json`, `secrets.json`, CA), mounted read-only as today.
  `secrets.json` gains the OAuth fields below. The proxy already watches
  and reloads it.
- **Host-side movers:** `hsh oauth promote` (pull up), and the session's
  host-side companion, which is `hsh run`/`hsh resume` while attached and
  `hsh-reaper` while detached. The companion refreshes OAuth secrets and
  pushes them down into its own session's `secrets.json`. A companion
  never writes another session's directory.

Consequence: captures are per session. `hsh rm` deletes a session's
captures with it, so a login worth keeping is promoted first. The
shared-store property of today's `oauth-tokens.json` (captures outlive
the sandbox) moves to promoted secrets in `secrets.db`.

## Ideas

### A. Capture for the headless flows (cheap)

Ship `openai-codex` and `anthropic` descriptors, have
`images/codex/hsh.json` and `images/claude-code/hsh.json` name them, and
document one recipe per harness:

```
sandbox$ codex login --device-auth      # open URL + code on the laptop
sandbox$ claude auth login              # paste code#state from the copy-code page
```

Neither recipe needs a callback bridge. The browser leg stays on the
laptop and the code exchange goes through the proxy, which captures the
real tokens into the session's `proxy/oauth-tokens.json` and hands the
CLI phantoms. Before promotion the session's proxy refreshes its own
captures as it does today: the refresh token is used inside that one
session only.
A on its own breaks both clients (opaque phantoms), so it ships together
with B and C.

Caveat: in these flows the authorization code (and, for device code, the
PKCE verifier) passes through the sandbox. An agent that is already
compromised during the login could send them to an allowed host and
exchange them elsewhere first. The window is short and they are
single-use, but D + F remove the in-sandbox login for every session after
the first.

### B. Claims-preserving JWT phantoms

New response field kind `jwt`. The phantom keeps the real token's
header and payload and replaces the signature segment with
`hsh_<64 hex>` (base64url). The client decodes the claims it needs
(account id, plan, `exp`), but the value is not a valid token anywhere.
Redemption looks up the full string, the same way it works for opaque
phantoms.

- Fixes Codex (`id_token`, `access_token`) and the pi `openai-codex`
  provider, which can then phantom `access_token` too.
- The payload does expose email/account id to the sandbox. That is
  identity, not a credential.

### C. Prefix templates

Per-field `format`, e.g. `"sk-ant-oat01-{phantom}"`. The proxy strips the
template before lookup. Required for Claude Code
(`accessToken`/`refreshToken`) and for pi's anthropic provider once its
OAuth tokens are captured.

### D. Host-managed OAuth secrets (the `hsh secret` answer)

Make a subscription login a first-class secret in `secrets.db` that the
host refreshes, instead of a value captured into one container. The
store records the provider name with the tokens, so the config only
references the secret:

```json
"secrets": {
  "CLAUDE_MAX": {"from": "hsh://claude-max"}
}
```

`hosts`, `header` and `prefix` default from the provider's `api`
section. A config may narrow `hosts`, never widen them.

- **Storage.** OAuth secrets live in the plan-0009 sqlite store
  (`$XDG_CONFIG_HOME/hsh/secrets.db`), in a new table next to `secrets`:
  provider, access token, refresh token, `expires_at`, `refreshed_at`.
  `secrets.db` stays host-only and is never mounted into a container.
  `hsh secret ls` lists both kinds and never shows values.
- **Refresh happens on the host, never in a proxy.** The session's
  companion checks `expires_at` for each OAuth secret the session
  references. Within a margin of expiry, it refreshes upstream using the
  descriptor's `refresh` section and writes the rotated pair back. With
  several sessions sharing one secret, the companions coordinate through
  a sqlite `BEGIN IMMEDIATE` transaction on the secret's row: the first
  one refreshes, and the others see the fresh `expires_at` and skip. This
  removes pain 4: exactly one process spends the refresh token.
- **Push down.** Each companion re-resolves its own session's
  `secrets.json` from `secrets.db` whenever the secret's `refreshed_at`
  changes (the same path the config watcher already uses). For an OAuth
  secret, `secrets.json` carries the **access token and `expires_at`
  only**. The refresh token never leaves `secrets.db` and the host
  process, so no proxy, writable session directory or container ever
  holds the long-lived credential.
- **Per-session phantoms.** At launch `hsh run` generates phantoms for
  each OAuth secret slot (`access_token`, `refresh_token`, `id_token`)
  and writes the phantom → slot mapping into `secrets.json`. They stay
  stable for the life of the session (`hsh resume` keeps them), and a
  refresh only changes the value behind the slot. This removes pain 3:
  every session gets a working login without logging in. A leaked
  phantom also identifies the session it came from.
- **Client refreshes are answered locally.** A refresh the client sends
  (phantom refresh token to the token endpoint) never goes upstream: the
  proxy returns the same phantoms and an `expires_in` that mirrors the
  real token's remaining lifetime from `secrets.json`. A client that
  refreshes too often is harmless.
- **A local answer must never fail.** Claude Code wipes its credentials
  file on a failed refresh (Phase 0), so the proxy never answers a local
  refresh with `invalid_grant` or similar.
- **401 storms.** Both clients refresh on any upstream 401 and retry
  (Phase 0). If the real access token is bad, a local answer followed by
  another 401 would loop. So the proxy rate-limits local answers per slot
  (e.g. one per 30 s; beyond that it answers 503 with `Retry-After`). On
  the first 401 after a local answer, it writes a `refresh-wanted` marker
  into its own `proxy/` directory. The companion pulls that marker and
  performs a real refresh, so it stays within the data-flow rule.
- **Expiry UX.** If the host refresh fails (`invalid_grant`, revoked),
  the companion records a monitor notice. The proxy answers API calls on
  that secret with a clear 401 body (`hsh: login claude-max expired; log
  in inside a sandbox and run hsh oauth promote`). If no companion is
  running (crashed), the access token simply ages out and the same notice
  appears at the next attach.

### E. Getting a login into D: promote a sandbox capture

`hsh oauth promote [--session ID] PROVIDER NAME` is a host-side pull. It
reads the newest capture for `PROVIDER` from the session's
`proxy/oauth-tokens.json` and copies it up into `secrets.db` as the
named, host-refreshed secret, recording the provider name with it. The
login runs once, inside any sandbox, through the CLI's own headless
flow. hsh adds no vendor login code and no per-vendor credential
parsers: capture is driven entirely by the descriptor.

Promotion has to **move** the login, not copy it. Otherwise the session's
proxy and the host both hold the same refresh token (pain 4). So after
the copy, promote pushes down a binding into the session's
`secrets.json`: the session's captured phantoms now map to the secret's
slots. On reload the proxy redeems those phantoms from the secret,
answers their refreshes locally, and drops the captured refresh token
from its own `oauth-tokens.json`. Each file has exactly one writer, so
there is no write race.

One race remains: if the proxy refreshes the capture at the moment of
promotion, it rotates the token promote just copied. Promote therefore
verifies the secret with an immediate host-side refresh. On
`invalid_grant` it re-reads the session's (now newer) capture and retries
once.

Considered and skipped: importing a laptop credential file
(`hsh secret import --format codex`), a native `hsh secret login` that
runs vendor flows host-side, and `claude setup-token` as a static secret.
The first two need provider-specific parsers or flows in the hsh command
line. The third is a narrow, Claude-only stopgap.

### F. Seed the sandbox with phantom credentials

With D, nobody needs to log in again inside the sandbox. At launch `hsh
run` (host side, pushing down) renders the provider descriptor's `seed`
templates with the session's phantoms and `expires_at`, and writes them
into the workspace:

- Claude Code: `~/.claude/.credentials.json` with the real token's
  `expiresAt`. When the client refreshes, the proxy answers locally.
- Codex: `~/.codex/auth.json` with JWT phantoms (B) and a recent
  `last_refresh`.
- pi: an `auth.json` `oauth` entry.

A secret whose descriptor has no `seed` is still redeemed. The client
just has to hold phantoms some other way (e.g. a resumed workspace).
`secrets.<NAME>.seed: false` opts out.

### G. Make browser-redirect flows less manual

These stay useful for harnesses without a headless mode:

- Recipe: `ssh -L 1455:localhost:1455 server` + `hsh run --forward 1455`
  (exists, plan 0030). The redirect then completes end to end with no
  paste. Document it for Codex.
- A monitor palette action "paste OAuth callback URL" (already in
  TODO.md), so the paste happens in the attached terminal instead of a
  second server shell.
- Auto-detect: the monitor notices a new loopback listener on 1455 and
  offers `forward`. Low priority.

## Phases

0. **Phase 0, prototype the client assumptions (gate).** Before any
   other work, check that Codex accepts claims-preserving JWT phantoms
   (B) and Claude Code accepts `sk-ant-oat01-hsh_…` phantoms (C). Use
   only what exists today: log in on the host to get real tokens, and
   store the real access token as a static `hsh://` secret injected as
   `Authorization: Bearer` for `chatgpt.com` / `api.anthropic.com`.
   Then hand-write the sandbox's credential file with phantom-shaped
   values:
   - Codex `~/.codex/auth.json`: real `id_token`/`access_token` header
     and payload with the signature replaced by an `hsh_` marker, and
     `refresh_token=hsh_<hex>`.
   - Claude `~/.claude/.credentials.json`: `sk-ant-oat01-hsh_<hex>` /
   `sk-ant-ort01-hsh_<hex>`.

   Pass: `codex exec`, `claude -p` and `claude auth status` work, and
   startup calls (profile, plan lookups) succeed. Also force an expiry
   (old `last_refresh`, past `expiresAt`) and record what each client
   sends to its token endpoint and how it reacts when the phantom
   refresh fails upstream (retry, log out, delete the file). That
   behaviour shapes D's local refresh answers.

   Record the findings in this plan. If either client rejects its
   phantoms, stop and redesign B/C (and possibly D and F) before going
   further. **Done; both clients accept their phantoms. See
   [Phase 0 results](#phase-0-results).**
1. **Phase 1, make in-sandbox headless logins safe**: provider
   descriptors (shipped + user layers, `hsh oauth provider` commands,
   labels name providers), `oauth-tokens.json` moved into the session's
   writable `proxy/` directory, + A + B + C. Shipped `openai`, `openai-codex`,
   `anthropic` descriptors; image labels updated; functional tests with
   the fake OAuth server issuing JWT and `sk-ant-` shaped tokens; docs
   recipes (device-auth, copy-code, `--forward 1455`).
   **Implemented.** Notes on how it landed:
   - `internal/oauthprov`: descriptors, the shipped set
     (`providers/*.json`, `go:embed`), user layers, RFC 7396 drop-ins,
     `base` staleness warnings, and `version` checks. `hsh oauth provider
     ls | cat [--shipped] | edit [--full] [--drop-in FILE] | add
     --from-image`. Image suggestions live in an
     `org.ifup.hsh.oauth-providers` label.
   - Config: `"oauth": {"providers": [...], "api_hosts": {name: [subset]}}`.
     Inline definitions are a load error with a pointer to the new
     shape. The v1 schema snapshot was regenerated in place (O5: no
     version bump).
   - `hsh run` resolves the named descriptors into `secrets.json`
     (`oauth` array), so the proxy never reads the descriptor
     directories, and provider edits reach a running session on the
     next reload.
   - Instead of stripping templates before lookup, the store keys every
     mapping by the **full** value the sandbox holds (formatted string or
     whole JWT). Redemption looks up whatever follows the configured
     prefix.
   - Added beyond the plan: redemption is scoped to the provider that
     captured the phantom, and the header/prefix come from the
     descriptor (`api.header`/`api.prefix`).
   - Fixed an existing bug: request-body redemption used
     `http.DetectContentType`, which never reports JSON, so JSON refresh
     bodies (Codex's refresh, Claude's) were never redeemed. JSON is now
     detected from `Content-Type` or a leading `{`.
   - `oauth-tokens.json` lives at `sessions/<id>/proxy/` (mounted
     read-write at `/hsh-proxy-state`). `hsh oauth status` reports per
     session (`--session`, `--store`). The proxy's old default store next
     to the monitor database is gone.
   - Functional test step 9 covers opaque, formatted (with a JSON refresh)
     and JWT providers from user descriptors, the per-session store,
     `hsh oauth status` and `hsh oauth provider ls`. It passes in both
     the container and gVisor runtimes.
   - First live Claude login (2026-11-02): the token response came back
     gzip-compressed, because Claude sends `Accept-Encoding: gzip,
     deflate, br`, the proxy forwarded it, and Go only decompresses
     transparently when it set the header itself. The proxy logged "not
     JSON" and passed the body through, so **real tokens reached that
     sandbox**. Fixed: token exchanges ask for `gzip` and decode it in the
     proxy. Added the fail-closed backstop: a 2xx token response that is
     not JSON, uses another encoding, or carries an uncaptured token-like
     field is answered with 502. Unit tests reproduce Claude's headers.
   - internal-docs/sandboxed-oauth.md (the original, never-implemented
     design) was removed. internal-docs/oauth.md, config.md, images.md
     and architecture.md describe the current behavior.
2. **Phase 2, logins become secrets**: D + E + F. Log in once in any
   sandbox, promote, and every later session works without a login. The
   host companion refreshes and pushes access tokens down. No proxy or
   sandbox ever holds the refresh token.

## Phase 0 results

Tested offline: each CLI ran with an isolated `HOME`/`CODEX_HOME`/
`CLAUDE_CONFIG_DIR` against a local fake server that records every
request. No real account was used. Versions: Codex 0.166.0 (musl release
binary; source read in `openai/codex` `codex-rs/login`), Claude Code
2.1.281 (binary inspected).

Harness: `plans/prototype/oauth-phantoms/run.sh` (set `CODEX=` to reuse
a binary, otherwise it downloads the latest release).

- `fake.py`: records method, path, auth headers and body.
  `/oauth/token` answers with claims-preserving JWT phantoms (signature
  `hsh_aaaa…`) or, with `FAKE_TOKEN_MODE=invalid_grant`, with
  `invalid_grant`. With `FAKE_API_MODE=ok` everything except
  `/responses` and `/messages` answers 200. Otherwise everything is 401.
- `codex-setup.py`: writes `auth.json` with `id_token` = `access_token` =
  a claims-preserving phantom (signature `hsh_cccc…`, claims: email, plan
  `plus`, `chatgpt_account_id`), `refresh_token=hsh_dddd…`, and a
  `config.toml` with a `requires_openai_auth` provider and
  `chatgpt_base_url` pointed at the fake. `CODEX_REFRESH_TOKEN_URL_OVERRIDE`
  points refresh at the fake.
- `claude-setup.py`: writes `.credentials.json` with
  `sk-ant-oat01-hsh_eeee…`, `sk-ant-ort01-hsh_ffff…`, `expiresAt` = now
  + 1 h, `scopes`, `subscriptionType: max`. `ANTHROPIC_BASE_URL` points
  inference at the fake.
- `summarize.py`: counts requests per path and per credential (the JWT
  signature tells original phantoms from refreshed ones).

| Scenario | Observed |
|---|---|
| `codex-ok` (valid exp, API 200 except `/responses` 401) | `login status`: logged in. 14 startup calls with `Bearer <hsh_cccc phantom>` + `chatgpt-account-id`. `/responses` 401 → 1 refresh (JSON, `unauthorized_recovery`) → 1 retry with the refreshed `hsh_aaaa` phantom → error. `auth.json` rewritten with the refreshed phantoms. |
| `codex-invalid-grant` (exp 10 min in the past) | Proactive refresh (`proactive_expiry`) ×4, all `invalid_grant` → "please log out and sign in again". `auth.json` byte-identical afterwards. `login status` still logged in. |
| `codex-401-storm` (every API call 401) | `wham/settings/user` 401 → refresh → retry loop: 674 refreshes and 2,018 settings calls before the 40 s timeout. |
| `claude` (valid `expiresAt`, `/messages` 401) | `auth status`: logged in, `authMethod: claude.ai`, `subscriptionType: max`. `/v1/messages` with `Bearer sk-ant-oat01-hsh_…` and `anthropic-beta: claude-code-20250219,oauth-2025-04-20,…`. The 401 → refresh against the real `platform.claude.com` (made-up token) → failure → credentials file wiped (`accessToken: ""`, `refreshToken: ""`, `expiresAt: 0`). |

### Codex: B works

- `decode_jwt_payload` splits `header.payload.signature`, requires three
  non-empty segments, and base64-decodes only the payload. **No signature
  check.** An `hsh_<64 hex>` signature segment passes.
- With `id_token` and `access_token` both claims-preserving phantoms,
  `codex login status` reports "Logged in using ChatGPT". Every
  `chatgpt.com/backend-api/*` call (including `codex/responses`) carries
  `Authorization: Bearer <phantom>` plus `chatgpt-account-id` taken from
  the claims.
- Proactive refresh is driven by the access token's `exp` claim
  (`refresh_reason=proactive_expiry`), with an 8-day `last_refresh`
  fallback. Claims-preserving phantoms therefore mirror the real expiry
  automatically.
- The refresh request is **JSON** (`client_id`, `grant_type`,
  `refresh_reason`, `refresh_token`). The code exchange is **form**. Both
  go to `/oauth/token`, so the descriptor needs `request_body: auto`
  (the existing proxy already branches on Content-Type).
- Phantoms returned by the token endpoint are accepted and used at once.
- On any 401 Codex refreshes (`unauthorized_recovery`) and retries. For
  `responses` it is one refresh and one retry, then an error. A startup
  endpoint that kept answering 401 produced 674–1,110 refreshes in 40 s
  across runs, hence the D rule on 401 storms.
- `invalid_grant` on refresh: error message, and `auth.json` is left
  untouched.
- Browser login only (not `--device-auth`): after login Codex posts
  `id_token` as `subject_token` in a token-exchange to `/oauth/token` to
  mint an API key, and tolerates failure. The `openai-codex` descriptor
  lists `subject_token` in `request_nonce_fields` and captures the
  response's `access_token` (an API key, not a JWT). So a field marked
  `jwt` falls back to an opaque phantom when the real value is not a JWT.

### Claude Code: C works

- The only token-shape check is `isAnthropicOAuthToken = startsWith("sk-ant-oat")`.
- A `.credentials.json` with `sk-ant-oat01-hsh_…` /
  `sk-ant-ort01-hsh_…`, `expiresAt`, `scopes` (`user:inference` is
  required for inference) and `subscriptionType` gives `claude auth
  status` → logged in, `authMethod: claude.ai`. `/v1/messages` carries
  `Authorization: Bearer sk-ant-oat01-hsh_…` and `anthropic-beta:
  oauth-2025-04-20,…`.
- Email and org come from `oauthAccount` in `.claude.json`, not from the
  credentials file. They are cosmetic (`null` without it), and the seed
  may write them.
- **On a failed refresh, Claude wipes the credentials file**
  (`accessToken: ""`, `refreshToken: ""`, `expiresAt: 0`), and also on
  `invalid_grant` (`tengu_oauth_refresh_token_cleared_on_disk`). Hence the
  D rule that a local answer never fails.
- `ANTHROPIC_BASE_URL` redirects inference but not OAuth.
  `CLAUDE_CODE_CUSTOM_OAUTH_URL` accepts only a hard-coded allowlist. So
  the refresh request shape and acceptance of a locally answered refresh
  could not be observed offline. Both are covered in Phase 1 by the
  functional test through the hsh proxy's TLS interception. The expected
  shape (from pi's implementation) is JSON `grant_type=refresh_token`,
  `refresh_token`, `client_id`.

## Verification

- Unit, descriptors: shipped-only, full override, drop-ins in lexical
  order, drop-in on top of a full override. A stale `base` warns. A newer
  `version` is rejected. A label that defines (rather than names) a
  provider is rejected. Narrowing hosts works and widening fails.
- Unit, phantoms: a JWT phantom keeps header and payload byte for byte
  and redeems by full string. A `jwt` field whose real value is not a JWT
  gets an opaque phantom. Local refresh answers are rate-limited per
  slot and never return an OAuth error. Prefix template round trip. Stable-phantom
  slot lookup survives a refresh. A client refresh is answered locally
  with no upstream call and an `expires_in` that mirrors the real
  expiry. Concurrent refreshes from two companions against one database
  spend the refresh token once. A promoted secret's `secrets.json` entry
  contains the access token and never the refresh token. Promote's
  binding makes the proxy drop its captured refresh token. Seed templates
  render and never contain real values.
- Unit, data flow: the proxy writes only under `sessions/<id>/proxy/`.
  The session's other files and `secrets.db` are never opened for
  writing from a container.
- Functional (fake provider in `test/`): device-code and copy-code
  logins in a sandbox store only phantoms; API calls get the real token;
  `hsh oauth promote` → new session → seeded credential file → API works
  without login; forced expiry → the host refreshes upstream once for two
  sessions and both proxies pick up the new access token; promoting
  while the session's proxy refreshes recovers via the retry; revoked
  refresh → clear 401 body; a user drop-in changing the
  token endpoint takes effect without rebuilding hsh.
- Claude refresh through the proxy (not observable offline in Phase 0).
  The functional test cannot point the real Claude binary at a fake
  provider: its token URL is hard-coded and `CLAUDE_CODE_CUSTOM_OAUTH_URL`
  is allowlisted. Phase 1 covers the proxy side with the fake provider:
  a JSON refresh carrying an `sk-ant-ort01-hsh_…` phantom reaches
  upstream with the real token, and the rotated response comes back as
  formatted phantoms. Claude itself accepting the refreshed phantom is
  part of the manual check below.
- Manual: real `codex login --device-auth` and `claude auth login` over
  SSH against real accounts; grep the workspace for `eyJ…` signatures and
  `sk-ant-o[ar]t01-` values that are not `hsh_`-marked.

## Decisions

- **O1/O2: client token checks.** Does Codex validate JWT signatures or
  only decode them? Does Claude Code check more than the `sk-ant-oat`
  prefix? Answered by the Phase 0 prototype before any other work. If
  either client rejects its phantoms, this plan gets redesigned.
- **O3: expiry reported to the client.** The proxy mirrors the real
  expiry. A client that refreshes too often is fine, because local
  answers cost nothing upstream (see D).
- **O4: storage.** `oauth-tokens.json` stays as the per-session capture
  store, moved into the session's writable `proxy/` directory. Promoted
  OAuth secrets live in `secrets.db`, which is never mounted into a
  container. Sessions write only into their own directory. Host
  processes pull from it (`hsh oauth promote`) and push down into it
  (`secrets.json`), as described in
  [Data flow](#data-flow-sessions-never-push-upward).
- **O5: label migration.** None. The `oauth` label format changes in
  place. With a single user, a migration path costs more than it saves.
- **O6: ToS.** Not a concern. A host-side refresher protects the user's
  own credentials (and the vendor) from a credential-stealing package
  running in the sandbox.

## Future work

- **Apply the data-flow rule to the rest of the proxy mounts.** Today
  every proxy gets the whole state directory read-write at `/hsh-state`.
  That includes every session's `secrets.json` and `ca.key` and the
  shared `monitor.db`, which the proxy writes directly. Under the rule,
  each proxy gets only its own session directory (read-only) plus its
  `proxy/` directory (read-write). Shared databases would be fed by
  host-side copies out of the session directory.

- **Network policies on the same layering.** The policy-files idea in
  TODO.md (github.com, the Go module proxy, …) wants the same structure:
  shipped descriptors in the binary, `$XDG_CONFIG_HOME/hsh/<kind>/NAME.json`
  overrides, `NAME.d/*.json` drop-ins, and labels that name rather than
  define. Build the layering as a generic package
  (`internal/descriptors`, keyed by kind) so policies reuse it.
- **Static secrets bound to hosts.** A label can already declare
  `"from": "hsh://anthropic-api-key", "hosts": ["evil.example"]`. Record
  the approved hosts when a secret is added, and let configs narrow but
  not widen them, the same rule D applies to OAuth secrets.
