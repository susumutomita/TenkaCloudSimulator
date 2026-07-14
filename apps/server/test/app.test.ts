import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  assertSimulatorClockAdvanceResponse,
  assertSimulatorErrorEnvelope,
  assertSimulatorSnapshot,
  assertSimulatorWorldResponse,
  SIMULATOR_PROTOCOL_VERSION,
  type SimulatorSnapshot,
} from '@tenkacloud/simulator-contracts';
import {
  contentHash,
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import { GcpProvider } from '@tenkacloud/simulator-provider-gcp';
import { createAuthenticatedSimulatorApp } from '../src/app';
import { LaunchTokenAuthority } from '../src/auth';

const SECRET = 'server-secret-0123456789abcdef0123';
const NAMESPACE = {
  tenantId: 'tenant-server',
  eventId: 'event-server',
  teamId: 'team-server',
  deploymentId: 'deployment-server',
};

interface TestRuntime {
  readonly authority: LaunchTokenAuthority;
  readonly baseUrl: string;
  readonly directory: string;
  readonly server: Bun.Server<undefined>;
  readonly store: SimulationStore;
  readonly token: string;
}

let runtime: TestRuntime;

function headers(token: string, mutation = true): HeadersInit {
  return {
    authorization: `Bearer ${token}`,
    ...(mutation
      ? {
          'content-type': 'application/json',
          'x-tenkacloud-simulator-protocol': SIMULATOR_PROTOCOL_VERSION,
        }
      : {}),
  };
}

function worldBody(overrides: Readonly<Record<string, string>> = {}): string {
  return JSON.stringify({ ...NAMESPACE, ...overrides });
}

async function createWorld(): Promise<{ worldId: string; consoleUrl: string }> {
  const response = await fetch(`${runtime.baseUrl}/v1/worlds`, {
    method: 'POST',
    headers: headers(runtime.token),
    body: worldBody(),
  });
  expect(response.status).toBe(201);
  const body: unknown = await response.json();
  assertSimulatorWorldResponse(body);
  return body;
}

function requiredRecord(
  value: unknown,
  label: string
): Readonly<Record<string, unknown>> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`${label} is missing`);
  }
  return Object.fromEntries(Object.entries(value));
}

function requiredArray(
  record: Readonly<Record<string, unknown>>,
  key: string
): readonly unknown[] {
  const value = record[key];
  if (!Array.isArray(value)) throw new Error(`${key} fixture is missing`);
  return value;
}

function rehashedSnapshot(
  snapshot: SimulatorSnapshot,
  resourceGraph: Readonly<Record<string, unknown>>
): SimulatorSnapshot {
  const candidate: unknown = {
    ...snapshot,
    resourceGraph,
    hash: contentHash({
      snapshotVersion: '1',
      world: resourceGraph['world'],
      events: resourceGraph['events'],
      deployments: resourceGraph['deployments'],
      resources: resourceGraph['resources'],
    }),
  };
  assertSimulatorSnapshot(candidate);
  return candidate;
}

beforeEach(async () => {
  const directory = await mkdtemp(join(tmpdir(), 'simulator-server-'));
  const store = new SimulationStore(join(directory, 'simulation.sqlite'));
  const registry = new ProviderRegistry([new GcpProvider()]);
  const core = new SimulationCore(store, registry);
  const authority = new LaunchTokenAuthority(SECRET);
  const app = createAuthenticatedSimulatorApp({
    core,
    registry,
    consoleBaseUrl: 'http://127.0.0.1:9444/console',
    launchTokens: authority,
  });
  const server = Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    fetch: app.fetch,
  });
  runtime = {
    authority,
    baseUrl: server.url.origin,
    directory,
    server,
    store,
    token: authority.issue(NAMESPACE),
  };
});

afterEach(async () => {
  await runtime.server.stop(true);
  runtime.store.close();
  await rm(runtime.directory, { recursive: true, force: true });
});

