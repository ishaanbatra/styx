#!/usr/bin/env bash
# Install the latest styx release, or build a checkout with --from-source.
# STYX_DOWNLOAD_BASE is intentionally undocumented; it exists for installer tests.

# The documented curl | sh invocation may run under a non-Bash /bin/sh.
if [ -n "${BASH_VERSION:-}" ]; then
  set -euo pipefail
else
  set -eu
fi

DOWNLOAD_BASE="${STYX_DOWNLOAD_BASE:-https://github.com/ishaanbatra/styx}"
DOWNLOAD_BASE="${DOWNLOAD_BASE%/}"
INSTALL_DIR="${STYX_INSTALL_DIR:-$HOME/bin}"
if [ "$INSTALL_DIR" != "/" ]; then
  INSTALL_DIR="${INSTALL_DIR%/}"
fi
TARGET_BIN="$INSTALL_DIR/styx"
TMP_DIR=""
INSTALL_TMP=""
FROM_SOURCE=0

usage() {
  echo "Usage: install.sh [--from-source]" >&2
}

die() {
  echo "Error: $*" >&2
  exit 1
}

cleanup() {
  if [ -n "${INSTALL_TMP:-}" ]; then
    rm -f "$INSTALL_TMP"
  fi
  if [ -n "${TMP_DIR:-}" ]; then
    rm -rf "$TMP_DIR"
  fi
}

trap cleanup EXIT
trap 'exit 1' HUP INT TERM

while [ "$#" -gt 0 ]; do
  case "$1" in
    --from-source) FROM_SOURCE=1 ;;
    -h|--help) usage; exit 0 ;;
    *) usage; die "Unknown argument '$1'" ;;
  esac
  shift
done

command -v mktemp >/dev/null 2>&1 || die "mktemp is required."
TMP_DIR=$(mktemp -d)

install_binary() {
  source_bin=$1

  [ -f "$source_bin" ] || die "Built or extracted styx binary was not found."
  mkdir -p "$INSTALL_DIR"

  # The temporary file lives beside the target so the final rename is atomic.
  INSTALL_TMP=$(mktemp "$INSTALL_DIR/.styx.tmp.XXXXXX")
  cp "$source_bin" "$INSTALL_TMP"
  chmod 755 "$INSTALL_TMP"

  if [ -e "$TARGET_BIN" ] && [ ! -L "$TARGET_BIN" ]; then
    cp "$TARGET_BIN" "$TARGET_BIN.old.bak"
    echo "Backed up existing styx -> $TARGET_BIN.old.bak"
  fi

  mv -f "$INSTALL_TMP" "$TARGET_BIN"
  INSTALL_TMP=""
  echo "Installed -> $TARGET_BIN"
}

finish_install() {
  if [ "$INSTALL_DIR" != "$HOME/.local/bin" ] && [ -f "$HOME/.local/bin/styx" ]; then
    echo "WARNING: Another styx binary exists at $HOME/.local/bin/styx; check your PATH order."
  fi

  if ! command -v agy >/dev/null 2>&1; then
    echo
    echo "NOTE: 'agy' (Antigravity CLI) is not installed."
    echo "      styx uses agy for Gemini access (replacing gemini-cli)."
    echo "      Install with: curl -fsSL https://antigravity.google/cli/install.sh | bash"
    echo "      Without agy, agy-routed verbs will fall back to ollama via the routing table."
  fi

  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) echo "NOTE: $INSTALL_DIR is not in PATH. Add: export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
  esac
}

if [ "$FROM_SOURCE" -eq 1 ]; then
  command -v go >/dev/null 2>&1 || die "Go is required for --from-source."
  SCRIPT_PATH="${BASH_SOURCE:-$0}"
  SCRIPT_DIR=$(CDPATH= cd "$(dirname "$SCRIPT_PATH")" && pwd)
  [ -f "$SCRIPT_DIR/go.mod" ] && [ -d "$SCRIPT_DIR/cmd/styx" ] || \
    die "--from-source must be run from a styx checkout (for example, ./install.sh --from-source)."

  echo "Building styx from source..."
  (cd "$SCRIPT_DIR" && go build -o "$TMP_DIR/styx" ./cmd/styx)
  install_binary "$TMP_DIR/styx"
  finish_install
  exit 0
