# Network enforcement and DNS profiles

This document describes `klitka` network policy behavior for hardened workloads.

## Threat model

Assume untrusted guest code can:
- unset `HTTP_PROXY` / `HTTPS_PROXY`
- attempt direct outbound sockets
- use DNS lookups for data exfiltration
- probe private/internal address space

DNS-specific risks are covered in detail in
[`dns-security-considerations.md`](./dns-security-considerations.md).

## Network policy surface

`NetworkPolicy` fields:
- `allow_hosts[]`
- `deny_hosts[]`
- `block_private_ranges`
- `egress_mode` (`compat` | `strict`)
- `dns_mode` (`open` | `trusted` | `synthetic`)
- `trusted_dns_servers[]` (used with `dns_mode=trusted`)

CLI flags:
- `--allow-host`, `--deny-host`, `--block-private`
- `--egress-mode compat|strict`
- `--dns-mode open|trusted|synthetic`
- `--trusted-dns <ip[:port]>` (repeatable)

SDK (`NetworkConfig`):
- `allowHosts`, `denyHosts`, `blockPrivateRanges`
- `egressMode`, `dnsMode`, `trustedDnsServers`

## Egress modes

### `compat` (default)

Backwards-compatible behavior:
- proxy env vars are injected into guest process env
- allow/deny/private-range policy is still enforced on host proxy path
- warning is logged because guest code may try alternate paths

Use this mode to preserve legacy behavior while preparing migration.

### `strict`

Hardened mode:
- VM backend is required (`host` backend fallback is rejected)
- policy is enforced through host mediation path
- bypass attempts covered by e2e regression tests are denied
- direct TCP/UDP attempts outside the mediation path are blocked
- on macOS, strict transport setup uses Packet Filter (`pf`) and requires
  daemon access to `/dev/pf`; setup failures are fail-closed (StartVM error)

### IPv6 behavior in strict mode

Current strict behavior is deterministic:
- outbound IPv6 egress is treated as unsupported and blocked
- if a hostname resolves only to IPv6 addresses, the request is denied
- dual-stack results are reduced to IPv4 addresses in strict mode

Compat mode keeps existing behavior.

### DoH / DoT semantics

`klitka` does not special-case DoH/DoT as a separate bypass channel.
They are treated as normal network endpoints under the same host policy:
- DoH/DoT traffic is allowed only when endpoint host policy allows it
- denylist and private-range policy still apply
- no hidden bypass exists outside strict mediation path

## DNS modes

For DNS security rationale, threat-model details, and mode trade-offs, see
[`dns-security-considerations.md`](./dns-security-considerations.md).

Operational summary:

- `open` (default): compatibility resolver behavior.
- `trusted`: resolver lookups use only `trusted_dns_servers[]`.
- `synthetic`: external DNS lookups are blocked in the proxy resolver path.

Validation rules:
- `trusted` requires at least one trusted DNS server
- trusted servers are only accepted in `trusted` mode

## Policy evaluation order

For each requested host:
1. normalize host
2. deny list check
3. allow list check (if configured)
4. private-range blocking (if enabled)
5. DNS profile lookup behavior

## Secrets and MITM

Secret injection behavior is unchanged:
- secrets are injected only for matching hosts
- values remain masked in guest env
- strict mode keeps MITM/secret workflows functional

## Observability

Structured logs include:
- `network_enforcement=compat|strict`
- `dns_mode=open|trusted|synthetic`
- policy cardinality (allow/deny/trusted dns counts)

Proxy shutdown log includes counters:
- `blocked_egress`
- `dns_blocked`

## Migration guide

1. Start with `egressMode: "compat"`.
2. Explicitly enumerate `allowHosts` and `denyHosts`.
3. Decide DNS profile:
   - `open` for compatibility
   - `trusted` for pinned resolvers
   - `synthetic` for DNS-minimized environments
4. Keep Linux CI as the mandatory gate (`go test ./...`, SDK tests).
5. Run macOS strict PF validation in a dedicated macOS VM (same test commands; PF checks execute there automatically).
6. Roll out strict mode in production sandboxes.

## Where strict PF checks execute

- Linux CI is the required automated gate.
- macOS strict PF checks are part of the normal Go/SDK test suites and execute
  automatically when the host is macOS with writable `/dev/pf`.
