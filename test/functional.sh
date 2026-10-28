#!/usr/bin/env bash
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
export XDG_STATE_HOME="$TMP/xdg"
mkdir -p "$XDG_STATE_HOME"
DUMMY_PID=""
PROXY_PID=""
OAUTH_PID=""
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
	if [ -n "$OAUTH_PID" ]; then
		kill "$OAUTH_PID" 2>/dev/null || true
	fi
	if [ -n "$RESUME_POD" ]; then
		podman pod rm -f "$RESUME_POD" >/dev/null 2>&1 || true
	fi
	for pod in $TEST_PODS; do
		podman pod rm -f "$pod" >/dev/null 2>&1 || true
	done
	if [ -n "${PREV_PI_IMAGE:-}" ]; then
		podman tag "$PREV_PI_IMAGE" ghcr.io/philips/hsh-pi:latest >/dev/null 2>&1 || true
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

echo "== building hsh and the dummy endpoint"
(cd "$REPO" && GOEXPERIMENT=simd CGO_ENABLED=0 go build -o "$TMP/hsh" .)
(cd "$REPO" && go build -o "$TMP/dummyopenai" ./test/dummyopenai)

echo "== ensuring the example image exists"
if ! podman image exists localhost/hsh-example:latest; then
	(cd "$REPO" && podman build -q -t hsh-example test/fixtures/)
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
DB_SESSION=""
cat >"$TMP/config.hsh" <<EOF
{
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash",
  "mounts": ["$TMP/workspace:/workspace"],
  "secrets": {
    "API_KEY": { "from": "HSH_TEST_SECRET", "hosts": ["api.mistral.ai"] }
  },
  "network": { "mode": "allowlist", "allow": ["host.containers.internal"] }
}
EOF

export HSH_TEST_SECRET=test-secret-value-123
HSH="$TMP/hsh"
DB="$TMP/denied.db"

# Sessions started by this test have a workspace label under $TMP. Never remove
# other hsh sessions on the host: `hsh rm --all` would.
test_pods() {
	podman pod ls --format '{{.Name}} {{.Labels}}' | awk -v t="hsh.workspace:$TMP" 'index($0, t) { print $1 }'
}
rm_test_sessions() {
	local p
	for p in $(test_pods); do
		"$HSH" rm -f "$p" >/dev/null 2>&1 || true
	done
}

echo "== step 1: curl to a not-yet-allowed endpoint is denied (407) and recorded"
"$HSH" run --config "$TMP/config.hsh" --denied-db "$DB" >"$TMP/session1.out" 2>&1 <<'CMDS' || true
curl -sS -o /dev/null -w 'code=%{http_code}\n' https://api.mistral.ai
echo "curl-rc=$?"
CMDS
grep -q "response 407" "$TMP/session1.out" || fail "expected the proxy to deny api.mistral.ai with 407; got: $(tail -3 "$TMP/session1.out")"
pass "api.mistral.ai denied with 407 before being allowlisted"
DB_SESSION="$(podman pod ls --filter "label=hsh.workspace=$TMP/workspace" --format '{{.Name}}' | sed 's/^hsh-//')"
[ -n "$DB_SESSION" ] || fail "the run left no session pod for the workspace"
[ -d "$XDG_STATE_HOME/hsh/sessions/$DB_SESSION" ] || fail "the run left no session state directory"
grep -q '"mounts"' "$XDG_STATE_HOME/hsh/sessions/$DB_SESSION/config.json" || fail "the session has no config copy"
[ "$(cat "$XDG_STATE_HOME/hsh/sessions/$DB_SESSION/workspace")" = "$TMP/workspace" ] || fail "the session workspace file is wrong"
pass "session created with a config copy and workspace file"

DENIED="$("$HSH" proxy denied --db "$DB")"
echo "$DENIED" | grep -q "api.mistral.ai" || fail "denial was not recorded in the database"
echo "$DENIED" | grep -q "$DB_SESSION" || fail "denial row is not attributed to the session"
echo "$DENIED" | grep -q "$TMP/workspace" || fail "denial row is not attributed to the workspace"
pass "denial recorded in the sqlite database with session + workspace attribution"

echo "== step 2: proxy allow adds the denied host to the session's config (TOFU)"
FILTERED="$("$HSH" proxy denied --session "$DB_SESSION" --db "$DB")"
echo "$FILTERED" | grep -qx "https://api.mistral.ai" || fail "session-filtered denied list must show bare URLs: $FILTERED"
if echo "$FILTERED" | grep -q "$DB_SESSION"; then fail "session-filtered denied list must not repeat the session column"; fi
pass "proxy denied --session filters to bare URLs"
OUT="$("$HSH" proxy allow --all --session "$DB_SESSION" --db "$DB")"
echo "$OUT"
echo "$OUT" | grep -q "added https://api.mistral.ai" || fail "proxy allow did not report adding api.mistral.ai"
SESSION_CONFIG="$XDG_STATE_HOME/hsh/sessions/$DB_SESSION/config.json"
grep -q '"api.mistral.ai"' "$SESSION_CONFIG" || fail "the session's config copy was not updated"
if sed -n '/"allow"/,/]$/p' "$TMP/config.hsh" | grep -q 'api.mistral.ai'; then
	fail "the seed config file must never be modified in place"
fi
pass "api.mistral.ai added to the session's config copy (seed config untouched)"

echo "== step 3: sandbox session — allowed endpoint, workspace mount, pi through the proxy"
"$HSH" run --resume "$DB_SESSION" >"$TMP/session2.out" 2>&1 <<'CMDS' || true
echo "--- curl to the now-allowed endpoint (intercepted, secret injected):"
curl -sS -o /dev/null -w 'code=%{http_code}\n' https://api.mistral.ai
echo "curl-rc=$?"
echo "--- workspace mount:"
cat /workspace/test.md
echo "--- capabilities of the shell:"
grep Cap /proc/self/status | grep -E "CapEff|CapPrm"
echo "--- new entries in image directories (overlayfs copy-up needs CAP_DAC_OVERRIDE):"
ln -s /etc/passwd /usr/lib64/hsh-test-symlink && echo new-entry-ok && rm -f /usr/lib64/hsh-test-symlink
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

grep -q "CapEff:.*0000000000000002" "$TMP/session2.out" || fail "the shell must hold exactly CAP_DAC_OVERRIDE after the capability drop"
pass "the shell runs with only CAP_DAC_OVERRIDE"
grep -q '^new-entry-ok$' "$TMP/session2.out" || fail "creating new entries in image-layer directories failed (overlayfs copy-up): $(grep -A2 'new entries' "$TMP/session2.out")"
pass "new entries in /usr/lib64 work (rpm/dnf installs can unpack)"

grep -q 'hello world' "$TMP/session2.out" || fail "pi did not return the dummy endpoint's response"
grep -q '^pi-rc=0$' "$TMP/session2.out" || fail "pi exited with an error"
pass "echo hi | pi returns hello world through the proxy"

