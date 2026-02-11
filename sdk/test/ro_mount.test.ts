import { test } from "node:test";
import assert from "node:assert/strict";
import path from "node:path";
import fs from "node:fs/promises";
import os from "node:os";

import { Sandbox } from "../src/index.ts";
import { launchDaemon, shutdownDaemon } from "./helpers.ts";

test("sdk ro mount", async () => {
  const { daemon, port } = await launchDaemon();

  try {
    const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "klitka-mount-"));
    const hostFile = path.join(tempDir, "hello.txt");
    await fs.writeFile(hostFile, "hello\n", "utf8");

    const sandbox = await Sandbox.start({
      baseUrl: `http://127.0.0.1:${port}`,
      fs: {
        mounts: [{ hostPath: tempDir, guestPath: "/mnt/host", mode: "ro" }],
      },
    });

    const readResult = await sandbox.exec([
      "sh",
      "-c",
      "for i in $(seq 1 20); do [ -f /mnt/host/hello.txt ] && break; sleep 0.1; done; cat /mnt/host/hello.txt",
    ]);
    const readOutput = new TextDecoder().decode(readResult.stdout);
    assert.ok(readOutput.includes("hello"));

    const writeResult = await sandbox.exec([
      "sh",
      "-c",
      "for i in $(seq 1 20); do [ -f /mnt/host/hello.txt ] && break; sleep 0.1; done; touch /mnt/host/new.txt",
    ]);
    assert.notEqual(writeResult.exitCode, 0);

    await sandbox.close();
  } finally {
    await shutdownDaemon(daemon);
  }
});
