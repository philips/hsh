#!/usr/bin/env bash
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
export XDG_STATE_HOME="$TMP/xdg"
mkdir -p "$XDG_STATE_HOME"
DUMMY_PID=""
PROXY_PID=""
OAUTH_PID=""
RELEASESRV_PID=""
RESUME_POD=""
TEST_PODS=""
PASSED=0
cleanup() {
	if [ -n "$RELEASESRV_PID" ]; then
		kill "$RELEASESRV_PID" 2>/dev/null || true
	fi
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
(cd "$REPO" && go build -ldflags "-X hsh/internal/version.Tag=v1.2.3" -o "$TMP/hsh-v1.2.3" .)
(cd "$REPO" && go build -o "$TMP/dummyopenai" ./test/dummyopenai)
(cd "$REPO" && go build -o "$TMP/releasesrv" ./test/releasesrv)

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
  "version": 1,
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash",
  "mounts": ["$TMP/workspace:/workspace"],
  "secrets": {
    "API_KEY": { "from": "HSH_TEST_SECRET", "hosts": ["api.mistral.ai"] }
  },
  "network": { "mode": "allowlist", "allow": ["host.containers.internal"] }
}
EOF

mkdir -p "$TMP/cwd"
cd "$TMP/cwd"

export HSH_TEST_SECRET=test-secret-value-123
HSH="$TMP/hsh"
DB="$TMP/monitor.db"

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

echo "== step 1: curl to a not-yet-allowed endpoint is denied (403) and recorded"
"$HSH" run --config "$TMP/config.hsh" --monitor-db "$DB" >"$TMP/session1.out" 2>&1 <<'CMDS' || true
curl -sS -o /dev/null -w 'code=%{http_code}\n' https://api.mistral.ai
echo "curl-rc=$?"
CMDS
grep -q "response 403" "$TMP/session1.out" || fail "expected the proxy to deny api.mistral.ai with 403; got: $(tail -3 "$TMP/session1.out")"
pass "api.mistral.ai denied with 403 before being allowlisted"
[ -f "$DB" ] || fail "the denial was not recorded in $DB"
[ ! -e "$(dirname "$DB")/denied.db" ] || fail "a denied.db was created next to the monitor database"
[ "$(python3 -c "import sqlite3,sys; print(sqlite3.connect(sys.argv[1]).execute('PRAGMA user_version').fetchone()[0])" "$DB")" = 1 ] || fail "the monitor database has no schema version"
pass "denials land in monitor.db, which carries a schema version"
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
curl -sS -o /dev/null --max-time 8 https://queue-check.example.net >/dev/null 2>&1
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
echo "$REMAINING" | grep -q "https://queue-check.example.net" || fail "the denied CONNECT is not pending in the review queue"
pass "approved hosts leave the single review queue"

grep -Eq '^[0-9a-fA-F:.]+[[:space:]]+api\.mistral\.ai' "$TMP/session2.out" || fail "DNS resolution of the allowlisted host failed"
pass "DNS resolves the allowlisted host through the proxy forwarder"

grep -Eq '^getent-denied-rc=[1-9]' "$TMP/session2.out" || fail "DNS resolution of a non-allowlisted host unexpectedly succeeded"
grep -q 'Failed to connect' "$TMP/session2.out" || fail "direct connection did not fail at the TCP layer"
if grep -q 'Could not resolve host' "$TMP/session2.out"; then
	fail "direct connection failed at DNS instead of TCP (resolution should succeed, egress stays blocked)"
fi
pass "non-allowlisted names are refused; allowed names resolve but stay unreachable directly"

if echo "$REMAINING" | grep -q "dns-denied.example.net"; then
	fail "a refused DNS lookup must not appear in the review queue"
fi
pass "refused DNS lookups are not reviewable; connection denials are"

if "$HSH" proxy denied --db "$DB" --dns >/dev/null 2>&1; then
	fail "the --dns flag must be gone with the separate table"
fi
pass "the --dns flag is gone"

echo "== step 4: standalone proxy — allowlist and secret injection"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -keyout "$TMP/ca.key" -out "$TMP/ca.crt" -days 1 -nodes -subj "/CN=hsh-test" >/dev/null 2>&1
cat >"$TMP/proxy-config.hsh" <<'EOF'
{ "version": 1, "network": { "mode": "allowlist", "allow": ["localhost"] } }
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
[ "$CODE" = "403" ] || fail "standalone proxy did not deny a non-allowlisted host with 403 (got $CODE)"
pass "standalone proxy denies non-allowlisted hosts with 403"

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
(cd "$TMP/workspace" && "$HSH" run --config config.hsh) >"$TMP/resume1.out" 2>&1 <<'CMDS' || true
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

(cd "$TMP/workspace" && "$HSH" run --config config.hsh --rm) >"$TMP/resume-rm.out" 2>&1 <<'CMDS' || true
echo ephemeral > /home/hsh/ephemeral.txt
exit
CMDS
LEFTOVER="$(podman pod ls --filter "label=hsh.workspace=$TMP/workspace" --format '{{.Name}}' | wc -l)"
[ "$LEFTOVER" = "1" ] || fail "--rm run left $LEFTOVER pods for the workspace, want only the persistent session"
pass "hsh run --rm removes its pod and state at exit"

(cd "$TMP/workspace" && "$HSH" run --config config.hsh) >"$TMP/resume-second.out" 2>&1 <<'CMDS' || true
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
LABEL org.ifup.hsh='{"version":1,"network":{"mode":"allowlist","allow":["host.containers.internal"]},"secrets":{"LABEL_KEY":{"from":"hsh://fn-label-key","hosts":["host.containers.internal"],"header":"Authorization","prefix":"Bearer "}}}'
EOF
podman build -q -t localhost/hsh-label-test:latest -f "$TMP/label.Containerfile" "$TMP" >/dev/null || fail "could not build the label test image"
echo label-secret-value-42 | "$HSH" secret add --value-stdin --db "$TMP/secretstore/secrets.db" fn-label-key >/dev/null || fail "secret add for the label test failed"
if (cd "$TMP/labelws" && "$HSH" run >"$TMP/label-noimg.out" 2>&1); then
	fail "hsh run without a config file or an image succeeded"
