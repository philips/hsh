We need to expand the monitor that is brough up with `hsh monitor` or
Ctrl+Space to have a type-ahead command pallette like Obsidian. We can use the
bubbletea list to accomplish the fuzzy filtering.
https://github.com/charmbracelet/bubbletea/blob/main/examples/list-fancy/main.go

The initial list of subcommands include:

Proxy: review denied requests
Proxy: add new allowlist item
Global Settings: change monitor key

When the user presses Ctrl+Space it should bring up the subcommand list UNLESS there is a proxy item in need of review. Then it will bring up the proxy list.

The UX for the proxy review is not set in stone- it is OK to change the approval UX to match this new command pallete format
