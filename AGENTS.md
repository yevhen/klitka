# AGENTS.md

Guidance for AI agents working on this repo.

## Project overview
`klitka` is a local sandbox runtime (Go daemon + Go CLI + TS SDK) that runs untrusted code in a microVM. The daemon manages VM lifecycle, virtio‑serial exec, and virtiofs mounts. The SDK/CLI speak the Connect/Protobuf API over a local socket or TCP.

## Repo layout
```
cli/            Go CLI (klitka)
cmd/            daemon entrypoint (klitka-daemon)
daemon/         VM lifecycle + backend logic
guest/          guest image build + sandboxd (Zig)
proto/          Protobuf contract
sdk/            TypeScript SDK
scripts/        bootstrap tooling
spec.md         architecture + slice plan
```

## Development environment
Preferred: **Nix dev shell** (guarantees QEMU/virtiofsd/Go/Node/Zig).

```bash
# One-command bootstrap (installs Nix if missing)
./scripts/bootstrap.sh

# Or enter dev shell
nix develop
# legacy
nix-shell
```

The dev shell includes: QEMU, virtiofsd (Linux), Go 1.22, Node 20, Zig, protoc tools.

## Build guest image
```bash
# Alpine default
./guest/image/build.sh

# Custom base rootfs tarball
BASE_ROOTFS=./rootfs.tar ./guest/image/build.sh
```
Artifacts land in `guest/image/out/`:
- `vmlinuz`
- `initramfs.cpio.gz`

## Run daemon + CLI
```bash
# daemon (TCP)
go run ./cmd/klitka-daemon --tcp 127.0.0.1:0

# exec via CLI
KLITKA_TCP=127.0.0.1:PORT go run ./cli exec -- uname -a

# shell via CLI
KLITKA_TCP=127.0.0.1:PORT go run ./cli shell
```

## SDK usage
```ts
import { Sandbox } from "@klitka/sdk";

const sandbox = await Sandbox.start({ baseUrl: "http://127.0.0.1:PORT" });
const res = await sandbox.exec(["uname", "-a"]);
await sandbox.close();
```

## Key environment variables
Connection:
- `KLITKA_SOCKET`: default UNIX socket path for CLI.
- `KLITKA_TCP`: default TCP address for CLI/SDK.

VM backend:
- `KLITKA_BACKEND`: `vm` | `host` | `auto` (default auto).
- `KLITKA_GUEST_KERNEL`: path to `vmlinuz`.
- `KLITKA_GUEST_INITRD`: path to `initramfs.cpio.gz`.
- `KLITKA_GUEST_APPEND`: extra kernel append args.
- `KLITKA_QEMU`: override QEMU binary path.
- `KLITKA_QEMU_MACHINE`: override QEMU machine type (default: `microvm` on Linux+KVM, fallback: `q35`).
- `KLITKA_FS_BACKEND`: `auto` | `fsrpc` | `virtiofs` (default `auto`: CI/macOS→FS-RPC, Linux→virtiofs when available, else FS-RPC).
- `KLITKA_VIRTIOFSD`: override virtiofsd path.
- `KLITKA_TMPDIR`: base temp dir for VM runtime data.
- `KLITKA_DEBUG_QEMU`: non-empty to log QEMU stdout/stderr.

Debug:
- `KLITKA_DEBUG_CONN=1`: log initial bytes for new connections.
- `KLITKA_DEBUG_HTTP=1`: log HTTP request lines.

## Tests
```bash
# Go tests
make test-go
# SDK tests
make test-sdk
# Full suite (build guest + tests)
make test
# Run inside nix develop (preferred/default for agents)
make test-nix
```
Agent rule:
- **Always run test commands via Nix dev shell** (`make test-nix` or `nix develop --command ...`) unless explicitly told otherwise.
- **Never run tests directly on host** (`go test ...`, `npm test`, etc.) outside Nix. Use `nix develop --command ...` for every test invocation.
- Do not assume host toolchain paths (QEMU/virtiofsd/zig/node/go) are available outside Nix.
- If `nix` is unavailable, report it immediately and ask to run `./scripts/bootstrap.sh` (or provide an alternative explicitly).

Notes:
- VM tests require QEMU + guest assets (built via `guest/image/build.sh`).
- If guest files change (`guest/image/init`, `guest/src/*`), rebuild guest assets before VM tests: `nix develop --command ./guest/image/build.sh`.
- Mount tests require `virtiofsd` (skipped in CI unless `KLITKA_ALLOW_VIRTIOFS=1`).

## Code style
- Go: run `gofmt` on modified `.go` files.
- TypeScript: no formatter enforced; keep existing style.
- Avoid adding new dependencies unless required by a slice.

## Commit conventions
Use **imperative, capitalized** messages (no prefixes/scopes). Examples:
- `Add guest image and sandboxd`
- `Add VM exec backend`
- `Harden streaming shell behavior and cleanup tests`
- `Update spec for completed slices`

Keep commits focused and align with the slice plan in `spec.md`.

## Generated artifacts (do not commit)
- `guest/image/out/`
- `guest/image/.cache/`
- `guest/zig-out/`
- `guest/.zig-cache/`
- `sdk/node_modules/`, `sdk/dist/`

## Maintaining the spec
When a slice or major milestone is completed, update `spec.md` checkboxes accordingly.
