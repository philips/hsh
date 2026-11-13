# hsh update

The `hsh update` command updates hsh to the latest release from GitHub. The
command also turns automatic updates on or off.

<!-- tape:output tools/terminal-demos/out/update.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## Update to the latest release

Type the command. The command finds the latest release. The command installs
it.

<!-- tape:run sleep=3s -->
```sh
hsh update
```

## Turn on automatic updates

Add the `--auto` flag. hsh checks for a release in the background. hsh installs
the release on the next command.

<!-- tape:run sleep=2s -->
```sh
hsh update --auto
```

<!-- tape END -->

## Reference

Usage:

```
hsh update [--version vTAG] [--no-metrics]
hsh update [--auto | --no-auto]
hsh update --autoupdate-check
```

| Flag | Function |
|------|----------|
| `--version STRING` | Update to a specific tag, such as `v1.2.3`. |
| `--no-metrics` | Do not send the anonymous usage metrics. |
| `--auto` | Turn on automatic updates. |
| `--no-auto` | Turn off automatic updates. |
| `--autoupdate-check` | Find the latest release and record it. Do not update. |
