# Subcommand Cleanup

Some of the subcommands on `hsh` need some cleanup.

## Monitor

Replace `proxy-watch` with `monitor`- the idea is that the user will be using this to monitor all of their sessions and we may add further access control rules beyond the proxy later.

## Proxy

Replace `proxy-denied` and `proxy-auto-allow` with subcommands under `proxy` like `secrets`.

User can list all denied requests

```
hsh proxy denied
SESSION ID      WORKSPACE       URL
3ba5a9ed451f    ex-project      https://example.com
3ba5a9ed451f    ex-project      https://foobar.example.com
022073c3fea9    ex-research     https://pdf.example.com
```

User can filter the requests to a single session

```
hsh proxy denied --session 3ba5a9ed451f
https://example.com
https://foobar.example.com
```

User can allow all denied requests for a session

```
hsh proxy allow-denied --session 3ba5a9ed451f
added https://example.com
added https://foobar.example.com
```

User can add a single URL to a single session

```
hsh proxy allow --session 3ba5a9ed451f https://example.com
added https://example.com
```

All of these commands take a `--watch` flag to continuously output new denied requests or approve them.
