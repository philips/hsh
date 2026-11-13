# Documentation staging

This directory holds draft command pages for `hsh`. Each page is a stub. Review
each page. Edit each page. When a page is correct, move it to its final home.

The text uses ASD-STE100 (Simplified Technical English):

- Write short sentences. Use 20 words or fewer in an instruction.
- Write one instruction in one sentence.
- Use the active voice.
- Use the present tense.
- Start an instruction with a verb. For example: "Type the command."
- Use the approved word for one meaning only. Do not use synonyms.
- Use articles such as "the" and "a".

## Page layout

Each page has three parts:

1. A recording configuration block at the top. The block is HTML comments. The
   block sets the output file and the terminal size.
2. A demo region between `<!-- tape START -->` and `<!-- tape END -->`. The tool
   `tools/terminal-demos/md2tape.py` records only this region.
3. A reference section after the demo region. The reference section lists the
   flags. The recorder ignores this section.

## Markup for terminal-demos

The pages use the directives that `md2tape.py` reads. The directives are HTML
comments. A reader on GitHub or a blog does not see them. Use these directives:

| Directive | Function |
|-----------|----------|
| `<!-- tape:output PATH -->` | Set the video output path. |
| `<!-- tape:set KEY VALUE -->` | Set a VHS option, such as `FontSize` or `Width`. |
| `<!-- tape START -->` / `<!-- tape END -->` | Mark the recorded region. |
| `<!-- tape:run sleep=2s -->` | Type the next code block and press Enter. Then wait. |
| `<!-- tape:run noenter -->` | Type the next code block. Do not press Enter. |
| `<!-- tape:wait /REGEX/ -->` | Wait until the screen shows text that matches REGEX. |
| `<!-- tape:hide -->` / `<!-- tape:show -->` | Stop and start the recording of keystrokes. |
| `<!-- tape:sleep 1s -->` | Wait for the given time. |
| `<!-- tape:key Enter -->` | Send one key, such as `Enter` or `Ctrl+C`. |
| `<!-- tape:type TEXT -->` | Type text without a code block. |
| `<!-- tape:monitor -->` | Open the monitor palette with Ctrl+Space. |

## The `tape:wait` directive does two jobs

The `tape:wait` directive makes the recording wait for output. The same
directive works as a test assertion. A test runner reads each `tape:wait` and
confirms that the command prints matching text. See the test plan in
`/plans/0054-documentation-testing.md`.

Write a `tape:wait` after each command that prints a known result. The wait
makes the demo reliable. The wait also keeps the page testable over time.

## Convert a page to a recording

```sh
python3 tools/terminal-demos/md2tape.py internal-docs/Documentation-staging/PAGE.md -o tools/terminal-demos/out/PAGE.tape
vhs tools/terminal-demos/out/PAGE.tape
```

Or use the wrapper:

```sh
tools/terminal-demos/render.sh preview internal-docs/Documentation-staging/PAGE.md
```

## Pages

| Page | Command |
|------|---------|
| `run.md` | `hsh run` |
| `resume.md` | `hsh resume` |
| `rename.md` | `hsh rename` |
| `ls.md` | `hsh ls` |
| `rm.md` | `hsh rm` |
| `detach.md` | `hsh detach` |
| `suspend.md` | `hsh suspend` |
| `secret.md` | `hsh secret` and its subcommands |
| `proxy.md` | `hsh proxy` and its subcommands |
| `policy.md` | `hsh policy` and its subcommands |
| `monitor.md` | `hsh monitor` |
| `oauth.md` | `hsh oauth` and its subcommands |
| `ssh.md` | `hsh ssh` and its subcommands |
| `forward.md` | `hsh forward` |
| `settings.md` | `hsh settings` |
| `version.md` | `hsh version` |
| `update.md` | `hsh update` |
| `wipe.md` | `hsh wipe` |