fi
grep -q "no image given" "$TMP/label-noimg.out" || fail "hsh run did not explain the missing config: $(cat "$TMP/label-noimg.out")"
(cd "$TMP/labelws" && "$HSH" run --secret-db "$TMP/secretstore/secrets.db" localhost/hsh-label-test:latest >"$TMP/label.out" 2>&1 <<'CMDS' || true
cat /workspace/marker.txt
curl -sS -o /dev/null -w 'allowed=%{http_code}\n' http://host.containers.internal:8899/v1/models
curl -sS -o /dev/null -w 'denied=%{http_code}\n' --max-time 8 http://example.com/ 2>&1 | tail -1
CMDS
)
grep -qx 'label-mount' "$TMP/label.out" || fail "the current directory was not mounted with the label config"
grep -Eq '^allowed=200$' "$TMP/label.out" || fail "the label allowlist did not permit the dummy endpoint: $(tail -5 "$TMP/label.out")"
grep -Eq '^denied=403$' "$TMP/label.out" || fail "the label config did not deny a non-allowlisted host"
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
  "version": 1,
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
	"$HSH" run --config "$TMP/oauth-config.hsh" --monitor-db "$DB" "$@" >"$TMP/oauth.out" 2>&1 <<'CMDS' || true
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
LABEL org.ifup.hsh='{"version":1,"agent":{"command":["/bin/sh","-c","cat <<'TASKEOF'\n{prompt}\nTASKEOF"]}}'
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
LABEL org.ifup.hsh='{"version":1,"agent":{"command":["/bin/sh","-c","exit 3"]}}'
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
  "version": 1,
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash"
}
EOF
"$HSH" run --config "$TMP/mode-config.hsh" --monitor-db "$DB" >"$TMP/mode-https.out" 2>&1 <<'CMDS' || true
curl -sS -o /dev/null -w 'https=%{http_code}\n' --max-time 15 https://example.com/ 2>&1 | tail -1
curl -sS -o /dev/null -w 'http=%{http_code}\n' --max-time 15 http://example.com/ 2>&1 | tail -1
getent hosts example.com >/dev/null && echo dns=resolved || echo dns=failed
CMDS
grep -Eq '^https=(200|301|30[0-9])$' "$TMP/mode-https.out" || fail "https mode did not allow TLS to an arbitrary host: $(cat "$TMP/mode-https.out")"
grep -Eq '^http=403$' "$TMP/mode-https.out" || fail "https mode allowed plaintext to a non-allowlisted host: $(cat "$TMP/mode-https.out")"
grep -q '^dns=resolved$' "$TMP/mode-https.out" || fail "https mode did not resolve arbitrary names: $(cat "$TMP/mode-https.out")"
pass "https mode (default) allows TLS to any host, denies plaintext, resolves all names"

"$HSH" run --config "$TMP/mode-config.hsh" --monitor-db "$DB" --network allowlist >"$TMP/mode-allowlist.out" 2>&1 <<'CMDS' || true
curl -sS -o /dev/null -w 'https=%{http_code}\n' --max-time 15 https://example.com/ 2>&1 | tail -1
CMDS
grep -Eq '^https=000$' "$TMP/mode-allowlist.out" || fail "--network allowlist did not deny TLS to a non-allowlisted host: $(cat "$TMP/mode-allowlist.out")"
pass "--network allowlist restores the deny-by-default policy"

"$HSH" run --config "$TMP/mode-config.hsh" --monitor-db "$DB" --network none >"$TMP/mode-none.out" 2>&1 <<'CMDS' || true
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
  "version": 1,
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash",
  "mounts": ["$TMP/workspace:/workspace"],
  "network": { "mode": "allowlist", "allow": ["host.containers.internal"] },
  "secrets": {
    "API_KEY": { "from": "hsh://fn-store-key", "hosts": ["host.containers.internal"], "header": "Authorization", "prefix": "Bearer " }
  }
}
EOF
"$HSH" run --config "$TMP/store-config.hsh" --monitor-db "$DB" --secret-db "$TMP/secretstore/secrets.db" >"$TMP/store-run.out" 2>&1 <<'CMDS' || true
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

"$HSH" run --config "$TMP/store-config.hsh" --monitor-db "$DB" --secret-db "$TMP/secretstore/secrets.db" >"$TMP/store-missing.out" 2>&1 <<'CMDS' || true
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
LABEL org.ifup.hsh='{"version":1,"shell":"/bin/bash","network":{"mode":"allowlist","allow":["host.containers.internal"]},"agent":{"launch":["bash","-c","echo HARNESS-LAUNCHED; sleep 120"]}}'
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
  "version": 1,
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
LABEL org.ifup.hsh='{"version":1,"shell":"/bin/bash","network":{"mode":"allowlist","allow":["host.containers.internal"]}}'
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
LABEL org.ifup.hsh='{"version":1,"shell":"/bin/bash","network":{"mode":"allowlist","allow":["host.containers.internal"]},"monitor":{"enabled":false}}'
EOF
	podman build -q -t localhost/hsh-nomonitor-test:latest -f "$TMP/nomonitor.Containerfile" "$TMP" >/dev/null || fail "could not build the no-monitor test image"
	python3 "$REPO/test/inline.py" "$HSH" "$TMP/detachws" localhost/hsh-detach-test:latest "$XDG_STATE_HOME/hsh" localhost/hsh-nomonitor-test:latest || fail "the inline review driver failed"
	pass "the status line, Ctrl+Space dialog, pause, and approval work through the relay"
	python3 "$REPO/test/security.py" "$HSH" "$TMP/detachws" localhost/hsh-detach-test:latest "$XDG_STATE_HOME/hsh" || fail "the agent isolation driver failed"
	pass "the agent cannot open the monitor or forge the status line: queries, replies, and split escape sequences are inert"
else
	echo "SKIP: inline review checks (no python3 for the pty driver)"
fi

echo "== step 19: denied requests are held for approval (network.approval_wait)"
HOLD_WS="$TMP/holdws"
mkdir -p "$HOLD_WS"
cat >"$TMP/hold.config.hsh" <<EOF
{
  "version": 1,
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash",
  "mounts": ["$HOLD_WS:/workspace"],
  "network": { "mode": "allowlist", "allow": ["host.containers.internal"], "approval_wait": "30s" }
}
EOF
cat >"$TMP/hold-off.config.hsh" <<EOF
{
  "version": 1,
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
(cd "$HOLD_WS" && "$HSH" run --config "$TMP/hold.config.hsh" --monitor-db "$DB") >"$TMP/hold-run.out" 2>&1 <<'CMDS' &
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
case "$HELD_CODE" in 000|403|"") fail "the approved request did not reach the host (code=$HELD_CODE): $(cat "$TMP/hold-run.out")" ;; esac
pass "approving the host releases the held request, which then succeeds (HTTP $HELD_CODE)"

BEFORE_IDS="$(session_ids)"
(cd "$HOLD_WS" && "$HSH" run --config "$TMP/hold-off.config.hsh" --monitor-db "$DB") >"$TMP/hold-off.out" 2>&1 <<'CMDS' &
sleep 3
curl -s -m 30 -w '\noptout-result code=%{http_code} time=%{time_total}\n' http://blocked-optout.example.test/ 2>&1
exit
CMDS
OFF_PID=$!
start_attended_loop "$BEFORE_IDS" "$OFF_PID"
wait "$OFF_PID" || true
kill "$ATTENDED_PID" 2>/dev/null || true
OFF_TIME="$(sed -n 's/.*optout-result code=403 time=\([0-9.]*\).*/\1/p' "$TMP/hold-off.out" | head -1)"
[ -n "$OFF_TIME" ] || fail "an opted-out session did not answer 403: $(cat "$TMP/hold-off.out")"
awk -v t="$OFF_TIME" 'BEGIN { exit !(t < 10) }' || fail "an opted-out session still waited ($OFF_TIME s)"
grep -q "blocked on purpose" "$TMP/hold-off.out" || fail "the denial text does not tell the agent what to do: $(cat "$TMP/hold-off.out")"
pass "holding is off by default: a denied request fails at once, with a message aimed at the agent"

