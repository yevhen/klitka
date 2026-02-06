import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";

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

function concatChunks(chunks: Uint8Array[]) {
  const total = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
  const buffer = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    buffer.set(chunk, offset);
    offset += chunk.length;
  }
  return buffer;
}

test("sdk shell smoke", async () => {
  const daemonEnv = await buildDaemonEnv();
  const daemon = spawn("go", ["run", "./cmd/klitka-daemon", "--tcp", "127.0.0.1:0"], {
    cwd: repoRoot,
    stdio: "pipe",
    detached: true,
    env: daemonEnv,
  });

  try {
    const port = await waitForDaemon(daemon);
    const sandbox = await Sandbox.start({ baseUrl: `http://127.0.0.1:${port}` });
    const session = sandbox.shell(["sh"]);
    session.resize(40, 120);

    const outputChunks: Uint8Array[] = [];
    const outputTask = (async () => {
      for await (const output of session.output) {
        outputChunks.push(output.data);
      }
    })();

    const encoder = new TextEncoder();
    session.write(encoder.encode("echo hi\r\n"));
    session.write(encoder.encode("exit\r\n"));
    session.end();

    const exitCode = await withTimeout(session.exit, 5000, "shell exit timeout");
    await withTimeout(outputTask, 5000, "shell output timeout");

    const output = new TextDecoder().decode(concatChunks(outputChunks)).toLowerCase();
    assert.ok(output.includes("hi"));
    assert.equal(exitCode, 0);

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

async function withTimeout<T>(promise: Promise<T>, ms: number, message: string): Promise<T> {
  let timer: NodeJS.Timeout | null = null;
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_, reject) => {
        timer = setTimeout(() => reject(new Error(message)), ms);
      }),
    ]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}
