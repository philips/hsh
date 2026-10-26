# hsh architecture

hsh runs an interactive shell inside a rootless podman sandbox with
controlled egress. It is a single static Go binary that plays three roles:

| Mode | Runs | Purpose |
|---|---|---|
| `hsh run` | on the host | loads config, creates the pod and containers, attaches to the workspace |
| `hsh proxy` | proxy container, inside the pod | egress proxy: network policy (https/allowlist/none), TLS interception, secret injection |
| `hsh workspace` / `hsh workspace-inner` | workspace container, inside the pod | network isolation setup, then the user's shell |

Host-side subcommands manage state around the sandbox: `hsh secret`
(the secret store below), `hsh proxy-denied`/`proxy-auto-allow`/`proxy-watch`
(denial review), and `hsh ls`/`hsh rm` (sandbox lifecycle).

The same binary is bind-mounted read-only at `/hsh` in both containers.

## Pod layout

`hsh run` creates one podman pod with two containers:

```
host
└── pod (shared network namespace, pasta networking, internet access)
    ├── proxy container    (hsh proxy, listens on 10.77.0.1:8080)
    └── workspace container (hsh workspace → user shell)
        └── inner network namespace (created by `hsh workspace-inner`)
            └── user shell + tools (no default route)
```

The pod's shared network namespace is what lets the workspace reach the
proxy. Pods do not share PID namespaces, so the workspace cannot see or
signal the proxy's processes.

## Network isolation

The workspace container starts in the pod's network namespace with
`NET_ADMIN` and `SYS_ADMIN` capabilities. Its entrypoint
(`hsh workspace`):

1. creates a veth pair (`hsh0`/`hsh1`) in the pod network namespace and
   configures `hsh0` with `10.77.0.1/24`
2. re-executes itself as `hsh workspace-inner`, which calls
   `unshare(CLONE_NEWNET)` to move into a fresh, empty network namespace
3. moves `hsh1` into the child's namespace (by PID, via netlink)
4. drops all of its own capabilities
5. signals the child, which configures `hsh1` with `10.77.0.2/24` and brings
   up loopback

The child namespace has exactly one route: `10.77.0.0/24 dev hsh1`. There is
no default route, so the workspace cannot reach the internet, the host, or
any other network directly. Its `/etc/resolv.conf` points at the proxy
(`10.77.0.1`), where a DNS forwarder runs (see below), so names resolve only
when allowlisted — but resolved names still cannot be reached directly.

The child then:

- installs the proxy CA into `/etc/ssl/certs/ca-certificates.crt`
- creates the home directory, strips setuid/setgid bits from the container
  filesystem (excluding the workspace mount)
- drops all capabilities (`capset` to zero) and sets `PR_SET_NO_NEW_PRIVS`
- `chdir`s into the workspace and `exec`s the configured shell

The shell runs as container uid 0, which rootless podman maps to the invoking
host user, so files written to mounted directories are owned by the host
user. With zero effective capabilities and `NO_NEW_PRIVS` it cannot modify
routes, create namespaces, or regain privileges through setuid binaries.

The workspace parent process (container PID 1) also drops its capabilities
after moving the veth, so it cannot be ptraced for privileges either.

## Egress proxy

The proxy listens on `10.77.0.1:8080` — the only address the workspace can
reach. The workspace shell gets `http_proxy`/`https_proxy`/`all_proxy`
(plus uppercase variants) pointing at it, and `no_proxy` for loopback and
the veth addresses.

Every request is checked against `network.allow`:

- **Denied**: the proxy answers `407 Proxy Authentication Required` with a
  `Proxy-Authenticate: Hsh realm="egress-allowlist"` header (RFC 7235)
  and records the host in the denial database. For `CONNECT` this surfaces
  in curl as `CONNECT tunnel failed, response 407`.
- **Allowed, no secret configured for the host**: the proxy tunnels the
  connection (`CONNECT` → `200 Connection Established`, then a raw TCP
  splice). TLS is end-to-end between the workspace and the upstream; any
  protocol works.
