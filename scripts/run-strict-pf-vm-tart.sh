#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
LOCAL_ARTIFACTS_DIR="${REPO_ROOT}/artifacts/strict-pf-tart-$(date +%Y%m%d-%H%M%S)"

VM_NAME="klitka-strict-pf"
VM_IMAGE="ghcr.io/cirruslabs/macos-sequoia-base:latest"
VM_USER="admin"
VM_PASSWORD="admin"
VM_WORKDIR="/Users/admin/klitka"
VM_ARTIFACTS_DIR="/Users/admin/klitka-artifacts"
RUN_FOCUSED=1
BOOTSTRAP_VM=0
DESTROY_VM=0
KEEP_VM_RUNNING=0
REMOTE_SUDO_MODE="auto" # auto|always|never
MIN_FREE_GIB=70
TART_HOME_OVERRIDE=""

SSH_OPTS=(
  -o StrictHostKeyChecking=no
  -o UserKnownHostsFile=/dev/null
  -o ConnectTimeout=5
)

usage() {
  cat <<'EOF'
Usage: scripts/run-strict-pf-vm-tart.sh [options]

Provision (if needed) a dedicated macOS VM via Tart and run strict PF suites in it.

Requirements:
  - Host macOS on Apple Silicon
  - tart, sshpass, ssh, rsync installed

Options:
  --artifacts-dir <path>      Local artifacts directory
  --vm-name <name>            Tart VM name (default: klitka-strict-pf)
  --vm-image <oci-ref>        Tart source image (default: ghcr.io/cirruslabs/macos-sequoia-base:latest)
  --vm-user <user>            VM SSH user (default: admin)
  --vm-password <password>    VM SSH password (default: admin)
  --vm-workdir <path>         Remote repo path in VM (default: /Users/admin/klitka)
  --vm-artifacts-dir <path>   Remote artifacts path in VM (default: /Users/admin/klitka-artifacts)
  --tart-home <path>          Override TART_HOME (e.g. external volume for VM storage)
  --remote-sudo <mode>        auto|always|never (default: auto)
  --min-free-gib <num>        Minimal free disk space on host data volume (default: 70, 0 disables)
  --no-focused                Run only full suites (skip focused strict suites)
  --bootstrap                 Run ./scripts/bootstrap.sh in VM before tests
  --destroy                   Delete VM after run
  --keep-running              Keep VM running after run
  -h, --help                  Show help
EOF
}

log() {
  echo "[strict-pf-tart] $*"
}

die() {
  echo "[strict-pf-tart] ERROR: $*" >&2
  exit 1
}

require_cmd() {
  local cmd="$1"
  command -v "$cmd" >/dev/null 2>&1 || die "required command not found: $cmd"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --artifacts-dir)
      LOCAL_ARTIFACTS_DIR="$2"
      shift 2
      ;;
    --vm-name)
      VM_NAME="$2"
      shift 2
      ;;
    --vm-image)
      VM_IMAGE="$2"
      shift 2
      ;;
    --vm-user)
      VM_USER="$2"
      shift 2
      ;;
    --vm-password)
      VM_PASSWORD="$2"
      shift 2
      ;;
    --vm-workdir)
      VM_WORKDIR="$2"
      shift 2
      ;;
    --vm-artifacts-dir)
      VM_ARTIFACTS_DIR="$2"
      shift 2
      ;;
    --tart-home)
      TART_HOME_OVERRIDE="$2"
      shift 2
      ;;
    --remote-sudo)
      REMOTE_SUDO_MODE="$2"
      shift 2
      ;;
    --min-free-gib)
      MIN_FREE_GIB="$2"
      shift 2
      ;;
    --no-focused)
      RUN_FOCUSED=0
      shift
      ;;
    --bootstrap)
      BOOTSTRAP_VM=1
      shift
      ;;
    --destroy)
      DESTROY_VM=1
      shift
      ;;
    --keep-running)
      KEEP_VM_RUNNING=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

if [[ "$REMOTE_SUDO_MODE" != "auto" && "$REMOTE_SUDO_MODE" != "always" && "$REMOTE_SUDO_MODE" != "never" ]]; then
  die "--remote-sudo must be one of: auto|always|never"
