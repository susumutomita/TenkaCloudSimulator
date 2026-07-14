import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import { createHmac, timingSafeEqual } from 'node:crypto';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createSimulatorApp } from '@tenkacloud/simulator-api';
import {
  assertSimulatorCapabilities,
  assertSimulatorDeploymentResponse,
  assertSimulatorEventPage,
  assertSimulatorResourceProjection,
  assertSimulatorSnapshot,
  assertSimulatorWorldResponse,
  canonicalSimulatorSnapshotIntegrityPayload,
  SIMULATOR_SNAPSHOT_INTEGRITY_ALGORITHM,
  SIMULATOR_SNAPSHOT_INTEGRITY_VERSION,
  type SimulatorSnapshot,
  type SimulatorSnapshotEnvelope,
  type SimulatorSnapshotIntegrityProof,
} from '@tenkacloud/simulator-contracts';
import {
  type MaterializedWorkload,
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
  type WorkloadDeclaration,
  type WorkloadEffectPort,
} from '@tenkacloud/simulator-core';
import {
  CLOUD_RUN_SERVICE,
  GcpProvider,
} from '@tenkacloud/simulator-provider-gcp';
import { runCli } from '../src/cli';
import {
  assertSimulatorDeleteResponse,
  DEFAULT_SIMULATOR_CLIENT_TIMEOUT_POLICY,
  decodeSimulatorResponse,
  parseProviderOperationResponse,
  SimulatorClient,
  SimulatorClientError,
  type SimulatorClientTimeoutPolicy,
} from '../src/client';

class BufferOutput {
  value = '';

  write(value: string): void {
    this.value += value;
  }
}

interface Invocation {
  readonly code: 0 | 1 | 2;
  readonly stderr: string;
  readonly stdout: string;
}

interface TestRuntime {
  readonly baseUrl: string;
  readonly core: SimulationCore;
  readonly directory: string;
  readonly server: Bun.Server<undefined>;
  readonly store: SimulationStore;
  readonly workloadEffects?: QuietWindowWorkloadEffects;
}

let runtime: TestRuntime;

const SNAPSHOT_INTEGRITY_SECRET = 'cli-test-snapshot-secret-0123456789abcdef';
const WORKLOAD_IMAGE = `ghcr.io/tenkacloud/cli-timeout@sha256:${'a'.repeat(64)}`;

class QuietWindowWorkloadEffects implements WorkloadEffectPort {
  readonly #servers = new Map<string, Bun.Server<undefined>[]>();

  constructor(readonly cleanupQuietMilliseconds: number) {}