grep -q 'POST /v1/chat/completions' "$TMP/requests.log" || fail "the dummy endpoint never received pi's request"
pass "pi's request reached the dummy endpoint through the proxy"

REMAINING="$("$HSH" proxy denied --db "$DB")"
echo "$REMAINING" | grep -q "https://api.mistral.ai" && fail "approved host must leave the review queue"
if echo "$REMAINING" | grep -q "no denied hosts awaiting review"; then
	fail "expected the refused DNS lookup to be pending in the single queue"
fi
pass "approved hosts leave the single review queue"

grep -Eq '^[0-9a-fA-F:.]+[[:space:]]+api\.mistral\.ai' "$TMP/session2.out" || fail "DNS resolution of the allowlisted host failed"
pass "DNS resolves the allowlisted host through the proxy forwarder"

grep -Eq '^getent-denied-rc=[1-9]' "$TMP/session2.out" || fail "DNS resolution of a non-allowlisted host unexpectedly succeeded"
grep -q 'Failed to connect' "$TMP/session2.out" || fail "direct connection did not fail at the TCP layer"
if grep -q 'Could not resolve host' "$TMP/session2.out"; then
	fail "direct connection failed at DNS instead of TCP (resolution should succeed, egress stays blocked)"
fi
pass "non-allowlisted names are refused; allowed names resolve but stay unreachable directly"

echo "$REMAINING" | grep -q "https://dns-denied.example.net" || fail "DNS denial is not in the single review queue"
pass "DNS denials share the single review queue with egress denials"

if "$HSH" proxy denied --db "$DB" --dns >/dev/null 2>&1; then
	fail "the --dns flag must be gone with the separate table"
fi
pass "the --dns flag is gone"

echo "== step 4: standalone proxy — allowlist and secret injection"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -keyout "$TMP/ca.key" -out "$TMP/ca.crt" -days 1 -nodes -subj "/CN=hsh-test" >/dev/null 2>&1
cat >"$TMP/proxy-config.hsh" <<'EOF'
{ "network": { "mode": "allowlist", "allow": ["localhost"] } }
EOF
cat >"$TMP/proxy-secrets.json" <<'EOF'
{ "secrets": [ { "name": "API_KEY", "value": "test-secret-value-123", "header": "Authorization", "prefix": "Bearer ", "hosts": ["localhost"] } ] }
EOF
cp "$HSH" "$TMP/hsh-proxy"
"$TMP/hsh-proxy" --listen 127.0.0.1:18080 --config "$TMP/proxy-config.hsh" --secrets "$TMP/proxy-secrets.json" --ca-cert "$TMP/ca.crt" --ca-key "$TMP/ca.key" >"$TMP/proxy.out" 2>&1 &
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

echo "== step 5: sessions persist by default; --resume re-enters; idle pods are stopped"
rm_test_sessions
if (cd "$TMP/workspace" && "$HSH" run --resume >"$TMP/resume-none.out" 2>&1); then
	fail "hsh run --resume with no session unexpectedly succeeded"
fi
grep -q "no session to resume" "$TMP/resume-none.out" || fail "resume without sessions did not explain: $(cat "$TMP/resume-none.out")"
pass "hsh run --resume with no session fails clearly"

cp "$TMP/config.hsh" "$TMP/workspace/config.hsh"
(cd "$TMP/workspace" && "$HSH" run) >"$TMP/resume1.out" 2>&1 <<'CMDS' || true
echo resume-marker > /home/hsh/marker.txt
echo "launch1-marker-written"
exit
CMDS
RESUME_POD="$(podman pod ls --filter "label=hsh.workspace=$TMP/workspace" --format '{{.Name}}' 2>/dev/null)"
if [ -z "$RESUME_POD" ]; then
	fail "a plain run must leave its session pod behind"
fi
RESUME_SESSION="${RESUME_POD#hsh-}"
pass "plain runs persist their session pod"

POD_STATE="$(podman pod inspect "$RESUME_POD" --format '{{.State}}')"
[ "$POD_STATE" = "Exited" ] || fail "the idle session pod is $POD_STATE, want Exited"
if [ -f "$XDG_STATE_HOME/hsh/sessions/$RESUME_SESSION/secrets.json" ]; then
	fail "secrets.json must be scrubbed while the session is idle"
fi
pass "idle sessions stop their pod and scrub secrets.json"

(cd "$TMP/workspace" && "$HSH" run --resume) >"$TMP/resume2.out" 2>&1 <<'CMDS' || true
echo "--- marker from the previous launch (same container?):"
cat /home/hsh/marker.txt
echo "--- proxied request after re-entry:"
curl -sS -o /dev/null -w 'code=%{http_code}\n' --max-time 15 http://host.containers.internal:8899/v1/models
echo "--- DNS after re-entry:"
getent hosts host.containers.internal | head -1
exit
CMDS
grep -qx 'resume-marker' "$TMP/resume2.out" || fail "container state did not persist across --resume launches"
pass "bare --resume enters the workspace's only session; state persisted"
grep -Eq '^code=200$' "$TMP/resume2.out" || fail "proxy not working after re-entry: $(cat "$TMP/resume2.out")"
pass "proxy works after re-entry"
grep -Eq '^[0-9a-fA-F:.]+[[:space:]]+host\.containers\.internal' "$TMP/resume2.out" || fail "DNS not working after re-entry"
pass "DNS works after re-entry"

(cd "$TMP/workspace" && "$HSH" run --rm) >"$TMP/resume-rm.out" 2>&1 <<'CMDS' || true
echo ephemeral > /home/hsh/ephemeral.txt
exit
CMDS
LEFTOVER="$(podman pod ls --filter "label=hsh.workspace=$TMP/workspace" --format '{{.Name}}' | wc -l)"
[ "$LEFTOVER" = "1" ] || fail "--rm run left $LEFTOVER pods for the workspace, want only the persistent session"
pass "hsh run --rm removes its pod and state at exit"

(cd "$TMP/workspace" && "$HSH" run) >"$TMP/resume-second.out" 2>&1 <<'CMDS' || true
echo second > /home/hsh/second.txt
exit
CMDS
if (cd "$TMP/workspace" && "$HSH" run --resume) >"$TMP/resume-multi.out" 2>&1; then
	fail "resume with multiple sessions unexpectedly succeeded"
fi
grep -q "multiple sessions for workspace" "$TMP/resume-multi.out" || fail "no multiple-sessions error: $(cat "$TMP/resume-multi.out")"
grep -q "$RESUME_SESSION" "$TMP/resume-multi.out" || fail "the error does not list the session ids"
pass "resume lists the sessions when a workspace has several"

