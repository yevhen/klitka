# Contributing to klitka

This repo ships a Nix dev shell with all required system dependencies to build and test the project.

## Development environment (Nix)

### One-command bootstrap
```bash
./scripts/bootstrap.sh
```

This installs Nix (if missing) and runs the full test suite via `make test-nix`.

macOS note: for a more robust Nix install, the Determinate Systems package is recommended:
https://dtr.mn/determinate-nix

### Manual setup
The dev shell includes:
- QEMU (virtiofsd on Linux)
- Go 1.22
- Node.js 20
- Zig
- Protobuf tools (`protoc`, `protoc-gen-go`)
- Build utilities (curl, python3, cpio, gzip)

To enter the shell:

```bash
# Flakes
nix develop

# Or legacy
nix-shell
```

If you need Connect/TS proto plugins:
```bash
go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
npm i -D @bufbuild/protoc-gen-es @connectrpc/protoc-gen-connect-es
```

## Common tasks

### Build guest image
```bash
# Alpine (default)
./guest/image/build.sh

# Custom base rootfs tarball
BASE_ROOTFS=./rootfs.tar ./guest/image/build.sh
```

Or via Make:
```bash
make guest
make guest-base BASE_ROOTFS=./rootfs.tar
```

Artifacts land in `guest/image/out/`:
- `vmlinuz`
- `initramfs.cpio.gz`

### Run tests
```bash
# Go tests (requires QEMU + virtiofsd in PATH)
go test ./...

# SDK tests
cd sdk && npm test
```

Or via Make:
```bash
make test
```

Nix-based test run:
```bash
make test-nix
```

Note: if `flake.nix` is not tracked in git (e.g. uncommitted changes), `make test-nix` falls back to `nix-shell`.

Bootstrap (installs Nix if missing + runs tests):
```bash
./scripts/bootstrap.sh
```

## CI note
The Nix dev shell can be used in CI to guarantee QEMU/virtiofsd availability:
```bash
nix develop --command bash -c "go test ./... && (cd sdk && npm test)"
```
