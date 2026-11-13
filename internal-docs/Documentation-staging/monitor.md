# hsh monitor

The `hsh monitor` command opens a command palette. The palette is a terminal
user interface (TUI). Use the palette to review the denied hosts. Use the
palette to add allowlist entries. Use the palette to change the settings. The
command needs a terminal.

<!-- tape:output tools/terminal-demos/out/monitor.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## Open the monitor

Type the command. The palette opens.

<!-- tape:run sleep=2s -->
```sh
hsh monitor
```
<!-- tape:sleep 2s -->

## Close the monitor

Press the escape key. The palette closes.

<!-- tape:key Escape -->
<!-- tape:sleep 1s -->

<!-- tape END -->

## Reference

Usage:

```
hsh monitor [FLAGS]
```

| Flag | Function |
|------|----------|
| `--all` | Show the denied hosts too. |
| `--session STRING` | Show one session only. Give the session id. |
| `--workspace STRING` | Show one workspace only. Give the directory path. |
