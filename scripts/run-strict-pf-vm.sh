#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
ARTIFACTS_DIR="${REPO_ROOT}/artifacts/strict-pf-$(date +%Y%m%d-%H%M%S)"
RUN_FOCUSED=0
SKIP_GUEST_BUILD=0

usage() {
  cat <<'EOF'
Usage: scripts/run-strict-pf-vm.sh [options]

Run strict PF validation in a dedicated macOS VM using normal project suites.

Options:
  --artifacts-dir <path>  Directory for logs/artifacts (default: ./artifacts/strict-pf-<timestamp>)
  --focused               Also run focused strict suites after full suites
  --skip-guest-build      Skip guest assets ensure step
  -h, --help              Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --artifacts-dir)
      ARTIFACTS_DIR="$2"
      shift 2
      ;;
    --focused)
      RUN_FOCUSED=1
      shift
      ;;
    --skip-guest-build)
      SKIP_GUEST_BUILD=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "required command not found: $cmd" >&2
    exit 2
  fi
}

require_cmd nix
require_cmd rg

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "this script must run in macOS VM (current: $(uname -s))" >&2
  exit 2
fi

if ! bash -lc 'exec 3<>/dev/pf && exec 3>&-'; then
  echo "strict PF checks require writable /dev/pf (run inside dedicated macOS VM with PF access)" >&2
  exit 2
fi

mkdir -p "$ARTIFACTS_DIR"

{
  echo "date=$(date -Iseconds)"
  echo "repo_root=$REPO_ROOT"
  echo "artifacts_dir=$ARTIFACTS_DIR"
  sw_vers || true
  uname -a
  whoami
  id
  echo "/dev/pf writable=yes"
} >"$ARTIFACTS_DIR/preflight.txt"

echo "Artifacts: $ARTIFACTS_DIR"

if [[ "$SKIP_GUEST_BUILD" -eq 0 ]]; then
  echo "==> Ensuring guest assets"
  nix develop --command bash -lc './scripts/build-guest-assets.sh' \
    >"$ARTIFACTS_DIR/guest-assets.log" 2>&1
fi

failures=()

run_logged() {
  local name="$1"
  local cmd="$2"
  local log="$ARTIFACTS_DIR/${name}.log"

  echo "==> $name"
  set +e
  nix develop --command bash -lc "$cmd" 2>&1 | tee "$log"
  local status=${PIPESTATUS[0]}
  set -e
  if [[ $status -ne 0 ]]; then
    failures+=("$name (exit $status)")
  fi
}

run_logged "go-test-full" "go test ./... -count=1 -v"
run_logged "sdk-test-full" "cd sdk && npm test"

if [[ "$RUN_FOCUSED" -eq 1 ]]; then
  run_logged "go-test-strict-focused" "go test ./tests -count=1 -v -run 'TestE2E(BypassProxyDenied|NetworkAllowlistStrict|DenyOverridesAllowStrict|PrivateRangeBlockStrict|LoopbackAndMetadataBlockedStrict|DNSModeSyntheticBlocksExfil|DNSModeTrustedUsesAllowedResolvers|DNSModeOpenCompatibilityStrict|DirectTCPOtherPortsBlockedStrict|UDPNon53BlockedStrict|IPv6BlockedStrict|DoHEndpointRespectsAllowlistStrict|SecretInjectionStrict|SecretHostMismatchStrict|StrictMultiVMConcurrentIsolation|StrictStopOneVMOtherContinues|StrictRepeatedStartStopCycles)'"
  run_logged "sdk-test-strict-focused" "cd sdk && tsx --test ./test/network_allowlist_strict.test.ts ./test/proxy_bypass_denied.test.ts ./test/dns_modes.test.ts ./test/secret_injection_strict.test.ts"
fi

if rg -n "strict transport requires /dev/pf write access" "$ARTIFACTS_DIR" >"$ARTIFACTS_DIR/pf-skip-detected.txt"; then
  failures+=("pf-checks-skipped")
fi

collect_pf_anchors() {
  local output_file="$1"

  if pfctl -s Anchors >"$output_file" 2>&1; then
    return 0
  fi
  if command -v sudo >/dev/null 2>&1 && sudo -n pfctl -s Anchors >"$output_file" 2>&1; then
    return 0
  fi
  return 1
}

if collect_pf_anchors "$ARTIFACTS_DIR/pf-anchors-after.txt"; then
  if rg -n "^com\\.klitka\\.vm/" "$ARTIFACTS_DIR/pf-anchors-after.txt" >"$ARTIFACTS_DIR/pf-stale-anchors.txt"; then
    failures+=("pf-stale-anchors")
  fi
else
  echo "warning: unable to inspect PF anchors (pfctl/sudo unavailable or denied)" | tee "$ARTIFACTS_DIR/pf-anchor-inspection-warning.txt"
  failures+=("pf-anchor-inspection")
fi

rg -n "network_enforcement=strict|dns_mode=|anchor_id=|blocked_egress=|dns_blocked=" "$ARTIFACTS_DIR" >"$ARTIFACTS_DIR/observability-hits.txt" || true

if [[ ${#failures[@]} -gt 0 ]]; then
  echo
  echo "STRICT PF VM RUN: FAIL"
  printf ' - %s\n' "${failures[@]}"
  echo "Artifacts: $ARTIFACTS_DIR"
  exit 1
fi

echo
echo "STRICT PF VM RUN: PASS"
echo "Artifacts: $ARTIFACTS_DIR"
