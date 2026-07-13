import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import { createHmac, timingSafeEqual } from 'node:crypto';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import {
  assertSimulatorCapabilities,
  assertSimulatorClockAdvanceResponse,
  assertSimulatorDeploymentResponse,
  assertSimulatorEventPage,
  assertSimulatorResourceProjection,
  assertSimulatorSnapshot,
  assertSimulatorWorldResponse,
  canonicalSimulatorSnapshotIntegrityPayload,
  SIMULATOR_PROTOCOL_VERSION,
  SIMULATOR_SNAPSHOT_INTEGRITY_ALGORITHM,
  SIMULATOR_SNAPSHOT_INTEGRITY_VERSION,
  type SimulatorSnapshot,
  type SimulatorSnapshotEnvelope,
  type SimulatorSnapshotIntegrityProof,
} from '@tenkacloud/simulator-contracts';
import {
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderModule,
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
  type WorkloadDeclaration,
  type WorkloadEffectPort,
} from '@tenkacloud/simulator-core';
import {
  createSimulatorApp,
  dataPlaneHeaders,
  dataPlaneIdentifier,
  dataPlaneMethod,
  MAX_EVENT_PAGE_SIZE,
  MAX_REQUEST_BODY_BYTES,
  NEXT_CURSOR_HEADER,
  PROTOCOL_HEADER,
} from '../src/index.js';

const CONTRACT_HTTP_RESPONSES: Readonly<
  Record<string, Readonly<Record<string, unknown>>>
> = {
  '/invalid-status': { StatusCode: 199, Headers: {}, Body: 'invalid' },
  '/invalid-empty-status-body': {
    StatusCode: 204,
    Headers: {},
    Body: 'invalid',
  },
  '/empty-status': { StatusCode: 204, Headers: {}, Body: '' },
  '/invalid-headers': { StatusCode: 200, Headers: [], Body: 'invalid' },
  '/invalid-body': { StatusCode: 200, Headers: {}, Body: {} },
  '/invalid-response': {
    StatusCode: 200,
    Headers: { connection: 'close' },
    Body: 'invalid',
  },
};

const SNAPSHOT_INTEGRITY_SECRET = 'api-test-snapshot-secret-0123456789abcdef';

function snapshotProofValue(envelope: SimulatorSnapshotEnvelope): string {
  return createHmac('sha256', SNAPSHOT_INTEGRITY_SECRET)
    .update(canonicalSimulatorSnapshotIntegrityPayload(envelope))
    .digest('base64url');
}

function signSnapshot(
  envelope: SimulatorSnapshotEnvelope
): SimulatorSnapshotIntegrityProof {
  return {
    version: SIMULATOR_SNAPSHOT_INTEGRITY_VERSION,
    algorithm: SIMULATOR_SNAPSHOT_INTEGRITY_ALGORITHM,
    value: snapshotProofValue(envelope),
  };
}

function signedSnapshot(
  envelope: SimulatorSnapshotEnvelope
): SimulatorSnapshot {
  const snapshot: unknown = {
    ...envelope,
    integrityProof: signSnapshot(envelope),
  };
  assertSimulatorSnapshot(snapshot);
  return snapshot;
}

function verifySnapshot(snapshot: SimulatorSnapshot): boolean {
  const { integrityProof, ...envelope } = snapshot;
  const expected = Buffer.from(snapshotProofValue(envelope), 'ascii');
  const provided = Buffer.from(integrityProof.value, 'ascii');
  return timingSafeEqual(expected, provided);
}

function contractHttpRequest(
  input: ProviderCommandInput
): ProviderCommandResult {
  const { Body, Headers, Method, Path } = input.input;
  const selected =
    typeof Path === 'string' ? CONTRACT_HTTP_RESPONSES[Path] : undefined;
  if (selected) {
    return {
      events: [],
      resources: [],
      deletedResourceIds: [],
      outputs: {},
      response: selected,
    };
  }
  return {
    events: [
      {
        type: 'ContractHttpRequested',
        payload: { method: Method, path: Path },
      },
    ],
    resources: [],
    deletedResourceIds: [],
    outputs: {},
    response: {
      StatusCode: 201,
      Headers: {
        'content-type': 'application/json; charset=utf-8',
        'x-contract-method': String(Method),
      },
      Body: JSON.stringify({
        method: Method,
        path: Path,
        headers: Headers,
        body: Body,
      }),
    },
  };
}

const CONTRACT_PROVIDER: ProviderModule = {
  provider: 'contract',
  engines: ['declarative'],
  capabilities: [
    {
      capabilityId: 'contract-deploy',
      provider: 'contract',
      engine: 'declarative',
      service: 'declarative',
      resourceType: 'ContractResource',
      operation: 'deploy',
      fidelity: ['L0', 'L1'],
    },
    {
      capabilityId: 'contract-update',
      provider: 'contract',
      engine: 'declarative',
      service: 'resources',
      resourceType: 'ContractResource',
      operation: 'update',
      fidelity: ['L0', 'L1'],
    },
    {
      capabilityId: 'contract-explode',
      provider: 'contract',
      engine: 'declarative',
      service: 'resources',
      resourceType: 'ContractResource',
      operation: 'explode',
      fidelity: ['L0'],
    },
    {
      capabilityId: 'contract-http-request',
      provider: 'contract',
      engine: 'declarative',
      service: 'http',
      resourceType: 'HTTP::Endpoint',
      operation: 'Request',
      fidelity: ['L0', 'L4'],
    },
  ],
  compile: (input) => ({
    targetId: input.targetId,
    provider: 'contract',
    engine: 'declarative',
    requirements: [
      {
        provider: 'contract',
        engine: 'declarative',
        service: 'declarative',
        resourceType: 'ContractResource',
        operation: 'deploy',
        fidelity: ['L0', 'L1'],
        source: { path: input.target.entry },
      },
    ],
    resources: [
      {
        provider: 'contract',
        resourceType: 'ContractResource',
        resourceId: `${input.targetId}-resource`,
        properties: {
          problemId: input.problemId,
          templateBody: input.templateBody,
          ...(input.simulationOverlay === undefined
            ? {}
            : { simulationOverlay: input.simulationOverlay }),
        },
      },
    ],
  }),
  deploy: (plan) => ({
    events: [
      {
        type: 'ContractResourceReady',
        payload: { targetId: plan.targetId },
      },
    ],
    resources: [],
    outputs: { resourceId: `${plan.targetId}-resource` },
  }),
  reduce: (input) => {
    if (input.operation === 'explode') {
      throw new Error('contract provider failed');
    }
    if (input.operation === 'Request') {
      return contractHttpRequest(input);
    }
    const { resourceId } = input.input;
    if (typeof resourceId !== 'string' || !resourceId) {
      throw new Error('resourceId is required');
    }
    return {
      events: [
        {
          type: 'ContractResourceUpdated',
          payload: { resourceId },
        },
      ],
      resources: [
        {
          provider: 'contract',
          resourceType: 'ContractResource',
          resourceId,
          properties: { updated: true },
        },
      ],
      deletedResourceIds: [],
      outputs: { lastOperation: input.operation },
      response: { resourceId, updated: true },
    };
  },
};

