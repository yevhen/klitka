# ADR 0002: Use isolated macOS VM runners for strict PF transport testing

- **Status:** Accepted (Phase 1: dedicated VM execution)
- **Date:** 2026-02-17
- **Last updated:** 2026-02-22
- **Deciders:** klitka maintainers
- **Related:** `pf.md`, ADR 0001

## Context

Strict transport tests on macOS require live PF operations. In local/dev environments without write access to `/dev/pf`, strict E2E suites are skipped by design.

Current situation:

- this prevents false failures on non-privileged developer machines,
- but also means strict PF behavior is not always exercised locally,
- and verification can drift toward unit/mocked coverage unless a privileged environment is available.

We need a repeatable, isolated place to run strict PF-path validation.

## Decision

Strict PF verification is executed in a **dedicated macOS VM** with `/dev/pf` access, while the mandatory CI gate remains Linux-only.

Execution model:

- run the normal project suites (`go test ./...`, SDK tests) in the macOS VM,
- PF-specific checks execute automatically there as part of the same suites,
- on non-macOS or macOS without PF write access, PF-specific checks may skip by design,
- collect logs/artifacts for policy counters and cleanup evidence.

This is a **testing transition ADR**, not a runtime backend migration.

## Why macOS VM runner

Compared with developer laptops and ad-hoc sudo runs:

- more reproducible environment,
- isolated from developer host state,
- easier to reason about PF side effects and cleanup,
- suitable for repeatable strict-path validation before introducing any CI automation.

## Non-goals

- replacing the production QEMU runtime backend,
- replacing all existing test lanes,
- using Linux-container-only runners as equivalent PF validation environments.

## Consequences

### Positive

- stricter, repeatable coverage for PF strict-path behavior,
- reduced dependence on local workstation privilege configuration,
- better signal for security-sensitive regressions.

### Negative

- additional infra complexity (VM provisioning, images, orchestration),
- possible performance cost vs bare-metal,
- still needs periodic cross-check against bare-metal macOS behavior.

## Rollout plan

1. **Phase 1 (current): dedicated VM execution (operator-triggered)**
   - run full Go + SDK suites in macOS VM,
   - verify PF-specific strict checks through normal suites,
   - collect flakes, timing, and cleanup residue metrics.
2. **Phase 2: stabilize operations**
   - codify setup/teardown via runbook,
   - tune timeouts and diagnostics,
   - ensure deterministic cleanup checks.
3. **Phase 3 (optional future): CI automation**
   - evaluate when/if to promote macOS VM lane to automated gate.

Operational procedure: [`macos-strict-pf-runbook.md`](../macos-strict-pf-runbook.md).

## Acceptance criteria for promotion

- consistent pass rate for strict PF suites,
- no persistent PF residue across repeated runs,
- clear failure diagnostics (startup/setup/cleanup stages),
- documented operator runbook.

## References

- Apple Virtualization docs:
  https://developer.apple.com/documentation/virtualization/running-macos-in-a-virtual-machine-on-apple-silicon
- Apple macOS SLA:
  https://www.apple.com/legal/sla/docs/macOSSequoia.pdf
- Docker Desktop macOS docs (Linux VM boundary):
  https://docs.docker.com/desktop/setup/install/mac-permission-requirements/
- Apple `container` README:
  https://github.com/apple/container
