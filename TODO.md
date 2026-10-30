# First run experience from README

Embed a little parser that will ask the user if they want to read examples about Pi+Mistral, OpenAI Codex or Claude.

Keep these tutorials in the README with Collapsed sections: https://docs.github.com/en/get-started/writing-on-github/working-with-advanced-formatting/organizing-information-with-collapsed-sections

Have a tutorial subcommand that is launched after install.sh finishes that uses the README information from <!--- install.sh START --> to <!--- install.sh END -->.

## hsh resume subcommand

remove `hsh run --resume` as that is a confusing use case

the ideal workflow for me would be:

```
hsh run pi .
<exit container>
hsh resume .
```

Need to consider how resume might interact with gVisor with checkpoint/restore. I think for that use case we can have a workflow like

```
hsh run pi .
ctrl+space select "suspend" OR hsh suspend --session
hsh resume .
```

## networking modes

## monitor proactive add URLs

It would be nice if a user could open the monitor, press a key, and past in URLs that they want to add to the current session's allow or denylist

## monitor suspend command

the monitor needs to be expanded to support additional 

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

## stop modifying config.json for every monitor add

I think modifying the users config.json is dangerous because we might get schema drift if the user provides a config.json with fields unknown to the current version of hsh.

Instead I would like to have a single `~/.local/state/hsh/session/<id>/session.db` with tables for secrets, network policy, etc.

See plans/0036-session-db-policy.md (plan, awaiting review).

## Add a "report" network policy

Users may want to monitor what their agent is doing for awhile. Add a report table to session.db.

## Add a "report to network policy" command

It should be easy for a user to do something like `hsh proxy report | hsh proxy add` or `hsh proxy report | hsh policy add my-project`.

## Toggle report/allowlist mode

In the session monitor view it should be possible to switch between report/allowlist mode.

## Fuzz the session monitor toggle

I am concerned there is a way for the agent to send commands to the session monitor. Investigate, fuzz, and write a report doc for users.

## Add Rate Limiting to Proxy

See smokescreen docs: https://github.com/stripe/smokescreen#rate-limiting

## flicker of status bar

the status bar flickers with dns install and other commands with a spinner. annoying. two paths:

- do the full emulator with libghostty (gah!)
- only pop-up the status bar when there is an approval needed

## Migrate forwarding to `hsh-sshd` and drop the relay

Serve `hsh forward`, `--forward`, `ssh hsh-<id>`, and `hsh oauth callback` from `/hsh-sshd` in both runtimes. In the default runtime it enters the shell's network namespace by setns plus re-exec. Remove the in-sandbox relay (`10.77.0.2:7788`) and `/hsh-workspace-inner --ssh-stdio`/`--ssh-session`/`--callback-relay`/`--oauth-deliver`. Sessions created before the change get a recreate message. See plans/0034-sshd-everywhere.md.
