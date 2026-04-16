#!/bin/sh
set -eu

repo="koltyakov/quant"
install_dir="$HOME/.local/bin"
binary="quant"

usage() {
  cat <<'EOF'
Install quant from GitHub Releases.

Installs the latest release to $HOME/.local/bin.

Examples:
  curl -fsSL https://raw.githubusercontent.com/koltyakov/quant/main/scripts/install.sh | sh
EOF
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "error: missing required command: $1" >&2
    exit 1
  fi
}

asset_os() {
  case "$(uname -s)" in
    Darwin) printf '%s' Darwin ;;
    Linux) printf '%s' Linux ;;
    *)
      echo "error: unsupported OS: $(uname -s)" >&2
      exit 1
      ;;
  esac
}

asset_arch() {
  case "$(uname -m)" in
    x86_64 | amd64) printf '%s' x86_64 ;;
    arm64 | aarch64) printf '%s' arm64 ;;
    *)
      echo "error: unsupported architecture: $(uname -m)" >&2
      exit 1
      ;;
  esac
}

download() {
  url="$1"
  out="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$out"
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -qO "$out" "$url"
    return
  fi
  echo "error: missing required command: curl or wget" >&2
  exit 1
}

ask_install_ollama() {
  if [ ! -t 1 ]; then
    return 1
  fi
  if ! { : </dev/tty >/dev/tty; } 2>/dev/null; then
    return 1
  fi

  printf "Install Ollama now? [y/N] " >/dev/tty 2>/dev/null || return 1
  answer=""
  IFS= read -r answer </dev/tty 2>/dev/null || answer=""
  case "$answer" in
    y | Y | yes | YES) return 0 ;;
    *) return 1 ;;
  esac
}

maybe_install_ollama() {
  if command -v ollama >/dev/null 2>&1; then
    echo "Ollama already installed: $(command -v ollama)"
    return
  fi

  echo "Ollama was not found on PATH."
  echo "quant uses Ollama by default for local embeddings."
  if ! ask_install_ollama; then
    echo "Skipping Ollama install."
    echo "Install Ollama with:"
    echo "  curl -fsSL https://ollama.com/install.sh | sh"
    echo "Or download it from: https://ollama.com/download"
    return
  fi

  ollama_installer="$tmp_dir/ollama-install.sh"
  echo "Installing Ollama from https://ollama.com/install.sh..."
  download "https://ollama.com/install.sh" "$ollama_installer"
  sh "$ollama_installer"

  if command -v ollama >/dev/null 2>&1; then
    echo "Installed Ollama: $(command -v ollama)"
    echo "quant will start Ollama and pull the embedding model on first use if needed."
  else
    echo "Ollama installer finished, but ollama is still not on PATH."
    echo "Open a new shell or follow the installer output before running quant."
  fi
}

need_cmd tar
need_cmd mktemp
need_cmd install

os="$(asset_os)"
arch="$(asset_arch)"
asset="${binary}_${os}_${arch}.tar.gz"
url="https://github.com/${repo}/releases/latest/download/${asset}"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT INT TERM

archive="$tmp_dir/$asset"
echo "Downloading $asset from $repo..."
download "$url" "$archive"

tar -xzf "$archive" -C "$tmp_dir"
if [ ! -f "$tmp_dir/$binary" ]; then
  echo "error: $binary not found in $asset" >&2
  exit 1
fi

mkdir -p "$install_dir"
install -m 0755 "$tmp_dir/$binary" "$install_dir/$binary"

echo "Installed $binary to $install_dir/$binary"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *)
    echo "Note: $install_dir is not on PATH."
    echo "Add it to your shell profile, or run: $install_dir/$binary"
    ;;
esac

maybe_install_ollama