(cd "$TMP/workspace" && "$HSH" run --resume "$RESUME_SESSION") >"$TMP/resume3.out" 2>&1 <<'CMDS' || true
cat /home/hsh/marker.txt
exit
CMDS
grep -qx 'resume-marker' "$TMP/resume3.out" || fail "--resume <id> did not re-enter the chosen session"
pass "hsh run --resume <id> re-enters the chosen session"

ORPHAN="deadsession01"
mkdir -p "$XDG_STATE_HOME/hsh/sessions/$ORPHAN"
echo '{}' >"$XDG_STATE_HOME/hsh/sessions/$ORPHAN/config.json"
"$HSH" gc >"$TMP/gc.out" || fail "hsh gc failed"
grep -q "removed session state $ORPHAN" "$TMP/gc.out" || fail "hsh gc did not collect the orphaned session state"
if [ -d "$XDG_STATE_HOME/hsh/sessions/$ORPHAN" ]; then
	fail "hsh gc left the orphaned session state behind"
fi
pass "hsh gc collects session state whose pod is gone"

rm_test_sessions
RESUME_POD=""

echo "== step 6: hsh ls / hsh rm"
rm_test_sessions
OTHER_HSH_PODS="$(podman pod ls --format '{{.Name}}' | grep '^hsh-' || true)"
POD_A="hsh-fn-a-$$"
POD_B="hsh-fn-b-$$"
SESS_A="${POD_A#hsh-}"
SESS_B="${POD_B#hsh-}"
TEST_PODS="$POD_A $POD_B"
podman pod create --name "$POD_A" --label "hsh.workspace=$TMP/workspace" >/dev/null
podman pod create --name "$POD_B" >/dev/null
podman run --pod "$POD_B" -d localhost/hsh-example:latest sleep 300 >/dev/null

OUT="$("$HSH" ls)"
echo "$OUT" | grep -q '^SESSION ' || fail "hsh ls did not start its header with SESSION"
echo "$OUT" | grep -q WORKSPACE || fail "hsh ls header has no WORKSPACE column"
echo "$OUT" | grep -q '^POD ID\|INFRA ID' && fail "hsh ls header still shows pod and infra IDs"
echo "$OUT" | grep -q "^$SESS_A " || fail "hsh ls did not list the stopped sandbox by session id"
echo "$OUT" | grep -q "^$SESS_B " || fail "hsh ls did not list the running sandbox by session id"
echo "$OUT" | grep -q "$TMP/workspace" || fail "hsh ls did not show the workspace label"
pass "hsh ls lists sandboxes with session id first, then status and workspace"

POD_A_ID="$(podman pod inspect "$POD_A" --format '{{.Id}}')"
"$HSH" ls -q | grep -q "^$POD_A_ID$" || fail "hsh ls -q did not print pod IDs"
"$HSH" ls --no-trunc -q | grep -q "^$POD_A_ID$" || fail "hsh ls --no-trunc -q did not print the full ID"
pass "hsh ls -q and --no-trunc -q print pod IDs"

OUT="$("$HSH" ls --workspace "$TMP/workspace")"
echo "$OUT" | grep -q "^$SESS_A " || fail "hsh ls --workspace did not list the workspace's sandbox"
echo "$OUT" | grep -q "^$SESS_B " && fail "hsh ls --workspace listed a sandbox of another workspace"
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

"$HSH" rm -f -t 1 "$SESS_B" >/dev/null || fail "hsh rm -f could not remove the running sandbox"
podman pod ls --format '{{.Name}}' | grep -qx "$POD_B" && fail "hsh rm -f reported success but the pod is still there"
pass "hsh rm -f stops and removes a running sandbox, addressed by session id"

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

echo "== step 7: monitor requires a terminal"
"$HSH" monitor --db "$DB" >"$TMP/watch.out" 2>&1 && fail "monitor succeeded without a terminal"
grep -q "requires a terminal" "$TMP/watch.out" || fail "monitor did not explain the missing terminal"
pass "monitor refuses to run without a terminal"

if "$HSH" proxy-watch --db "$DB" >/dev/null 2>&1; then
	fail "the old proxy-watch command must be gone"
fi
if "$HSH" proxy-denied --db "$DB" >/dev/null 2>&1; then
	fail "the old proxy-denied command must be gone"
fi
if "$HSH" proxy-auto-allow --db "$DB" >/dev/null 2>&1; then
	fail "the old proxy-auto-allow command must be gone"
fi
pass "the old subcommand names are gone"

echo "== step 8: image label default config (--image without a config file)"
mkdir -p "$TMP/labelws"
echo label-mount >"$TMP/labelws/marker.txt"
cat >"$TMP/label.Containerfile" <<EOF
FROM localhost/hsh-example:latest
LABEL org.ifup.hsh='{"network":{"mode":"allowlist","allow":["host.containers.internal"]},"secrets":{"LABEL_KEY":{"from":"hsh://fn-label-key","hosts":["host.containers.internal"],"header":"Authorization","prefix":"Bearer "}}}'
EOF
podman build -q -t localhost/hsh-label-test:latest -f "$TMP/label.Containerfile" "$TMP" >/dev/null || fail "could not build the label test image"
echo label-secret-value-42 | "$HSH" secret add --value-stdin --db "$TMP/secretstore/secrets.db" fn-label-key >/dev/null || fail "secret add for the label test failed"
if (cd "$TMP/labelws" && "$HSH" run >"$TMP/label-noimg.out" 2>&1); then
	fail "hsh run without a config file or an image succeeded"
fi
grep -q "no config file" "$TMP/label-noimg.out" || fail "hsh run did not explain the missing config: $(cat "$TMP/label-noimg.out")"
(cd "$TMP/labelws" && "$HSH" run --secret-db "$TMP/secretstore/secrets.db" localhost/hsh-label-test:latest >"$TMP/label.out" 2>&1 <<'CMDS' || true
cat /workspace/marker.txt
curl -sS -o /dev/null -w 'allowed=%{http_code}\n' http://host.containers.internal:8899/v1/models
curl -sS -o /dev/null -w 'denied=%{http_code}\n' --max-time 8 http://example.com/ 2>&1 | tail -1
CMDS
)
grep -qx 'label-mount' "$TMP/label.out" || fail "the current directory was not mounted with the label config"
grep -Eq '^allowed=200$' "$TMP/label.out" || fail "the label allowlist did not permit the dummy endpoint: $(tail -5 "$TMP/label.out")"
grep -Eq '^denied=407$' "$TMP/label.out" || fail "the label config did not deny a non-allowlisted host"
grep -q "Authorization: Bearer label-secret-value-42" "$TMP/requests.log" || fail "the label secrets were not injected"
if grep -Eq '^denied=200$' "$TMP/label.out"; then
	fail "a non-allowlisted host was reachable with the label config"
fi
pass "hsh run --image uses the org.ifup.hsh label config (mount, allowlist, hsh:// store secrets)"

