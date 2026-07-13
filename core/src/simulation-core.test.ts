import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { contentHash, deterministicId } from './canonical';
import type {
  CreateWorldInput,
  DeploymentInput,
  EventRecord,
  ExecuteCommandInput,
  MaterializedWorkload,
  ProviderCapability,
  ProviderClockInput,
  ProviderClockResult,
  ProviderCommandResult,
  ProviderCompileInput,
  ProviderDeploymentResult,
  ProviderModule,
  ProviderTargetPlan,
  ProviderWorldView,
  ResourceRecord,
  SingleRuntimeTarget,
  SnapshotPayload,
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

interface Deferred {
  readonly promise: Promise<void>;
  readonly resolve: () => void;
}

function deferred(): Deferred {
  let resolve = (): void => {
    throw new Error('deferred promise is not initialized');
  };
  const promise = new Promise<void>((complete) => {
    resolve = complete;
  });
  return { promise, resolve };
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function requiredResource(
  resources: readonly ResourceRecord[],
  predicate: (resource: ResourceRecord) => boolean,
  message: string
): ResourceRecord {
  const resource = resources.find(predicate);
  if (!resource) throw new Error(message);
  return resource;
}

function materializedUrl(resource: ResourceRecord, message: string): string {
  const materialization = resource.properties['materialization'];
  if (!isRecord(materialization)) throw new Error(message);
  const endpoint = materialization['endpoint'];
  if (typeof endpoint !== 'string') throw new Error(message);
  return endpoint;
}

function forgedPortablePayload(
  snapshot: WorldSnapshot,
  workload: ResourceRecord,
  endpointResource: ResourceRecord,
  oldUrl: string
): SnapshotPayload {
  return {
    ...snapshot.payload,
    resources: snapshot.payload.resources.map((resource) => {
      if (resource === workload) {
        return {
          ...resource,
          properties: {
            ...resource.properties,
            materialization: { endpoint: 'http://127.0.0.1:1' },
          },
        };
      }
      if (resource === endpointResource) {
        return {
          ...resource,
          resourceType: 'Runtime::Endpoint',
          properties: {
            ...resource.properties,
            ManagedPlacement: { EffectiveUrl: oldUrl },
            state: { overrideUrl: oldUrl, preserved: true },
          },
        };
      }
      return resource;
    }),
  };
}

class LocalWorkloadEffects implements WorkloadEffectPort {
  readonly received: WorkloadDeclaration[][] = [];
  readonly cleanupWorlds: string[] = [];
  readonly cleanupStarts: (() => void)[] = [];
  readonly cleanupGates: Promise<void>[] = [];
  readonly faults: (MaterializationFault | undefined)[] = [];
  readonly gates: Promise<void>[] = [];
  readonly #servers = new Map<string, Bun.Server<undefined>[]>();
  cleanupFails = false;

  async materialize(
    worldId: string,
    declarations: readonly WorkloadDeclaration[]
  ): Promise<readonly MaterializedWorkload[]> {
    this.received.push(declarations.map((declaration) => ({ ...declaration })));
    const gate = this.gates.shift();
    if (gate) await gate;
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

  hasActiveWorld(worldId: string): boolean {
    return (this.#servers.get(worldId)?.length ?? 0) > 0;
  }

  async cleanup(worldId: string): Promise<void> {
    this.cleanupWorlds.push(worldId);
    this.cleanupStarts.shift()?.();
    const gate = this.cleanupGates.shift();
    if (gate) await gate;
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
  ): ProviderDeploymentResult {
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
  ): ProviderCommandResult {
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

type InvalidPlanIdentity =
  | 'targetId'
  | 'provider'
  | 'engine'
  | 'resourceProvider';

class InvalidPlanIdentityProvider extends DeterministicProvider {
  constructor(
    provider: string,
    engine: string,
    readonly invalidIdentity: InvalidPlanIdentity
  ) {
    super(provider, engine);
  }

  override compile(input: ProviderCompileInput): ProviderTargetPlan {
    const plan = super.compile(input);
    switch (this.invalidIdentity) {
      case 'targetId':
        return { ...plan, targetId: 'different-target' };
      case 'provider':
        return { ...plan, provider: 'different-provider' };
      case 'engine':
        return { ...plan, engine: 'different-engine' };
      case 'resourceProvider':
        return {
          ...plan,
          resources: plan.resources.map((resource) => ({
            ...resource,
            provider: 'different-provider',
          })),
        };
    }
  }
}

class InvalidDeploymentResultProvider extends DeterministicProvider {
  override deploy(
    plan: ProviderTargetPlan,
    world: ProviderWorldView
  ): ProviderDeploymentResult {
    const result = super.deploy(plan, world);
    return {
      ...result,
      resources: result.resources.map((resource) => ({
        ...resource,
        provider: 'different-provider',
      })),
    };
  }
}

class MutatingDeploymentPlanProvider extends DeterministicProvider {
  override deploy(
    plan: ProviderTargetPlan,
    world: ProviderWorldView
  ): ProviderDeploymentResult {
    Reflect.set(plan, 'targetId', 'mutated-target');
    Reflect.set(plan, 'provider', 'mutated-provider');
    for (const resource of plan.resources) {
      Reflect.set(resource, 'provider', 'mutated-provider');
    }
    return super.deploy(plan, world);
  }
}

class MutatingCompileInputProvider extends DeterministicProvider {
  override compile(input: ProviderCompileInput): ProviderTargetPlan {
    Reflect.set(input.target, 'id', 'hijacked-target');
    return {
      ...super.compile(input),
      targetId: input.target.id ?? 'hijacked-target',
    };
  }
}

class InvalidCommandResultProvider extends DeterministicProvider {
  override reduce(
    command: Parameters<ProviderModule['reduce']>[0],
    world: Parameters<ProviderModule['reduce']>[1]
  ): ProviderCommandResult {
    const result = super.reduce(command, world);
    return {
      ...result,
      resources: result.resources.map((resource) => ({
        ...resource,
        provider: 'different-provider',
      })),
    };
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

class DeployHookProvider extends DeterministicProvider {
  afterDeploy: (() => void) | undefined;

  override deploy(
    plan: ProviderTargetPlan,
    world: ProviderWorldView
  ): ProviderDeploymentResult {
    const result = super.deploy(plan, world);
    const afterDeploy = this.afterDeploy;
    this.afterDeploy = undefined;
    afterDeploy?.();
    return result;
  }
}

class SwapResourceProvider extends DeterministicProvider {
  override reduce(
    command: Parameters<ProviderModule['reduce']>[0],
    world: Parameters<ProviderModule['reduce']>[1]
  ): ProviderCommandResult {
    const result = super.reduce(command, world);
    const deletedResourceId = command.input['deletedResourceId'];
    return {
      ...result,
      deletedResourceIds:
        typeof deletedResourceId === 'string' ? [deletedResourceId] : [],
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
  gate: Promise<void> | undefined;
  calls = 0;

  async reduceAsync(
    command: Parameters<ProviderModule['reduce']>[0],
    world: Parameters<ProviderModule['reduce']>[1]
  ) {
    this.calls += 1;
    await (this.gate ?? Promise.resolve());
    return super.reduce(command, world);
  }
}

class ProjectionReadProvider extends DeterministicProvider {
  afterRead: (() => void) | undefined;
  calls = 0;

  commandMode() {
    return 'projection-read' as const;
  }

  override reduce(
    command: Parameters<ProviderModule['reduce']>[0],
    world: Parameters<ProviderModule['reduce']>[1]
  ): ProviderCommandResult {
    this.calls += 1;
    if (command.input['mutate'] === true) {
      return super.reduce(command, world);
    }
    const response = {
      calls: this.calls,
      observedResources: world.resources.length,
    };
    this.afterRead?.();
    return {
      events: [],
      resources: [],
      deletedResourceIds: [],
      outputs: {},
      response,
    };
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

    it('namespace と deployment から response loss 後の world を回復し deleted も再 cleanup できる', async () => {
      const created = createWorld();
      const lookupNamespace: WorldNamespace = {
        tenantId: created.tenantId,
        eventId: created.eventId,
        teamId: created.teamId,
      };

      expect(
        core.worldByDeployment(
          lookupNamespace,
          created.deploymentId,
          'world-key'
        )
      ).toEqual(created);
      expect(
        captureCoreError(() =>
          core.worldByDeployment(
            { ...lookupNamespace, teamId: 'other-team' },
            created.deploymentId,
            'world-key'
          )
        ).code
      ).toBe('NotFound');
      expect(
        captureCoreError(() =>
          core.worldByDeployment(
            lookupNamespace,
            'missing-deployment',
            'world-key'
          )
        ).code
      ).toBe('NotFound');
      expect(
        captureCoreError(() =>
          core.worldByDeployment(
            lookupNamespace,
            created.deploymentId,
            'missing-key'
          )
        ).code
      ).toBe('NotFound');

      await core.deleteWorld(created.worldId);
      expect(
        core.worldByDeployment(
          lookupNamespace,
          created.deploymentId,
          'world-key'
        ).status
      ).toBe('deleted');
      await core.deleteWorld(created.worldId);
    });

    it('同じ namespace と deployment に複数 world があっても create key で元 world を特定する', () => {
      const first = createWorld(WORLD_INPUT, 'first-world-key');
      const second = createWorld(WORLD_INPUT, 'second-world-key');

      expect(
        core.worldByDeployment(
          WORLD_INPUT,
          WORLD_INPUT.deploymentId,
          'first-world-key'
        )
      ).toEqual(first);
      expect(
        core.worldByDeployment(
          WORLD_INPUT,
          WORLD_INPUT.deploymentId,
          'second-world-key'
        )
      ).toEqual(second);
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

    it('should not overwrite an existing deployment identity with a new idempotency key', () => {
      const world = createWorld();
      const first = createReadyDeployment(world.worldId);
      const eventsBefore = core.events(world.worldId);
      const resourcesBefore = core.resources(world.worldId);

      expect(
        captureCoreError(() =>
          core.createDeployment(
            world.worldId,
            { ...singleDeployment(), problemId: 'replacement-problem' },
            'replacement-deployment-key'
          )
        ).code
      ).toBe('Conflict');
      expect(core.deployment(world.worldId, first.deploymentId)).toEqual(first);
      expect(core.events(world.worldId)).toEqual(eventsBefore);
      expect(core.resources(world.worldId)).toEqual(resourcesBefore);
    });

    it('should return the same-key deployment winner committed during provider evaluation', () => {
      const world = createWorld();
      const input = singleDeployment('interleaved-deployment');
      const provider = new DeployHookProvider('alpha', 'engine-a');
      const evaluatingCore = new SimulationCore(
        store,
        new ProviderRegistry([provider])
      );
      const concurrentStore = new SimulationStore(
        path.join(directory, 'simulation.sqlite')
      );
      const concurrentCore = new SimulationCore(concurrentStore, registry);
      let winner: ReturnType<SimulationCore['createDeployment']> | undefined;
      provider.afterDeploy = () => {
        winner = concurrentCore.createDeployment(
          world.worldId,
          input,
          'interleaved-deployment-key'
        );
      };

      try {
        const replayed = evaluatingCore.createDeployment(
          world.worldId,
          input,
          'interleaved-deployment-key'
        );
        if (!winner) throw new Error('concurrent deployment did not commit');
        expect(replayed).toEqual(winner);
        expect(
          evaluatingCore
            .events(world.worldId)
            .filter((event) => event.type === 'DeploymentRequested')
        ).toHaveLength(1);
        expect(evaluatingCore.resources(world.worldId)).toHaveLength(1);
      } finally {
        concurrentStore.close();
      }
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
        expect(store.reservedEventCount(world.worldId)).toBe(0);

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

    it('同じ workload retry を single-flight にして effect と成功 event を一度だけ実行する', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'single-flight-workload'
      );
      effects.faults.push('throw');
      expect(
        (
          await captureCoreErrorAsync(() =>
            core.materializeWorkloads(world.worldId, deployment.deploymentId)
          )
        ).code
      ).toBe('WorkloadEffectFailed');
      const gate = deferred();
      effects.gates.push(gate.promise);

      const first = core.materializeWorkloads(
        world.worldId,
        deployment.deploymentId
      );
      const duplicate = core.materializeWorkloads(
        world.worldId,
        deployment.deploymentId
      );
      await Promise.resolve();
      expect(effects.received).toHaveLength(2);
      gate.resolve();
      const [firstResult, duplicateResult] = await Promise.all([
        first,
        duplicate,
      ]);

      expect(duplicateResult).toEqual(firstResult);
      expect(effects.received).toHaveLength(2);
      expect(
        core
          .events(world.worldId)
          .filter((event) => event.type === 'WorkloadMaterialized')
      ).toHaveLength(1);
      expect(store.reservedEventCount(world.worldId)).toBe(0);
      expect(
        core
          .events(world.worldId)
          .filter((event) => event.type === 'DeploymentReady')
      ).toHaveLength(1);
    });

    it('should reject duplicate materialization from another live store before running a second effect', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'cross-store-materialization'
      );
      const concurrentStore = new SimulationStore(
        path.join(directory, 'simulation.sqlite')
      );
      const concurrentCore = new SimulationCore(concurrentStore, registry, {
        workloadEffects: effects,
      });
      const gate = deferred();
      effects.gates.push(gate.promise);

      try {
        const first = core.materializeWorkloads(
          world.worldId,
          deployment.deploymentId
        );
        await Promise.resolve();
        expect(effects.received).toHaveLength(1);
        expect(
          (
            await captureCoreErrorAsync(() =>
              concurrentCore.materializeWorkloads(
                world.worldId,
                deployment.deploymentId
              )
            )
          ).code
        ).toBe('Conflict');
        expect(effects.received).toHaveLength(1);

        gate.resolve();
        expect((await first).status).toBe('ready');
        expect(
          (
            await concurrentCore.materializeWorkloads(
              world.worldId,
              deployment.deploymentId
            )
          ).status
        ).toBe('ready');
        expect(effects.received).toHaveLength(1);
      } finally {
        gate.resolve();
        concurrentStore.close();
      }
    });

    it('should keep a cross-store delete behind a live materialization lease', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'cross-store-delete-behind-materialization'
      );
      const deletingStore = new SimulationStore(
        path.join(directory, 'simulation.sqlite')
      );
      const deletingCore = new SimulationCore(deletingStore, registry, {
        workloadEffects: effects,
      });
      const gate = deferred();
      effects.gates.push(gate.promise);

      try {
        const materialization = core.materializeWorkloads(
          world.worldId,
          deployment.deploymentId
        );
        await Promise.resolve();
        expect(effects.received).toHaveLength(1);

        expect(
          (
            await captureCoreErrorAsync(() =>
              deletingCore.deleteWorld(world.worldId)
            )
          ).code
        ).toBe('Conflict');
        expect(effects.cleanupWorlds).toEqual([]);
        expect(core.world(world.worldId).status).toBe('active');

        gate.resolve();
        await materialization;
        await deletingCore.deleteWorld(world.worldId);
        expect(core.world(world.worldId).status).toBe('deleted');
        expect(effects.hasActiveWorld(world.worldId)).toBe(false);
      } finally {
        gate.resolve();
        deletingStore.close();
      }
    });

    it('should reject a materialization result when workload declaration content changes during the effect', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'materialization-content-guard'
      );
      const concurrentStore = new SimulationStore(
        path.join(directory, 'simulation.sqlite')
      );
      const gate = deferred();
      effects.gates.push(gate.promise);

      try {
        const materialization = core.materializeWorkloads(
          world.worldId,
          deployment.deploymentId
        );
        await Promise.resolve();
        const workload = requiredResource(
          concurrentStore.resources(world.worldId),
          (resource) => resource.resourceType === 'Runtime::Workload',
          'workload resource is missing'
        );
        concurrentStore.transaction(() => {
          concurrentStore.saveResource({
            ...workload,
            properties: {
              ...workload.properties,
              command: ['tampered-after-reservation'],
            },
          });
        });
        gate.resolve();

        expect((await captureCoreErrorAsync(() => materialization)).code).toBe(
          'WorkloadEffectFailed'
        );
        const stored = requiredResource(
          concurrentStore.resources(world.worldId),
          (resource) => resource.resourceId === workload.resourceId,
          'guarded workload resource is missing'
        );
        expect(stored.properties['command']).toEqual([
          'tampered-after-reservation',
        ]);
        expect(stored.properties['materialization']).toBeUndefined();
        expect(stored.status).toBe('failed');
        expect(
          core
            .events(world.worldId)
            .filter((event) => event.type === 'WorkloadMaterialized')
        ).toHaveLength(0);
        expect(store.reservedEventCount(world.worldId)).toBe(0);
      } finally {
        gate.resolve();
        concurrentStore.close();
      }
    });

    it('should clean up materialization when its active world disappears and preserve cleanup failure', async () => {
      const effects = enableWorkloadEffects();
      const workloads: readonly WorkloadDeclaration[] = [
        {
          id: 'api',
          targetId: 'default',
          resourceRef: 'ApiFunction',
          image: WORKLOAD_IMAGE,
          command: ['bun', 'run', 'start'],
          containerPort: 3000,
          healthPath: '/healthz',
        },
        {
          id: 'worker',
          targetId: 'default',
          resourceRef: 'WorkerFunction',
          image: WORKLOAD_IMAGE,
          command: ['bun', 'run', 'start'],
          containerPort: 3000,
          healthPath: '/healthz',
        },
      ];

      for (const [index, cleanupFails] of [false, true].entries()) {
        const world = createWorld(
          { ...WORLD_INPUT, teamId: `lost-world-${index}` },
          `lost-world-${index}`
        );
        const deployment = core.createDeployment(
          world.worldId,
          {
            ...singleDeployment(`lost-world-deployment-${index}`),
            simulationOverlay: {
              schemaVersion: '1',
              workloads,
            },
          },
          `lost-world-deployment-${index}`
        );
        const gate = deferred();
        effects.gates.push(gate.promise);
        effects.cleanupFails = cleanupFails;

        const materialization = core.materializeWorkloads(
          world.worldId,
          deployment.deploymentId
        );
        await Promise.resolve();
        expect(effects.received.at(-1)).toHaveLength(2);
        store.transaction(() => {
          store.setWorldState(world.worldId, world.virtualTime, 'deleted');
        });
        gate.resolve();

        const error = await captureCoreErrorAsync(() => materialization);
        expect(error).toMatchObject({
          code: 'WorkloadEffectFailed',
          message: cleanupFails
            ? 'workload materialization cleanup failed and can be retried'
            : 'workload materialization lost its active world and was cleaned up',
        });
        expect(effects.cleanupWorlds.at(-1)).toBe(world.worldId);
        expect(effects.hasActiveWorld(world.worldId)).toBe(cleanupFails);
        expect(store.reservedEventCount(world.worldId)).toBe(0);
        if (cleanupFails) {
          effects.cleanupFails = false;
          await core.deleteWorld(world.worldId);
          expect(effects.hasActiveWorld(world.worldId)).toBe(false);
        }
      }
    });

    it('materialize 中の delete は effect 完了後に cleanup して deleted projection を維持する', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'delete-materializing-workload'
      );
      const gate = deferred();
      effects.gates.push(gate.promise);

      const materialization = core.materializeWorkloads(
        world.worldId,
        deployment.deploymentId
      );
      await Promise.resolve();
      expect(effects.received).toHaveLength(1);
      const deleter = new SimulationCore(store, registry, {
        workloadEffects: effects,
      });
      const deletion = deleter.deleteWorld(world.worldId);
      expect(
        (
          await captureCoreErrorAsync(() =>
            core.materializeWorkloads(world.worldId, deployment.deploymentId)
          )
        ).code
      ).toBe('Conflict');
      gate.resolve();
      await materialization;
      await deletion;

      expect(effects.cleanupWorlds).toEqual([world.worldId]);
      expect(effects.hasActiveWorld(world.worldId)).toBe(false);
      expect(core.world(world.worldId).status).toBe('deleted');
      expect(
        core.deployment(world.worldId, deployment.deploymentId).status
      ).toBe('deleted');
      expect(
        core
          .resources(world.worldId)
          .every((resource) => resource.status === 'deleted')
      ).toBe(true);
      expect(core.events(world.worldId).at(-1)?.type).toBe('WorldDeleted');
      expect(store.reservedEventCount(world.worldId)).toBe(0);
    });

    it('should prevent a cross-store materialization after delete intent commits', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'materialization-behind-cross-store-delete'
      );
      const deletingStore = new SimulationStore(
        path.join(directory, 'simulation.sqlite')
      );
      const deletingCore = new SimulationCore(deletingStore, registry, {
        workloadEffects: effects,
      });
      const cleanupStarted = deferred();
      const cleanupGate = deferred();
      effects.cleanupStarts.push(cleanupStarted.resolve);
      effects.cleanupGates.push(cleanupGate.promise);

      try {
        const deletion = deletingCore.deleteWorld(world.worldId);
        await cleanupStarted.promise;
        expect(deletingStore.reservedEventCount(world.worldId)).toBe(1);

        expect(
          (
            await captureCoreErrorAsync(() =>
              core.materializeWorkloads(world.worldId, deployment.deploymentId)
            )
          ).code
        ).toBe('Conflict');
        expect(effects.received).toHaveLength(0);

        cleanupGate.resolve();
        await deletion;
        expect(core.world(world.worldId).status).toBe('deleted');
      } finally {
        cleanupGate.resolve();
        deletingStore.close();
      }
    });

    it('別 store の live materialization lease 中は delete cleanup を開始しない', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'cross-store-delete-workload'
      );
      const gate = deferred();
      effects.gates.push(gate.promise);
      const materialization = core.materializeWorkloads(
        world.worldId,
        deployment.deploymentId
      );
      await Promise.resolve();
      expect(effects.received).toHaveLength(1);
      const databasePath = path.join(directory, 'simulation.sqlite');
      const deletingStore = new SimulationStore(databasePath);
      const deletingCore = new SimulationCore(deletingStore, registry, {
        workloadEffects: effects,
      });
      try {
        expect(
          (
            await captureCoreErrorAsync(() =>
              deletingCore.deleteWorld(world.worldId)
            )
          ).code
        ).toBe('Conflict');
        expect(effects.cleanupWorlds).toEqual([]);
        expect(core.world(world.worldId).status).toBe('active');

        gate.resolve();
        await materialization;
        await deletingCore.deleteWorld(world.worldId);
        expect(effects.hasActiveWorld(world.worldId)).toBe(false);
        expect(core.world(world.worldId).status).toBe('deleted');
      } finally {
        gate.resolve();
        deletingStore.close();
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
      expect(store.reservedEventCount(world.worldId)).toBe(1);
      expect(core.world(world.worldId).status).toBe('active');
      expect(
        core.resources(world.worldId).some((item) => item.status === 'ready')
      ).toBe(true);

      effects.cleanupFails = false;
      await core.deleteWorld(world.worldId);
      await core.deleteWorld(world.worldId);
      expect(effects.cleanupWorlds).toEqual([
        world.worldId,
        world.worldId,
        world.worldId,
      ]);
      expect(core.world(world.worldId).status).toBe('deleted');
      expect(
        core.resources(world.worldId).every((item) => item.status === 'deleted')
      ).toBe(true);
    });

    it('should preserve delete intent after a failed commit and finish on retry', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'delete-commit-failure'
      );
      await core.materializeWorkloads(world.worldId, deployment.deploymentId);
      const pendingDeployment = core.createDeployment(
        world.worldId,
        workloadDeployment('delete-commit-pending-workload'),
        'delete-commit-pending-workload'
      );
      const eventCount = core.events(world.worldId).length;
      const limitedCore = new SimulationCore(store, registry, {
        maxEventsPerWorld: eventCount + 1,
        workloadEffects: effects,
      });
      const foreignStore = new SimulationStore(
        path.join(directory, 'simulation.sqlite')
      );
      const cleanupGate = deferred();
      const cleanupStarted = deferred();
      effects.cleanupStarts.push(cleanupStarted.resolve);
      effects.cleanupGates.push(cleanupGate.promise);

      try {
        const deletion = limitedCore.deleteWorld(world.worldId);
        await cleanupStarted.promise;
        expect(store.reservedEventCount(world.worldId)).toBe(1);
        foreignStore.transaction(() => {
          foreignStore.appendEvent(
            world.worldId,
            'ForeignEventWithoutQuotaGuard',
            'foreign-event-command',
            {}
          );
        });
        cleanupGate.resolve();

        expect((await captureCoreErrorAsync(() => deletion)).code).toBe(
          'QuotaExceeded'
        );
        expect(store.reservedEventCount(world.worldId)).toBe(1);
        expect(core.world(world.worldId).status).toBe('active');
        expect(core.events(world.worldId)).toHaveLength(eventCount + 1);
        expect(
          (
            await captureCoreErrorAsync(() =>
              core.materializeWorkloads(
                world.worldId,
                pendingDeployment.deploymentId
              )
            )
          ).code
        ).toBe('Conflict');

        await core.deleteWorld(world.worldId);
        expect(core.world(world.worldId).status).toBe('deleted');
        expect(store.reservedEventCount(world.worldId)).toBe(0);
      } finally {
        cleanupGate.resolve();
        foreignStore.close();
      }
    });

    it('should enforce delete quota across processes and let an open survivor reclaim a crashed deletion', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'delete-quota-reservation'
      );
      await core.materializeWorkloads(world.worldId, deployment.deploymentId);
      const eventCount = core.events(world.worldId).length;
      const maxEventsPerWorld = eventCount + 1;
      const databasePath = path.join(directory, 'simulation.sqlite');
      const survivorStore = new SimulationStore(databasePath);
      const survivorCore = new SimulationCore(survivorStore, registry, {
        maxEventsPerWorld,
        workloadEffects: effects,
      });
      const storeModulePath = path.join(import.meta.dir, 'store.ts');
      const coreModulePath = path.join(import.meta.dir, 'simulation-core.ts');
      const registryModulePath = path.join(
        import.meta.dir,
        'provider-registry.ts'
      );
      const child = Bun.spawn({
        cmd: [
          process.execPath,
          '--eval',
          `
            import { SimulationCore } from ${JSON.stringify(coreModulePath)};
            import { ProviderRegistry } from ${JSON.stringify(registryModulePath)};
            import { SimulationStore } from ${JSON.stringify(storeModulePath)};
            const childStore = new SimulationStore(${JSON.stringify(databasePath)});
            const heldCleanup = {
              async materialize() { return []; },
              async cleanup() {
                console.log('DELETE_RESERVED');
                await new Promise(() => {});
              },
            };
            const childCore = new SimulationCore(
              childStore,
              new ProviderRegistry(),
              {
                maxEventsPerWorld: ${maxEventsPerWorld},
                workloadEffects: heldCleanup,
              },
            );
            await childCore.deleteWorld(${JSON.stringify(world.worldId)});
          `,
        ],
        cwd: import.meta.dir,
        stdout: 'pipe',
        stderr: 'pipe',
      });

      try {
        const reader = child.stdout.getReader();
        let timeout: ReturnType<typeof setTimeout> | undefined;
        const ready = await (async () => {
          try {
            return await Promise.race([
              reader.read(),
              new Promise<never>((_, reject) => {
                timeout = setTimeout(
                  () => reject(new Error('delete reservation child timeout')),
                  5_000
                );
              }),
            ]);
          } finally {
            if (timeout) clearTimeout(timeout);
          }
        })();
        expect(new TextDecoder().decode(ready.value)).toContain(
          'DELETE_RESERVED'
        );
        expect(survivorStore.reservedEventCount(world.worldId)).toBe(1);
        expect(
          captureCoreError(() =>
            survivorCore.createDeployment(
              world.worldId,
              singleDeployment(
                'blocked-deployment',
                'missing-provider',
                'missing-engine'
              ),
              'blocked-deployment-key'
            )
          ).code
        ).toBe('QuotaExceeded');
        expect(
          captureCoreError(() => survivorCore.advanceClock(world.worldId, 1))
            .code
        ).toBe('QuotaExceeded');
        expect(survivorCore.events(world.worldId)).toHaveLength(eventCount);
        expect(
          survivorStore.deployment(world.worldId, 'blocked-deployment')
        ).toBeUndefined();

        child.kill('SIGKILL');
        await child.exited;
        expect(survivorStore.reservedEventCount(world.worldId)).toBe(1);
        await survivorCore.deleteWorld(world.worldId);

        expect(survivorCore.events(world.worldId)).toHaveLength(
          maxEventsPerWorld
        );
        expect(survivorCore.world(world.worldId).status).toBe('deleted');
        expect(survivorStore.reservedEventCount(world.worldId)).toBe(0);
      } finally {
        if (child.exitCode === null) child.kill('SIGKILL');
        await child.exited;
        survivorStore.close();
      }
    });

    it('should keep a crashed delete intent ahead of materialization and reconcile it on the open survivor', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment('crashed-delete-pending-workload'),
        'crashed-delete-pending-workload'
      );
      const databasePath = path.join(directory, 'simulation.sqlite');
      const survivorStore = new SimulationStore(databasePath);
      const survivorCore = new SimulationCore(survivorStore, registry, {
        workloadEffects: effects,
      });
      const storeModulePath = path.join(import.meta.dir, 'store.ts');
      const coreModulePath = path.join(import.meta.dir, 'simulation-core.ts');
      const registryModulePath = path.join(
        import.meta.dir,
        'provider-registry.ts'
      );
      const child = Bun.spawn({
        cmd: [
          process.execPath,
          '--eval',
          `
            import { Database } from 'bun:sqlite';
            import { SimulationCore } from ${JSON.stringify(coreModulePath)};
            import { ProviderRegistry } from ${JSON.stringify(registryModulePath)};
            import { SimulationStore } from ${JSON.stringify(storeModulePath)};
            const childStore = new SimulationStore(${JSON.stringify(databasePath)});
            let commitBlocker;
            const heldCleanup = {
              async materialize() { return []; },
              async cleanup() {
                commitBlocker = new Database(${JSON.stringify(databasePath)});
                commitBlocker.exec('PRAGMA busy_timeout = 5000');
                commitBlocker.exec('BEGIN IMMEDIATE');
                console.log('DELETE_CLEANUP_SUCCEEDED');
              },
            };
            const childCore = new SimulationCore(
              childStore,
              new ProviderRegistry(),
              { workloadEffects: heldCleanup },
            );
            await childCore.deleteWorld(${JSON.stringify(world.worldId)});
          `,
        ],
        cwd: import.meta.dir,
        stdout: 'pipe',
        stderr: 'pipe',
      });

      try {
        const reader = child.stdout.getReader();
        const ready = await Promise.race([
          reader.read(),
          new Promise<never>((_, reject) =>
            setTimeout(
              () => reject(new Error('delete intent child timeout')),
              5_000
            )
          ),
        ]);
        expect(new TextDecoder().decode(ready.value)).toContain(
          'DELETE_CLEANUP_SUCCEEDED'
        );
        child.kill('SIGKILL');
        await child.exited;

        expect(
          (
            await captureCoreErrorAsync(() =>
              survivorCore.materializeWorkloads(
                world.worldId,
                deployment.deploymentId
              )
            )
          ).code
        ).toBe('Conflict');
        expect(effects.received).toHaveLength(0);

        await survivorCore.reconcilePendingLifecycleOperations();
        expect(survivorCore.world(world.worldId).status).toBe('deleted');
        expect(survivorStore.reservedEventCount(world.worldId)).toBe(0);
      } finally {
        if (child.exitCode === null) child.kill('SIGKILL');
        await child.exited;
        if (survivorCore.world(world.worldId).status === 'active') {
          await survivorCore.deleteWorld(world.worldId).catch(() => undefined);
        }
        survivorStore.close();
      }
    });

    it('Composite の target 数、ID 形式、必須、一意性を検証する', () => {
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
        [target, { ...target, id: 'Invalid' }],
        [target, { ...target, id: 'invalid_target' }],
        [target, { ...target, id: `a${'b'.repeat(32)}` }],
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

    it('provider plan の target identity と resource provider が入力と違えば永続化前に拒否する', () => {
      const world = createWorld();
      const invalidIdentities: readonly InvalidPlanIdentity[] = [
        'targetId',
        'provider',
        'engine',
        'resourceProvider',
      ];

      invalidIdentities.forEach((invalidIdentity, index) => {
        const provider = `invalid-plan-${index}`;
        const engine = `engine-${index}`;
        const deploymentId = `invalid-plan-deployment-${index}`;
        const isolatedCore = new SimulationCore(
          store,
          new ProviderRegistry([
            new InvalidPlanIdentityProvider(provider, engine, invalidIdentity),
          ])
        );

        const error = captureCoreError(() =>
          isolatedCore.createDeployment(
            world.worldId,
            singleDeployment(deploymentId, provider, engine),
            `invalid-plan-key-${index}`
          )
        );

        expect(error.code).toBe('ValidationFailed');
        expect(store.deployment(world.worldId, deploymentId)).toBeUndefined();
      });
      expect(store.resources(world.worldId)).toEqual([]);
    });

    it('provider deploy result の resource provider が plan と違えば永続化前に拒否する', () => {
      const world = createWorld();
      const isolatedCore = new SimulationCore(
        store,
        new ProviderRegistry([
          new InvalidDeploymentResultProvider('invalid-result', 'engine-r'),
        ])
      );

      const error = captureCoreError(() =>
        isolatedCore.createDeployment(
          world.worldId,
          singleDeployment(
            'invalid-result-deployment',
            'invalid-result',
            'engine-r'
          ),
          'invalid-result-key'
        )
      );

      expect(error.code).toBe('ValidationFailed');
      expect(store.resources(world.worldId)).toEqual([]);
      expect(
        store.deployment(world.worldId, 'invalid-result-deployment')
      ).toBeUndefined();
    });

    it('provider が compile 入力の target を変更しても検証基準と永続化 identity を変更しない', () => {
      const world = createWorld();
      const isolatedCore = new SimulationCore(
        store,
        new ProviderRegistry([
          new MutatingCompileInputProvider('mutating-input', 'engine-i'),
        ])
      );
      const eventsBefore = isolatedCore.events(world.worldId);

      const error = captureCoreError(() =>
        isolatedCore.createDeployment(
          world.worldId,
          singleDeployment(
            'mutating-input-deployment',
            'mutating-input',
            'engine-i'
          ),
          'mutating-input-key'
        )
      );

      expect(error.code).toBe('ValidationFailed');
      expect(error.message).toContain('does not match target default');
      expect(isolatedCore.events(world.worldId)).toEqual(eventsBefore);
      expect(store.resources(world.worldId)).toEqual([]);
      expect(
        store.deployment(world.worldId, 'mutating-input-deployment')
      ).toBeUndefined();
    });

    it('provider が deploy 中に検証済み plan を変更しても永続化しない', () => {
      const world = createWorld();
      const isolatedCore = new SimulationCore(
        store,
        new ProviderRegistry([
          new MutatingDeploymentPlanProvider('mutating-plan', 'engine-p'),
        ])
      );
      const eventsBefore = isolatedCore.events(world.worldId);

      const error = captureCoreError(() =>
        isolatedCore.createDeployment(
          world.worldId,
          singleDeployment(
            'mutating-plan-deployment',
            'mutating-plan',
            'engine-p'
          ),
          'mutating-plan-key'
        )
      );

      expect(error.code).toBe('ValidationFailed');
      expect(error.message).toContain('mutated provider plan');
      expect(isolatedCore.events(world.worldId)).toEqual(eventsBefore);
      expect(store.resources(world.worldId)).toEqual([]);
      expect(
        store.deployment(world.worldId, 'mutating-plan-deployment')
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

    it('存在しない deployment を NotFound にし、削除済み world の deploy を拒否する', async () => {
      const world = createWorld();

      expect(
        captureCoreError(() => core.deployment(world.worldId, 'missing')).code
      ).toBe('NotFound');
      await core.deleteWorld(world.worldId);
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
    it('should recover a saved mutation response after its provider becomes unavailable', () => {
      const world = createWorld();
      const deployment = createReadyDeployment(world.worldId);
      const input = commandInput(deployment.deploymentId);
      const response = core.executeCommand(
        world.worldId,
        input,
        'recover-without-provider'
      );
      const providerless = new SimulationCore(store, new ProviderRegistry());

      expect(
        providerless.executeCommand(
          world.worldId,
          input,
          'recover-without-provider'
        )
      ).toEqual(response);
      expect(() =>
        providerless.executeCommand(
          world.worldId,
          input,
          'missing-provider-command'
        )
      ).toThrow('provider does not exist');
    });

    it('should reevaluate projection reads for a stable key and reject provider mutations', () => {
      const world = createWorld();
      const deployment = createReadyDeployment(world.worldId);
      const provider = new ProjectionReadProvider('alpha', 'engine-a');
      const projectionCore = new SimulationCore(
        store,
        new ProviderRegistry([provider])
      );
      const input = commandInput(deployment.deploymentId);
      const events = projectionCore.events(world.worldId).length;

      expect(
        projectionCore.executeCommand(world.worldId, input, 'stable-read-key')
      ).toMatchObject({ calls: 1 });
      expect(
        projectionCore.executeCommand(world.worldId, input, 'stable-read-key')
      ).toMatchObject({ calls: 2 });
      expect(
        store.idempotentResponse(
          `command:${world.worldId}:alpha`,
          'stable-read-key'
        )
      ).toBeUndefined();
      expect(projectionCore.events(world.worldId)).toHaveLength(events);
      expect(() =>
        projectionCore.executeCommand(
          world.worldId,
          { ...input, input: { mutate: true } },
          'mutating-read-key'
        )
      ).toThrow('projection read command returned state mutations');
      expect(projectionCore.resources(world.worldId)).toHaveLength(1);
      expect(projectionCore.events(world.worldId)).toHaveLength(events);
    });

    it('should reject a projection read when another store mutates its evaluated state before commit', () => {
      const world = createWorld();
      const deployment = createReadyDeployment(world.worldId);
      const provider = new ProjectionReadProvider('alpha', 'engine-a');
      const projectionCore = new SimulationCore(
        store,
        new ProviderRegistry([provider])
      );
      const concurrentStore = new SimulationStore(
        path.join(directory, 'simulation.sqlite')
      );
      const concurrentCore = new SimulationCore(concurrentStore, registry);
      provider.afterRead = () => {
        provider.afterRead = undefined;
        concurrentCore.executeCommand(
          world.worldId,
          commandInput(deployment.deploymentId, 'put', {
            resourceId: 'concurrent-resource',
            value: 'concurrent',
          }),
          'concurrent-projection-mutation'
        );
      };

      try {
        expect(
          captureCoreError(() =>
            projectionCore.executeCommand(
              world.worldId,
              commandInput(deployment.deploymentId),
              'stale-projection-read'
            )
          ).code
        ).toBe('Conflict');
        expect(
          concurrentCore
            .resources(world.worldId)
            .some((resource) => resource.resourceId === 'concurrent-resource')
        ).toBe(true);
        expect(
          store.idempotentResponse(
            `command:${world.worldId}:alpha`,
            'stale-projection-read'
          )
        ).toBeUndefined();
      } finally {
        concurrentStore.close();
      }
    });

    it('同じ provider と resource ID を使う Composite target 間で read write output を分離する', () => {
      const world = createWorld();
      const deployment = createReadyDeployment(world.worldId, {
        deploymentId: 'same-provider-composite',
        problemId: 'same-provider-problem',
        runtime: {
          kind: 'composite',
          targets: [
            {
              id: 'primary',
              provider: 'alpha',
              engine: 'engine-a',
              entry: 'primary.yaml',
            },
            {
              id: 'secondary',
              provider: 'alpha',
              engine: 'engine-a',
              entry: 'secondary.yaml',
            },
          ],
        },
        templateBody: 'resources: []',
      });
      const targetCommand = (
        targetId: string,
        operation: 'put' | 'delete',
        value?: string
      ): ExecuteCommandInput => ({
        ...commandInput(deployment.deploymentId, operation, {
          resourceId: 'shared-resource',
          ...(value === undefined ? {} : { value }),
        }),
        targetId,
      });

      core.executeCommand(
        world.worldId,
        targetCommand('primary', 'put', 'primary-value'),
        'primary-put'
      );
      core.executeCommand(
        world.worldId,
        targetCommand('secondary', 'put', 'secondary-value'),
        'secondary-put'
      );

      const sharedResources = core
        .resources(world.worldId)
        .filter((resource) => resource.resourceId === 'shared-resource');
      expect(sharedResources).toHaveLength(2);
      expect(
        sharedResources.map((resource) => ({
          targetId: resource.targetId,
          value: resource.properties['value'],
          observedResources: resource.properties['observedResources'],
        }))
      ).toEqual([
        {
          targetId: 'primary',
          value: 'primary-value',
          observedResources: 1,
        },
        {
          targetId: 'secondary',
          value: 'secondary-value',
          observedResources: 1,
        },
      ]);

      core.executeCommand(
        world.worldId,
        targetCommand('primary', 'delete'),
        'primary-delete'
      );

      expect(
        core
          .resources(world.worldId)
          .filter((resource) => resource.resourceId === 'shared-resource')
          .map(({ targetId, status }) => ({ targetId, status }))
      ).toEqual([
        { targetId: 'primary', status: 'deleted' },
        { targetId: 'secondary', status: 'ready' },
      ]);
      expect(
        core.deployment(world.worldId, deployment.deploymentId).outputs
      ).toMatchObject({
        primary: { lastMutation: 'delete' },
        secondary: { lastMutation: 'put' },
      });
    });

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

    it('should return the same-key provider command winner committed while an async reducer is pending', async () => {
      const world = createWorld();
      const deployment = createReadyDeployment(world.worldId);
      const gate = deferred();
      const provider = new AsyncProvider('alpha', 'engine-a');
      provider.gate = gate.promise;
      const evaluatingCore = new SimulationCore(
        store,
        new ProviderRegistry([provider])
      );
      const concurrentStore = new SimulationStore(
        path.join(directory, 'simulation.sqlite')
      );
      const concurrentCore = new SimulationCore(concurrentStore, registry);
      const input = commandInput(deployment.deploymentId, 'put', {
        resourceId: 'interleaved-command-resource',
        value: 'winner',
      });

      try {
        const pending = evaluatingCore.executeCommandAsync(
          world.worldId,
          input,
          'interleaved-command-key'
        );
        await Promise.resolve();
        expect(provider.calls).toBe(1);
        const winner = concurrentCore.executeCommand(
          world.worldId,
          input,
          'interleaved-command-key'
        );
        gate.resolve();

        expect(await pending).toEqual(winner);
        expect(
          evaluatingCore
            .events(world.worldId)
            .filter((event) => event.type === 'ProviderResourceMutated')
        ).toHaveLength(1);
      } finally {
        gate.resolve();
        concurrentStore.close();
      }
    });

    it('遅い async reducer を完了してから delete し late commit で world を復活させない', async () => {
      const world = createWorld();
      const deployment = createReadyDeployment(world.worldId);
      const gate = deferred();
      const provider = new AsyncProvider('alpha', 'engine-a');
      provider.gate = gate.promise;
      const asyncCore = new SimulationCore(
        store,
        new ProviderRegistry([provider])
      );
      const input = commandInput(deployment.deploymentId, 'put', {
        resourceId: 'late-resource',
        value: 'late-value',
      });

      const abandonedRequest = asyncCore.executeCommandAsync(
        world.worldId,
        input,
        'late-async-command'
      );
      await Promise.resolve();
      expect(provider.calls).toBe(1);
      const duplicate = asyncCore.executeCommandAsync(
        world.worldId,
        input,
        'late-async-command'
      );
      expect(provider.calls).toBe(1);
      let deletionSettled = false;
      const deletion = core.deleteWorld(world.worldId).then(() => {
        deletionSettled = true;
      });
      expect(
        (
          await captureCoreErrorAsync(() =>
            asyncCore.executeCommandAsync(
              world.worldId,
              { ...input, input: { value: 'blocked' } },
              'blocked-during-delete'
            )
          )
        ).code
      ).toBe('Conflict');
      await Promise.resolve();
      expect(deletionSettled).toBe(false);

      gate.resolve();
      await expect(abandonedRequest).resolves.toEqual({
        resourceId: 'late-resource',
        value: 'late-value',
      });
      await expect(duplicate).resolves.toEqual({
        resourceId: 'late-resource',
        value: 'late-value',
      });
      await deletion;
      await Promise.resolve();

      expect(provider.calls).toBe(1);
      expect(asyncCore.world(world.worldId).status).toBe('deleted');
      expect(
        asyncCore.deployment(world.worldId, deployment.deploymentId).status
      ).toBe('deleted');
      expect(
        asyncCore
          .resources(world.worldId)
          .every((resource) => resource.status === 'deleted')
      ).toBe(true);
      expect(asyncCore.events(world.worldId).at(-1)?.type).toBe('WorldDeleted');
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

    it('provider command result の resource provider が入力と違えば state を変更しない', () => {
      const world = createWorld();
      const provider = 'invalid-command';
      const engine = 'engine-c';
      const isolatedCore = new SimulationCore(
        store,
        new ProviderRegistry([
          new InvalidCommandResultProvider(provider, engine),
        ])
      );
      const deployment = isolatedCore.createDeployment(
        world.worldId,
        singleDeployment('invalid-command-deployment', provider, engine),
        'invalid-command-deployment-key'
      );
      const eventsBefore = isolatedCore.events(world.worldId);
      const resourcesBefore = store.resources(world.worldId);

      const error = captureCoreError(() =>
        isolatedCore.executeCommand(
          world.worldId,
          {
            ...commandInput(deployment.deploymentId),
            provider,
            engine,
          },
          'invalid-command-key'
        )
      );

      expect(error.code).toBe('ValidationFailed');
      expect(isolatedCore.events(world.worldId)).toEqual(eventsBefore);
      expect(store.resources(world.worldId)).toEqual(resourcesBefore);
    });

    it('deployment に属さない target identity を reducer 実行前に拒否する', () => {
      const world = createWorld();
      const deployment = createReadyDeployment(world.worldId);
      const eventsBefore = core.events(world.worldId);
      const resourcesBefore = store.resources(world.worldId);
      const invalidInputs: ExecuteCommandInput[] = [
        { ...commandInput(deployment.deploymentId), targetId: 'ghost' },
        { ...commandInput(deployment.deploymentId), provider: 'beta' },
        { ...commandInput(deployment.deploymentId), engine: 'engine-b' },
      ];

      for (const [index, input] of invalidInputs.entries()) {
        const error = captureCoreError(() =>
          core.executeCommand(world.worldId, input, `invalid-target-${index}`)
        );
        expect(error.code).toBe('ValidationFailed');
        expect(error.message).toContain('does not belong');
      }
      expect(core.events(world.worldId)).toEqual(eventsBefore);
      expect(store.resources(world.worldId)).toEqual(resourcesBefore);
      expect(
        core.deployment(world.worldId, deployment.deploymentId).outputs['ghost']
      ).toBeUndefined();
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

    it('command の key、world status、event quota、resource quota を検証する', async () => {
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

      await core.deleteWorld(world.worldId);
      expect(
        captureCoreError(() =>
          core.executeCommand(world.worldId, input, 'deleted-world-command')
        ).code
      ).toBe('Conflict');
    });

    it('should enforce resource quota against the final locked projection without double-counting updates', () => {
      const world = createWorld();
      const swapRegistry = new ProviderRegistry([
        new SwapResourceProvider('swap', 'engine-swap'),
      ]);
      const quotaCore = new SimulationCore(store, swapRegistry, {
        maxResourcesPerWorld: 1,
      });
      const deployment = quotaCore.createDeployment(
        world.worldId,
        singleDeployment('swap-deployment', 'swap', 'engine-swap'),
        'swap-deployment-key'
      );
      const original = requiredResource(
        quotaCore.resources(world.worldId),
        (resource) => resource.status === 'ready',
        'original quota resource is missing'
      );
      const input = {
        ...commandInput(deployment.deploymentId, 'put', {
          resourceId: original.resourceId,
          value: 'updated-in-place',
        }),
        provider: 'swap',
        engine: 'engine-swap',
      };

      expect(
        quotaCore.executeCommand(world.worldId, input, 'quota-update-existing')
      ).toMatchObject({ resourceId: original.resourceId });
      expect(
        quotaCore.executeCommand(
          world.worldId,
          {
            ...input,
            input: {
              resourceId: 'replacement-resource',
              deletedResourceId: original.resourceId,
              value: 'replacement',
            },
          },
          'quota-swap-resource'
        )
      ).toMatchObject({ resourceId: 'replacement-resource' });
      expect(
        quotaCore
          .resources(world.worldId)
          .filter((resource) => resource.status !== 'deleted')
          .map((resource) => resource.resourceId)
      ).toEqual(['replacement-resource']);
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

    it('should reject a clock result when another store changes the deployment target set during evaluation', () => {
      const world = createWorld({
        ...WORLD_INPUT,
        virtualTime: '2026-07-12T00:00:00.000Z',
      });
      createReadyDeployment(world.worldId);
      const concurrentStore = new SimulationStore(
        path.join(directory, 'simulation.sqlite')
      );
      const concurrentCore = new SimulationCore(concurrentStore, registry);
      let committedEvents: readonly EventRecord[] | undefined;
      const clockProvider = new ClockProvider('alpha', 'engine-a', () => {
        concurrentCore.createDeployment(
          world.worldId,
          singleDeployment(
            'deployment-created-during-clock',
            'beta',
            'engine-b'
          ),
          'deployment-created-during-clock-key'
        );
        committedEvents = concurrentCore.events(world.worldId);
        return {
          events: [],
          resources: [],
          deletedResourceRefs: [],
          appliedTransitionIds: [],
        };
      });
      const clockCore = new SimulationCore(
        store,
        new ProviderRegistry([clockProvider])
      );

      try {
        expect(
          captureCoreError(() => clockCore.advanceClock(world.worldId, 1_000))
            .code
        ).toBe('Conflict');
        if (!committedEvents) {
          throw new Error('concurrent clock deployment did not commit');
        }
        expect(clockCore.events(world.worldId)).toEqual(committedEvents);
        expect(
          clockCore
            .events(world.worldId)
            .filter((event) => event.type === 'ClockAdvanced')
        ).toHaveLength(0);
        expect(clockCore.world(world.worldId).virtualTime).toBe(
          world.virtualTime
        );
        expect(
          clockCore.deployment(world.worldId, 'deployment-created-during-clock')
            .status
        ).toBe('ready');
      } finally {
        concurrentStore.close();
      }
    });

    it('should allow only one store to commit from the same old clock', () => {
      const world = createWorld({
        ...WORLD_INPUT,
        virtualTime: '2026-07-12T00:00:00.000Z',
      });
      const concurrentStore = new SimulationStore(
        path.join(directory, 'simulation.sqlite')
      );
      const concurrentCore = new SimulationCore(concurrentStore, registry);
      let concurrentResult:
        | ReturnType<SimulationCore['advanceClock']>
        | undefined;
      const clockProvider = new ClockProvider('alpha', 'engine-a', () => {
        concurrentResult = concurrentCore.advanceClock(world.worldId, 500);
        return {
          events: [],
          resources: [],
          deletedResourceRefs: [],
          appliedTransitionIds: [],
        };
      });
      const clockCore = new SimulationCore(
        store,
        new ProviderRegistry([clockProvider])
      );

      try {
        expect(
          captureCoreError(() => clockCore.advanceClock(world.worldId, 1_000))
            .code
        ).toBe('Conflict');
        if (!concurrentResult) {
          throw new Error('concurrent clock did not commit');
        }
        expect(clockCore.world(world.worldId).virtualTime).toBe(
          concurrentResult.virtualTime
        );
        expect(
          clockCore
            .events(world.worldId)
            .filter((event) => event.type === 'ClockAdvanced')
        ).toHaveLength(1);
      } finally {
        concurrentStore.close();
      }
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
            deletedResourceRefs: [],
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
            deletedResourceRefs: [],
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
            deletedResourceRefs: [
              {
                deploymentId: original.deploymentId,
                targetId: original.targetId,
                resourceId: original.resourceId,
              },
            ],
            appliedTransitionIds: ['transition-delete'],
          })),
        ])
      );

      clockCore.advanceClock(world.worldId, 1);

      expect(clockCore.resources(world.worldId)[0]?.status).toBe('deleted');
    });

    it('同じ provider と resource ID の Composite target は指定した target だけ clock 削除する', () => {
      const world = createWorld();
      const deployment = createReadyDeployment(world.worldId, {
        deploymentId: 'clock-composite',
        problemId: 'clock-composite-problem',
        runtime: {
          kind: 'composite',
          targets: [
            {
              id: 'primary',
              provider: 'alpha',
              engine: 'engine-a',
              entry: 'primary.yaml',
            },
            {
              id: 'secondary',
              provider: 'alpha',
              engine: 'engine-a',
              entry: 'secondary.yaml',
            },
          ],
        },
        templateBody: 'resources: []',
      });
      for (const targetId of ['primary', 'secondary']) {
        store.saveResource({
          worldId: world.worldId,
          deploymentId: deployment.deploymentId,
          targetId,
          provider: 'alpha',
          resourceType: 'Object',
          resourceId: 'shared-clock-resource',
          properties: { targetId },
          status: 'ready',
        });
      }
      const clockCore = new SimulationCore(
        store,
        new ProviderRegistry([
          new ClockProvider('alpha', 'engine-a', () => ({
            events: [],
            resources: [],
            deletedResourceRefs: [
              {
                deploymentId: deployment.deploymentId,
                targetId: 'primary',
                resourceId: 'shared-clock-resource',
              },
            ],
            appliedTransitionIds: ['transition-target-delete'],
          })),
        ])
      );

      clockCore.advanceClock(world.worldId, 1);

      expect(
        clockCore
          .resources(world.worldId)
          .filter((resource) => resource.resourceId === 'shared-clock-resource')
          .map(({ targetId, status }) => ({ targetId, status }))
      ).toEqual([
        { targetId: 'primary', status: 'deleted' },
        { targetId: 'secondary', status: 'ready' },
      ]);
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
        deletedResourceRefs: [],
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
          deletedResourceRefs: [],
          appliedTransitionIds: [],
        },
        {
          events: [],
          resources: [{ ...original, provider: 'beta' }],
          deletedResourceRefs: [],
          appliedTransitionIds: [],
        },
        {
          events: [],
          resources: [original, original],
          deletedResourceRefs: [],
          appliedTransitionIds: [],
        },
        {
          events: [],
          resources: [],
          deletedResourceRefs: [
            {
              deploymentId: original.deploymentId,
              targetId: original.targetId,
              resourceId: 'missing-resource',
            },
          ],
          appliedTransitionIds: [],
        },
        {
          events: [],
          resources: [],
          deletedResourceRefs: [
            {
              deploymentId: 'missing-deployment',
              targetId: original.targetId,
              resourceId: original.resourceId,
            },
          ],
          appliedTransitionIds: [],
        },
        {
          events: [],
          resources: [],
          deletedResourceRefs: [
            {
              deploymentId: original.deploymentId,
              targetId: 'missing-target',
              resourceId: original.resourceId,
            },
          ],
          appliedTransitionIds: [],
        },
        {
          events: [],
          resources: [],
          deletedResourceRefs: [
            {
              deploymentId: original.deploymentId,
              targetId: original.targetId,
              resourceId: original.resourceId,
            },
            {
              deploymentId: original.deploymentId,
              targetId: original.targetId,
              resourceId: original.resourceId,
            },
          ],
          appliedTransitionIds: [],
        },
        {
          events: [],
          resources: [original],
          deletedResourceRefs: [
            {
              deploymentId: original.deploymentId,
              targetId: original.targetId,
              resourceId: original.resourceId,
            },
          ],
          appliedTransitionIds: [],
        },
        {
          events: [],
          resources: [],
          deletedResourceRefs: [],
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

    it('world delete は resource と deployment を論理削除し再送を idempotent にする', async () => {
      const world = createWorld();
      createReadyDeployment(world.worldId);

      await core.deleteWorld(world.worldId);
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

      await core.deleteWorld(world.worldId);
      expect(core.events(world.worldId)).toHaveLength(eventCount);
    });

    it('削除済み world の clock は進めない', async () => {
      const world = createWorld();
      await core.deleteWorld(world.worldId);

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
    it('snapshot を hash 検証して新しい world へ復元し、同じ key の再送を idempotent にする', async () => {
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
      const restored = await core.restoreSnapshot(snapshot, 'restore-key');
      const repeated = await core.restoreSnapshot(snapshot, 'restore-key');

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
      expect(
        core.restoredWorld(source.worldId, snapshot.hash, 'restore-key', source)
      ).toEqual(restored);
      expect(
        captureCoreError(() =>
          core.restoredWorld(
            source.worldId,
            snapshot.hash,
            'missing-restore-key',
            source
          )
        ).code
      ).toBe('NotFound');
      expect(
        captureCoreError(() =>
          core.restoredWorld(
            source.worldId,
            'f'.repeat(64),
            'restore-key',
            source
          )
        ).code
      ).toBe('NotFound');
      for (const invalidHash of [
        '',
        'A'.repeat(64),
        'a'.repeat(65),
        'x'.repeat(10_000),
      ]) {
        expect(
          captureCoreError(() =>
            core.restoredWorld(
              source.worldId,
              invalidHash,
              'restore-key',
              source
            )
          ).code
        ).toBe('NotFound');
      }

      const another = await core.restoreSnapshot(snapshot, 'restore-key-two');
      expect(another.worldId).not.toBe(restored.worldId);
      expect(
        core.restoredWorld(
          source.worldId,
          snapshot.hash,
          'restore-key-two',
          source
        )
      ).toEqual(another);
    });

    it('hash 改ざん、未知 version、空 key を Snapshot/Validation error にする', async () => {
      const world = createWorld();
      const snapshot = core.exportSnapshot(world.worldId);
      const changedHash: WorldSnapshot = { ...snapshot, hash: 'changed' };
      expect(
        (
          await captureCoreErrorAsync(() =>
            core.restoreSnapshot(changedHash, 'changed-hash')
          )
        ).code
      ).toBe('SnapshotIncompatible');

      const changedVersion = structuredClone(snapshot);
      Reflect.set(changedVersion.payload, 'snapshotVersion', '2');
      expect(
        (
          await captureCoreErrorAsync(() =>
            core.restoreSnapshot(changedVersion, 'changed-version')
          )
        ).code
      ).toBe('SnapshotIncompatible');
      expect(
        (
          await captureCoreErrorAsync(() =>
            core.restoreSnapshot(snapshot, '   ')
          )
        ).code
      ).toBe('ValidationFailed');
    });

    it('再計算済み hash を持つ forged target resource を永続化前に拒否する', async () => {
      const world = createWorld();
      createReadyDeployment(world.worldId);
      const snapshot = core.exportSnapshot(world.worldId);
      const resource = snapshot.payload.resources.at(0);
      if (!resource) throw new Error('snapshot resource がありません');
      const forgedPayload: SnapshotPayload = {
        ...snapshot.payload,
        resources: [{ ...resource, targetId: 'forged-target' }],
      };
      const forged: WorldSnapshot = {
        payload: forgedPayload,
        hash: contentHash(forgedPayload),
      };
      const countsBefore = store.database
        .query<{ worlds: number; idempotency: number }, []>(
          `SELECT
             (SELECT COUNT(*) FROM worlds) AS worlds,
             (SELECT COUNT(*) FROM idempotency) AS idempotency`
        )
        .get();

      const error = await captureCoreErrorAsync(() =>
        core.restoreSnapshot(forged, 'forged-target-restore')
      );

      expect(error.code).toBe('SnapshotIncompatible');
      expect(
        store.database
          .query<{ worlds: number; idempotency: number }, []>(
            `SELECT
               (SELECT COUNT(*) FROM worlds) AS worlds,
               (SELECT COUNT(*) FROM idempotency) AS idempotency`
          )
          .get()
      ).toEqual(countsBefore);
    });

    it('再計算済み hash でも接続 token を持つ resource を一件も永続化しない', async () => {
      const world = createWorld();
      createReadyDeployment(world.worldId);
      const snapshot = core.exportSnapshot(world.worldId);
      const resource = snapshot.payload.resources.at(0);
      if (!resource) throw new Error('snapshot resource がありません');
      const forgedPayload: SnapshotPayload = {
        ...snapshot.payload,
        resources: snapshot.payload.resources.map((candidate) =>
          candidate === resource
            ? {
                ...candidate,
                properties: {
                  ...candidate.properties,
                  state: { tokenValue: 'forged-connection-token' },
                },
              }
            : candidate
        ),
      };
      const forged: WorldSnapshot = {
        payload: forgedPayload,
        hash: contentHash(forgedPayload),
      };
      const countsBefore = store.database
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

      const error = await captureCoreErrorAsync(() =>
        core.restoreSnapshot(forged, 'forged-token-restore')
      );

      expect(error.code).toBe('SnapshotIncompatible');
      expect(error.message).toContain('unredacted connection credential');
      expect(
        store.database
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
          .get()
      ).toEqual(countsBefore);
    });

    it('再計算済み hash でも forged workload endpoint を信頼済み projection として復元しない', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'workload-snapshot-deployment'
      );
      await core.materializeWorkloads(world.worldId, deployment.deploymentId);
      const snapshot = core.exportSnapshot(world.worldId);
      const workload = requiredResource(
        snapshot.payload.resources,
        (resource) => resource.resourceType === 'Runtime::Workload',
        'workload resource がありません'
      );
      const oldUrl = materializedUrl(
        workload,
        'workload endpoint URL がありません'
      );
      const endpointResource = requiredResource(
        snapshot.payload.resources,
        (resource) => resource.provider !== 'runtime',
        'provider resource がありません'
      );
      const portablePayload = forgedPortablePayload(
        snapshot,
        workload,
        endpointResource,
        oldUrl
      );
      const forgedPayload: SnapshotPayload = {
        ...portablePayload,
        events: portablePayload.events.map((event, index) => {
          if (index !== 0) return event;
          const payload = {
            ...event.payload,
            endpointHistory: [oldUrl, { nestedEndpoint: oldUrl }],
          };
          return { ...event, payload, payloadHash: contentHash(payload) };
        }),
      };
      const restored = await core.restoreSnapshot(
        { payload: forgedPayload, hash: contentHash(forgedPayload) },
        'forged-workload-restore'
      );
      const restoredResources = core.resources(restored.worldId);
      const restoredWorkload = requiredResource(
        restoredResources,
        (resource) => resource.resourceType === 'Runtime::Workload',
        'restored workload endpoint がありません'
      );
      const restoredEndpoint = requiredResource(
        restoredResources,
        (resource) => resource.resourceId === endpointResource.resourceId,
        'restored provider resource がありません'
      );
      const restoredUrl = materializedUrl(
        restoredWorkload,
        'restored workload endpoint URL がありません'
      );
      expect(restoredUrl).toMatch(/^http:\/\/127\.0\.0\.1:/);
      expect(restoredUrl).not.toBe(oldUrl);
      expect(restoredUrl).not.toBe('http://127.0.0.1:1');
      expect(restoredEndpoint?.properties['ManagedPlacement']).toBeUndefined();
      expect(restoredEndpoint?.properties['state']).toEqual({
        preserved: true,
      });
      const restoredDeployment = core.deployment(
        restored.worldId,
        deployment.deploymentId
      );
      expect(
        restoredDeployment.outputs['default']?.['Workload.api.Endpoint']
      ).toBe(restoredUrl);
      const activeProjection = JSON.stringify({
        deployments: store.deployments(restored.worldId),
        resources: restoredResources,
      });
      const restoredEvents = JSON.stringify(core.events(restored.worldId));
      expect(activeProjection).not.toContain(oldUrl);
      expect(activeProjection).not.toContain('http://127.0.0.1:1');
      expect(restoredEvents).not.toContain(oldUrl);
      expect(restoredEvents).toContain('<portable-endpoint-removed>');
      expect(effects.received).toHaveLength(2);
    });

    it('active snapshot の deleted workload forge を永続化前に拒否する', async () => {
      enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'deleted-workload-forge-source'
      );
      await core.materializeWorkloads(world.worldId, deployment.deploymentId);
      const snapshot = core.exportSnapshot(world.worldId);
      const forgedPayload: SnapshotPayload = {
        ...snapshot.payload,
        resources: snapshot.payload.resources.map((resource) =>
          resource.resourceType === 'Runtime::Workload'
            ? { ...resource, status: 'deleted' }
            : resource
        ),
      };
      const countsBefore = store.database
        .query<{ worlds: number; idempotency: number }, []>(
          `SELECT
             (SELECT COUNT(*) FROM worlds) AS worlds,
             (SELECT COUNT(*) FROM idempotency) AS idempotency`
        )
        .get();

      expect(
        (
          await captureCoreErrorAsync(() =>
            core.restoreSnapshot(
              {
                payload: forgedPayload,
                hash: contentHash(forgedPayload),
              },
              'deleted-workload-forge'
            )
          )
        ).code
      ).toBe('SnapshotIncompatible');
      expect(
        store.database
          .query<{ worlds: number; idempotency: number }, []>(
            `SELECT
               (SELECT COUNT(*) FROM worlds) AS worlds,
               (SELECT COUNT(*) FROM idempotency) AS idempotency`
          )
          .get()
      ).toEqual(countsBefore);
    });

    it('deleted source restore から全 workload endpoint と旧 loopback URL を除去する', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'deleted-workload-source'
      );
      await core.materializeWorkloads(world.worldId, deployment.deploymentId);
      const materialized = requiredResource(
        core.resources(world.worldId),
        (resource) => resource.resourceType === 'Runtime::Workload',
        'materialized workload がありません'
      );
      const oldUrl = materializedUrl(
        materialized,
        'materialized workload URL がありません'
      );
      await core.deleteWorld(world.worldId);
      const snapshot = core.exportSnapshot(world.worldId);
      expect(JSON.stringify(snapshot.payload)).toContain(oldUrl);
      const forgedPayload: SnapshotPayload = {
        ...snapshot.payload,
        deployments: snapshot.payload.deployments.map((item) => ({
          ...item,
          outputs: {
            ...item.outputs,
            default: {
              ...item.outputs['default'],
              'Workload.ghost.Endpoint': oldUrl,
            },
          },
        })),
      };

      const restored = await core.restoreSnapshot(
        { payload: forgedPayload, hash: contentHash(forgedPayload) },
        'deleted-portable-restore'
      );
      const restoredPayload = core.exportSnapshot(restored.worldId).payload;

      expect(restored.status).toBe('deleted');
      expect(JSON.stringify(restoredPayload)).not.toContain(oldUrl);
      expect(
        restoredPayload.deployments
          .flatMap((item) => Object.values(item.outputs))
          .flatMap((output) => Object.keys(output))
          .some(
            (key) => key.startsWith('Workload.') && key.endsWith('.Endpoint')
          )
      ).toBe(false);
      expect(effects.received).toHaveLength(1);
      expect(effects.cleanupWorlds).toEqual([world.worldId]);
    });

    it('workload restore の外部失敗を同じ new world へ idempotent に再試行して cleanup できる', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'workload-snapshot-deployment'
      );
      await core.materializeWorkloads(world.worldId, deployment.deploymentId);
      const snapshot = core.exportSnapshot(world.worldId);
      effects.faults.push('throw');

      const first = await captureCoreErrorAsync(() =>
        core.restoreSnapshot(snapshot, 'workload-restore')
      );
      expect(first.code).toBe('WorkloadEffectFailed');
      const committed = core.restoredWorld(
        world.worldId,
        snapshot.hash,
        'workload-restore',
        world
      );
      expect(
        core.deployment(committed.worldId, deployment.deploymentId).status
      ).toBe('failed');

      const restored = await core.restoreSnapshot(snapshot, 'workload-restore');
      expect(restored.worldId).toBe(committed.worldId);
      expect(
        core.deployment(restored.worldId, deployment.deploymentId).status
      ).toBe('ready');
      expect(effects.received).toHaveLength(3);

      await core.deleteWorld(restored.worldId);
      expect(
        core.restoredWorld(
          world.worldId,
          snapshot.hash,
          'workload-restore',
          world
        ).status
      ).toBe('deleted');
      await core.deleteWorld(restored.worldId);
      await core.deleteWorld(world.worldId);
      expect(effects.cleanupWorlds).toEqual([
        restored.worldId,
        restored.worldId,
        world.worldId,
      ]);
    });

    it('同じ snapshot restore の並行 replay は workload effect を single-flight にする', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'concurrent-restore-source'
      );
      await core.materializeWorkloads(world.worldId, deployment.deploymentId);
      const snapshot = core.exportSnapshot(world.worldId);
      const gate = deferred();
      effects.gates.push(gate.promise);

      const first = core.restoreSnapshot(snapshot, 'concurrent-restore');
      const committed = core.restoredWorld(
        world.worldId,
        snapshot.hash,
        'concurrent-restore',
        world
      );
      const replay = core.restoreSnapshot(snapshot, 'concurrent-restore');
      await Promise.resolve();
      expect(effects.received).toHaveLength(2);
      gate.resolve();
      const [firstResult, replayResult] = await Promise.all([first, replay]);

      expect(replayResult).toEqual(firstResult);
      expect(firstResult.worldId).toBe(committed.worldId);
      expect(effects.received).toHaveLength(2);
      expect(
        core
          .events(firstResult.worldId)
          .filter((event) => event.type === 'WorkloadMaterialized')
      ).toHaveLength(
        snapshot.payload.events.filter(
          (event) => event.type === 'WorkloadMaterialized'
        ).length + 1
      );
      expect(
        core
          .events(firstResult.worldId)
          .filter((event) => event.type === 'DeploymentReady')
      ).toHaveLength(
        snapshot.payload.events.filter(
          (event) => event.type === 'DeploymentReady'
        ).length + 1
      );
    });

    it('restore quota の予約内で failure event 後の retry success を完了する', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'quota-retry-source'
      );
      await core.materializeWorkloads(world.worldId, deployment.deploymentId);
      const snapshot = core.exportSnapshot(world.worldId);
      const exactQuota = snapshot.payload.events.length + 4;
      const limited = new SimulationCore(store, registry, {
        maxEventsPerWorld: exactQuota,
        workloadEffects: effects,
      });
      effects.faults.push('throw');

      expect(
        (
          await captureCoreErrorAsync(() =>
            limited.restoreSnapshot(snapshot, 'quota-retry-restore')
          )
        ).code
      ).toBe('WorkloadEffectFailed');
      expect(store.reservedEventCount(world.worldId)).toBe(0);
      const restored = await limited.restoreSnapshot(
        snapshot,
        'quota-retry-restore'
      );

      expect(
        limited.deployment(restored.worldId, deployment.deploymentId).status
      ).toBe('ready');
      expect(limited.events(restored.worldId)).toHaveLength(exactQuota);
      expect(
        limited
          .events(restored.worldId)
          .map((event) => event.type)
          .slice(-3)
      ).toEqual([
        'WorkloadMaterializationFailed',
        'WorkloadMaterialized',
        'DeploymentReady',
      ]);
    });

    it('複数 deployment の部分成功を保持して failed workload だけ再試行する', async () => {
      const effects = enableWorkloadEffects();
      const world = createWorld();
      const firstInput = workloadDeployment('workload-a');
      const secondBase = workloadDeployment('workload-b');
      const secondInput: DeploymentInput = {
        ...secondBase,
        simulationOverlay: {
          schemaVersion: '1',
          workloads: [
            {
              id: 'worker',
              targetId: 'default',
              resourceRef: 'WorkerFunction',
              image: WORKLOAD_IMAGE,
              command: ['bun', 'run', 'worker'],
              containerPort: 3001,
              healthPath: '/healthz',
            },
          ],
        },
      };
      const first = core.createDeployment(
        world.worldId,
        firstInput,
        'workload-a-create'
      );
      const second = core.createDeployment(
        world.worldId,
        secondInput,
        'workload-b-create'
      );
      await core.materializeWorkloads(world.worldId, first.deploymentId);
      await core.materializeWorkloads(world.worldId, second.deploymentId);
      const snapshot = core.exportSnapshot(world.worldId);
      effects.faults.push(undefined, 'throw');

      expect(
        (
          await captureCoreErrorAsync(() =>
            core.restoreSnapshot(snapshot, 'partial-workload-restore')
          )
        ).code
      ).toBe('WorkloadEffectFailed');
      const committed = core.restoredWorld(
        world.worldId,
        snapshot.hash,
        'partial-workload-restore',
        world
      );
      expect(
        core.deployment(committed.worldId, first.deploymentId).status
      ).toBe('ready');
      expect(
        core.deployment(committed.worldId, second.deploymentId).status
      ).toBe('failed');
      expect(effects.received).toHaveLength(4);

      await core.restoreSnapshot(snapshot, 'partial-workload-restore');
      expect(
        core.deployment(committed.worldId, first.deploymentId).status
      ).toBe('ready');
      expect(
        core.deployment(committed.worldId, second.deploymentId).status
      ).toBe('ready');
      expect(effects.received).toHaveLength(5);
    });

    it('workload effect がない portable restore は transaction 前に拒否する', async () => {
      enableWorkloadEffects();
      const world = createWorld();
      const deployment = core.createDeployment(
        world.worldId,
        workloadDeployment(),
        'workload-no-effect-source'
      );
      await core.materializeWorkloads(world.worldId, deployment.deploymentId);
      const snapshot = core.exportSnapshot(world.worldId);
      const countsBefore = store.database
        .query<{ worlds: number; idempotency: number }, []>(
          `SELECT
             (SELECT COUNT(*) FROM worlds) AS worlds,
             (SELECT COUNT(*) FROM idempotency) AS idempotency`
        )
        .get();
      const unavailable = new SimulationCore(store, registry);

      expect(
        (
          await captureCoreErrorAsync(() =>
            unavailable.restoreSnapshot(snapshot, 'workload-no-effect')
          )
        ).code
      ).toBe('SnapshotIncompatible');
      expect(
        store.database
          .query<{ worlds: number; idempotency: number }, []>(
            `SELECT
               (SELECT COUNT(*) FROM worlds) AS worlds,
               (SELECT COUNT(*) FROM idempotency) AS idempotency`
          )
          .get()
      ).toEqual(countsBefore);
    });

    it('閉じていない deployment target resource graph を import 前に拒否する', async () => {
      const world = createWorld();
      createReadyDeployment(world.worldId);
      const snapshot = core.exportSnapshot(world.worldId);
      const deployment = snapshot.payload.deployments.at(0);
      const resource = snapshot.payload.resources.at(0);
      const event = snapshot.payload.events.at(0);
      if (!(deployment && resource && event)) {
        throw new Error('snapshot graph がありません');
      }
      const target = deployment.targets.at(0);
      if (!target) throw new Error('snapshot target がありません');
      const invalidPayloads: readonly SnapshotPayload[] = [
        {
          ...snapshot.payload,
          events: [{ ...event, worldId: 'foreign-world' }],
        },
        {
          ...snapshot.payload,
          deployments: [{ ...deployment, worldId: 'foreign-world' }],
        },
        {
          ...snapshot.payload,
          deployments: [deployment, deployment],
        },
        {
          ...snapshot.payload,
          deployments: [{ ...deployment, targets: [] }],
        },
        {
          ...snapshot.payload,
          deployments: [{ ...deployment, targets: [{ ...target, id: ' ' }] }],
        },
        {
          ...snapshot.payload,
          deployments: [{ ...deployment, targets: [target, { ...target }] }],
        },
        {
          ...snapshot.payload,
          resources: [{ ...resource, worldId: 'foreign-world' }],
        },
        {
          ...snapshot.payload,
          resources: [{ ...resource, deploymentId: 'missing-deployment' }],
        },
        {
          ...snapshot.payload,
          resources: [{ ...resource, provider: 'beta' }],
        },
        {
          ...snapshot.payload,
          resources: [resource, resource],
        },
      ];

      for (const [index, payload] of invalidPayloads.entries()) {
        const error = await captureCoreErrorAsync(() =>
          core.restoreSnapshot(
            { payload, hash: contentHash(payload) },
            `invalid-graph-${index}`
          )
        );
        expect(error.code).toBe('SnapshotIncompatible');
      }
    });

    it('snapshot event quota と resource quota を import 前に検証する', async () => {
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
        (
          await captureCoreErrorAsync(() =>
            eventLimited.restoreSnapshot(snapshot, 'event-limited-restore')
          )
        ).code
      ).toBe('QuotaExceeded');
      expect(
        (
          await captureCoreErrorAsync(() =>
            resourceLimited.restoreSnapshot(
              snapshot,
              'resource-limited-restore'
            )
          )
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