fi

command -v curl >/dev/null 2>&1 || die "curl is required."
command -v tar >/dev/null 2>&1 || die "tar is required."

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  darwin|linux) ;;
  *) die "Unsupported OS '$OS'." ;;
esac

MACHINE_ARCH=$(uname -m)
case "$MACHINE_ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) die "Unsupported architecture '$MACHINE_ARCH'." ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
  HASH_TOOL="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  HASH_TOOL="shasum"
elif command -v openssl >/dev/null 2>&1; then
  HASH_TOOL="openssl"
else
  die "No SHA-256 utility found (need sha256sum, shasum, or openssl); installation aborted."
fi

echo "Resolving latest version..."
VERSION=""
if LATEST_JSON=$(curl -fsSL "$DOWNLOAD_BASE/releases/latest/download/latest.json"); then
  VERSION=$(printf '%s\n' "$LATEST_JSON" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
fi

if [ -z "$VERSION" ]; then
  LATEST_URL=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$DOWNLOAD_BASE/releases/latest") || \
    die "Could not resolve the latest release version."
  VERSION=$(printf '%s\n' "$LATEST_URL" | sed -n 's#^.*/tag/\([^/?#]*\).*$#\1#p')
fi

if [ -z "$VERSION" ] || ! printf '%s\n' "$VERSION" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z][0-9A-Za-z.-]*)?$'; then
  die "Could not resolve a valid latest release version."
fi

ARTIFACT_VERSION=${VERSION#v}
ARCHIVE_NAME="styx_${ARTIFACT_VERSION}_${OS}_${ARCH}.tar.gz"
RELEASE_BASE="$DOWNLOAD_BASE/releases/download/$VERSION"

echo "Installing styx $VERSION ($OS/$ARCH)..."
curl -fsSL -o "$TMP_DIR/$ARCHIVE_NAME" "$RELEASE_BASE/$ARCHIVE_NAME"
curl -fsSL -o "$TMP_DIR/checksums.txt" "$RELEASE_BASE/checksums.txt"

if ! EXPECTED_SHA=$(awk -v name="$ARCHIVE_NAME" '
  $2 == name { hash = $1; matches++ }
  END { if (matches == 1) print hash; else exit 1 }
' "$TMP_DIR/checksums.txt"); then
  die "Expected exactly one checksum entry for $ARCHIVE_NAME."
fi

if ! printf '%s\n' "$EXPECTED_SHA" | grep -Eq '^[0-9A-Fa-f]{64}$'; then
  die "Invalid checksum entry for $ARCHIVE_NAME."
fi

case "$HASH_TOOL" in
  sha256sum) ACTUAL_SHA=$(sha256sum "$TMP_DIR/$ARCHIVE_NAME" | awk '{print $1}') ;;
  shasum) ACTUAL_SHA=$(shasum -a 256 "$TMP_DIR/$ARCHIVE_NAME" | awk '{print $1}') ;;
  openssl) ACTUAL_SHA=$(openssl dgst -sha256 "$TMP_DIR/$ARCHIVE_NAME" | awk '{print $NF}') ;;
esac

EXPECTED_SHA=$(printf '%s' "$EXPECTED_SHA" | tr '[:upper:]' '[:lower:]')
ACTUAL_SHA=$(printf '%s' "$ACTUAL_SHA" | tr '[:upper:]' '[:lower:]')
if [ "$EXPECTED_SHA" != "$ACTUAL_SHA" ]; then
  die "Checksum mismatch for $ARCHIVE_NAME (expected $EXPECTED_SHA, got $ACTUAL_SHA)."
fi
echo "Checksum verified."

tar -xzf "$TMP_DIR/$ARCHIVE_NAME" -C "$TMP_DIR"
install_binary "$TMP_DIR/styx"
finish_install