echo "== step 9: sandboxed OAuth logins (phantom tokens via the proxy)"
(cd "$REPO" && go build -o "$TMP/oauthsrv" ./test/oauthsrv)
"$TMP/oauthsrv" -addr 0.0.0.0:8897 -log "$TMP/oauth.log" &
OAUTH_PID=$!
cat >"$TMP/oauth-config.hsh" <<EOF
{
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash",
  "network": { "mode": "allowlist", "allow": ["host.containers.internal"] },
  "oauth": {
    "testprov": {
      "token_endpoint": { "host": "host.containers.internal", "path": "/token" },
      "response_fields": ["access_token", "refresh_token"],
      "request_nonce_fields": ["refresh_token"],
      "api_hosts": ["host.containers.internal"]
    }
  }
}
EOF
oauth_flow() {
	"$HSH" run --config "$TMP/oauth-config.hsh" --denied-db "$DB" "$@" >"$TMP/oauth.out" 2>&1 <<'CMDS' || true
RESP="$(curl -sS -X POST -d 'grant_type=password&username=u&password=p' http://host.containers.internal:8897/token)"
echo "login=$RESP"
ACCESS="$(echo "$RESP" | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)"
curl -sS -o /dev/null -w "api=%{http_code}\n" -H "Authorization: Bearer $ACCESS" http://host.containers.internal:8897/api
REFRESH="$(echo "$RESP" | grep -o '"refresh_token":"[^"]*"' | cut -d'"' -f4)"
RESP2="$(curl -sS -X POST -d "grant_type=refresh_token&refresh_token=$REFRESH" http://host.containers.internal:8897/token)"
echo "refresh=$RESP2"
ACCESS2="$(echo "$RESP2" | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)"
curl -sS -o /dev/null -w "api2=%{http_code}\n" -H "Authorization: Bearer $ACCESS2" http://host.containers.internal:8897/api
CMDS
}
oauth_flow
grep -q '"access_token":"hsh_' "$TMP/oauth.out" || fail "the sandbox did not receive phantom tokens: $(cat "$TMP/oauth.out")"
if grep -q 'real-access\|real-refresh' "$TMP/oauth.out"; then
	fail "real OAuth tokens leaked into the sandbox"
fi
grep -Eq '^api=200$' "$TMP/oauth.out" || fail "api call with a phantom token failed: $(cat "$TMP/oauth.out")"
grep -Eq '^api2=200$' "$TMP/oauth.out" || fail "api call after refresh failed: $(cat "$TMP/oauth.out")"
grep -q "refresh=real-refresh-1" "$TMP/oauth.log" || fail "the upstream did not receive the real refresh token during the refresh flow"
grep -q "auth=Bearer real-access-1" "$TMP/oauth.log" || fail "the upstream did not receive the real access token"
if ! curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(grep -o '"access_token":"hsh_[0-9a-f]*"' "$TMP/oauth.out" | head -1 | cut -d'"' -f4)" http://127.0.0.1:8897/api | grep -q 401; then
	fail "a phantom token redeemed directly against the provider (bypassing the proxy)"
fi
grep -q "real-access-1" "$TMP/oauth-tokens.json" || fail "the host-side oauth token store does not hold the real tokens"
pass "oauth login inside the sandbox yields phantoms; real tokens stay on the host"

echo "== step 10: the oauth flow inside the hsh-vibe image"
if podman image exists localhost/hsh-vibe:latest; then
	oauth_flow localhost/hsh-vibe:latest
	grep -Eq '^api2=200$' "$TMP/oauth.out" || fail "the oauth flow failed inside the hsh-vibe image: $(cat "$TMP/oauth.out")"
	pass "oauth flow works inside the hsh-vibe sandbox image"
else
	echo "SKIP: hsh-vibe image not built (podman build -t hsh-vibe images/vibe/)"
fi
kill "$OAUTH_PID" 2>/dev/null || true
OAUTH_PID=""
OAUTH_PID=""


echo "== step 11: headless agent runs (--tasks)"
cat >"$TMP/tasks.Containerfile" <<'EOF'
FROM localhost/hsh-example:latest
LABEL org.ifup.hsh='{"agent":{"command":["/bin/sh","-c","cat <<'TASKEOF'\n{prompt}\nTASKEOF"]}}'
EOF
podman build -q -t localhost/hsh-tasks-test:latest -f "$TMP/tasks.Containerfile" "$TMP" >/dev/null || fail "could not build the tasks test image"
cat >"$TMP/setup-tasks.txt" <<'EOF'
# setup tasks
Install gh for github access and clone https://github.com/philips/hsh

Setup a Golang environment using the latest stable release
EOF
(cd "$TMP/labelws" && "$HSH" run --tasks "$TMP/setup-tasks.txt" localhost/hsh-tasks-test:latest >"$TMP/tasks.out" 2>&1)
grep -q "1. Install gh for github access and clone https://github.com/philips/hsh" "$TMP/tasks.out" || fail "task 1 was not in the prompt: $(cat "$TMP/tasks.out")"
grep -q "2. Setup a Golang environment using the latest stable release" "$TMP/tasks.out" || fail "task 2 was not in the prompt"
grep -q "a comment" "$TMP/tasks.out" && fail "comment lines leaked into the prompt"
pass "hsh run --tasks runs the image's agent headless with a numbered prompt"

cat >"$TMP/tasks-fail.Containerfile" <<'EOF'
FROM localhost/hsh-example:latest
LABEL org.ifup.hsh='{"agent":{"command":["/bin/sh","-c","exit 3"]}}'
EOF
podman build -q -t localhost/hsh-tasks-fail:latest -f "$TMP/tasks-fail.Containerfile" "$TMP" >/dev/null
rc=0
(cd "$TMP/labelws" && "$HSH" run --tasks "$TMP/setup-tasks.txt" localhost/hsh-tasks-fail:latest >"$TMP/tasks-fail.out" 2>&1) || rc=$?
[ "$rc" = "3" ] || fail "agent exit code was not propagated (got $rc)"
pass "headless agent exit codes propagate"

if (cd "$TMP/labelws" && "$HSH" run --tasks "$TMP/setup-tasks.txt" localhost/hsh-label-test:latest >"$TMP/tasks-noagent.out" 2>&1); then
	fail "hsh run --tasks succeeded without an agent command in the image"
fi
grep -q "agent command" "$TMP/tasks-noagent.out" || fail "missing-agent error did not explain the problem: $(cat "$TMP/tasks-noagent.out")"
pass "hsh run --tasks fails clearly without an agent command"

echo "== step 12: network policy modes"
cat >"$TMP/mode-config.hsh" <<'EOF'
{
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash"
}
EOF
"$HSH" run --config "$TMP/mode-config.hsh" --denied-db "$DB" >"$TMP/mode-https.out" 2>&1 <<'CMDS' || true
curl -sS -o /dev/null -w 'https=%{http_code}\n' --max-time 15 https://example.com/ 2>&1 | tail -1
curl -sS -o /dev/null -w 'http=%{http_code}\n' --max-time 15 http://example.com/ 2>&1 | tail -1
getent hosts example.com >/dev/null && echo dns=resolved || echo dns=failed
CMDS
grep -Eq '^https=(200|301|30[0-9])$' "$TMP/mode-https.out" || fail "https mode did not allow TLS to an arbitrary host: $(cat "$TMP/mode-https.out")"
grep -Eq '^http=407$' "$TMP/mode-https.out" || fail "https mode allowed plaintext to a non-allowlisted host: $(cat "$TMP/mode-https.out")"
grep -q '^dns=resolved$' "$TMP/mode-https.out" || fail "https mode did not resolve arbitrary names: $(cat "$TMP/mode-https.out")"
pass "https mode (default) allows TLS to any host, denies plaintext, resolves all names"

"$HSH" run --config "$TMP/mode-config.hsh" --denied-db "$DB" --network allowlist >"$TMP/mode-allowlist.out" 2>&1 <<'CMDS' || true
curl -sS -o /dev/null -w 'https=%{http_code}\n' --max-time 15 https://example.com/ 2>&1 | tail -1
CMDS
grep -Eq '^https=000$' "$TMP/mode-allowlist.out" || fail "--network allowlist did not deny TLS to a non-allowlisted host: $(cat "$TMP/mode-allowlist.out")"
pass "--network allowlist restores the deny-by-default policy"

"$HSH" run --config "$TMP/mode-config.hsh" --denied-db "$DB" --network none >"$TMP/mode-none.out" 2>&1 <<'CMDS' || true
curl -sS -o /dev/null -w 'https=%{http_code}\n' --max-time 15 https://example.com/ 2>&1 | tail -1
CMDS
grep -Eq '^https=000$' "$TMP/mode-none.out" || fail "--network none did not deny TLS: $(cat "$TMP/mode-none.out")"
pass "--network none denies everything"

echo "== step 13: the hsh secret store (hsh:// references)"
export XDG_CONFIG_HOME="$TMP/xdg-config"
mkdir -p "$XDG_CONFIG_HOME"
echo stored-value-42 | "$HSH" secret add --value-stdin --db "$TMP/secretstore/secrets.db" fn-store-key >"$TMP/secret-add.out" 2>&1 || fail "secret add failed: $(cat "$TMP/secret-add.out")"
[ -f "$TMP/secretstore/secrets.db" ] || fail "the secret store was not created"
STAT="$(stat -c '%a' "$TMP/secretstore/secrets.db")"
[ "$STAT" = "600" ] || fail "secret store mode is $STAT, want 600"
STAT="$(stat -c '%a' "$TMP/secretstore")"
[ "$STAT" = "700" ] || fail "store directory mode is $STAT, want 700"
if "$HSH" secret ls --db "$TMP/secretstore/secrets.db" | grep -q stored-value-42; then
	fail "secret ls printed the secret value"
fi
"$HSH" secret ls --db "$TMP/secretstore/secrets.db" | grep -q fn-store-key || fail "secret ls did not list the stored name"
pass "secret add stores values (0600/0700), ls shows names but never values"

if echo other | "$HSH" secret add --value-stdin --db "$TMP/secretstore/secrets.db" fn-store-key >/dev/null 2>&1; then
	fail "secret add overwrote an existing name without --force"
fi
echo other | "$HSH" secret add --value-stdin --force --db "$TMP/secretstore/secrets.db" fn-store-key || fail "secret add --force failed"
pass "secret add refuses duplicates without --force"

cat >"$TMP/store-config.hsh" <<EOF
{
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash",
  "mounts": ["$TMP/workspace:/workspace"],
  "network": { "mode": "allowlist", "allow": ["host.containers.internal"] },
  "secrets": {
    "API_KEY": { "from": "hsh://fn-store-key", "hosts": ["host.containers.internal"], "header": "Authorization", "prefix": "Bearer " }
  }
}
EOF
"$HSH" run --config "$TMP/store-config.hsh" --denied-db "$DB" --secret-db "$TMP/secretstore/secrets.db" >"$TMP/store-run.out" 2>&1 <<'CMDS' || true
curl -sS -o /dev/null -w 'code=%{http_code}\n' http://host.containers.internal:8899/v1/models
CMDS
grep -Eq '^code=200$' "$TMP/store-run.out" || fail "the sandbox could not reach the dummy endpoint with an hsh:// secret: $(cat "$TMP/store-run.out")"
grep -q 'Authorization: Bearer other' "$TMP/requests.log" || fail "the stored secret was not injected (after --force overwrite)"
pass "hsh:// secrets resolve through the store and are injected"

if "$HSH" secret rm --db "$TMP/secretstore/secrets.db" --config "$TMP/store-config.hsh" fn-store-key >"$TMP/secret-rm.out" 2>&1; then
	pass "secret rm deletes the entry"
else
	fail "secret rm failed: $(cat "$TMP/secret-rm.out")"
fi
grep -q "still references hsh://fn-store-key" "$TMP/secret-rm.out" || fail "secret rm did not warn about the dangling config reference"
grep -q 'hsh://fn-store-key' "$TMP/store-config.hsh" || fail "secret rm must not edit the config"
pass "secret rm warns about dangling config references without editing them"

"$HSH" run --config "$TMP/store-config.hsh" --denied-db "$DB" --secret-db "$TMP/secretstore/secrets.db" >"$TMP/store-missing.out" 2>&1 <<'CMDS' || true
curl -sS -o /dev/null -w 'code=%{http_code}\n' http://host.containers.internal:8899/v1/models
CMDS
grep -Eq '^code=200$' "$TMP/store-missing.out" || fail "the request was forwarded after secret removal"
grep -q "hsh://fn-store-key is not set" "$TMP/store-missing.out" || fail "hsh run did not warn about the missing store entry: $(cat "$TMP/store-missing.out")"
LAST_REQ="$(grep '/v1/models' "$TMP/requests.log" | tail -1)"
if grep -q 'Authorization: Bearer other' <<<"$LAST_REQ"; then
	fail "the secret was still injected after secret rm"
fi
pass "a missing store entry warns and fails open, like an unset env var"
unset XDG_CONFIG_HOME

echo "== step 14: interactive sessions auto-launch the agent harness (Ctrl-Z suspends to bash)"
mkdir -p "$TMP/launchws"
echo launch-mount >"$TMP/launchws/marker.txt"
cat >"$TMP/launch.Containerfile" <<EOF
FROM localhost/hsh-example:latest
LABEL org.ifup.hsh='{"shell":"/bin/bash","network":{"mode":"allowlist","allow":["host.containers.internal"]},"agent":{"launch":["bash","-c","echo HARNESS-LAUNCHED; sleep 120"]}}'
EOF
podman build -q -t localhost/hsh-launch-test:latest -f "$TMP/launch.Containerfile" "$TMP" >/dev/null || fail "could not build the launch test image"

(cd "$TMP/launchws" && "$HSH" run localhost/hsh-launch-test:latest) >"$TMP/launch-piped.out" 2>&1 <<'CMDS' || true
echo piped-session
exit
CMDS
if grep -q HARNESS-LAUNCHED "$TMP/launch-piped.out"; then
	fail "a non-interactive session must not auto-launch the harness"
fi
pass "piped sessions do not auto-launch the harness"

if command -v python3 >/dev/null; then
	LAUNCH_OUT="$(cd "$TMP/launchws" && python3 - "$HSH" <<'PYDRIVER'
import os, pty, select, sys, time
hsh = sys.argv[1]
pid, fd = pty.fork()
if pid == 0:
    os.execv(hsh, [hsh, "run", "localhost/hsh-launch-test:latest"])
out = b""
sent_z = False
deadline = time.time() + 60
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 1)
    if r:
        try:
            chunk = os.read(fd, 4096)
        except OSError:
            break
        if not chunk:
            break
        out += chunk
        if b"HARNESS-LAUNCHED" in out and not sent_z:
            sent_z = True
            time.sleep(1)
            os.write(fd, b"\x1a")
            time.sleep(2)
            os.write(fd, b"echo SUSPENDED-TO-BASH; jobs\r")
    if b"SUSPENDED-TO-BASH" in out:
        time.sleep(1)
        os.write(fd, b"exit\r")
        break
