#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NIX_PROFILE="/nix/var/nix/profiles/default"

# Load nix environment for this shell if present
if [ -f "${NIX_PROFILE}/etc/profile.d/nix-daemon.sh" ]; then
  # shellcheck disable=SC1091
  . "${NIX_PROFILE}/etc/profile.d/nix-daemon.sh"
fi
if [ -x "${NIX_PROFILE}/bin/nix" ]; then
  export PATH="${NIX_PROFILE}/bin:${PATH}"
fi

if ! command -v nix >/dev/null 2>&1; then
  if [[ "$(uname -s)" == "Darwin" ]]; then
    echo "[bootstrap] Nix not found on macOS."
    echo "[bootstrap] Please install Determinate Nix first: https://dtr.mn/determinate-nix"
    echo "[bootstrap] After installation, re-run: ./scripts/bootstrap.sh"
    exit 1
  fi

  echo "[bootstrap] Nix not found. Installing..."
  echo "[bootstrap] This will prompt for sudo to create /nix."
  curl -L https://install.determinate.systems/nix | sh -s -- install
  if [ -f "${NIX_PROFILE}/etc/profile.d/nix-daemon.sh" ]; then
    # shellcheck disable=SC1091
    . "${NIX_PROFILE}/etc/profile.d/nix-daemon.sh"
  fi
  if [ -x "${NIX_PROFILE}/bin/nix" ]; then
    export PATH="${NIX_PROFILE}/bin:${PATH}"
  fi
fi

cd "$ROOT_DIR"
make test-nix
