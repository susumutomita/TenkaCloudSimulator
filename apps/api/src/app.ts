import {
  assertSimulatorClockAdvanceRequest,
  assertSimulatorDeploymentRequest,
  assertSimulatorMaterializeWorkloadsRequest,
  assertSimulatorSnapshot,
  assertSimulatorWorldRequest,
  SIMULATOR_PROTOCOL_VERSION,
  type SimulatorSnapshot,
  type SimulatorSnapshotEnvelope,
  type SimulatorSnapshotIntegrityProof,
} from '@tenkacloud/simulator-contracts';
import type {
  CreateWorldInput,
  DeploymentInput,
  ExecuteCommandInput,
  ProviderRegistry,
  SimulationCore,
  WorldNamespace,
} from '@tenkacloud/simulator-core';
import { Hono } from 'hono';
import { bodyLimit } from 'hono/body-limit';
import { streamSSE } from 'hono/streaming';
import { simulatorCapabilities } from './capabilities.js';
import { executeDataPlaneRequest, isDataPlanePath } from './data-plane.js';
import {
  bodyLimitResponse,
  errorResponse,
  handleAppError,
  MAX_REQUEST_BODY_BYTES,
  PROTOCOL_HEADER,
  protocolMismatchResponse,
  RequestValidationError,
} from './errors.js';
import { eventCursor, eventPage, NEXT_CURSOR_HEADER } from './events.js';
import {
  deploymentResponse,
  resourceProjection,
  worldResponse,
} from './presenters.js';
import { idempotencyKey, parseProviderCommand, readJson } from './request.js';
import { coreSnapshot, simulatorSnapshot } from './snapshot.js';

export interface SimulatorAppOptions {
  readonly core: SimulationCore;
  readonly registry: ProviderRegistry;
  readonly consoleBaseUrl: string;
  readonly resolveWorldNamespace: (request: Request) => WorldNamespace;
  readonly signSnapshot: (
    envelope: SimulatorSnapshotEnvelope
  ) => SimulatorSnapshotIntegrityProof;
  readonly verifySnapshot: (snapshot: SimulatorSnapshot) => boolean;
}

function normalizedConsoleBaseUrl(value: string): string {
  const url = new URL(value);
  return url.toString().replace(/\/+$/, '');
}

function requiresProtocol(method: string): boolean {
  return method === 'POST' || method === 'DELETE';
}

