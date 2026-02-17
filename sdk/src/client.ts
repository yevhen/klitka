import { createPromiseClient, type PromiseClient } from "@connectrpc/connect";
import { createConnectTransport, Http2SessionManager } from "@connectrpc/connect-node";

import { DaemonService } from "./gen/klitka/v1/daemon_connect";
import {
  DNSMode,
  EgressMode,
  ExecInput,
  ExecRequest,
  ExecStart,
  ExecStreamRequest,
  Mount,
  MountMode,
  NetworkPolicy,
  PtyResize,
  Secret,
  SecretFormat,
  StartVMRequest,
  StopVMRequest,
} from "./gen/klitka/v1/daemon_pb";

export type MountConfig = {
  hostPath: string;
  guestPath: string;
  mode?: "ro" | "rw";
};

export type FileSystemConfig = {
  mounts?: MountConfig[];
};

export type NetworkConfig = {
  allowHosts?: string[];
  denyHosts?: string[];
  blockPrivateRanges?: boolean;
  egressMode?: "compat" | "strict";
  dnsMode?: "open" | "trusted" | "synthetic";
  trustedDnsServers?: string[];
};

export type SecretConfig = {
  hosts: string[];
  value: string;
  header?: string;
  format?: "bearer" | "raw";
};

export type SandboxOptions = {
  baseUrl?: string;
  fs?: FileSystemConfig;
  network?: NetworkConfig;
  secrets?: Record<string, SecretConfig>;
};

export type ExecResult = {
  exitCode: number;
  stdout: Uint8Array;
  stderr: Uint8Array;
};

export type ShellOutput = {
  stream: "stdout" | "stderr";
  data: Uint8Array;
};

export class ShellSession {
  readonly output: AsyncIterable<ShellOutput>;
  readonly exit: Promise<number>;

  private readonly requestQueue: AsyncQueue<ExecStreamRequest>;

  constructor(
    output: AsyncIterable<ShellOutput>,
    exit: Promise<number>,
    requestQueue: AsyncQueue<ExecStreamRequest>
  ) {
    this.output = output;
    this.exit = exit;
    this.requestQueue = requestQueue;
  }

  write(data: Uint8Array) {
    if (!data || data.length === 0) return;
    this.requestQueue.push(
      new ExecStreamRequest({
        payload: {
          case: "input",
          value: new ExecInput({ data }),
        },
      })
    );
  }

  resize(rows: number, cols: number) {
    this.requestQueue.push(
      new ExecStreamRequest({
        payload: {
          case: "resize",
          value: new PtyResize({ rows, cols }),
        },
      })
    );
  }

  end() {
    this.requestQueue.push(
      new ExecStreamRequest({
        payload: {
          case: "input",
          value: new ExecInput({ eof: true }),
        },
      })
    );
    this.requestQueue.close();
  }
}

export class Sandbox {
  private vmId: string | null = null;
  private client: PromiseClient<typeof DaemonService>;
  private closeTransport: () => void;

  private constructor(client: PromiseClient<typeof DaemonService>, closeTransport: () => void) {
    this.client = client;
    this.closeTransport = closeTransport;
  }

