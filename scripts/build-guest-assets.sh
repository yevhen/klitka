#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUT_DIR="${REPO_ROOT}/guest/image/out"
KERNEL="${OUT_DIR}/vmlinuz"
INITRD="${OUT_DIR}/initramfs.cpio.gz"

if [[ -f "${KERNEL}" && -f "${INITRD}" ]]; then
  echo "guest assets already present"
  exit 0
fi

"${REPO_ROOT}/guest/image/build.sh"

if [[ ! -f "${KERNEL}" || ! -f "${INITRD}" ]]; then
  echo "guest assets missing after build" >&2
  exit 1
fi