OFF_SESSION="$(for id in $(session_ids); do case " $BEFORE_IDS " in *" $id "*) ;; *) echo "$id" ;; esac; done | head -1)"
"$HSH" proxy hold --session "$OFF_SESSION" | grep -q "holding is off, the default" || fail "hsh proxy hold did not report that holding is off by default"
"$HSH" proxy hold --session "$OFF_SESSION" 4s | grep -q "up to 4s" || fail "hsh proxy hold 4s did not apply"
(cd "$HOLD_WS" && "$HSH" run --monitor-db "$DB" --resume "$OFF_SESSION") >"$TMP/hold-4s.out" 2>&1 <<'CMDS' &
sleep 3
curl -s -m 30 -w '\nfourgs-result code=%{http_code} time=%{time_total}\n' http://blocked-four.example.test/ 2>&1
exit
CMDS
FOUR_PID=$!
start_attended_loop "$BEFORE_IDS" "$FOUR_PID"
wait "$FOUR_PID" || true
kill "$ATTENDED_PID" 2>/dev/null || true
FOUR_TIME="$(sed -n 's/.*fourgs-result code=403 time=\([0-9.]*\).*/\1/p' "$TMP/hold-4s.out" | head -1)"
[ -n "$FOUR_TIME" ] || fail "the 4s hold did not end in a 403: $(cat "$TMP/hold-4s.out")"
awk -v t="$FOUR_TIME" 'BEGIN { exit !(t >= 3.5 && t < 20) }' || fail "an unanswered hold should last about 4s, got $FOUR_TIME s"
pass "hsh proxy hold 4s turns holding on live; an unapproved request is held about 4s, then denied"

echo "== step 20: hsh update — self-update from a fake release, metrics, session counter"
UPD_ARCH=amd64
[ "$(uname -m)" = "aarch64" ] && UPD_ARCH=arm64
UPD_ASSET="hsh-linux-$UPD_ARCH"
RELDIR="$TMP/releases"
mkdir -p "$RELDIR/v1.2.3" "$RELDIR/v1.3.0"
printf 'fake hsh release binary v1.2.3\n' > "$RELDIR/v1.2.3/$UPD_ASSET"
printf 'fake hsh release binary v1.3.0\n' > "$RELDIR/v1.3.0/$UPD_ASSET"
(cd "$RELDIR/v1.2.3" && sha256sum "$UPD_ASSET" > SHA256SUMS)
(cd "$RELDIR/v1.3.0" && sha256sum "$UPD_ASSET" > SHA256SUMS)
"$TMP/releasesrv" -addr 127.0.0.1:8898 -releases "$RELDIR" -metrics-log "$TMP/metrics-received.log" -hits-log "$TMP/latest-hits.log" &
RELEASESRV_PID=$!
sleep 0.5
curl -s -o /dev/null -w 'releasesrv-latest=%{http_code}\n' http://127.0.0.1:8898/latest | grep -q 'releasesrv-latest=302' || fail "the fake release server did not redirect /latest"
UPD_ENV="HSH_RELEASE_BASE=http://127.0.0.1:8898 HSH_METRICS_URL=http://127.0.0.1:8898/hooks/metrics_report XDG_STATE_HOME=$TMP/upd-xdg"
mkdir -p "$TMP/upd-xdg" "$TMP/upd-a" "$TMP/upd-b" "$TMP/upd-c" "$TMP/upd-ro"
cp "$TMP/hsh-v1.2.3" "$TMP/upd-a/hsh"

# two throwaway sessions bump the session counter
cat > "$TMP/upd-config.hsh" <<'EOF'
{
	"version": 1,
	"image": "localhost/hsh-example:latest",
	"shell": "/bin/bash"
}
EOF
for i in 1 2; do
	env XDG_STATE_HOME="$TMP/upd-xdg" "$TMP/hsh" run --rm --config "$TMP/upd-config.hsh" --monitor-db "$TMP/upd-deny.db" >"$TMP/upd-sess$i.out" 2>&1 <<'CMDS' || true
exit
CMDS
done

# target == current: up to date, no download, no metrics POST, counter untouched
env $UPD_ENV "$TMP/upd-a/hsh" update --version v1.2.3 >"$TMP/upd-uptodate.out" 2>&1
grep -q 'hsh v1.2.3 is up to date' "$TMP/upd-uptodate.out" || fail "an up-to-date binary was not recognized as such: $(cat "$TMP/upd-uptodate.out")"
[ ! -s "$TMP/metrics-received.log" ] || fail "the up-to-date run posted metrics: $(cat "$TMP/metrics-received.log")"
pass "an up-to-date install reports so, downloads nothing, and posts no metrics"

# real update: binary replaced, metrics POST carries the session count
grep -q 'No identifiable information is uploaded' "$TMP/upd-uptodate.out" || fail "the metrics notice was not printed: $(cat "$TMP/upd-uptodate.out")"
env $UPD_ENV "$TMP/upd-a/hsh" update >"$TMP/upd-a.out" 2>&1
grep -q '✓ hsh v1.3.0 installed (was v1.2.3)' "$TMP/upd-a.out" || fail "the update did not report success: $(cat "$TMP/upd-a.out")"
cmp -s "$TMP/upd-a/hsh" "$RELDIR/v1.3.0/$UPD_ASSET" || fail "the running binary was not replaced by the release asset"
[ "$(wc -l < "$TMP/metrics-received.log")" = "1" ] || fail "expected exactly one metrics POST, got: $(cat "$TMP/metrics-received.log")"
grep -q "\"product\":\"hsh\",\"version\":\"v1.2.3\",\"platform\":\"linux/$UPD_ARCH\",\"sessions_since_last_update\":2" "$TMP/metrics-received.log" || fail "the metrics report was wrong: $(cat "$TMP/metrics-received.log")"
pass "hsh update replaces the running binary and reports the pre-update version with the session count"

# the counter reset: a second install reports zero sessions
cp "$TMP/hsh-v1.2.3" "$TMP/upd-b/hsh"
env $UPD_ENV "$TMP/upd-b/hsh" update >"$TMP/upd-b.out" 2>&1
grep -q "\"sessions_since_last_update\":0" "$TMP/metrics-received.log" || fail "the session counter did not reset after the previous update: $(cat "$TMP/metrics-received.log")"
pass "the session counter resets after each update's report"

# --no-metrics: no notice, no POST, update still happens
cp "$TMP/hsh-v1.2.3" "$TMP/upd-c/hsh"
env $UPD_ENV "$TMP/upd-c/hsh" update --no-metrics >"$TMP/upd-c.out" 2>&1
[ "$(wc -l < "$TMP/metrics-received.log")" = "2" ] || fail "--no-metrics still posted metrics: $(cat "$TMP/metrics-received.log")"
grep -q 'No identifiable information is uploaded' "$TMP/upd-c.out" && fail "--no-metrics still printed the metrics notice"
cmp -s "$TMP/upd-c/hsh" "$RELDIR/v1.3.0/$UPD_ASSET" || fail "--no-metrics did not update the binary"
pass "--no-metrics skips the notice and the report but still updates"

# unwritable install dir: clear error, nothing replaced
cp "$TMP/hsh-v1.2.3" "$TMP/upd-ro/hsh"
chmod 555 "$TMP/upd-ro"
env $UPD_ENV "$TMP/upd-ro/hsh" update --version v1.3.0 >"$TMP/upd-ro.out" 2>&1 && fail "updating a read-only install dir succeeded"
grep -q 'could not write to' "$TMP/upd-ro.out" || fail "the read-only install error does not explain the problem: $(cat "$TMP/upd-ro.out")"
cmp -s "$TMP/upd-ro/hsh" "$TMP/hsh-v1.2.3" || fail "the read-only install was modified"
chmod 755 "$TMP/upd-ro"
pass "an unwritable install dir fails with guidance and replaces nothing"

