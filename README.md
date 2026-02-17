# klitka

`klitka` is a programmable sandbox environment for running AI agents, with policy-controlled networking and filesystem access. It was heavily inspired by [Gondolin](https://github.com/earendil-works/gondolin). While Gondolin implements its network and filesystem stacks in JavaScript for maximum flexibility, `klitka` is leveraging established system tools and a client-server architecture:

*   **Daemon-based Control Plane:** A long-lived Go daemon (`klitka-daemon`) manages the VM fleet. This allows multiple SDK clients to connect to the same environments and ensures resources are cleaned up even if the parent application crashes.
*   **Standard System Interfaces:** Instead of a custom network stack, `klitka` uses standard TAP/VMNet interfaces and a high-performance Go-based MITM proxy. 
*   **Virtiofs Support:** Filesystem sharing is handled by `virtiofsd`, providing near-native I/O performance and robust enforcement of read-only/read-write permissions.
*   **Connect Protocol:** All communication between the SDK/CLI and the daemon happens over a Protobuf/Connect API, making it easy to build clients in any language.


## Features

*   **Network Isolation:** Default-deny policy for non-whitelisted traffic. HTTP/HTTPS traffic can be proxied and inspected by the host.
*   **Hardened Egress Mode:** `egressMode: "strict"` forces VM egress through the host mediation path and blocks direct bypass attempts.
*   **DNS Profiles:** `dnsMode: "open" | "trusted" | "synthetic"` controls resolver behavior (compat, pinned resolvers, or DNS-minimized mode).
*   **DNS Guard:** Prevents DNS rebinding attacks by resolving and verifying IP addresses on the host before they reach the guest.
*   **Secret Masking:** Sensitive credentials (like API keys) are injected at the network proxy layer. The guest environment only sees placeholder values, making it harder for untrusted code to exfiltrate real secrets.
*   **Filesystem Boundary:** No host files are accessible to the guest unless explicitly mounted.

## TypeScript SDK Usage

```typescript
import { Sandbox } from "@klitka/sdk";

const vm = await Sandbox.start({
  fs: {
    mounts: [
      { hostPath: "./workspace", guestPath: "/data", mode: "rw" }
    ]
  },
  network: {
    allowHosts: ["api.github.com", "api.openai.com"],
    blockPrivateRanges: true,
    egressMode: "strict",
    dnsMode: "trusted",
    trustedDnsServers: ["1.1.1.1:53"]
  },
  secrets: {
    GITHUB_TOKEN: {
      hosts: ["api.github.com"],
      value: process.env.GITHUB_TOKEN!
    }
  }
});

// Execute a command and buffer the result
const { exitCode, stdout } = await vm.exec("ls -la /data");

// Or use a streaming session for interactive work
const session = await vm.shell();
session.write(new TextEncoder().encode("echo 'hello'\n"));

await vm.close();
```

## Network enforcement and DNS profiles

`network.egressMode`:
- `"compat"` (default): backwards-compatible mode. Proxy env vars are injected, but guest code can still bypass them.
- `"strict"`: hardened mode. VM networking is restricted to the host-enforced path.

`network.dnsMode`:
- `"open"` (default): system resolver behavior (compat).
- `"trusted"`: resolver lookups only via `trustedDnsServers`.
- `"synthetic"`: no external DNS lookups from the enforcement proxy (limits DNS-based exfil).

Migration note: start with `egressMode: "compat"` for legacy workloads, then switch to `"strict"` after validating outbound dependencies and DNS requirements.

Detailed policy semantics and migration checklist: [docs/network-enforcement.md](docs/network-enforcement.md).

## Key Components

### klitka-daemon (Go)
The core service that manages VM instances (QEMU), wires up networking via a local MITM proxy, and configures `virtiofsd` for filesystem access. It exposes a ConnectRPC interface over a local Unix socket or TCP.

### klitka CLI (Go)
A thin wrapper around the daemon's API that allows starting, stopping, and executing commands in sandboxes from the terminal.

### @klitka/sdk (TypeScript)
A high-level client library for Node.js applications. It handles the connection to the daemon and provides an ergonomic API for sandbox lifecycle management.

### sandboxd (Zig)
A minimal agent running inside the guest microVM that handles process execution and I/O over `virtio-serial`.

## Platform Support

*   **macOS:** QEMU with Hypervisor.framework (HVF).
*   **Linux:** QEMU with KVM.
*   **Windows:** Integration via WSL2 (the daemon runs inside a WSL2 distribution, while the Windows CLI/SDK connects to it over a local TCP port).

## Linux filesystem backend (virtiofsd vs FS-RPC)

On Linux, `klitka` supports two mount backends:

- `virtiofs` (native performance path, requires `virtiofsd` on the host)
- `fsrpc` (portable fallback, no `virtiofsd` required)

Default behavior is `KLITKA_FS_BACKEND=auto`:

- if `virtiofsd` is available, `auto` uses `virtiofs`
- otherwise, `auto` falls back to `fsrpc`

If you want the native virtiofs path, install `virtiofsd` in your Linux environment and ensure it is discoverable (`command -v virtiofsd`).
If your distro installs it in a non-standard path, set:

```bash
KLITKA_VIRTIOFSD=/path/to/virtiofsd
```

You can also force backend selection explicitly:

```bash
KLITKA_FS_BACKEND=virtiofs   # require virtiofsd
KLITKA_FS_BACKEND=fsrpc      # force FS-RPC
```

## Development

`klitka` uses Nix to manage its development environment. See [CONTRIBUTING.md](CONTRIBUTING.md) for details on building the guest image, running tests, and project conventions.
