# Network enforcement and DNS profiles

This document describes `klitka` network policy behavior for hardened workloads.

## Threat model

Assume untrusted guest code can:
- unset `HTTP_PROXY` / `HTTPS_PROXY`
- attempt direct outbound sockets
- use DNS lookups for data exfiltration
- probe private/internal address space

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

## DNS modes

### `open` (default)

Compatibility resolver behavior.

### `trusted`

Resolver lookups are performed only via `trusted_dns_servers[]`.

Validation rules:
- requires at least one trusted DNS server
- trusted servers are only accepted in `trusted` mode

### `synthetic`

External DNS lookups are blocked in the proxy resolver path.

This reduces DNS-based exfil vectors.

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
4. Enable `egressMode: "strict"` in CI and run e2e/sdk suites.
5. Roll out strict mode in production sandboxes.

## Test coverage

Go e2e:
- `TestE2ENetworkAllowlistStrict`
- `TestE2EBypassProxyDenied`
- `TestE2EDNSModeSyntheticBlocksExfil`
- `TestE2EDNSModeTrustedUsesAllowedResolvers`
- `TestE2ESecretInjectionStrict`

SDK:
- `network_allowlist_strict.test.ts`
- `proxy_bypass_denied.test.ts`
- `dns_modes.test.ts`
- `secret_injection_strict.test.ts`
