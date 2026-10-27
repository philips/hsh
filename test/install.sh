#!/bin/bash
# Tests install.sh against a local mock GitHub release server (no network).
set -u

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
SRV=""
PASSED=0

fail() { echo "FAIL: $1" >&2; exit 1; }
pass() { echo "PASS: $1"; }
cleanup() {
	if [ -n "$SRV" ]; then
		kill "$SRV" 2>/dev/null || true
	fi
	rm -rf "$TMP"
}
trap cleanup EXIT

command -v python3 >/dev/null 2>&1 || fail "python3 is required"

echo "== building the binary that the mock release serves"
(cd "$REPO" && CGO_ENABLED=0 go build -o "$TMP/hsh-linux-amd64" .) || fail "go build failed"

echo "== laying out the mock release"
# URL layout mirrors GitHub: <base>/download/<tag>/<asset> and <base>/latest
mkdir -p "$TMP/www/releases/download/v9.9.9" "$TMP/www/releases/download/v8.8.8"
cp "$TMP/hsh-linux-amd64" "$TMP/www/releases/download/v9.9.9/hsh-linux-amd64"
printf '#!/bin/sh\necho v8.8.8-marker\n' > "$TMP/www/releases/download/v8.8.8/hsh-linux-amd64"
chmod +x "$TMP/www/releases/download/v8.8.8/hsh-linux-amd64"
(cd "$TMP/www/releases/download/v9.9.9" && sha256sum hsh-linux-amd64 > SHA256SUMS)
(cd "$TMP/www/releases/download/v8.8.8" && sha256sum hsh-linux-amd64 > SHA256SUMS)

echo "== starting the mock release server"
python3 - "$TMP/www" "$TMP/port" <<'PY' &
import http.server
import socketserver
import sys

root, port_file = sys.argv[1], sys.argv[2]


