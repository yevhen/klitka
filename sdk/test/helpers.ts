import { execFile, spawn } from "node:child_process";
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
  } catch {
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

export async function launchDaemon(extraEnv: NodeJS.ProcessEnv = {}) {
  const daemonEnv = {
    ...(await buildDaemonEnv()),
    ...extraEnv,
  };

  const daemon = spawn("go", ["run", "./cmd/klitka-daemon", "--tcp", "127.0.0.1:0"], {
    cwd: repoRoot,
    stdio: "pipe",
    detached: true,
    env: daemonEnv,
  });

  const port = await waitForDaemon(daemon);
  return { daemon, port };
}

export async function waitForDaemon(proc: ReturnType<typeof spawn>, timeoutMs = 15000): Promise<number> {
  const deadline = Date.now() + timeoutMs;
  let output = "";

  return await new Promise((resolve, reject) => {
    const onExit = (code: number | null) => {
      reject(new Error(`daemon exited early (code: ${code ?? "unknown"})\n${output}`));
    };

    const onData = (chunk: Buffer) => {
      const message = chunk.toString();
      output += message;
      const match = output.match(/daemon listening on ([\d.:]+)/);
      if (match) {
        cleanup();
        const addr = match[1];
        const port = Number(addr.split(":").pop());
        resolve(port);
      }
    };

    const timer = setInterval(() => {
      if (Date.now() > deadline) {
        cleanup();
        reject(new Error(`daemon did not start in time\n${output}`));
      }
    }, 100);

    const cleanup = () => {
      clearInterval(timer);
      proc.off("exit", onExit);
      proc.stdout?.off("data", onData);
      proc.stderr?.off("data", onData);
    };

    proc.on("exit", onExit);
    proc.stdout?.on("data", onData);
    proc.stderr?.on("data", onData);
  });
}

export async function shutdownDaemon(proc: ReturnType<typeof spawn>) {
  if (proc.killed) return;
  killProcess(proc, "SIGTERM");
  const exited = await waitForExit(proc, 2000);
  if (!exited) {
    killProcess(proc, "SIGKILL");
    await waitForExit(proc, 2000);
  }
}

function killProcess(proc: ReturnType<typeof spawn>, signal: NodeJS.Signals) {
  if (proc.pid && process.platform !== "win32") {
    process.kill(-proc.pid, signal);
  } else {
    proc.kill(signal);
  }
}

function waitForExit(proc: ReturnType<typeof spawn>, timeoutMs: number): Promise<boolean> {
  return new Promise((resolve) => {
    const timer = setTimeout(() => resolve(false), timeoutMs);
    proc.once("exit", () => {
      clearTimeout(timer);
      resolve(true);
    });
  });
}

async function fileExists(pathname: string): Promise<boolean> {
  try {
    const stats = await fs.stat(pathname);
    return stats.isFile();
  } catch {
    return false;
  }
}
