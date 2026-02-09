import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import path from "node:path";
import fs from "node:fs/promises";

const execFileAsync = promisify(execFile);

export const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

export async function ensureGuestAssets(): Promise<{ kernel: string; initrd: string }> {
  const outDir = path.join(repoRoot, "guest", "image", "out");
  const kernel = path.join(outDir, "vmlinuz");
  const initrd = path.join(outDir, "initramfs.cpio.gz");

  if (await fileExists(kernel) && await fileExists(initrd)) {
    return { kernel, initrd };
  }

  const buildScript = path.join(repoRoot, "guest", "image", "build.sh");
  await execFileAsync("bash", [buildScript], { cwd: repoRoot, env: process.env });

  if (!await fileExists(kernel) || !await fileExists(initrd)) {
    throw new Error(`guest build did not produce kernel/initrd in ${outDir}`);
  }

  return { kernel, initrd };
}

export async function ensureCommand(name: string): Promise<void> {
  try {
    await execFileAsync("bash", ["-c", `command -v ${name}`]);
  } catch (err) {
    throw new Error(`${name} not found in PATH`);
  }
}

export async function ensureQemu(): Promise<void> {
  const candidate = process.arch === "arm64" ? "qemu-system-aarch64" : "qemu-system-x86_64";
  await ensureCommand(candidate);
}

export async function ensureVirtiofsd(): Promise<boolean> {
  if (process.env.CI && !process.env.KLITKA_ALLOW_VIRTIOFS) {
    return false;
  }
  try {
    await ensureCommand("virtiofsd");
    return true;
  } catch {
    return false;
  }
}

export async function buildDaemonEnv(): Promise<NodeJS.ProcessEnv> {
  const { kernel, initrd } = await ensureGuestAssets();
  await ensureQemu();

  return {
    ...process.env,
    KLITKA_BACKEND: "vm",
    KLITKA_GUEST_KERNEL: kernel,
    KLITKA_GUEST_INITRD: initrd,
    KLITKA_TMPDIR: "/tmp",
  };
}

async function fileExists(pathname: string): Promise<boolean> {
  try {
    const stats = await fs.stat(pathname);
    return stats.isFile();
  } catch {
    return false;
  }
}
