# klitkavm — SDK + local daemon spec

## 0. Summary
`klitkavm` is a local, cross‑platform sandbox runtime that provides a **simple SDK** and **CLI** while hiding the complexity of running a **microVM** with controlled **filesystem and network policies**. It ships a **local daemon** that manages hypervisor lifecycle, VFS mounts, network proxying, and policy enforcement. The SDK/CLI talk to the daemon over a local IPC channel.

Implementation choices (baseline):
- **SDK:** TypeScript (Node).
- **CLI:** Go (single binary).
- **Daemon:** Go.
- **Contract:** Protobuf with Connect (HTTP/1.1 + HTTP/2).

Key goals:
- Provide a **real Linux sandbox** that matches the environment LLMs are typically trained and reinforced on.
- Enforce **explicit, code‑driven policies** for network and filesystem access.
- Optimize for **fast create/exec/teardown** to support LLM‑style workflows.
- Preserve **consistent behavior across macOS and production Linux**.
- Guarantee **strong tenant isolation** to avoid cross‑account data access.

Non‑goals:
- No native Windows backend (Windows support is via WSL2).
- No cloud service dependency; entirely local.

Contract evolution:
- The Protobuf API starts minimal and **only grows when a slice needs it**.
- Avoid speculative endpoints; new methods are introduced together with the slice that uses them.

---

## 1. Architecture Overview

```
┌──────────────────┐        IPC        ┌────────────────────────┐
│  SDK (TS) / CLI  │  ───────────────▶ │  klitkavm-daemon        │
│     (Go)         │                  │
└──────────────────┘                   │  (local service)        │
                                       ├────────────────────────┤
                                       │ VM Manager             │
                                       │  - QEMU / CloudHV      │
                                       │  - image cache         │
                                       │  - virtio-serial RPC   │
                                       │ FS Manager             │
                                       │  - virtiofsd/FUSE       │
                                       │ Network Manager        │
                                       │  - TAP/VMNET           │
                                       │  - HTTP/TLS proxy      │
                                       │  - DNS guard           │
                                       │ Policy Engine          │
                                       │  - allow/deny           │
                                       │ Secrets + Certs        │
                                       └────────────────────────┘
```

### Main components
1) **SDK (TypeScript)**: Exposes the public API used by app developers.
2) **CLI (Go)**: Uses the same daemon contract directly (no SDK dependency) for `klitkavm exec`, `klitkavm shell`, `klitkavm status`.
3) **Daemon (Go)**: Long‑lived service; manages VM and policy infrastructure.
4) **Guest Image**: Minimal Linux image with `sandboxd` (virtio‑serial exec service).

---

## 2. SDK API Specification

### Package name
`@klitkavm/sdk`

### Public API
```ts
import { Sandbox, type SandboxOptions, type ExecResult } from "@klitkavm/sdk";

const vm = await Sandbox.start({
  fs: {
    mounts: {
      "/": { type: "mem" },
      "/workspace": { type: "host", path: "/Users/me/proj", mode: "rw" },
    },
  },
  network: {
    allowHosts: ["api.github.com", "*.example.com"],
    blockPrivateRanges: true,
  },
  secrets: {
    GITHUB_TOKEN: { hosts: ["api.github.com"], value: process.env.GITHUB_TOKEN! },
  },
  env: { FOO: "bar" },
});

const res: ExecResult = await vm.exec("ls -la /workspace");
await vm.close();
```

### API Types
```ts
type SandboxOptions = {
  image?: string;              // optional image id or path
  cpu?: number;                // vCPU count
  memory?: string;             // e.g. "2G"
  env?: Record<string, string> | string[];
  fs?: FileSystemConfig | null; // null disables VFS
  network?: NetworkConfig | null; // null disables networking
  secrets?: Record<string, SecretConfig>;
  debug?: boolean;
};

type FileSystemConfig = {
  mounts: Record<string, MountConfig>;
  fuseMount?: string;          // default: /data
};

type MountConfig =
  | { type: "mem" }
  | { type: "host"; path: string; mode?: "ro" | "rw" }
  | { type: "volume"; name: string; mode?: "ro" | "rw" };

// network

type NetworkConfig = {
  allowHosts?: string[];       // allowlist with wildcards
  denyHosts?: string[];        // explicit denylist
  blockPrivateRanges?: boolean; // default true
  allowTcp?: boolean;          // default false (only HTTP/TLS)
  allowUdp?: boolean;          // default DNS only
  dnsServers?: string[];       // override (default: 8.8.8.8)
};

type SecretConfig = {
  hosts: string[];
  value: string;
  header?: string;             // default: Authorization
  format?: "bearer" | "raw"; // default: bearer
};

// runtime handle

class Sandbox {
  static start(opts?: SandboxOptions): Promise<Sandbox>;
  exec(cmd: string | string[], opts?: ExecOptions): Promise<ExecResult>;
  shell(opts?: ShellOptions): ExecProcess; // streaming stdout/stderr
  close(): Promise<void>;
  getId(): string;
  getState(): "starting" | "running" | "stopped";
}
```

