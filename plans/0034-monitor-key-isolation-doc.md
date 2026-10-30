# User-facing doc: the agent cannot open the monitor (Ctrl+Space)

> **Status: implemented** as docs/agent-terminal-isolation.md, cross-linked
> from config.md's inline-proxy-review section and architecture.md's relay
> section. The claims cite the 0032 tests, including the split-write region
> smuggling bug they found and fixed.

## Goal

A user reading about the inline proxy review will ask: "the agent writes to
my terminal — can it type Ctrl+Space at me, open the review screen, forge
the status line, or otherwise trick hsh?" Write a short user-facing doc in
`docs/` answering this directly, with the reasoning and the evidence.

## Audience and tone

Non-expert user of hsh who knows the agent is untrusted. Direct answer
first, mechanism second, evidence last. No internal function names in the
prose (file paths okay in a footnote-style "for developers" section).

## Location

`docs/agent-terminal-isolation.md` — standalone page, linked from
`docs/config.md` (the inline-proxy-review section, where Ctrl+Space is
introduced), from `docs/architecture.md` (relay section), and from the
README's docs index if one exists (do not edit README.md itself beyond any
pre-existing index conventions — per AGENTS.md, README.md is not edited).

## Outline

1. **The question.** "Can the agent trigger Ctrl+Space / the monitor, or
   fake the status line?" One-sentence answer up front: No — the review
   screen opens only on a keystroke physically read from your terminal's
   input, and the agent has no way to write to that stream or to spoof the
   bytes it is matched against.
2. **The two directions.** A short diagram-level explanation: the agent's
   output flows one way (agent → embedded ghostty surface → filtered copy →
   your terminal); your keystrokes flow the other (your terminal → hsh →
   the agent). The monitor key is matched only on the keystroke path.
3. **What about terminal replies?** The one subtle case, spelled out: the
   agent can print queries that make *your terminal* reply on the input
   path (this is how terminals work; every terminal program can do it).
   Those replies are printable parameter bytes — they can never contain
   the NUL byte that is Ctrl+Space, and in the kitty keyboard protocol
   hsh only accepts sequences with the Ctrl modifier on exactly the
   configured key and no key-release events. Query replies use a form hsh
   never matches.
4. **The status line.** The "N proxy requests need review" text is
   computed from the local denial database, not from anything the agent
   writes; the agent's own escape sequences that try to paint or scroll
   into the status row are filtered out. An in-band notification channel
   the proxy uses can be written by the agent too, but it carries no text
   — worst case it makes hsh re-check the database.
5. **What if it could?** Honest statement of the stakes: opening the
   review screen pauses output and waits for the user — annoying, a
   nuisance-DoS, but the review screen itself only ever acts on real
   proxy requests, and nothing agent-controlled executes from it.
6. **Changing the key.** Note that `monitor.key` (see config.md) can move
   the trigger to any Ctrl key, and the isolation is enforced for every
   valid key, not just the default.
7. **Evidence (for developers).** One short closing section pointing at:
   `internal/relay/security_test.go`, the fuzz targets in
   `internal/relay/fuzz_test.go`, `test/security.py`, and
   plans/0035-monitor-key-isolation-testing.md for the full threat model.

## Rules for writing it

- No claims about the future; only what the tests prove once 0032 lands.
- If any Phase 1–2 test finds a real injection, the doc's answer changes —
  fix the bug first, keep the doc honest.
- Keep it under ~120 lines; the full threat model lives in the plan, not
  the doc.
- Update `docs/architecture.md`'s relay section with a link to the new
  page, and remove or fold in anything in `docs/terminal-checks.md` it
  duplicates.