- For reproducible PF validation, run suites in a dedicated macOS VM.
- Outside that environment, PF-specific checks may be skipped by design.

Runbook: [`macos-strict-pf-runbook.md`](./macos-strict-pf-runbook.md).
Helper scripts: `./scripts/run-strict-pf-vm.sh --focused` (inside VM), `./scripts/run-strict-pf-vm-tart.sh` (host orchestration via Tart).

## Test coverage

Go e2e:
- `TestE2ENetworkAllowlistStrict`
- `TestE2EBypassProxyDenied`
- `TestE2EDenyOverridesAllowStrict`
- `TestE2EPrivateRangeBlockStrict`
- `TestE2ELoopbackAndMetadataBlockedStrict`
- `TestE2EDNSModeSyntheticBlocksExfil`
- `TestE2EDNSModeTrustedUsesAllowedResolvers`
- `TestE2EDNSModeOpenCompatibilityStrict`
- `TestE2EDirectTCPOtherPortsBlockedStrict`
- `TestE2EUDPNon53BlockedStrict`
- `TestE2EIPv6BlockedStrict`
- `TestE2EDoHEndpointRespectsAllowlistStrict`
- `TestE2ESecretInjectionStrict`
- `TestE2ESecretHostMismatchStrict`
- `TestE2EStrictMultiVMConcurrentIsolation`
- `TestE2EStrictStopOneVMOtherContinues`
- `TestE2EStrictRepeatedStartStopCycles`

Daemon unit/integration:
- `TestProxyShutdownCountersReflectDeniedTraffic`
- `TestNetworkManagerStrictLogIncludesVMAndAnchor`
- `TestNetworkManagerStrictCloseIdempotent`
- `TestNewNetworkManagerStrictTransportSetupFailure`
- `TestNewNetworkManagerStrictTransportPrepareFailure`

SDK:
- `network_allowlist_strict.test.ts`
- `proxy_bypass_denied.test.ts`
- `dns_modes.test.ts`
- `secret_injection_strict.test.ts`

## FR / NFR traceability matrix

| Requirement | Covered by tests |
| --- | --- |
| FR-1 Strict activation fail-closed | `TestNewNetworkManagerStrictTransportSetupFailure`, `TestNewNetworkManagerStrictTransportPrepareFailure`, `TestE2EBypassProxyDenied` |
| FR-2 Direct egress denial | `TestE2EBypassProxyDenied`, `TestE2EDirectTCPOtherPortsBlockedStrict`, `TestE2EUDPNon53BlockedStrict`, `TestE2EIPv6BlockedStrict` |
| FR-3 Controlled DNS path | `TestE2EDNSModeSyntheticBlocksExfil`, `TestE2EDNSModeTrustedUsesAllowedResolvers`, `TestE2EDNSModeOpenCompatibilityStrict`, `TestE2EDNSModeTrustedRequiresResolverConfig`, `TestE2EDNSModeTrustedRejectsInvalidResolverEndpoint` |
| FR-4 Lifecycle cleanup | `TestNetworkManagerStrictCloseIdempotent`, `TestStrictRepeatedStartStopNoAnchorLeak`, `TestStrictResidueCleanupPrepareBeforeFactory`, `TestE2EStrictRepeatedStartStopCycles` |
| FR-5 Observability fields/counters | `TestNetworkManagerStrictLogIncludesVMAndAnchor`, `TestProxyShutdownCountersReflectDeniedTraffic` |
| FR-6 Compat/backward compatibility | `TestE2ENetworkAllowlist`, SDK `network_allowlist.test.ts`, SDK `secret_injection.test.ts`, `TestE2EDNSModeOpenCompatibilityStrict` |
| NFR-1 Per-VM rule isolation | `TestStrictMultiVMConcurrentIsolation`, `TestE2EStrictMultiVMConcurrentIsolation` |
| NFR-2 No cross-VM policy leaks | `TestStrictStopOneVMKeepsOtherActive`, `TestE2EStrictStopOneVMOtherContinues` |
| NFR-3 Cleanup idempotence | `TestNetworkManagerStrictCloseIdempotent`, `TestStrictRepeatedStartStopNoAnchorLeak` |
| NFR-4 Partial failure robustness | `TestNewVMStrictRollbackClosesTransportOnBackendError`, `TestStrictResidueCleanupPrepareBeforeFactory` |
