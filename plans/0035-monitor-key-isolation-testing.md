# Test and fuzz the agent→monitor isolation (Ctrl+Space)

> **Status: implemented** (Phases 1–4, verified: the full `go test` suite
> plus 60 s per fuzz target pass). Two real bypasses were found and fixed
> while building the tests. First, the overlong-param give-up emitted the
> lone `ESC` and flowed the rest through as text, so an agent splitting
> `ESC[1;1;1;1;1;1;1;1;1` + `1;40r` across writes reassembled a live DECSTBM
> that scrolled over the status row (the hint filter's 512-byte give-up had
> the same shape); both are fixed by dropping the introducer byte on give-up.
> Second, the fuzzer found `ESC[ESC[r`: the region filter passed the
> interrupted CSI through, but a terminal aborts the CSI at the embedded
> `ESC` and reads the trailing `ESC[r` as a full-screen region reset that
> includes the status row; the filter now aborts at the embedded `ESC` and
> re-parses from it, so the reset is rewritten like any other. The input
> fuzz also found that a multi-byte rune split across reads lost its first
> half; `inputParser` now holds incomplete rune tails like it already held
> split paste markers. The fuzz crashers are kept in `testdata/fuzz` as
> regression seeds.

## Goal

The inline proxy review is opened by the user pressing the configured
monitor key (default Ctrl+Space). Users will ask: **can the agent — which
controls every byte written to its pty — open the monitor itself, forge
the status line, or otherwise abuse the review UI?** This plan defines the
tests and fuzzing that must exist so the answer is demonstrably "no", and
regressions are caught.

## Decisions

- The reply corpus deliberately excludes every genuine encoding of every
  valid monitor key; the tests assert the stronger property instead — any
  match must be the genuine raw byte or kitty encoding, which the fuzz
  target enforces on arbitrary input.
- The paste-framing tests stayed at the `inputParser` level: `inputLocked`
  matches the key on raw input bytes, so a user-pasted NUL opens the
  overlay — user-initiated and out of scope for the agent threat model.
- `test/security.py` reuses the inline driver's plumbing (the detach-test
  image, `podman`, and a `pump` that answers the agent's queries the way a
  real terminal would) and also covers the split-write region smuggle end
  to end; `docs/terminal-checks.md` gained a manual "Agent isolation"
  section (checks 20–22) for real terminals.

## Threat model

The agent (any code run on its behalf) is untrusted. It controls:

1. **All bytes on its output stream.** These are rendered on the embedded
   ghostty surface and (in attach) filtered by `internal/relay` and written
   to the user's terminal (`processLocked`).
2. **Indirect input.** By emitting terminal queries in its output that hsh
   passes through, the agent can make the *user's terminal* reply on the
   input stream (`inputLoop` → `inputLocked`): DA1 (`ESC c` responses), DSR
   cursor position (`CSI 6n` → `CSI row;col R`), DECRPM (`CSI ?Pd;Ps $ y`),
   XTGETTCAP, kitty keyboard/capability queries (`CSI ? u`, `CSI = u`),
   and modification of the user terminal's keyboard encoding
   (`CSI > flags u` push/pop).
3. **Sequences designed to confuse hsh's own parsers**: OSC 7777 hint
   bodies, bracketed-paste markers, partial escape sequences split across
   writes, and anything aimed at `internal/vt` (the screen model the
   status-line region is computed from).

