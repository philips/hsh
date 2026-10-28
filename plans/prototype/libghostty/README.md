# libghostty prototype (ptymux)

A small pty multiplexer that uses libghostty-vt (via
`code.selman.me/hauntty/libghostty`, the wasm build converted to pure Go)
to keep the screen state of a session. Mechanics behind
plans/0025-libghostty.md: detach/reattach with screen restore, an approval
popover, and (decision 0) a one-line `hsh` status line that announces
proxy requests needing review and turns red while a review dialog is up.

Needs Go 1.27 (the commands below set `GOEXPERIMENT=simd`) and python3
for the pty driver in `test.sh`. Separate module, so the repository's
`./...` ignores it.

## Try the status line

    GOEXPERIMENT=simd go run . demo

This starts your shell (or `demo -- CMD`) inside ptymux with fake proxy
denials arriving every few seconds.

- The bottom row is the status line. It is **green** and says
  `proxy: nothing to review`; after about three seconds it becomes
  `* N proxy requests need review | Ctrl+Space to review`.
- **Ctrl+Space** turns it **red** and pops over the approval list. Inside
  the dialog: up/down (or j/k) select, `a` approve, `d` deny, `A` approve
  all, `D` deny all, `Esc` or Ctrl+Space close. Closing redraws the
  screen underneath from emulator state and the line goes green again.
- Ctrl-] detaches (and ends the demo). Decisions are printed on exit.

The list is fake: nothing talks to the proxy or `denied.db`.

## Other commands

    ptymux serve --sock S [--fake] -- bash     # session daemon
    ptymux attach --sock S                     # Ctrl-] detaches
    ptymux deny --sock S HOST                  # pretend the proxy denied HOST
    ptymux status --sock S                     # mode=... color=... pending=N
    ptymux dump --sock S                       # plain-text screen from the emulator
    ptymux notify --sock S "Allow example.com?"   # y/n bar; prints approved|denied|timeout

## Tests

    ./test.sh                                   # unit tests + pty-driven end to end
    GOEXPERIMENT=simd go test -v -run TestStatusLine ./...   # prints the client screen with the dialog open