time.sleep(2)
try:
    while True:
        r, _, _ = select.select([fd], [], [], 0.5)
        if not r:
            break
        chunk = os.read(fd, 4096)
        if not chunk:
            break
        out += chunk
except OSError:
    pass
sys.stdout.write(out.decode(errors="replace"))
PYDRIVER
)"
	echo "$LAUNCH_OUT" | grep -q HARNESS-LAUNCHED || fail "the harness did not auto-launch in the interactive session"
	echo "$LAUNCH_OUT" | grep -q "SUSPENDED-TO-BASH" || fail "Ctrl-Z did not suspend the harness to the shell"
	echo "$LAUNCH_OUT" | grep -q "Stopped" || fail "the suspended harness is not listed as a stopped job"
	pass "interactive sessions auto-launch the harness; Ctrl-Z suspends it into bash"
else
	echo "SKIP: interactive launch check (no python3 for the pty driver)"
fi

echo "== step 15: image shortnames, transport references, and the pull policy"
PREV_PI_IMAGE="$(podman image inspect --format '{{.Id}}' ghcr.io/philips/hsh-pi:latest 2>/dev/null || true)"
podman tag localhost/hsh-example:latest ghcr.io/philips/hsh-pi:latest
mkdir -p "$TMP/shortws"
(cd "$TMP/shortws" && "$HSH" run --pull=never pi) >"$TMP/short-run.out" 2>&1 <<'CMDS' || true
echo SHORTNAME-SESSION
exit
CMDS
grep -q '^SHORTNAME-SESSION$' "$TMP/short-run.out" || fail "hsh run --pull=never pi did not start the ghcr.io/philips/hsh-pi shortname: $(cat "$TMP/short-run.out")"
SN_SESSION="$(podman pod ls --filter "label=hsh.workspace=$TMP/shortws" --format '{{.Name}}' | head -1)"
[ -n "$SN_SESSION" ] || fail "the shortname run left no session pod"
SESSION_CFG="$XDG_STATE_HOME/hsh/sessions/${SN_SESSION#hsh-}/config.json"
grep -q '"image": "ghcr.io/philips/hsh-pi"' "$SESSION_CFG" || fail "the session config does not hold the resolved image reference: $(cat "$SESSION_CFG" 2>/dev/null)"
pass "hsh run pi resolves to ghcr.io/philips/hsh-pi and the session stores the resolved reference"

