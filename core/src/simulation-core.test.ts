import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { contentHash, deterministicId } from './canonical';
import type {
  CreateWorldInput,
  DeploymentInput,
  ExecuteCommandInput,
  MaterializedWorkload,
  ProviderCapability,
  ProviderClockInput,
  ProviderClockResult,
  ProviderCompileInput,
  ProviderModule,
  ProviderTargetPlan,
  ProviderWorldView,
  SingleRuntimeTarget,
  WorkloadDeclaration,
  WorkloadEffectPort,
  WorldNamespace,
  WorldRecord,
  WorldSnapshot,
} from './domain';
import { CoreError } from './errors';
import { ProviderRegistry } from './provider-registry';
import { SimulationCore } from './simulation-core';
import { SimulationStore } from './store';

const DEFAULT_TARGET_ID = 'default';
const ENDPOINT_OUTPUT = 'endpoint';
const LAST_MUTATION_OUTPUT = 'lastMutation';
const WORKLOAD_IMAGE = `ghcr.io/tenkacloud/workload@sha256:${'a'.repeat(64)}`;

type MaterializationFault = 'throw' | 'missing' | 'identity' | 'endpoint';

class LocalWorkloadEffects implements WorkloadEffectPort {
  readonly received: WorkloadDeclaration[][] = [];
  readonly cleanupWorlds: string[] = [];
  readonly faults: MaterializationFault[] = [];
  readonly #servers = new Map<string, Bun.Server<undefined>[]>();
  cleanupFails = false;

