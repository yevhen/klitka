#!/usr/bin/env bash
set -euo pipefail

archive="${1:-}"
if [[ -z "${archive}" ]]; then
  os=$(go env GOOS)
  arch=$(go env GOARCH)
  archive=$(ls "dist/klitka_*_${os}_${arch}.tar.gz" 2>/dev/null | head -n 1 || true)
fi

if [[ -z "${archive}" ]]; then
  echo "no archive found for smoke test" >&2
  exit 1
fi

work_dir=$(mktemp -d)
cleanup() {
  if [[ -n "${daemon_pid:-}" ]]; then
    kill "${daemon_pid}" >/dev/null 2>&1 || true
    wait "${daemon_pid}" >/dev/null 2>&1 || true
  fi
  rm -rf "${work_dir}" "${socket_dir}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

if [[ "$(uname -s)" == "Linux" && ! -e /dev/kvm && "${KLITKA_ALLOW_TCG:-}" != "1" ]]; then
  echo "KVM not available; skipping VM smoke test (set KLITKA_ALLOW_TCG=1 to run with TCG)"
  exit 0
fi

tar -xzf "${archive}" -C "${work_dir}"
root_dir=$(find "${work_dir}" -maxdepth 1 -type d -name "klitka_*" | head -n 1 || true)
if [[ -z "${root_dir}" ]]; then
  echo "archive did not contain expected root directory" >&2
  exit 1
fi

export PATH="${root_dir}:${PATH}"
export KLITKA_GUEST_KERNEL="${root_dir}/share/klitka/guest/vmlinuz"
export KLITKA_GUEST_INITRD="${root_dir}/share/klitka/guest/initramfs.cpio.gz"

socket_dir=$(mktemp -d)
socket_path="${socket_dir}/klitka.sock"

"${root_dir}/klitka-daemon" --socket "${socket_path}" >"${work_dir}/daemon.log" 2>&1 &
daemon_pid=$!

for _ in $(seq 1 50); do
  if [[ -S "${socket_path}" ]]; then
    break
  fi
  if ! kill -0 "${daemon_pid}" >/dev/null 2>&1; then
    echo "daemon exited early" >&2
    cat "${work_dir}/daemon.log" >&2 || true
    exit 1
  fi
  sleep 0.1
done

if [[ ! -S "${socket_path}" ]]; then
  echo "daemon did not create socket" >&2
  cat "${work_dir}/daemon.log" >&2 || true
  exit 1
fi

klitka exec --socket "${socket_path}" -- uname -a
printf "echo hi\nexit\n" | klitka shell --socket "${socket_path}"