if (cd "$TMP/shortws" && "$HSH" run --pull=never definitely-not-a-harness) >"$TMP/pull-never.out" 2>&1; then
	fail "--pull=never with no local image unexpectedly succeeded"
fi
grep -q "not in the local storage" "$TMP/pull-never.out" || fail "--pull=never did not explain the missing local image: $(cat "$TMP/pull-never.out")"
pass "--pull=never errors clearly when the image is not in the local storage"

if (cd "$TMP/shortws" && "$HSH" run definitely-not-a-harness) >"$TMP/short-err.out" 2>&1; then
	fail "hsh run of an unknown shortname unexpectedly succeeded"
fi
grep -q "ghcr.io/philips/hsh-definitely-not-a-harness" "$TMP/short-err.out" || fail "the error does not name what the shortname resolved to"
grep -q "shortnames.conf" "$TMP/short-err.out" || fail "the error does not point at the shortnames config"
pass "an unknown shortname names what it resolved to and the escape hatches"

mkdir -p "$TMP/pullws"
(cd "$TMP/pullws" && "$HSH" run localhost/hsh-example:latest) >"$TMP/pull-newer.out" 2>&1 <<'CMDS' || true
echo NEWER-FALLBACK
exit
CMDS
grep -q '^NEWER-FALLBACK$' "$TMP/pull-newer.out" || fail "the default --pull=newer run did not start a session: $(cat "$TMP/pull-newer.out")"
grep -q "using the local image" "$TMP/pull-newer.out" || fail "the default --pull=newer did not warn about the failed pull of the unpullable image: $(cat "$TMP/pull-newer.out")"
pass "the default --pull=newer falls back to the local image with a warning"

if (cd "$TMP/pullws" && "$HSH" run --pull=always localhost/hsh-example:latest) >"$TMP/pull-always.out" 2>&1; then
	fail "--pull=always with an unpullable image unexpectedly succeeded"
fi
grep -q "pull failed" "$TMP/pull-always.out" || fail "--pull=always did not report the pull failure: $(cat "$TMP/pull-always.out")"
pass "--pull=always errors when the pull fails instead of falling back"

podman save -q -o "$TMP/img.tar" localhost/hsh-example:latest
(cd "$TMP/shortws" && "$HSH" run docker-archive:"$TMP/img.tar") >"$TMP/transport-run.out" 2>&1 <<'CMDS' || true
echo TRANSPORT-SESSION
exit
CMDS
grep -q '^TRANSPORT-SESSION$' "$TMP/transport-run.out" || fail "hsh run docker-archive:... did not start a session: $(cat "$TMP/transport-run.out")"
pass "docker-archive: transport references are pulled and run"

