import type { SimulatorConsoleClient } from './client';
import type { ConsoleWorldData } from './model';

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

export async function loadConsoleData(
  client: SimulatorConsoleClient,
  route: ConsoleRoute,
  signal?: AbortSignal
): Promise<ConsoleWorldData> {
  const [resources, eventPage, deployment] = await Promise.all([
    client.resources(route.worldId, signal),
    client.events(route.worldId, 0, signal),
    route.deploymentId
      ? client.deployment(route.worldId, route.deploymentId, signal)
      : undefined,
  ]);
  return {
    worldId: route.worldId,
    ...(deployment ? { deployment } : {}),
    resources,
    events: eventPage.events,
    cursor: eventPage.nextCursor,
  };
}