- **Allowed, secret configured for the host**: the proxy intercepts.
  `CONNECT` is answered with `200`, the proxy terminates the client's TLS
  with a per-host certificate signed by the run's CA, reads the HTTP
  request, injects the configured headers, and forwards the request
  upstream over a fresh verified TLS connection. Responses stream back.

Plain-HTTP proxy requests (absolute URIs) are forwarded the same way, with
injection applied when the host matches a secret.

Host matching is case-insensitive, ignores a trailing dot, and supports
`*.example.com` wildcards (matching one or more leading labels).

### DNS

The workspace's `/etc/resolv.conf` points at the proxy (`10.77.0.1`), where
a DNS forwarder (UDP and TCP 53) runs alongside the HTTP proxy. Query names
are matched against `network.allow` with the same rules as HTTP: allowed
names are forwarded to the pod's upstream resolvers (from the proxy
container's `/etc/resolv.conf`), everything else gets `REFUSED`.

Because the workspace has no route beyond the veth, resolved names still
cannot be reached directly — local DNS feeds tools that resolve before
connecting through the proxy, and debugging (`getent`, `nslookup`). The
policy matters: an unrestricted resolver would let the workspace exfiltrate
data via DNS tunneling without ever opening a TCP connection the proxy
could check.

DNS denials are recorded in a separate `dns_denied` table in the same
denial database. They do not appear in the default `hsh proxy-denied`
listing and are not auto-allowed, so noisy lookups (telemetry domains, PTR
queries) cannot pollute the HTTP review flow; `hsh proxy-denied --dns`
lists them. When `proxy-auto-allow` allows a host, matching `dns_denied`
rows are marked allowed as well.

### Denial database

Every denial is recorded in a SQLite database (modernc.org/sqlite — pure Go,
so the `CGO_ENABLED=0` static build is unaffected) at
`$XDG_STATE_HOME/hsh/denied.db` (default `~/.local/state/hsh/denied.db`,
override with `hsh run --denied-db PATH`). The database is opened in WAL
mode with a busy timeout, so the proxy (writer) and the CLI (reader) can use
it concurrently, including across simultaneous sessions.

```sql
CREATE TABLE IF NOT EXISTS denied (
    host       TEXT PRIMARY KEY,
    target     TEXT NOT NULL,   -- host:port as requested
    count      INTEGER NOT NULL,
    first_seen TEXT NOT NULL,   -- RFC 3339, UTC
    last_seen  TEXT NOT NULL,
    allowed    INTEGER NOT NULL DEFAULT 0,
    allowed_at TEXT
);
```

Recording is an upsert on `host` (count incremented, `last_seen` bumped);
`allowed` marks hosts already handled. There are two tables: `denied` for
HTTP egress denials (the review list behind `proxy-denied` and
`proxy-auto-allow`) and `dns_denied` for DNS query denials (audit only, see
the DNS section above). The state directory is bind-mounted read-write into
the proxy container only — the workspace never sees it, so the agent
cannot erase or forge its own denial record. Database errors are logged
and never change the response: a denial is a denial with or without the
log.

Three host-side subcommands work with the database:

```
hsh proxy-denied [--db PATH] [--all]          list hosts awaiting review
hsh proxy-auto-allow [--config PATH] [--db PATH]
hsh proxy-watch [--config PATH] [--db PATH] [--all]
```

`proxy-denied` prints pending rows (`allowed = 0`) as a table (host,
target, count, first/last seen, sorted by count); `--all` includes
already-allowed hosts. `proxy-auto-allow` adds every pending host to the
config file's `network.allow`, prints `added <host>` for each, prints
`already allowed <host>` for hosts already present, marks the rows allowed
so they leave the review list, and prints a summary — trust on first use:
the first attempt is denied and recorded, review allows it. The config
file is rewritten as canonical JSON (2-space indent, keys sorted); only
`network.allow` changes semantically.

`proxy-watch` is the interactive counterpart (bubbletea/bubbles TUI,
requires a terminal): a multi-select list of pending denials, most
recently requested first, refreshed live every 2s. Keys: `space` select,
`a`/`n` select/clear all (ignoring any active filter), `/` filter,
`enter` approve the selected hosts (added to `network.allow`, marked
allowed — running proxies hot-reload the config within ~1s), `i` ignore
them (marked handled without allowing: they leave the review list
permanently), `t` toggle a view of ignored hosts (dimmed,
non-selectable), `r` refresh, `?` help, `q`/`esc`/`ctrl+c` quit. To tell
ignored rows apart from approved ones the schema has an `ignored` column
(migrated in on open for older databases). DNS denials stay audit-only
with `proxy-denied --dns`.

