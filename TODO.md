## Migrate forwarding to `hsh-sshd` and drop the relay

Serve `hsh forward`, `--forward`, `ssh hsh-<id>`, and `hsh oauth callback` from `/hsh-sshd` in both runtimes. In the default runtime it enters the shell's network namespace by setns plus re-exec. Remove the in-sandbox relay (`10.77.0.2:7788`) and `/hsh-workspace-inner --ssh-stdio`/`--ssh-session`/`--callback-relay`/`--oauth-deliver`. Sessions created before the change get a recreate message. See plans/0034-sshd-everywhere.md.

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
