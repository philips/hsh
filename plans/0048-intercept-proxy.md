# Intercept proxy

> **Status:** future work, split out of plan 0045.

Today the proxy only TLS-intercepts hosts that have a configured secret or an
OAuth endpoint; every other connection is a blind tunnel, so the proxy knows
`CONNECT host:port`, byte counts and duration, nothing more. The features below
all need the proxy to see requests inside TLS, so they share one design.

## Path rules in network policies

Extend a 0045 policy rule with paths:

```json
{"hosts": ["dl.google.com"], "paths": ["/go/*"]}
```

`paths` defaults to `["*"]` when absent.

To decide:

- a host with a path rule must be intercepted; in `https` mode a path rule is
  meaningless unless it restricts (every host on 443 is already allowed)
- glob semantics: does `*` cross `/`, are query strings part of the match,
  percent-encoding and dot-segment normalization (`/go/%2e%2e/secret`) before
  matching
- denial records are keyed by host (`denied.host PRIMARY KEY` in proxy.db);
  the review queue needs the path, and approving needs a path-shaped answer
- clients that pin certificates or do not read the session CA break for any
  host that gets intercepted

## Methods

`"methods": ["GET", "HEAD"]` on a rule. Same interception requirement as paths.

## Full request log (HAR-like)

Plan 0045's report mode records connections. A HAR-like log of requests
(method, URL, status, headers, timings) needs interception of every host in
report mode.

Candidate schema to evaluate:
https://github.com/brucehart/harlite/blob/main/src/db/schema.rs

To decide:

- headers must be recorded before secret injection or redacted; the proxy
  rewrites `Authorization` and other headers for secret hosts
- bodies: probably never stored, or size-capped and opt-in
- retention and size caps for proxy.db (the `denied` table caps at
  `MaxHosts`)