describe('Authenticated Simulator server', () => {
  it('capability discovery は公開し world API は simulator token を必須にする', async () => {
    expect((await fetch(`${runtime.baseUrl}/v1/capabilities`)).status).toBe(
      200
    );
    const unauthorized = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: {
        'content-type': 'application/json',
        'x-tenkacloud-simulator-protocol': SIMULATOR_PROTOCOL_VERSION,
      },
      body: worldBody(),
    });
    expect(unauthorized.status).toBe(401);
    const error: unknown = await unauthorized.json();
    assertSimulatorErrorEnvelope(error);
    expect(error.error.code).toBe('UnauthorizedOperation');
    expect(unauthorized.headers.get('x-tenkacloud-simulator-protocol')).toBe(
      SIMULATOR_PROTOCOL_VERSION
    );
  });

  it('token namespace と deployment が一致する world だけを操作できる', async () => {
    const mismatched = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: headers(runtime.token),
      body: worldBody({ teamId: 'other-team' }),
    });
    expect(mismatched.status).toBe(404);

    const world = await createWorld();
    const observed = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/events`,
      { headers: headers(runtime.token, false) }
    );
    expect(observed.status).toBe(200);
    const clockResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/clock/advance`,
      {
        method: 'POST',
        headers: headers(runtime.token),
        body: JSON.stringify({ milliseconds: 1_000 }),
      }
    );
    expect(clockResponse.status).toBe(200);
    const clock: unknown = await clockResponse.json();
    assertSimulatorClockAdvanceResponse(clock);
    expect(clock.clock).toBe('1970-01-01T00:00:01.000Z');

    const otherToken = runtime.authority.issue({
      ...NAMESPACE,
      teamId: 'other-team',
    });
    const isolated = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/events`,
      { headers: headers(otherToken, false) }
    );
    expect(isolated.status).toBe(404);
    expect(
      (
        await fetch(
          `${runtime.baseUrl}/v1/worlds/${world.worldId}/clock/advance`,
          {
            method: 'POST',
            headers: headers(otherToken),
            body: JSON.stringify({ milliseconds: 1_000 }),
          }
        )
      ).status
    ).toBe(404);
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/missing/events`, {
          headers: headers(runtime.token, false),
        })
      ).status
    ).toBe(404);
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/`, {
          headers: headers(runtime.token, false),
        })
      ).status
    ).toBe(404);
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/%/events`, {
          headers: headers(runtime.token, false),
        })
      ).status
    ).toBe(404);
  });

  it('create response loss 後の lookup を token namespace に限定し replay と cleanup を完了する', async () => {
    const recoveryKey = 'authenticated-response-loss-key';
    const lostResponse = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: { ...headers(runtime.token), 'idempotency-key': recoveryKey },
      body: worldBody(),
    });
    expect(lostResponse.status).toBe(201);
    await lostResponse.body?.cancel();

    const lookupUrl = `${runtime.baseUrl}/v1/worlds/by-deployment/${NAMESPACE.deploymentId}`;
    const recoveredResponse = await fetch(lookupUrl, {
      headers: {
        ...headers(runtime.token, false),
        'idempotency-key': recoveryKey,
      },
    });
    expect(recoveredResponse.status).toBe(200);
    const recovered: unknown = await recoveredResponse.json();
    assertSimulatorWorldResponse(recovered);

    const replay = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: { ...headers(runtime.token), 'idempotency-key': recoveryKey },
      body: worldBody(),
    });
    expect(replay.status).toBe(201);
    expect(await replay.json()).toEqual(recovered);
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/${recovered.worldId}`, {
          method: 'DELETE',
          headers: headers(runtime.token),
        })
      ).status
    ).toBe(204);
    expect(
      (
        await fetch(lookupUrl, {
          headers: {
            ...headers(runtime.token, false),
            'idempotency-key': recoveryKey,
          },
        })
      ).status
    ).toBe(200);
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/${recovered.worldId}`, {
          method: 'DELETE',
          headers: headers(runtime.token),
        })
      ).status
    ).toBe(204);

    const otherNamespaceToken = runtime.authority.issue({
      ...NAMESPACE,
      teamId: 'other-team',
    });
    expect(
      (
        await fetch(lookupUrl, {
          headers: {
            ...headers(otherNamespaceToken, false),
            'idempotency-key': recoveryKey,
          },
        })
      ).status
    ).toBe(404);
    const otherDeploymentToken = runtime.authority.issue({
      ...NAMESPACE,
      deploymentId: 'other-deployment',
    });
    expect(
      (
        await fetch(lookupUrl, {
          headers: {
            ...headers(otherDeploymentToken, false),
            'idempotency-key': recoveryKey,
          },
        })
      ).status
    ).toBe(404);
    const missingToken = runtime.authority.issue({
      ...NAMESPACE,
      deploymentId: 'missing-deployment',
    });
    expect(
      (
        await fetch(
          `${runtime.baseUrl}/v1/worlds/by-deployment/missing-deployment`,
          { headers: headers(missingToken, false) }
        )
      ).status
    ).toBe(404);
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/by-deployment/`, {
          headers: headers(runtime.token, false),
        })
      ).status
    ).toBe(404);
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/by-deployment/%`, {
          headers: headers(runtime.token, false),
        })
      ).status
    ).toBe(404);
  });

  it('body size、JSON、schema、snapshot namespace を token 境界で検証する', async () => {
    const noBody = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: headers(runtime.token),
    });
    expect(noBody.status).toBe(400);
    const invalidJson = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: headers(runtime.token),
      body: '{invalid',
    });
    expect(invalidJson.status).toBe(400);
    const invalidSchema = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: headers(runtime.token),
      body: '{}',
    });
    expect(invalidSchema.status).toBe(400);
    const oversized = await fetch(`${runtime.baseUrl}/v1/worlds`, {
      method: 'POST',
      headers: headers(runtime.token),
      body: JSON.stringify({ value: 'x'.repeat(1_048_577) }),
    });
    expect(oversized.status).toBe(413);

    const world = await createWorld();
    const invalidDeployment = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/deployments`,
      { method: 'POST', headers: headers(runtime.token), body: '{}' }
    );
    expect(invalidDeployment.status).toBe(400);
    const deployment = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/deployments`,
      {
        method: 'POST',
        headers: headers(runtime.token),
        body: JSON.stringify({
          problemId: 'snapshot-authenticity',
          runtime: {
            provider: 'gcp',
            engine: 'infra-manager',
            entry: 'main.tf',
          },
          templateBody: `resource "google_cloud_run_v2_service" "snapshot" {
  name = "snapshot-service"
  location = "asia-northeast1"
}`,
        }),
      }
    );
    expect(deployment.status).toBe(201);
    const snapshotResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      { headers: headers(runtime.token, false) }
    );
    const snapshot: unknown = await snapshotResponse.json();
    assertSimulatorSnapshot(snapshot);
    expect(JSON.stringify(snapshot)).not.toContain(SECRET);

    const graph = snapshot.resourceGraph;
    const resources = requiredArray(graph, 'resources');
    const resource = requiredRecord(resources[0], 'snapshot resource');
    const resourceProperties = requiredRecord(
      resource['properties'],
      'snapshot resource properties'
    );
    const events = requiredArray(graph, 'events');
    const event = requiredRecord(events[0], 'snapshot event');
    const deployments = requiredArray(graph, 'deployments');
    const projectedDeployment = requiredRecord(
      deployments[0],
      'snapshot deployment'
    );
    const outputs = requiredRecord(
      projectedDeployment['outputs'],
      'snapshot outputs'
    );
    const defaultOutputs = requiredRecord(
      outputs['default'],
      'snapshot default outputs'
    );
    const tamperedSnapshots = [
      rehashedSnapshot(snapshot, {
        ...graph,
        resources: [
          {
            ...resource,
            properties: { ...resourceProperties, callerTampered: true },
          },
          ...resources.slice(1),
        ],
      }),
      rehashedSnapshot(snapshot, {
        ...graph,
        events: [{ ...event, type: 'CallerTampered' }, ...events.slice(1)],
      }),
      rehashedSnapshot(snapshot, {
        ...graph,
        deployments: [
          { ...projectedDeployment, problemId: 'caller-tampered' },
          ...deployments.slice(1),
        ],
      }),
      rehashedSnapshot(snapshot, {
        ...graph,
        deployments: [
          {
            ...projectedDeployment,
            outputs: {
              ...outputs,
              default: {
                ...defaultOutputs,
                GcpHelloUrl: 'https://caller.invalid',
              },
            },
          },
          ...deployments.slice(1),
        ],
      }),
    ];
    const durableCounts = (): {
      worlds: number;
      events: number;
      deployments: number;
      resources: number;
      idempotency: number;
    } | null =>
      runtime.store.database
        .query<
          {
            worlds: number;
            events: number;
            deployments: number;
            resources: number;
            idempotency: number;
          },
          []
        >(
          `SELECT
             (SELECT COUNT(*) FROM worlds) AS worlds,
             (SELECT COUNT(*) FROM events) AS events,
             (SELECT COUNT(*) FROM deployments) AS deployments,
             (SELECT COUNT(*) FROM resources) AS resources,
             (SELECT COUNT(*) FROM idempotency) AS idempotency`
        )
        .get();
    const countsBeforeTampering = durableCounts();
    for (const [index, tampered] of tamperedSnapshots.entries()) {
      const rejected = await fetch(
        `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
        {
          method: 'POST',
          headers: {
            ...headers(runtime.token),
            'idempotency-key': `tampered-restore-${index}`,
          },
          body: JSON.stringify(tampered),
        }
      );
      expect(rejected.status).toBe(400);
      expect(await rejected.json()).toMatchObject({
        error: { code: 'ValidationFailed' },
      });
      expect(durableCounts()).toEqual(countsBeforeTampering);
    }

    const { integrityProof: _integrityProof, ...unsignedSnapshot } = snapshot;
    for (const malformedProof of [
      unsignedSnapshot,
      {
        ...snapshot,
        integrityProof: {
          ...snapshot.integrityProof,
          value: `${snapshot.integrityProof.value}=`,
        },
      },
      {
        ...snapshot,
        integrityProof: { ...snapshot.integrityProof, source: 'caller' },
      },
    ]) {
      const rejected = await fetch(
        `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
        {
          method: 'POST',
          headers: headers(runtime.token),
          body: JSON.stringify(malformedProof),
        }
      );
      expect(rejected.status).toBe(400);
      expect(durableCounts()).toEqual(countsBeforeTampering);
    }
    const changed: SimulatorSnapshot = {
      ...snapshot,
      namespace: { ...snapshot.namespace, teamId: 'other-team' },
    };
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`, {
          method: 'POST',
          headers: headers(runtime.token),
          body: JSON.stringify(changed),
        })
      ).status
    ).toBe(404);
    const projectedWorld = snapshot.resourceGraph['world'];
    if (
      projectedWorld === null ||
      typeof projectedWorld !== 'object' ||
      Array.isArray(projectedWorld)
    ) {
      throw new Error('snapshot projected world is missing');
    }
    const mismatchedWorld = {
      ...projectedWorld,
      deploymentId: 'other-deployment',
    };
    const mismatchedGraph: SimulatorSnapshot['resourceGraph'] = {
      ...snapshot.resourceGraph,
      world: mismatchedWorld,
    };
    const mismatchedDeployment: SimulatorSnapshot = {
      ...snapshot,
      resourceGraph: mismatchedGraph,
      hash: contentHash({
        snapshotVersion: '1',
        world: mismatchedWorld,
        events: mismatchedGraph['events'],
        deployments: mismatchedGraph['deployments'],
        resources: mismatchedGraph['resources'],
      }),
    };
    const countsBefore = durableCounts();
    expect(
      (
        await fetch(`${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`, {
          method: 'POST',
          headers: {
            ...headers(runtime.token),
            'idempotency-key': 'mismatched-deployment-restore',
          },
          body: JSON.stringify(mismatchedDeployment),
        })
      ).status
    ).toBe(404);
    expect(durableCounts()).toEqual(countsBefore);
    const restored = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      {
        method: 'POST',
        headers: {
          ...headers(runtime.token),
          'idempotency-key': 'server-restore-key',
        },
        body: JSON.stringify(snapshot),
      }
    );
    expect(restored.status).toBe(201);
    await restored.body?.cancel();
    const restoreLookupUrl = `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots/restores/${snapshot.hash}`;
    const restoredLookup = await fetch(restoreLookupUrl, {
      headers: {
        ...headers(runtime.token, false),
        'idempotency-key': 'server-restore-key',
      },
    });
    expect(restoredLookup.status).toBe(200);
    const restoredBody: unknown = await restoredLookup.json();
    assertSimulatorWorldResponse(restoredBody);
    const replayedRestore = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      {
        method: 'POST',
        headers: {
          ...headers(runtime.token),
          'idempotency-key': 'server-restore-key',
        },
        body: JSON.stringify(snapshot),
      }
    );
    expect(replayedRestore.status).toBe(201);
    expect(await replayedRestore.json()).toEqual(restoredBody);
    for (const worldId of [restoredBody.worldId, world.worldId]) {
      expect(
        (
          await fetch(`${runtime.baseUrl}/v1/worlds/${worldId}`, {
            method: 'DELETE',
            headers: headers(runtime.token),
          })
        ).status
      ).toBe(204);
    }
    const deletedLookup = await fetch(restoreLookupUrl, {
      headers: {
        ...headers(runtime.token, false),
        'idempotency-key': 'server-restore-key',
      },
    });
    expect(deletedLookup.status).toBe(200);
    expect(await deletedLookup.json()).toEqual(restoredBody);
    for (const worldId of [restoredBody.worldId, world.worldId]) {
      expect(
        (
          await fetch(`${runtime.baseUrl}/v1/worlds/${worldId}`, {
            method: 'DELETE',
            headers: headers(runtime.token),
          })
        ).status
      ).toBe(204);
    }
    const otherToken = runtime.authority.issue({
      ...NAMESPACE,
      teamId: 'other-team',
    });
    expect(
      (
        await fetch(restoreLookupUrl, {
          headers: {
            ...headers(otherToken, false),
            'idempotency-key': 'server-restore-key',
          },
        })
      ).status
    ).toBe(404);
  });
});