cat >"$TMP/arch.Containerfile" <<'EOF'
FROM localhost/hsh-example:latest
ENV HSH_ARCH_MARKER=arch-v1
EOF
podman build -q -t localhost/hsh-arch:latest -f "$TMP/arch.Containerfile" "$TMP" >/dev/null || fail "could not build the archive test image"
rm -f "$TMP/arch.tar"
podman save -q -o "$TMP/arch.tar" localhost/hsh-arch:latest
(cd "$TMP/pullws" && "$HSH" run docker-archive:"$TMP/arch.tar") >"$TMP/arch-run1.out" 2>&1 <<'CMDS' || true
echo marker=$HSH_ARCH_MARKER
exit
CMDS
grep -q '^marker=arch-v1$' "$TMP/arch-run1.out" || fail "the first docker-archive run did not see the v1 marker: $(cat "$TMP/arch-run1.out")"
cat >"$TMP/arch.Containerfile" <<'EOF'
FROM localhost/hsh-example:latest
ENV HSH_ARCH_MARKER=arch-v2
EOF
podman build -q -t localhost/hsh-arch:latest -f "$TMP/arch.Containerfile" "$TMP" >/dev/null || fail "could not rebuild the archive test image"
rm -f "$TMP/arch.tar"
podman save -q -o "$TMP/arch.tar" localhost/hsh-arch:latest
(cd "$TMP/pullws" && "$HSH" run docker-archive:"$TMP/arch.tar") >"$TMP/arch-run2.out" 2>&1 <<'CMDS' || true
echo marker=$HSH_ARCH_MARKER
exit
CMDS
grep -q '^marker=arch-v2$' "$TMP/arch-run2.out" || fail "the second docker-archive run did not pick up the re-saved archive (the pull did not refresh the local copy): $(cat "$TMP/arch-run2.out")"
pass "the default --pull=newer re-pulls and picks up new content for the same reference"