### Exec Options
```ts
type ExecOptions = {
  cwd?: string;
  env?: Record<string, string> | string[];
  stdin?: boolean | string | Buffer | Readable;
  pty?: boolean;
  signal?: AbortSignal;
};
```

### SDK Behavior
- SDK is a thin client to the daemon using the Protobuf/Connect contract over local IPC/TCP.
- SDK is optional: any client can speak the daemon contract directly.
- All lifecycle state tracked by daemon; SDK is stateless except for handle IDs.
- `exec` returns buffered output; `shell` returns streaming handle (event‑driven).
- `close` tears down VM, VFS, and network resources.

---

## 3. CLI Specification

### Binary
`klitkavm`

### Implementation
- Go binary using the same Protobuf/Connect contract as the daemon.

### Commands
- `klitkavm start [--config path]`: start VM and keep alive; returns VM id.
- `klitkavm exec [--config path] -- <cmd>`: run command, exit with code.
- `klitkavm shell [--config path]`: interactive shell.
- `klitkavm status <id>`: show state and uptime.
- `klitkavm stop <id>`: stop VM.

### CLI Config File
YAML/JSON accepted. Matches `SandboxOptions` schema.

---

## 4. Daemon Specification

### Binary
`klitkavm-daemon`

### IPC
- Unix domain socket on Linux: `/var/run/klitkavm.sock`.
- macOS: `~/Library/Application Support/klitkavm/daemon.sock`.
- Windows (WSL2): daemon exposes a localhost TCP port (e.g. `127.0.0.1:5711`) for the Windows SDK/CLI; inside WSL2 it still uses the Unix socket.
- Protocol: Protobuf with Connect (HTTP/1.1 or HTTP/2) including streaming for stdout/stderr frames.

### Daemon Responsibilities
1) **VM Manager**
   - Select hypervisor: QEMU+KVM (Linux), QEMU+HVF (macOS) OR Cloud Hypervisor if available.
   - Build QEMU command line (virtio‑serial + virtio‑net + virtio‑fs).
   - Manage kernel/initrd images; caching and update logic.
   - Lifecycle: start/stop/restart; handle auto‑restart on crashes.

2) **FS Manager**
   - Start `virtiofsd` with per‑VM mount roots.
   - Convert mount config to host paths.
   - Enforce read‑only by virtiofsd + host permissions.
   - Optional FUSE policy layer for hooks/audit.

3) **Network Manager**
   - Create TAP interface (Linux) or VMNET (macOS) and attach to VM.
   - Force all VM traffic via host proxy (Envoy/mitmproxy).
   - DNS guard (unbound/dnsmasq) with anti‑rebinding checks.
   - Forbid raw TCP/UDP unless explicitly allowed.

4) **Policy Engine**
   - Translate `NetworkConfig` into proxy allow/deny rules.
   - Translate `FileSystemConfig` into virtiofsd + host perms.
   - Apply secret injection and header rewriting.

5) **Secrets & Certs**
   - Generate CA certificate (per‑machine) if not present.
   - Mint leaf certs for MITM per host.
   - Inject CA into guest image at boot (trusted store).

6) **Observability**
   - Log per‑VM: start/stop, exec commands, network allow/deny, DNS rebind blocks.
   - Provide `GET /logs` IPC stream.

---

## 5. Guest Image Specification

- Minimal Alpine/Ubuntu initramfs.
- `sandboxd` daemon listens on virtio‑serial for exec requests.
- Optionally installs CA certificate into guest trust store.
- Provides `/etc/resolv.conf` pointing to host DNS guard.

---

## 6. Policy Model

### Network Policy
- Default deny for non‑HTTP/TLS.
- HTTP/TLS allowlist by hostname.
- Block private ranges by default (RFC1918 + loopback + link‑local).
- DNS rebind protection: host resolves and verifies IP against policy.

### Filesystem Policy
- Explicit mounts only; default root is memfs.
- Each mount has `ro` or `rw` mode.
- Optional hooks for audit (before/after).

### Secret Injection
- Secrets only injected for allowed hosts.
- Injection is done in proxy layer (host), guest never sees real values.

---

## 7. Cross‑Platform Details

### Linux
- Hypervisor: QEMU + KVM.
- Network: TAP + iptables/nftables redirect to proxy.
- DNS: unbound/dnsmasq on localhost.

### macOS
- Hypervisor: QEMU + HVF.
- Network: vmnet.framework (or user‑space socket backend + proxy).
- DNS: local guard running on loopback.

### Windows (WSL2)
- Daemon runs inside a WSL2 distribution (Linux backend).
- Windows SDK/CLI connects via localhost TCP to the WSL2 daemon.
- Hypervisor: QEMU inside WSL2 (TCG by default; use KVM if `/dev/kvm` is available).
- Network: TAP inside WSL2 + host proxy/DNS guard, same policy flow as Linux.
- Filesystem mounts map Windows paths into WSL2 mount points (e.g. `/mnt/c/...`).

---

