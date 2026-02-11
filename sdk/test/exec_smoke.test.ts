import { test } from "node:test";
import assert from "node:assert/strict";

import { Sandbox } from "../src/index.ts";
import { launchDaemon, shutdownDaemon } from "./helpers.ts";

test("sdk exec smoke", async () => {
  const { daemon, port } = await launchDaemon();

  try {
    const sandbox = await Sandbox.start({ baseUrl: `http://127.0.0.1:${port}` });
    const result = await sandbox.exec(["uname", "-a"]);
    await sandbox.close();

    const output = new TextDecoder().decode(result.stdout).toLowerCase();
    assert.ok(output.includes("linux") || output.includes("darwin"));
  } finally {
    await shutdownDaemon(daemon);
  }
});
