## create policy config files

It would be good to create and maintain "policy" files for popular services like Fedora, sr.ht, gitlab, github, npm, Golang, Python packages, etc. 

Policies could be managed with the `hsh policy` subcommand like

```
hsh policy add github.com https://example.com/github-policy
```

This is an example policy that might be used for Golang access:

```
{"hosts":["go.dev"]},
{"hosts":["dl.google.com"], paths:["/go/*"]},
{"hosts":["proxy.golang.org"]},
```

paths is implied to ["*"] unless specified for a host.

Which would add a file to `~/.config/hsh/policy/network/github.com.json`

Then a user could do something like `hsh policy list` and `hsh session apply-policy <session-id> <policy-id>`. Policies could also be referenced from config.json files.

## Hostname matching

(lifted from https://github.com/stripe/smokescreen/blob/master/README.md)

A host can be specified with or without a wildcard, and a glob may contain at most one wildcard. The host must be in Punycode to prevent ambiguity.

A leading *. matches one or more subdomain labels: *.example.com matches api.example.com and a.b.example.com, but not example.com.
A * spanning a label other than the leftmost one matches exactly one label: access-analyzer.*.amazonaws.com matches access-analyzer.us-west-2.amazonaws.com, but not access-analyzer.a.b.amazonaws.com.
A * after a literal prefix within a label matches one or more characters in that label: api*.example.com matches api-east.example.com, but not api.example.com, api.a.example.com, or a.api-east.example.com.

| host                              | valid   |
| --------------------------------- | ------- |
| `example.com`                     | yes     |
| `*.example.com`                   | yes     |
| `access-analyzer.*.amazonaws.com` | yes     |
| `api*.example.com`                | yes     |
| `web*-canary.example.com`         | yes     |
| `web.qa-*.internal.example.com`   | yes     |
| `*.*.example.com`                 | no      |
| `*.api*.example.com`              | no      |
| `*example.com`                    | no      |
| `*-canary.example.com`            | no      |
| `ex*ample.com`                    | no      |
| `login.*.com`                     | no      |
| `foo*.github.io`                  | no      |
| `éxämple.com`                     | no      |
| `example.*`                       | hell no |

## rename https mode to tls_passthrough



https://github.com/agent-substrate/substrate/blob/75a061c290952b432de1131397bdb9a222a33e3e/docs/guides/egress-trust-bundle.md?plain=1#L22

## add a "report" network policy

Users may want to monitor what their agent is doing for awhile. Add a HAR-like set of tables to proxy.db.

Lets evaluate a SQLite for HAR files used in this project:

https://github.com/brucehart/harlite/blob/main/src/db/schema.rs

Also, we need to ammend the schema with a transformations log like iron-proxy

https://github.com/paradigmxyz/iron-proxy#audit-log-format

```
  "request_transforms": [
    {
      "name": "allowlist",
      "action": "continue"
    },
    {
      "name": "secrets",
      "action": "continue",
      "annotations": {
        "swapped": [{ "secret": "OPENAI_API_KEY", "locations": ["header:Authorization"] }]
      }
    }
  ],
```

# UX of Adding/Removing Policies

## Add a "report to network policy" command

It should be easy for a user to do something like `hsh proxy report | hsh proxy add` or `hsh proxy report | hsh policy add my-project`.

# Out of Scope

For the initial work I don't want the proxy to become too crazy to configure- that can turn into a distraction to getting things done.

Future work: add methods `methods:["GET", "HEAD"]`
