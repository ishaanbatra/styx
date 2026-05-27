#!/usr/bin/env bash
# Build and install styx to ~/bin/styx, backing up any prior install.
set -euo pipefail

cd "$(dirname "$0")"

BIN_DIR="$HOME/bin"
BIN="$BIN_DIR/styx"

go build -o ./bin/styx ./cmd/styx

mkdir -p "$BIN_DIR"
if [ -e "$BIN" ] && [ ! -L "$BIN" ]; then
  mv "$BIN" "$BIN.old.bak"
  echo "Backed up existing styx -> $BIN.old.bak"
fi
cp ./bin/styx "$BIN"
chmod 755 "$BIN"
echo "Installed -> $BIN"

if ! command -v agy >/dev/null 2>&1; then
  echo
  echo "NOTE: 'agy' (Antigravity CLI) is not installed."
  echo "      styx v0.2 uses agy for Gemini access (replacing gemini-cli)."
  echo "      Install with: curl -fsSL https://antigravity.google/cli/install.sh | bash"
  echo "      Without agy, agy-routed verbs will fall back to ollama via the routing table."
fi

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "NOTE: $BIN_DIR is not in PATH. Add: export PATH=\"\$HOME/bin:\$PATH\"";;
esac