echo "== step 21: hsh update --auto — background check, inline install"
mkdir -p "$RELDIR/v2.0.0"
(cd "$REPO" && go build -ldflags "-X hsh/internal/version.Tag=v2.0.0" -o "$RELDIR/v2.0.0/$UPD_ASSET" .)
(cd "$RELDIR/v2.0.0" && sha256sum "$UPD_ASSET" > SHA256SUMS)
mkdir -p "$TMP/au-xdg" "$TMP/au-a" "$TMP/au-c"
cp "$TMP/hsh-v1.2.3" "$TMP/au-a/hsh"
cp "$TMP/hsh-v1.2.3" "$TMP/au-c/hsh"
AU_ENV="HSH_RELEASE_BASE=http://127.0.0.1:8898 HSH_METRICS_URL=http://127.0.0.1:8898/hooks/metrics_report XDG_STATE_HOME=$TMP/au-xdg"

AU_HITS0="$(wc -l < "$TMP/latest-hits.log" 2>/dev/null || echo 0)"
env $AU_ENV "$TMP/au-a/hsh" version >"$TMP/au-off.out" 2>&1
sleep 1
grep -q 'hsh v1.2.3' "$TMP/au-off.out" || fail "version output wrong before any update: $(cat "$TMP/au-off.out")"
[ ! -e "$TMP/au-xdg/hsh/metrics.db" ] || fail "an opt-out install created update-check state"
[ "$(wc -l < "$TMP/latest-hits.log" 2>/dev/null || echo 0)" = "$AU_HITS0" ] || fail "an opt-out install checked for releases"
pass "autoupdate is off by default: no background check, no state, no update"

env $AU_ENV "$TMP/au-a/hsh" update --auto >"$TMP/au-on.out" 2>&1
grep -q 'automatic updates enabled' "$TMP/au-on.out" || fail "hsh update --auto did not confirm: $(cat "$TMP/au-on.out")"
pass "hsh update --auto opts the install into automatic updates"

env $AU_ENV "$TMP/au-a/hsh" version >"$TMP/au-v1.out" 2>&1
grep -q 'hsh v1.2.3' "$TMP/au-v1.out" || fail "the first command after enabling changed version output: $(cat "$TMP/au-v1.out")"
sleep 1
[ "$(wc -l < "$TMP/latest-hits.log" 2>/dev/null || echo 0)" -gt "$AU_HITS0" ] || fail "the enabled install did not check for releases in the background"
sleep 1
cmp -s "$TMP/au-a/hsh" "$TMP/hsh-v1.2.3" || fail "the background check touched the binary"
pass "the background check records the pending release without touching the binary"

env $AU_ENV "$TMP/au-a/hsh" version >"$TMP/au-v2.out" 2>&1
grep -q 'hsh v2.0.0 is available (this is v1.2.3), updating' "$TMP/au-v2.out" || fail "the next command did not announce the inline update: $(cat "$TMP/au-v2.out")"
grep -q 'No identifiable information is uploaded' "$TMP/au-v2.out" || fail "the inline update did not print the metrics notice: $(cat "$TMP/au-v2.out")"
grep -q '✓ hsh v2.0.0 installed (was v1.2.3)' "$TMP/au-v2.out" || fail "the inline update did not report success: $(cat "$TMP/au-v2.out")"
cmp -s "$TMP/au-a/hsh" "$RELDIR/v2.0.0/$UPD_ASSET" || fail "the inline update did not replace the binary"
tail -1 "$TMP/metrics-received.log" | grep -q '"version":"v1.2.3"' || fail "the inline update did not report the pre-update version: $(tail -1 "$TMP/metrics-received.log")"
pass "the next hsh command installs the pending release inline, exactly as hsh update would"

env $AU_ENV "$TMP/au-a/hsh" version >"$TMP/au-v3.out" 2>&1
grep -q 'hsh v2.0.0' "$TMP/au-v3.out" || fail "the updated binary does not run: $(cat "$TMP/au-v3.out")"
grep -q 'available' "$TMP/au-v3.out" && fail "a settled install still printed an update notice: $(cat "$TMP/au-v3.out")"
pass "a settled install prints nothing further"

HITS1="$(wc -l < "$TMP/latest-hits.log")"
env $AU_ENV "$TMP/au-a/hsh" version >/dev/null 2>&1
env $AU_ENV "$TMP/au-a/hsh" version >/dev/null 2>&1
sleep 1
[ "$HITS1" = "$(wc -l < "$TMP/latest-hits.log")" ] || fail "a fresh check was re-spawned immediately (throttle broken)"
pass "the check throttle holds: a fresh check is not re-spawned"

env $AU_ENV "$TMP/au-a/hsh" update --autoupdate-check >"$TMP/au-uptodate.out" 2>&1
grep -q 'hsh v2.0.0 is up to date' "$TMP/au-uptodate.out" || fail "an up-to-date check printed the wrong thing: $(cat "$TMP/au-uptodate.out")"
pass "an up-to-date install reports so and installs nothing"

HITS2="$(wc -l < "$TMP/latest-hits.log")"
env $AU_ENV HSH_NO_UPDATE_CHECK=1 "$TMP/au-c/hsh" update --auto >/dev/null
env $AU_ENV HSH_NO_UPDATE_CHECK=1 "$TMP/au-c/hsh" version >"$TMP/au-c1.out" 2>&1
sleep 1
[ "$HITS2" = "$(wc -l < "$TMP/latest-hits.log")" ] || fail "HSH_NO_UPDATE_CHECK did not stop the background check"
env $AU_ENV "$TMP/au-c/hsh" update --autoupdate-check >"$TMP/au-chk.out" 2>&1
grep -q 'hsh v2.0.0 is available (this is v1.2.3)' "$TMP/au-chk.out" || fail "the hand-run check did not report the pending release: $(cat "$TMP/au-chk.out")"
env $AU_ENV HSH_NO_UPDATE_CHECK=1 "$TMP/au-c/hsh" version >"$TMP/au-c2.out" 2>&1
grep -q 'available' "$TMP/au-c2.out" && fail "HSH_NO_UPDATE_CHECK did not suppress the update notice: $(cat "$TMP/au-c2.out")"
cmp -s "$TMP/au-c/hsh" "$TMP/hsh-v1.2.3" || fail "HSH_NO_UPDATE_CHECK did not suppress the inline update"
pass "HSH_NO_UPDATE_CHECK silences both the background check and the inline update"

kill "$RELEASESRV_PID"
RELEASESRV_PID=""
env $AU_ENV "$TMP/au-c/hsh" version >"$TMP/au-fail.out" 2>&1
grep -q 'warning: automatic update to v2.0.0 failed' "$TMP/au-fail.out" || fail "a failed inline update did not warn: $(cat "$TMP/au-fail.out")"
grep -q 'hsh v1.2.3' "$TMP/au-fail.out" || fail "a failed inline update broke the command: $(cat "$TMP/au-fail.out")"
cmp -s "$TMP/au-c/hsh" "$TMP/hsh-v1.2.3" || fail "a failed inline update modified the binary"
pass "a failed inline update warns and never fails the command"

