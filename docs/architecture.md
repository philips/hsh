# harnessh architecture

harnessh runs an interactive shell inside a rootless podman sandbox with
controlled egress. It is a single static Go binary that plays three roles:

| Mode | Runs | Purpose |
|---|---|---|
| `harnessh run` | on the host | loads config, creates the pod and containers, attaches to the workspace |
| `harnessh proxy` | proxy container, inside the pod | egress proxy: allowlist, TLS interception, secret injection |
| `harnessh workspace` / `harnessh workspace-inner` | workspace container, inside the pod | network isolation setup, then the user's shell |

The same binary is bind-mounted read-only at `/harnessh` in both containers.

## Pod layout

`harnessh run` creates one podman pod with two containers:

```
host
└── pod (shared network namespace, pasta networking, internet access)
    ├── proxy container    (harnessh proxy, listens on 10.77.0.1:8080)
    └── workspace container (harnessh workspace → user shell)
        └── inner network namespace (created by `harnessh workspace-inner`)
            └── user shell + tools (no default route)
```

The pod's shared network namespace is what lets the workspace reach the
proxy. Pods do not share PID namespaces, so the workspace cannot see or
signal the proxy's processes.

## Network isolation

The workspace container starts in the pod's network namespace with
`NET_ADMIN` and `SYS_ADMIN` capabilities. Its entrypoint
(`harnessh workspace`):

1. creates a veth pair (`hsh0`/`hsh1`) in the pod network namespace and
   configures `hsh0` with `10.77.0.1/24`
2. re-executes itself as `harnessh workspace-inner`, which calls
   `unshare(CLONE_NEWNET)` to move into a fresh, empty network namespace
3. moves `hsh1` into the child's namespace (by PID, via netlink)
4. drops all of its own capabilities
5. signals the child, which configures `hsh1` with `10.77.0.2/24` and brings
   up loopback

The child namespace has exactly one route: `10.77.0.0/24 dev hsh1`. There is
no default route, so the workspace cannot reach the internet, the host, or
any other network directly. Its `/etc/resolv.conf` points at `127.0.0.1`
(inside its own namespace), so direct connections fail immediately at DNS
instead of timing out.

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
  `Proxy-Authenticate: Harnessh realm="egress-allowlist"` header (RFC 7235)
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

### Denial database

Every denial is recorded in a SQLite database (modernc.org/sqlite — pure Go,
so the `CGO_ENABLED=0` static build is unaffected) at
`$XDG_STATE_HOME/harnessh/denied.db` (default `~/.local/state/harnessh/denied.db`,
override with `harnessh run --denied-db PATH`). The database is opened in WAL
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
`allowed` marks hosts already handled. The state directory is bind-mounted
read-write into the proxy container only — the workspace never sees it, so
the agent cannot erase or forge its own denial record. Database errors are
logged and never change the response: a denial is a denial with or without
the log.

Two host-side subcommands work with the database:

```
harnessh proxy-denied [--db PATH] [--all]          list hosts awaiting review
harnessh proxy-auto-allow [--config PATH] [--db PATH]
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

## Secret injection

Secrets are declared in the config as `secrets.<NAME>` with:

- `from`: host environment variable to read the value from
- `hosts`: hosts the secret may be injected for
- `header` (optional): header name; defaults to the secret name with `_`
  replaced by `-` (e.g. `API_KEY` → `Api-Key`)
- `prefix` (optional): prepended verbatim to the value (e.g. `"Bearer "`)

The flow keeps the secret out of the workspace entirely:

1. `harnessh run` reads the values from the host environment and writes them
   to `secrets.json` in a temporary directory (mode 0600)
2. the temporary directory is bind-mounted read-only into the **proxy
   container only** — the workspace container never sees it
3. the proxy injects `header: prefix+value` into requests whose host matches
   `hosts`, replacing any value the workspace sent

The workspace cannot read the proxy container's files (different container,
private PID namespace, root-owned 0600 file), cannot redirect an injected
request to another host (the proxy dials the `CONNECT` target itself), and
the proxy logs header names but never values.

## TLS interception

`harnessh run` generates a fresh ECDSA P-256 CA per run (never persisted).
The CA certificate is bind-mounted into the workspace (which appends it to
the system trust bundle and exports `SSL_CERT_FILE`, `GIT_SSL_CAINFO`,
`NODE_EXTRA_CA_CERTS`, `CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE`); the CA
private key is mounted into the proxy container only. The proxy signs
per-host leaf certificates on demand (cached per host).

Upstream TLS is verified against the host's CA bundle (bind-mounted into the
proxy container, falling back to the image's bundle).

## Configuration hot reload

The proxy polls the config file and the resolved-secrets file once per
second and swaps its in-memory state when either changes. `harnessh run`
likewise watches the config file and rewrites `secrets.json` when it
changes, so `network.allow` and secret values/hosts update without
restarting the sandbox. Both files are reached through **directory**
bind mounts so that replacing the file (editors and `sed -i` rename files)
is picked up.

Mounts, image, and shell changes require a restart.

## Lifecycle and cleanup

- The pod is named `harnessh-<random>`; containers are `<pod>-proxy` and
  `<pod>-ws`.
- `harnessh run` attaches to the workspace container (`podman run -it`, or
  `-i` when stdin is not a terminal) and propagates its exit code.
- On exit, SIGINT, or SIGTERM, the pod is removed (`podman pod rm -f`) and
  the temporary directory deleted.
- Proxy logs: `podman logs <pod>-proxy` while the session is running.

## Security properties and limitations

Protected against:

- credential exposure to inference providers: secrets are injected by the
  proxy and never enter the workspace's environment, filesystem, or
  snapshots
- local filesystem damage: only configured mounts are visible; the shell
  has no capabilities
- unrestricted network egress: no default route; the proxy enforces the
  allowlist for every connection

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

```
CGO_ENABLED=0 go build -o harnessh .
```

Requires Go 1.24+ and a working rootless podman installation.

## Example workspace image

`examples/Containerfile` builds a Fedora workspace image with the Pi coding
agent pre-installed:

```
podman build -t harnessh-example examples/
```

`examples/config.harnessh` uses it (`image: localhost/harnessh-example:latest`,
`shell: /bin/bash`). Pre-installing tools in the image means they work without
allowlisting their install sources, so the egress allowlist can stay minimal
(the example config's allowlist is exactly the MVP plan's).

The workspace shell inherits the image's environment (including `PATH`), so
`ENV` directives in the image apply. The workspace home directory is
`/home/harnessh`; the example image sets `HOME` to the same value and
installs Pi there.

The example image also pre-installs `fd-find` and `ripgrep` and sets
`PI_OFFLINE=1`. Pi resolves its `find`/`grep` tools from system PATH before
downloading them, so this keeps Pi's tools working without allowlisting
GitHub (see docs/config.md), and `PI_OFFLINE` keeps Pi from attempting any
tool downloads or update checks inside the sandbox.