  async materialize(
    worldId: string,
    declarations: readonly WorkloadDeclaration[]
  ): Promise<readonly MaterializedWorkload[]> {
    const servers = this.#servers.get(worldId) ?? [];
    const materialized = declarations.map((declaration) => {
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
    return materialized;
  }

  async cleanup(worldId: string): Promise<void> {
    for (const server of this.#servers.get(worldId) ?? []) server.stop(true);
    this.#servers.delete(worldId);
    await Bun.sleep(this.cleanupQuietMilliseconds);
  }

  close(): void {
    for (const servers of this.#servers.values()) {
      for (const server of servers) server.stop(true);
    }
    this.#servers.clear();
  }
}

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

function verifySnapshot(snapshot: SimulatorSnapshot): boolean {
  const { integrityProof, ...envelope } = snapshot;
  const expected = Buffer.from(snapshotProofValue(envelope), 'ascii');
  const provided = Buffer.from(integrityProof.value, 'ascii');
  return timingSafeEqual(expected, provided);
}

async function invoke(
  args: readonly string[],
  timeoutPolicy?: SimulatorClientTimeoutPolicy
): Promise<Invocation> {
  const stdout = new BufferOutput();
  const stderr = new BufferOutput();
  const code = await runCli(args, stdout, stderr, timeoutPolicy);
  return { code, stdout: stdout.value, stderr: stderr.value };
}

function parseJson(value: string): unknown {
  return JSON.parse(value);
}

async function openRuntime(
  workloadEffects?: QuietWindowWorkloadEffects
): Promise<TestRuntime> {
  const directory = await mkdtemp(join(tmpdir(), 'simulator-cli-'));
  const store = new SimulationStore(join(directory, 'simulation.sqlite'));
  const registry = new ProviderRegistry([new GcpProvider()]);
  const core = new SimulationCore(store, registry, {
    ...(workloadEffects === undefined ? {} : { workloadEffects }),
  });
  const app = createSimulatorApp({
    core,
    registry,
    consoleBaseUrl: 'http://127.0.0.1:9444/console',
    resolveWorldNamespace: () => ({
      tenantId: 'tenant-cli',
      eventId: 'event-cli',
      teamId: 'team-cli',
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
    core,
    directory,
    server,
    store,
    ...(workloadEffects === undefined ? {} : { workloadEffects }),
  };
}

async function replaceRuntime(
  workloadEffects: QuietWindowWorkloadEffects
): Promise<void> {
  await runtime.server.stop(true);
  runtime.workloadEffects?.close();
  runtime.store.close();
  await rm(runtime.directory, { recursive: true, force: true });
  runtime = await openRuntime(workloadEffects);
}

async function createWorkloadWorld(deploymentId: string): Promise<string> {
  const client = new SimulatorClient(runtime.baseUrl);
  const world = await client.createWorld({
    tenantId: 'tenant-cli',
    eventId: 'event-cli',
    teamId: 'team-cli',
    deploymentId,
  });
  const templateBody = await readFile(
    new URL('./fixtures/main.tf', import.meta.url),
    'utf8'
  );
  const deployment = await client.createDeployment(world.worldId, {
    problemId: deploymentId,
    runtime: {
      provider: 'gcp',
      engine: 'infra-manager',
      entry: 'main.tf',
    },
    templateBody,
    simulationOverlay: {
      schemaVersion: '1',
      workloads: [
        {
          id: 'api',
          targetId: 'default',
          resourceRef: 'google_cloud_run_v2_service.hello',
          image: WORKLOAD_IMAGE,
          containerPort: 8080,
          healthPath: '/',
        },
      ],
    },
  });
  expect(deployment.status).toBe('running');
  return world.worldId;
}

beforeEach(async () => {
  runtime = await openRuntime();
});

afterEach(async () => {
  await runtime.server.stop(true);
  runtime.workloadEffects?.close();
  runtime.store.close();
  await rm(runtime.directory, { recursive: true, force: true });
});

describe('Simulator CLI', () => {
  it('実 HTTP と SQLite を介して lifecycle、operation、snapshot を同じ world へ反映する', async () => {
    const capabilities = await invoke([
      'capabilities',
      '--url',
      runtime.baseUrl,
      '--token',
      'tc_sim_v1.test.signature',
    ]);
    expect(capabilities.code).toBe(0);
    const capabilityBody = parseJson(capabilities.stdout);
    assertSimulatorCapabilities(capabilityBody);
    expect(capabilityBody.providers['gcp']).toBeDefined();

    const created = await invoke([
      'world-create',
      '--url',
      runtime.baseUrl,
      '--tenant',
      'tenant-cli',
      '--event',
      'event-cli',
      '--team',
      'team-cli',
      '--deployment',
      'cli-deployment',
    ]);
    const world = parseJson(created.stdout);
    assertSimulatorWorldResponse(world);

    const runtimePath = join(runtime.directory, 'runtime.json');
    await writeFile(
      runtimePath,
      JSON.stringify({
        provider: 'gcp',
        engine: 'infra-manager',
        entry: 'main.tf',
      })
    );
    const templatePath = new URL('./fixtures/main.tf', import.meta.url)
      .pathname;
    const deployed = await invoke([
      'deploy',
      '--url',
      runtime.baseUrl,
      '--world',
      world.worldId,
      '--problem',
      'cli-problem',
      '--runtime',
      runtimePath,
      '--template',
      templatePath,
    ]);
    expect(deployed.code).toBe(0);
    const deployment = parseJson(deployed.stdout);
    assertSimulatorDeploymentResponse(deployment);
    expect(deployment.status).toBe('running');

    const fetched = await invoke([
      'deployment-get',
      '--url',
      runtime.baseUrl,
      '--world',
      world.worldId,
      '--deployment',
      'cli-deployment',
    ]);
    expect(parseJson(fetched.stdout)).toEqual(deployment);

    const resources = await invoke([
      'resources',
      '--url',
      runtime.baseUrl,
      '--world',
      world.worldId,
    ]);
    const projection = parseJson(resources.stdout);
    assertSimulatorResourceProjection(projection);
    const service = projection.resources.find(
      (resource) => resource.resourceType === CLOUD_RUN_SERVICE
    );
    if (!service) throw new Error('Cloud Run service was not deployed');

    const inputPath = join(runtime.directory, 'operation.json');
    await writeFile(inputPath, JSON.stringify({ id: service.resourceId }));
    const operated = await invoke([
      'operation',
      '--url',
      runtime.baseUrl,
      '--world',
      world.worldId,
      '--provider',
      'gcp',
      '--operation',
      'GetService',
      '--deployment',
      'cli-deployment',
      '--target',
      'default',
      '--engine',
      'infra-manager',
      '--service',
      'run',
      '--resource',
      CLOUD_RUN_SERVICE,
      '--input',
      inputPath,
      '--idempotency',
      'cli-get-service',
    ]);
    expect(parseJson(operated.stdout)).toMatchObject({ status: 'Ready' });

    const allEvents = await invoke([
      'events',
      '--url',
      runtime.baseUrl,
      '--world',
      world.worldId,
    ]);
    const allEventPage = parseJson(allEvents.stdout);
    assertSimulatorEventPage(allEventPage);
    expect(allEventPage.events.length).toBeGreaterThan(1);
    const laterEvents = await invoke([
      'events',
      '--url',
      runtime.baseUrl,
      '--world',
      world.worldId,
      '--after',
      '1',
    ]);
    const laterEventPage = parseJson(laterEvents.stdout);
    assertSimulatorEventPage(laterEventPage);
    expect(laterEventPage.events[0]?.sequence).toBeGreaterThan(1);

    const snapshotPath = join(runtime.directory, 'snapshot.json');
    const exported = await invoke([
      'snapshot-export',
      '--url',
      runtime.baseUrl,
      '--world',
      world.worldId,
      '--output',
      snapshotPath,
    ]);
    expect(parseJson(exported.stdout)).toMatchObject({ output: snapshotPath });
    const snapshot: unknown = JSON.parse(await readFile(snapshotPath, 'utf8'));
    assertSimulatorSnapshot(snapshot);
    const imported = await invoke([
      'snapshot-import',
      '--url',
      runtime.baseUrl,
      '--input',
      snapshotPath,
    ]);
    const restoredWorld = parseJson(imported.stdout);
    assertSimulatorWorldResponse(restoredWorld);
    expect(restoredWorld.worldId).not.toBe(world.worldId);

    const deleted = await invoke([
      'world-delete',
      '--url',
      runtime.baseUrl,
      '--world',
      world.worldId,
    ]);
    expect(parseJson(deleted.stdout)).toEqual({ deleted: true });
  });

  it('world-delete は通常 request deadline を越える cleanup の完了を実 HTTP で待つ', async () => {
    const workloadEffects = new QuietWindowWorkloadEffects(75);
    await replaceRuntime(workloadEffects);
    const worldId = await createWorkloadWorld('cli-delete-timeout');

    const deleted = await invoke(
      ['world-delete', '--url', runtime.baseUrl, '--world', worldId],
      { requestMilliseconds: 25 }
    );

    expect(deleted).toMatchObject({
      code: 0,
      stderr: '',
    });
    expect(parseJson(deleted.stdout)).toEqual({ deleted: true });
    expect(runtime.core.world(worldId).status).toBe('deleted');
  });

  it('deleteWorld は caller の明示 AbortSignal で server completion 待機を中断する', async () => {
    const workloadEffects = new QuietWindowWorkloadEffects(75);
    await replaceRuntime(workloadEffects);
    const worldId = await createWorkloadWorld('client-delete-cancellation');
    const client = new SimulatorClient(runtime.baseUrl, undefined, {
      requestMilliseconds: 25,
    });

    await expect(
      client.deleteWorld(worldId, AbortSignal.timeout(25))
    ).rejects.toMatchObject({ name: 'TimeoutError' });
    await Bun.sleep(200);
    expect(runtime.core.world(worldId).status).toBe('deleted');
  });

  it('入力、cursor、contract、API error を終了コードと診断へ変換する', async () => {
    expect((await invoke([])).stdout).toContain('Usage:');
    expect((await invoke(['--help'])).code).toBe(0);
    expect((await invoke(['capabilities', '--url'])).code).toBe(2);
    expect((await invoke(['capabilities'])).code).toBe(2);
    expect(
      (await invoke(['capabilities', '--url', 'http://example.com'])).code
    ).toBe(2);
    expect(
      (
        await invoke([
          'events',
          '--url',
          runtime.baseUrl,
          '--world',
          'unknown',
          '--after',
          '-1',
        ])
      ).stderr
    ).toContain('non-negative integer');

    const invalidPath = join(runtime.directory, 'invalid.json');
    await writeFile(invalidPath, '[]');
    const badRuntime = await invoke([
      'deploy',
      '--url',
      runtime.baseUrl,
      '--world',
      'unknown',
      '--problem',
      'invalid',
      '--runtime',
      invalidPath,
      '--template',
      new URL('./fixtures/main.tf', import.meta.url).pathname,
    ]);
    expect(badRuntime.code).toBe(2);
    const badInput = await invoke([
      'operation',
      '--url',
      runtime.baseUrl,
      '--world',
      'world',
      '--provider',
      'gcp',
      '--operation',
      'GetService',
      '--deployment',
      'deployment',
      '--target',
      'default',
      '--engine',
      'infra-manager',
      '--service',
      'run',
      '--resource',
      CLOUD_RUN_SERVICE,
      '--input',
      invalidPath,
      '--idempotency',
      'invalid',
    ]);
    expect(badInput.stderr).toContain('must contain an object');
    const badSnapshot = await invoke([
      'snapshot-import',
      '--url',
      runtime.baseUrl,
      '--input',
      invalidPath,
    ]);
    expect(badSnapshot.code).toBe(2);

    const missing = await invoke([
      'deployment-get',
      '--url',
      runtime.baseUrl,
      '--world',
      'missing-world',
      '--deployment',
      'missing-deployment',
    ]);
    expect(missing.code).toBe(1);
    expect(missing.stderr).toContain('world does not exist');
    expect(
      (await invoke(['unknown', '--url', runtime.baseUrl])).stderr
    ).toContain('Unknown command');

    const throwingOutput = {
      write(): void {
        throw 'output sink failed';
      },
    };
    const stderr = new BufferOutput();
    expect(
      await runCli(
        ['capabilities', '--url', runtime.baseUrl],
        throwingOutput,
        stderr
      )
    ).toBe(1);
    expect(stderr.value).toContain('output sink failed');
  });

  it('client の URL と response 境界を schema で検証する', async () => {
    expect(DEFAULT_SIMULATOR_CLIENT_TIMEOUT_POLICY).toEqual({
      requestMilliseconds: 10_000,
    });
    expect(() => new SimulatorClient('http://example.com')).toThrow('HTTPS');
    expect(
      () => new SimulatorClient('https://example.com', 'real-oauth-token')
    ).toThrow('simulator launch token');
    expect(() => new SimulatorClient('https://example.com')).not.toThrow();
    expect(() => new SimulatorClient('http://localhost:8787')).not.toThrow();
    expect(() => new SimulatorClient('http://[::1]:8787')).not.toThrow();
    expect(
      () =>
        new SimulatorClient('https://example.com', undefined, {
          requestMilliseconds: 0,
        })
    ).toThrow('client request timeout');
    expect(
      () =>
        new SimulatorClient('https://example.com', undefined, {
          requestMilliseconds: 600_001,
        })
    ).toThrow('bounded');
    expect(
      () =>
        new SimulatorClient('https://example.com', undefined, {
          requestMilliseconds: 600_000,
        })
    ).not.toThrow();
    const mutableTimeoutPolicy = { requestMilliseconds: 250 };
    const stablePolicyClient = new SimulatorClient(
      runtime.baseUrl,
      undefined,
      mutableTimeoutPolicy
    );
    mutableTimeoutPolicy.requestMilliseconds = 0;
    expect(
      (await stablePolicyClient.capabilities()).providers['gcp']
    ).toBeDefined();
    expect(await decodeSimulatorResponse(new Response(null))).toBeUndefined();
    await expect(
      decodeSimulatorResponse(new Response('{invalid'))
    ).rejects.toBeInstanceOf(TypeError);
    expect(parseProviderOperationResponse({ ready: true })).toEqual({
      ready: true,
    });
    expect(() => parseProviderOperationResponse([])).toThrow(
      'must be an object'
    );

    const errorResponse = await fetch(
      `${runtime.baseUrl}/v1/worlds/missing/deployments/missing`
    );
    await expect(
      assertSimulatorDeleteResponse(errorResponse)
    ).rejects.toBeInstanceOf(SimulatorClientError);
  });
});
