# hsh rename

The `hsh rename` command names a session. A name lets you re-enter the session
with `hsh resume NAME`. The command also changes a name or removes a name.

<!-- tape:output tools/terminal-demos/out/rename.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## Name a session

Type the command. Give the session id and the new name.

<!-- tape:run sleep=2s -->
```sh
hsh rename 1a2b3c my-session
```
<!-- tape:wait /is now named/ -->

## Remove a name

Add the `--clear` flag. Give the session.

<!-- tape:run sleep=2s -->
```sh
hsh rename --clear my-session
```
<!-- tape:wait /is unnamed/ -->

<!-- tape END -->

## Reference

Usage:

```
hsh rename SESSION NAME
hsh rename --clear SESSION
```

| Flag | Function |
|------|----------|
| `--clear` | Remove the session name. |
