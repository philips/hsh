# Use the official libghostty-vt release

> **Status: implemented.**

## Goal

TODO.md asked to stop building the terminal emulator's WebAssembly module
from a Ghostty tip revision vendored inside a third-party Go module, and
instead consume the official `libghostty-vt` releases that Ghostty
publishes, with checksums taken from the release's own `SHA256SUMS`.

## Background

`internal/vt` drives libghostty-vt through the wasm2go route chosen in
plans/0025: a `.wasm` module converted to generated Go, compiled with
`GOEXPERIMENT=simd`, no runtime dependency. Until now the module came
from `code.selman.me/hauntty`, a third-party module that committed a
wasm built from Ghostty tip revision `7f62fe70` — a moving, unofficial
snapshot reachable only through that module, with provenance recorded in
a `provenance_test.go` that pinned the revision and the blob's sha256.

Ghostty now publishes official `libghostty-vt` releases. Each version
`X.Y.Z` lives in one directory:

    https://release.files.ghostty.org/libghostty-vt/X.Y.Z/

with `ghostty-vt-X.Y.Z.wasm` (speed), `ghostty-vt-X.Y.Z-small.wasm`
(size), and a `SHA256SUMS` covering every file. hsh uses the small
build, as before.

## Design

### Vendor the Go wrapper, generate the wasm binding ourselves

The hauntty module had two parts: a hand-written Go wrapper around the
libghostty-vt C API (~1.7k lines, MIT), and the wasm binding
(`internal/wasmvt`: a committed `.wasm` plus a ~100k-line generated
file). Both move into the hsh repo:

- `internal/vt/libghostty/` — the wrapper, copied from hauntty
  (MIT-licensed, comments and license header kept) with two changes:
  the import path for the generated package, and the key-encoder calls
  renamed to the 1.0.0 API (`ghostty_key_encoder_setopt` →
  `ghostty_key_encoder_set`; the official release renamed them).
- `internal/vt/libghostty/wasmvt/` — `vt.generated.go` generated from
  the official `ghostty-vt-1.0.0-small.wasm`, the `.wasm` blob itself
  (committed so the binding can be regenerated and verified offline),
  and a `PROVENANCE` file recording the release version, the sha256 of
  the blob, and the `SHA256SUMS` line it was verified against.

`internal/vt/ghostty.go` imports the vendored package and the hauntty
dependency is dropped from go.mod.

### The generator and its patch

The binding is generated with the same wasm2go fork hauntty used
(`github.com/seruman/wasm2go`, reached by replacing
`github.com/ncruces/wasm2go`), because it is the only wasm2go that
emits `GOEXPERIMENT=simd` code. It was written to cover exactly the
SIMD opcodes the tip wasm used; the official 1.0.0 module uses two
more: `i16x8.add_sat_u` (0xFD 0x90) and `i16x8.max_u` (0xFD 0x99).
Both map directly onto stdlib `simd/archsimd` methods
(`Uint16x8.AddSaturated`, `Uint16x8.Max`), so the fork needs a
two-function, two-case patch:

- `internal-docs/libghostty-wasm2go.patch` adds the two opcodes to
  `code_simd.go` and the two helpers to `helpers/simd.go` (the
  generator embeds that file's source into its output, so the helpers
  end up inlined in `vt.generated.go`).

The patch is committed rather than a fork of wasm2go: it is reviewable,
and the bump script applies it to the pinned fork version fetched from
the Go module proxy. Future releases may use further SIMD opcodes; the
bump fails loudly (`unsupported opcode (SIMD): 0xFD 0xNN`) and the
patch grows the same way (opcode case + helper).

### The bump script

`tools/bump-libghostty.sh VERSION`:

1. downloads `ghostty-vt-VERSION-small.wasm` and `SHA256SUMS` from the
   release directory,
2. verifies the blob against the `SHA256SUMS` line,
3. fetches the pinned wasm2go fork from the module proxy, applies the
   committed patch, and regenerates
   `internal/vt/libghostty/wasmvt/vt.generated.go`,
4. rewrites `PROVENANCE`,
5. builds and tests `internal/vt` with `GOEXPERIMENT=simd` (API drift
   between libghostty-vt versions surfaces here), and reminds about the
   wrapper if exports changed.

### Provenance test

`provenance_test.go` now checks the committed artifacts instead of the
module cache: the sha256 of the committed `.wasm` must equal the pin in
`PROVENANCE`, and the pin must carry the official `SHA256SUMS` line
format. The version and hash are the official release's, not a tip
revision.

## Consequences

- The wasm blob hsh ships is byte-identical to the official Ghostty
  release, verified against Ghostty's own checksums; the only third
  party left in the chain is the wasm2go fork, which is a build-time
  code generator whose output is committed and diffable.
- Bumps are mechanical: run the script with the new version, review the
  generated diff, commit.
- `GOEXPERIMENT=simd` remains required (unchanged).
- The vendored wrapper keeps its upstream comments; AGENTS.md's
  no-comments rule applies to hsh's own code, not vendored files.
