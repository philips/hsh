# Keep the command documentation working over time

> **Status: proposed.**

## Goal

The command pages in `internal-docs/Documentation-staging/` are both prose and
the source for terminal recordings. Each page shows real `hsh` commands and the
output they produce. The commands and the output drift as the CLI changes. This
plan describes how to test the pages so they keep working over time.

Three kinds of drift break a page:

1. A command string in a page is no longer valid. A flag was renamed or removed,
   or a subcommand moved.
2. A `tape:wait /REGEX/` assertion no longer matches the real output. The output
   text changed.
3. The recording tooling (`md2tape.py`, VHS) can no longer convert the page.

The plan addresses each kind.

## Markup already present

Each page carries machine-readable markup:

- `<!-- tape:run ... -->` before a fenced `sh` block marks a command to run.
- `<!-- tape:wait /REGEX/ -->` after a command marks the output to expect.
- `<!-- tape START -->` / `<!-- tape END -->` mark the recorded region.

The test harness reads the same markup. No extra annotation is needed.

## Layer 1 — markup lints (fast, no sandbox, runs in CI)

A small checker parses each page and fails on a structural problem. This layer
needs no container and no network. It runs on every push.

Checks:

- Every page converts with `md2tape.py` without an error.
- `md2tape.py --script PAGE` produces a script without an error.
- Each `tape START` has a matching `tape END`.
- Each `tape:run` is followed by a fenced code block.
- Each command's first word is `hsh` (catch stray prose in a run block).
- Each `tape:output` path is under `tools/terminal-demos/out/`.

Put the checker at `tools/terminal-demos/lint_docs.py`. Wire it into CI next to
the existing Go tests.

## Layer 2 — command validity (medium, no side effects)

This layer confirms that each command string is a real command with real flags.
It does not start a sandbox. It parses the command line against the CLI.

Approach: add a hidden check mode to the CLI, for example
`hsh __check "run --rm pi ."`. The mode parses the arguments with the same `ff`
flag sets the real commands use. The mode reports an unknown command or an
unknown flag. The mode does not run the command.

The test harness extracts each command with `md2tape.py --script`, strips the
value arguments, and feeds each command to `hsh __check`. A rename of a flag
then fails this layer with the page name and the bad command.

This layer catches drift kind 1 (invalid command) without a container.

## Layer 3 — end-to-end replay (slow, gated, nightly)

This layer runs the commands against a live sandbox and confirms the output. It
needs podman and the built `hsh` binary. It runs nightly, not on every push.

Approach:

1. Build `hsh` with `./build.sh`.
2. Run each command from the page in order. Capture the stdout and the stderr of
   each command. Run the commands in a clean, disposable environment (a fresh
   `HOME` and a fresh `XDG_CONFIG_HOME`), with a known image and a throwaway
   workspace.
3. After a command that has a `tape:wait /REGEX/`, grep the captured output for
   the regex. A miss means the output drifted. Report the page and the command.

The harness parses the page itself. It does not rely on `md2tape.py --script`
for the assertion. The `--script` mode is for live human debugging: it turns a
`tape:wait /REGEX/` into `until /REGEX/ ...`, which is not a valid shell test.
The harness instead reads each `tape:wait /REGEX/` and matches the regex against
the real output it captured.

A non-zero exit from a command, or a `tape:wait` miss, means the page drifted.
Report the page and the step.

An alternative keeps the script route viable: add a shell-command wait form to
the markup, for example `<!-- tape:wait hsh secret ls | grep -q GITHUB_TOKEN -->`.
The `--script` mode already turns a shell-command wait into an `until ... ; sleep
0.5; done` poll. Choose one route; do not mix the regex form and the
shell-command form for the same assertion.

Make the scripts safe to replay:

- Prefer `hsh run --rm` so a session cleans itself up.
- Add a teardown step that runs `hsh rm --all --force` and `hsh wipe`.
- Keep destructive pages (`rm`, `wipe`) in a sub-suite that runs last in the
  disposable environment only.

This layer catches drift kind 1 and kind 2 (wrong expected output).

## Prefer screen assertions over fixed timing

A `Sleep` hides drift, because a demo still records even when the output is
wrong. A `tape:wait /REGEX/` fails when the output is wrong. So:

- Write a `tape:wait /REGEX/` after every command that prints a known result.
- Keep `tape:sleep` only for a visual pause, not as a correctness check.

Layer 3 depends on this rule: a page with no `tape:wait` can only fail on a
non-zero exit, not on wrong text.

## Keep the reference tables honest

Each page has a flag table built from the CLI help. A flag table drifts when the
help changes. Add a generator test in Go:

- The test renders the help for each command (the `ff` command tree already
  exists in `internal/cli`).
- The test checks that every flag named in a page's reference table still exists
  in the command's flag set, and that no flag is missing from the table.
- The test lives beside the existing `internal/cli/help_test.go`.

This test is fast, needs no sandbox, and runs on every push. It catches a flag
rename at the source, before Layer 2 or Layer 3.

## Rollout

1. Move the reviewed pages out of `Documentation-staging/` to their final home.
2. Add `tools/terminal-demos/lint_docs.py` and wire Layer 1 into CI.
3. Add the `hsh __check` mode and the Layer 2 harness.
4. Add the Go reference-table test.
5. Add the nightly Layer 3 job with a disposable environment and teardown.

## Open questions

- Which image should Layer 3 use? A small, pinned image keeps the run fast and
  deterministic.
- Should Layer 3 record the video too, to catch VHS regressions, or only replay
  the extracted script? The script is faster; the video catches more.
- How to assert on a TUI page (`hsh monitor`)? A screen assertion needs the
  recorded frame, not the script. Consider a frame capture with
  `Screenshot out/frame.png` plus an image diff, or exclude TUI pages from
  Layer 3.
