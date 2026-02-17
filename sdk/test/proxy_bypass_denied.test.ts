import { test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import type { AddressInfo } from "node:net";

import { Sandbox } from "../src/index.ts";
import { launchDaemon, shutdownDaemon } from "./helpers.ts";

test("sdk proxy bypass denied in strict mode", async () => {
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
    const targetUrl = `http://localhost:${address.port}`;

    const sandbox = await Sandbox.start({
      baseUrl: `http://127.0.0.1:${port}`,
      network: {
        allowHosts: ["localhost"],
        blockPrivateRanges: false,
        egressMode: "strict",
      },
    });

    const result = await sandbox.exec([
      "sh",
      "-c",
      `unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy; curl -fsS --max-time 5 ${targetUrl}`,
    ]);

    assert.notEqual(result.exitCode, 0, "direct egress should fail in strict mode");
    await sandbox.close();
  } finally {
    server.close();
    await shutdownDaemon(daemon);
  }
});
