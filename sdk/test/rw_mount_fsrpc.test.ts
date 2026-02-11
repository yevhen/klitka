import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import path from "node:path";
import fs from "node:fs/promises";
import os from "node:os";

import { Sandbox } from "../src/index.ts";
import { buildDaemonEnv, repoRoot } from "./helpers.ts";

async function waitForDaemon(proc: ReturnType<typeof spawn>, timeoutMs = 15000): Promise<number> {
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

test("sdk rw mount with fsrpc", async () => {
  const daemonEnv = await buildDaemonEnv();
  daemonEnv.KLITKA_FS_BACKEND = "fsrpc";

  const daemon = spawn("go", ["run", "./cmd/klitka-daemon", "--tcp", "127.0.0.1:0"], {
    cwd: repoRoot,
    stdio: "pipe",
    detached: true,
    env: daemonEnv,
  });

  try {
    const port = await waitForDaemon(daemon);

    const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "klitka-fsrpc-"));
    const readyFile = path.join(tempDir, ".ready");
    await fs.writeFile(readyFile, "ok");

    const sandbox = await Sandbox.start({
      baseUrl: `http://127.0.0.1:${port}`,
      fs: {
        mounts: [{ hostPath: tempDir, guestPath: "/mnt/host", mode: "rw" }],
      },
    });

    const writeResult = await sandbox.exec([
      "sh",
      "-c",
      "for i in $(seq 1 20); do [ -f /mnt/host/.ready ] && break; sleep 0.1; done; mkdir -p /mnt/host/work && printf hello >/mnt/host/work/file.txt",
    ]);
    assert.equal(writeResult.exitCode, 0);

    const hostFile = path.join(tempDir, "work", "file.txt");
    const hostData = await fs.readFile(hostFile, "utf-8");
    assert.equal(hostData, "hello");

    const removeFile = await sandbox.exec(["rm", "/mnt/host/work/file.txt"]);
    assert.equal(removeFile.exitCode, 0);

    const existsAfterDelete = await fs.stat(path.join(tempDir, "work", "file.txt")).then(
      () => true,
      () => false
    );
    assert.equal(existsAfterDelete, false);

    await sandbox.close();
  } finally {
    await shutdownDaemon(daemon);
  }
});

async function shutdownDaemon(proc: ReturnType<typeof spawn>) {
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
