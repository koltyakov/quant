#!/bin/sh
set -eu

install_dir="$HOME/.local/bin"
binary="quant"

usage() {
  cat <<'EOF'
Uninstall quant installed by scripts/install.sh.

Removes $HOME/.local/bin/quant. User data, MCP client config, and Ollama are left untouched.

Examples:
  curl -fsSL https://raw.githubusercontent.com/koltyakov/quant/main/scripts/uninstall.sh | sh
EOF
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

target="$install_dir/$binary"

if [ ! -e "$target" ]; then
  echo "$binary is not installed at $target"
  exit 0
fi

rm -f "$target"
echo "Removed $target"

case ":$PATH:" in
  *":$install_dir:"*)
    echo "Note: $install_dir remains on PATH; remove it from your shell profile if it is no longer needed."
    ;;
esac

echo "User data, MCP client config, and Ollama were not removed."