const WORKLOAD_IMAGE = `ghcr.io/tenkacloud/api@sha256:${'a'.repeat(64)}`;

class HttpWorkloadEffects implements WorkloadEffectPort {
  readonly #servers = new Map<string, Bun.Server<undefined>[]>();
  materializeFails = false;
  cleanupFails = false;

  async materialize(
    worldId: string,
    declarations: readonly WorkloadDeclaration[]
  ) {
    if (this.materializeFails) {
      this.materializeFails = false;
      throw new Error('HTTP workload effect failed');
    }
    const servers = this.#servers.get(worldId) ?? [];
    const results = declarations.map((declaration) => {
      const server = Bun.serve({
        hostname: '127.0.0.1',
        port: 0,
        fetch: () => new Response('healthy'),
      });
      servers.push(server);
      return {
        worldId,
        workloadId: declaration.id,
        targetId: declaration.targetId,
        resourceRef: declaration.resourceRef,
        image: declaration.image,
        healthPath: declaration.healthPath ?? '/',
        endpoint: server.url.origin,
      };
    });
    this.#servers.set(worldId, servers);
    return results;
  }

  async cleanup(worldId: string): Promise<void> {
    if (this.cleanupFails) throw new Error('HTTP workload cleanup failed');
    for (const server of this.#servers.get(worldId) ?? []) server.stop(true);
    this.#servers.delete(worldId);
  }

  close(): void {
    for (const servers of this.#servers.values()) {
      for (const server of servers) server.stop(true);
    }
    this.#servers.clear();
  }
}

interface TestRuntime {
  readonly baseUrl: string;
  readonly app: ReturnType<typeof createSimulatorApp>;
  readonly core: SimulationCore;
  readonly server: Bun.Server<undefined>;
  readonly store: SimulationStore;
  readonly directory: string;
  readonly workloadEffects?: HttpWorkloadEffects;
}

let runtime: TestRuntime;

function protocolHeaders(): HeadersInit {
  return {
    'content-type': 'application/json',
    [PROTOCOL_HEADER]: SIMULATOR_PROTOCOL_VERSION,
  };
}

async function createWorld(deploymentId: string): Promise<{
  readonly worldId: string;
  readonly consoleUrl: string;
}> {
  const response = await fetch(`${runtime.baseUrl}/v1/worlds`, {
    method: 'POST',
    headers: protocolHeaders(),
    body: JSON.stringify({
      tenantId: 'tenant-a',
      eventId: 'event-a',
      teamId: 'team-a',
      deploymentId,
      seed: 'seed-a',
    }),
  });
  expect(response.status).toBe(201);
  const body: unknown = await response.json();
  assertSimulatorWorldResponse(body);
  return body;
}

async function deploySingle(worldId: string): Promise<Response> {
  return fetch(`${runtime.baseUrl}/v1/worlds/${worldId}/deployments`, {
    method: 'POST',
    headers: protocolHeaders(),
    body: JSON.stringify({
      problemId: 'contract-problem',
      runtime: {
        provider: 'contract',
        engine: 'declarative',
        entry: 'plan.json',
      },
      templateBody: '{"resources":[]}',
      simulationOverlay: {
        schemaVersion: '1',
        requirements: [
          {
            targetId: 'default',
            service: 'declarative',
            resourceType: 'ContractResource',
            operation: 'deploy',
            fidelity: 'L0',
            plane: 'deploy',
          },
        ],
      },
    }),
  });
}

function openRuntime(workloadEffects?: HttpWorkloadEffects): TestRuntime {
  const directory = mkdtempSync(path.join(tmpdir(), 'simulator-api-'));
  const store = new SimulationStore(path.join(directory, 'simulation.sqlite'));
  const registry = new ProviderRegistry([CONTRACT_PROVIDER]);
  const core = new SimulationCore(store, registry, {
    ...(workloadEffects === undefined ? {} : { workloadEffects }),
  });
  const app = createSimulatorApp({
    core,
    registry,
    consoleBaseUrl: 'http://127.0.0.1:9444/console/',
    resolveWorldNamespace: () => ({
      tenantId: 'tenant-a',
      eventId: 'event-a',
      teamId: 'team-a',
    }),
    signSnapshot,
    verifySnapshot,
  });
  const server = Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    fetch: app.fetch,
  });
  return {
    baseUrl: server.url.origin,
    app,
    core,
    server,
    store,
    directory,
    ...(workloadEffects === undefined ? {} : { workloadEffects }),
  };
}

