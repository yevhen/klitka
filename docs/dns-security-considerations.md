# DNS security considerations for sandboxed workloads

This document explains why DNS deserves special treatment in sandbox environments,
what classes of risk it introduces, and how `klitka` mitigates those risks with
DNS profiles.

It is intended as a threat-model companion to [`network-enforcement.md`](./network-enforcement.md).

## Why DNS is a separate security topic

Even when HTTP/TLS egress policy is strict, DNS can still become:

1. a **covert exfiltration channel**,
2. a source of **policy ambiguity** (guest resolution vs host enforcement), and
3. an input to **network targeting attacks** (rebinding, internal probing).

DNS is often treated as harmless plumbing, but in untrusted-code scenarios it is
an active attack surface.

## Threat model assumptions

Assume guest code may be adversarial and can:

- unset proxy env vars,
- attempt direct sockets,
- issue arbitrary DNS lookups,
- encode data into queried hostnames,
- probe internal/private destinations.

Security goals:

- avoid unintended egress paths,
- keep policy decisions deterministic,
- reduce data exfiltration opportunities,
- preserve compatibility where explicitly requested.

For the broader network threat model see the
[threat model section](./network-enforcement.md#threat-model) in `network-enforcement.md`.

## DNS-specific risks

### Data exfiltration via query names

A process can encode secrets in subdomains:

- `part1.<secret>.attacker.tld`
- `part2.<secret>.attacker.tld`

An attacker-controlled authoritative resolver receives query names and reconstructs
payloads. This works even if normal HTTP destinations are tightly allowlisted.
DNS payload bandwidth is low but usually enough for credentials and tokens.

DNS can also be abused as a generic command-and-control transport
(request/response encoding in labels, TXT records, etc.), turning the resolver
path into a bidirectional tunnel.

### Resolver mismatch and policy ambiguity

If guest and host resolve differently (split-horizon, resolver-specific answers,
cache timing), policy can become non-deterministic:

- guest thinks target resolves one way,
- host enforces against another view.

### DNS rebinding

Hostnames can rapidly change resolved IPs. Without strict host-side checks and
private-range protections, this can become a path into internal address space.

### Infrastructure metadata leakage

DNS behavior itself can leak environmental information: resolver topology,
search domains, timing, and naming conventions. Even failed lookups reveal
what the guest is trying to reach.

## What DNS profiles are for

`klitka` provides `dns_mode` to make trade-offs explicit and auditable.

- `open`: compatibility-first.
- `trusted`: constrained to configured trusted resolvers.
- `synthetic`: blocks external DNS lookups in proxy resolver path.

This is not just convenience; it makes security posture a deliberate setting
rather than an accidental property of the host environment.

## Security properties by mode

### `open`

Best compatibility, weakest DNS constraints.

Use when:

- migrating legacy workloads,
- strict DNS controls are not yet feasible.

Risk profile:

- highest DNS exfil/tunneling exposure,
- strongest dependence on surrounding egress enforcement.

### `trusted`

DNS is pinned to `trusted_dns_servers[]`.

Use when:

- you need real DNS,
- but want deterministic resolver control.

Risk profile:

- reduced resolver ambiguity,
- still susceptible to DNS-level exfil if allowed by workload behavior.

### `synthetic`

External DNS lookups are blocked in the proxy resolver path.

Use when:

- highest DNS minimization is desired,
- workload can operate without external DNS resolution.

Risk profile:

- strongest DNS-channel reduction,
- potentially lower compatibility.

### Quick reference

| Scenario | `dns_mode` | Notes |
|---|---|---|
| Local dev / compatibility first | `open` | Pair with allow/deny lists |
| Production with standard internet needs | `trusted` | Pin resolvers explicitly |
| High-assurance agent workloads | `synthetic` | Combine with `egress_mode=strict` |

## Relationship to egress enforcement

DNS profiles reduce DNS attack surface, but they do **not** by themselves guarantee
that no alternate transport path exists.

A hardened deployment still requires strict transport-level mediation (host-enforced
path, no bypass around policy plane).

In other words:

- DNS controls are **defense-in-depth**,
- transport mediation is the **primary boundary**.

## Practical deployment guidance

Recommended progression:

1. Start with explicit allow/deny host policy.
2. Move from `dns_mode=open` to `trusted` where possible.
3. Use `synthetic` in high-security workloads that do not require external DNS.
4. Enable strict egress mode and keep bypass regression tests mandatory.

For observability counters and test coverage, see
[`network-enforcement.md`](./network-enforcement.md#observability).

## Summary

DNS is not just name resolution in sandbox security; it is a policy and exfiltration
surface.

`klitka` DNS profiles exist to make this surface explicit, configurable, and
observable. The strongest posture is achieved when DNS profiles are combined with
strict transport-level egress mediation.