What the attacker gains if successful: opening the overlay pauses the
session's output and demands user attention (an agent-triggerable nuisance
/ soft DoS); forging hints corrupts the status line (fake "requests need
review" or hiding real ones); corrupting the vt model can paint over the
status line. Nothing in the overlay executes agent-controlled code — the
harm is nuisance and loss of trust in the indicator, but it must still be
impossible.

### Structural invariant

The overlay trigger (`openReviewLocked`) is only reachable from
`inputLocked` via `cfg.Key.find`. No code path feeds agent data (output
stream or ghostty surface state) into `inputParser`, `Key.find`, or
`reviewInputLocked`. Tests should assert this structurally (e.g. a test
that greps the package for writers to the input pty buffer, or better: a
typed unit boundary review note in `docs/architecture.md` refreshed by
this plan).

## Why the default key is safe (to encode in tests and docs)

- Raw form: Ctrl+Space is `0x00`. Terminal replies to queries contain only
  printable/parameter bytes and finals — never NUL — so agent-provoked
  replies cannot match the raw byte form.
- Kitty form: `CSI 32;5u` (space with ctrl modifier). `decodeCtrl` requires
  the ctrl bit (mod bit 4) set, no unexpected other bits (only
  shift/meta/`4|64|128` allowed), and rejects the release event. Query
  replies use `CSI ? … u` (interrogation mark) which `decodeCtrl` never
  matches.
- These properties must be *fuzz-enforced*, not just asserted by reading
  the code.

## Phase 1 — deterministic Go tests (internal/relay)

New file `internal/relay/security_test.go` alongside the existing
`relay_test.go`:

1. **Reply corpus can never trigger**: table of realistic terminal reply
   bytes — `CSI 12;40 R`, DA1 (`CSI ? 62;… c`), DECRPM
   (`CSI ? 2004;2 y`), XTGETTCAP DCS responses, kitty reply
   `CSI ? 1;0;2004u` — fed through `inputParser.feed` and directly through
   `Key.find` for *every* valid monitor key (`ctrl-space` … `ctrl-z`,
   `ctrl-\`, `ctrl-]`, `ctrl-^`, `ctrl-_` from `ParseKey` over the whole
   valid set). Assert: no match, no overlay key event, and (for
   `inputLocked`-level tests using the fake pty pattern in
   `relay_test.go`) that `openReviewLocked` never fires.
2. **Query injection end-to-end at the relay level**: reuse the harness
   from `relay_test.go` (the one that verifies "the kitty-encoded
   Ctrl+Space opens the overlay"). Drive the *output* side with a battery
   of queries (`CSI 6n`, `CSI c`, `CSI ? u`, `CSI = 1;1u`, `CSI > 1u`,
   `CSI < u`, paste markers `CSI 2004h/l`) and assert the overlay never
   opens and the output written to the user terminal is byte-identical to
   the passthrough expectation (except hint/region filtering).
3. **Paste framing**: `inputParser` with agent-fed nonsense — a paste
   start without end, NUL inside a paste body, `CSI 32;5u` inside a paste
   body (must be delivered as paste runes to the agent pty, not matched as
   the monitor key), and paste markers split across `feed` calls.
4. **Overlay-open isolation**: while `review.open`, agent output is paused
   and `reviewInputLocked` consumes input; test that a flood of agent
   output during the overlay neither leaks to the user terminal nor
   escapes the pause (`held` buffer drained only on resume), and that
   agent output cannot reach `reviewInputLocked`.
5. **Hint forging**: `hintFilter.feed` with `ESC ] 7777 ; <attacker text>
   BEL/ST` — split at every byte position, nested/malformed variants
   (unclosed, >512-byte body, `ESC` immediately before `\\`). Assert the
   hint count matches the number of well-formed hints the proxy itself
   emits, the status line text is never derived from the OSC body, and
   `processLocked` treats forged hints as inert (they trigger a poll,
   which simply reads the real database — assert no text leaks).
6. **Region integrity**: `regionFilter.feed` / `rewriteRegion` — agent
   output that tries to scroll or rewrite into the status rows must be
   neutralized (existing `docs/terminal-checks.md` step 17 covers this
   manually; add a Go unit test).
7. **`translateKeys` / detach keys**: detach keys are translated from the
   input stream; ensure agent-provoked replies cannot synthesize a detach
   either (same corpus as test 1).

## Phase 2 — Go native fuzzing

Add `internal/relay/fuzz_test.go` (native `FuzzX` targets, corpus seeded
from Phase 1 inputs; run in CI via `go test -fuzz=… -fuzztime=60s` per
target, seeds run on every normal `go test`):

1. `FuzzKeyFind(f)`: `Key.find` over random bytes for every valid monitor
   key. Property: a match requires either (a) the exact byte `k.Byte`, or
   (b) a kitty sequence with ctrl bit set, correct code, no release, no
   unknown modifier bits. Any match outside these shapes is a failure.
2. `FuzzInputParser(f)`: `inputParser.feed` over random bytes, split at
   random points (two `feed` calls per iteration). Property: never panics,
   never emits a monitor-key `KeyMsg` unless the input contains one of the
   two valid encodings outside paste mode, and always preserves bytes
   destined for the agent pty (compare concatenated output against a
   reference translate).
3. `FuzzDecodeCtrl(f)`: `decodeCtrl` over random `ESC [`-prefixed data:
   no panics; returned events always have the ctrl bit; width never
   exceeds the buffer.
4. `FuzzHintFilter(f)`: `hintFilter.feed` with random data split at random
   offsets: no panics; `held` never grows unbounded across many calls
   (bounded by the 512-byte rule); concatenated `clean` + held reassembly
   equals input modulo well-formed hints.
5. `FuzzRegionFilter(f)`: `regionFilter.feed` with random escape-heavy
   data: no panics; output never addresses rows inside the status region.
6. `FuzzVT(f)` in `internal/vt`: random byte streams must not panic or
   OOM (screen dimensions bounded), so the rendered status line is
   computed from a well-defined state.

Success criteria: no panics, and — via the property assertions — proof
that agent-influenced bytes cannot match the monitor key except when they
are the genuine user keypress encodings.

## Phase 3 — end-to-end harness (test/inline.py extension)

Extend `test/inline.py` (or a sibling `test/security.py` driving the same
`spawn` plumbing) with a scripted agent process inside the session that:

1. Sprays every query/reply-inducing sequence from Phase 1 at its pty in
   bursts (with and without an attached terminal, with pi running so the
   kitty protocol is active on the surface).
2. Asserts over a fixed window: the screen never switches to the monitor
   view, the status line only changes in response to real proxy events,
   and the session output is paused only by a real user Ctrl+Space
   (injected on the *outer* pty by the harness, which must still work —
   the isolation must not break the feature).
3. Covers `monitor.key` set to `ctrl-a` and `ctrl-]` to prove isolation is
   key-independent.

## Phase 4 — CI and docs wiring

- Wire Phase 1–2 into the existing test suite (`docs/testing.md` gets a
  short section listing the fuzz targets and their properties).
- Add a manual step to `docs/terminal-checks.md` (after step 17) covering
  the Phase 3 script.
- Record the verdict and remaining rationale in the user-facing doc
  (plans/0034-monitor-key-isolation-doc.md).

## Deliverables

- `internal/relay/security_test.go` (Phase 1)
- `internal/relay/fuzz_test.go` (Phase 2, six targets)
- `internal/vt` fuzz target (Phase 2.6)
- `test/security.py` (Phase 3)
- `docs/testing.md` + `docs/terminal-checks.md` updates (Phase 4)
