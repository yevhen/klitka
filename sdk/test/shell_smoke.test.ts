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
      const match = message.match(/daemon listening on ([\d.:]+)/);
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
  const daemon = spawn("go", ["run", "./cmd/klitkavm-daemon", "--tcp", "127.0.0.1:0"], {
    cwd: repoRoot,
    stdio: "pipe",
    detached: true,
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
    session.write(encoder.encode("echo hi\n"));
    session.write(encoder.encode("exit\n"));
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
  if (proc.pid) {
    process.kill(-proc.pid, "SIGTERM");
  } else {
    proc.kill("SIGTERM");
  }
  const exited = await waitForExit(proc, 2000);
  if (!exited) {
    if (proc.pid) {
      process.kill(-proc.pid, "SIGKILL");
    } else {
      proc.kill("SIGKILL");
    }
    await waitForExit(proc, 2000);
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
