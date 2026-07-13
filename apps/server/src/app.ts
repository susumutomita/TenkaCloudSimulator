import { randomUUID } from 'node:crypto';
import {
  createSimulatorApp,
  MAX_REQUEST_BODY_BYTES,
  PROTOCOL_HEADER,
  type SimulatorAppOptions,
} from '@tenkacloud/simulator-api';
import {
  assertSimulatorSnapshot,
  assertSimulatorWorldRequest,
  SIMULATOR_PROTOCOL_VERSION,
  type SimulatorErrorCode,
  type SimulatorErrorEnvelope,
  type SimulatorNamespace,
} from '@tenkacloud/simulator-contracts';
import type { Context, Next } from 'hono';
import { Hono } from 'hono';
import {
  bearerLaunchToken,
  type LaunchTokenAuthority,
  type LaunchTokenClaims,
  LaunchTokenError,
} from './auth';

export interface AuthenticatedSimulatorOptions
  extends Omit<
    SimulatorAppOptions,
    'resolveWorldNamespace' | 'signSnapshot' | 'verifySnapshot'
  > {
  readonly launchTokens: LaunchTokenAuthority;
}

const REQUEST_BODY_TOO_LARGE = Symbol('request-body-too-large');

function errorEnvelope(
  code: SimulatorErrorCode,
  message: string
): SimulatorErrorEnvelope {
  return {
    error: {
      code,
      message,
      requestId: randomUUID(),
      retryable: false,
      diagnostics: [],
    },
  };
}

function namespace(claims: LaunchTokenClaims): SimulatorNamespace {
  return {
    tenantId: claims.tenantId,
    eventId: claims.eventId,
    teamId: claims.teamId,
  };
}

function sameNamespace(
  left: SimulatorNamespace,
  right: SimulatorNamespace
): boolean {
  return (
    left.tenantId === right.tenantId &&
    left.eventId === right.eventId &&
    left.teamId === right.teamId
  );
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

async function limitedJson(request: Request): Promise<unknown> {
  const reader = request.clone().body?.getReader();
  if (!reader) return undefined;
  const chunks: Uint8Array[] = [];
  let total = 0;
  while (true) {
    const chunk = await reader.read();
    if (chunk.done) break;
    total += chunk.value.byteLength;
    if (total > MAX_REQUEST_BODY_BYTES) {
      await reader.cancel();
      throw REQUEST_BODY_TOO_LARGE;
    }
    chunks.push(chunk.value);
  }
  const body = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    body.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return JSON.parse(new TextDecoder().decode(body));
}

function routeWorldId(pathname: string): string | undefined {
  if (pathname.startsWith('/v1/worlds/by-deployment/')) return undefined;
  const encoded = /^\/v1\/worlds\/([^/]+)/.exec(pathname)?.[1];
  if (!encoded) return undefined;
  try {
    return decodeURIComponent(encoded);
  } catch {
    return undefined;
  }
}

function routeLookupDeploymentId(pathname: string): string | undefined {
  const encoded = /^\/v1\/worlds\/by-deployment\/([^/]+)$/.exec(pathname)?.[1];
  if (!encoded) return undefined;
  try {
    return decodeURIComponent(encoded);
  } catch {
    return undefined;
  }
}

function authorizeExistingWorld(
  options: AuthenticatedSimulatorOptions,
  claims: LaunchTokenClaims,
  pathname: string
): boolean {
  const worldId = routeWorldId(pathname);
  if (!worldId) return false;
  const world = options.core.world(worldId, namespace(claims));
  return world.deploymentId === claims.deploymentId;
}

async function authorizeBody(
  c: Context,
  claims: LaunchTokenClaims
): Promise<boolean> {
  if (c.req.path === '/v1/worlds') {
    const value = await limitedJson(c.req.raw);
    assertSimulatorWorldRequest(value);
    return (
      value.tenantId === claims.tenantId &&
      value.eventId === claims.eventId &&
      value.teamId === claims.teamId &&
      value.deploymentId === claims.deploymentId
    );
  }
  if (c.req.path.endsWith('/snapshots')) {
    const value = await limitedJson(c.req.raw);
    assertSimulatorSnapshot(value);
    const projectedWorld = value.resourceGraph['world'];
    return (
      sameNamespace(value.namespace, namespace(claims)) &&
      isRecord(projectedWorld) &&
      projectedWorld['tenantId'] === claims.tenantId &&
      projectedWorld['eventId'] === claims.eventId &&
      projectedWorld['teamId'] === claims.teamId &&
      projectedWorld['deploymentId'] === claims.deploymentId
    );
  }
  return true;
}

async function authorizeRequest(
  c: Context,
  options: AuthenticatedSimulatorOptions
): Promise<Response | undefined> {
  const token = bearerLaunchToken(c.req.header('authorization'));
  const claims = options.launchTokens.verify(token);
  const lookupDeploymentId = routeLookupDeploymentId(c.req.path);
  if (lookupDeploymentId !== undefined) {
    if (lookupDeploymentId !== claims.deploymentId) {
      return c.json(errorEnvelope('NotFound', 'world does not exist'), 404);
    }
    return undefined;
  }
  const isCreation = c.req.path === '/v1/worlds';
  if (!isCreation && !authorizeExistingWorld(options, claims, c.req.path)) {
    return c.json(errorEnvelope('NotFound', 'world does not exist'), 404);
  }
  if (c.req.method === 'POST' && !(await authorizeBody(c, claims))) {
    return c.json(errorEnvelope('NotFound', 'world does not exist'), 404);
  }
  return undefined;
}

function authorizationError(c: Context, error: unknown): Response {
  if (error === REQUEST_BODY_TOO_LARGE) {
    return c.json(
      errorEnvelope('ValidationFailed', 'request body is too large'),
      413
    );
  }
  if (error instanceof LaunchTokenError) {
    return c.json(errorEnvelope('UnauthorizedOperation', error.message), 401);
  }
  if (error instanceof SyntaxError || error instanceof TypeError) {
    return c.json(
      errorEnvelope('ValidationFailed', 'request body is invalid'),
      400
    );
  }
  return c.json(errorEnvelope('NotFound', 'world does not exist'), 404);
}

function authorizationMiddleware(options: AuthenticatedSimulatorOptions) {
  return async (c: Context, next: Next): Promise<Response | undefined> => {
    try {
      const denied = await authorizeRequest(c, options);
      if (denied) return denied;
      await next();
      return undefined;
    } catch (error) {
      return authorizationError(c, error);
    }
  };
}

export function createAuthenticatedSimulatorApp(
  options: AuthenticatedSimulatorOptions
): Hono {
  const app = new Hono();
  app.use('*', async (c, next) => {
    await next();
    c.header(PROTOCOL_HEADER, SIMULATOR_PROTOCOL_VERSION);
  });
  const authorize = authorizationMiddleware(options);
  app.use('/v1/worlds', authorize);
  app.use('/v1/worlds/*', authorize);
  app.route(
    '/',
    createSimulatorApp({
      core: options.core,
      registry: options.registry,
      consoleBaseUrl: options.consoleBaseUrl,
      resolveWorldNamespace: (request) =>
        namespace(
          options.launchTokens.verify(
            bearerLaunchToken(request.headers.get('authorization') ?? undefined)
          )
        ),
      signSnapshot: (envelope) => options.launchTokens.signSnapshot(envelope),
      verifySnapshot: (snapshot) =>
        options.launchTokens.verifySnapshot(snapshot),
    })
  );
  return app;
}
