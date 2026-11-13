# Demo: build and run a web server inside an hsh sandbox

This walkthrough launches Claude Code in an `hsh` sandbox, has it install Go and
write a web server, approves the egress to `go.dev` through the monitor, then
forwards the port to try the server from the host.

<!-- tape:output out/basics.webm -->
<!-- tape:require hsh -->
<!-- tape:set Shell "bash" -->
<!-- tape:set WaitTimeout 3m -->
<!-- tape:set Theme { "background": "#ffffff", "foreground": "#000000", "cursor": "#000000", "selection": "#b3d7ff", "black": "#000000", "red": "#c91b00", "green": "#00a600", "yellow": "#999900", "blue": "#0225c7", "magenta": "#ca30c7", "cyan": "#00a6a6", "white": "#777777", "brightBlack": "#555555", "brightRed": "#ff4b3e", "brightGreen": "#00d900", "brightYellow": "#e6e600", "brightBlue": "#4c7bff", "brightMagenta": "#ff57ff", "brightCyan": "#00e6e6", "brightWhite": "#000000" } -->
<!-- tape:set FontSize 22 -->
<!-- tape:set Width 1280 -->
<!-- tape:set Height 720 -->
<!-- tape:set TypingSpeed 60ms -->

<!-- tape START -->

<!-- tape:run sleep=none -->
```sh
# ensure the session is clean
export HSH_SESSION_NAME=forward-example
hsh rm -f $HSH_SESSION_NAME; echo CLEANUP-FI""NISHED
```
<!-- tape:wait /CLEANUP-FINISHED/ -->

## Step 1 — launch Claude Code in a sandbox

<!-- tape:run sleep=10s -->
```sh
clear
hsh run claude-code .
```

## Step 2 — ask the agent to build a web server

The agent will try to reach `go.dev`, which the
proxy holds for approval.

<!-- tape:run sleep=none -->
```text
1. install golang from fedora
2. build and run a Go based hello world web server on port 8080
```

The agent works. When the go.dev request is held, hsh suspends the agent and
the status line shows the suspension — the tape waits for exactly that:

## Step 3 — approve the first Fedora mirrors request

<!-- tape:wait /SUSPENDED: proxy approval/ -->
Bring up the monitor, approve the pending requests to connect to Fedora mirrors, and confirm.

<!-- tape:monitor -->
<!-- tape:sleep 500ms -->
<!-- tape:type a -->
<!-- tape:sleep 500ms -->
<!-- tape:key Enter -->
<!-- tape:sleep 1s -->
<!-- tape:monitor -->
<!-- tape:sleep 1s -->

<!-- tape:wait /SUSPENDED: proxy approval/ -->

<!-- tape:monitor -->
<!-- tape:sleep 500ms -->
<!-- tape:type a -->
<!-- tape:sleep 500ms -->
<!-- tape:key Enter -->
<!-- tape:sleep 1s -->
<!-- tape:monitor -->

## Step 4 — forward the port and try the web server

Open the monitor again, start a forward, and point it at port 8080.

<!-- tape:wait /1 shell still running/ -->

<!-- tape:monitor -->
<!-- tape:type forward start -->
<!-- tape:key Enter -->
<!-- tape:wait /8080/ -->
<!-- tape:sleep 500ms -->
<!-- tape:type 8080 -->
<!-- tape:sleep 500ms -->
<!-- tape:key Enter -->
<!-- tape:sleep 500ms -->
<!-- tape:type detach session -->
<!-- tape:sleep 500ms -->
<!-- tape:key Enter -->
<!-- tape:sleep 3000ms -->

## Step 5 — hit the server from the host

<!-- tape:run -->
```sh
clear && sleep 1
```

<!-- tape:run -->
```sh
# now outside hsh sandbox
curl http://localhost:8080
```

<!-- tape:run sleep=3s -->
```sh
hsh resume
```
<!-- tape:run sleep=2s -->
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
