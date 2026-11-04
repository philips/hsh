## Review all console and TUI messages for terseness and accuracy

Some messages are chatty. Run `./internal-docs/find-messages.sh > /tmp/hsh-messages.txt` then `nvim -q /tmp/hsh-messages.txt` and walk the quickfix (it indexes every user-facing message: CLI prints, error strings surfaced by main.go, verbose progress, and the monitor TUI — palette status lines, proxywatch help text, relay status bar) with the review checklist in `internal-docs/messages.md`. Reword for terseness and accuracy; check that suggested commands, key names, and counts are still right, and that warnings are on stderr and progress is in `infof`. Error-string rewording can break tests — run the suite after each pass.

## Document that `podman exec` bypasses the workspace network namespace

In `container`-runtime sessions, `podman exec` into the workspace container starts in the pod network namespace, outside the inner namespace the shell runs in, so anything run that way skips the veth isolation and the egress proxy entirely. Document in `internal-docs/architecture.md` that the hsh commands (`hsh resume`, `hsh ssh`, `hsh forward`) are the supported ways to reach inside a sandbox, and that direct `podman exec` is not: anything that should follow the network policy must go through them. gVisor sessions are unaffected (their traffic goes through the netstack).

## hsh bug subcommand

Implement a subcommand that can gather up info and send it to IL automatically via the webhook

POST https://infinitelogic.app/o/hsh-home/hooks/bug_report
Content-Type: application/json

{
  "version": "v0.0.9",              // required
  "uname": "Linux box 6.8.0 ...",   // required
  "os_release": "Ubuntu 24.04.1",   // required
  "description": "crash on startup", // optional
  "logs": "traceback...",             // optional
  "email": "user@example.org"         // optional
}




Notes:
The bug_report object mirrors the metrics one: append-only-friendly storage with automatic created_at timestamps, browsable in the org's generated UI. I made version, uname, and os_release required (a report without them isn't actionable) — easy to change if you'd rather have them optional.
Returns {"ok": true, "id": "bug_01..."} so your CLI can print a reference id for the user.
Same caveats as the metrics hook: unsigned and publicly postable, so spam is possible; and reports appear in the org UI immediately.
Field naming: os-release became os_release (Infinite Logic requires snake_case field names).
CLI-side sketch:
sh
Copy

curl -X POST https://infinitelogic.app/o/hsh-home/hooks/bug_report \
  -H "Content-Type: application/json" \
  -d "{\"version\":\"$VERSION\",\"uname\":\"$(uname -a)\",\"os_release\":\"$(. /etc/os-release && echo \"$PRETTY_NAME\")\",\"description\":\"$1\"}"



## add monitor subcommand for starting/stopping forward

## add monitor subcommand for oauth callback

## add monitor subcommand to add/remove secrets from 

# First run experience from README

Embed a little parser that will ask the user if they want to read examples about Pi+Mistral, OpenAI Codex or Claude.

Keep these tutorials in the README with Collapsed sections: https://docs.github.com/en/get-started/writing-on-github/working-with-advanced-formatting/organizing-information-with-collapsed-sections

Have a tutorial subcommand that is launched after install.sh finishes that uses the README information from <!--- install.sh START --> to <!--- install.sh END -->.

## gVisor suspend/resume

`hsh resume` re-enters sessions (see plans/0041-resume-subcommand.md). Add checkpoint/restore for gVisor sessions:

```
hsh run --runtime gvisor pi .
ctrl+space select "suspend" OR hsh suspend --session
hsh resume .
```

`hsh resume` would restore from a checkpoint image in the session directory instead of starting the container. Obstacles: the embedded runsc payload drops `checkpointgofer`, `podman container checkpoint` expects CRIU-style runtime flags, and the proxy's netstack socket is recreated on each start so a restored sandbox must reconnect.


## Toggle report/allowlist mode

In the session monitor view it should be possible to switch between report/allowlist mode.

## Add Rate Limiting to Proxy

See smokescreen docs: https://github.com/stripe/smokescreen#rate-limiting

## flicker of status bar

the status bar flickers with dns install and other commands with a spinner. annoying. two paths:

- do the full emulator with libghostty (gah!)
- only pop-up the status bar when there is an approval needed

## add external proxy helper

would be cool to have a way to let people pass certain domains off to policy
proxies like https://docs.socket.dev/docs/socket-firewall-overview

## macOS Support

There is a fork of gVisor that support macOS:

- https://github.com/maci0/gvisor/blob/master/README-MACOS.md
- https://github.com/google/gvisor/issues/1270#issuecomment-4369433538


## Document the terminal requirements for clickable links in Documentation/sessions.md

Interactive sessions set `FORCE_HYPERLINK=1` when the outer `TERM` names a hyperlink-capable terminal or tmux, but links only render clickable if the whole terminal stack forwards OSC 8 and the click reaches the outer terminal. Document in `Documentation/sessions.md` (user-facing, so keep it practical, with the quick `printf` check):

- Directly in a hyperlink-capable terminal (ghostty, kitty, ...): nothing to do; Cmd/Ctrl+click the link.
- Under tmux: tmux drops OSC 8 unless its client has the `hyperlinks` feature, which no terminfo entry advertises — add `set -as terminal-features '<client-term>:hyperlinks'` to `~/.tmux.conf` (e.g. `xterm-256color`), detach and reattach. With `set -g mouse on`, tmux captures clicks; Cmd+Shift+click (on macOS ghostty) opens the link without giving up tmux mouse mode, verified working.
- Over transports that re-encode the terminal (mosh): OSC 8 is stripped and the link degrades to plain text; plain URLs stay clickable where the outer terminal auto-detects them (ghostty does).
- A dropped OSC 8 always degrades to the link's plain text, so nothing above can break output.

## Update README.md and Documentation/ for secret host binding

Plan 0043 changed the secret store's user-facing commands, and the user docs still show the old ones:

- `hsh secret add NAME` now fails without `--host HOST` (repeatable or comma-separated): every static secret records the hosts it may be sent to. `README.md` (`hsh secret add mistral-api-key`) and `Documentation/secrets.md` (`gh auth token | hsh secret add --value-stdin ...`) need the `--host` for their example's API host.
- Document `hsh secret allow NAME HOST...`, `hsh secret disallow NAME HOST...`, `hsh secret edit NAME` (the host list in `$EDITOR`), and the HOSTS column of `hsh secret ls`.
- Explain the launch behavior: a config route to an unapproved host is dropped with a warning naming the label or config file and the `hsh secret allow` command, and an interactive `hsh run` asks first.
- `env://VAR` and bare `VAR` secret references are refused in configs and image labels; remove any examples that use them and show `hsh secret add` instead. Sessions created with such a reference no longer resume.
- The shared `monitor.db` and the `--monitor-db`/`--db` flags are gone; denials live per session (`sessions/<id>/proxy/proxy.db`, decisions in `sessions/<id>/monitor.db`).

The design side is in `internal-docs/config.md#host-binding` and `internal-docs/architecture.md#denial-records-and-review-decisions`.

## Keyring backend for static secrets (`keyring://NAME`)

`secrets.db` stores values in plaintext (0600, like `~/.netrc`). Add an OS keyring backend (Secret Service on Linux, Keychain on macOS) as a new reference scheme, keeping `hsh://NAME` unchanged:

- `keyring://NAME` resolves the value from the keyring on the host at launch; host binding applies the same way (approved hosts stay in `secrets.db`, keyed by the reference).
- `hsh secret add --keyring` stores the value in the keyring instead of `secrets.db`.
- Caveat to document: unattended OAuth refresh (the companion, `hsh-reaper`) and non-interactive runs need an unlocked keyring, which headless servers often lack. OAuth logins stay in `secrets.db` unless the keyring is known to stay unlocked.
- Until then, document that `secrets.db` and `$XDG_STATE_HOME/hsh` (which holds `secrets.json` while a session runs and the proxy's captured tokens) should live on an encrypted home directory.

See plan 0043, section 4.

## Review an image's built-in config before trusting it

An image's built-in hsh config (its `org.ifup.hsh` label) is untrusted until the user trusts that image id (`hsh run --trust-image IMAGE`, recorded in `$XDG_CONFIG_HOME/hsh/trusted-images`); until then `hsh run` drops its allowlist, network mode, OAuth providers, and mounts (see `internal-docs/images.md#image-trust`). That is deny by default with an all-or-nothing flag.

Replace the flag with a review on the first `hsh run` of an image id: show what the built-in config asks for (allowlist, mode, secret routes, OAuth providers, mounts, agent command), let the user accept it (or parts of it) in the terminal, and for a new id of a repository that was trusted before, show the diff against the last trusted config. Also add a way to list and revoke trusted images (`hsh image trust ls`/`rm`, or an edit command like `hsh secret edit`).

See plan 0043, section 3.
