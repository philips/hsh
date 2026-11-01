#!/bin/bash
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=${WORK:-$(mktemp -d /tmp/oauth-phantoms.XXXXXX)}
PORT=${PORT:-18080}
CODEX=${CODEX:-$WORK/bin/codex}
CLAUDE=${CLAUDE:-$(command -v claude || true)}

fake_start() {
	fake_stop
	rm -f "$WORK/$1.log"
	setsid python3 "$HERE/fake.py" "$PORT" "$WORK/$1.log" "$WORK/claims.json" >/dev/null 2>&1 </dev/null &
	echo $! >"$WORK/fake.pid"
	sleep 1
}

fake_stop() {
	[ -f "$WORK/fake.pid" ] && kill "$(cat "$WORK/fake.pid")" 2>/dev/null
	rm -f "$WORK/fake.pid"
	sleep 0.3
}

codex_env() {
	env HOME="$WORK/codexhome" CODEX_HOME="$WORK/codexhome/.codex" \
		CODEX_REFRESH_TOKEN_URL_OVERRIDE="http://127.0.0.1:$PORT/oauth/token" "$@"
}

fetch_codex() {
	[ -x "$CODEX" ] && return
	mkdir -p "$WORK/bin"
	curl -fsSL https://github.com/openai/codex/releases/latest/download/codex-x86_64-unknown-linux-musl.tar.gz |
		tar xz -C "$WORK/bin"
	mv "$WORK"/bin/codex-x86_64-unknown-linux-musl "$CODEX"
}

codex_scenario() {
	local name=$1 exp=$2
	rm -rf "$WORK/codexhome"
	python3 "$HERE/codex-setup.py" "$WORK/codexhome/.codex" "$exp" "$PORT" "$WORK/claims.json"
	local before
	before=$(md5sum <"$WORK/codexhome/.codex/auth.json")
	fake_start "$name"
	echo "== $name: codex login status"
	codex_env "$CODEX" login status 2>&1 | grep -v "PATH aliases"
	echo "== $name: codex exec"
	codex_env timeout 40 "$CODEX" exec --skip-git-repo-check "say hi" 2>&1 | grep -v rmcp | tail -3
	fake_stop
	[ "$before" = "$(md5sum <"$WORK/codexhome/.codex/auth.json")" ] && echo "auth.json unchanged" || echo "auth.json changed"
	python3 "$HERE/summarize.py" "$WORK/$name.log"
}

claude_scenario() {
	rm -rf "$WORK/claudehome"
	python3 "$HERE/claude-setup.py" "$WORK/claudehome" 3600
	fake_start claude
	local -a e=(env HOME="$WORK/claudehome" CLAUDE_CONFIG_DIR="$WORK/claudehome/.claude"
		ANTHROPIC_BASE_URL="http://127.0.0.1:$PORT" CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 DISABLE_TELEMETRY=1)
	echo "== claude auth status"
	"${e[@]}" timeout 30 "$CLAUDE" auth status
	echo "== claude -p"
	"${e[@]}" timeout 60 "$CLAUDE" -p "say hi" 2>&1 | tail -3
	fake_stop
	echo "== credentials after the run"
	python3 -c "import json,sys; d=json.load(open(sys.argv[1]))['claudeAiOauth']; print({k:(v[:24] if isinstance(v,str) else v) for k,v in d.items()})" \
		"$WORK/claudehome/.claude/.credentials.json"
	python3 "$HERE/summarize.py" "$WORK/claude.log"
}

echo "work dir: $WORK"
fetch_codex
FAKE_API_MODE=ok codex_scenario codex-ok 3600
FAKE_API_MODE=ok FAKE_TOKEN_MODE=invalid_grant codex_scenario codex-invalid-grant -600
FAKE_API_MODE=deny codex_scenario codex-401-storm 3600
if [ -n "$CLAUDE" ]; then
	FAKE_API_MODE=ok claude_scenario
else
	echo "claude not found; skipping the Claude Code scenario"
fi