export function createSimulatorApp(options: SimulatorAppOptions): Hono {
  if (
    typeof options.signSnapshot !== 'function' ||
    typeof options.verifySnapshot !== 'function'
  ) {
    throw new TypeError('snapshot signer and verifier are required');
  }
  const app = new Hono();
  const consoleBaseUrl = normalizedConsoleBaseUrl(options.consoleBaseUrl);

  app.use('*', async (c, next) => {
    await next();
    c.header(PROTOCOL_HEADER, SIMULATOR_PROTOCOL_VERSION);
  });
  app.use('*', async (c, next) => {
    if (
      requiresProtocol(c.req.method) &&
      !isDataPlanePath(c.req.path) &&
      c.req.header(PROTOCOL_HEADER) !== SIMULATOR_PROTOCOL_VERSION
    ) {
      return protocolMismatchResponse(c);
    }
    await next();
  });
  app.use(
    '*',
    bodyLimit({
      maxSize: MAX_REQUEST_BODY_BYTES,
      onError: bodyLimitResponse,
    })
  );

  app.get('/v1/capabilities', (c) => {
    return c.json(
      simulatorCapabilities(
        options.registry,
        options.core.workloadEffectsAvailable
      )
    );
  });

  app.get('/v1/worlds/by-deployment/:deploymentId', (c) => {
    const deploymentId = c.req.param('deploymentId');
    const world = options.core.worldByDeployment(
      options.resolveWorldNamespace(c.req.raw),
      deploymentId,
      idempotencyKey(c, deploymentId, 'create-world')
    );
    return c.json(worldResponse(world, consoleBaseUrl));
  });

  app.post('/v1/worlds', async (c) => {
    const request = await readJson(c);
    assertSimulatorWorldRequest(request);
    const input: CreateWorldInput = {
      tenantId: request.tenantId,
      eventId: request.eventId,
      teamId: request.teamId,
      deploymentId: request.deploymentId,
      ...(request.seed === undefined ? {} : { seed: request.seed }),
      ...(request.virtualClock === undefined
        ? {}
        : { virtualTime: request.virtualClock }),
    };
    const world = options.core.createWorld(
      input,
      idempotencyKey(c, request.deploymentId, 'create-world')
    );
    return c.json(worldResponse(world, consoleBaseUrl), 201);
  });

  app.post('/v1/worlds/:worldId/deployments', async (c) => {
    const request = await readJson(c);
    assertSimulatorDeploymentRequest(request);
    const world = options.core.world(c.req.param('worldId'));
    const input: DeploymentInput = {
      deploymentId: world.deploymentId,
      problemId: request.problemId,
      runtime: request.runtime,
      templateBody: request.templateBody,
      ...(request.metadata === undefined ? {} : { metadata: request.metadata }),
      ...(request.simulationOverlay === undefined
        ? {}
        : { simulationOverlay: request.simulationOverlay }),
    };
    let deployment = options.core.createDeployment(
      world.worldId,
      input,
      idempotencyKey(c, world.deploymentId, 'create-deployment')
    );
    if (deployment.status === 'deploying' || deployment.status === 'failed') {
      deployment = await options.core.materializeWorkloads(
        world.worldId,
        deployment.deploymentId
      );
    }
    return c.json(deploymentResponse(deployment), 201);
  });

  app.post('/v1/worlds/:worldId/workloads/materialize', async (c) => {
    const request = await readJson(c);
    assertSimulatorMaterializeWorkloadsRequest(request);
    const deployment = await options.core.materializeWorkloads(
      c.req.param('worldId'),
      request.deploymentId
    );
    return c.json(deploymentResponse(deployment));
  });

  app.get('/v1/worlds/:worldId/deployments/:deploymentId', (c) => {
    const deployment = options.core.deployment(
      c.req.param('worldId'),
      c.req.param('deploymentId')
    );
    return c.json(deploymentResponse(deployment));
  });

  app.delete('/v1/worlds/:worldId', async (c) => {
    await options.core.deleteWorld(c.req.param('worldId'));
    return c.body(null, 204);
  });

  app.post('/v1/worlds/:worldId/clock/advance', async (c) => {
    const request = await readJson(c);
    assertSimulatorClockAdvanceRequest(request);
    const result = options.core.advanceClock(
      c.req.param('worldId'),
      request.milliseconds
    );
    return c.json({
      clock: result.virtualTime,
      appliedTransitions: result.appliedTransitions,
    });
  });

  app.post(
    '/v1/worlds/:worldId/providers/:provider/operations/:operation',
    async (c) => {
      const request = parseProviderCommand(await readJson(c));
      const operation = c.req.param('operation');
      const input: ExecuteCommandInput = {
        deploymentId: request.deploymentId,
        targetId: request.targetId,
        provider: c.req.param('provider'),
        engine: request.engine,
        service: request.service,
        operation,
        resourceType: request.resourceType,
        input: request.input,
      };
      const response = await options.core.executeCommandAsync(
        c.req.param('worldId'),
        input,
        idempotencyKey(
          c,
          request.deploymentId,
          `${request.targetId}:${operation}`
        )
      );
      return c.json(response);
    }
  );

  app.all('/v1/worlds/:worldId/data-plane/:provider/:targetId/*', (c) =>
    executeDataPlaneRequest(c, options)
  );

  app.get('/v1/worlds/:worldId/resources', (c) => {
    return c.json(
      resourceProjection(options.core.resources(c.req.param('worldId')))
    );
  });

  app.get('/v1/worlds/:worldId/events', (c) => {
    const page = eventPage(
      options.core.events(c.req.param('worldId')),
      eventCursor(c)
    );
    return c.json(page);
  });

  app.get('/v1/worlds/:worldId/events/stream', (c) => {
    const page = eventPage(
      options.core.events(c.req.param('worldId')),
      eventCursor(c)
    );
    c.header(NEXT_CURSOR_HEADER, String(page.nextCursor));
    return streamSSE(c, async (stream) => {
      for (const event of page.events) {
        await stream.writeSSE({
          data: JSON.stringify(event),
          event: event.type,
          id: String(event.sequence),
        });
      }
    });
  });

  app.get('/v1/worlds/:worldId/snapshots', (c) => {
    const envelope = simulatorSnapshot(
      options.core.exportSnapshot(c.req.param('worldId'))
    );
    const response: unknown = {
      ...envelope,
      integrityProof: options.signSnapshot(envelope),
    };
    assertSimulatorSnapshot(response);
    return c.json(response);
  });

  app.get('/v1/worlds/:worldId/snapshots/restores/:snapshotHash', (c) => {
    const sourceWorldId = c.req.param('worldId');
    const source = options.core.world(
      sourceWorldId,
      options.resolveWorldNamespace(c.req.raw)
    );
    const restored = options.core.restoredWorld(
      sourceWorldId,
      c.req.param('snapshotHash'),
      idempotencyKey(c, source.deploymentId, 'restore-snapshot'),
      source
    );
    return c.json(worldResponse(restored, consoleBaseUrl));
  });

  app.post('/v1/worlds/:worldId/snapshots', async (c) => {
    const request = await readJson(c);
    assertSimulatorSnapshot(request);
    if (!options.verifySnapshot(request)) {
      throw new RequestValidationError('snapshot integrity proof is invalid');
    }
    if (request.worldId !== c.req.param('worldId')) {
      return errorResponse(
        c,
        'ValidationFailed',
        'snapshot worldId must match the route worldId',
        400
      );
    }
    const snapshot = coreSnapshot(request);
    const restored = await options.core.restoreSnapshot(
      snapshot,
      idempotencyKey(c, snapshot.payload.world.deploymentId, 'restore-snapshot')
    );
    return c.json(worldResponse(restored, consoleBaseUrl), 201);
  });

  app.notFound((c) =>
    errorResponse(c, 'NotFound', 'route does not exist', 404)
  );
  app.onError(handleAppError);
  return app;
}