class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=root, **kwargs)

    def _latest(self):
        self.send_response(302)
        self.send_header("Location", "/releases/tag/v9.9.9")
        self.end_headers()

    def _tag_page(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/html")
        self.end_headers()
        self.wfile.write(b"<html>release v9.9.9</html>")

    def _route(self):
        if self.path == "/releases/latest":
            self._latest()
            return True
        if self.path.startswith("/releases/tag/"):
            self._tag_page()
            return True
        return False

    def do_HEAD(self):
        if not self._route():
            super().do_HEAD()

    def do_GET(self):
        if not self._route():
            super().do_GET()


# Port 0: let the OS pick a free port, then report it.
socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", 0), Handler) as srv:
    with open(port_file, "w") as f:
        f.write(str(srv.server_address[1]))
    srv.serve_forever()
PY
SRV=$!
for _ in $(seq 1 50); do
	[ -s "$TMP/port" ] && break
	sleep 0.1
done
[ -s "$TMP/port" ] || fail "mock server did not start"
PORT="$(cat "$TMP/port")"
curl -s -o /dev/null "http://127.0.0.1:$PORT/releases/latest" || fail "mock server did not start"

echo "== install from the mock release (latest)"
export HOME="$TMP/home"
export SHELL=/bin/bash
export HSH_RELEASE_BASE="http://127.0.0.1:$PORT/releases"
unset HSH_VERSION HSH_INSTALL_DIR
mkdir -p "$HOME"
echo "# my bashrc" > "$HOME/.bashrc"

sh "$REPO/install.sh" >"$TMP/install1.out" 2>&1 || { cat "$TMP/install1.out"; fail "install.sh failed"; }
grep -q "installed to $HOME/.local/bin/hsh" "$TMP/install1.out" || { cat "$TMP/install1.out"; fail "install message missing"; }
[ -x "$HOME/.local/bin/hsh" ] || fail "binary not installed"
"$HOME/.local/bin/hsh" help >/dev/null 2>&1 || fail "installed binary does not run"
pass "downloads, verifies, and installs to ~/.local/bin"

echo "== PATH handling"
grep -qF "export PATH=\"$HOME/.local/bin:\$PATH\"" "$HOME/.bashrc" || { cat "$HOME/.bashrc"; fail "PATH export not added to .bashrc"; }
pass "adds ~/.local/bin to PATH in the shell rc file"

sh "$REPO/install.sh" >"$TMP/install2.out" 2>&1 || { cat "$TMP/install2.out"; fail "second run failed"; }
[ "$(grep -c 'added by the hsh installer' "$HOME/.bashrc")" = "1" ] || fail "PATH block added twice"
pass "PATH setup is idempotent"

echo "== checksum mismatch is rejected"
sed -i 's/^[0-9a-f][0-9a-f]*/deadbeef/' "$TMP/www/releases/download/v9.9.9/SHA256SUMS"
if sh "$REPO/install.sh" >"$TMP/bad.out" 2>&1; then
	fail "checksum mismatch was not rejected"
fi
grep -q "checksum mismatch" "$TMP/bad.out" || { cat "$TMP/bad.out"; fail "wrong error for a checksum mismatch"; }
pass "rejects a checksum mismatch"
(cd "$TMP/www/releases/download/v9.9.9" && sha256sum hsh-linux-amd64 > SHA256SUMS)

echo "== explicit version override"
HSH_VERSION=v8.8.8 sh "$REPO/install.sh" >"$TMP/install3.out" 2>&1 || { cat "$TMP/install3.out"; fail "install with HSH_VERSION failed"; }
[ "$("$HOME/.local/bin/hsh")" = "v8.8.8-marker" ] || fail "HSH_VERSION did not select the v8.8.8 asset"
pass "HSH_VERSION installs a specific release"

echo "== already on PATH: no rc edit"
rm -f "$HOME/.bashrc"
export PATH="$HOME/.local/bin:$PATH"
sh "$REPO/install.sh" >"$TMP/install4.out" 2>&1 || { cat "$TMP/install4.out"; fail "install with the dir on PATH failed"; }
[ ! -f "$HOME/.bashrc" ] || fail "rc file was edited even though the dir is on PATH"
pass "leaves the rc file alone when the dir is already on PATH"

echo "== README section is printed"
cat > "$TMP/www/README.md" <<'EOF'
# hsh
intro

<!--- install.sh START -->

Quick start:

```
hsh run pi
```

<!--- install.sh END -->

trailing text
EOF
cp "$REPO/README.md" "$TMP/readme-repo-copy"
HSH_README_URL="http://127.0.0.1:$PORT/README.md" sh "$REPO/install.sh" >"$TMP/install5.out" 2>&1 \
	|| { cat "$TMP/install5.out"; fail "install with HSH_README_URL failed"; }
grep -qF "Quick start:" "$TMP/install5.out" || { cat "$TMP/install5.out"; fail "README section missing from output"; }
grep -qF "hsh run pi" "$TMP/install5.out" || { cat "$TMP/install5.out"; fail "README code block missing from output"; }
! grep -qF "intro" "$TMP/install5.out" || { cat "$TMP/install5.out"; fail "text before the START marker was printed"; }
! grep -qF "trailing text" "$TMP/install5.out" || { cat "$TMP/install5.out"; fail "text after the END marker was printed"; }
! grep -qF "install.sh START" "$TMP/install5.out" || { cat "$TMP/install5.out"; fail "marker lines were printed"; }
grep -qF "Get started:" "$TMP/install5.out" || { cat "$TMP/install5.out"; fail "post-README output missing"; }
pass "prints the README section between the markers"

sin1=$(grep -nF "<!--- install.sh START -->" "$REPO/README.md" | head -1 | cut -d: -f1)
sin2=$(grep -nF "<!--- install.sh END -->" "$REPO/README.md" | head -1 | cut -d: -f1)
[ -n "$sin1" ] && [ -n "$sin2" ] && [ "$sin1" -lt "$sin2" ] \
	|| fail "README.md must contain START and END markers in order"
pass "README.md contains well-formed markers"

echo "== README fetch failure is non-fatal"
mv "$TMP/www/README.md" "$TMP/www/README.bak"
cat > "$TMP/www/README.md" <<'EOF'
no markers here
EOF
HSH_README_URL="http://127.0.0.1:$PORT/README.md" sh "$REPO/install.sh" >"$TMP/install6.out" 2>&1 \
	|| { cat "$TMP/install6.out"; fail "install failed when README had no markers"; }
! grep -qF "no markers here" "$TMP/install6.out" || { cat "$TMP/install6.out"; fail "printed README content despite missing markers"; }
grep -qF "Get started:" "$TMP/install6.out" || { cat "$TMP/install6.out"; fail "missing tail output with no markers"; }
rm "$TMP/www/README.md"
HSH_README_URL="http://127.0.0.1:$PORT/README.md" sh "$REPO/install.sh" >"$TMP/install7.out" 2>&1 \
	|| { cat "$TMP/install7.out"; fail "install failed when README fetch 404'd"; }
grep -qF "Get started:" "$TMP/install7.out" || { cat "$TMP/install7.out"; fail "missing tail output on 404"; }
pass "a missing or marker-less README does not break the install"

echo
echo "ALL INSTALLER TESTS PASSED"
PASSED=1
