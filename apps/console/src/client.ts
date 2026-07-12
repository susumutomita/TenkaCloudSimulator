import {
  assertSimulatorDeploymentResponse,
  assertSimulatorErrorEnvelope,
  assertSimulatorEvent,
  assertSimulatorEventPage,
  assertSimulatorResourceProjection,
  type SimulatorDeploymentResponse,
  type SimulatorErrorEnvelope,
  type SimulatorEvent,
  type SimulatorEventPage,
  type SimulatorResourceProjection,
} from '@tenkacloud/simulator-contracts';
import { simulatorLaunchToken } from './launch-token';

const NEXT_CURSOR_HEADER = 'x-tenkacloud-next-cursor';

export class ConsoleClientError extends Error {
  public constructor(
    message: string,
    public readonly status: number,
    public readonly envelope?: SimulatorErrorEnvelope
  ) {
    super(message);
    this.name = 'ConsoleClientError';
  }
}

function apiUrl(baseUrl: string, path: string): URL {
  const base = new URL(baseUrl);
  const normalized = base.toString().replace(/\/+$/, '');
  return new URL(`${normalized}${path}`);
}

async function responseBody(response: Response): Promise<unknown> {
  const text = await response.text();
  if (!text) return undefined;
  return JSON.parse(text);
}

async function checkedJson(response: Response): Promise<unknown> {
  const body = await responseBody(response);
  if (response.ok) return body;
  assertSimulatorErrorEnvelope(body);
  throw new ConsoleClientError(body.error.message, response.status, body);
}

function parseSseBlock(block: string): SimulatorEvent | undefined {
  const data: string[] = [];
  for (const line of block.split(/\r?\n/)) {
    if (!line || line.startsWith(':')) continue;
    const separator = line.indexOf(':');
    const field = separator === -1 ? line : line.slice(0, separator);
    const rawValue = separator === -1 ? '' : line.slice(separator + 1);
    const value = rawValue.startsWith(' ') ? rawValue.slice(1) : rawValue;
    if (field === 'data') data.push(value);
  }
  if (data.length === 0) return undefined;
  let body: unknown;
  try {
    body = JSON.parse(data.join('\n'));
  } catch (error) {
    throw new ConsoleClientError(
      `Event stream contains invalid JSON: ${String(error)}`,
      502
    );
  }
  assertSimulatorEvent(body);
  return body;
}

export function parseEventStream(payload: string): readonly SimulatorEvent[] {
  return payload
    .split(/\r?\n\r?\n/)
    .map(parseSseBlock)
    .filter((event): event is SimulatorEvent => event !== undefined);
}

export interface StreamBatch {
  readonly events: readonly SimulatorEvent[];
  readonly nextCursor: number;
}

export class SimulatorConsoleClient {
  readonly #baseUrl: string;
  readonly #authorization: string;

  public constructor(baseUrl: string, token: string) {
    const parsed = new URL(baseUrl);
    if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') {
      throw new TypeError('Simulator API URL must use HTTP or HTTPS');
    }
    this.#baseUrl = parsed.toString();
    this.#authorization = `Bearer ${simulatorLaunchToken(token)}`;
  }

  public async resources(
    worldId: string,
    signal?: AbortSignal
  ): Promise<SimulatorResourceProjection> {
    const response = await fetch(
      apiUrl(
        this.#baseUrl,
        `/v1/worlds/${encodeURIComponent(worldId)}/resources`
      ),
      {
        headers: { authorization: this.#authorization },
        ...(signal ? { signal } : {}),
      }
    );
    const body = await checkedJson(response);
    assertSimulatorResourceProjection(body);
    return body;
  }

  public async deployment(
    worldId: string,
    deploymentId: string,
    signal?: AbortSignal
  ): Promise<SimulatorDeploymentResponse> {
    const response = await fetch(
      apiUrl(
        this.#baseUrl,
        `/v1/worlds/${encodeURIComponent(worldId)}/deployments/${encodeURIComponent(deploymentId)}`
      ),
      {
        headers: { authorization: this.#authorization },
        ...(signal ? { signal } : {}),
      }
    );
    const body = await checkedJson(response);
    assertSimulatorDeploymentResponse(body);
    return body;
  }

  public async events(
    worldId: string,
    after = 0,
    signal?: AbortSignal
  ): Promise<SimulatorEventPage> {
    const url = apiUrl(
      this.#baseUrl,
      `/v1/worlds/${encodeURIComponent(worldId)}/events`
    );
    url.searchParams.set('after', String(after));
    const response = await fetch(url, {
      headers: { authorization: this.#authorization },
      ...(signal ? { signal } : {}),
    });
    const body = await checkedJson(response);
    assertSimulatorEventPage(body);
    return body;
  }

  public async stream(
    worldId: string,
    after: number,
    signal?: AbortSignal
  ): Promise<StreamBatch> {
    const response = await fetch(
      apiUrl(
        this.#baseUrl,
        `/v1/worlds/${encodeURIComponent(worldId)}/events/stream`
      ),
      {
        headers: {
          authorization: this.#authorization,
          'last-event-id': String(after),
        },
        ...(signal ? { signal } : {}),
      }
    );
    if (!response.ok) {
      await checkedJson(response);
      throw new ConsoleClientError(
        'Event stream request failed',
        response.status
      );
    }
    const events = parseEventStream(await response.text());
    const nextCursor = Number(
      response.headers.get(NEXT_CURSOR_HEADER) ??
        events.at(-1)?.sequence ??
        after
    );
    return { events, nextCursor };
  }
}
