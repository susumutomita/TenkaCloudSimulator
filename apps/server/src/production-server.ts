import type { SimulatorRuntime } from './runtime';

export const PRODUCTION_SERVER_IDLE_TIMEOUT_SECONDS = 10;

type ProductionServerFetch = (
  request: Request,
  server: Parameters<SimulatorRuntime['fetch']>[1]
) => Response | Promise<Response | undefined> | undefined;

export function isCompletionBoundWorldDelete(request: Request): boolean {
  const url = new URL(request.url);
  return (
    request.method === 'DELETE' &&
    url.search === '' &&
    /^\/v1\/worlds\/[^/]+$/.test(url.pathname)
  );
}

async function fetchCompletionBoundWorldDelete(
  fetch: SimulatorRuntime['fetch'],
  request: Request,
  server: Parameters<SimulatorRuntime['fetch']>[1],
  idleTimeoutSeconds: number
): Promise<Response | undefined> {
  server.timeout(request, 0);
  try {
    return await fetch(request, server);
  } finally {
    server.timeout(request, idleTimeoutSeconds);
  }
}

export function productionServerFetch(
  fetch: SimulatorRuntime['fetch'],
  idleTimeoutSeconds: number
): ProductionServerFetch {
  return (request, server) => {
    if (!isCompletionBoundWorldDelete(request)) {
      return fetch(request, server);
    }
    return fetchCompletionBoundWorldDelete(
      fetch,
      request,
      server,
      idleTimeoutSeconds
    );
  };
}