echo "== step 22: session.db — the version event log per session"
SESS_XDG="$TMP/sess-xdg"
mkdir -p "$SESS_XDG" "$TMP/sess-ws"
cat > "$TMP/sess.config.hsh" <<EOF
{
	"version": 1,
	"image": "localhost/hsh-example:latest",
	"shell": "/bin/bash",
	"mounts": ["$TMP/sess-ws:/workspace"]
}
EOF
(cd "$TMP/sess-ws" && env XDG_STATE_HOME="$SESS_XDG" "$TMP/hsh" run --config "$TMP/sess.config.hsh" --monitor-db "$TMP/sess-deny.db") >"$TMP/sess1.out" 2>&1 <<'CMDS' || true
exit
CMDS
SESS_ID="$(ls "$SESS_XDG/hsh/sessions" 2>/dev/null | head -1)"
[ -n "$SESS_ID" ] || fail "the session state directory never appeared: $(cat "$TMP/sess1.out")"
SESS_DB="$SESS_XDG/hsh/sessions/$SESS_ID/session.db"
[ -f "$SESS_DB" ] || fail "session.db was not created: $(cat "$TMP/sess1.out")"
grep -aq 'created' "$SESS_DB" "$SESS_DB-wal" 2>/dev/null || fail "session.db has no created event"
grep -aq 'hsh dev' "$SESS_DB" "$SESS_DB-wal" 2>/dev/null || fail "session.db does not record the creating binary's version"
(cd "$TMP/sess-ws" && env XDG_STATE_HOME="$SESS_XDG" "$TMP/au-a/hsh" run --config "$TMP/sess.config.hsh" --monitor-db "$TMP/sess-deny.db" --resume "$SESS_ID") >"$TMP/sess2.out" 2>&1 <<'CMDS' || true
exit
CMDS
grep -aq 'resumed' "$SESS_DB" "$SESS_DB-wal" 2>/dev/null || fail "session.db has no resumed event after --resume: $(cat "$TMP/sess2.out")"
grep -aq 'hsh v2.0.0' "$SESS_DB" "$SESS_DB-wal" 2>/dev/null || fail "session.db does not record the resuming binary's version"
pass "session.db records every binary that created or re-entered the session"
env XDG_STATE_HOME="$SESS_XDG" "$TMP/hsh" rm "hsh-$SESS_ID" >/dev/null 2>&1 || podman pod rm -f "hsh-$SESS_ID" >/dev/null 2>&1 || true
[ ! -e "$SESS_DB" ] || fail "hsh rm did not remove session.db"

echo "== step 23: ssh into a session — port forwards to the sandbox's loopback"
if ! command -v ssh >/dev/null 2>&1; then
	echo "SKIP: no ssh client"
else
	mkdir -p "$TMP/sshws"
	rm -f "$TMP/sshws/done"
	cat >"$TMP/sshws/server.js" <<'JS'
require("http").createServer((q, r) => {
	if (q.url === "/big") {
		r.end("a".repeat(5 * 1024 * 1024));
		return;
	}
	r.end("hello from the sandbox " + q.url + "\n");
}).listen(8000, "127.0.0.1");
JS
	(cd "$TMP/sshws" && "$HSH" run localhost/hsh-example:latest) >"$TMP/ssh-run.out" 2>&1 <<'CMDS' &
node /workspace/server.js &
while [ ! -f /workspace/done ]; do sleep 0.5; done
exit
CMDS
	SSH_SESSION_PID=$!
	SSH_POD=""
	for _ in $(seq 1 60); do
		SSH_POD="$(podman pod ls --filter "label=hsh.workspace=$TMP/sshws" --format '{{.Name}}' | head -1)"
		[ -n "$SSH_POD" ] && break
		sleep 0.5
	done
	[ -n "$SSH_POD" ] || fail "the ssh test session never started: $(cat "$TMP/ssh-run.out")"
	for _ in $(seq 1 60); do
		[ "$(podman inspect --format '{{.State.Status}}' "$SSH_POD-ws" 2>/dev/null)" = "running" ] && break
		sleep 0.5
	done
	[ "$(podman inspect --format '{{.State.Status}}' "$SSH_POD-ws" 2>/dev/null)" = "running" ] || fail "the ssh test session's workspace container never ran: $(cat "$TMP/ssh-run.out")"
	for _ in $(seq 1 60); do
		[ "$(podman inspect --format '{{.State.Running}}' "$SSH_POD-ws" 2>/dev/null)" = "true" ] && break
		sleep 0.5
	done
	sed "s|ProxyCommand hsh|ProxyCommand $HSH|" < <("$HSH" ssh config) >"$TMP/ssh_config"
	grep -q "ProxyCommand $HSH ssh proxy %h" "$TMP/ssh_config" || fail "hsh ssh config did not produce the ProxyCommand: $(cat "$TMP/ssh_config")"
	SSH_FWD_PID=""
	ssh -F "$TMP/ssh_config" -N -o ExitOnForwardFailure=yes \
		-L 18000:localhost:8000 -L 18001:localhost:8001 -L 18002:example.com:443 "$SSH_POD" >"$TMP/ssh-fwd.out" 2>&1 &
	SSH_FWD_PID=$!
	GOT=""
	for _ in $(seq 1 40); do
		GOT="$(curl -s --max-time 3 http://127.0.0.1:18000/abc || true)"
		[ -n "$GOT" ] && break
		sleep 0.5
	done
	[ "$GOT" = "hello from the sandbox /abc" ] || fail "ssh -L did not reach the server in the sandbox: '$GOT' $(cat "$TMP/ssh-fwd.out")"
	pass "ssh -L reaches a server on the sandbox's loopback"

	SIZE="$(curl -s --max-time 20 http://127.0.0.1:18000/big | wc -c)"
	[ "$SIZE" = "5242880" ] || fail "a 5 MiB response through the forward arrived as $SIZE bytes"
	CURL_PIDS=""
	for i in 1 2 3 4 5 6 7 8; do
		curl -s --max-time 10 "http://127.0.0.1:18000/p$i" >"$TMP/ssh-par-$i.out" &
		CURL_PIDS="$CURL_PIDS $!"
	done
	wait $CURL_PIDS
	for i in 1 2 3 4 5 6 7 8; do
		[ "$(cat "$TMP/ssh-par-$i.out")" = "hello from the sandbox /p$i" ] || fail "concurrent request $i through one ssh connection failed: $(cat "$TMP/ssh-par-$i.out")"
	done
	pass "large responses and concurrent connections work over one ssh connection"

	if curl -s --max-time 5 -o /dev/null http://127.0.0.1:18001/; then
		fail "a forward to a closed sandbox port returned a response"
	fi
	GOT="$(curl -s --max-time 3 http://127.0.0.1:18000/after-error || true)"
	[ "$GOT" = "hello from the sandbox /after-error" ] || fail "a failed forward broke the ssh connection: '$GOT'"
	pass "a closed sandbox port fails that connection only"

	if curl -s --max-time 5 -o /dev/null https://127.0.0.1:18002/ -k; then
		fail "a forward to example.com was served"
	fi
	if podman logs "$SSH_POD-proxy" 2>&1 | grep -q "example.com"; then
		fail "forwarding to example.com reached the egress proxy"
	fi
	pass "ssh -L to anything but the sandbox's loopback is refused"

	if timeout 20 ssh -F "$TMP/ssh_config" -N -R 9000:localhost:80 -o ExitOnForwardFailure=yes "$SSH_POD" >"$TMP/ssh-R.out" 2>&1; then
		fail "ssh -R was accepted"
	fi
	pass "ssh -R is refused"

	OUT="$(ssh -F "$TMP/ssh_config" "$SSH_POD" 'echo shell-ok; echo $HOME' 2>&1)"
	echo "$OUT" | grep -q "^shell-ok$" || fail "ssh into the session did not run a command: $OUT"
	echo "$OUT" | grep -q "^/home/hsh$" || fail "the ssh shell did not inherit the session environment: $OUT"
	ssh -F "$TMP/ssh_config" "$SSH_POD" 'cat /proc/net/dev' >"$TMP/ssh-netdev.out" 2>/dev/null || fail "ssh shell could not read /proc/net/dev"
	grep -q "hsh1" "$TMP/ssh-netdev.out" || fail "the ssh shell did not run in the session's inner network namespace: $(cat "$TMP/ssh-netdev.out")"
	ssh -F "$TMP/ssh_config" "$SSH_POD" 'exit 42' >/dev/null 2>&1 && RC=0 || RC=$?
	[ "$RC" = "42" ] || fail "the ssh shell's exit status did not propagate: $RC"
	OUT="$(ssh -tt -F "$TMP/ssh_config" "$SSH_POD" 'echo pty-ok' 2>/dev/null | tr -d '\r\n')"
	[ "$OUT" = "pty-ok" ] || fail "a pty session over ssh did not work: '$OUT'"
	ssh -F "$TMP/ssh_config" "$SSH_POD" 'env | grep -c "^http_proxy=http://10.77.0.1:8080$"' >"$TMP/ssh-env.out" 2>/dev/null
	grep -q "^1$" "$TMP/ssh-env.out" || fail "the ssh shell does not use the egress proxy: $(cat "$TMP/ssh-env.out")"
	pass "ssh into the session runs commands in the sandbox's shell environment"

	kill "$SSH_FWD_PID" 2>/dev/null || true
	touch "$TMP/sshws/done"
	wait "$SSH_SESSION_PID" 2>/dev/null || true
	if [ -n "$(ss -ltn 2>/dev/null | grep -E ':(18000|18001|18002) ' || true)" ]; then
		fail "a forwarding listener outlived the ssh client"
	fi
	pass "no listeners are left behind"
