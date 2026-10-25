#!/usr/bin/env bash
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
DUMMY_PID=""
PROXY_PID=""
PASSED=0
cleanup() {
	if [ -n "$DUMMY_PID" ]; then
		kill "$DUMMY_PID" 2>/dev/null || true
	fi
	if [ -n "$PROXY_PID" ]; then
		kill "$PROXY_PID" 2>/dev/null || true
	fi
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

echo "== building harnessh and the dummy endpoint"
(cd "$REPO" && CGO_ENABLED=0 go build -o "$TMP/harnessh" .)
(cd "$REPO" && go build -o "$TMP/dummyopenai" ./test/dummyopenai)

echo "== ensuring the example image exists"
if ! podman image exists localhost/harnessh-example:latest; then
	(cd "$REPO" && podman build -q -t harnessh-example examples/)
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
cat >"$TMP/config.harnessh" <<EOF
{
  "image": "localhost/harnessh-example:latest",
  "shell": "/bin/bash",
  "mounts": ["$TMP/workspace:/workspace"],
  "secrets": {
    "API_KEY": { "from": "HARNESSH_TEST_SECRET", "hosts": ["api.mistral.ai"] }
  },
  "network": { "allow": ["host.containers.internal"] }
}
EOF

export HARNESSH_TEST_SECRET=test-secret-value-123
HARNESSH="$TMP/harnessh"
DB="$TMP/denied.db"

echo "== step 1: curl to a not-yet-allowed endpoint is denied (407) and recorded"
"$HARNESSH" run --config "$TMP/config.harnessh" --denied-db "$DB" >"$TMP/session1.out" 2>&1 <<'CMDS' || true
curl -sS -o /dev/null -w 'code=%{http_code}\n' https://api.mistral.ai
echo "curl-rc=$?"
CMDS
grep -q "response 407" "$TMP/session1.out" || fail "expected the proxy to deny api.mistral.ai with 407; got: $(tail -3 "$TMP/session1.out")"
pass "api.mistral.ai denied with 407 before being allowlisted"

DENIED="$("$HARNESSH" proxy-denied --db "$DB")"
echo "$DENIED" | grep -q "api.mistral.ai" || fail "denial was not recorded in the database"
pass "denial recorded in the sqlite database"

echo "== step 2: proxy-auto-allow adds the denied host (TOFU)"
OUT="$("$HARNESSH" proxy-auto-allow --config "$TMP/config.harnessh" --db "$DB")"
echo "$OUT"
echo "$OUT" | grep -q "added api.mistral.ai" || fail "proxy-auto-allow did not report adding api.mistral.ai"
grep -q '"api.mistral.ai"' "$TMP/config.harnessh" || fail "config file was not updated"
pass "api.mistral.ai added to network.allow"

echo "== step 3: sandbox session — allowed endpoint, workspace mount, pi through the proxy"
"$HARNESSH" run --config "$TMP/config.harnessh" --denied-db "$DB" >"$TMP/session2.out" 2>&1 <<'CMDS' || true
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

REMAINING="$("$HARNESSH" proxy-denied --db "$DB")"
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

DNSDENIED="$("$HARNESSH" proxy-denied --db "$DB" --dns)"
echo "$DNSDENIED" | grep -q "dns-denied.example.net" || fail "DNS denial was not recorded in the dns_denied table"
if echo "$REMAINING" | grep -q "dns-denied.example.net"; then
	fail "DNS denial leaked into the HTTP review list (tables must be separate)"
fi
pass "DNS denials recorded in a separate table, not in the HTTP review list"

echo "== step 4: standalone proxy — allowlist and secret injection"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -keyout "$TMP/ca.key" -out "$TMP/ca.crt" -days 1 -nodes -subj "/CN=harnessh-test" >/dev/null 2>&1
cat >"$TMP/proxy-config.harnessh" <<'EOF'
{ "network": { "allow": ["localhost"] } }
EOF
cat >"$TMP/proxy-secrets.json" <<'EOF'
{ "secrets": [ { "name": "API_KEY", "value": "test-secret-value-123", "header": "Authorization", "prefix": "Bearer ", "hosts": ["localhost"] } ] }
EOF
"$HARNESSH" proxy --listen 127.0.0.1:18080 --config "$TMP/proxy-config.harnessh" --secrets "$TMP/proxy-secrets.json" --ca-cert "$TMP/ca.crt" --ca-key "$TMP/ca.key" >"$TMP/proxy.out" 2>&1 &
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

echo
echo "ALL FUNCTIONAL TESTS PASSED"
PASSED=1