async function replaceRuntime(
  workloadEffects: HttpWorkloadEffects
): Promise<void> {
  await runtime.server.stop(true);
  runtime.workloadEffects?.close();
  runtime.store.close();
  rmSync(runtime.directory, { recursive: true, force: true });
  runtime = openRuntime(workloadEffects);
}

beforeEach(() => {
  runtime = openRuntime();
});

afterEach(async () => {
  await runtime.server.stop(true);
  runtime.workloadEffects?.close();
  runtime.store.close();
  rmSync(runtime.directory, { recursive: true, force: true });
});

describe('Simulator HTTP protocol', () => {
  it('generic API は snapshot signer と verifier の明示 injection を必須にする', () => {
    const baseOptions = {
      core: runtime.core,
      registry: new ProviderRegistry([CONTRACT_PROVIDER]),
      consoleBaseUrl: 'http://127.0.0.1:9444/console/',
      resolveWorldNamespace: () => ({
        tenantId: 'tenant-a',
        eventId: 'event-a',
        teamId: 'team-a',
      }),
    };
    for (const incompleteOptions of [
      { ...baseOptions, signSnapshot },
      { ...baseOptions, verifySnapshot },
    ]) {
      expect(() =>
        Reflect.apply(createSimulatorApp, undefined, [incompleteOptions])
      ).toThrow('snapshot signer and verifier are required');
    }
  });

  it('capability は header なしで取得でき、mutation は完全一致 header を要求する', async () => {
    const capabilitiesResponse = await fetch(
      `${runtime.baseUrl}/v1/capabilities`
    );
    expect(capabilitiesResponse.status).toBe(200);
    expect(capabilitiesResponse.headers.get(PROTOCOL_HEADER)).toBe(
      SIMULATOR_PROTOCOL_VERSION
    );
    const capabilities: unknown = await capabilitiesResponse.json();
    assertSimulatorCapabilities(capabilities);
    const { contract } = capabilities.providers;
    const { declarative } = contract?.engines ?? {};
    expect(declarative).toMatchObject({
      operations: ['deploy'],
      resources: ['ContractResource', 'HTTP::Endpoint'],
      fidelity: ['contract', 'control', 'data-plane'],
    });
    expect(
      capabilities.capabilities?.some(
        (capability) => capability.resourceType === 'Runtime::Workload'
      )
    ).toBe(false);
    expect(
      capabilities.capabilities?.every(
        (capability) => capability.engine === 'declarative'
      )
    ).toBe(true);

    const requestBody = JSON.stringify({
      tenantId: 'tenant-a',
      eventId: 'event-a',
      teamId: 'team-a',
      deploymentId: 'deployment-header',
    });
    const missing = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: requestBody,
    });
    expect(missing.status).toBe(400);
    expect(missing.headers.get(PROTOCOL_HEADER)).toBe(
      SIMULATOR_PROTOCOL_VERSION
    );
    expect(await missing.json()).toMatchObject({
      error: { code: 'ProtocolVersionMismatch', retryable: false },
    });

    const mismatched = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: {
        'content-type': 'application/json',
        [PROTOCOL_HEADER]: '2026-07-10',
      },
      body: requestBody,
    });
    expect(mismatched.status).toBe(400);
    expect(await mismatched.json()).toMatchObject({
      error: { code: 'ProtocolVersionMismatch' },
    });
  });

  it('clock advance は正の安全整数だけを受理して適用済み transition を返す', async () => {
    const world = await createWorld('deployment-clock');
    const url = `${runtime.baseUrl}/v1/worlds/${world.worldId}/clock/advance`;
    const response = await fetch(url, {
      method: 'POST',
      headers: protocolHeaders(),
      body: JSON.stringify({ milliseconds: 2_500 }),
    });

    expect(response.status).toBe(200);
    const body: unknown = await response.json();
    assertSimulatorClockAdvanceResponse(body);
    expect(body).toEqual({
      clock: '1970-01-01T00:00:02.500Z',
      appliedTransitions: [],
    });
    expect(runtime.core.events(world.worldId).at(-1)?.type).toBe(
      'ClockAdvanced'
    );

    const rejected = await fetch(url, {
      method: 'POST',
      headers: protocolHeaders(),
      body: JSON.stringify({ milliseconds: 0 }),
    });
    expect(rejected.status).toBe(400);
    expect(await rejected.json()).toMatchObject({
      error: { code: 'ValidationFailed' },
    });
    expect(runtime.core.world(world.worldId).virtualTime).toBe(body.clock);
  });

  it('world と single、Composite deployment を作成、参照、削除できる', async () => {
    const world = await createWorld('deployment-single');
    expect(world.consoleUrl).toBe(
      `http://127.0.0.1:9444/console/${world.worldId}`
    );

    const repeatedWorld = await createWorld('deployment-single');
    expect(repeatedWorld).toEqual(world);

    const conflictedWorld = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: protocolHeaders(),
      body: JSON.stringify({
        tenantId: 'tenant-a',
        eventId: 'event-a',
        teamId: 'team-a',
        deploymentId: 'deployment-single',
        seed: 'different-seed',
      }),
    });
    expect(conflictedWorld.status).toBe(409);
    expect(await conflictedWorld.json()).toMatchObject({
      error: { code: 'IdempotencyConflict' },
    });

    const deployed = await deploySingle(world.worldId);
    expect(deployed.status).toBe(201);
    const deployment: unknown = await deployed.json();
    assertSimulatorDeploymentResponse(deployment);
    expect(deployment).toMatchObject({
      deploymentId: 'deployment-single',
      status: 'running',
      outputs: { resourceId: 'default-resource' },
    });
    expect((await deploySingle(world.worldId)).status).toBe(201);

    const read = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/deployments/deployment-single`
    );
    expect(read.status).toBe(200);
    expect(read.headers.get(PROTOCOL_HEADER)).toBe(SIMULATOR_PROTOCOL_VERSION);
    const readDeployment: unknown = await read.json();
    assertSimulatorDeploymentResponse(readDeployment);
    expect(readDeployment).toEqual(deployment);

    const resourcesResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/resources`
    );
    const resources: unknown = await resourcesResponse.json();
    assertSimulatorResourceProjection(resources);
    expect(resources.resources).toMatchObject([
      {
        resourceId: 'default-resource',
        status: 'ready',
        properties: {
          simulationOverlay: {
            schemaVersion: '1',
            requirements: [
              {
                targetId: 'default',
                service: 'declarative',
                resourceType: 'ContractResource',
                operation: 'deploy',
                fidelity: 'L0',
                plane: 'deploy',
              },
            ],
          },
        },
      },
    ]);

    const compositeWorld = await createWorld('deployment-composite');
    const compositeResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/${compositeWorld.worldId}/deployments`,
      {
        method: 'POST',
        headers: protocolHeaders(),
        body: JSON.stringify({
          problemId: 'composite-problem',
          runtime: {
            kind: 'composite',
            targets: [
              {
                id: 'alpha',
                provider: 'contract',
                engine: 'declarative',
                entry: 'alpha.json',
              },
              {
                id: 'beta',
                provider: 'contract',
                engine: 'declarative',
                entry: 'beta.json',
              },
            ],
          },
          templateBody: '{"resources":[]}',
        }),
      }
    );
    expect(compositeResponse.status).toBe(201);
    const composite: unknown = await compositeResponse.json();
    assertSimulatorDeploymentResponse(composite);
    expect(composite.outputs).toEqual({
      'alpha.resourceId': 'alpha-resource',
      'beta.resourceId': 'beta-resource',
    });

    const unversionedDelete = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}`,
      { method: 'DELETE' }
    );
    expect(unversionedDelete.status).toBe(400);
    expect(runtime.core.world(world.worldId).status).toBe('active');

    const deleted = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}`,
      { method: 'DELETE', headers: protocolHeaders() }
    );
    expect(deleted.status).toBe(204);
    expect(deleted.headers.get(PROTOCOL_HEADER)).toBe(
      SIMULATOR_PROTOCOL_VERSION
    );
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/${world.worldId}`, {
          method: 'DELETE',
          headers: protocolHeaders(),
        })
      ).status
    ).toBe(204);

    const deletedProjection = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/deployments/deployment-single`
    );
    const deletedBody: unknown = await deletedProjection.json();
    assertSimulatorDeploymentResponse(deletedBody);
    expect(deletedBody.status).toBe('deleted');
    const deletedResourcesResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/resources`
    );
    const deletedResources: unknown = await deletedResourcesResponse.json();
    assertSimulatorResourceProjection(deletedResources);
    expect(deletedResources.resources[0]?.status).toBe('deleted');
  });

  it('create response loss 後に deployment lookup で world を回復し replay と delete を完了できる', async () => {
    const recoveryKey = 'response-loss-create-key';
    const createResponse = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: { ...protocolHeaders(), 'idempotency-key': recoveryKey },
      body: JSON.stringify({
        tenantId: 'tenant-a',
        eventId: 'event-a',
        teamId: 'team-a',
        deploymentId: 'response-loss-deployment',
      }),
    });
    expect(createResponse.status).toBe(201);
    await createResponse.body?.cancel();

    const lookupUrl = `${runtime.baseUrl}/v1/worlds/by-deployment/response-loss-deployment`;
    expect((await fetch(lookupUrl)).status).toBe(404);
    const lookup = await fetch(lookupUrl, {
      headers: { 'idempotency-key': recoveryKey },
    });
    expect(lookup.status).toBe(200);
    expect(lookup.headers.get(PROTOCOL_HEADER)).toBe(
      SIMULATOR_PROTOCOL_VERSION
    );
    const recovered: unknown = await lookup.json();
    assertSimulatorWorldResponse(recovered);

    const replayResponse = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: { ...protocolHeaders(), 'idempotency-key': recoveryKey },
      body: JSON.stringify({
        tenantId: 'tenant-a',
        eventId: 'event-a',
        teamId: 'team-a',
        deploymentId: 'response-loss-deployment',
      }),
    });
    expect(replayResponse.status).toBe(201);
    const replayed: unknown = await replayResponse.json();
    assertSimulatorWorldResponse(replayed);
    expect(replayed).toEqual(recovered);
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/${replayed.worldId}`, {
          method: 'DELETE',
          headers: protocolHeaders(),
        })
      ).status
    ).toBe(204);

    const deletedLookup = await fetch(lookupUrl, {
      headers: { 'idempotency-key': recoveryKey },
    });
    expect(deletedLookup.status).toBe(200);
    expect(await deletedLookup.json()).toEqual(recovered);
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/${replayed.worldId}`, {
          method: 'DELETE',
          headers: protocolHeaders(),
        })
      ).status
    ).toBe(204);
    expect(
      (
        await fetch(
          `${runtime.baseUrl}/v1/worlds/by-deployment/missing-deployment`
        )
      ).status
    ).toBe(404);

    const defaultCreate = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: protocolHeaders(),
      body: JSON.stringify({
        tenantId: 'tenant-a',
        eventId: 'event-a',
        teamId: 'team-a',
        deploymentId: 'default-recovery-deployment',
      }),
    });
    expect(defaultCreate.status).toBe(201);
    const defaultCreated: unknown = await defaultCreate.json();
    assertSimulatorWorldResponse(defaultCreated);
    const defaultLookup = await fetch(
      `${runtime.baseUrl}/v1/worlds/by-deployment/default-recovery-deployment`
    );
    expect(defaultLookup.status).toBe(200);
    expect(await defaultLookup.json()).toEqual(defaultCreated);
  });

  it('deployment POST で workload を自動 materialize し失敗後は専用 route から再試行する', async () => {
    const effects = new HttpWorkloadEffects();
    await replaceRuntime(effects);
    effects.materializeFails = true;
    const capabilitiesResponse = await fetch(
      `${runtime.baseUrl}/v1/capabilities`
    );
    const capabilities: unknown = await capabilitiesResponse.json();
    assertSimulatorCapabilities(capabilities);
    expect(
      capabilities.capabilities?.some(
        (capability) =>
          capability.resourceType === 'Runtime::Workload' &&
          capability.operation === 'Materialize'
      )
    ).toBe(true);
    const world = await createWorld('deployment-workload');
    const deploymentUrl = `${runtime.baseUrl}/v1/worlds/${world.worldId}/deployments`;
    const deploymentBody = JSON.stringify({
      problemId: 'workload-problem',
      runtime: {
        provider: 'contract',
        engine: 'declarative',
        entry: 'plan.json',
      },
      templateBody: '{"resources":[]}',
      simulationOverlay: {
        schemaVersion: '1',
        workloads: [
          {
            id: 'api',
            targetId: 'default',
            resourceRef: 'ApiFunction',
            image: WORKLOAD_IMAGE,
            containerPort: 3000,
            healthPath: '/healthz',
          },
        ],
      },
    });

    const failed = await fetch(deploymentUrl, {
      method: 'POST',
      headers: protocolHeaders(),
      body: deploymentBody,
    });
    expect(failed.status).toBe(500);
    expect(await failed.json()).toMatchObject({
      error: { code: 'WorkloadEffectFailed', retryable: true },
    });
    expect(
      runtime.core.deployment(world.worldId, 'deployment-workload').status
    ).toBe('failed');
    const failedSnapshotResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`
    );
    expect(failedSnapshotResponse.status).toBe(200);
    const failedSnapshot: unknown = await failedSnapshotResponse.json();
    assertSimulatorSnapshot(failedSnapshot);
    const workloadRestore = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      {
        method: 'POST',
        headers: protocolHeaders(),
        body: JSON.stringify(failedSnapshot),
      }
    );
    expect(workloadRestore.status).toBe(201);
    const restoredWorld: unknown = await workloadRestore.json();
    assertSimulatorWorldResponse(restoredWorld);
    expect(restoredWorld.worldId).not.toBe(world.worldId);
    expect(
      runtime.core.deployment(restoredWorld.worldId, 'deployment-workload')
        .status
    ).toBe('ready');
    const restoredWorkload = runtime.core
      .resources(restoredWorld.worldId)
      .find((resource) => resource.resourceType === 'Runtime::Workload');
    expect(restoredWorkload).toMatchObject({
      status: 'ready',
      properties: {
        materialization: {
          endpoint: expect.stringMatching(/^http:\/\/127\.0\.0\.1:/),
        },
      },
    });

    const retryUrl = `${runtime.baseUrl}/v1/worlds/${world.worldId}/workloads/materialize`;
    const invalidRetry = await fetch(retryUrl, {
      method: 'POST',
      headers: protocolHeaders(),
      body: JSON.stringify({
        deploymentId: 'deployment-workload',
        extra: true,
      }),
    });
    expect(invalidRetry.status).toBe(400);

    const retried = await fetch(retryUrl, {
      method: 'POST',
      headers: protocolHeaders(),
      body: JSON.stringify({ deploymentId: 'deployment-workload' }),
    });
    expect(retried.status).toBe(200);
    const deployment: unknown = await retried.json();
    assertSimulatorDeploymentResponse(deployment);
    expect(deployment).toMatchObject({
      deploymentId: 'deployment-workload',
      status: 'running',
      outputs: {
        'Workload.api.Endpoint': expect.stringMatching(
          /^http:\/\/127\.0\.0\.1:/
        ),
      },
    });
    const resourcesResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/resources`
    );
    const resources: unknown = await resourcesResponse.json();
    assertSimulatorResourceProjection(resources);
    expect(
      resources.resources.find(
        (resource) => resource.resourceType === 'Runtime::Workload'
      )
    ).toMatchObject({ status: 'ready' });

    effects.cleanupFails = true;
    const failedDelete = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}`,
      { method: 'DELETE', headers: protocolHeaders() }
    );
    expect(failedDelete.status).toBe(500);
    expect(runtime.core.world(world.worldId).status).toBe('active');

    effects.cleanupFails = false;
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/${restoredWorld.worldId}`, {
          method: 'DELETE',
          headers: protocolHeaders(),
        })
      ).status
    ).toBe(204);
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/${world.worldId}`, {
          method: 'DELETE',
          headers: protocolHeaders(),
        })
      ).status
    ).toBe(204);
  });

  it('raw data-plane が method・query・UTF-8 body を保ちhop-by-hop responseを拒否する', async () => {
    const world = await createWorld('deployment-data-plane');
    expect((await deploySingle(world.worldId)).status).toBe(201);
    const url = `${runtime.baseUrl}/v1/worlds/${world.worldId}/data-plane/contract/default/search?page=1`;
    const options = {
      method: 'QUERY',
      headers: {
        authorization: 'Bearer must-not-reach-provider',
        'content-type': 'application/json',
        'idempotency-key': 'data-plane-query-repeat',
        'x-observed': 'yes',
      },
      body: JSON.stringify({ query: '天下' }),
    };

    const first = await fetch(url, options);
    expect(first.status).toBe(201);
    expect(first.headers.get('x-contract-method')).toBe('QUERY');
    const firstBody = await first.json();
    expect(firstBody).toMatchObject({
      method: 'QUERY',
      path: '/search?page=1',
      headers: {
        'content-type': 'application/json',
        'x-observed': 'yes',
      },
      body: JSON.stringify({ query: '天下' }),
    });
    expect(firstBody.headers).not.toHaveProperty('authorization');
    expect(
      runtime.core
        .events(world.worldId)
        .filter((event) => event.type === 'ContractHttpRequested')
    ).toHaveLength(1);

    expect((await fetch(url, options)).status).toBe(201);
    expect(
      runtime.core
        .events(world.worldId)
        .filter((event) => event.type === 'ContractHttpRequested')
    ).toHaveLength(1);
    expect(
      (
        await fetch(url, {
          ...options,
          body: JSON.stringify({ query: 'cloud' }),
        })
      ).status
    ).toBe(409);
    expect(
      runtime.core
        .events(world.worldId)
        .filter((event) => event.type === 'ContractHttpRequested')
    ).toHaveLength(1);

    expect(
      (
        await fetch(url, {
          method: 'QUERY',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ query: 'cloud' }),
        })
      ).status
    ).toBe(201);
    expect(
      runtime.core
        .events(world.worldId)
        .filter((event) => event.type === 'ContractHttpRequested')
    ).toHaveLength(2);

    const rawPost = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/data-plane/contract/default/search`,
      {
        method: 'POST',
        headers: { 'content-type': 'text/plain' },
        body: 'post-body',
      }
    );
    expect(rawPost.status).toBe(201);

    const oversized = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/data-plane/contract/default/search`,
      { method: 'PUT', body: 'x'.repeat(64 * 1024 + 1) }
    );
    expect(oversized.status).toBe(429);
    const invalidUtf8 = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/data-plane/contract/default/search`,
      { method: 'QUERY', body: new Uint8Array([0xc3, 0x28]) }
    );
    expect(invalidUtf8.status).toBe(400);

    const invalidResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/data-plane/contract/default/invalid-response`
    );
    expect(invalidResponse.status).toBe(400);
    expect(await invalidResponse.json()).toMatchObject({
      error: { code: 'ValidationFailed' },
    });

    for (const suffix of [
      'invalid-status',
      'invalid-empty-status-body',
      'invalid-headers',
      'invalid-body',
    ]) {
      const invalidProviderResponse = await fetch(
        `${runtime.baseUrl}/v1/worlds/${world.worldId}/data-plane/contract/default/${suffix}`
      );
      expect(invalidProviderResponse.status).toBe(400);
    }
    expect(
      (
        await fetch(
          `${runtime.baseUrl}/v1/worlds/${world.worldId}/data-plane/contract/default/empty-status`
        )
      ).status
    ).toBe(204);
    const head = await fetch(url, { method: 'HEAD' });
    expect(head.status).toBe(201);
    expect(await head.text()).toBe('');

    const directUrl = `http://simulator.test/v1/worlds/${world.worldId}/data-plane/contract/default`;
    const invalidRequests: readonly [string, RequestInit][] = [
      [`${directUrl}//authority`, { method: 'GET' }],
      [`${directUrl}/${'x'.repeat(2049)}`, { method: 'GET' }],
    ];
    expect(
      await Promise.all(
        invalidRequests.map(
          async ([invalidUrl, init]) =>
            (await runtime.app.request(invalidUrl, init)).status
        )
      )
    ).toEqual(invalidRequests.map(() => 400));
    expect(() => dataPlaneIdentifier('CONTRACT', 'provider')).toThrow();
    expect(() => dataPlaneIdentifier(undefined, 'provider')).toThrow();
    expect(() => dataPlaneMethod('A'.repeat(33))).toThrow();
    expect(() => dataPlaneHeaders(new Headers({ te: 'trailers' }))).toThrow();
    expect(() =>
      dataPlaneHeaders(new Headers({ 'x-large': 'x'.repeat(8193) }))
    ).toThrow();
    expect(() =>
      dataPlaneHeaders(
        new Headers(
          Object.fromEntries(
            Array.from({ length: 65 }, (_, index) => [
              `x-data-${index}`,
              'value',
            ])
          )
        )
      )
    ).toThrow();

    const missingTarget = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/data-plane/contract/missing/search`
    );
    expect(missingTarget.status).toBe(404);
    const missingProvider = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/data-plane/missing/default/search`
    );
    expect(missingProvider.status).toBe(422);
  });

  it('unsupported capability は resource 作成前に拒否して診断 event を残す', async () => {
    const world = await createWorld('deployment-unsupported');
    const response = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/deployments`,
      {
        method: 'POST',
        headers: protocolHeaders(),
        body: JSON.stringify({
          problemId: 'unsupported-problem',
          runtime: {
            provider: 'missing-provider',
            engine: 'missing-engine',
            entry: 'missing.yaml',
          },
          templateBody: '{}',
        }),
      }
    );
    expect(response.status).toBe(422);
    expect(await response.json()).toMatchObject({
      error: {
        code: 'UnsupportedCapability',
        diagnostics: [
          {
            code: 'MissingProvider',
            provider: 'missing-provider',
            engine: 'missing-engine',
            message: 'missing-provider/missing-engine/missing-engine/*/deploy',
          },
        ],
      },
    });
    expect(runtime.store.resources(world.worldId)).toEqual([]);
    expect(
      (
        await fetch(
          `${runtime.baseUrl}/v1/worlds/${world.worldId}/data-plane/contract/default/search`
        )
      ).status
    ).toBe(409);

    const eventsResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/events`
    );
    expect(eventsResponse.status).toBe(200);
    const eventBody: unknown = await eventsResponse.json();
    assertSimulatorEventPage(eventBody);
    expect(eventBody).toMatchObject({
      events: [{ type: 'WorldCreated' }, { type: 'DeploymentRejected' }],
      nextCursor: 2,
    });
    expect(JSON.stringify(eventBody)).not.toContain('ResourceDeclared');

    const snapshotResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`
    );
    const snapshot: unknown = await snapshotResponse.json();
    assertSimulatorSnapshot(snapshot);
    const restored = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      {
        method: 'POST',
        headers: protocolHeaders(),
        body: JSON.stringify(snapshot),
      }
    );
    expect(restored.status).toBe(201);
  });

  it('provider command、event、内部 failure を共通 HTTP 境界で扱う', async () => {
    const world = await createWorld('deployment-command');
    expect((await deploySingle(world.worldId)).status).toBe(201);

    const commandUrl = `${runtime.baseUrl}/v1/worlds/${world.worldId}/providers/contract/operations/update`;
    const commandBody = JSON.stringify({
      deploymentId: 'deployment-command',
      targetId: 'default',
      engine: 'declarative',
      service: 'resources',
      resourceType: 'ContractResource',
      input: { resourceId: 'default-resource' },
    });
    const updated = await fetch(commandUrl, {
      method: 'POST',
      headers: protocolHeaders(),
      body: commandBody,
    });
    expect(updated.status).toBe(200);
    expect(await updated.json()).toEqual({
      resourceId: 'default-resource',
      updated: true,
    });

    const eventCount = runtime.core.events(world.worldId).length;
    expect(
      (
        await fetch(commandUrl, {
          method: 'POST',
          headers: protocolHeaders(),
          body: commandBody,
        })
      ).status
    ).toBe(200);
    expect(runtime.core.events(world.worldId)).toHaveLength(eventCount);

    const unsupported = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/providers/contract/operations/missing`,
      {
        method: 'POST',
        headers: protocolHeaders(),
        body: commandBody,
      }
    );
    expect(unsupported.status).toBe(422);
    expect(await unsupported.json()).toMatchObject({
      error: { code: 'UnsupportedCapability' },
    });

    const failed = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/providers/contract/operations/explode`,
      {
        method: 'POST',
        headers: protocolHeaders(),
        body: commandBody,
      }
    );
    expect(failed.status).toBe(500);
    const failedBody: unknown = await failed.json();
    expect(failedBody).toMatchObject({
      error: { code: 'InternalError', retryable: true },
    });
    expect(JSON.stringify(failedBody)).not.toContain(
      'contract provider failed'
    );

    const events = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/events`
    );
    const eventPageBody: unknown = await events.json();
    assertSimulatorEventPage(eventPageBody);
    const eventJson = JSON.stringify(eventPageBody);
    expect(eventJson).toContain('"sequence":1');
    expect(eventJson).toContain(
      '"virtualTimestamp":"1970-01-01T00:00:00.000Z"'
    );
    expect(eventJson).toContain('ContractResourceUpdated');
  });

  it('event replay を 100 件に制限し cursor から SSE 再接続できる', async () => {
    const world = await createWorld('deployment-events');
    for (let index = 0; index < 105; index += 1) {
      runtime.core.advanceClock(world.worldId, 1);
    }

    const firstPageResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/events`
    );
    const firstPage: unknown = await firstPageResponse.json();
    assertSimulatorEventPage(firstPage);
    expect(firstPage.events).toHaveLength(MAX_EVENT_PAGE_SIZE);
    expect(firstPage.nextCursor).toBe(100);

    const stream = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/events/stream?after=100`
    );
    expect(stream.status).toBe(200);
    expect(stream.headers.get('content-type')).toContain('text/event-stream');
    expect(stream.headers.get(PROTOCOL_HEADER)).toBe(
      SIMULATOR_PROTOCOL_VERSION
    );
    expect(stream.headers.get(NEXT_CURSOR_HEADER)).toBe('106');
    const streamBody = await stream.text();
    expect(streamBody).toContain('id: 101');
    expect(streamBody).toContain('event: ClockAdvanced');
    expect(streamBody).not.toContain('WorldCreated');

    const reconnected = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/events/stream`,
      { headers: { 'last-event-id': '106' } }
    );
    expect(reconnected.headers.get(NEXT_CURSOR_HEADER)).toBe('106');
    expect(await reconnected.text()).toBe('');

    const invalidCursor = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/events?after=-1`
    );
    expect(invalidCursor.status).toBe(400);
    const unsafeCursor = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/events?after=999999999999999999999`
    );
    expect(unsafeCursor.status).toBe(400);
  });

  it('snapshot を公開 schema で export し、厳密検証後に新 world へ復元する', async () => {
    const world = await createWorld('deployment-snapshot');
    expect((await deploySingle(world.worldId)).status).toBe(201);

    const exported = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`
    );
    expect(exported.status).toBe(200);
    const snapshot: unknown = await exported.json();
    assertSimulatorSnapshot(snapshot);
    const graphJson = JSON.stringify(snapshot.resourceGraph);
    expect(graphJson).toContain(`"worldId":"${world.worldId}"`);
    expect(graphJson).toContain('WorldCreated');
    expect(graphJson).toContain('"deploymentId":"deployment-snapshot"');
    expect(graphJson).toContain('"targetId":"default"');
    expect(graphJson).toContain('"resourceId":"default-resource"');

    const { integrityProof: _integrityProof, ...unsignedSnapshot } = snapshot;
    const unsignedRestore = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      {
        method: 'POST',
        headers: protocolHeaders(),
        body: JSON.stringify(unsignedSnapshot),
      }
    );
    expect(unsignedRestore.status).toBe(400);
    expect(await unsignedRestore.json()).toMatchObject({
      error: { code: 'ValidationFailed' },
    });
    const invalidProofRestore = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      {
        method: 'POST',
        headers: protocolHeaders(),
        body: JSON.stringify({
          ...snapshot,
          integrityProof: {
            ...snapshot.integrityProof,
            value: 'B'.repeat(43),
          },
        }),
      }
    );
    expect(invalidProofRestore.status).toBe(400);

    const restoreKey = 'snapshot-response-loss-key';
    const restoredResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      {
        method: 'POST',
        headers: { ...protocolHeaders(), 'idempotency-key': restoreKey },
        body: JSON.stringify(snapshot),
      }
    );
    expect(restoredResponse.status).toBe(201);
    await restoredResponse.body?.cancel();

    const restoreLookupUrl = `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots/restores/${snapshot.hash}`;
    expect((await fetch(restoreLookupUrl)).status).toBe(404);
    for (const invalidHash of ['A'.repeat(64), 'a'.repeat(65)]) {
      expect(
        (
          await fetch(
            `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots/restores/${invalidHash}`,
            { headers: { 'idempotency-key': restoreKey } }
          )
        ).status
      ).toBe(404);
    }
    const restoreLookup = await fetch(restoreLookupUrl, {
      headers: { 'idempotency-key': restoreKey },
    });
    expect(restoreLookup.status).toBe(200);
    const restored: unknown = await restoreLookup.json();
    assertSimulatorWorldResponse(restored);
    expect(restored.worldId).not.toBe(world.worldId);
    const restoreReplay = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      {
        method: 'POST',
        headers: { ...protocolHeaders(), 'idempotency-key': restoreKey },
        body: JSON.stringify(snapshot),
      }
    );
    expect(restoreReplay.status).toBe(201);
    expect(await restoreReplay.json()).toEqual(restored);

    const restoredEvents = await fetch(
      `${runtime.baseUrl}/v1/worlds/${restored.worldId}/events`
    );
    expect(JSON.stringify(await restoredEvents.json())).toContain(
      'SnapshotRestored'
    );

    const incompatible = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      {
        method: 'POST',
        headers: { ...protocolHeaders(), 'idempotency-key': restoreKey },
        body: JSON.stringify(
          signedSnapshot({ ...unsignedSnapshot, hash: 'b'.repeat(64) })
        ),
      }
    );
    expect(incompatible.status).toBe(422);
    expect(await incompatible.json()).toMatchObject({
      error: { code: 'SnapshotIncompatible' },
    });

    const malformed = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      {
        method: 'POST',
        headers: protocolHeaders(),
        body: JSON.stringify(
          signedSnapshot({
            ...unsignedSnapshot,
            resourceGraph: {
              ...snapshot.resourceGraph,
              resources: 'invalid',
            },
          })
        ),
      }
    );
    expect(malformed.status).toBe(400);
    expect(await malformed.json()).toMatchObject({
      error: { code: 'ValidationFailed' },
    });

    const targetlessSnapshot = structuredClone(unsignedSnapshot);
    const targetlessResources = targetlessSnapshot.resourceGraph['resources'];
    if (!Array.isArray(targetlessResources)) {
      throw new Error('snapshot resource fixture がありません');
    }
    const targetlessResource = targetlessResources[0];
    if (
      typeof targetlessResource !== 'object' ||
      targetlessResource === null ||
      Array.isArray(targetlessResource)
    ) {
      throw new Error('snapshot resource fixture が不正です');
    }
    Reflect.deleteProperty(targetlessResource, 'targetId');
    const targetless = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      {
        method: 'POST',
        headers: protocolHeaders(),
        body: JSON.stringify(signedSnapshot(targetlessSnapshot)),
      }
    );
    expect(targetless.status).toBe(400);
    expect(await targetless.json()).toMatchObject({
      error: { code: 'ValidationFailed' },
    });

    const mismatchedRoute = await fetch(
      `${runtime.baseUrl}/v1/worlds/different-world/snapshots`,
      {
        method: 'POST',
        headers: protocolHeaders(),
        body: JSON.stringify(snapshot),
      }
    );
    expect(mismatchedRoute.status).toBe(400);

    const mismatchedEnvelope = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      {
        method: 'POST',
        headers: protocolHeaders(),
        body: JSON.stringify(
          signedSnapshot({ ...unsignedSnapshot, seed: 'different-seed' })
        ),
      }
    );
    expect(mismatchedEnvelope.status).toBe(400);
  });

  it('不正 JSON、1 MiB 超過、未知 route を構造化 error にする', async () => {
    const invalidJson = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: protocolHeaders(),
      body: '{',
    });
    expect(invalidJson.status).toBe(400);
    expect(await invalidJson.json()).toMatchObject({
      error: { code: 'ValidationFailed' },
    });

    const invalidContract = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: protocolHeaders(),
      body: JSON.stringify({ teamId: '' }),
    });
    expect(invalidContract.status).toBe(400);
    expect(await invalidContract.json()).toMatchObject({
      error: { code: 'ValidationFailed' },
    });

    const commandUrl = `${runtime.baseUrl}/v1/worlds/missing/providers/contract/operations/update`;
    const missingInput = await fetch(commandUrl, {
      method: 'POST',
      headers: protocolHeaders(),
      body: '{}',
    });
    expect(missingInput.status).toBe(400);

    const missingText = await fetch(commandUrl, {
      method: 'POST',
      headers: protocolHeaders(),
      body: JSON.stringify({ input: {} }),
    });
    expect(missingText.status).toBe(400);

    const oversized = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: { ...protocolHeaders(), connection: 'close' },
      body: JSON.stringify({ padding: 'x'.repeat(MAX_REQUEST_BODY_BYTES) }),
    });
    expect(oversized.status).toBe(413);
    expect(await oversized.json()).toMatchObject({
      error: { code: 'QuotaExceeded' },
    });

    const missing = await fetch(`${runtime.baseUrl}/v1/unknown`);
    expect(missing.status).toBe(404);
    expect(await missing.json()).toMatchObject({
      error: { code: 'NotFound' },
    });
  });
});
