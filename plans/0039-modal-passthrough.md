We need to have a single key that brings up the "hsh" mode. Lets default to Ctrl+Space. If a user needs to send the actual key they can tap it twice like tmux does. So, Ctrl+Space, Ctrl+Space send Ctrl+Space to the actual terminal.

Why it matters: Users running interactive agents (like Claude Code) or text editors never have to guess which keybindings are being swallowed.

Keep the configurable monitor key. Remove the Ctrl+] key. We will add that
feature back in 0040.
