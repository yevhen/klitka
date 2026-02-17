import { test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import type { AddressInfo } from "node:net";

import { Sandbox } from "../src/index.ts";
import { launchDaemon, shutdownDaemon } from "./helpers.ts";

test("sdk network allowlist strict", async () => {
  const { daemon, port } = await launchDaemon();

  const server = http.createServer((_req, res) => {
    res.writeHead(200, { "content-type": "text/plain" });
    res.end("ok");
  });

  await new Promise<void>((resolve) => {
    server.listen(0, "127.0.0.1", () => resolve());
  });

  try {
    const address = server.address() as AddressInfo;
    const allowedHost = "localhost";
    const allowedUrl = `http://localhost:${address.port}`;
    const blockedUrl = `http://127.0.0.1:${address.port}`;

    const sandbox = await Sandbox.start({
      baseUrl: `http://127.0.0.1:${port}`,
      network: {
        allowHosts: [allowedHost],
        blockPrivateRanges: false,
        egressMode: "strict",
      },
    });

    const allowed = await sandbox.exec(["curl", "-fsS", allowedUrl]);
    const allowedOutput = new TextDecoder().decode(allowed.stdout);
    assert.equal(allowed.exitCode, 0);
    assert.ok(allowedOutput.includes("ok"));

    const blocked = await sandbox.exec(["curl", "-fsS", blockedUrl]);
    assert.notEqual(blocked.exitCode, 0);

    await sandbox.close();
  } finally {
    server.close();
    await shutdownDaemon(daemon);
  }
});
