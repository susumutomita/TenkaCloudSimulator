import {
  assertSimulatorCapabilities,
  assertSimulatorDeploymentResponse,
  assertSimulatorErrorEnvelope,
  assertSimulatorEventPage,
  assertSimulatorResourceProjection,
  assertSimulatorSnapshot,
  assertSimulatorWorldResponse,
  SIMULATOR_PROTOCOL_VERSION,
  type SimulatorCapabilities,
  type SimulatorDeploymentRequest,
  type SimulatorDeploymentResponse,
  type SimulatorErrorEnvelope,
  type SimulatorEventPage,
  type SimulatorResourceProjection,
  type SimulatorSnapshot,
  type SimulatorWorldRequest,
  type SimulatorWorldResponse,
} from '@tenkacloud/simulator-contracts';

export interface SimulatorClientTimeoutPolicy {
  readonly requestMilliseconds: number;
}

const MAX_SIMULATOR_CLIENT_TIMEOUT_MILLISECONDS = 600_000;

export const DEFAULT_SIMULATOR_CLIENT_TIMEOUT_POLICY: SimulatorClientTimeoutPolicy =
  Object.freeze({
    requestMilliseconds: 10_000,
  });

function assertTimeoutPolicy(policy: SimulatorClientTimeoutPolicy): void {
  if (
    !Number.isSafeInteger(policy.requestMilliseconds) ||
    policy.requestMilliseconds < 1 ||
    policy.requestMilliseconds > MAX_SIMULATOR_CLIENT_TIMEOUT_MILLISECONDS
  ) {
    throw new TypeError(
      'client request timeout must be a bounded positive integer'
    );
  }
}

export class SimulatorClientError extends Error {
  constructor(
    readonly status: number,
    readonly envelope: SimulatorErrorEnvelope
  ) {
    super(envelope.error.message);
    this.name = 'SimulatorClientError';
  }
}

function normalizedBaseUrl(value: string): string {
  const url = new URL(value);
  const loopback =
    url.hostname === '127.0.0.1' ||
    url.hostname === 'localhost' ||
    url.hostname === '[::1]';
  if (url.protocol !== 'https:' && !(url.protocol === 'http:' && loopback)) {
    throw new TypeError('Simulator URL must use HTTPS or loopback HTTP');
  }
  return url.toString().replace(/\/$/, '');
}

export async function decodeSimulatorResponse(
  response: Response
): Promise<unknown> {
  const text = await response.text();
  if (!text) return undefined;
  try {
    return JSON.parse(text);
  } catch {
    throw new TypeError(
      `Simulator returned invalid JSON with HTTP ${response.status}`
    );
  }
}

async function successfulJson(response: Response): Promise<unknown> {
  const value = await decodeSimulatorResponse(response);
  if (response.ok) return value;
  assertSimulatorErrorEnvelope(value);
  throw new SimulatorClientError(response.status, value);
}

export async function assertSimulatorDeleteResponse(
  response: Response
): Promise<void> {
  if (response.ok) return;
  await successfulJson(response);
}

export interface ProviderOperationRequest {
  readonly deploymentId: string;
  readonly targetId: string;
  readonly engine: string;
  readonly service: string;
  readonly resourceType: string;
  readonly input: Readonly<Record<string, unknown>>;
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

export function parseProviderOperationResponse(
  value: unknown
): Readonly<Record<string, unknown>> {
  if (!isRecord(value)) {
    throw new TypeError('Provider operation response must be an object');
  }
  return value;
}

export class SimulatorClient {
  readonly #baseUrl: string;
  readonly #launchToken: string | undefined;
  readonly #requestTimeoutMilliseconds: number;

  constructor(
    baseUrl: string,
    launchToken?: string,
    timeoutPolicy = DEFAULT_SIMULATOR_CLIENT_TIMEOUT_POLICY
  ) {
    this.#baseUrl = normalizedBaseUrl(baseUrl);
    if (launchToken && !launchToken.startsWith('tc_sim_v1.')) {
      throw new TypeError('CLI token must be a simulator launch token');
    }
    assertTimeoutPolicy(timeoutPolicy);
    this.#launchToken = launchToken;
    this.#requestTimeoutMilliseconds = timeoutPolicy.requestMilliseconds;
  }

  #headers(idempotencyKey?: string): Headers {
    const headers = new Headers({
      'content-type': 'application/json',
      'x-tenkacloud-simulator-protocol': SIMULATOR_PROTOCOL_VERSION,
    });
    if (idempotencyKey) headers.set('idempotency-key', idempotencyKey);
    return headers;
  }