## Secret injection

Secrets are declared in the config as `secrets.<NAME>` with:

- `from`: a reference URL — `env://VAR` (host environment variable,
  also written as a bare `VAR`), or `hsh://NAME` (the hsh secret store;
  see below). Unknown schemes fail at config load.
- `hosts`: hosts the secret may be injected for
- `header` (optional): header name; defaults to the secret name with `_`
  replaced by `-` (e.g. `API_KEY` → `Api-Key`)
- `prefix` (optional): prepended verbatim to the value (e.g. `"Bearer "`)

The flow keeps the secret out of the workspace entirely:

1. `hsh run` resolves the references (`os.Getenv` for `env://`, the
   secret store for `hsh://`) and writes the values
   to `secrets.json` in a temporary directory (mode 0600)
2. the temporary directory is bind-mounted read-only into the **proxy
   container only** — the workspace container never sees it
3. the proxy injects `header: prefix+value` into requests whose host matches
   `hosts`, replacing any value the workspace sent

The workspace cannot read the proxy container's files (different container,
private PID namespace, root-owned 0600 file), cannot redirect an injected
request to another host (the proxy dials the `CONNECT` target itself), and
the proxy logs header names but never values.

### The hsh secret store

`hsh secret add/rm/ls` manage a first-party store so users stop
hand-editing configs or exporting keys into their shell environment:

- a SQLite database at `$XDG_CONFIG_HOME/hsh/secrets.db` (directory
  `0700`, file `0600`, plaintext values — the same trust level as
  `~/.netrc`; a first-use note warns about it)
- `secret add NAME` prompts with echo disabled (or `--value-stdin` for
  scripting); there is deliberately no `--value` flag, since values on
  the command line leak into shell history and the process list
- `secret add --host/--header/--prefix` also writes the
  `secrets.<NAME>` config entry with `from: "hsh://NAME"`
  (RawMessage-preserving rewrite, like `proxy-auto-allow`)
- `secret rm` warns when the config still references the entry but never
  edits the config; `secret ls` prints names and timestamps, never values
- only the host-side `hsh run` process and the `secret` subcommands touch
  the store; the store lives on the host and is never mounted into the
  sandbox. A missing entry behaves exactly like an unset environment
  variable: a warning, and the secret is skipped.

