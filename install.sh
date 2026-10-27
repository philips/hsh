#!/bin/sh
# hsh installer — https://github.com/philips/hsh
#
#   curl -fsSL https://raw.githubusercontent.com/philips/hsh/main/install.sh | sh
#
# Downloads the latest hsh release for your platform from GitHub, verifies
# its SHA-256 checksum, installs the binary to ~/.local/bin, and makes sure
# ~/.local/bin is on your PATH.
#
# Environment overrides:
#   HSH_VERSION       install a specific tag (e.g. v1.2.3) instead of latest
#   HSH_INSTALL_DIR   install location (default: ~/.local/bin)
#   HSH_RELEASE_BASE  release base URL (default: GitHub releases; for testing)

set -eu

REPO="philips/hsh"
BIN="hsh"
BASE="${HSH_RELEASE_BASE:-https://github.com/$REPO/releases}"

err()  { printf 'error: %s\n' "$1" >&2; exit 1; }
info() { printf '%s\n' "$1"; }
need() { command -v "$1" >/dev/null 2>&1 || err "required command not found: $1"; }

need curl
need uname

# --- detect platform ---------------------------------------------------------
# hsh sandboxes Linux containers (rootless podman, network namespaces), so
# the binary itself is Linux-only.
os="$(uname -s)"
arch="$(uname -m)"

[ "$os" = "Linux" ] || err "unsupported OS: $os (hsh runs Linux sandboxes; on macOS or Windows, use a Linux VM or WSL2)"

case "$arch" in
  x86_64|amd64)  target_arch="amd64" ;;
  aarch64|arm64) target_arch="arm64" ;;
  *) err "unsupported architecture: $arch (hsh releases cover linux/amd64 and linux/arm64)" ;;
esac

# --- resolve version ---------------------------------------------------------
version="${HSH_VERSION:-}"
if [ -z "$version" ]; then
  # Follow the 'latest' redirect to discover the newest tag (no API rate limit).
  version="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$BASE/latest" \
    | sed -n 's#.*/tag/\(v[^/]*\)$#\1#p')"
  [ -n "$version" ] || err "could not determine the latest version"
fi

asset="${BIN}-linux-${target_arch}"

info "Installing hsh $version ($asset)..."

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

# --- download ----------------------------------------------------------------
curl -fsSL "$BASE/download/$version/$asset" -o "$tmp/$asset" || err "failed to download $asset"
curl -fsSL "$BASE/download/$version/SHA256SUMS" -o "$tmp/SHA256SUMS" || err "failed to download checksums"

# --- verify checksum ---------------------------------------------------------
expected="$(awk -v a="$asset" '$2==a {print $1}' "$tmp/SHA256SUMS")"
[ -n "$expected" ] || err "no checksum found for $asset"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
else
  err "need sha256sum or shasum to verify the download"
fi
[ "$expected" = "$actual" ] || err "checksum mismatch for $asset"

chmod +x "$tmp/$asset"

# --- install -----------------------------------------------------------------
install_dir="${HSH_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$install_dir"

if mv "$tmp/$asset" "$install_dir/$BIN" 2>/dev/null; then
  :
elif command -v sudo >/dev/null 2>&1; then
  info "Writing to $install_dir requires elevated permissions..."
  sudo mv "$tmp/$asset" "$install_dir/$BIN"
else
  err "could not write to $install_dir (set HSH_INSTALL_DIR to a writable path)"
fi

info ""
info "✓ hsh $version installed to $install_dir/$BIN"

# --- make sure the install dir is on PATH ------------------------------------
case ":$PATH:" in
  *":$install_dir:"*)
    ;;
  *)
    line="export PATH=\"$install_dir:\$PATH\""
    marker="# added by the hsh installer"
    rc="$HOME/.profile"
    case "${SHELL:-}" in
      *zsh)  rc="$HOME/.zshrc" ;;
      *bash) rc="$HOME/.bashrc" ;;
    esac
    if [ -f "$rc" ] && grep -qF "$install_dir" "$rc" 2>/dev/null; then
      info "  $install_dir is already referenced in $rc"
    else
      printf '\n%s\n%s\n' "$marker" "$line" >> "$rc"
      info "  added $install_dir to PATH in $rc"
    fi
    info "  restart your shell (or run: . \"$rc\") to pick it up"
    ;;
esac

# --- print README section ----------------------------------------------------
README_URL="${HSH_README_URL:-https://raw.githubusercontent.com/philips/hsh/refs/heads/main/README.md}"
if readme="$(curl -fsSL "$README_URL" 2>/dev/null)" && [ -n "$readme" ]; then
  section="$(printf '%s\n' "$readme" | sed -n '/<!--- install.sh START -->/,/<!--- install.sh END -->/p' \
    | sed '1d;$d' | sed -e '/./,$!d' -e ':a' -e '/^[[:space:]]*$/{$d;N;ba' -e '}')"
  if [ -n "$section" ]; then
    info ""
    printf '%s\n' "$section"
  fi
fi
