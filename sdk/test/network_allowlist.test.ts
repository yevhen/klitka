import { test } from "node:test";
import assert from "node:assert/strict";

import { Sandbox } from "../src/index.ts";
import { launchDaemon, shutdownDaemon } from "./helpers.ts";

test("sdk network allowlist", async () => {
  const { daemon, port } = await launchDaemon();

  try {
    const allowedHost = "example.com";
    const allowedUrl = "https://example.com";

    const sandbox = await Sandbox.start({
      baseUrl: `http://127.0.0.1:${port}`,
      network: { allowHosts: [allowedHost], blockPrivateRanges: false },
    });

    const allowed = await sandbox.exec(["curl", "-fsS", allowedUrl]);
    const allowedOutput = new TextDecoder().decode(allowed.stdout);
    assert.equal(allowed.exitCode, 0);
    assert.ok(allowedOutput.includes("Example Domain"));

    const blocked = await sandbox.exec(["curl", "-fsS", "https://example.org"]);
    assert.notEqual(blocked.exitCode, 0);

    await sandbox.close();
  } finally {
    await shutdownDaemon(daemon);
  }
});
