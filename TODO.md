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

## networking modes

rename https mode to tls_passthrough

we need a separate mode to record headers, bodies, etc too for auditing in the case of hacks.

https://github.com/agent-substrate/substrate/blob/75a061c290952b432de1131397bdb9a222a33e3e/docs/guides/egress-trust-bundle.md?plain=1#L22

## create policy config files

It would be good to create and maintain "policy" files for popular services like sr.ht, gitlab, github, etc. 

Policies could be managed with the `hsh policy` subcommand like

```
hsh policy add github.com https://gist.githubusercontent.com/philips/2f755b38577e41acbb94a75b153a9fb4/raw/f4e064e86c7e189311fbdbefd9fa5c0447e2161e/gistfile1.txt
```

An example policy might be Go access

```
"go.dev",
"dl.google.com",
"proxy.golang.org",
"storage.googleapis.com"
```

Which would add a file to `~/.config/hsh/policy/network/github.com.json`

Then a user could do something like `hsh policy list` and `hsh session apply-policy <session-id> <policy-id>`. Policies could also be referenced from config.json files.

Coupled with this we should probably 

Longerterm it would be awesome to get services to publish those policy files themselves. 

## Add a "report" network policy

Users may want to monitor what their agent is doing for awhile. Add a report table to session.db.

## Add a "report to network policy" command

It should be easy for a user to do something like `hsh proxy report | hsh proxy add` or `hsh proxy report | hsh policy add my-project`.

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


## `hsh ls -q` should print session ids

`hsh ls -q` passes `--quiet` to `podman pod ls` and prints 64-character podman pod IDs. No hsh command takes those: `hsh rm`, `hsh resume`, `--session`, and the `hsh-<session>-proxy` container names all use the session id from the `SESSION` column of `hsh ls`. Print session ids instead, so `hsh rm $(hsh ls -q ...)` and `podman logs -f hsh-$(hsh ls -q --latest)-proxy` work.

## Clickable links from harnesses inside the sandbox

Outside hsh, `claude auth login` prints a clickable login link; inside hsh it does not. The workspace only gets `TERM=xterm`, without `TERM_PROGRAM`, `TERM_PROGRAM_VERSION`, `COLORTERM`, `VTE_VERSION`, or `WT_SESSION`, so terminal-capability detection (supports-hyperlinks style) decides the terminal cannot render OSC 8 hyperlinks and the harness prints a plain URL that its UI hard-wraps across lines, which the outer terminal cannot detect as one link. Forward the outer terminal's capability variables into the workspace (or set `FORCE_HYPERLINK=1` when the outer terminal supports OSC 8), and check that the relay and libghostty-vt pass OSC 8 sequences through unchanged, including across the status-line repaint.
