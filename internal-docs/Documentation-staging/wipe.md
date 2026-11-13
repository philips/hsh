# hsh wipe

The `hsh wipe` command removes all the hsh configuration files. The command also
removes all the state files. The command asks for confirmation first. Caution:
this action is not reversible.

<!-- tape:output tools/terminal-demos/out/wipe.mp4 -->
<!-- tape:set Shell "bash" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

## Wipe the configuration

Type the command. The command asks for confirmation.

<!-- tape:run noenter -->
```sh
hsh wipe
```
<!-- tape:key Enter -->
<!-- tape:sleep 2s -->

<!-- tape END -->

## Reference

Usage:

```
hsh wipe
```

The command takes no flags other than `-h, --help`.
