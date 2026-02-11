import { test } from "node:test";
import assert from "node:assert/strict";

import { Sandbox } from "../src/index.ts";
import { launchDaemon, shutdownDaemon } from "./helpers.ts";

function concatChunks(chunks: Uint8Array[]) {
  const total = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
  const buffer = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    buffer.set(chunk, offset);
    offset += chunk.length;
  }
  return buffer;
}

test("sdk shell smoke", async () => {
  const { daemon, port } = await launchDaemon();

  try {
    const sandbox = await Sandbox.start({ baseUrl: `http://127.0.0.1:${port}` });
    const session = sandbox.shell(["sh"]);
    session.resize(40, 120);

    const outputChunks: Uint8Array[] = [];
    const outputTask = (async () => {
      for await (const output of session.output) {
        outputChunks.push(output.data);
      }
    })();

    const encoder = new TextEncoder();
    session.write(encoder.encode("echo hi\r\n"));
    session.write(encoder.encode("exit\r\n"));
    session.end();

    const exitCode = await withTimeout(session.exit, 5000, "shell exit timeout");
    await withTimeout(outputTask, 5000, "shell output timeout");

    const output = new TextDecoder().decode(concatChunks(outputChunks)).toLowerCase();
    assert.ok(output.includes("hi"));
    assert.equal(exitCode, 0);

    await sandbox.close();
  } finally {
    await shutdownDaemon(daemon);
  }
});

async function withTimeout<T>(promise: Promise<T>, ms: number, message: string): Promise<T> {
  let timer: NodeJS.Timeout | null = null;
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_, reject) => {
        timer = setTimeout(() => reject(new Error(message)), ms);
      }),
    ]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}
