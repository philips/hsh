#!/bin/sh

# Audit every message hsh shows the user. Run from the repo root:
#   ./internal-docs/find-messages.sh            (sections on stderr, list on stdout)
#   ./internal-docs/find-messages.sh > /tmp/hsh-messages.txt
#   nvim -q /tmp/hsh-messages.txt                (jump through messages in the quickfix)
# Sections: CLI prints, error strings surfaced by main.go, and the monitor
# TUI (command palette, inline proxy review, relay status bar and overlays).
# Surfaces documented in internal-docs/messages.md.

say() { printf '\n===== %s =====\n' "$1" >&2; }

say "CLI prints: results and prompts (stdout) / warnings and errors (stderr)"
rg -n 'fmt\.(Fprint|Print)' internal/cli/ internal/crashlog/ --glob '!*_test.go'

say "error strings: rendered as 'hsh: <message>' by main.go on failure"
rg -n 'fmt\.Errorf|errors\.New' internal/cli/ --glob '!*_test.go'

say "verbose-only progress (infof: hidden unless --verbose or HSH_VERBOSE)"
rg -n 'infof\(' internal/cli/ --glob '!*_test.go'

say "monitor TUI: status messages (palette setStatus, proxywatch m.status)"
rg -n 'setStatus\(|m\.status =' internal/tui/palette/model.go internal/tui/proxywatch/model.go

say "monitor TUI: rendered strings (views, help lines, empty states, prompts)"
rg -n '"[^"]*[a-z][^"]*\s[^"]*"' internal/tui/palette/ internal/tui/proxywatch/ --glob '!*_test.go' | grep -vE ':[0-9]+:[[:space:]]*"'

say "relay: status bar and overlay text shown mid-session"
rg -nH 'text = |line = |crash =' internal/relay/overlay.go

say "non-TUI strings around the monitor (launch, inline review, holds)"
rg -n 'fmt\.(Fprint|Print)|"[^"]*[a-z][^"]*\s[^"]*"' internal/cli/monitor.go internal/cli/inline.go internal/cli/proxy_hold.go --glob '!*_test.go' | grep -vE ':[0-9]+:[[:space:]]*"'
