# Isolating an agent with hsh

This page doubles as a blog post *and* the source for a screen-recorded demo.
The prose you are reading is the narration/voiceover. The `<!-- tape:... -->`
HTML comments are invisible when rendered on GitHub or a blog, but
`md2tape.py` turns them into a [VHS](https://github.com/charmbracelet/vhs) tape.

<!-- tape:output tools/terminal-demos/out/sessions.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

Everything above configures the recording. Everything below is the demo.
Only content between `tape START` / `tape END` is recorded, so you can keep
non-demo documentation in the same file.

<!-- tape START -->

## Step 1 — check for running sessions

First we confirm the sandbox is clean. `tape:run` types the next code block
and presses Enter, then waits 2s by default.

<!-- tape:run sleep=2s -->
```sh
hsh ls
```

## Step 2 — launch an isolated agent

Now we drop an agent into a sandbox. We slow the typing down for this hero
command, and give it longer to boot.

<!-- tape:run typingspeed=90ms sleep=4s -->
```sh
hsh run pi .
```

## Step 3 — run quiet setup off-camera

Sometimes you need setup steps the viewer shouldn't watch. `tape:hide` stops
recording keystrokes/output until `tape:show`.

<!-- tape:hide -->
<!-- tape:run sleep=1s -->
```sh
clear
```
<!-- tape:show -->

## Step 4 — show an egress denial

Type a command, pause for effect, then send a Ctrl+C to interrupt.

<!-- tape:run noenter -->
```sh
curl https://example.com
```
<!-- tape:sleep 1s -->
<!-- tape:key Enter -->
<!-- tape:sleep 3s -->
<!-- tape:key Ctrl+C -->

## Step 5 — wrap up

<!-- tape:type Thanks for watching! -->
<!-- tape:sleep 2s -->

<!-- tape END -->

## Appendix (not recorded)

This section is outside the tape region, so it is ignored by the recorder but
still renders in the blog post.