  async materialize(
    worldId: string,
    declarations: readonly WorkloadDeclaration[]
  ): Promise<readonly MaterializedWorkload[]> {
    this.received.push(declarations.map((declaration) => ({ ...declaration })));
    const fault = this.faults.shift();
    if (fault === 'throw') throw new Error('local workload failed');
    const servers = this.#servers.get(worldId) ?? [];
    const results = declarations.map((declaration): MaterializedWorkload => {
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
    if (fault === 'missing') return results.slice(1);
    if (fault === 'identity' && results[0]) {
      return [
        { ...results[0], targetId: 'unexpected-target' },
        ...results.slice(1),
      ];
    }
    if (fault === 'endpoint' && results[0]) {
      return [
        { ...results[0], endpoint: 'https://example.test:443' },
        ...results.slice(1),
      ];
    }
    return results;
  }

  async cleanup(worldId: string): Promise<void> {
    this.cleanupWorlds.push(worldId);
    if (this.cleanupFails) throw new Error('local cleanup failed');
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

class DeterministicProvider implements ProviderModule {
  readonly engines: readonly string[];
  readonly capabilities: readonly ProviderCapability[];

  constructor(
    readonly provider: string,
    readonly engine: string
  ) {
    this.engines = [engine];
    this.capabilities = [
      {
        capabilityId: `${provider}-${engine}-deploy`,
        provider,
        engine,
        service: engine,
        resourceType: '*',
        operation: 'deploy',
        fidelity: ['L0', 'L1'],
      },
      ...['put', 'delete'].map(
        (operation): ProviderCapability => ({
          capabilityId: `${provider}-objects-Object-${operation}`,
          provider,
          engine,
          service: 'objects',
          resourceType: 'Object',
          operation,
          fidelity: ['L0'],
        })
      ),
    ];
  }

  compile(input: ProviderCompileInput): ProviderTargetPlan {
    const resourceId = deterministicId('resource', {
      provider: this.provider,
      targetId: input.targetId,
      problemId: input.problemId,
    });
    return {
      targetId: input.targetId,
      provider: this.provider,
      engine: input.target.engine,
      requirements: [
        {
          provider: this.provider,
          engine: input.target.engine,
          service: input.target.engine,
          resourceType: '*',
          operation: 'deploy',
          fidelity: ['L0'],
          source: { path: input.target.entry },
        },
      ],
      resources: [
        {
          provider: this.provider,
          resourceType: 'Object',
          resourceId,
          properties: {
            stage: 'planned',
            problemId: input.problemId,
            templateBody: input.templateBody,
            ...(input.metadata === undefined
              ? {}
              : { metadata: input.metadata }),
            ...(input.simulationOverlay === undefined
              ? {}
              : { simulationOverlay: input.simulationOverlay }),
          },
        },
      ],
    };
  }

  deploy(
    plan: ProviderTargetPlan,
    world: Parameters<ProviderModule['deploy']>[1]
  ) {
    return {
      events: [
        {
          type: 'ProviderDeployed',
          payload: {
            provider: this.provider,
            targetId: plan.targetId,
            observedResources: world.resources.length,
          },
        },
      ],
      resources: plan.resources.map((resource) => ({
        ...resource,
        properties: {
          ...resource.properties,
          stage: 'ready',
          worldSeed: world.world.seed,
        },
      })),
      outputs: {
        endpoint: `https://${this.provider}.${plan.targetId}.simulator.test`,
        seed: world.world.seed,
      },
    };
  }

  reduce(
    command: Parameters<ProviderModule['reduce']>[0],
    world: Parameters<ProviderModule['reduce']>[1]
  ) {
    const { resourceId: requestedResourceId, value } = command.input;
    const resourceId =
      typeof requestedResourceId === 'string' && requestedResourceId.length > 0
        ? requestedResourceId
        : deterministicId('resource', {
            provider: this.provider,
            deploymentId: command.deploymentId,
            value,
          });
    if (command.operation === 'delete') {
      return {
        events: [
          {
            type: 'ProviderResourceDeleted',
            payload: { provider: this.provider, resourceId },
          },
        ],
        resources: [],
        deletedResourceIds: [resourceId],
        outputs: { lastMutation: 'delete' },
        response: { deletedResourceId: resourceId },
      };
    }
    return {
      events: [
        {
          type: 'ProviderResourceMutated',
          payload: {
            provider: this.provider,
            resourceId,
            value: value ?? null,
          },
        },
      ],
      resources: [
        {
          provider: this.provider,
          resourceType: command.resourceType,
          resourceId,
          properties: {
            value: value ?? null,
            observedResources: world.resources.length,
          },
        },
      ],
      deletedResourceIds: [],
      outputs: { lastMutation: command.operation },
      response: { resourceId, value: value ?? null },
    };
  }
}

class MisroutedProvider extends DeterministicProvider {
  override compile(input: ProviderCompileInput): ProviderTargetPlan {
    return { ...super.compile(input), provider: 'provider-not-registered' };
  }
}

class MultiResourceProvider extends DeterministicProvider {
  override compile(input: ProviderCompileInput): ProviderTargetPlan {
    const plan = super.compile(input);
    return {
      ...plan,
      resources: [
        ...plan.resources,
        {
          provider: this.provider,
          resourceType: 'Object',
          resourceId: 'resource-sorts-after-deterministic-id',
          properties: { stage: 'planned' },
        },
      ],
    };
  }
}

class ClockProvider extends DeterministicProvider {
  constructor(
    provider: string,
    engine: string,
    readonly evaluateClock: (
      input: ProviderClockInput,
      world: ProviderWorldView
    ) => ProviderClockResult
  ) {
    super(provider, engine);
  }

  advanceClock(
    input: ProviderClockInput,
    world: ProviderWorldView
  ): ProviderClockResult {
    return this.evaluateClock(input, world);
  }
}

class AsyncProvider extends DeterministicProvider {
  async reduceAsync(
    command: Parameters<ProviderModule['reduce']>[0],
    world: Parameters<ProviderModule['reduce']>[1]
  ) {
    await Promise.resolve();
    return super.reduce(command, world);
  }
}

const WORLD_INPUT: CreateWorldInput = {
  tenantId: 'tenant-a',
  eventId: 'event-a',
  teamId: 'team-a',
  deploymentId: 'world-deployment-a',
};

function singleDeployment(
  deploymentId = 'deployment-a',
  provider = 'alpha',
  engine = 'engine-a'
): DeploymentInput {
  return {
    deploymentId,
    problemId: 'problem-a',
    runtime: {
      provider,
      engine,
      entry: 'problem/template.yaml',
    },
    templateBody: 'resources: []',
  };
}

function commandInput(
  deploymentId = 'deployment-a',
  operation = 'put',
  input: Readonly<Record<string, unknown>> = { value: 'updated' }
): ExecuteCommandInput {
  return {
    deploymentId,
    targetId: 'default',
    provider: 'alpha',
    engine: 'engine-a',
    service: 'objects',
    operation,
    resourceType: 'Object',
    input,
  };
}

function captureCoreError(operation: () => unknown): CoreError {
  try {
    operation();
  } catch (error) {
    if (error instanceof CoreError) return error;
    throw error;
  }
  throw new Error('CoreError が発生しませんでした');
}

async function captureCoreErrorAsync(
  operation: () => Promise<unknown>
): Promise<CoreError> {
  try {
    await operation();
  } catch (error) {
    if (error instanceof CoreError) return error;
    throw error;
  }
  throw new Error('CoreError が発生しませんでした');
}

describe('SimulationCore の振る舞い', () => {
  let directory = '';
  let store: SimulationStore;
  let registry: ProviderRegistry;
  let core: SimulationCore;
  let workloadEffects: LocalWorkloadEffects | undefined;

  beforeEach(() => {
    directory = mkdtempSync(path.join(tmpdir(), 'simulation-core-'));
    store = new SimulationStore(path.join(directory, 'simulation.sqlite'));
    registry = new ProviderRegistry([
      new DeterministicProvider('alpha', 'engine-a'),
      new DeterministicProvider('beta', 'engine-b'),
    ]);
    core = new SimulationCore(store, registry);
  });

  afterEach(() => {
    workloadEffects?.close();
    workloadEffects = undefined;
    store.close();
    rmSync(directory, { recursive: true, force: true });
  });

  function createWorld(
    input: CreateWorldInput = WORLD_INPUT,
    idempotencyKey = 'world-key'
  ): WorldRecord {
    return core.createWorld(input, idempotencyKey);
  }

  function createReadyDeployment(
    worldId: string,
    input: DeploymentInput = singleDeployment(),
    idempotencyKey = 'deployment-key'
  ) {
    return core.createDeployment(worldId, input, idempotencyKey);
  }

  function workloadDeployment(
    deploymentId = 'workload-deployment'
  ): DeploymentInput {
    return {
      ...singleDeployment(deploymentId),
      simulationOverlay: {
        schemaVersion: '1',
        workloads: [
          {
            id: 'api',
            targetId: 'default',
            resourceRef: 'ApiFunction',
            image: WORKLOAD_IMAGE,
            command: ['bun', 'run', 'start'],
            containerPort: 3000,
            healthPath: '/healthz',
          },
        ],
      },
    };
  }

  function enableWorkloadEffects(): LocalWorkloadEffects {
    workloadEffects = new LocalWorkloadEffects();
    core = new SimulationCore(store, registry, { workloadEffects });
    return workloadEffects;
  }

  describe('world namespace と idempotency', () => {
    it('同じ namespace と key の再送は同じ world を返し event を増やさない', () => {
      const first = createWorld();
      const repeated = createWorld();
      const isolated = createWorld(
        { ...WORLD_INPUT, teamId: 'team-b' },
        'world-key'
      );

      expect(repeated).toEqual(first);
      expect(core.events(first.worldId).map((event) => event.type)).toEqual([
        'WorldCreated',
      ]);
      expect(isolated.worldId).not.toBe(first.worldId);
      expect(core.events(isolated.worldId)).toHaveLength(1);
      expect(first.seed).toMatch(/^[a-f0-9]{64}$/);
      expect(first.virtualTime).toBe('1970-01-01T00:00:00.000Z');
    });

    it('同じ idempotency key を別 payload で再利用すると Conflict にする', () => {
      createWorld();

      const error = captureCoreError(() =>
        createWorld({ ...WORLD_INPUT, deploymentId: 'changed' })
      );

      expect(error.code).toBe('IdempotencyConflict');
    });

    it('別 namespace からの参照は存在しない world として扱う', () => {
      const world = createWorld();
      const namespace: WorldNamespace = {
        tenantId: world.tenantId,
        eventId: world.eventId,
        teamId: 'other-team',
      };

      expect(
        captureCoreError(() => core.world(world.worldId, namespace)).code
      ).toBe('NotFound');
      expect(captureCoreError(() => core.world('missing-world')).code).toBe(
        'NotFound'
      );
    });

    it('指定した seed と virtual time をそのまま world の初期状態にする', () => {
      const world = createWorld({
        ...WORLD_INPUT,
        seed: 'fixed-seed',
        virtualTime: '2026-07-12T09:30:00+09:00',
      });

      expect(world.seed).toBe('fixed-seed');
      expect(world.virtualTime).toBe('2026-07-12T00:30:00.000Z');
    });

    it('必須 namespace、deployment、key、virtual time の不正値を拒否する', () => {
      for (const field of [
        'tenantId',
        'eventId',
        'teamId',
        'deploymentId',
      ] as const) {
        const error = captureCoreError(() =>
          createWorld({ ...WORLD_INPUT, [field]: '   ' }, `key-${field}`)
        );
        expect(error.code).toBe('ValidationFailed');
      }
      expect(
        captureCoreError(() =>
          createWorld({ ...WORLD_INPUT, virtualTime: 'not-a-date' })
        ).code
      ).toBe('ValidationFailed');
      expect(captureCoreError(() => createWorld(WORLD_INPUT, '  ')).code).toBe(
        'ValidationFailed'
      );
    });

    it('world event quota がゼロなら作成を atomic に拒否する', () => {
      const limited = new SimulationCore(store, registry, {
        maxEventsPerWorld: 0,
      });

      const error = captureCoreError(() =>
        limited.createWorld(WORLD_INPUT, 'limited-world')
      );

      expect(error.code).toBe('QuotaExceeded');
      expect(
        store.world(
          deterministicId('world', {
            input: WORLD_INPUT,
            idempotencyKey: 'limited-world',
          })
        )
      ).toBeUndefined();
    });
  });

  describe('deployment と capability preflight', () => {
    it('single target を deploy し、plan と provider result を一つの resource に統合する', () => {
      const world = createWorld();
      const input: DeploymentInput = {
        ...singleDeployment(),
        metadata: { source: 'catalog' },
        simulationOverlay: {
          schemaVersion: '1',
          requirements: [
            {
              targetId: 'default',
              service: 'objects',
              resourceType: 'Object',
              operation: 'put',
              fidelity: 'L0',
              plane: 'participant',
            },
          ],
        },
      };

      const deployment = createReadyDeployment(world.worldId, input);
      const resource = core.resources(world.worldId)[0];

      expect(deployment.status).toBe('ready');
      expect(deployment.outputs[DEFAULT_TARGET_ID]?.[ENDPOINT_OUTPUT]).toBe(
        'https://alpha.default.simulator.test'
      );
      expect(resource?.properties).toEqual({
        metadata: { source: 'catalog' },
        problemId: 'problem-a',
        simulationOverlay: {
          schemaVersion: '1',
          requirements: [
            {
              targetId: 'default',
              service: 'objects',
              resourceType: 'Object',
              operation: 'put',
              fidelity: 'L0',
              plane: 'participant',
            },
          ],
        },
        stage: 'ready',
        templateBody: 'resources: []',
        worldSeed: world.seed,
      });
      expect(core.deployment(world.worldId, deployment.deploymentId)).toEqual(
        deployment
      );
      expect(captureCoreError(() => core.resources('missing-world')).code).toBe(
        'NotFound'
      );
      expect(core.events(world.worldId).map((event) => event.type)).toEqual([
        'WorldCreated',
        'DeploymentRequested',
        'ResourceDeclared',
        'ProviderDeployed',
        'DeploymentReady',
      ]);
    });

    it('同じ deployment key の再送を idempotent にし、別 payload を拒否する', () => {
      const world = createWorld();
      const input = singleDeployment();
      const first = createReadyDeployment(world.worldId, input);
      const eventCount = core.events(world.worldId).length;

      expect(createReadyDeployment(world.worldId, input)).toEqual(first);
      expect(core.events(world.worldId)).toHaveLength(eventCount);
      expect(
        captureCoreError(() =>
          createReadyDeployment(world.worldId, {
            ...input,
            problemId: 'changed-problem',
          })
        ).code
      ).toBe('IdempotencyConflict');
    });

    it('Composite target を同じ world、sequence、clock へ deploy する', () => {
      const world = createWorld({
        ...WORLD_INPUT,
        seed: 'composite-seed',
        virtualTime: '2026-01-01T00:00:00.000Z',
      });
      const input: DeploymentInput = {
        deploymentId: 'composite-deployment',
        problemId: 'composite-problem',
        runtime: {
          kind: 'composite',
          targets: [
            {
              id: 'primary',
              provider: 'alpha',
              engine: 'engine-a',
              entry: 'alpha.yaml',
            },
            {
              id: 'secondary',
              provider: 'beta',
              engine: 'engine-b',
              entry: 'beta.yaml',
            },
          ],
        },
        templateBody: 'composite: true',
      };

      const deployment = createReadyDeployment(
        world.worldId,
        input,
        'composite-key'
      );
      const events = core.events(world.worldId);

      expect(Object.keys(deployment.outputs).sort()).toEqual([
        'primary',
        'secondary',
      ]);
      expect(
        store.resources(world.worldId).map((item) => item.provider)
      ).toEqual(['alpha', 'beta']);
      expect(events.map((event) => event.sequence)).toEqual(
        events.map((_, index) => index + 1)
      );
      expect(new Set(events.map((event) => event.virtualTime))).toEqual(
        new Set(['2026-01-01T00:00:00.000Z'])
      );
    });

    it('Composite artifact bundle は target ごとの entry 本文だけを compiler に渡す', () => {
      const world = createWorld();
      const input: DeploymentInput = {
        deploymentId: 'artifact-deployment',
        problemId: 'artifact-problem',
        runtime: {
          kind: 'composite',
          targets: [
            {
              id: 'primary',
              provider: 'alpha',
              engine: 'engine-a',
              entry: 'alpha.yaml',
            },
            {
              id: 'secondary',
              provider: 'beta',
              engine: 'engine-b',
              entry: 'beta/terraform',
            },
          ],
        },
        templateBody: JSON.stringify({
          format: 'tenkacloud.simulator.artifacts.v1',
          targets: [
            {
              id: 'primary',
              provider: 'alpha',
              engine: 'engine-a',
              entry: 'alpha.yaml',
              artifacts: [{ path: 'alpha.yaml', content: 'alpha: source\n' }],
            },
            {
              id: 'secondary',
              provider: 'beta',
              engine: 'engine-b',
              entry: 'beta/terraform',
              artifacts: [
                {
                  path: 'beta/terraform/main.tf',
                  content: 'resource "beta" "main" {}\n',
                },
                {
                  path: 'beta/terraform/variables.tf',
                  content: 'variable "seed" {}\n',
                },
              ],
            },
          ],
        }),
      };

      createReadyDeployment(world.worldId, input, 'artifact-key');
      const bodies = Object.fromEntries(
        store
          .resources(world.worldId)
          .map((resource) => [
            resource.provider,
            resource.properties['templateBody'],
          ])
      );
      expect(bodies).toEqual({
        alpha: 'alpha: source\n',
        beta: 'resource "beta" "main" {}\n',
      });
    });

    it('全 target の preflight が揃う前は resource event を一件も作らない', () => {
      const world = createWorld();
      const input: DeploymentInput = {
        deploymentId: 'unsupported-deployment',
        problemId: 'unsupported-problem',
        runtime: {
          kind: 'composite',
          targets: [
            {
              id: 'supported',
              provider: 'alpha',
              engine: 'engine-a',
              entry: 'alpha.yaml',
            },
            {
              id: 'unsupported',
              provider: 'missing-provider',
              engine: 'missing-engine',
              entry: 'missing.yaml',
            },
          ],
        },
        templateBody: 'composite: true',
      };

      const first = captureCoreError(() =>
        core.createDeployment(world.worldId, input, 'unsupported-key')
      );

      expect(first.code).toBe('UnsupportedCapability');
      expect(first.diagnostics.map((item) => item.code)).toEqual([
        'MissingProvider',
      ]);
      expect(store.resources(world.worldId)).toEqual([]);
      expect(core.events(world.worldId).map((event) => event.type)).toEqual([
        'WorldCreated',
        'DeploymentRejected',
      ]);
      expect(store.deployment(world.worldId, input.deploymentId)?.status).toBe(
        'rejected'
      );

      const repeated = captureCoreError(() =>
        core.createDeployment(world.worldId, input, 'unsupported-key')
      );
      expect(repeated.code).toBe('UnsupportedCapability');
      expect(core.events(world.worldId)).toHaveLength(2);
    });

    it('overlay requirement のproviderとengineをtargetから継承してresource作成前に診断する', () => {
      const world = createWorld();
      const input: DeploymentInput = {
        ...singleDeployment(),
        simulationOverlay: {
          schemaVersion: '1',
          requirements: [
            {
              targetId: 'default',
              service: 'http',
              resourceType: 'HTTP::Endpoint',
              operation: 'Probe',
              fidelity: 'L4',
              plane: 'scoring',
            },
          ],
        },
      };

      const error = captureCoreError(() =>
        core.createDeployment(world.worldId, input, 'overlay-preflight')
      );

      expect(error.code).toBe('UnsupportedCapability');
      expect(error.diagnostics).toEqual([
        {
          provider: 'alpha',
          engine: 'engine-a',
          service: 'http',
          resourceType: 'HTTP::Endpoint',
          operation: 'Probe',
          fidelity: ['L4'],
          source: { path: 'simulation-overlay' },
          code: 'MissingCapability',
          availableFidelity: [],
        },
      ]);
      expect(core.resources(world.worldId)).toEqual([]);
    });

    it('workload effect が未設定なら Runtime::Workload を preflight で拒否する', () => {
      const world = createWorld();

      const error = captureCoreError(() =>
        core.createDeployment(
          world.worldId,
          workloadDeployment(),
          'workload-unavailable'
        )
      );

      expect(core.workloadEffectsAvailable).toBe(false);
      expect(error).toMatchObject({
        code: 'UnsupportedCapability',
        diagnostics: [
          {
            provider: 'alpha',
            engine: 'engine-a',
            service: 'runtime',
            resourceType: 'Runtime::Workload',
            operation: 'Materialize',
            fidelity: ['L4'],
            code: 'MissingCapability',
            availableFidelity: [],
          },
        ],
      });
      expect(core.resources(world.worldId)).toEqual([]);
    });

    it('workload を pending 保存して正規化済み declaration だけを materialize する', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const input = workloadDeployment();

      const deploying = core.createDeployment(
        world.worldId,
        input,
        'workload-ready'
      );
      const pending = core
        .resources(world.worldId)
        .find((resource) => resource.resourceType === 'Runtime::Workload');

      expect(core.workloadEffectsAvailable).toBe(true);
      expect(deploying.status).toBe('deploying');
      expect(pending).toMatchObject({ provider: 'runtime', status: 'pending' });
      expect(effects.received).toEqual([]);

      const ready = await core.materializeWorkloads(
        world.worldId,
        deploying.deploymentId
      );
      const materialized = core
        .resources(world.worldId)
        .find((resource) => resource.resourceType === 'Runtime::Workload');

      expect(ready.status).toBe('ready');
      expect(effects.received).toEqual([
        [
          {
            id: 'api',
            targetId: 'default',
            resourceRef: 'ApiFunction',
            image: WORKLOAD_IMAGE,
            command: ['bun', 'run', 'start'],
            containerPort: 3000,
            healthPath: '/healthz',
          },
        ],
      ]);
      expect(materialized?.status).toBe('ready');
      expect(materialized?.properties['materialization']).toMatchObject({
        endpoint: expect.stringMatching(/^http:\/\/127\.0\.0\.1:/),
      });
      expect(ready.outputs['default']?.['Workload.api.Endpoint']).toMatch(
        /^http:\/\/127\.0\.0\.1:/
      );
      expect(core.events(world.worldId).map((event) => event.type)).toEqual([
        'WorldCreated',
        'DeploymentRequested',
        'ResourceDeclared',
        'ResourceDeclared',
        'ProviderDeployed',
        'DeploymentDeploying',
        'WorkloadMaterialized',
        'DeploymentReady',
      ]);

      expect(
        await core.materializeWorkloads(world.worldId, deploying.deploymentId)
      ).toEqual(ready);
      expect(
        core.createDeployment(world.worldId, input, 'workload-ready')
      ).toEqual(ready);
      expect(effects.received).toHaveLength(1);
    });

    it('結果集合・identity・loopback endpoint の不一致を failed event にして再試行できる', async () => {
      const effects = enableWorkloadEffects();
      const faults: readonly MaterializationFault[] = [
        'missing',
        'identity',
        'endpoint',
        'throw',
      ];

      for (const [index, fault] of faults.entries()) {
        const world = createWorld(
          { ...WORLD_INPUT, teamId: `team-workload-${index}` },
          `world-workload-${index}`
        );
        const deployment = core.createDeployment(
          world.worldId,
          workloadDeployment(`workload-deployment-${index}`),
          `workload-failure-${index}`
        );
        effects.faults.push(fault);

        const error = await captureCoreErrorAsync(() =>
          core.materializeWorkloads(world.worldId, deployment.deploymentId)
        );

        expect(error.code).toBe('WorkloadEffectFailed');
        expect(
          core.deployment(world.worldId, deployment.deploymentId).status
        ).toBe('failed');
        expect(
          core
            .resources(world.worldId)
            .find((resource) => resource.resourceType === 'Runtime::Workload')
            ?.status
        ).toBe('failed');
        expect(core.events(world.worldId).at(-1)).toMatchObject({
          type: 'WorkloadMaterializationFailed',
          payload: {
            deploymentId: deployment.deploymentId,
            code: 'WorkloadEffectFailed',
            retryable: true,
            workloadIds: ['api'],
          },
        });

        expect(
          (
            await core.materializeWorkloads(
              world.worldId,
              deployment.deploymentId
            )
          ).status
        ).toBe('ready');
      }
    });

    it('workload cleanup が成功するまで world を tombstone にしない', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'workload-cleanup'
      );
      await core.materializeWorkloads(world.worldId, deployment.deploymentId);
      effects.cleanupFails = true;

      expect(
        (await captureCoreErrorAsync(() => core.deleteWorld(world.worldId)))
          .code
      ).toBe('WorkloadEffectFailed');
      expect(core.world(world.worldId).status).toBe('active');
      expect(
        core.resources(world.worldId).some((item) => item.status === 'ready')
      ).toBe(true);

      effects.cleanupFails = false;
      await core.deleteWorld(world.worldId);
      await core.deleteWorld(world.worldId);
      expect(effects.cleanupWorlds).toEqual([world.worldId, world.worldId]);
      expect(core.world(world.worldId).status).toBe('deleted');
      expect(
        core.resources(world.worldId).every((item) => item.status === 'deleted')
      ).toBe(true);
    });

    it('Composite の target 数、ID 必須、一意性を検証する', () => {
      const world = createWorld();
      const target = {
        id: 'one',
        provider: 'alpha',
        engine: 'engine-a',
        entry: 'alpha.yaml',
      };
      const withoutId: SingleRuntimeTarget = {
        provider: 'alpha',
        engine: 'engine-a',
        entry: 'alpha.yaml',
      };
      const invalidTargets: ReadonlyArray<readonly SingleRuntimeTarget[]> = [
        [target],
        Array.from({ length: 9 }, (_, index) => ({
          ...target,
          id: `target-${index}`,
        })),
        [target, withoutId],
        [target, { ...target }],
      ];

      invalidTargets.forEach((targets, index) => {
        const error = captureCoreError(() =>
          core.createDeployment(
            world.worldId,
            {
              deploymentId: `invalid-composite-${index}`,
              problemId: 'problem',
              runtime: { kind: 'composite', targets },
              templateBody: 'resources: []',
            },
            `invalid-composite-key-${index}`
          )
        );
        expect(error.code).toBe('ValidationFailed');
      });
    });

    it('deployment と target の必須文字列を検証する', () => {
      const world = createWorld();
      const base = singleDeployment();
      const invalidInputs: DeploymentInput[] = [
        { ...base, deploymentId: ' ' },
        { ...base, problemId: ' ' },
        { ...base, runtime: { ...base.runtime, provider: ' ' } },
        { ...base, runtime: { ...base.runtime, engine: ' ' } },
        { ...base, runtime: { ...base.runtime, entry: ' ' } },
      ];

      invalidInputs.forEach((input, index) => {
        expect(
          captureCoreError(() =>
            core.createDeployment(
              world.worldId,
              input,
              `invalid-deployment-${index}`
            )
          ).code
        ).toBe('ValidationFailed');
      });
      expect(
        captureCoreError(() =>
          core.createDeployment(world.worldId, base, '   ')
        ).code
      ).toBe('ValidationFailed');
    });

    it('provider が compile 後に未登録 provider の plan を返したら NotFound にする', () => {
      const world = createWorld();
      const isolatedRegistry = new ProviderRegistry([
        new MisroutedProvider('misrouted', 'engine-m'),
      ]);
      const isolatedCore = new SimulationCore(store, isolatedRegistry);

      const error = captureCoreError(() =>
        isolatedCore.createDeployment(
          world.worldId,
          singleDeployment('misrouted-deployment', 'misrouted', 'engine-m'),
          'misrouted-key'
        )
      );

      expect(error.code).toBe('NotFound');
      expect(
        store.deployment(world.worldId, 'misrouted-deployment')
      ).toBeUndefined();
    });

    it('一つの target が複数 resource を返すと canonical identity 順に保存する', () => {
      const world = createWorld();
      const isolatedRegistry = new ProviderRegistry([
        new MultiResourceProvider('multi', 'engine-m'),
      ]);
      const isolatedCore = new SimulationCore(store, isolatedRegistry);

      isolatedCore.createDeployment(
        world.worldId,
        singleDeployment('multi-deployment', 'multi', 'engine-m'),
        'multi-key'
      );

      const identities = store
        .resources(world.worldId)
        .map((resource) => `${resource.provider}:${resource.resourceId}`);
      expect(identities).toEqual([...identities].sort());
      expect(identities).toHaveLength(2);
    });

    it('存在しない deployment を NotFound にし、削除済み world の deploy を拒否する', () => {
      const world = createWorld();

      expect(
        captureCoreError(() => core.deployment(world.worldId, 'missing')).code
      ).toBe('NotFound');
      core.deleteWorld(world.worldId);
      expect(
        captureCoreError(() =>
          core.createDeployment(
            world.worldId,
            singleDeployment(),
            'after-delete'
          )
        ).code
      ).toBe('Conflict');
    });

    it('event quota と resource quota を transaction 前に検査する', () => {
      const world = createWorld();
      const eventLimited = new SimulationCore(store, registry, {
        maxEventsPerWorld: 4,
      });
      expect(
        captureCoreError(() =>
          eventLimited.createDeployment(
            world.worldId,
            singleDeployment('event-limited'),
            'event-limited-key'
          )
        ).code
      ).toBe('QuotaExceeded');
      expect(core.events(world.worldId)).toHaveLength(1);

      const resourceLimited = new SimulationCore(store, registry, {
        maxResourcesPerWorld: 0,
      });
      expect(
        captureCoreError(() =>
          resourceLimited.createDeployment(
            world.worldId,
            singleDeployment('resource-limited'),
            'resource-limited-key'
          )
        ).code
      ).toBe('QuotaExceeded');
      expect(store.resources(world.worldId)).toEqual([]);
      expect(store.deployments(world.worldId)).toEqual([]);
    });
  });

  describe('shared command mutation', () => {
    it('async reducer と sync fallback を同じidempotent commit境界で実行する', async () => {
      const world = createWorld();
      const deployment = createReadyDeployment(world.worldId);
      const input = commandInput(deployment.deploymentId);
      const asyncCore = new SimulationCore(
        store,
        new ProviderRegistry([new AsyncProvider('alpha', 'engine-a')])
      );

      const asynchronous = await asyncCore.executeCommandAsync(
        world.worldId,
        input,
        'async-command'
      );
      const repeated = await asyncCore.executeCommandAsync(
        world.worldId,
        input,
        'async-command'
      );
      const fallback = await core.executeCommandAsync(
        world.worldId,
        { ...input, input: { value: 'fallback' } },
        'sync-fallback-command'
      );

      expect(asynchronous).toEqual(repeated);
      expect(asynchronous).toMatchObject({ value: 'updated' });
      expect(fallback).toMatchObject({ value: 'fallback' });
    });

    it('別の core instance から同じ SQLite world の mutation を観測できる', () => {
      const world = createWorld();
      const deployment = createReadyDeployment(world.worldId);
      const observer = new SimulationCore(store, registry);
      const eventsBefore = observer.events(world.worldId).length;
      const input = commandInput(deployment.deploymentId, 'put', {
        resourceId: 'shared-resource',
        value: 'from-cli',
      });

      const response = core.executeCommand(
        world.worldId,
        input,
        'shared-command-key'
      );

      expect(response).toEqual({
        resourceId: 'shared-resource',
        value: 'from-cli',
      });
      expect(
        observer.store
          .resources(world.worldId)
          .find((resource) => resource.resourceId === 'shared-resource')
          ?.properties
      ).toEqual({ observedResources: 1, value: 'from-cli' });
      expect(
        observer.deployment(world.worldId, deployment.deploymentId).outputs[
          DEFAULT_TARGET_ID
        ]
      ).toMatchObject({
        endpoint: 'https://alpha.default.simulator.test',
        lastMutation: 'put',
      });
      expect(observer.events(world.worldId)).toHaveLength(eventsBefore + 1);

      expect(
        core.executeCommand(world.worldId, input, 'shared-command-key')
      ).toEqual(response);
      expect(observer.events(world.worldId)).toHaveLength(eventsBefore + 1);
      expect(
        captureCoreError(() =>
          core.executeCommand(
            world.worldId,
            { ...input, input: { ...input.input, value: 'changed' } },
            'shared-command-key'
          )
        ).code
      ).toBe('IdempotencyConflict');
    });

    it('delete command は resource を論理削除し output を更新する', () => {
      const world = createWorld();
      const deployment = createReadyDeployment(world.worldId);
      const resource = store.resources(world.worldId)[0];
      if (!resource) throw new Error('deploy resource がありません');

      const response = core.executeCommand(
        world.worldId,
        commandInput(deployment.deploymentId, 'delete', {
          resourceId: resource.resourceId,
        }),
        'delete-command-key'
      );

      expect(response).toEqual({ deletedResourceId: resource.resourceId });
      expect(store.resources(world.worldId)[0]?.status).toBe('deleted');
      expect(
        core.deployment(world.worldId, deployment.deploymentId).outputs[
          DEFAULT_TARGET_ID
        ]?.[LAST_MUTATION_OUTPUT]
      ).toBe('delete');
    });

    it('未登録 operation を UnsupportedCapability にし state を変更しない', () => {
      const world = createWorld();
      const deployment = createReadyDeployment(world.worldId);
      const eventsBefore = core.events(world.worldId);
      const resourcesBefore = store.resources(world.worldId);

      const error = captureCoreError(() =>
        core.executeCommand(
          world.worldId,
          commandInput(deployment.deploymentId, 'unknown-operation'),
          'unsupported-command-key'
        )
      );

      expect(error.code).toBe('UnsupportedCapability');
      expect(error.diagnostics[0]?.code).toBe('MissingCapability');
      expect(core.events(world.worldId)).toEqual(eventsBefore);
      expect(store.resources(world.worldId)).toEqual(resourcesBefore);
    });

    it('存在しない deployment への mutation は SQLite transaction を rollback する', () => {
      const world = createWorld();
      const eventsBefore = core.events(world.worldId);
      const resourcesBefore = store.resources(world.worldId);

      const error = captureCoreError(() =>
        core.executeCommand(
          world.worldId,
          commandInput('missing-deployment', 'put', {
            resourceId: 'rolled-back-resource',
            value: 'must-not-persist',
          }),
          'rollback-command-key'
        )
      );

      expect(error.code).toBe('NotFound');
      expect(core.events(world.worldId)).toEqual(eventsBefore);
      expect(store.resources(world.worldId)).toEqual(resourcesBefore);
    });

    it('command の key、world status、event quota、resource quota を検証する', () => {
      const world = createWorld();
      const deployment = createReadyDeployment(world.worldId);
      const input = commandInput(deployment.deploymentId);

      expect(
        captureCoreError(() => core.executeCommand(world.worldId, input, ' '))
          .code
      ).toBe('ValidationFailed');

      const eventLimited = new SimulationCore(store, registry, {
        maxEventsPerWorld: core.events(world.worldId).length,
      });
      expect(
        captureCoreError(() =>
          eventLimited.executeCommand(world.worldId, input, 'event-quota-key')
        ).code
      ).toBe('QuotaExceeded');

      const resourceLimited = new SimulationCore(store, registry, {
        maxResourcesPerWorld: store.resources(world.worldId).length,
      });
      expect(
        captureCoreError(() =>
          resourceLimited.executeCommand(
            world.worldId,
            input,
            'resource-quota-key'
          )
        ).code
      ).toBe('QuotaExceeded');

      core.deleteWorld(world.worldId);
      expect(
        captureCoreError(() =>
          core.executeCommand(world.worldId, input, 'deleted-world-command')
        ).code
      ).toBe('Conflict');
    });
  });

  describe('virtual clock と delete', () => {
    it('正の整数 milliseconds だけ clock を進め、更新時刻で event を記録する', () => {
      const world = createWorld({
        ...WORLD_INPUT,
        virtualTime: '2026-07-12T00:00:00.000Z',
      });

      const advanced = core.advanceClock(world.worldId, 1_500);
      const event = core.events(world.worldId).at(-1);

      expect(advanced.virtualTime).toBe('2026-07-12T00:00:01.500Z');
      expect(event?.type).toBe('ClockAdvanced');
      expect(event?.virtualTime).toBe(advanced.virtualTime);
      expect(event?.payload).toEqual({
        appliedTransitions: [],
        milliseconds: 1_500,
        virtualTime: advanced.virtualTime,
      });
      expect(advanced.appliedTransitions).toEqual([]);
    });

    it('provider 遷移を provider と ID 順に適用し resource と event を同じ時刻へ原子的に保存する', () => {
      const world = createWorld({
        ...WORLD_INPUT,
        virtualTime: '2026-07-12T00:00:00.000Z',
      });
      const deployment = createReadyDeployment(world.worldId);
      const original = core.resources(world.worldId)[0];
      if (!original) throw new Error('resource が作成されませんでした');
      const clockCore = new SimulationCore(
        store,
        new ProviderRegistry([
          new ClockProvider('beta', 'engine-b', (input) => ({
            events: [
              {
                type: 'BetaTransitionApplied',
                payload: { previous: input.previousVirtualTime },
              },
            ],
            resources: [],
            deletedResourceIds: [],
            appliedTransitionIds: ['transition-beta'],
          })),
          new ClockProvider('alpha', 'engine-a', (input, view) => ({
            events: [
              {
                type: 'AlphaTransitionApplied',
                payload: {
                  previous: input.previousVirtualTime,
                  current: input.virtualTime,
                },
              },
            ],
            resources: [
              {
                ...original,
                properties: {
                  ...original.properties,
                  transitionedAt: input.virtualTime,
                  observedResources: view.resources.length,
                },
              },
              {
                ...original,
                resourceId: 'resource-created-by-clock',
                properties: { transitionedAt: input.virtualTime },
              },
            ],
            deletedResourceIds: [],
            appliedTransitionIds: ['transition-z', 'transition-a'],
          })),
        ])
      );

      const advanced = clockCore.advanceClock(world.worldId, 2_000);
      const lastEvents = clockCore.events(world.worldId).slice(-3);

      expect(advanced).toMatchObject({
        virtualTime: '2026-07-12T00:00:02.000Z',
        appliedTransitions: [
          { provider: 'alpha', transitionId: 'transition-a' },
          { provider: 'alpha', transitionId: 'transition-z' },
          { provider: 'beta', transitionId: 'transition-beta' },
        ],
      });
      expect(lastEvents.map((event) => event.type)).toEqual([
        'ClockAdvanced',
        'AlphaTransitionApplied',
        'BetaTransitionApplied',
      ]);
      expect(new Set(lastEvents.map((event) => event.virtualTime))).toEqual(
        new Set(['2026-07-12T00:00:02.000Z'])
      );
      expect(lastEvents[0]?.payload).toMatchObject({
        appliedTransitions: advanced.appliedTransitions,
      });
      expect(
        clockCore
          .resources(world.worldId)
          .find((resource) => resource.resourceId === original.resourceId)
          ?.properties
      ).toMatchObject({
        transitionedAt: '2026-07-12T00:00:02.000Z',
        observedResources: 1,
      });
      expect(
        clockCore
          .resources(world.worldId)
          .find(
            (resource) => resource.resourceId === 'resource-created-by-clock'
          )?.deploymentId
      ).toBe(deployment.deploymentId);
    });

    it('provider 遷移は既存 resource を論理削除できる', () => {
      const world = createWorld();
      createReadyDeployment(world.worldId);
      const original = core.resources(world.worldId)[0];
      if (!original) throw new Error('resource が作成されませんでした');
      const clockCore = new SimulationCore(
        store,
        new ProviderRegistry([
          new ClockProvider('alpha', 'engine-a', () => ({
            events: [],
            resources: [],
            deletedResourceIds: [original.resourceId],
            appliedTransitionIds: ['transition-delete'],
          })),
        ])
      );

      clockCore.advanceClock(world.worldId, 1);

      expect(clockCore.resources(world.worldId)[0]?.status).toBe('deleted');
    });

    it('provider event と新規 resource を含めて clock quota を反映前に検証する', () => {
      const world = createWorld();
      createReadyDeployment(world.worldId);
      const original = core.resources(world.worldId)[0];
      if (!original) throw new Error('resource が作成されませんでした');
      const provider = new ClockProvider('alpha', 'engine-a', () => ({
        events: [{ type: 'TransitionApplied', payload: {} }],
        resources: [
          {
            ...original,
            resourceId: 'new-clock-resource',
          },
        ],
        deletedResourceIds: [],
        appliedTransitionIds: [],
      }));
      const eventLimited = new SimulationCore(
        store,
        new ProviderRegistry([provider]),
        { maxEventsPerWorld: core.events(world.worldId).length + 1 }
      );
      const resourceLimited = new SimulationCore(
        store,
        new ProviderRegistry([provider]),
        { maxResourcesPerWorld: 1 }
      );

      expect(
        captureCoreError(() => eventLimited.advanceClock(world.worldId, 1)).code
      ).toBe('QuotaExceeded');
      expect(
        captureCoreError(() => resourceLimited.advanceClock(world.worldId, 1))
          .code
      ).toBe('QuotaExceeded');
      expect(core.world(world.worldId)).toEqual(world);
    });

    it('provider の不正な event、resource、delete、transition を一件も反映しない', () => {
      const world = createWorld();
      createReadyDeployment(world.worldId);
      const original = core.resources(world.worldId)[0];
      if (!original) throw new Error('resource が作成されませんでした');
      const invalidEvent = { type: 'InvalidClockEvent', payload: {} };
      Reflect.set(invalidEvent, 'payload', []);
      const invalidResults: readonly ProviderClockResult[] = [
        {
          events: [invalidEvent],
          resources: [],
          deletedResourceIds: [],
          appliedTransitionIds: [],
        },
        {
          events: [],
          resources: [{ ...original, provider: 'beta' }],
          deletedResourceIds: [],
          appliedTransitionIds: [],
        },
        {
          events: [],
          resources: [original, original],
          deletedResourceIds: [],
          appliedTransitionIds: [],
        },
        {
          events: [],
          resources: [],
          deletedResourceIds: ['missing-resource'],
          appliedTransitionIds: [],
        },
        {
          events: [],
          resources: [],
          deletedResourceIds: [],
          appliedTransitionIds: [
            'duplicate-transition',
            'duplicate-transition',
          ],
        },
      ];

      for (const result of invalidResults) {
        const clockCore = new SimulationCore(
          store,
          new ProviderRegistry([
            new ClockProvider('alpha', 'engine-a', () => result),
          ])
        );
        expect(
          captureCoreError(() => clockCore.advanceClock(world.worldId, 1)).code
        ).toBe('ValidationFailed');
      }
      expect(core.world(world.worldId)).toEqual(world);
    });

    it('ゼロ、負数、小数、無限大、安全整数外の clock advance を拒否する', () => {
      for (const milliseconds of [
        0,
        -1,
        1.5,
        Number.POSITIVE_INFINITY,
        Number.MAX_SAFE_INTEGER + 1,
      ]) {
        expect(
          captureCoreError(() =>
            core.advanceClock('validation-before-world-lookup', milliseconds)
          ).code
        ).toBe('ValidationFailed');
      }
    });

    it('clock event quota を超える advance を拒否する', () => {
      const world = createWorld();
      const limited = new SimulationCore(store, registry, {
        maxEventsPerWorld: 1,
      });

      expect(
        captureCoreError(() => limited.advanceClock(world.worldId, 1)).code
      ).toBe('QuotaExceeded');
      expect(core.world(world.worldId).virtualTime).toBe(world.virtualTime);
    });

    it('world delete は resource と deployment を論理削除し再送を idempotent にする', () => {
      const world = createWorld();
      createReadyDeployment(world.worldId);

      core.deleteWorld(world.worldId);
      const eventCount = core.events(world.worldId).length;

      expect(core.world(world.worldId).status).toBe('deleted');
      expect(
        store
          .resources(world.worldId)
          .every((item) => item.status === 'deleted')
      ).toBe(true);
      expect(
        store
          .deployments(world.worldId)
          .every((item) => item.status === 'deleted')
      ).toBe(true);
      expect(core.events(world.worldId).at(-1)?.type).toBe('WorldDeleted');

      core.deleteWorld(world.worldId);
      expect(core.events(world.worldId)).toHaveLength(eventCount);
    });

    it('削除済み world の clock は進めない', () => {
      const world = createWorld();
      core.deleteWorld(world.worldId);

      const error = captureCoreError(() => core.advanceClock(world.worldId, 1));

      expect(error.code).toBe('Conflict');
    });

    it('delete event quota 超過時は world を active のまま保つ', async () => {
      const world = createWorld();
      const limited = new SimulationCore(store, registry, {
        maxEventsPerWorld: 1,
      });

      expect(
        (await captureCoreErrorAsync(() => limited.deleteWorld(world.worldId)))
          .code
      ).toBe('QuotaExceeded');
      expect(core.world(world.worldId).status).toBe('active');
    });
  });

  describe('snapshot hash と restore', () => {
    it('snapshot を hash 検証して新しい world へ復元し、同じ key の再送を idempotent にする', () => {
      const source = createWorld({
        ...WORLD_INPUT,
        seed: 'snapshot-seed',
        virtualTime: '2026-07-12T00:00:00.000Z',
      });
      const deployment = createReadyDeployment(source.worldId);
      core.executeCommand(
        source.worldId,
        commandInput(deployment.deploymentId, 'put', {
          resourceId: 'snapshot-resource',
          value: 'persisted',
        }),
        'snapshot-command-key'
      );
      core.advanceClock(source.worldId, 2_000);
      const snapshot = core.exportSnapshot(source.worldId);

      expect(snapshot.hash).toBe(contentHash(snapshot.payload));
      const restored = core.restoreSnapshot(snapshot, 'restore-key');
      const repeated = core.restoreSnapshot(snapshot, 'restore-key');

      expect(restored.worldId).not.toBe(source.worldId);
      expect(repeated).toEqual(restored);
      expect(restored.seed).toBe(source.seed);
      expect(restored.virtualTime).toBe('2026-07-12T00:00:02.000Z');
      expect(
        store.deployments(restored.worldId).map((item) => ({
          ...item,
          worldId: source.worldId,
        }))
      ).toEqual([...snapshot.payload.deployments]);
      expect(
        store.resources(restored.worldId).map((item) => ({
          ...item,
          worldId: source.worldId,
        }))
      ).toEqual([...snapshot.payload.resources]);
      expect(core.events(restored.worldId).at(-1)?.type).toBe(
        'SnapshotRestored'
      );
      expect(core.events(restored.worldId)).toHaveLength(
        snapshot.payload.events.length + 1
      );

      const another = core.restoreSnapshot(snapshot, 'restore-key-two');
      expect(another.worldId).not.toBe(restored.worldId);
    });

    it('hash 改ざん、未知 version、空 key を Snapshot/Validation error にする', () => {
      const world = createWorld();
      const snapshot = core.exportSnapshot(world.worldId);
      const changedHash: WorldSnapshot = { ...snapshot, hash: 'changed' };
      expect(
        captureCoreError(() =>
          core.restoreSnapshot(changedHash, 'changed-hash')
        ).code
      ).toBe('SnapshotIncompatible');

      const changedVersion = structuredClone(snapshot);
      Reflect.set(changedVersion.payload, 'snapshotVersion', '2');
      expect(
        captureCoreError(() =>
          core.restoreSnapshot(changedVersion, 'changed-version')
        ).code
      ).toBe('SnapshotIncompatible');
      expect(
        captureCoreError(() => core.restoreSnapshot(snapshot, '   ')).code
      ).toBe('ValidationFailed');
    });

    it('snapshot event quota と resource quota を import 前に検証する', () => {
      const world = createWorld();
      createReadyDeployment(world.worldId);
      const snapshot = core.exportSnapshot(world.worldId);
      const eventLimited = new SimulationCore(store, registry, {
        maxEventsPerWorld: snapshot.payload.events.length,
      });
      const resourceLimited = new SimulationCore(store, registry, {
        maxResourcesPerWorld: 0,
      });

      expect(
        captureCoreError(() =>
          eventLimited.restoreSnapshot(snapshot, 'event-limited-restore')
        ).code
      ).toBe('QuotaExceeded');
      expect(
        captureCoreError(() =>
          resourceLimited.restoreSnapshot(snapshot, 'resource-limited-restore')
        ).code
      ).toBe('QuotaExceeded');
    });

    it('存在しない world の event と snapshot export を NotFound にする', () => {
      expect(captureCoreError(() => core.events('missing-world')).code).toBe(
        'NotFound'
      );
      expect(
        captureCoreError(() => core.exportSnapshot('missing-world')).code
      ).toBe('NotFound');
    });
  });
});
