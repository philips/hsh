# libghostty prototype (ptymux)

A small pty multiplexer that uses libghostty-vt (via
`code.selman.me/hauntty/libghostty`, the wasm build converted to pure Go)
to keep the screen state of a session. Mechanics behind
plans/0025-libghostty.md: detach/reattach with screen restore, and an
approval popover that is drawn over the client and removed by redrawing
from emulator state.

    ./test.sh

Needs Go 1.27 (the script sets `GOEXPERIMENT=simd`) and python3 for the
pty driver. Separate module, so the repository's `./...` ignores it.

    ptymux serve --sock S -- bash
    ptymux attach --sock S          # Ctrl-] detaches
    ptymux dump --sock S            # plain-text screen from the emulator
    ptymux notify --sock S "Allow example.com?"   # prints approved|denied|timeout
