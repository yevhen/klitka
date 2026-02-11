import { test } from "node:test";
import assert from "node:assert/strict";
import path from "node:path";
import fs from "node:fs/promises";
import os from "node:os";

import { Sandbox } from "../src/index.ts";
import { launchDaemon, shutdownDaemon } from "./helpers.ts";

test("sdk rw mount with fsrpc", async () => {
  const { daemon, port } = await launchDaemon({ KLITKA_FS_BACKEND: "fsrpc" });

  try {
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
