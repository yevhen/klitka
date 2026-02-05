import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import http from "node:http";
import type { AddressInfo } from "node:net";

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

test("sdk network allowlist", async () => {
  const daemonEnv = await buildDaemonEnv();
  const daemon = spawn("go", ["run", "./cmd/klitkavm-daemon", "--tcp", "127.0.0.1:0"], {
    cwd: repoRoot,
    stdio: "pipe",
    detached: true,
    env: daemonEnv,
  });

  const server = http.createServer((_, res) => {
    res.writeHead(200, { "content-type": "text/plain" });
    res.end("ok");
  });

  await new Promise<void>((resolve) => {
    server.listen(0, "127.0.0.1", () => resolve());
  });

  try {
    const port = await waitForDaemon(daemon);
    const address = server.address() as AddressInfo;
    const allowedUrl = `http://localhost:${address.port}`;

    const sandbox = await Sandbox.start({
      baseUrl: `http://127.0.0.1:${port}`,
      network: { allowHosts: ["localhost"], blockPrivateRanges: false },
    });

    const allowed = await sandbox.exec(["curl", "-fsS", allowedUrl]);
    const allowedOutput = new TextDecoder().decode(allowed.stdout);
    assert.equal(allowed.exitCode, 0);
    assert.ok(allowedOutput.includes("ok"));

    const blocked = await sandbox.exec(["curl", "-fsS", "http://example.com"]);
    assert.notEqual(blocked.exitCode, 0);

    await sandbox.close();
  } finally {
    server.close();
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
