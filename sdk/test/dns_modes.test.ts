import { test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import dgram from "node:dgram";
import type { AddressInfo } from "node:net";

import { Sandbox } from "../src/index.ts";
import { launchDaemon, shutdownDaemon } from "./helpers.ts";

test("sdk dns synthetic blocks exfil", async () => {
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
    const domain = "exfil.test";

    const sandbox = await Sandbox.start({
      baseUrl: `http://127.0.0.1:${port}`,
      network: {
        allowHosts: [domain],
        blockPrivateRanges: false,
        egressMode: "strict",
        dnsMode: "synthetic",
      },
    });

    const result = await sandbox.exec(["curl", "-fsS", `http://${domain}:${address.port}`]);
    assert.notEqual(result.exitCode, 0, "synthetic DNS mode should block domain lookup");

    await sandbox.close();
  } finally {
    server.close();
    await shutdownDaemon(daemon);
  }
});

test("sdk dns trusted uses allowed resolvers", async () => {
  const { daemon, port } = await launchDaemon();

  const server = http.createServer((_req, res) => {
    res.writeHead(200, { "content-type": "text/plain" });
    res.end("ok");
  });

  await new Promise<void>((resolve) => {
    server.listen(0, "127.0.0.1", () => resolve());
  });

  const domain = "trusted.test";
  const dns = await startDnsServer({ [domain]: "127.0.0.1" });

  try {
    const address = server.address() as AddressInfo;

    const sandbox = await Sandbox.start({
      baseUrl: `http://127.0.0.1:${port}`,
      network: {
        allowHosts: [domain],
        blockPrivateRanges: false,
        egressMode: "strict",
        dnsMode: "trusted",
        trustedDnsServers: [dns.addr],
      },
    });

    const result = await sandbox.exec(["curl", "-fsS", `http://${domain}:${address.port}`]);
    const output = new TextDecoder().decode(result.stdout);

    assert.equal(result.exitCode, 0, `trusted DNS request failed: ${output}`);
    assert.ok(output.includes("ok"));
    assert.ok(dns.queries.some((query) => query === domain), "trusted resolver did not receive query");

    await sandbox.close();
  } finally {
    await dns.close();
    server.close();
    await shutdownDaemon(daemon);
  }
});

type DnsServer = {
  addr: string;
  queries: string[];
  close: () => Promise<void>;
};

async function startDnsServer(records: Record<string, string>): Promise<DnsServer> {
  const normalized: Record<string, Buffer> = {};
  for (const [name, value] of Object.entries(records)) {
    const parts = value.split(".").map((part) => Number(part));
    if (parts.length !== 4 || parts.some((part) => Number.isNaN(part) || part < 0 || part > 255)) {
      throw new Error(`invalid IPv4 record for ${name}: ${value}`);
    }
    normalized[normalizeHost(name)] = Buffer.from(parts);
  }

  const socket = dgram.createSocket("udp4");
  const queries: string[] = [];

  socket.on("message", (msg, rinfo) => {
    const parsed = parseDnsQuestion(msg);
    if (!parsed) {
      return;
    }

    queries.push(parsed.name);
    const answer = normalized[parsed.name];
    const response = buildDnsResponse(msg, parsed, answer);
    socket.send(response, rinfo.port, rinfo.address);
  });

  await new Promise<void>((resolve) => {
    socket.bind(0, "127.0.0.1", () => resolve());
  });

  const info = socket.address() as AddressInfo;
  return {
    addr: `127.0.0.1:${info.port}`,
    queries,
    close: async () => {
      await new Promise<void>((resolve) => {
        socket.close(() => resolve());
      });
    },
  };
}

function parseDnsQuestion(message: Buffer): { name: string; qtype: number; qclass: number; questionEnd: number } | null {
  if (message.length < 12) return null;
  const qdCount = message.readUInt16BE(4);
  if (qdCount < 1) return null;

  let offset = 12;
  const labels: string[] = [];

  while (true) {
    if (offset >= message.length) return null;
    const labelLen = message[offset];
    offset += 1;
    if (labelLen === 0) {
      break;
    }
    if ((labelLen & 0xc0) !== 0) {
      return null;
    }
    if (offset + labelLen > message.length) {
      return null;
    }
    labels.push(message.subarray(offset, offset + labelLen).toString("utf8").toLowerCase());
    offset += labelLen;
  }

  if (offset + 4 > message.length) return null;
  const qtype = message.readUInt16BE(offset);
  const qclass = message.readUInt16BE(offset + 2);
  offset += 4;

  return {
    name: labels.join("."),
    qtype,
    qclass,
    questionEnd: offset,
  };
}

function buildDnsResponse(
  query: Buffer,
  parsed: { name: string; qtype: number; qclass: number; questionEnd: number },
  answer?: Buffer
): Buffer {
  const question = query.subarray(12, parsed.questionEnd);

  // Response + recursion available.
  let flags = 0x8180;
  let answerCount = 0;
  let answerRecord = Buffer.alloc(0);

  if (parsed.qclass === 1 && parsed.qtype === 1 && answer) {
    answerCount = 1;
    answerRecord = Buffer.from([
      0xc0,
      0x0c, // name pointer
      0x00,
      0x01, // A
      0x00,
      0x01, // IN
      0x00,
      0x00,
      0x00,
      0x3c, // ttl
      0x00,
      0x04,
      ...answer,
    ]);
  }

  if (parsed.qtype === 1 && !answer) {
    flags = 0x8183; // NXDOMAIN for missing A
  }

  const header = Buffer.alloc(12);
  query.copy(header, 0, 0, 2);
  header.writeUInt16BE(flags, 2);
  header.writeUInt16BE(1, 4);
  header.writeUInt16BE(answerCount, 6);
  header.writeUInt16BE(0, 8);
  header.writeUInt16BE(0, 10);

  return Buffer.concat([header, question, answerRecord]);
}

function normalizeHost(value: string): string {
  return value.trim().toLowerCase().replace(/\.$/, "");
}
