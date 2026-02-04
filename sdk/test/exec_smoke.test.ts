import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import net from "node:net";
import { fileURLToPath } from "node:url";
import path from "node:path";

import { Sandbox } from "../src/index.ts";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..", "..");

async function pickPort(): Promise<number> {
  return await new Promise((resolve, reject) => {
    const server = net.createServer();
    server.on("error", reject);
    server.listen(0, () => {
      const address = server.address();
      if (typeof address === "object" && address) {
        const port = address.port;
        server.close(() => resolve(port));
      } else {
        server.close(() => reject(new Error("failed to allocate port")));
      }
    });
  });
}

test("sdk exec smoke", async () => {
  const port = await pickPort();
  const daemon = spawn("go", ["run", "./cmd/klitkavm-daemon", "--tcp", `127.0.0.1:${port}`], {
    cwd: repoRoot,
    stdio: "pipe",
  });

  await new Promise((resolve) => setTimeout(resolve, 800));

  try {
    const sandbox = await Sandbox.start({ baseUrl: `http://127.0.0.1:${port}` });
    const result = await sandbox.exec(["uname", "-a"]);
    await sandbox.close();

    const output = new TextDecoder().decode(result.stdout).toLowerCase();
    assert.ok(output.includes("linux") || output.includes("darwin"));
  } finally {
    daemon.kill("SIGTERM");
  }
});
