import type {
  ConsoleProviderOperationResponse,
  SimulatorConsoleClient,
  StreamBatch,
} from './client';
import type { ConsoleWorldData } from './model';
import { mergeEvents } from './model';

export interface ConsoleRoute {
  readonly worldId: string;
  readonly deploymentId?: string;
}

export function parseConsoleRoute(url: URL): ConsoleRoute {
  const match = /^\/console\/([^/]+)\/?$/.exec(url.pathname);
  if (!match?.[1]) {
    throw new Error('Expected /console/:worldId');
  }
  const worldId = decodeURIComponent(match[1]);
  if (!worldId) throw new Error('World ID must not be empty');
  const deploymentId = url.searchParams.get('deploymentId')?.trim();
  return {
    worldId,
    ...(deploymentId ? { deploymentId } : {}),
  };
}

type ConsoleProjections = Pick<ConsoleWorldData, 'deployment' | 'resources'>;

async function loadConsoleProjections(
  client: SimulatorConsoleClient,
  route: ConsoleRoute,
  signal?: AbortSignal
): Promise<ConsoleProjections> {
  const [resources, deployment] = await Promise.all([
    client.resources(route.worldId, signal),
    route.deploymentId
      ? client.deployment(route.worldId, route.deploymentId, signal)
      : undefined,
  ]);
  return {
    resources,
    ...(deployment ? { deployment } : {}),
  };
}

export async function loadConsoleData(
  client: SimulatorConsoleClient,
  route: ConsoleRoute,
  signal?: AbortSignal
): Promise<ConsoleWorldData> {
  const [projections, eventPage] = await Promise.all([
    loadConsoleProjections(client, route, signal),
    client.events(route.worldId, 0, signal),
  ]);
  return {
    worldId: route.worldId,
    ...projections,
    events: eventPage.events,
    cursor: eventPage.nextCursor,
  };
}

export async function applyConsoleStreamBatch(
  client: SimulatorConsoleClient,
  route: ConsoleRoute,
  current: ConsoleWorldData,
  batch: StreamBatch,
  signal?: AbortSignal
): Promise<ConsoleWorldData> {
  const projections =
    batch.events.length === 0
      ? {
          resources: current.resources,
          ...(current.deployment ? { deployment: current.deployment } : {}),
        }
      : await loadConsoleProjections(client, route, signal);
  return {
    ...current,
    ...projections,
    events: mergeEvents(current.events, batch.events),
    cursor: batch.nextCursor,
  };
}

export async function loadConsoleStreamUpdate(
  client: SimulatorConsoleClient,
  route: ConsoleRoute,
  current: ConsoleWorldData,
  signal?: AbortSignal
): Promise<ConsoleWorldData | undefined> {
  const batch = await client.stream(route.worldId, current.cursor, signal);
  if (batch.events.length === 0) return undefined;
  return applyConsoleStreamBatch(client, route, current, batch, signal);
}

function requiredFormText(formData: FormData, name: string): string {
  const value = formData.get(name);
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${name} must not be empty`);
  }
  return value.trim();
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function operationInput(formData: FormData): Readonly<Record<string, unknown>> {
  let value: unknown;
  try {
    value = JSON.parse(requiredFormText(formData, 'input'));
  } catch (error) {
    throw new Error('input must be a JSON object', { cause: error });
  }
  if (!isRecord(value)) {
    throw new Error('input must be a JSON object');
  }
  return value;
}

export async function submitConsoleOperation(
  client: SimulatorConsoleClient,
  route: ConsoleRoute,
  formData: FormData
): Promise<ConsoleProviderOperationResponse> {
  if (!route.deploymentId) {
    throw new Error('Provider operations require a selected deployment');
  }
  return client.operation(route.worldId, {
    deploymentId: route.deploymentId,
    provider: requiredFormText(formData, 'provider'),
    targetId: requiredFormText(formData, 'targetId'),
    engine: requiredFormText(formData, 'engine'),
    service: requiredFormText(formData, 'service'),
    resourceType: requiredFormText(formData, 'resourceType'),
    operation: requiredFormText(formData, 'operation'),
    input: operationInput(formData),
    idempotencyKey: requiredFormText(formData, 'idempotencyKey'),
  });
}

export function createConsoleOperationAction(
  client: SimulatorConsoleClient,
  route: ConsoleRoute,
  refresh: () => void
): (formData: FormData) => Promise<void> {
  return async (formData) => {
    await submitConsoleOperation(client, route, formData);
    refresh();
  };
}
