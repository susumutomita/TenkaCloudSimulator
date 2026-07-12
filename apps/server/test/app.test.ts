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
    const snapshotResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      { headers: headers(runtime.token, false) }
    );
    const snapshot: unknown = await snapshotResponse.json();
    assertSimulatorSnapshot(snapshot);
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
    const restored = await fetch(
      `${runtime.baseUrl}/v1/worlds/${world.worldId}/snapshots`,
      {
        method: 'POST',
        headers: headers(runtime.token),
        body: JSON.stringify(snapshot),
      }
    );
    expect(restored.status).toBe(201);
  });
});
