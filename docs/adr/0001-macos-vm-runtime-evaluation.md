# ADR 0001: Evaluate macOS VM as a future guest-runtime backend

- **Status:** Proposed
- **Date:** 2026-02-17
- **Deciders:** klitka maintainers
- **Related:** `pf.md`, `docs/network-enforcement.md`, ADR 0002

## Context

`klitka` currently runs guest workloads via the QEMU-based VM backend and enforces strict network policy on macOS via host Packet Filter (PF) integration.

During strict-path hardening and test work we confirmed:

1. **Nix dev shell is toolchain isolation, not kernel isolation.**
   Running via `nix develop` still uses the host macOS kernel and host `/dev/pf` device.
2. **PF access is privilege-gated.**
   Writing firewall rules requires access to `/dev/pf` (typically elevated privileges).
3. **macOS “containers” are not a drop-in replacement for host PF enforcement.**
   - Docker Desktop on macOS runs containers in a lightweight Linux VM.
   - Apple `container` also runs Linux containers as lightweight VMs.
   These do not provide direct host-macOS PF semantics for `klitka` strict-path verification.
4. **macOS virtualization is available on Apple Silicon** (Virtualization.framework), so an isolated macOS VM execution environment is possible.

Because of this, a future transition to running the guest-image workflow “inside macOS VM” was discussed.

## Decision

We **do not switch runtime backend now**.

We keep the current QEMU guest runtime as the production path and treat macOS VM runtime as a **future exploratory option** requiring a dedicated proof-of-concept.

## Why not switch now

A full backend switch is not guaranteed to preserve current security/behavioral properties without re-validation:

- network path and mediation semantics can differ from the current QEMU+host-PF flow,
- nested virtualization and acceleration constraints may impact reliability/performance,
- strict policy guarantees must be re-proven end-to-end for the new topology,
- lifecycle and cleanup assumptions (anchors, routing, startup ordering) must be re-verified.

In short: this is not a transparent transport swap.

## Consequences

### Positive

- preserves current known-good runtime behavior,
- avoids premature migration risk in strict-security work,
- allows targeted experimentation without destabilizing mainline runtime.

### Negative

- no immediate simplification of local strict verification on privilege-restricted developer machines,
- continued dependence on host PF privileges for full strict-path local validation.

## Follow-up plan (exploration gate)

A future runtime-switch ADR should only proceed after a PoC demonstrates:

1. policy parity for allow/deny/private-range behavior,
2. strict no-bypass guarantees comparable to current path,
3. deterministic lifecycle cleanup under failure and restart,
4. acceptable performance and operational complexity,
5. clear migration and rollback strategy.

If these gates pass, a separate “Adopt macOS VM runtime backend” ADR can move to Accepted.

## References

- Docker Desktop macOS permissions/docs (containers run in Linux VM):
  https://docs.docker.com/desktop/setup/install/mac-permission-requirements/
- Apple `container` project README (Linux containers in lightweight VMs):
  https://github.com/apple/container
- Apple Virtualization docs (run macOS VM on Apple Silicon):
  https://developer.apple.com/documentation/virtualization/running-macos-in-a-virtual-machine-on-apple-silicon
- Apple macOS SLA (virtual instances on Apple-branded hardware):
  https://www.apple.com/legal/sla/docs/macOSSequoia.pdf
