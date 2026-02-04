import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

import { Sandbox } from "../src/index.ts";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

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

test("sdk exec smoke", async () => {
  const daemon = spawn("go", ["run", "./cmd/klitkavm-daemon", "--tcp", "127.0.0.1:0"], {
    cwd: repoRoot,
    stdio: "pipe",
    detached: true,
  });

  try {
    const port = await waitForDaemon(daemon);

    const sandbox = await Sandbox.start({ baseUrl: `http://127.0.0.1:${port}` });
    const result = await sandbox.exec(["uname", "-a"]);
    await sandbox.close();

    const output = new TextDecoder().decode(result.stdout).toLowerCase();
    assert.ok(output.includes("linux") || output.includes("darwin"));
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
