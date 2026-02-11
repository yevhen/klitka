import { test } from "node:test";
import assert from "node:assert/strict";
import path from "node:path";
import fs from "node:fs/promises";
import os from "node:os";

import { Sandbox } from "../src/index.ts";
import { launchDaemon, shutdownDaemon } from "./helpers.ts";

test("sdk rw mount + memfs root", async () => {
  const { daemon, port } = await launchDaemon();

  try {
    const tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "klitka-mount-"));
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
      "for i in $(seq 1 20); do [ -f /mnt/host/.ready ] && break; sleep 0.1; done; touch /mnt/host/new.txt",
    ]);
    assert.equal(writeResult.exitCode, 0);

    const hostFile = path.join(tempDir, "new.txt");
    await fs.stat(hostFile);

    const memfsName = `memfs-${Date.now()}.txt`;
    const hostTmp = path.join("/tmp", memfsName);
    await fs.rm(hostTmp, { force: true });

    const memfsWrite = await sandbox.exec(["touch", `/tmp/${memfsName}`]);
    assert.equal(memfsWrite.exitCode, 0);

    const hostTmpStat = await fs.stat(hostTmp).then(
      () => true,
      () => false
    );
    assert.equal(hostTmpStat, false);

    const listResult = await sandbox.exec(["ls", "/tmp"]);
    const listOutput = new TextDecoder().decode(listResult.stdout);
    assert.ok(listOutput.includes(memfsName));

    await sandbox.close();
  } finally {
    await shutdownDaemon(daemon);
  }
});
