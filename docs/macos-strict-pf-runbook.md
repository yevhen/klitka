# macOS VM runbook for strict PF suites

This runbook defines how to validate macOS strict transport (`pf`) behavior in a
reproducible way.

## Execution model (important)

- Mandatory CI gate remains **Linux-only**.
- macOS/PF verification is executed in a **dedicated macOS VM**.
- Test commands are the same as normal (`go test`, `sdk npm test`):
  no separate custom runner is required.
- PF-specific checks run automatically when host OS is macOS and `/dev/pf` is
  writable; otherwise those checks are skipped by design.

## Who runs this and when

- **Who:** developer/reviewer working on strict networking, or release/security owner.
- **Where:** dedicated macOS VM with PF access.
- **When:**
  - before merging changes in strict networking/PF/DNS enforcement paths,
  - before security-sensitive releases touching network policy.
- **CI relation:** Linux CI remains mandatory; this VM run is an additional
  validation lane (not a required CI job at this stage).

Convenience targets:

```bash
# Run inside an already prepared dedicated macOS VM
make test-strict-pf-vm

# Provision/start dedicated macOS VM via Tart and run suites remotely
make test-strict-pf-vm-tart
```

## Preconditions

If using Tart orchestration from host:

1. Host is macOS Apple Silicon.
2. `tart`, `sshpass`, `ssh`, `rsync` are installed.
3. A Cirrus macOS image is available or pullable (default in script:
   `ghcr.io/cirruslabs/macos-sequoia-base:latest`).
4. Enough free disk for image pull/unpack (script default check: at least 70 GiB
   free on host data volume).

Inside the dedicated macOS VM:

1. Repository is checked out.
2. Nix dev environment is available (`./scripts/bootstrap.sh` if needed).
3. Current test user has write access to `/dev/pf`.
4. Guest assets exist (or can be built).

Preflight:

```bash
sw_vers
uname -a
bash -lc 'exec 3<>/dev/pf && exec 3>&- && echo "/dev/pf writable"'
```

If `/dev/pf` is not writable, strict PF validations will skip.

## One-time setup per fresh VM

```bash
./scripts/bootstrap.sh
nix develop --command ./guest/image/build.sh
```

## Reproducible validation commands

### Preferred: one-command runner (inside VM)

```bash
./scripts/run-strict-pf-vm.sh --focused
```

It executes preflight, full suites, optional focused strict suites, PF residue checks,
and writes artifacts to `./artifacts/strict-pf-<timestamp>`.

### Optional: one-command orchestration from host (Tart)

```bash
./scripts/run-strict-pf-vm-tart.sh
```

It can clone/start a dedicated macOS VM, sync repository, run the same strict PF
validation command inside VM, and pull artifacts back to host.

Disk-space check can be tuned/disabled if needed:

```bash
./scripts/run-strict-pf-vm-tart.sh --min-free-gib 0
```

Use external storage for Tart images (recommended on low free-space hosts):

```bash
./scripts/run-strict-pf-vm-tart.sh --tart-home /Volumes/ExternalSSD/tart
```

### Manual equivalent (same logic)

Create artifact folder:

```bash
ARTIFACTS="./artifacts/strict-pf-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$ARTIFACTS"
```

### 1) Go full suite (authoritative project gate)

```bash
nix develop --command bash -lc 'go test ./... -count=1 -v' \
  | tee "$ARTIFACTS/go-test-full.log"
```

### 2) SDK full suite

```bash
nix develop --command bash -lc 'cd sdk && npm test' \
  | tee "$ARTIFACTS/sdk-test-full.log"
```

### 3) Optional focused strict pass (diagnostics)

```bash
nix develop --command bash -lc '
  go test ./tests -count=1 -v -run "TestE2E(BypassProxyDenied|NetworkAllowlistStrict|DenyOverridesAllowStrict|PrivateRangeBlockStrict|LoopbackAndMetadataBlockedStrict|DNSModeSyntheticBlocksExfil|DNSModeTrustedUsesAllowedResolvers|DNSModeOpenCompatibilityStrict|DirectTCPOtherPortsBlockedStrict|UDPNon53BlockedStrict|IPv6BlockedStrict|DoHEndpointRespectsAllowlistStrict|SecretInjectionStrict|SecretHostMismatchStrict|StrictMultiVMConcurrentIsolation|StrictStopOneVMOtherContinues|StrictRepeatedStartStopCycles)"
' | tee "$ARTIFACTS/go-test-strict-focused.log"

nix develop --command bash -lc '
  cd sdk && tsx --test ./test/network_allowlist_strict.test.ts ./test/proxy_bypass_denied.test.ts ./test/dns_modes.test.ts ./test/secret_injection_strict.test.ts
' | tee "$ARTIFACTS/sdk-test-strict-focused.log"
```

## Pass / fail criteria

Run is **PASS** when all conditions hold:

1. `go test ./...` exits `0`.
2. `cd sdk && npm test` exits `0`.
3. No PF-permission skip appears in strict validation logs:

```bash
if rg -n "strict transport requires /dev/pf write access" "$ARTIFACTS"; then
  echo "FAIL: PF checks skipped"
  exit 1
fi
```

4. No stale `klitka` PF anchors remain after run:

```bash
if sudo pfctl -s Anchors | rg "^com\.klitka\.vm/"; then
  echo "FAIL: stale anchors"
  exit 1
else
  echo "No stale anchors"
fi
```

## Observability evidence checklist

Collect these from logs/test outputs:

- strict activation fields: `network_enforcement=strict`, `dns_mode`, `vm_id`, `anchor_id`
- shutdown counters: `blocked_egress`, `dns_blocked`

Quick grep:

```bash
rg -n "network_enforcement=strict|dns_mode=|anchor_id=|blocked_egress=|dns_blocked=" "$ARTIFACTS"
```

Note: some assertions are validated inside tests (pass/fail signal) and may not
print full daemon logs unless run with verbose diagnostics.

## Cleanup

```bash
# Best-effort cleanup visibility
sudo pfctl -s Anchors | rg "^com\.klitka\.vm/" || true

# If anything is left unexpectedly, flush explicitly (careful in shared hosts):
# sudo pfctl -a com.klitka.vm/<anchor-id> -F all
```

Keep the artifact directory for traceability of each VM run.
