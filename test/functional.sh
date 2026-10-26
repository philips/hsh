#!/usr/bin/env bash
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
DUMMY_PID=""
PROXY_PID=""
RESUME_POD=""
TEST_PODS=""
PASSED=0
cleanup() {
	if [ -n "$DUMMY_PID" ]; then
		kill "$DUMMY_PID" 2>/dev/null || true
	fi
	if [ -n "$PROXY_PID" ]; then
		kill "$PROXY_PID" 2>/dev/null || true
	fi
	if [ -n "$RESUME_POD" ]; then
		podman pod rm -f "$RESUME_POD" >/dev/null 2>&1 || true
	fi
	for pod in $TEST_PODS; do
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
(cd "$REPO" && CGO_ENABLED=0 go build -o "$TMP/hsh" .)
(cd "$REPO" && go build -o "$TMP/dummyopenai" ./test/dummyopenai)

echo "== ensuring the example image exists"
if ! podman image exists localhost/hsh-example:latest; then
	(cd "$REPO" && podman build -q -t hsh-example examples/)
fi

echo "== starting the dummy OpenAI-compatible endpoint on 0.0.0.0:8899"
DUMMY_ID="functional-$$"
"$TMP/dummyopenai" -addr 0.0.0.0:8899 -log "$TMP/requests.log" -id "$DUMMY_ID" &
DUMMY_PID=$!
for _ in $(seq 1 50); do
	if [ "$(curl -s --max-time 2 http://127.0.0.1:8899/id)" = "$DUMMY_ID" ]; then
		break
	fi
	sleep 0.1
done
[ "$(curl -s --max-time 2 http://127.0.0.1:8899/id)" = "$DUMMY_ID" ] || fail "dummy endpoint did not come up on 127.0.0.1:8899 (is the port already in use?)"

mkdir -p "$TMP/workspace"
echo meow >"$TMP/workspace/test.md"
cat >"$TMP/config.hsh" <<EOF
{
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash",
  "mounts": ["$TMP/workspace:/workspace"],
  "secrets": {
    "API_KEY": { "from": "HSH_TEST_SECRET", "hosts": ["api.mistral.ai"] }
  },
  "network": { "allow": ["host.containers.internal"] }
}
EOF

export HSH_TEST_SECRET=test-secret-value-123
HSH="$TMP/hsh"
DB="$TMP/denied.db"

echo "== step 1: curl to a not-yet-allowed endpoint is denied (407) and recorded"
"$HSH" run --config "$TMP/config.hsh" --denied-db "$DB" >"$TMP/session1.out" 2>&1 <<'CMDS' || true
curl -sS -o /dev/null -w 'code=%{http_code}\n' https://api.mistral.ai
echo "curl-rc=$?"
CMDS
grep -q "response 407" "$TMP/session1.out" || fail "expected the proxy to deny api.mistral.ai with 407; got: $(tail -3 "$TMP/session1.out")"
pass "api.mistral.ai denied with 407 before being allowlisted"

DENIED="$("$HSH" proxy-denied --db "$DB")"
echo "$DENIED" | grep -q "api.mistral.ai" || fail "denial was not recorded in the database"
pass "denial recorded in the sqlite database"

echo "== step 2: proxy-auto-allow adds the denied host (TOFU)"
OUT="$("$HSH" proxy-auto-allow --config "$TMP/config.hsh" --db "$DB")"
echo "$OUT"
echo "$OUT" | grep -q "added api.mistral.ai" || fail "proxy-auto-allow did not report adding api.mistral.ai"
grep -q '"api.mistral.ai"' "$TMP/config.hsh" || fail "config file was not updated"
pass "api.mistral.ai added to network.allow"

echo "== step 3: sandbox session — allowed endpoint, workspace mount, pi through the proxy"
"$HSH" run --config "$TMP/config.hsh" --denied-db "$DB" >"$TMP/session2.out" 2>&1 <<'CMDS' || true
echo "--- curl to the now-allowed endpoint (intercepted, secret injected):"
curl -sS -o /dev/null -w 'code=%{http_code}\n' https://api.mistral.ai
echo "curl-rc=$?"
echo "--- workspace mount:"
cat /workspace/test.md
echo "--- configuring pi for the dummy endpoint:"
mkdir -p "$HOME/.pi/agent"
cat >"$HOME/.pi/agent/models.json" <<'JSON'
{
  "providers": {
    "dummy": {
      "baseUrl": "http://host.containers.internal:8899/v1",
      "api": "openai-completions",
      "apiKey": "dummy-key",
      "models": [
        {
          "id": "dummy-model",
          "name": "Dummy Model",
          "reasoning": false,
          "input": ["text"],
          "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 },
          "contextWindow": 128000,
          "maxTokens": 8192
        }
      ]
    }
  }
}
JSON
cat >"$HOME/.pi/agent/settings.json" <<'JSON'
{ "defaultProvider": "dummy", "defaultModel": "dummy-model" }
JSON
echo "--- echo hi | pi:"
echo hi | pi
echo "pi-rc=$?"
echo "--- DNS via the proxy forwarder:"
getent hosts api.mistral.ai | head -1
echo "getent-allowed-rc=$?"
getent hosts dns-denied.example.net >/dev/null 2>&1
echo "getent-denied-rc=$?"
curl -sS --noproxy '*' --max-time 8 -o /dev/null https://api.mistral.ai 2>&1 | head -1
CMDS
cat "$TMP/session2.out"

grep -Eq '^code=[1-5][0-9][0-9]$' "$TMP/session2.out" || fail "curl to the allowed endpoint returned no HTTP status"
grep -q '^curl-rc=0$' "$TMP/session2.out" || fail "curl to the allowed endpoint failed"
pass "proxy allows the endpoint after auto-allow (real upstream response, intercepted)"

grep -qx 'meow' "$TMP/session2.out" || fail "workspace mount not visible"
pass "workspace mount works"

grep -q 'hello world' "$TMP/session2.out" || fail "pi did not return the dummy endpoint's response"
grep -q '^pi-rc=0$' "$TMP/session2.out" || fail "pi exited with an error"
pass "echo hi | pi returns hello world through the proxy"

grep -q 'POST /v1/chat/completions' "$TMP/requests.log" || fail "the dummy endpoint never received pi's request"
pass "pi's request reached the dummy endpoint through the proxy"

REMAINING="$("$HSH" proxy-denied --db "$DB")"
echo "$REMAINING" | grep -q "no denied hosts awaiting review" || fail "review list was not cleared after auto-allow"
pass "denial review list cleared"

grep -Eq '^[0-9a-fA-F:.]+[[:space:]]+api\.mistral\.ai' "$TMP/session2.out" || fail "DNS resolution of the allowlisted host failed"
pass "DNS resolves the allowlisted host through the proxy forwarder"

grep -Eq '^getent-denied-rc=[1-9]' "$TMP/session2.out" || fail "DNS resolution of a non-allowlisted host unexpectedly succeeded"
grep -q 'Failed to connect' "$TMP/session2.out" || fail "direct connection did not fail at the TCP layer"
if grep -q 'Could not resolve host' "$TMP/session2.out"; then
	fail "direct connection failed at DNS instead of TCP (resolution should succeed, egress stays blocked)"
fi
pass "non-allowlisted names are refused; allowed names resolve but stay unreachable directly"

DNSDENIED="$("$HSH" proxy-denied --db "$DB" --dns)"
echo "$DNSDENIED" | grep -q "dns-denied.example.net" || fail "DNS denial was not recorded in the dns_denied table"
if echo "$REMAINING" | grep -q "dns-denied.example.net"; then
	fail "DNS denial leaked into the HTTP review list (tables must be separate)"
fi
pass "DNS denials recorded in a separate table, not in the HTTP review list"

echo "== step 4: standalone proxy — allowlist and secret injection"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -keyout "$TMP/ca.key" -out "$TMP/ca.crt" -days 1 -nodes -subj "/CN=hsh-test" >/dev/null 2>&1
cat >"$TMP/proxy-config.hsh" <<'EOF'
{ "network": { "allow": ["localhost"] } }
EOF
cat >"$TMP/proxy-secrets.json" <<'EOF'
{ "secrets": [ { "name": "API_KEY", "value": "test-secret-value-123", "header": "Authorization", "prefix": "Bearer ", "hosts": ["localhost"] } ] }
EOF
"$HSH" proxy --listen 127.0.0.1:18080 --config "$TMP/proxy-config.hsh" --secrets "$TMP/proxy-secrets.json" --ca-cert "$TMP/ca.crt" --ca-key "$TMP/ca.key" >"$TMP/proxy.out" 2>&1 &
PROXY_PID=$!
for _ in $(seq 1 50); do
	if curl -s -o /dev/null -x http://127.0.0.1:18080 http://localhost:8899/v1/models; then
		break
	fi
	sleep 0.1
done

CODE="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer fake-key-from-client" -x http://127.0.0.1:18080 http://localhost:8899/v1/models)"
[ "$CODE" = "200" ] || fail "standalone proxy did not forward to the allowed host (got $CODE)"
pass "standalone proxy forwards to the allowlisted host"

grep -q 'Authorization: Bearer test-secret-value-123' "$TMP/requests.log" || fail "the secret was not injected as Authorization: Bearer into the forwarded request"
if grep -q 'fake-key-from-client' "$TMP/requests.log"; then
	fail "the client's placeholder credential was not replaced by the injected secret"
fi
pass "secret injected as Authorization: Bearer, replacing the client's placeholder"

CODE="$(curl -s -o /dev/null -w '%{http_code}' -x http://127.0.0.1:18080 http://example.com/)"
[ "$CODE" = "407" ] || fail "standalone proxy did not deny a non-allowlisted host with 407 (got $CODE)"
pass "standalone proxy denies non-allowlisted hosts with 407"

kill "$PROXY_PID" 2>/dev/null || true
PROXY_PID=""

echo "== step 5: --resume re-enters the same container"
export XDG_STATE_HOME="$TMP/xdg"
mkdir -p "$XDG_STATE_HOME"
RESUME_POD="$(podman pod ls -q --filter "label=hsh.workspace=$TMP/workspace" 2>/dev/null || true)"
if [ -n "$RESUME_POD" ]; then
	fail "unexpected existing sandbox for the test workspace"
fi
"$HSH" run --config "$TMP/config.hsh" --denied-db "$DB" --resume >"$TMP/resume1.out" 2>&1 <<'CMDS' || true
echo resume-marker > /home/hsh/marker.txt
echo "launch1-marker-written"
exit
CMDS
RESUME_POD="$(podman pod ls -q --filter "label=hsh.workspace=$TMP/workspace" 2>/dev/null || true)"
if [ -z "$RESUME_POD" ]; then
	fail "sandbox pod was not left behind after a --resume exit"
fi
pass "sandbox persists after a --resume exit"
"$HSH" run --config "$TMP/config.hsh" --denied-db "$DB" --resume >"$TMP/resume2.out" 2>&1 <<'CMDS' || true
echo "--- marker from the previous launch (same container?):"
cat /home/hsh/marker.txt
echo "--- proxied request after re-entry:"
curl -sS -o /dev/null -w 'code=%{http_code}\n' --max-time 15 https://api.mistral.ai
echo "--- DNS after re-entry:"
getent hosts api.mistral.ai | head -1
exit
CMDS
grep -qx 'resume-marker' "$TMP/resume2.out" || fail "container state did not persist across --resume launches"
pass "same container re-entered; state persisted"
grep -Eq '^code=[1-5][0-9][0-9]$' "$TMP/resume2.out" || fail "proxy not working after re-entry"
pass "proxy works after re-entry"
grep -Eq '^[0-9a-fA-F:.]+[[:space:]]+api\.mistral\.ai' "$TMP/resume2.out" || fail "DNS not working after re-entry"
pass "DNS works after re-entry"
podman pod rm -f "$RESUME_POD" >/dev/null 2>&1 || true
RESUME_POD=""

echo "== step 6: hsh ls / hsh rm"
OTHER_HSH_PODS="$(podman pod ls --format '{{.Name}}' | grep '^hsh-' || true)"
POD_A="hsh-fn-a-$$"
POD_B="hsh-fn-b-$$"
TEST_PODS="$POD_A $POD_B"
podman pod create --name "$POD_A" --label "hsh.workspace=$TMP/workspace" >/dev/null
podman pod create --name "$POD_B" >/dev/null
podman run --pod "$POD_B" -d localhost/hsh-example:latest sleep 300 >/dev/null

OUT="$("$HSH" ls)"
echo "$OUT" | grep -q '^POD ID' || fail "hsh ls did not print a header"
echo "$OUT" | grep -q WORKSPACE || fail "hsh ls header has no WORKSPACE column"
echo "$OUT" | grep -q "$POD_A" || fail "hsh ls did not list the stopped sandbox"
echo "$OUT" | grep -q "$POD_B" || fail "hsh ls did not list the running sandbox"
echo "$OUT" | grep -q "$TMP/workspace" || fail "hsh ls did not show the workspace label"
pass "hsh ls lists sandboxes with name, status, and workspace"

POD_A_ID="$(podman pod inspect "$POD_A" --format '{{.Id}}')"
"$HSH" ls -q | grep -q "^$POD_A_ID$" || fail "hsh ls -q did not print pod IDs"
"$HSH" ls --no-trunc -q | grep -q "^$POD_A_ID$" || fail "hsh ls --no-trunc -q did not print the full ID"
pass "hsh ls -q and --no-trunc -q print pod IDs"

OUT="$("$HSH" ls --workspace "$TMP/workspace")"
echo "$OUT" | grep -q "$POD_A" || fail "hsh ls --workspace did not list the workspace's sandbox"
echo "$OUT" | grep -q "$POD_B" && fail "hsh ls --workspace listed a sandbox of another workspace"
pass "hsh ls --workspace filters by the hsh.workspace label"

if "$HSH" rm "$POD_B" >"$TMP/rm-running.out" 2>&1; then
	fail "hsh rm removed a sandbox with a running container without --force"
fi
grep -q "without force" "$TMP/rm-running.out" || fail "hsh rm did not explain the refusal: $(cat "$TMP/rm-running.out")"
pass "hsh rm refuses a running sandbox without --force"

if "$HSH" rm "hsh-fn-missing-$$" >/dev/null 2>&1; then
	fail "hsh rm succeeded on a missing sandbox without --ignore"
fi
"$HSH" rm --ignore "hsh-fn-missing-$$" >/dev/null 2>&1 || fail "hsh rm --ignore failed on a missing sandbox"
pass "hsh rm fails on a missing sandbox, succeeds with --ignore"

SESSION_DIR="$XDG_STATE_HOME/hsh/sessions/fn-a-$$"
mkdir -p "$SESSION_DIR"
echo ca >"$SESSION_DIR/ca.crt"
"$HSH" rm "$POD_A" >/dev/null || fail "hsh rm could not remove the stopped sandbox"
podman pod ls --format '{{.Name}}' | grep -qx "$POD_A" && fail "hsh rm reported success but the pod is still there"
[ ! -d "$SESSION_DIR" ] || fail "hsh rm did not remove the sandbox's session state directory"
pass "hsh rm removes a stopped sandbox and its session state"

"$HSH" rm -f -t 1 "$POD_B" >/dev/null || fail "hsh rm -f could not remove the running sandbox"
podman pod ls --format '{{.Name}}' | grep -qx "$POD_B" && fail "hsh rm -f reported success but the pod is still there"
pass "hsh rm -f stops and removes a running sandbox"

if [ -n "$OTHER_HSH_PODS" ]; then
	echo "SKIP: hsh rm --all check (other hsh sandboxes exist on this host)"
else
	POD_C="hsh-fn-c-$$"
	KEEP_POD="fn-keep-$$"
	TEST_PODS="$TEST_PODS $POD_C $KEEP_POD"
	podman pod create --name "$POD_C" --label "hsh.workspace=$TMP/workspace" >/dev/null
	podman run --pod "$POD_C" -d localhost/hsh-example:latest sleep 300 >/dev/null
	podman pod create --name "$KEEP_POD" >/dev/null
	mkdir -p "$XDG_STATE_HOME/hsh/sessions/fn-orphan-$$"
	"$HSH" rm --all -f >/dev/null || fail "hsh rm --all -f failed"
	if podman pod ls --format '{{.Name}}' | grep -q '^hsh-'; then
		fail "hsh rm --all -f left hsh sandboxes behind"
	fi
	podman pod ls --format '{{.Name}}' | grep -qx "$KEEP_POD" || fail "hsh rm --all removed a non-hsh pod"
	[ ! -d "$XDG_STATE_HOME/hsh/sessions" ] || [ -z "$(ls -A "$XDG_STATE_HOME/hsh/sessions")" ] || fail "hsh rm --all did not sweep session state directories"
	pass "hsh rm --all removes every sandbox and sweeps session state"
fi

echo "== step 7: proxy-watch requires a terminal"
"$HSH" proxy-watch --config "$TMP/config.hsh" --db "$DB" >"$TMP/watch.out" 2>&1 && fail "proxy-watch succeeded without a terminal"
grep -q "requires a terminal" "$TMP/watch.out" || fail "proxy-watch did not explain the missing terminal"
pass "proxy-watch refuses to run without a terminal"

echo
echo "ALL FUNCTIONAL TESTS PASSED"
PASSED=1