The scheme-based `from` makes further backends (OS keyrings, `dotenv`,
password managers) a scheme-registration addition without changing the
config format or the proxy; the model is nono.sh's credential injection
(https://nono.sh/docs/cli/features/credential-injection).

## TLS interception

`hsh run` generates a fresh ECDSA P-256 CA per run; with `--resume` it is
persisted in the session state (`$XDG_STATE_HOME/hsh/sessions/<hash>/`) so
the CA the workspace trusts stays stable across launches, and it is never
written anywhere else. The CA certificate is bind-mounted into the
workspace (which appends it to the system trust bundle and exports
`SSL_CERT_FILE`, `GIT_SSL_CAINFO`, `NODE_EXTRA_CA_CERTS`,
`CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE`); the CA private key is mounted
into the proxy container only. The proxy signs per-host leaf certificates
on demand (cached per host).

Upstream TLS is verified against the host's CA bundle (bind-mounted into the
proxy container, falling back to the image's bundle).

Interception is required for secret hosts and is a deliberate design
decision, not a gap: injecting a header into an HTTPS request means
terminating TLS, and the secret must never enter the workspace. Hosts
without secrets are tunneled end-to-end instead. The known costs are that
the proxy can read and modify all traffic to intercepted hosts, non-HTTP
protocols to those hosts break, and clients that use their own CA bundle
need a per-client CA variable (see docs/sandboxed-oauth.md).

## Configuration hot reload

The proxy polls the config file and the resolved-secrets file once per
second and swaps its in-memory state when either changes. `hsh run`
likewise watches the config file and rewrites `secrets.json` when it
changes, so `network.allow` and secret values/hosts update without
restarting the sandbox. Both files are reached through **directory**
bind mounts so that replacing the file (editors and `sed -i` rename files)
is picked up.

Mounts, image, and shell changes require a restart.

## Lifecycle and cleanup

- Progress information (pod creation, workspace start, config reloads) is
  suppressed by default so the sandbox starts with a clean terminal prompt;
  pass `--verbose` (or set `HSH_VERBOSE=1`) to see it on stderr.
  Warnings and errors are always printed.
- The pod is named `hsh-<random>`; containers are `<pod>-proxy` and
  `<pod>-ws`. Every hsh pod carries the label
  `hsh.workspace=<first mount source>`, so the workspace a sandbox
  belongs to is always discoverable.
- `hsh run` attaches to the workspace container (`podman run -it`, or
  `-i` when stdin is not a terminal) and propagates its exit code.
- On exit, SIGINT, or SIGTERM, the pod is removed (`podman pod rm -f`) and
  the temporary directory deleted.
- Proxy logs: `podman logs <pod>-proxy` while the session is running.

A killed or crashed `hsh` does not get to remove its pod, so sandboxes
can be left behind. `hsh ls` and `hsh rm` find and clean those up; both
operate on pods whose name contains `hsh-` and follow the flags and
semantics of `podman pod ls` / `podman pod rm`:

```
hsh ls                                    # table with an extra WORKSPACE column
hsh ls -q --sort name                     # quiet, sorted
hsh ls --workspace ~/repo                 # the sandbox for one workspace
hsh rm hsh-53bd3fc7e5ba                   # remove a leftover (refuses if running)
hsh rm -f hsh-53bd3fc7e5ba                # stop running containers, then remove
hsh rm ~/repo                             # remove via the workspace path
hsh rm --all -f                           # full cleanup
```

`hsh rm` passes the usual podman flags through (`-f/--force`,
`-t/--time`, `-i/--ignore`, `-l/--latest`, `-a/--all`). A sandbox with
running containers is refused without `--force`, exactly like
`podman pod rm`. Removing a sandbox also removes its session state
directory (`$XDG_STATE_HOME/hsh/sessions/<hash>/`), and `rm --all`
sweeps orphaned session state directories whose pod is already gone.
Pods that are not hsh sandboxes are never touched.

## Sandbox reuse: `hsh --resume`

By default every launch creates a fresh pod (random name) and removes it on
exit. With `--resume`, the sandbox is keyed by the workspace directory (the
first mount's source) and persists across launches:

- The pod is named `hsh-<sha256(workspace)[:12]>`; find it with
  `hsh ls --workspace <path>` or
  `podman pod ls --filter label=hsh.workspace=<path>`.
- On exit the pod is left in place (the workspace container stops, the
  proxy keeps running). A later `--resume` re-enters the **same
  container** — its filesystem persists, so state inside the workspace
  container (pi's `~/.pi` sign-in and sessions, packages installed into
  the container, files in its home) survives across launches.
- Per-sandbox state (the MITM CA and resolved secrets) lives in
  `$XDG_STATE_HOME/hsh/sessions/<hash>/` and is reused, so the CA the
  workspace trusts stays stable across launches.
- On re-entry the workspace entrypoint deletes the stale veth pair left
  in the pod network namespace and creates a fresh one; the proxy's
  listeners keep working across this.
- Secrets are re-resolved from the host environment on every launch and
  pushed to the running proxy via its hot-reloaded `secrets.json`.
- `--fresh` discards the existing pod and starts a new one for the same
  workspace (the CA and session state are kept). Two concurrent
  `--resume` sessions for the same workspace are refused — exit the
  running one first.

A reused sandbox accumulates container state; use `--fresh` (or
`hsh rm -f <sandbox>` followed by a fresh `--resume`) to reset it.

## Why podman

hsh uses podman because it is the only container runtime that works
rootless, without a central daemon, and with the same CLI as Docker. It is
the only runtime that can run the hsh sandbox without requiring root
or a system-wide daemon.

## Why shell out to podman

hsh uses the `podman` CLI (shelling out) instead of the Go bindings
(`github.com/containers/podman/v5/bindings`) or the internal `libpod`
library. The CLI is the stable contract: it is guaranteed to match the
installed podman version, adds zero dependencies, and inherits podman's
rootless handling, storage, and image management for free. The Go
bindings require a running `podman system service` daemon (REST over a
Unix socket) and drag a massive module graph into the build; `libpod` is
podman's internal core and is not a supported public API.

Shelling out is also the standard for podman tooling (podman-compose,
buildah, skopeo) and is the right fit for hsh's simple usage: exit
codes, pod IDs, and a handful of operations (pod create, container
create/start, pod rm, pod ls).

## Why not Docker

Docker requires a central daemon (`dockerd`) running as root, and the
Docker CLI (`docker`) talks to it over a Unix socket. This means that
any user who can talk to the Docker socket effectively has root access
on the host. This is a security risk and is not acceptable for a tool
that is meant to be run by unprivileged users.

Docker also does not support rootless containers in the same way that
podman does. While Docker has added some rootless support, it is not as
mature or as well-integrated as podman's rootless support.

## Security properties and limitations

Protected against:

- credential exposure to inference providers: secrets are injected by the
  proxy and never enter the workspace's environment, filesystem, or
  snapshots
- local filesystem damage: only configured mounts are visible; the shell
  has no capabilities
- plaintext egress: in every mode the sandbox cannot speak plaintext
  HTTP to a host that is not allowlisted

The default network policy is `https`: any host may be reached over
TLS on port 443, and DNS resolves every name. That trades destination
control for out-of-the-box usability; the `allowlist` mode (`--network
allowlist` or `network.mode` in the config, used by the pre-built
harness images) restores deny-by-default — only `network.allow` hosts
on any port, DNS gated by the same list, denials recorded for review.
`none` disables egress entirely. In `https` mode a compromised agent
can exfiltrate data to any TLS endpoint; use `allowlist` for untrusted
code.

Not protected against (see the README: use a VM for persistent attackers or
hostile inference providers):

- a compromised workspace process attacking the proxy over the veth (the
  proxy is a normal TCP service; request smuggling against upstream
  connections is not mitigated)
- memory/ptrace attacks on the proxy from the host or other local users
  (the proxy listens on a veth address inside the pod's network namespace)
- non-HTTP protocols tunneled to allowlisted hosts that also have a secret
  configured (those connections are intercepted as TLS and will fail)
- DNS: names of allowlisted hosts are resolved by the proxy; the workspace
  itself has no DNS

## Building

Install from the latest GitHub release (downloads, verifies the SHA-256
checksum, installs to `~/.local/bin`, and makes sure that directory is on
your PATH):

```
curl -fsSL https://raw.githubusercontent.com/philips/hx/main/install.sh | sh
```

Or build from source:

```
CGO_ENABLED=0 go build -o hsh .
```

Requires Go 1.26+ and a working rootless podman installation.

## Example workspace image

`examples/Containerfile` builds a Fedora workspace image with the Pi coding
agent pre-installed:

```
podman build -t hsh-example examples/
```

Each release also publishes a prebuilt multi-arch (amd64/arm64) image to
the GitHub Container Registry, so a config can use it directly instead of
building locally:

```
ghcr.io/philips/hsh-example:latest
```

`examples/config.hsh` uses it (`image: localhost/hsh-example:latest`,
`shell: /bin/bash`). Pre-installing tools in the image means they work without
allowlisting their install sources, so the egress allowlist can stay minimal
(the example config's allowlist is exactly the MVP plan's).

The workspace shell inherits the image's environment (including `PATH`), so
`ENV` directives in the image apply. The workspace home directory is
`/home/hsh`; the example image sets `HOME` to the same value and
installs Pi there.

The example image also pre-installs `fd-find` and `ripgrep` and sets
`PI_OFFLINE=1`. Pi resolves its `find`/`grep` tools from system PATH before
downloading them, so this keeps Pi's tools working without allowlisting
GitHub (see docs/config.md), and `PI_OFFLINE` keeps Pi from attempting any
tool downloads or update checks inside the sandbox.
