# Demo: build and run a web server inside an hsh sandbox

This walkthrough launches Claude Code in an `hsh` sandbox, has it install Go and
write a web server, approves the egress to `go.dev` through the monitor, then
forwards the port to try the server from the host.

<!-- tape:output demo/out/webserver.webm -->
<!-- tape:require hsh -->
<!-- tape:set Shell "bash" -->
<!-- tape:set Theme { "background": "#ffffff", "foreground": "#000000", "cursor": "#000000", "selection": "#b3d7ff", "black": "#000000", "red": "#c91b00", "green": "#00a600", "yellow": "#999900", "blue": "#0225c7", "magenta": "#ca30c7", "cyan": "#00a6a6", "white": "#777777", "brightBlack": "#555555", "brightRed": "#ff4b3e", "brightGreen": "#00d900", "brightYellow": "#e6e600", "brightBlue": "#4c7bff", "brightMagenta": "#ff57ff", "brightCyan": "#00e6e6", "brightWhite": "#000000" } -->
<!-- tape:set Theme "Catppuccin Latte" -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

<!-- tape:run -->
```sh
export HSH_SESSION_NAME=forward-example
hsh rm -f $HSH_SESSION_NAME
clear
```

## Step 1 — launch Claude Code in a sandbox

<!-- tape:run sleep=10s -->
```sh
hsh run claude-code .
```

## Step 2 — ask the agent to build a web server

We let the agent type its own task. It will try to reach `go.dev`, which the
proxy holds for approval.

<!-- tape:run sleep=none -->
```text
install the latest go from go.dev and build a hello world web server on port 8080
```

The agent works. When the go.dev request is held, hsh suspends the agent and
the status line shows the suspension — the tape waits for exactly that:

<!-- tape:wait /SUSPENDED: proxy approval/ -->

## Step 3 — approve the proxy requests to go.dev

Bring up the monitor, approve the pending request, and confirm.

<!-- tape:monitor -->
<!-- tape:sleep 500ms -->
<!-- tape:type a -->
<!-- tape:sleep 500ms -->
<!-- tape:key Enter -->
<!-- tape:sleep 3s -->
<!-- tape:monitor -->

## Step 4 — ask the agent to build a web server

We let the agent type its own task. It will try to reach `go.dev`, which the
proxy holds for approval.

<!-- tape:run sleep=8s -->
```text
go.dev access via egress proxy approved
```
<!-- tape:sleep 8s -->

Bring up the monitor, approve the pending request, and confirm.

<!-- tape:monitor -->
<!-- tape:sleep 500ms -->
<!-- tape:type a -->
<!-- tape:sleep 500ms -->
<!-- tape:key Enter -->
<!-- tape:sleep 3s -->
<!-- tape:monitor -->

<!-- tape:run sleep=8s -->
```text
dl.google.com access via egress proxy approved
```

<!-- tape:wait /SUSPENDED: proxy approval/ -->

## Step 4 — forward the port and try the web server

Open the monitor again, start a forward, and point it at port 8080.

<!-- tape:monitor -->
<!-- tape:type start a forward -->
<!-- tape:key Enter -->
<!-- tape:sleep 500ms -->
<!-- tape:type 8080 -->
<!-- tape:sleep 500ms -->
<!-- tape:key Enter -->
<!-- tape:sleep 3s -->
<!-- tape:type detatch session -->
<!-- tape:sleep 500ms -->
<!-- tape:key Enter -->

## Step 5 — hit the server from the host

<!-- tape:run sleep=3s -->
```sh
curl http://localhost:8080
```

<!-- tape:run sleep=3s -->
```sh
hsh resume
```
```text
thank you for watching
```
<!-- tape:monitor -->
<!-- tape:type exit -->
<!-- tape:sleep 500ms -->
<!-- tape:key Enter -->
<!-- tape:sleep 500ms -->
<!-- tape:key Enter -->

<!-- tape END -->