fi

echo "== step 24: the network notice reaches the agent (launch flag, managed block)"
mkdir -p "$TMP/notice-ws"
printf '# user rules\nkeep me\n' >"$TMP/notice-ws/ctx.md"
notice_config() {
	cat >"$1" <<EOF
{
	"version": 1,
	"image": "localhost/hsh-example:latest",
	"shell": "/bin/bash",
	"mounts": ["$TMP/notice-ws:/workspace"],
	"network": {"mode": "allowlist", "allow": [], "notice": $2},
	"agent": {
		"command": ["/bin/bash", "-c", "echo ARGC=\$#; echo FIRST=\$1; echo SECOND=\$2; if [ -r /hsh-run/notice/network-policy.md ]; then grep -q '403 Forbidden' /hsh-run/notice/network-policy.md && echo NOTICE-TEXT; else echo NOTICE-ABSENT; fi; touch /hsh-run/notice/x 2>/dev/null && echo NOTICE-WRITABLE; grep -c 'hsh:network-policy begin' ~/.hsh-notice/AGENTS.md 2>/dev/null | sed 's/^/HOMEBLOCKS=/'; true", "_", "--append-system-prompt", "{network_policy}"],
		"context_files": ["/workspace/ctx.md", "~/.hsh-notice/AGENTS.md"]
	}
}
EOF
}
notice_config "$TMP/notice-on.hsh" true
notice_config "$TMP/notice-off.hsh" false
echo "report the network notice" >"$TMP/notice-tasks.txt"

BEFORE_IDS="$(session_ids)"
(cd "$TMP/notice-ws" && "$HSH" run --config "$TMP/notice-on.hsh" --monitor-db "$DB" --tasks "$TMP/notice-tasks.txt") >"$TMP/notice-on.out" 2>&1 || fail "the notice session failed: $(cat "$TMP/notice-on.out")"
sed -i 's/\r$//' "$TMP/notice-on.out"
NOTICE_SESSION="$(for id in $(session_ids); do case " $BEFORE_IDS " in *" $id "*) ;; *) echo "$id" ;; esac; done | head -1)"
grep -q 'ARGC=3$' "$TMP/notice-on.out" || fail "the agent argv lost arguments: $(cat "$TMP/notice-on.out")"
grep -qx 'SECOND=/hsh-run/notice/network-policy.md' "$TMP/notice-on.out" || fail "{network_policy} was not replaced with the notice path: $(cat "$TMP/notice-on.out")"
grep -qx 'NOTICE-TEXT' "$TMP/notice-on.out" || fail "the notice file is not readable at the stable path: $(cat "$TMP/notice-on.out")"
grep -qx 'NOTICE-WRITABLE' "$TMP/notice-on.out" && fail "the notice mount is writable from the sandbox"
grep -qx 'HOMEBLOCKS=1' "$TMP/notice-on.out" || fail "the managed block was not created in a missing ~ instruction file: $(cat "$TMP/notice-on.out")"
[ -f "$XDG_STATE_HOME/hsh/sessions/$NOTICE_SESSION/notice/network-policy.md" ] || fail "the notice was not written into the session directory"
pass "the notice is mounted read-only and {network_policy} expands to its path"

"$HSH" run --monitor-db "$DB" --resume "$NOTICE_SESSION" >"$TMP/notice-resume.out" 2>&1 </dev/null || fail "resuming the notice session failed: $(cat "$TMP/notice-resume.out")"
sed -i 's/\r$//' "$TMP/notice-resume.out"
grep -qx 'SECOND=/hsh-run/notice/network-policy.md' "$TMP/notice-resume.out" || fail "the resumed agent did not get the notice: $(cat "$TMP/notice-resume.out")"
[ "$(grep -c 'hsh:network-policy begin' "$TMP/notice-ws/ctx.md")" = 1 ] || fail "two starts did not leave exactly one managed block: $(cat "$TMP/notice-ws/ctx.md")"
[ "$(head -2 "$TMP/notice-ws/ctx.md")" = "$(printf '# user rules\nkeep me')" ] || fail "the user's own instructions were not preserved: $(cat "$TMP/notice-ws/ctx.md")"
pass "a pre-existing instruction file keeps its text and one managed block across two starts"

BEFORE_IDS="$(session_ids)"
(cd "$TMP/notice-ws" && "$HSH" run --config "$TMP/notice-off.hsh" --monitor-db "$DB" --tasks "$TMP/notice-tasks.txt") >"$TMP/notice-off.out" 2>&1 || fail "the notice:false session failed: $(cat "$TMP/notice-off.out")"
sed -i 's/\r$//' "$TMP/notice-off.out"
OFF_NOTICE_SESSION="$(for id in $(session_ids); do case " $BEFORE_IDS " in *" $id "*) ;; *) echo "$id" ;; esac; done | head -1)"
grep -q 'ARGC=1$' "$TMP/notice-off.out" || fail "network.notice: false did not drop the whole flag: $(cat "$TMP/notice-off.out")"
grep -qx 'NOTICE-ABSENT' "$TMP/notice-off.out" || fail "network.notice: false still provided a notice file"
[ ! -e "$XDG_STATE_HOME/hsh/sessions/$OFF_NOTICE_SESSION/notice/network-policy.md" ] || fail "network.notice: false wrote a notice file"
grep -q 'hsh:network-policy' "$TMP/notice-ws/ctx.md" && fail "a disabled notice left its managed block behind: $(cat "$TMP/notice-ws/ctx.md")"
[ "$(cat "$TMP/notice-ws/ctx.md")" = "$(printf '# user rules\nkeep me')" ] || fail "removing the block changed the user's text: $(cat "$TMP/notice-ws/ctx.md")"
pass "network.notice: false writes no notice, drops the flag, and removes an existing managed block"

