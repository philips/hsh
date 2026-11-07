#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat <<'USAGE'
usage: ./tools/bump-libghostty.sh VERSION

Rebuilds internal/vt/libghostty/wasmvt from an official libghostty-vt
release (https://release.files.ghostty.org/libghostty-vt/VERSION/):
downloads ghostty-vt-VERSION-small.wasm, verifies it against the
release's SHA256SUMS, regenerates vt.generated.go with the pinned
wasm2go build (patched with internal-docs/libghostty-wasm2go.patch),
rewrites PROVENANCE, and runs the vt tests.

If the wasm uses SIMD opcodes the generator does not know, generation
fails with "unsupported opcode (SIMD): 0xFD 0xNN"; extend the patch
(opcode case in code_simd.go plus a helper in helpers/simd.go) and
retry. If the libghostty-vt C API changed, the wrapper in
internal/vt/libghostty needs the same change; see internal-docs/libghostty.md.
USAGE
}

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

if [ $# -ne 1 ] || [ "$1" = "-h" ] || [ "$1" = "--help" ]; then
	usage >&2
	exit 2
fi

version="$1"
base="https://release.files.ghostty.org/libghostty-vt/$version"
wasm_name="ghostty-vt-$version-small.wasm"
wasm_dir="internal/vt/libghostty/wasmvt"

wasm2go_version="v0.0.0-20260820204434-52e0ccde57f4"
wasm2go_module="github.com/seruman/wasm2go"
wasm2go_flags=(-unsafe -nanbox -pkg wasmvt)

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "downloading $base/$wasm_name"
curl -fsSL --retry 3 -o "$tmp/$wasm_name" "$base/$wasm_name"
curl -fsSL --retry 3 -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"

expected="$(grep -F " $wasm_name" "$tmp/SHA256SUMS" | head -1 | awk '{print $1}')"
if [ -z "$expected" ]; then
	echo "SHA256SUMS has no entry for $wasm_name" >&2
	exit 1
fi

actual="$(sha256sum "$tmp/$wasm_name" | awk '{print $1}')"
if [ "$actual" != "$expected" ]; then
	echo "sha256 mismatch for $wasm_name: $actual != $expected" >&2
	exit 1
fi
echo "checksum ok: $expected"

echo "fetching wasm2go $wasm2go_module@$wasm2go_version"
mkdir "$tmp/gen"
cat >"$tmp/gen/go.mod" <<EOF
module scratch

go 1.27.3

require github.com/ncruces/wasm2go v0.3.1

replace github.com/ncruces/wasm2go => github.com/seruman/wasm2go $wasm2go_version
EOF
(cd "$tmp/gen" && go mod download github.com/ncruces/wasm2go)

fork_src="$(go env GOMODCACHE)/$wasm2go_module@$wasm2go_version"
cp -r "$fork_src" "$tmp/wasm2go"
chmod -R u+w "$tmp/wasm2go"
git -C "$tmp/wasm2go" apply "$root/internal-docs/libghostty-wasm2go.patch"
(cd "$tmp/wasm2go" && go build -o "$tmp/wasm2go-bin" .)

echo "generating $wasm_dir/vt.generated.go"
"$tmp/wasm2go-bin" "${wasm2go_flags[@]}" -o "$wasm_dir/vt.generated.go" "$tmp/$wasm_name"

cp "$tmp/$wasm_name" "$wasm_dir/ghostty-vt-small.wasm"

cat >"$wasm_dir/PROVENANCE" <<EOF
libghostty-vt $version

source $base/$wasm_name
sha256 $expected
sha256sums $expected  $wasm_name
EOF

echo "testing"
GOEXPERIMENT=simd go test ./internal/vt/... ./internal/relay/...

echo
echo "done: libghostty-vt $version"
echo "review the diff, including internal/vt/libghostty if the C API changed"