## 8. Security Considerations
- VM boundary is primary isolation.
- No direct host filesystem access except explicit mounts.
- Proxy prevents direct socket connections to blocked hosts.
- CA stored locally and only used by daemon.
- Daemon runs with least privileges needed to create TAP/vmnet and virtiofsd.

---

## 9. Error Handling
- All IPC calls return typed errors (`code`, `message`, `details`).
- VM crash → daemon signals `state=stopped` and exposes logs.
- Network block → returns HTTP 403 with clear reason.

## 9.1 Future improvements (track for later)
- Replace ad‑hoc console logging with **structured logging** (daemon + CLI) and log levels.
- Decide whether debug connection hooks stay behind env flags or move into structured logs.
- Consider **build tags** (e.g. `-tags debug`) to exclude debug hooks from release binaries.
- Add centralized log collection endpoint and retention policy.

---

## 10. Iterative Implementation Plan (E2E slices)

Each slice delivers a usable, testable end‑to‑end feature. Every increment must run a real VM and exercise the full SDK/CLI → daemon → guest path.

Definition of Done (applies to every slice):
- Automated E2E test(s) added and run in CI.
- CLI and SDK paths are both exercised (unless the slice is platform‑specific).
- Tests assert output + exit code (or explicit failure state).

### Slice 1 — Minimal VM exec (hello‑sandbox)
**Goal:** Boot a VM and run one command via CLI/SDK.
- [x] Repo layout (`sdk/`, `daemon/`, `cli/`, `guest/`, `proto/`).
- [x] Protobuf IDL + Connect server/client.
- [x] Daemon provides a placeholder exec backend (QEMU + guest image integration pending).
- [x] CLI `exec` + SDK `start/exec/close`.
- ✅ Automated test: `e2e_exec_smoke` runs `klitkavm exec -- "uname -a"` and asserts output + exit code.
- ✅ DoD: SDK test `sdk_exec_smoke` runs `start/exec/close` against the daemon.

### Slice 2 — Streaming IO + interactive shell
**Goal:** Full stdout/stderr streaming and PTY shell.
- [x] Stream output frames over Connect.
- [x] CLI `shell` and SDK `shell` (pty, resize).
- ✅ Automated test: `e2e_shell_pty` opens a PTY, sends `echo hi; exit`, asserts streamed output.
- ✅ DoD: PTY resize event tested (change cols/rows and verify no crash).

### Slice 3 — Read‑only host mounts
**Goal:** Mount a host path read‑only into guest.
- [ ] Integrate `virtiofsd` for mount exposure.
- [ ] FS config parsing in daemon.
- [ ] CLI/SDK `fs.mounts` supported (ro).
- ✅ Automated test: `e2e_ro_mount` reads a host file and verifies write fails with EROFS.

### Slice 4 — RW mounts + memfs root
**Goal:** Realistic FS policy controls.
- [ ] memfs root mount.
- [ ] RW host mounts with enforcement.
- ✅ Automated test: `e2e_rw_mount` writes to RW mount, fails on RO, and verifies memfs root is writable.

### Slice 5 — Network allowlist + DNS guard
**Goal:** Controlled HTTP/TLS egress.
- [ ] Host proxy (Envoy or mitmproxy) wired into VM network.
- [ ] DNS guard + rebind protection.
- [ ] Allow/deny host policy in daemon.
- ✅ Automated test: `e2e_net_allowlist` hits an allowed host (local test server) and a blocked host; asserts allow/deny behavior.

### Slice 6 — TLS MITM + secret injection
**Goal:** Secure credential injection without guest exposure.
- [ ] CA generation and guest trust install.
- [ ] Per‑host cert minting.
- [ ] Secret injection for allowed hosts only.
- ✅ Automated test: `e2e_secret_injection` verifies injected header at allowed host and placeholder‑only env inside guest.

### Slice 7 — WSL2 bootstrap
**Goal:** Windows support via WSL2.
- [ ] Windows CLI bootstraps WSL2 distro and daemon.
- [ ] TCP IPC to WSL2 daemon from Windows SDK/CLI.
- ✅ Automated test: `e2e_wsl2_smoke` runs `klitkavm.exe exec -- "uname -a"` on Windows runners.

### Slice 8 — Packaging + smoke tests
**Goal:** Ship‑ready developer experience.
- [ ] Homebrew + Linux package artifacts.
- [ ] Cross‑platform smoke tests (macOS/Linux/Windows‑WSL2).
- ✅ Automated test: install + run `exec` + `shell` on all platforms.

---

## 11. Repository Layout (Target)
```
klitkavm/
  sdk/
  cli/
  daemon/
  cmd/
  guest/
  proto/
  docs/
  scripts/
```

---

## 12. Acceptance Criteria
- SDK `start/exec/shell/close` works identically on macOS, Linux, and Windows via WSL2.
- Network allowlist enforced for HTTP/HTTPS.
- DNS rebind protection works.
- Read‑only mounts enforced.
- Secrets injected only for allowed hosts.
- CLI supports `exec` and `shell` with config file.