  static async start(options: SandboxOptions = {}): Promise<Sandbox> {
    const { client, close } = createClient(options);
    const sandbox = new Sandbox(client, close);
    const response = await client.startVM(
      new StartVMRequest({
        mounts: buildMounts(options.fs?.mounts),
        network: buildNetworkPolicy(options.network),
        secrets: buildSecrets(options.secrets),
      })
    );
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

  shell(command: string | string[] = ["sh"]): ShellSession {
    if (!this.vmId) {
      throw new Error("sandbox not started");
    }

    const [cmd, args] = normalizeCommand(command);
    const requestQueue = new AsyncQueue<ExecStreamRequest>();
    requestQueue.push(
      new ExecStreamRequest({
        payload: {
          case: "start",
          value: new ExecStart({
            vmId: this.vmId,
            command: cmd,
            args,
            pty: true,
          }),
        },
      })
    );

    const responses = this.client.execStream(requestQueue);

    const outputQueue = new AsyncQueue<ShellOutput>();
    let exitResolve: (code: number) => void = () => undefined;
    let exitReject: (error: Error) => void = () => undefined;
    let exitSettled = false;

    const exit = new Promise<number>((resolve, reject) => {
      exitResolve = (code) => {
        if (!exitSettled) {
          exitSettled = true;
          resolve(code);
        }
      };
      exitReject = (error) => {
        if (!exitSettled) {
          exitSettled = true;
          reject(error);
        }
      };
    });

    void (async () => {
      try {
        for await (const response of responses) {
          if (response.payload.case === "output") {
            const output = response.payload.value;
            const stream = output.stream === "stderr" ? "stderr" : "stdout";
            outputQueue.push({ stream, data: output.data });
          }
          if (response.payload.case === "exit") {
            exitResolve(response.payload.value.exitCode);
            break;
          }
        }
      } catch (err) {
        exitReject(err instanceof Error ? err : new Error(String(err)));
      } finally {
        outputQueue.close();
        requestQueue.close();
        if (!exitSettled) {
          exitResolve(1);
        }
      }
    })();

    return new ShellSession(outputQueue, exit, requestQueue);
  }

  async close(): Promise<void> {
    if (this.vmId) {
      await this.client.stopVM(new StopVMRequest({ vmId: this.vmId }));
      this.vmId = null;
    }
    this.closeTransport();
  }

  getId(): string | null {
    return this.vmId;
  }
}

class AsyncQueue<T> implements AsyncIterable<T> {
  private queue: T[] = [];
  private resolvers: Array<(value: IteratorResult<T>) => void> = [];
  private closed = false;

  push(item: T) {
    if (this.closed) {
      throw new Error("queue closed");
    }
    const resolver = this.resolvers.shift();
    if (resolver) {
      resolver({ value: item, done: false });
    } else {
      this.queue.push(item);
    }
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    while (this.resolvers.length > 0) {
      const resolver = this.resolvers.shift();
      if (resolver) {
        resolver({ value: undefined as T, done: true });
      }
    }
  }

