# The agent cannot open the monitor

A common question about the inline proxy review: *the agent writes escape
sequences to my terminal — can it press Ctrl+Space for me, open the review
screen, fake the status line, or otherwise abuse hsh's UI?*

**No.** The review screen opens only when a keystroke physically read from
your terminal's input matches the configured monitor key, and there is no
path for the agent to write to that stream or to spoof the bytes the key is
matched against.

## The two directions

Bytes flow one way and keys flow the other:

```
agent output ──▶ embedded ghostty surface ──▶ filtered copy ──▶ your terminal
your keys   ──▶ hsh (monitor key matched here) ──▶ the agent
```

The monitor key is matched only on the keystroke path. The agent's output
is never fed to the key matcher, the review screen's input parser, or the
status line — it can only paint pixels in the content area above the status
row, under the same filtering every other byte goes through.

## What about terminal replies?

There is one subtle case, and it is worth spelling out. Terminal programs
can print queries that make *your terminal* reply on the input path —
cursor position, device attributes, keyboard-protocol support. The agent
can print those queries, and every terminal program can; it is how
terminals work. Those replies arrive at hsh on the keystroke path, so they
pass through the key matcher.

They cannot match:

- replies consist of printable parameter bytes and never contain the NUL
  byte that is Ctrl+Space (or the raw byte of any other valid `monitor.key`)
- in the kitty keyboard protocol, hsh only accepts a sequence with the
  Ctrl modifier on exactly the configured key, and never a key-release
  event; query replies use a `CSI ?`-form hsh does not match at all

This is enforced mechanically, not just by reading the code: the Go test
suite feeds every reply a terminal realistically produces (cursor reports,
device attributes, mode reports, kitty flags, mouse, focus, paste markers,
key releases) through the key matcher for **every** valid `monitor.key`
value, and the fuzz targets assert on arbitrary byte streams that a match
can only ever be the genuine encoding of the configured key.

## The status line

The `* N proxy requests need review` text is computed from hsh's local
denial database, never from anything the agent writes. The agent's own
sequences that try to paint, scroll, or reset their way into the status
row are rewritten or dropped before reaching your terminal, including
escape sequences deliberately split across writes to slip past the
filters — hsh's filters reassemble split sequences and give up on
unbounded ones in a way that cannot reassemble into anything live.
The in-band notification channel the proxy uses can be written by the
agent too, but it carries no text: worst case it makes hsh re-check the
database, which is what the status line shows anyway.

## What if it could?

The stakes, stated honestly: opening the review screen pauses the
session's output and waits for you — an agent-triggerable nuisance, not a
compromise. The review screen itself only ever acts on real proxy requests
recorded in the local database, and nothing agent-controlled executes from
it. The isolation exists so that what you see is trustworthy: the count,
the color, and the pause are all yours.

## Changing the key

`monitor.key` (see [config.md](config.md#inline-proxy-review)) can move the
trigger to any Ctrl key. The isolation holds for every valid key, not just
the default Ctrl+Space, and the tests cover the whole valid set.

## For developers

- `internal/relay/security_test.go` — reply corpus, query sprays, hint
  forging, split-write smuggling, region integrity, per-key coverage
- `internal/relay/fuzz_test.go` and `internal/vt/fuzz_test.go` — the same
  properties fuzzed over arbitrary byte streams
- `test/security.py` — the end-to-end driver (a scripted agent spraying
  queries while the harness answers as a real terminal)
- [plans/0035-monitor-key-isolation-testing.md](../plans/0035-monitor-key-isolation-testing.md)
  — the full threat model
