# Gondolin Guest Sandbox

This directory contains the guest-side components for the Gondolin sandbox: the
Zig `sandboxd` supervisor, the Alpine initramfs image builder, and helper
tooling to boot the micro-VM under QEMU.

## What it does

- Builds `sandboxd`, a tiny supervisor that listens on a virtio-serial port for
  exec requests, spawns processes inside the guest, and streams
  stdout/stderr/stdin over the wire.
- Assembles a minimal Alpine initramfs with `sandboxd`, an init script, and
  optional packages for networking and certificates.

## Layout

- `src/` — Zig sources for `sandboxd` and the virtio-serial RPC handling.
- `image/` — initramfs build scripts and the minimal `/init`.
- `build.zig` — Zig build definition for `sandboxd`.
- `Makefile` — helpers to build, create images, and run QEMU.

## Common tasks

Mandatory build command (builds the initramfs image and kernel without booting):

```sh
make build
```

Build `sandboxd` only:

```sh
make build-bins
```

Create the Alpine initramfs image:

```sh
make image
```

Fetch the Alpine kernel and boot the guest under QEMU:

```sh
make qemu
```

The QEMU target creates a virtio-serial socket at `image/out/virtio.sock` for
the host controller to connect.
