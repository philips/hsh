# unix-packet-transport probe

For plans/0055-gvx.md: can the link between a gvx sandbox and hsh's proxy be
an AF_UNIX `SOCK_SEQPACKET` socket, as runsc's `--network-proxy-path` uses,
on macOS? For comparison it also tests `SOCK_DGRAM` and `SOCK_STREAM`, which
are the fallbacks.

    cd plans/prototype/unix-packet-transport
    go run .

Stdlib only; separate module so it is not built by the repository's `./...`.
Runs on Linux too, for a baseline.

For each socket type it reports:

- `socketpair` and bind/listen/connect/accept on a path (what runsc does),
  and the error a client of another type gets when it connects to that path
- default and 4 MiB `SO_SNDBUF`/`SO_RCVBUF`, and what the kernel grants
- record boundaries for 1, 1500, 9000, and 65520-byte packets (65520 is the
  MTU hsh uses under pasta)
- the largest message that can be sent, with default and 4 MiB buffers
- what a read with a short buffer does (`MSG_TRUNC`, rest discarded or kept)
- read and write after the peer closes (EOF is how the sandbox's link and
  the proxy notice a disconnect)
- throughput with 1500 and 65520-byte packets

It also prints the relevant sysctls and the `sun_path` length limit against
the session socket paths hsh would use.