fi
if ! [[ "$MIN_FREE_GIB" =~ ^[0-9]+$ ]]; then
  die "--min-free-gib must be a non-negative integer"
fi

if [[ "$(uname -s)" != "Darwin" ]]; then
  die "this orchestrator must run on macOS host"
fi
if [[ "$(uname -m)" != "arm64" ]]; then
  die "this orchestrator expects Apple Silicon host (arm64)"
fi

require_cmd tart
require_cmd sshpass
require_cmd ssh
require_cmd rsync
require_cmd rg

tart_cmd() {
  if [[ -n "$TART_HOME_OVERRIDE" ]]; then
    TART_HOME="$TART_HOME_OVERRIDE" tart "$@"
  else
    tart "$@"
  fi
}

host_data_path="/"
if [[ -n "$TART_HOME_OVERRIDE" ]]; then
  mkdir -p "$TART_HOME_OVERRIDE"
  host_data_path="$TART_HOME_OVERRIDE"
elif [[ -d "/System/Volumes/Data" ]]; then
  host_data_path="/System/Volumes/Data"
fi
if [[ "$MIN_FREE_GIB" -gt 0 ]]; then
  avail_kib=$(df -k "$host_data_path" | awk 'NR==2 {print $4}')
  avail_gib=$((avail_kib / 1024 / 1024))
  if [[ "$avail_gib" -lt "$MIN_FREE_GIB" ]]; then
    die "insufficient disk space on $host_data_path: ${avail_gib}GiB free, need at least ${MIN_FREE_GIB}GiB (set --min-free-gib 0 to disable check)"
  fi
fi

mkdir -p "$LOCAL_ARTIFACTS_DIR"

VM_STARTED_BY_SCRIPT=0
VM_IP=""

