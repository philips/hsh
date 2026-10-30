#!/usr/bin/env bash
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [ -z "${HSH_RUNSC:-}" ] && ! command -v runsc >/dev/null 2>&1; then
	echo "SKIP: runsc not found (install gVisor or set HSH_RUNSC)"
	exit 0
fi

TMP="$(mktemp -d)"
export XDG_STATE_HOME="$TMP/xdg"
mkdir -p "$XDG_STATE_HOME"
DUMMY_PID=""
PASSED=0
cleanup() {
	if [ -n "$DUMMY_PID" ]; then
		kill "$DUMMY_PID" 2>/dev/null || true
	fi
	for pod in $(podman pod ls --format '{{.Name}} {{.Labels}}' | awk -v t="hsh.workspace:$TMP" 'index($0, t) { print $1 }'); do
		podman pod rm -f "$pod" >/dev/null 2>&1 || true
	done
	if [ "$PASSED" = "1" ]; then
		rm -rf "$TMP"
	else
		echo "artifacts kept in $TMP" >&2
	fi
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

pass() {
	echo "PASS: $*"
}

echo "== building hsh and the dummy endpoint"
(cd "$REPO" && go build -o "$TMP/hsh" .)
(cd "$REPO" && go build -o "$TMP/dummyopenai" ./test/dummyopenai)
HSH="$TMP/hsh"

if ! podman image exists localhost/hsh-example:latest; then
	(cd "$REPO" && podman build -q -t hsh-example test/fixtures/)
fi

DUMMY_ID="gvisor-$$"
"$TMP/dummyopenai" -addr 0.0.0.0:8899 -log "$TMP/requests.log" -id "$DUMMY_ID" &
DUMMY_PID=$!
for _ in $(seq 1 50); do
	if [ "$(curl -s --max-time 2 http://127.0.0.1:8899/id)" = "$DUMMY_ID" ]; then
		break
	fi
	sleep 0.1
done
[ "$(curl -s --max-time 2 http://127.0.0.1:8899/id)" = "$DUMMY_ID" ] || fail "dummy endpoint did not come up on 127.0.0.1:8899"

mkdir -p "$TMP/workspace"
cat >"$TMP/config.hsh" <<EOF
{
  "version": 1,
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash",
  "mounts": ["$TMP/workspace:/workspace"],
  "secrets": {
    "API_KEY": { "from": "env://HSH_GVISOR_SECRET", "hosts": ["host.containers.internal"], "header": "Authorization", "prefix": "Bearer " }
  },
  "network": { "mode": "allowlist", "allow": ["host.containers.internal"] }
}
EOF
export HSH_GVISOR_SECRET=gvisor-secret-value-77
cd "$TMP/workspace"

echo "== step 1: a gVisor session proxies, injects secrets, and denies"
"$HSH" run --config "$TMP/config.hsh" --runtime gvisor --pull never >"$TMP/run1.out" 2>&1 <<'CMDS' || true
echo "kernel=$(uname -r)"
echo "proxyenv=$(env | grep -ci _proxy)"
echo "secretenv=$(env | grep -c gvisor-secret-value)"
curl -sS -o /dev/null -w 'allowed=%{http_code}\n' --max-time 15 http://host.containers.internal:8899/v1/models
curl -sS -w '\ndenied=%{http_code}\n' --max-time 15 https://api.mistral.ai/v1/models
curl -sS -o /dev/null -w 'iplit=%{http_code}\n' --max-time 5 https://1.1.1.1 || true
ping -c1 -W2 1.1.1.1 >/dev/null 2>&1 && echo ping=leak || echo ping=blocked
echo marker > /home/hsh/marker.txt
exit
CMDS
grep -q '^kernel=.*gvisor' "$TMP/run1.out" || fail "the workspace is not running under gVisor: $(cat "$TMP/run1.out")"
pass "the workspace runs under gVisor"
grep -qx 'proxyenv=0' "$TMP/run1.out" || fail "proxy variables are set in a netstack session"
grep -qx 'secretenv=0' "$TMP/run1.out" || fail "the secret leaked into the workspace environment"
pass "no proxy variables and no secret in the workspace environment"
grep -qx 'allowed=200' "$TMP/run1.out" || fail "allowlisted plain HTTP was not forwarded: $(cat "$TMP/run1.out")"
grep -q 'Authorization: Bearer gvisor-secret-value-77' "$TMP/requests.log" || fail "the secret was not injected"
pass "allowlisted traffic is forwarded transparently with the secret injected"
grep -qx 'denied=403' "$TMP/run1.out" || fail "a denied HTTPS host did not get 403: $(cat "$TMP/run1.out")"
grep -q 'blocked on purpose by the sandbox policy' "$TMP/run1.out" || fail "the denial message did not reach the client"
pass "a denied HTTPS host gets the 403 denial message over intercepted TLS"
grep -qx 'iplit=000' "$TMP/run1.out" || fail "a direct connection to an IP literal was not refused"
grep -qx 'ping=blocked' "$TMP/run1.out" || fail "ICMP left the sandbox"
pass "IP literals and ICMP are refused"
SESSION="$(ls "$XDG_STATE_HOME/hsh/sessions" | head -1)"
"$HSH" proxy denied --db "$XDG_STATE_HOME/hsh/monitor.db" 2>/dev/null | grep "$SESSION" | grep -q 'api.mistral.ai' || fail "the denial was not recorded in monitor.db"
pass "denials are recorded in monitor.db with the session id"

echo "== step 2: resuming a stopped gVisor session reconnects the netstack"
"$HSH" run --resume "$SESSION" >"$TMP/run2.out" 2>&1 <<'CMDS' || true
cat /home/hsh/marker.txt
curl -sS -o /dev/null -w 'resumed=%{http_code}\n' --max-time 15 http://host.containers.internal:8899/v1/models
exit
CMDS
grep -qx 'marker' "$TMP/run2.out" || fail "container state did not persist: $(cat "$TMP/run2.out")"
grep -qx 'resumed=200' "$TMP/run2.out" || fail "network did not work after resume: $(cat "$TMP/run2.out")"
pass "resume restarts the proxy and the sandbox reconnects"

echo "== step 3: killing the proxy cuts all egress"
"$HSH" run --resume "$SESSION" >"$TMP/run3.out" 2>&1 <<'CMDS' &
curl -sS -o /dev/null -w 'before=%{http_code}\n' --max-time 15 http://host.containers.internal:8899/v1/models
sleep 8
curl -sS -o /dev/null -w 'after=%{http_code}\n' --max-time 5 http://host.containers.internal:8899/v1/models || true
curl -sS -o /dev/null -w 'afterip=%{http_code}\n' --max-time 5 https://1.1.1.1 || true
exit
CMDS
RUN_PID=$!
sleep 5
podman kill "hsh-$SESSION-proxy" >/dev/null
wait "$RUN_PID" || true
grep -qx 'before=200' "$TMP/run3.out" || fail "egress did not work before the proxy was killed: $(cat "$TMP/run3.out")"
grep -qx 'after=000' "$TMP/run3.out" || fail "egress worked without the proxy: $(cat "$TMP/run3.out")"
grep -qx 'afterip=000' "$TMP/run3.out" || fail "egress to an IP worked without the proxy: $(cat "$TMP/run3.out")"
pass "the workspace has no egress without the proxy"

echo "== step 4: /hsh-sshd forwards ports, opens shells, and delivers callbacks inside the sandbox"
podman pod stop -t 2 "hsh-$SESSION" >/dev/null 2>&1 || true
"$HSH" run --resume "$SESSION" >"$TMP/run4.out" 2>&1 <<'CMDS' &
(while true; do /hsh-workspace-inner --http-echo 8000 >/dev/null 2>&1; done &)
sleep 40
exit
CMDS
RUN_PID=$!
for _ in $(seq 1 100); do
	if "$HSH" forward --session "$SESSION" --list 2>/dev/null | grep -q '^8000'; then
		break
	fi
	sleep 0.3
done
"$HSH" forward --session "$SESSION" --list 2>/dev/null | grep -q '^8000' || fail "hsh forward --list does not show the sandbox's listener"
pass "hsh forward --list sees the sandbox's listeners"
"$HSH" forward --session "$SESSION" 18800:8000 >"$TMP/fwd.out" 2>&1 &
FWD_PID=$!
sleep 3
CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:18800/)"
kill "$FWD_PID" 2>/dev/null || true
[ "$CODE" = "200" ] || fail "hsh forward did not reach the sandbox's port: $CODE $(cat "$TMP/fwd.out")"
pass "hsh forward reaches a server on the sandbox's loopback"
SSHO=(-F /dev/null -o "ProxyCommand=$HSH ssh proxy %h" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR)
ssh "${SSHO[@]}" "hsh-$SESSION" 'echo "kernel=$(uname -r)"; echo "cwd=$(pwd)"; grep NoNewPrivs /proc/self/status; curl -sS -o /dev/null -w "allowed=%{http_code}\n" --max-time 15 http://host.containers.internal:8899/v1/models' >"$TMP/ssh.out" 2>&1 || true
grep -q '^kernel=.*gvisor' "$TMP/ssh.out" || fail "ssh did not open a shell in the gVisor sandbox: $(cat "$TMP/ssh.out")"
grep -qx 'cwd=/workspace' "$TMP/ssh.out" || fail "ssh shell is not in the session's working directory"
grep -q 'NoNewPrivs:.*1' "$TMP/ssh.out" || fail "ssh shell does not have no_new_privs"
grep -qx 'allowed=200' "$TMP/ssh.out" || fail "ssh shell has no proxied network"
pass "ssh hsh-SESSION opens a restricted shell in the sandbox with the proxied network"
podman exec -d "hsh-$SESSION-ws" /hsh-workspace-inner --http-echo 1455 >/dev/null
sleep 1
"$HSH" oauth callback --session "$SESSION" "http://127.0.0.1:1455/cb?code=gvisorcode" >"$TMP/oauth.out" 2>&1 || fail "hsh oauth callback failed: $(cat "$TMP/oauth.out")"
grep -q 'HTTP/1.1 200 OK' "$TMP/oauth.out" || fail "the callback was not delivered: $(cat "$TMP/oauth.out")"
pass "hsh oauth callback delivers into the sandbox"
podman exec "hsh-$SESSION-ws" sh -c 'test -e /hsh-session || test -e /hsh-netstack' && fail "the workspace container can see proxy state"
pass "the sandbox has no access to the proxy's session state"
kill "$RUN_PID" 2>/dev/null || true
wait "$RUN_PID" 2>/dev/null || true

PASSED=1
echo
echo "ALL GVISOR TESTS PASSED"
