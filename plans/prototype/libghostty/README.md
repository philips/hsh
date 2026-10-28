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

## Try passthrough mode

    GOEXPERIMENT=simd go run . demo --mode passthrough

Same status line and dialog, but the session's bytes go to your terminal
unchanged: your terminal's own scrollback, selection, and search keep
working, and nothing is drawn on the alternate screen. Compare it with the
default render mode (`demo`), where ptymux sends redrawn frames and your
terminal's scrollback stays empty.

In passthrough mode Ctrl+Space stops reading the pty, so the session
blocks in `write()` instead of printing under the dialog; closing the
dialog repaints the rows it covered from emulator state and delivers the
held output. Try `seq 1 100000` in the shell, press Ctrl+Space while it
runs, and close the dialog: no lines are lost and your scrollback is
intact.

## Other commands

    ptymux serve --sock S [--fake] [--mode passthrough] -- bash   # session daemon
    ptymux attach --sock S                     # Ctrl-] detaches
    ptymux deny --sock S HOST                  # pretend the proxy denied HOST
    ptymux status --sock S                     # mode=... color=... pending=N
    ptymux dump --sock S                       # plain-text screen from the emulator
    ptymux notify --sock S "Allow example.com?"   # y/n bar; prints approved|denied|timeout

## Tests

    ./test.sh                                   # unit tests + pty-driven end to end
    GOEXPERIMENT=simd go test -v -run TestStatusLine ./...   # prints the client screen with the dialog open