  #fetch(
    path: string,
    init: RequestInit,
    signal: AbortSignal | null
  ): Promise<Response> {
    const headers = new Headers(init.headers);
    if (this.#launchToken) {
      headers.set('authorization', `Bearer ${this.#launchToken}`);
    }
    return fetch(`${this.#baseUrl}${path}`, {
      ...init,
      headers,
      signal,
    });
  }

  #request(path: string, init: RequestInit = {}): Promise<Response> {
    return this.#fetch(
      path,
      init,
      AbortSignal.timeout(this.#requestTimeoutMilliseconds)
    );
  }

  async capabilities(): Promise<SimulatorCapabilities> {
    const value = await successfulJson(await this.#request('/v1/capabilities'));
    assertSimulatorCapabilities(value);
    return value;
  }

  async createWorld(
    request: SimulatorWorldRequest,
    idempotencyKey = `world:${request.deploymentId}`
  ): Promise<SimulatorWorldResponse> {
    const value = await successfulJson(
      await this.#request('/v1/worlds', {
        method: 'POST',
        headers: this.#headers(idempotencyKey),
        body: JSON.stringify(request),
      })
    );
    assertSimulatorWorldResponse(value);
    return value;
  }

  async createDeployment(
    worldId: string,
    request: SimulatorDeploymentRequest,
    idempotencyKey = `deployment:${request.problemId}`
  ): Promise<SimulatorDeploymentResponse> {
    const value = await successfulJson(
      await this.#request(
        `/v1/worlds/${encodeURIComponent(worldId)}/deployments`,
        {
          method: 'POST',
          headers: this.#headers(idempotencyKey),
          body: JSON.stringify(request),
        }
      )
    );
    assertSimulatorDeploymentResponse(value);
    return value;
  }

  async getDeployment(
    worldId: string,
    deploymentId: string
  ): Promise<SimulatorDeploymentResponse> {
    const value = await successfulJson(
      await this.#request(
        `/v1/worlds/${encodeURIComponent(worldId)}/deployments/${encodeURIComponent(deploymentId)}`
      )
    );
    assertSimulatorDeploymentResponse(value);
    return value;
  }

  async deleteWorld(worldId: string, signal?: AbortSignal): Promise<void> {
    const response = await this.#fetch(
      `/v1/worlds/${encodeURIComponent(worldId)}`,
      { method: 'DELETE', headers: this.#headers() },
      signal ?? null
    );
    await assertSimulatorDeleteResponse(response);
  }

  async operation(
    worldId: string,
    provider: string,
    operation: string,
    request: ProviderOperationRequest,
    idempotencyKey: string
  ): Promise<Readonly<Record<string, unknown>>> {
    const value = await successfulJson(
      await this.#request(
        `/v1/worlds/${encodeURIComponent(worldId)}/providers/${encodeURIComponent(provider)}/operations/${encodeURIComponent(operation)}`,
        {
          method: 'POST',
          headers: this.#headers(idempotencyKey),
          body: JSON.stringify(request),
        }
      )
    );
    return parseProviderOperationResponse(value);
  }

  async resources(worldId: string): Promise<SimulatorResourceProjection> {
    const value = await successfulJson(
      await this.#request(`/v1/worlds/${encodeURIComponent(worldId)}/resources`)
    );
    assertSimulatorResourceProjection(value);
    return value;
  }

  async events(worldId: string, after = 0): Promise<SimulatorEventPage> {
    const value = await successfulJson(
      await this.#request(
        `/v1/worlds/${encodeURIComponent(worldId)}/events?after=${encodeURIComponent(String(after))}`
      )
    );
    assertSimulatorEventPage(value);
    return value;
  }

  async snapshot(worldId: string): Promise<SimulatorSnapshot> {
    const value = await successfulJson(
      await this.#request(`/v1/worlds/${encodeURIComponent(worldId)}/snapshots`)
    );
    assertSimulatorSnapshot(value);
    return value;
  }

  async restoreSnapshot(
    snapshot: SimulatorSnapshot,
    idempotencyKey = `snapshot:${snapshot.hash}`
  ): Promise<SimulatorWorldResponse> {
    const value = await successfulJson(
      await this.#request(
        `/v1/worlds/${encodeURIComponent(snapshot.worldId)}/snapshots`,
        {
          method: 'POST',
          headers: this.#headers(idempotencyKey),
          body: JSON.stringify(snapshot),
        }
      )
    );
    assertSimulatorWorldResponse(value);
    return value;
  }
}