mkdir -p "$TMP/archcfgws"
cat >"$TMP/archcfgws/pull-config.hsh" <<EOF
{
  "image": "docker-archive:$TMP/arch.tar",
  "shell": "/bin/bash"
}
EOF
(cd "$TMP/archcfgws" && "$HSH" run --config pull-config.hsh) >"$TMP/arch-cfg.out" 2>&1 <<'CMDS' || true
echo marker=$HSH_ARCH_MARKER
exit
CMDS
grep -q '^marker=arch-v2$' "$TMP/arch-cfg.out" || fail "the config file's image field did not go through the pull policy: $(cat "$TMP/arch-cfg.out")"
CFG_SESSION="$(podman pod ls --filter "label=hsh.workspace=$TMP/archcfgws" --format '{{.Name}}' | head -1)"
[ -n "$CFG_SESSION" ] || fail "the config-file archive run left no session pod"
grep -q '"image": "localhost/hsh-arch:latest"' "$XDG_STATE_HOME/hsh/sessions/${CFG_SESSION#hsh-}/config.json" || fail "the session config does not hold the normalized local image: $(cat "$XDG_STATE_HOME/hsh/sessions/${CFG_SESSION#hsh-}/config.json" 2>/dev/null)"
pass "a config file's image field goes through the pull policy and is normalized"

echo "== step 16: hsh oauth callback replays a loopback OAuth callback into the session"
mkdir -p "$TMP/oauthws"
(cd "$TMP/oauthws" && "$HSH" run localhost/hsh-example:latest) >"$TMP/oauth-run.out" 2>&1 <<'CMDS' &
/hsh-workspace-inner --http-echo 1455 > /workspace/callback.txt 2>&1 &
sleep 30
exit
CMDS
OAUTH_SESSION_PID=$!
for _ in $(seq 1 50); do
	if podman pod ls --filter "label=hsh.workspace=$TMP/oauthws" --format '{{.Name}}' | grep -q hsh-; then
		break
	fi
	sleep 0.5
done
sleep 3
OUT="$(cd "$TMP/oauthws" && "$HSH" oauth callback 'http://127.0.0.1:1455/auth/callback?code=xyz123&state=abc789')"
echo "$OUT"
echo "$OUT" | grep -q "delivered the callback to session" || fail "the callback was not delivered: $OUT"
echo "$OUT" | grep -q "200 OK" || fail "the session's listener did not answer 200: $OUT"
wait "$OAUTH_SESSION_PID"
grep -q "GET /auth/callback?code=xyz123&state=abc789 HTTP/1.1" "$TMP/oauthws/callback.txt" || fail "the loopback listener never received the callback: $(cat "$TMP/oauthws/callback.txt" 2>/dev/null)"
pass "hsh oauth callback delivers a browser callback URL into the waiting session"

if (cd "$TMP/oauthws" && "$HSH" oauth callback 'https://evil.example.com/auth') >/dev/null 2>&1; then
	fail "oauth callback must reject non-loopback URLs"
fi
(cd "$TMP/oauthws" && "$HSH" oauth callback 'https://evil.example.com/auth') >"$TMP/oauth-reject.out" 2>&1 || true
grep -q "not a loopback callback" "$TMP/oauth-reject.out" || fail "oauth callback did not explain the loopback requirement: $(cat "$TMP/oauth-reject.out")"
pass "oauth callback rejects non-loopback URLs"

echo "== step 17: detach and attach with podman's own flow, and the reaper"
if command -v python3 >/dev/null; then
	cat >"$TMP/detach.Containerfile" <<'EOF'
FROM localhost/hsh-example:latest
LABEL org.ifup.hsh='{"shell":"/bin/bash","network":{"mode":"allowlist","allow":["host.containers.internal"]}}'
EOF
	podman build -q -t localhost/hsh-detach-test:latest -f "$TMP/detach.Containerfile" "$TMP" >/dev/null || fail "could not build the detach test image"
	mkdir -p "$TMP/detachws"
	python3 "$REPO/test/detach.py" "$HSH" "$TMP/detachws" localhost/hsh-detach-test:latest "$XDG_STATE_HOME/hsh" || fail "the detach/attach driver failed"
	pass "detach, attach, closed terminals, the reaper, and gc behave as planned"
else
	echo "SKIP: detach/attach checks (no python3 for the pty driver)"
fi

echo "== step 18: inline proxy review (status line, Ctrl+Space dialog, pause)"
if command -v python3 >/dev/null; then
	cat >"$TMP/nomonitor.Containerfile" <<'EOF'
FROM localhost/hsh-example:latest
LABEL org.ifup.hsh='{"shell":"/bin/bash","network":{"mode":"allowlist","allow":["host.containers.internal"]},"monitor":{"enabled":false}}'
EOF
	podman build -q -t localhost/hsh-nomonitor-test:latest -f "$TMP/nomonitor.Containerfile" "$TMP" >/dev/null || fail "could not build the no-monitor test image"
	python3 "$REPO/test/inline.py" "$HSH" "$TMP/detachws" localhost/hsh-detach-test:latest "$XDG_STATE_HOME/hsh" localhost/hsh-nomonitor-test:latest || fail "the inline review driver failed"
	pass "the status line, Ctrl+Space dialog, pause, and approval work through the relay"
else
	echo "SKIP: inline review checks (no python3 for the pty driver)"
fi

echo "== step 19: denied requests are held for approval (network.approval_wait)"
HOLD_WS="$TMP/holdws"
mkdir -p "$HOLD_WS"
cat >"$TMP/hold.config.hsh" <<EOF
{
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash",
  "mounts": ["$HOLD_WS:/workspace"],
  "network": { "mode": "allowlist", "allow": ["host.containers.internal"], "approval_wait": "30s" }
}
EOF
cat >"$TMP/hold-off.config.hsh" <<EOF
{
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash",
  "mounts": ["$HOLD_WS:/workspace"],
  "network": { "mode": "allowlist", "allow": ["host.containers.internal"] }
}
EOF

start_attended_loop() {
	local before="$1" out_pid="$2"
	(
		for _ in $(seq 1 3000); do
			kill -0 "$out_pid" 2>/dev/null || exit 0
			for d in "$XDG_STATE_HOME"/hsh/sessions/*/; do
				id="$(basename "$d")"
				case " $before " in *" $id "*) continue ;; esac
				touch "$d/attended"
			done
			sleep 0.2
		done
	) &
	ATTENDED_PID=$!
}
session_ids() { ls "$XDG_STATE_HOME/hsh/sessions" 2>/dev/null | tr '\n' ' '; }

BEFORE_IDS="$(session_ids)"
(cd "$HOLD_WS" && "$HSH" run --config "$TMP/hold.config.hsh" --denied-db "$DB") >"$TMP/hold-run.out" 2>&1 <<'CMDS' &
sleep 3
curl -sS -m 40 -o /dev/null -w 'held-result code=%{http_code} time=%{time_total}\n' https://api.mistral.ai 2>&1
exit
CMDS
HOLD_PID=$!
start_attended_loop "$BEFORE_IDS" "$HOLD_PID"
HOLD_SESSION=""
for _ in $(seq 1 120); do
	for id in $(session_ids); do
		case " $BEFORE_IDS " in *" $id "*) ;; *) HOLD_SESSION="$id" ;; esac
	done
	[ -n "$HOLD_SESSION" ] && break
	sleep 0.5
done
[ -n "$HOLD_SESSION" ] || fail "the hold test session never appeared"
for _ in $(seq 1 120); do
	"$HSH" proxy denied --db "$DB" --session "$HOLD_SESSION" 2>/dev/null | grep -q "api.mistral.ai" && break
	sleep 0.5
done
"$HSH" proxy denied --db "$DB" --session "$HOLD_SESSION" 2>/dev/null | grep -q "api.mistral.ai" || fail "the held request was never recorded as a denial: $(cat "$TMP/hold-run.out")"
sleep 3
grep -q "held-result" "$TMP/hold-run.out" && fail "the denied request was answered instead of held: $(cat "$TMP/hold-run.out")"
pass "with approval_wait set, a denied CONNECT stays pending (held) while someone is watching"

"$HSH" proxy allow --db "$DB" --session "$HOLD_SESSION" https://api.mistral.ai >/dev/null || fail "hsh proxy allow failed"
wait "$HOLD_PID" || true
kill "$ATTENDED_PID" 2>/dev/null || true
grep -q "held-result" "$TMP/hold-run.out" || fail "the held request never completed after approval: $(cat "$TMP/hold-run.out")"
HELD_CODE="$(sed -n 's/.*held-result code=\([0-9]*\).*/\1/p' "$TMP/hold-run.out" | head -1)"
case "$HELD_CODE" in 000|407|"") fail "the approved request did not reach the host (code=$HELD_CODE): $(cat "$TMP/hold-run.out")" ;; esac
pass "approving the host releases the held request, which then succeeds (HTTP $HELD_CODE)"

BEFORE_IDS="$(session_ids)"
(cd "$HOLD_WS" && "$HSH" run --config "$TMP/hold-off.config.hsh" --denied-db "$DB") >"$TMP/hold-off.out" 2>&1 <<'CMDS' &
sleep 3
curl -s -m 30 -w '\noptout-result code=%{http_code} time=%{time_total}\n' http://blocked-optout.example.test/ 2>&1
exit
CMDS
OFF_PID=$!
start_attended_loop "$BEFORE_IDS" "$OFF_PID"
wait "$OFF_PID" || true
kill "$ATTENDED_PID" 2>/dev/null || true
OFF_TIME="$(sed -n 's/.*optout-result code=407 time=\([0-9.]*\).*/\1/p' "$TMP/hold-off.out" | head -1)"
[ -n "$OFF_TIME" ] || fail "an opted-out session did not answer 407: $(cat "$TMP/hold-off.out")"
awk -v t="$OFF_TIME" 'BEGIN { exit !(t < 10) }' || fail "an opted-out session still waited ($OFF_TIME s)"
grep -q "needs the user's approval" "$TMP/hold-off.out" || fail "the denial text does not tell the agent what to do: $(cat "$TMP/hold-off.out")"
pass "holding is off by default: a denied request fails at once, with a message aimed at the agent"

OFF_SESSION="$(for id in $(session_ids); do case " $BEFORE_IDS " in *" $id "*) ;; *) echo "$id" ;; esac; done | head -1)"
"$HSH" proxy hold --session "$OFF_SESSION" | grep -q "holding is off, the default" || fail "hsh proxy hold did not report that holding is off by default"
"$HSH" proxy hold --session "$OFF_SESSION" 4s | grep -q "up to 4s" || fail "hsh proxy hold 4s did not apply"
(cd "$HOLD_WS" && "$HSH" run --denied-db "$DB" --resume "$OFF_SESSION") >"$TMP/hold-4s.out" 2>&1 <<'CMDS' &
sleep 3
curl -s -m 30 -w '\nfourgs-result code=%{http_code} time=%{time_total}\n' http://blocked-four.example.test/ 2>&1
exit
CMDS
FOUR_PID=$!
start_attended_loop "$BEFORE_IDS" "$FOUR_PID"
wait "$FOUR_PID" || true
kill "$ATTENDED_PID" 2>/dev/null || true
FOUR_TIME="$(sed -n 's/.*fourgs-result code=407 time=\([0-9.]*\).*/\1/p' "$TMP/hold-4s.out" | head -1)"
[ -n "$FOUR_TIME" ] || fail "the 4s hold did not end in a 407: $(cat "$TMP/hold-4s.out")"
awk -v t="$FOUR_TIME" 'BEGIN { exit !(t >= 3.5 && t < 20) }' || fail "an unanswered hold should last about 4s, got $FOUR_TIME s"
pass "hsh proxy hold 4s turns holding on live; an unapproved request is held about 4s, then denied"

echo "== final sweep: every session the test created is removable"
rm_test_sessions
if [ -n "$(test_pods)" ]; then
	fail "sessions were left behind after the final sweep: $(test_pods)"
fi
[ -z "$(ls -A "$XDG_STATE_HOME/hsh/sessions" 2>/dev/null)" ] || fail "session state was left behind after the final sweep"
pass "hsh rm removes every session the test created and its state"

echo
echo "ALL FUNCTIONAL TESTS PASSED"
PASSED=1