(cd "$TMP/notice-ws" && "$HSH" run --config "$TMP/notice-on.hsh" --monitor-db "$DB" --network none --tasks "$TMP/notice-tasks.txt") >"$TMP/notice-none.out" 2>&1 || fail "the none-mode notice session failed: $(cat "$TMP/notice-none.out")"
sed -i 's/\r$//' "$TMP/notice-none.out"
grep -q 'ARGC=1$' "$TMP/notice-none.out" || fail "none mode did not drop the notice flag: $(cat "$TMP/notice-none.out")"
grep -qx 'NOTICE-ABSENT' "$TMP/notice-none.out" || fail "none mode provided a notice"
pass "none mode gets no notice"

echo "== step 25: hsh forward and hsh run --forward"
mkdir -p "$TMP/fwdws"
rm -f "$TMP/fwdws/done" "$TMP/fwdws/start2"
cat >"$TMP/fwdws/server.js" <<'JS'
const port = parseInt(process.argv[2], 10);
require("http").createServer((q, r) => {
	r.end("hello from the sandbox port " + port + " " + q.url + "\n");
}).listen(port, "127.0.0.1");
JS
python3 -m http.server 18199 --bind 127.0.0.1 >/dev/null 2>&1 &
BUSY_PID=$!
sleep 1
if (cd "$TMP/fwdws" && "$HSH" run --forward 18199:1 localhost/hsh-example:latest) >"$TMP/fwd-busy.out" 2>&1 </dev/null; then
	kill "$BUSY_PID" 2>/dev/null || true
	fail "hsh run --forward with a busy local port started anyway"
fi
kill "$BUSY_PID" 2>/dev/null || true
grep -q "cannot listen on 127.0.0.1:18199" "$TMP/fwd-busy.out" || fail "a busy --forward port did not fail clearly: $(cat "$TMP/fwd-busy.out")"
[ -z "$(test_pods | while read -r p; do podman pod ls --filter "name=$p" --format '{{.Labels}}' | grep -F "$TMP/fwdws"; done)" ] || fail "a failed --forward left a session behind"
pass "hsh run --forward fails before starting when the local port is busy"

(cd "$TMP/fwdws" && "$HSH" run --forward 18100:18200 localhost/hsh-example:latest) >"$TMP/fwd-run.out" 2>&1 <<'CMDS' &
node /workspace/server.js 18200 &
while [ ! -f /workspace/start2 ]; do sleep 0.3; done
node /workspace/server.js 18201 &
while [ ! -f /workspace/done ]; do sleep 0.5; done
exit
CMDS
FWD_SESSION_PID=$!
GOT=""
for _ in $(seq 1 80); do
	GOT="$(curl -s --max-time 3 http://127.0.0.1:18100/x || true)"
	[ -n "$GOT" ] && break
	sleep 0.5
done
[ "$GOT" = "hello from the sandbox port 18200 /x" ] || fail "hsh run --forward did not reach the sandbox's server: '$GOT' $(cat "$TMP/fwd-run.out")"
pass "hsh run --forward 18100:18200 reaches a server in the sandbox while attached"

LISTED=""
for _ in $(seq 1 20); do
	LISTED="$(cd "$TMP/fwdws" && "$HSH" forward --list 2>&1 || true)"
	echo "$LISTED" | grep -q "^18200" && break
	sleep 0.5
done
echo "$LISTED" | grep -q "^18200" || fail "hsh forward --list did not show the sandbox's listener: $LISTED"
echo "$LISTED" | grep -q "^7788" && fail "hsh forward --list showed the relay's own port: $LISTED"
pass "hsh forward --list shows the ports the sandbox listens on, not the relay's"

(cd "$TMP/fwdws" && exec "$HSH" forward 18300:18200) >"$TMP/fwd-explicit.out" 2>&1 &
FWD_EXPLICIT_PID=$!
GOT=""
for _ in $(seq 1 40); do
	GOT="$(curl -s --max-time 3 http://127.0.0.1:18300/y || true)"
	[ -n "$GOT" ] && break
	sleep 0.5
done
[ "$GOT" = "hello from the sandbox port 18200 /y" ] || fail "hsh forward LOCAL:PORT did not forward: '$GOT' $(cat "$TMP/fwd-explicit.out")"
grep -q "forwarding 127.0.0.1:18300 -> the session's port 18200" "$TMP/fwd-explicit.out" || fail "hsh forward did not announce the forward: $(cat "$TMP/fwd-explicit.out")"
kill "$FWD_EXPLICIT_PID" 2>/dev/null || true
wait "$FWD_EXPLICIT_PID" 2>/dev/null || true
pass "hsh forward LOCAL:PORT works on an already-running session"

(cd "$TMP/fwdws" && exec "$HSH" forward) >"$TMP/fwd-auto.out" 2>&1 &
FWD_AUTO_PID=$!
for _ in $(seq 1 40); do
	grep -q "forwarding 127.0.0.1:18200" "$TMP/fwd-auto.out" && break
	sleep 0.5
done
GOT="$(curl -s --max-time 3 http://127.0.0.1:18200/auto || true)"
[ "$GOT" = "hello from the sandbox port 18200 /auto" ] || fail "auto mode did not forward the running server: '$GOT' $(cat "$TMP/fwd-auto.out")"
touch "$TMP/fwdws/start2"
for _ in $(seq 1 60); do
	grep -q "forwarding 127.0.0.1:18201" "$TMP/fwd-auto.out" && break
	sleep 0.5
done
GOT="$(curl -s --max-time 3 http://127.0.0.1:18201/late || true)"
[ "$GOT" = "hello from the sandbox port 18201 /late" ] || fail "auto mode did not pick up a server started later: '$GOT' $(cat "$TMP/fwd-auto.out")"
pass "hsh forward with no ports follows servers as they start listening"

kill "$FWD_AUTO_PID" 2>/dev/null || true
wait "$FWD_AUTO_PID" 2>/dev/null || true
touch "$TMP/fwdws/done"
wait "$FWD_SESSION_PID" 2>/dev/null || true
if curl -s --max-time 2 -o /dev/null http://127.0.0.1:18100/; then
	fail "the --forward listener outlived hsh run"
fi
pass "forward listeners go away with the process that owns them"

echo "== step 26: config and database schema versions"
VER_WS="$TMP/verws"
mkdir -p "$VER_WS"
ver_expect_failure() {
	local label="$1" want="$2"
	shift 2
	local before after
	before="$(session_ids)|$(test_pods)"
	if (cd "$VER_WS" && "$@") >"$TMP/ver.out" 2>&1 </dev/null; then
		fail "$label: hsh run succeeded"
	fi
	grep -q -- "$want" "$TMP/ver.out" || fail "$label: expected $(printf '%q' "$want") in: $(cat "$TMP/ver.out")"
	after="$(session_ids)|$(test_pods)"
	[ "$before" = "$after" ] || fail "$label: a session was created"
}
printf '%s\n' '{ "image": "localhost/hsh-example:latest", "shell": "/bin/bash" }' >"$TMP/ver-missing.hsh"
printf '%s\n' '{ "version": 99, "image": "localhost/hsh-example:latest", "shell": "/bin/bash" }' >"$TMP/ver-newer.hsh"
printf '%s\n' '{ "version": 1, "image": "localhost/hsh-example:latest", "network": { "alow": [] } }' >"$TMP/ver-typo.hsh"
ver_expect_failure "a seed config without a version" 'no "version" field (this hsh reads version 1)' \
	"$HSH" run --config "$TMP/ver-missing.hsh" --monitor-db "$DB"
ver_expect_failure "a seed config from a newer hsh" 'config version 99 is newer than this hsh reads (version 1); update hsh' \
	"$HSH" run --config "$TMP/ver-newer.hsh" --monitor-db "$DB"
