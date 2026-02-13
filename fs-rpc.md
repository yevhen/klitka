# FS-RPC transition plan (klitka)

## Goals
- Provide **filesystem parity on macOS and Linux** without relying on `virtiofsd`.
- Keep **mount semantics identical** for CLI/SDK (`host → guest` paths, RO/RW modes).
- Preserve **strong isolation**: guest never sees host paths, only a controlled RPC surface.
- Minimize moving parts in CI (avoid `virtiofsd` hangs) while keeping local performance options.

Non-goals (for initial rollout):
- Advanced caching, batching, or opportunistic prefetching.
- Symlink semantics beyond strict safe defaults.

## Current state
- FS backend selector is implemented (`KLITKA_FS_BACKEND=auto|fsrpc|virtiofs`).
- `auto` resolves to:
  - macOS / CI: `fsrpc`
  - Linux with `virtiofsd`: `virtiofs`
  - fallback: `fsrpc`
- Guest ships **sandboxfs** + **fs_rpc**:
  - `guest/src/sandboxfs.zig`: FUSE filesystem that talks to host over RPC.
  - `guest/src/fs_rpc.zig`: CBOR frame client for `fs_request` / `fs_response`.
- QEMU mount path supports per-mount FS-RPC virtio-serial channels (`virtio-fs-<idx>`) and virtiofs fallback.

## Target architecture
```
Host (daemon)
  ├─ Exec virtio-serial: /dev/virtio-ports/virtio-port
  └─ FS-RPC virtio-serial: /dev/virtio-ports/virtio-fs-<n>
        ▲
        │ CBOR frames (fs_request/fs_response)
        ▼
Guest (sandboxfs + FUSE)
  - sandboxfs mounts guest path (e.g. /workspace)
  - sandboxfs forwards FUSE ops over fs_rpc to host
```

### Mount strategy
- **One FS-RPC port per mount** (simplest isolation and namespace mapping).
- QEMU creates a `virtserialport` named `virtio-fs-<idx>` for each mount.
- Guest `init` starts a sandboxfs instance per mount:
  - `sandboxfs --mount <guestPath> --rpc-path /dev/virtio-ports/virtio-fs-<idx> --require-rpc`
  - startup performs `ping` readiness before mounting and waits for `fuse.sandboxfs` to appear in `/proc/mounts`.

### FS backend selector
Introduce `KLITKA_FS_BACKEND` with values:
- `auto` (default):
  - macOS → `fsrpc`
  - Linux + virtiofsd available → `virtiofs`
  - otherwise → `fsrpc`
- `fsrpc`: always FS-RPC
- `virtiofs`: always virtiofsd (Linux only)

## RPC contract (as used by sandboxfs)
CBOR frame structure:
- Request:
  - `t = "fs_request"`
  - `id` (u32)
  - `p = { op, req }`
- Response:
  - `t = "fs_response"`
  - `id`
  - `p = { op, err, res, message }`

Supported ops (already expected by guest):
- `lookup`: `{ parent_ino, name }` → `{ entry }`
- `getattr`: `{ ino }` → `{ attr, attr_ttl_ms? }`
- `readdir`: `{ ino, offset, max_entries }` → `{ entries }`
- `open`: `{ ino, flags }` → `{ fh, open_flags }`
- `read`: `{ fh, offset, size }` → `{ data }`
- `write`: `{ fh, offset, data }` → `{ size }`
- `create`: `{ parent_ino, name, mode, flags }` → `{ entry, fh, open_flags }`
- `mkdir`: `{ parent_ino, name, mode }` → `{ entry }`
- `unlink`: `{ parent_ino, name }` → `{}`
- `rename`: `{ parent_ino, name, new_parent_ino, new_name }` → `{}`
- `truncate`: `{ ino, size }` → `{}`
- `release`: `{ fh }` → `{}`

Response structures:
- `entry`: `{ ino, attr, attr_ttl_ms?, entry_ttl_ms? }`
- `attr`: `{ ino, size, blocks, atime_ms, mtime_ms, ctime_ms, mode, nlink, uid, gid, rdev, blksize }`
- `entries`: array of `{ ino, name, type, offset }`

Error handling:
- `err` is a **Linux errno** integer.
- RO mounts return `EROFS` for mutating ops.

## Security rules
- Normalize and validate paths. Reject:
  - `..`, empty segments, non-normalized paths.
  - absolute paths outside mount root.
- No symlink traversal in initial implementation (return `ELOOP` or `EPERM`).
- Enforce RO/RW at the RPC layer (server‑side).

## Implementation plan (incremental)

### FS-0 — Prep (no behavior change)
**Goal:** Introduce FS backend selector and port naming.
- Add `KLITKA_FS_BACKEND` handling in daemon.
- Add virtio-serial port naming helpers (`virtio-fs-<idx>`).
- Update guest `init` to parse `klitka.fsrpc=` specs (no-op if none).
- DoD: existing tests still pass; no new RPC traffic.

### FS-1 — Read-only FS-RPC
**Goal:** RO mounts via FS-RPC (parity on macOS/Linux).
- Add FS-RPC server in daemon (`daemon/fsrpc`):
  - frame encoding/decoding (reuse CBOR + 4-byte length prefix).
  - per‑mount root with inode mapping.
- QEMU adds virtio-serial ports for mounts when backend = `fsrpc`.
- Guest `init` launches `sandboxfs` per mount with `--rpc-path`.
- Update mount tests to run with `KLITKA_FS_BACKEND=fsrpc`.
- DoD:
  - `e2e_ro_mount` passes on macOS + Linux.
  - SDK `ro_mount` test passes without `virtiofsd`.

### FS-2 — Read/write FS-RPC
**Goal:** RW parity without virtiofsd.
- Implement mutating ops (mkdir/create/write/rename/unlink/truncate).
- Handle file handles and offseted writes safely.
- DoD:
  - `e2e_rw_mount` passes on macOS + Linux.
  - SDK RW mount + memfs tests pass.

### FS-3 — Stability & semantics
**Goal:** Correctness under load and clean shutdown.
- Properly close file handles on release/VM shutdown.
- Implement LRU inode + handle GC on host.
- Add structured errors and logging for RPC failures.
- DoD:
  - Stress test: create/delete tree without leaks.
  - No lingering host FDs after VM stop.

### FS-4 — Default switch + virtiofs fallback
**Goal:** Make FS-RPC the default backend.
- Default `auto` resolves to `fsrpc` on macOS and CI, `virtiofs` on Linux when available.
- Keep `virtiofs` as opt‑in performance path on Linux.
- Update docs + AGENTS.md for FS backend behavior.

## CI / test strategy
- Default CI should **not** require `virtiofsd`.
- Add env toggles:
  - `KLITKA_FS_BACKEND=fsrpc` for all mount tests.
  - `KLITKA_ALLOW_VIRTIOFS=1` to run virtiofs tests on Linux CI when desired.

## Open questions
- ✅ Added `ping` op for lightweight host-side FS-RPC health checks.
- ✅ Added FS-RPC summary metrics in daemon logs (requests/errors/bytes + per-op latency breakdown).
- Do we want a single multi‑mount RPC channel (complex) later, or keep per‑mount channels (simple)?
