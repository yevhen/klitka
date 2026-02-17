import { test } from "node:test";
import assert from "node:assert/strict";
import https from "node:https";
import type { AddressInfo } from "node:net";

import { Sandbox } from "../src/index.ts";
import { launchDaemon, shutdownDaemon } from "./helpers.ts";
import { TEST_CERT, TEST_KEY } from "./tls_fixture.ts";

for (const fsBackend of ["auto", "fsrpc"] as const) {
  test(`sdk secret injection strict (fs_backend=${fsBackend})`, async () => {
    const { daemon, port } = await launchDaemon({
      KLITKA_PROXY_INSECURE: "1",
      KLITKA_FS_BACKEND: fsBackend,
    });

    let resolveHeader: (value: string) => void = () => undefined;
    const headerPromise = new Promise<string>((resolve) => {
      resolveHeader = resolve;
    });

    const server = https.createServer({ key: TEST_KEY, cert: TEST_CERT }, (req, res) => {
      resolveHeader(String(req.headers["authorization"] ?? ""));
      res.writeHead(200, { "content-type": "text/plain" });
      res.end("ok");
    });

    await new Promise<void>((resolve) => {
      server.listen(0, "127.0.0.1", () => resolve());
    });

    try {
      const address = server.address() as AddressInfo;
      const secret = "super-secret";

      const sandbox = await Sandbox.start({
        baseUrl: `http://127.0.0.1:${port}`,
        network: {
          allowHosts: ["localhost"],
          blockPrivateRanges: false,
          egressMode: "strict",
        },
        secrets: {
          API_KEY: { hosts: ["localhost"], value: secret },
        },
      });

      const targetUrl = `https://localhost:${address.port}`;
      const result = await sandbox.exec([
        "sh",
        "-c",
        `echo $API_KEY; curl -fsS -H "Authorization: Bearer $API_KEY" ${targetUrl}`,
      ]);

      const output = new TextDecoder().decode(result.stdout);
      assert.equal(result.exitCode, 0, `exec failed (code ${result.exitCode}): ${output}`);
      assert.ok(!output.includes(secret), `secret leaked in output: ${output}`);

      const header = await promiseWithTimeout(headerPromise, 5000);
      assert.equal(header, `Bearer ${secret}`);

      await sandbox.close();
    } finally {
      server.close();
      await shutdownDaemon(daemon);
    }
  });
}

function promiseWithTimeout<T>(promise: Promise<T>, timeoutMs: number): Promise<T> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("timed out waiting for response")), timeoutMs);
    promise
      .then((value) => {
        clearTimeout(timer);
        resolve(value);
      })
      .catch((err) => {
        clearTimeout(timer);
        reject(err);
      });
  });
}