  [Symbol.asyncIterator](): AsyncIterator<T> {
    return {
      next: () => {
        if (this.queue.length > 0) {
          const value = this.queue.shift() as T;
          return Promise.resolve({ value, done: false });
        }
        if (this.closed) {
          return Promise.resolve({ value: undefined as T, done: true });
        }
        return new Promise<IteratorResult<T>>((resolve) => {
          this.resolvers.push(resolve);
        });
      },
      return: () => {
        this.close();
        return Promise.resolve({ value: undefined as T, done: true });
      },
      throw: (err) => {
        this.close();
        return Promise.reject(err);
      },
    };
  }
}

function normalizeCommand(command: string | string[]): [string, string[]] {
  if (Array.isArray(command)) {
    if (command.length === 0) {
      throw new Error("command array must not be empty");
    }
    return [command[0], command.slice(1)];
  }
  return ["sh", ["-c", command]];
}

function createClient(options: SandboxOptions) {
  const rawBaseUrl = options.baseUrl ?? process.env.KLITKA_TCP;
  if (!rawBaseUrl) {
    throw new Error("baseUrl or KLITKA_TCP must be set for SDK connections");
  }
  const baseUrl = normalizeBaseUrl(rawBaseUrl);
  const sessionManager = new Http2SessionManager(baseUrl, {
    idleConnectionTimeoutMs: 500,
  });
  const transport = createConnectTransport({
    baseUrl,
    httpVersion: "2",
    sessionManager,
  });
  const client = createPromiseClient(DaemonService, transport);
  return {
    client,
    close: () => sessionManager.abort(),
  };
}

function normalizeBaseUrl(baseUrl: string) {
  if (baseUrl.startsWith("http://") || baseUrl.startsWith("https://")) {
    return baseUrl;
  }
  return `http://${baseUrl}`;
}

function buildMounts(mounts?: MountConfig[]): Mount[] {
  if (!mounts || mounts.length === 0) {
    return [];
  }
  return mounts.map((mount) =>
    new Mount({
      hostPath: mount.hostPath,
      guestPath: mount.guestPath,
      mode: mountModeFromConfig(mount.mode),
    })
  );
}

function buildNetworkPolicy(network?: NetworkConfig): NetworkPolicy | undefined {
  if (!network) {
    return undefined;
  }

  const allowHosts = (network.allowHosts ?? []).filter(Boolean);
  const denyHosts = (network.denyHosts ?? []).filter(Boolean);
  const trustedDnsServers = (network.trustedDnsServers ?? []).filter(Boolean);
  const blockPrivateRanges = network.blockPrivateRanges ?? true;
  const egressMode = egressModeFromConfig(network.egressMode);
  const dnsMode = dnsModeFromConfig(network.dnsMode);

  if (dnsMode === DNSMode.DNS_MODE_TRUSTED && trustedDnsServers.length === 0) {
    throw new Error("network.dnsMode=trusted requires at least one trustedDnsServers entry");
  }
  if (dnsMode !== DNSMode.DNS_MODE_TRUSTED && trustedDnsServers.length > 0) {
    throw new Error("network.trustedDnsServers can only be used with network.dnsMode=trusted");
  }

  const hasPolicy =
    allowHosts.length > 0 ||
    denyHosts.length > 0 ||
    trustedDnsServers.length > 0 ||
    network.blockPrivateRanges !== undefined ||
    network.egressMode !== undefined ||
    network.dnsMode !== undefined;

  if (!hasPolicy) {
    return undefined;
  }

  return new NetworkPolicy({
    allowHosts,
    denyHosts,
    blockPrivateRanges,
    egressMode,
    dnsMode,
    trustedDnsServers,
  });
}

function egressModeFromConfig(mode?: NetworkConfig["egressMode"]): EgressMode {
  if (mode === "strict") {
    return EgressMode.STRICT;
  }
  return EgressMode.COMPAT;
}

function dnsModeFromConfig(mode?: NetworkConfig["dnsMode"]): DNSMode {
  if (mode === "trusted") {
    return DNSMode.DNS_MODE_TRUSTED;
  }
  if (mode === "synthetic") {
    return DNSMode.DNS_MODE_SYNTHETIC;
  }
  return DNSMode.DNS_MODE_OPEN;
}

function buildSecrets(secrets?: Record<string, SecretConfig>): Secret[] {
  if (!secrets) {
    return [];
  }
  const entries = Object.entries(secrets);
  if (entries.length === 0) {
    return [];
  }

  return entries.map(([name, config]) => {
    const hosts = (config.hosts ?? []).filter(Boolean);
    if (hosts.length === 0) {
      throw new Error(`secret ${name} requires at least one host`);
    }
    if (!config.value) {
      throw new Error(`secret ${name} requires a value`);
    }
    return new Secret({
      name,
      hosts,
      value: config.value,
      header: config.header ?? "",
      format: secretFormatFromConfig(config.format),
    });
  });
}

function secretFormatFromConfig(format?: SecretConfig["format"]): SecretFormat {
  if (format === "raw") {
    return SecretFormat.RAW;
  }
  if (format === "bearer") {
    return SecretFormat.BEARER;
  }
  return SecretFormat.UNSPECIFIED;
}

function mountModeFromConfig(mode?: MountConfig["mode"]): MountMode {
  if (mode === "rw") {
    return MountMode.RW;
  }
  if (mode === "ro") {
    return MountMode.RO;
  }
  return MountMode.RO;
}
