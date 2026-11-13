# hsh settings

The `hsh settings` command edits the global hsh settings. The command also edits
the overrides for one session. A global setting applies everywhere. A session
override replaces the global setting for that session.

<!-- tape:output tools/terminal-demos/out/settings.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## Edit the global settings

Type the command. The command opens the settings file in `$EDITOR`.

<!-- tape:run sleep=2s -->
```sh
hsh settings edit
```
<!-- tape:sleep 1s -->

<!-- tape END -->

## Reference

Usage:

```
hsh settings SUBCOMMAND
```

### Subcommands

| Subcommand | Function |
|------------|----------|
| `edit` | Edit the global settings, or the overrides of one session with `--session`. |

### Flags for `edit`

| Flag | Function |
|------|----------|
| `--session STRING` | Edit the approval and suspend overrides of this session. |
