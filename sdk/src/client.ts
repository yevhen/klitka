import { createPromiseClient, type PromiseClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";

import { DaemonService } from "./gen/klitkavm/v1/daemon_connect";
import { ExecRequest, StartVMRequest, StopVMRequest } from "./gen/klitkavm/v1/daemon_pb";

export type SandboxOptions = {
  baseUrl?: string;
};

export type ExecResult = {
  exitCode: number;
  stdout: Uint8Array;
  stderr: Uint8Array;
};

export class Sandbox {
  private vmId: string | null = null;
  private client: PromiseClient<typeof DaemonService>;

  private constructor(client: PromiseClient<typeof DaemonService>) {
    this.client = client;
  }

  static async start(options: SandboxOptions = {}): Promise<Sandbox> {
    const client = createClient(options);
    const sandbox = new Sandbox(client);
    const response = await client.startVM(new StartVMRequest({}));
    sandbox.vmId = response.vmId;
    return sandbox;
  }

  async exec(command: string | string[]): Promise<ExecResult> {
    if (!this.vmId) {
      throw new Error("sandbox not started");
    }
    const [cmd, args] = normalizeCommand(command);
    const response = await this.client.exec(
      new ExecRequest({
        vmId: this.vmId,
        command: cmd,
        args,
      })
    );
    return {
      exitCode: response.exitCode,
      stdout: response.stdout,
      stderr: response.stderr,
    };
  }

  async close(): Promise<void> {
    if (!this.vmId) return;
    await this.client.stopVM(new StopVMRequest({ vmId: this.vmId }));
    this.vmId = null;
  }

  getId(): string | null {
    return this.vmId;
  }
}

function normalizeCommand(command: string | string[]): [string, string[]] {
  if (Array.isArray(command)) {
    if (command.length === 0) {
      throw new Error("command array must not be empty");
    }
    return [command[0], command.slice(1)];
  }
  return [command, []];
}

function createClient(options: SandboxOptions) {
  const rawBaseUrl = options.baseUrl ?? process.env.KLITKAVM_TCP;
  if (!rawBaseUrl) {
    throw new Error("baseUrl or KLITKAVM_TCP must be set for SDK connections");
  }
  const baseUrl = normalizeBaseUrl(rawBaseUrl);
  const transport = createConnectTransport({ baseUrl, httpVersion: "1.1" });
  return createPromiseClient(DaemonService, transport);
}

function normalizeBaseUrl(baseUrl: string) {
  if (baseUrl.startsWith("http://") || baseUrl.startsWith("https://")) {
    return baseUrl;
  }
  return `http://${baseUrl}`;
}