ver_expect_failure "a seed config with an unknown field" 'unknown field "alow"' \
	"$HSH" run --config "$TMP/ver-typo.hsh" --monitor-db "$DB"
pass "seed configs without a version, from a newer hsh, or with unknown fields are refused and create no session"

cat >"$TMP/ver-label.Containerfile" <<'EOF'
FROM localhost/hsh-example:latest
LABEL org.ifup.hsh='{"version":99,"shell":"/bin/bash"}'
EOF
podman build -q -t localhost/hsh-ver-newer:latest -f "$TMP/ver-label.Containerfile" "$TMP" >/dev/null || fail "could not build the newer-label image"
cat >"$TMP/ver-label2.Containerfile" <<'EOF'
FROM localhost/hsh-example:latest
LABEL org.ifup.hsh='{"shell":"/bin/bash"}'
EOF
podman build -q -t localhost/hsh-ver-missing:latest -f "$TMP/ver-label2.Containerfile" "$TMP" >/dev/null || fail "could not build the unversioned-label image"
ver_expect_failure "an image label from a newer hsh" 'org.ifup.hsh image label: config version 99 is newer than this hsh reads (version 1); update hsh' \
	"$HSH" run --pull=never --monitor-db "$DB" localhost/hsh-ver-newer:latest
ver_expect_failure "an image label without a version" 'no "version" field (this hsh reads version 1); rebuild the image' \
	"$HSH" run --pull=never --monitor-db "$DB" localhost/hsh-ver-missing:latest
pass "image labels without a version or from a newer hsh are refused with what to do"

for db in secrets monitor; do
	python3 - "$TMP/ver-$db.db" <<'PY'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
c.execute("CREATE TABLE IF NOT EXISTS t (x)")
c.execute("PRAGMA user_version = 99")
c.commit()
PY
done
"$HSH" secret ls --db "$TMP/ver-secrets.db" >"$TMP/ver.out" 2>&1 && fail "a secret store with a newer schema was opened"
grep -q "secrets.db has schema version 99, newer than this hsh reads (version 1); update hsh" "$TMP/ver.out" || fail "unexpected secret store error: $(cat "$TMP/ver.out")"
"$HSH" proxy denied --db "$TMP/ver-monitor.db" >"$TMP/ver.out" 2>&1 && fail "a monitor database with a newer schema was opened"
grep -q "monitor.db has schema version 99, newer than this hsh reads (version 1); update hsh" "$TMP/ver.out" || fail "unexpected monitor database error: $(cat "$TMP/ver.out")"
pass "secrets.db and monitor.db with a newer schema version are refused"

cat >"$TMP/ver-live.hsh" <<EOF
{
  "version": 1,
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash",
  "mounts": ["$VER_WS:/workspace"],
  "network": { "mode": "allowlist", "allow": ["host.containers.internal"] }
}
EOF
BEFORE_IDS="$(session_ids)"
(cd "$VER_WS" && "$HSH" run --config "$TMP/ver-live.hsh" --monitor-db "$DB") >"$TMP/ver-live.out" 2>&1 <<'CMDS' &
echo "before: $(curl -sS -o /dev/null --max-time 8 https://example.com 2>&1 | tail -1)"
while [ ! -e /workspace/edited ]; do sleep 1; done
sleep 4
echo "after: $(curl -sS -o /dev/null --max-time 8 https://example.com 2>&1 | tail -1)"
exit
CMDS
VER_PID=$!
VER_SESSION=""
for _ in $(seq 1 60); do
	VER_SESSION="$(for id in $(session_ids); do case " $BEFORE_IDS " in *" $id "*) ;; *) echo "$id" ;; esac; done | head -1)"
	if [ -n "$VER_SESSION" ] && grep -q '^before:' "$TMP/ver-live.out" 2>/dev/null; then break; fi
	sleep 1
done
[ -n "$VER_SESSION" ] || fail "the live session never started: $(cat "$TMP/ver-live.out")"
VER_CFG="$XDG_STATE_HOME/hsh/sessions/$VER_SESSION/config.json"
grep -q 'before:.*response 403' "$TMP/ver-live.out" || fail "example.com was not denied before the edit: $(cat "$TMP/ver-live.out")"
printf '%s\n' '{ "version": 99, "network": { "mode": "allowlist", "allow": ["example.com", "host.containers.internal"] } }' >"$VER_CFG"
cp "$VER_CFG" "$TMP/ver-cfg.saved"
"$HSH" proxy allow --db "$DB" --session "$VER_SESSION" https://other.example >"$TMP/ver.out" 2>&1 && fail "proxy allow edited a config from a newer hsh"
grep -q "config version 99 is newer than this hsh reads (version 1); update hsh" "$TMP/ver.out" || fail "proxy allow did not explain: $(cat "$TMP/ver.out")"
cmp -s "$VER_CFG" "$TMP/ver-cfg.saved" || fail "a refused proxy allow modified the config"
touch "$VER_WS/edited"
wait "$VER_PID" || true
grep -q 'after:.*response 403' "$TMP/ver-live.out" || fail "the running proxy stopped enforcing after an unreadable reload: $(cat "$TMP/ver-live.out")"
podman logs "hsh-$VER_SESSION-proxy" 2>&1 | grep -q "configuration reload failed.*newer than this hsh reads" || fail "the proxy did not log the failed reload: $(podman logs "hsh-$VER_SESSION-proxy" 2>&1 | tail -5)"
pass "a running proxy keeps its last readable policy when the session config becomes unreadable, and logs why"

"$HSH" run --monitor-db "$DB" --resume "$VER_SESSION" >"$TMP/ver.out" 2>&1 </dev/null && fail "resumed a session whose config is from a newer hsh"
grep -q "session $VER_SESSION: .*config version 99 is newer than this hsh reads (version 1); update hsh" "$TMP/ver.out" || fail "resume did not explain: $(cat "$TMP/ver.out")"
printf '%s\n' '{ "image": "localhost/hsh-example:latest" }' >"$VER_CFG"
"$HSH" run --monitor-db "$DB" --resume "$VER_SESSION" >"$TMP/ver.out" 2>&1 </dev/null && fail "resumed a session whose config has no version"
grep -q "remove the session with: hsh rm $VER_SESSION" "$TMP/ver.out" || fail "resume did not say how to remove the stale session: $(cat "$TMP/ver.out")"
"$HSH" rm "hsh-$VER_SESSION" >/dev/null 2>&1 || fail "hsh rm could not remove a session whose config is unreadable"
[ ! -e "$XDG_STATE_HOME/hsh/sessions/$VER_SESSION" ] || fail "hsh rm left the session state"
pass "resuming a session with an unreadable config says why and how to remove it, and hsh rm still removes it"

echo "== final sweep: every session the test created is removable"
rm_test_sessions
if [ -n "$(test_pods)" ]; then
	fail "sessions were left behind after the final sweep: $(test_pods)"
fi
if [ -n "$(ls -A "$XDG_STATE_HOME/hsh/sessions" 2>/dev/null)" ]; then
	for d in "$XDG_STATE_HOME"/hsh/sessions/*/; do
		echo "leftover: $d: $(ls -A "$d" | tr '\n' ' ') workspace=$(cat "$d/workspace" 2>/dev/null)" >&2
	done
	podman pod ls --format '{{.Name}} {{.Status}} {{.Labels}}' >&2
	fail "session state was left behind after the final sweep"
fi
pass "hsh rm removes every session the test created and its state"

echo
echo "ALL FUNCTIONAL TESTS PASSED"
PASSED=1