cleanup() {
  if [[ "$KEEP_VM_RUNNING" -eq 0 && "$VM_STARTED_BY_SCRIPT" -eq 1 ]]; then
    log "Stopping VM: $VM_NAME"
    tart_cmd stop "$VM_NAME" >/dev/null 2>&1 || true
  fi
  if [[ "$DESTROY_VM" -eq 1 ]]; then
    log "Deleting VM: $VM_NAME"
    tart_cmd delete "$VM_NAME" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

ssh_cmd() {
  sshpass -p "$VM_PASSWORD" ssh "${SSH_OPTS[@]}" "${VM_USER}@${VM_IP}" "$@"
}

rsync_to_vm() {
  sshpass -p "$VM_PASSWORD" rsync -az --delete \
    --exclude .git \
    --exclude artifacts \
    --exclude .pi \
    --exclude guest/image/out \
    --exclude guest/image/.cache \
    --exclude guest/.zig-cache \
    --exclude guest/zig-out \
    --exclude sdk/node_modules \
    -e "ssh ${SSH_OPTS[*]}" \
    "$REPO_ROOT/" "${VM_USER}@${VM_IP}:${VM_WORKDIR}/"
}

rsync_from_vm_artifacts() {
  mkdir -p "$LOCAL_ARTIFACTS_DIR/vm-artifacts"
  sshpass -p "$VM_PASSWORD" rsync -az \
    -e "ssh ${SSH_OPTS[*]}" \
    "${VM_USER}@${VM_IP}:${VM_ARTIFACTS_DIR}/" "$LOCAL_ARTIFACTS_DIR/vm-artifacts/" || true
}

vm_exists() {
  tart_cmd list 2>/dev/null | rg -q "^${VM_NAME}(\s|$)"
}

log "Local artifacts: $LOCAL_ARTIFACTS_DIR"
if [[ -n "$TART_HOME_OVERRIDE" ]]; then
  log "Using TART_HOME=$TART_HOME_OVERRIDE"
fi

if vm_exists; then
  log "VM already exists: $VM_NAME"
else
  log "Cloning VM image: $VM_IMAGE -> $VM_NAME"
  tart_cmd clone "$VM_IMAGE" "$VM_NAME" | tee "$LOCAL_ARTIFACTS_DIR/tart-clone.log"
fi

if VM_IP=$(tart_cmd ip "$VM_NAME" 2>/dev/null | tr -d '\r' | head -n 1); [[ -n "$VM_IP" ]]; then
  log "VM already running at IP: $VM_IP"
else
  log "Starting VM (headless): $VM_NAME"
  (tart_cmd run --no-graphics "$VM_NAME" >"$LOCAL_ARTIFACTS_DIR/tart-run.log" 2>&1) &
  VM_STARTED_BY_SCRIPT=1

  for _ in $(seq 1 180); do
    VM_IP=$(tart_cmd ip "$VM_NAME" 2>/dev/null | tr -d '\r' | head -n 1 || true)
    if [[ -n "$VM_IP" ]]; then
      break
    fi
    sleep 2
  done
fi

[[ -n "$VM_IP" ]] || die "failed to obtain VM IP"
log "VM IP: $VM_IP"

echo "vm_name=$VM_NAME" >"$LOCAL_ARTIFACTS_DIR/metadata.txt"
echo "vm_image=$VM_IMAGE" >>"$LOCAL_ARTIFACTS_DIR/metadata.txt"
echo "vm_ip=$VM_IP" >>"$LOCAL_ARTIFACTS_DIR/metadata.txt"
echo "tart_home=${TART_HOME_OVERRIDE:-~/.tart}" >>"$LOCAL_ARTIFACTS_DIR/metadata.txt"
echo "date=$(date -Iseconds)" >>"$LOCAL_ARTIFACTS_DIR/metadata.txt"

log "Waiting for SSH"
for _ in $(seq 1 180); do
  if ssh_cmd "echo ready" >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
ssh_cmd "echo ready" >/dev/null 2>&1 || die "VM SSH did not become ready"

log "Preparing VM directories"
ssh_cmd "mkdir -p '$VM_WORKDIR' '$VM_ARTIFACTS_DIR'"

log "Syncing repository to VM"
rsync_to_vm | tee "$LOCAL_ARTIFACTS_DIR/rsync-to-vm.log"

if [[ "$BOOTSTRAP_VM" -eq 1 ]]; then
  log "Running bootstrap in VM"
  ssh_cmd "bash -lc 'cd $(printf '%q' "$VM_WORKDIR") && ./scripts/bootstrap.sh'" | tee "$LOCAL_ARTIFACTS_DIR/bootstrap.log"
fi

runner_args=(--artifacts-dir "$VM_ARTIFACTS_DIR")
if [[ "$RUN_FOCUSED" -eq 1 ]]; then
  runner_args+=(--focused)
fi

remote_runner_cmd="cd $(printf '%q' "$VM_WORKDIR") && ./scripts/run-strict-pf-vm.sh"
for arg in "${runner_args[@]}"; do
  remote_runner_cmd+=" $(printf '%q' "$arg")"
done

use_sudo=0
case "$REMOTE_SUDO_MODE" in
  always)
    use_sudo=1
    ;;
  never)
    use_sudo=0
    ;;
  auto)
    if ssh_cmd "bash -lc 'exec 3<>/dev/pf && exec 3>&-'" >/dev/null 2>&1; then
      use_sudo=0
    else
      use_sudo=1
    fi
    ;;
esac

log "Running strict PF suites inside VM (sudo=$use_sudo)"
set +e
if [[ "$use_sudo" -eq 1 ]]; then
  ssh_cmd "echo '$VM_PASSWORD' | sudo -S env PATH=\"\$PATH\" bash -lc $(printf '%q' "$remote_runner_cmd")" \
    | tee "$LOCAL_ARTIFACTS_DIR/remote-run.log"
  remote_status=${PIPESTATUS[0]}
else
  ssh_cmd "bash -lc $(printf '%q' "$remote_runner_cmd")" \
    | tee "$LOCAL_ARTIFACTS_DIR/remote-run.log"
  remote_status=${PIPESTATUS[0]}
fi
set -e

log "Syncing artifacts from VM"
rsync_from_vm_artifacts

if [[ "$remote_status" -ne 0 ]]; then
  die "remote strict PF run failed (exit $remote_status), see $LOCAL_ARTIFACTS_DIR"
fi

log "DONE: strict PF run succeeded"
log "Artifacts: $LOCAL_ARTIFACTS_DIR"
