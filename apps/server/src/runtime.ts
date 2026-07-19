import { chmod, lstat, mkdir, realpath } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { isLowercaseDigestPinnedImage } from '@tenkacloud/simulator-contracts/image-reference';
import {
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import { AwsProvider } from '@tenkacloud/simulator-provider-aws';
import { AzureProvider } from '@tenkacloud/simulator-provider-azure';
import { GcpProvider } from '@tenkacloud/simulator-provider-gcp';
import { SakuraProvider } from '@tenkacloud/simulator-provider-sakura';
import {
  DockerWorkloadRunner,
  type WorkloadPolicy,
} from '@tenkacloud/simulator-workload-runner';
import type { Hono } from 'hono';
import { createAuthenticatedSimulatorApp } from './app';
import { LaunchTokenAuthority } from './auth';
import { createHostedSimulatorApp } from './hosted-app';
import {
  createNativeGatewayHandler,
  type NativeGatewayCredentials,
} from './native-app';
import {
  type SsmSessionSocketData,
  SsmSessionStreamGateway,
} from './ssm-session-stream';

const LOOPBACK_HOSTS = new Set(['127.0.0.1', 'localhost', '::1']);
const CONTAINER_HOST = '0.0.0.0';
const CONTROL_CONTAINER = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/;
const MAX_WORKLOAD_IMAGES = 64;

export interface SimulatorRuntimeEnvironment {
  readonly TENKACLOUD_SIMULATOR_AWS_ACCESS_KEY_ID?: string;
  readonly TENKACLOUD_SIMULATOR_AZURE_CREDENTIAL?: string;
  readonly TENKACLOUD_SIMULATOR_CONSOLE_DIR?: string;
  readonly TENKACLOUD_SIMULATOR_CONTAINER_MODE?: string;
  readonly TENKACLOUD_SIMULATOR_GCP_CREDENTIAL?: string;
  readonly TENKACLOUD_SIMULATOR_HOST?: string;
  readonly TENKACLOUD_SIMULATOR_LAUNCH_SECRET?: string;
  readonly TENKACLOUD_SIMULATOR_PORT?: string;
  readonly TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN?: string;
  readonly TENKACLOUD_SIMULATOR_SAKURA_CREDENTIAL?: string;
  readonly TENKACLOUD_SIMULATOR_STATE_DIR?: string;
  readonly TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES?: string;
  readonly TENKACLOUD_SIMULATOR_WORKLOAD_CONTROL_CONTAINER?: string;
  readonly TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MEMORY_BYTES?: string;
  readonly TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MILLI_CPU?: string;
  readonly TENKACLOUD_SIMULATOR_WORKLOAD_MAX_PIDS?: string;
  readonly TENKACLOUD_SIMULATOR_WORKLOAD_PROXY_IMAGE?: string;
}

const WORKLOAD_POLICY_KEYS = [
  'TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES',
  'TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MEMORY_BYTES',
  'TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MILLI_CPU',
  'TENKACLOUD_SIMULATOR_WORKLOAD_MAX_PIDS',
  'TENKACLOUD_SIMULATOR_WORKLOAD_PROXY_IMAGE',
] as const;

type WorkloadPolicyKey = (typeof WORKLOAD_POLICY_KEYS)[number];

function requiredWorkloadPolicyValue(
  environment: SimulatorRuntimeEnvironment,
  key: WorkloadPolicyKey
): string {
  const value = environment[key];
  if (value === undefined || !value) {
    throw new TypeError(
      'all simulator workload policy environment variables are required when one is set'
    );
  }
  return value;
}

function boundedPolicyInteger(
  environment: SimulatorRuntimeEnvironment,
  key: WorkloadPolicyKey,
  maximum: number
): number {
  const value = Number(requiredWorkloadPolicyValue(environment, key));
  if (!Number.isSafeInteger(value) || value < 1 || value > maximum) {
    throw new TypeError(`${key} must be an integer between 1 and ${maximum}`);
  }
  return value;
}

function isStringArray(value: unknown): value is readonly string[] {
  return (
    Array.isArray(value) && value.every((item) => typeof item === 'string')
  );
}

export function workloadPolicy(
  environment: SimulatorRuntimeEnvironment
): WorkloadPolicy | undefined {
  if (WORKLOAD_POLICY_KEYS.every((key) => environment[key] === undefined)) {
    if (
      environment.TENKACLOUD_SIMULATOR_WORKLOAD_CONTROL_CONTAINER !== undefined
    ) {
      throw new TypeError(
        'workload control container requires the complete workload policy'
      );
    }
    return undefined;
  }
  const encodedImages = requiredWorkloadPolicyValue(
    environment,
    'TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES'
  );
  let parsedImages: unknown;
  try {
    parsedImages = JSON.parse(encodedImages);
  } catch {
    throw new TypeError(
      'TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES must be a JSON array'
    );
  }
  if (
    !isStringArray(parsedImages) ||
    parsedImages.length < 1 ||
    parsedImages.length > MAX_WORKLOAD_IMAGES ||
    parsedImages.some((image) => !isLowercaseDigestPinnedImage(image)) ||
    new Set(parsedImages).size !== parsedImages.length
  ) {
    throw new TypeError(
      'TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES must contain unique digest-pinned images'
    );
  }
  const proxyImage = requiredWorkloadPolicyValue(
    environment,
    'TENKACLOUD_SIMULATOR_WORKLOAD_PROXY_IMAGE'
  );
  const allowedImages = new Set(parsedImages);
  if (!allowedImages.has(proxyImage)) {
    throw new TypeError(
      'TENKACLOUD_SIMULATOR_WORKLOAD_PROXY_IMAGE must be in the workload image allowlist'
    );
  }
  const controlContainer =
    environment.TENKACLOUD_SIMULATOR_WORKLOAD_CONTROL_CONTAINER;
  if (
    controlContainer !== undefined &&
    !CONTROL_CONTAINER.test(controlContainer)
  ) {
    throw new TypeError(
      'TENKACLOUD_SIMULATOR_WORKLOAD_CONTROL_CONTAINER must be a bounded Docker container name'
    );
  }
  if (
    environment.TENKACLOUD_SIMULATOR_CONTAINER_MODE === '1' &&
    controlContainer === undefined
  ) {
    throw new TypeError(
      'container mode workload policy requires TENKACLOUD_SIMULATOR_WORKLOAD_CONTROL_CONTAINER'
    );
  }
  return {
    allowedImages,
    proxyImage,
    ...(controlContainer === undefined ? {} : { controlContainer }),
    maxMemoryBytes: boundedPolicyInteger(
      environment,
      'TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MEMORY_BYTES',
      8 * 1024 * 1024 * 1024
    ),
    maxMilliCpu: boundedPolicyInteger(
      environment,
      'TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MILLI_CPU',
      8_000
    ),
    maxPids: boundedPolicyInteger(
      environment,
      'TENKACLOUD_SIMULATOR_WORKLOAD_MAX_PIDS',
      4_096
    ),
  };
}

function nativeCredentials(
  environment: SimulatorRuntimeEnvironment
): NativeGatewayCredentials {
  const awsAccessKeyId = environment.TENKACLOUD_SIMULATOR_AWS_ACCESS_KEY_ID;
  const azureCredential = environment.TENKACLOUD_SIMULATOR_AZURE_CREDENTIAL;
  const gcpCredential = environment.TENKACLOUD_SIMULATOR_GCP_CREDENTIAL;
  const sakuraCredential = environment.TENKACLOUD_SIMULATOR_SAKURA_CREDENTIAL;
  if (
    !awsAccessKeyId ||
    !azureCredential ||
    !gcpCredential ||
    !sakuraCredential
  ) {
    throw new TypeError(
      'all simulator native gateway credentials are required'
    );
  }
  return {
    awsAccessKeyId,
    azureCredential,
    gcpCredential,
    sakuraCredential,
  };
}

export interface SimulatorRuntime {
  readonly app: Hono;
  readonly fetch: (
    request: Request,
    server: Bun.Server<SsmSessionSocketData>
  ) => Response | Promise<Response> | undefined;
  readonly host: string;
  readonly port: number;
  readonly store: SimulationStore;
  readonly websocket: Bun.WebSocketHandler<SsmSessionSocketData>;
  close(): void;
}

function requiredEnvironment(
  environment: SimulatorRuntimeEnvironment,
  key: 'TENKACLOUD_SIMULATOR_LAUNCH_SECRET' | 'TENKACLOUD_SIMULATOR_STATE_DIR'
): string {
  const value = environment[key];
  if (!value) throw new TypeError(`${key} is required`);
  return value;
}

function runtimeHost(environment: SimulatorRuntimeEnvironment): string {
  const host = environment.TENKACLOUD_SIMULATOR_HOST ?? '127.0.0.1';
  const containerMode = environment.TENKACLOUD_SIMULATOR_CONTAINER_MODE;
  if (containerMode !== undefined && containerMode !== '1') {
    throw new TypeError(
      'TENKACLOUD_SIMULATOR_CONTAINER_MODE must be 1 when set'
    );
  }
  if (
    !LOOPBACK_HOSTS.has(host) &&
    !(containerMode === '1' && host === CONTAINER_HOST)
  ) {
    throw new TypeError(
      'TENKACLOUD_SIMULATOR_HOST must be loopback or the explicit container bind'
    );
  }
  return host;
}

function runtimePort(environment: SimulatorRuntimeEnvironment): number {
  const value = environment.TENKACLOUD_SIMULATOR_PORT ?? '7777';
  const port = Number(value);
  if (!Number.isSafeInteger(port) || port < 1 || port > 65_535) {
    throw new TypeError(
      'TENKACLOUD_SIMULATOR_PORT must be an integer between 1 and 65535'
    );
  }
  return port;
}

function launchSecret(environment: SimulatorRuntimeEnvironment): Uint8Array {
  const encoded = requiredEnvironment(
    environment,
    'TENKACLOUD_SIMULATOR_LAUNCH_SECRET'
  );
  if (!/^[A-Za-z0-9_-]+$/.test(encoded)) {
    throw new TypeError('TENKACLOUD_SIMULATOR_LAUNCH_SECRET must be base64url');
  }
  const secret = Buffer.from(encoded, 'base64url');
  if (secret.byteLength < 32) {
    throw new TypeError(
      'TENKACLOUD_SIMULATOR_LAUNCH_SECRET must decode to at least 32 bytes'
    );
  }
  return secret;
}

export function needsPrivateDirectoryModeCorrection(mode: number): boolean {
  return (mode & 0o7777) !== 0o700;
}

async function privateDirectory(path: string): Promise<string> {
  const requested = resolve(path);
  await mkdir(requested, { recursive: true, mode: 0o700 });
  const stat = await lstat(requested);
  if (!stat.isDirectory() || stat.isSymbolicLink()) {
    throw new TypeError('TENKACLOUD_SIMULATOR_STATE_DIR must be a directory');
  }
  if (needsPrivateDirectoryModeCorrection(stat.mode)) {
    await chmod(requested, 0o700);
  }
  const canonical = await realpath(requested);
  if (canonical !== requested) {
    throw new TypeError(
      'TENKACLOUD_SIMULATOR_STATE_DIR must not traverse a symbolic link'
    );
  }
  return canonical;
}

async function consoleDirectory(
  environment: SimulatorRuntimeEnvironment
): Promise<string> {
  const configured =
    environment.TENKACLOUD_SIMULATOR_CONSOLE_DIR ??
    new URL('../../console/dist', import.meta.url).pathname;
  const canonical = await realpath(resolve(configured)).catch(() => undefined);
  if (!canonical || !(await lstat(canonical)).isDirectory()) {
    throw new TypeError('Simulator Console build directory does not exist');
  }
  return canonical;
}

function runtimeOrigin(
  environment: SimulatorRuntimeEnvironment,
  host: string,
  port: number
): string {
  const configured = environment.TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN;
  if (!configured) {
    if (host === CONTAINER_HOST) {
      throw new TypeError(
        'TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN is required for container bind'
      );
    }
    const hostname = host === '::1' ? '[::1]' : host;
    return `http://${hostname}:${port}`;
  }
  let origin: URL;
  try {
    origin = new URL(configured);
  } catch {
    throw new TypeError(
      'TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN must be an absolute URL'
    );
  }
  if (
    !['http:', 'https:'].includes(origin.protocol) ||
    (origin.protocol === 'http:' &&
      !LOOPBACK_HOSTS.has(origin.hostname.replace(/^\[|\]$/g, ''))) ||
    origin.username ||
    origin.password ||
    origin.search ||
    origin.hash ||
    (origin.pathname !== '/' && origin.pathname !== '')
  ) {
    throw new TypeError(
      'TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN must be an HTTP origin without credentials, path, query, or fragment'
    );
  }
  return origin.origin;
}

function consoleOrigin(origin: string): string {
  return `${origin}/console`;
}

export async function createSimulatorRuntime(
  environment: SimulatorRuntimeEnvironment
): Promise<SimulatorRuntime> {
  const host = runtimeHost(environment);
  const port = runtimePort(environment);
  const origin = runtimeOrigin(environment, host, port);
  const secret = launchSecret(environment);
  const credentials = nativeCredentials(environment);
  const stateDirectory = await privateDirectory(
    requiredEnvironment(environment, 'TENKACLOUD_SIMULATOR_STATE_DIR')
  );
  const console = await consoleDirectory(environment);
  const registry = new ProviderRegistry([
    new AwsProvider(),
    new AzureProvider(),
    new GcpProvider(),
    new SakuraProvider(),
  ]);
  const store = new SimulationStore(join(stateDirectory, 'simulator.sqlite'));
  const policy = workloadPolicy(environment);
  const core = new SimulationCore(store, registry, {
    ...(policy === undefined
      ? {}
      : { workloadEffects: new DockerWorkloadRunner(policy) }),
  });
  try {
    await core.reconcilePendingLifecycleOperations();
  } catch (error) {
    store.close();
    throw error;
  }
  const simulator = createAuthenticatedSimulatorApp({
    core,
    registry,
    consoleBaseUrl: consoleOrigin(origin),
    launchTokens: new LaunchTokenAuthority(secret),
  });
  const ssmSessionStream = new SsmSessionStreamGateway({ core });
  const nativeGateway = createNativeGatewayHandler({
    core,
    credentials,
    simulatorOrigin: origin,
    beforeAwsCommand: (command) => ssmSessionStream.beforeAwsCommand(command),
    onAwsCommandSuccess: (command, response) =>
      ssmSessionStream.onAwsCommandSuccess(command, response),
  });
  const app = createHostedSimulatorApp(simulator, console, nativeGateway);
  const fetch = (
    request: Request,
    server: Bun.Server<SsmSessionSocketData>
  ): Response | Promise<Response> | undefined =>
    ssmSessionStream.handles(request)
      ? ssmSessionStream.upgrade(request, server)
      : app.fetch(request);
  return {
    app,
    fetch,
    host,
    port,
    store,
    websocket: ssmSessionStream.websocket,
    close: () => store.close(),
  };
}
