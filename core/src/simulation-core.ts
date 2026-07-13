import { resolveTargetSources } from './artifact-bundle';
import { contentHash, deterministicId } from './canonical';
import type {
  AppliedTransition,
  CapabilityDiagnostic,
  CapabilityRequirement,
  ClockAdvanceResult,
  CreateWorldInput,
  DeploymentInput,
  DeploymentRecord,
  DeploymentTargetIdentity,
  EventRecord,
  ExecuteCommandInput,
  MaterializedWorkload,
  ProviderClockResult,
  ProviderCommandInput,
  ProviderCommandResult,
  ProviderDeploymentResult,
  ProviderEvent,
  ProviderModule,
  ProviderTargetPlan,
  ProviderWorldView,
  ResourceDeclaration,
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
import type { ProviderRegistry } from './provider-registry';
import { resolveSimulationOverlay } from './simulation-overlay';
import type { SimulationStore } from './store';

interface SimulationCoreOptions {
  readonly maxEventsPerWorld?: number;
  readonly maxResourcesPerWorld?: number;
  readonly workloadEffects?: WorkloadEffectPort;
}

interface CompiledDeployment {
  readonly targets: readonly DeploymentTargetIdentity[];
  readonly plans: readonly ProviderTargetPlan[];
  readonly diagnostics: readonly CapabilityDiagnostic[];
  readonly workloads: readonly WorkloadDeclaration[];
}

interface TargetResult {
  readonly plan: ProviderTargetPlan;
  readonly result: ProviderDeploymentResult;
}

interface TargetResourceDeclaration extends ResourceDeclaration {
  readonly targetId: string;
}

interface ProviderClockEvaluation {
  readonly module: ProviderModule;
  readonly events: readonly ProviderEvent[];
  readonly resources: readonly ResourceRecord[];
  readonly deletedResourceRefs: ProviderClockResult['deletedResourceRefs'];
  readonly resolvedDeletedResources: readonly ResourceRecord[];
  readonly appliedTransitionIds: readonly string[];
}

interface PreparedProviderCommand {
  readonly kind: 'prepared';
  readonly scope: string;
  readonly module: ProviderModule;
  readonly command: ProviderCommandInput;
  readonly view: ProviderWorldView;
}

interface ExistingProviderCommand {
  readonly kind: 'existing';
  readonly response: Readonly<Record<string, unknown>>;
}

type ProviderCommandPreparation =
  | ExistingProviderCommand
  | PreparedProviderCommand;

const INITIAL_VIRTUAL_TIME = '1970-01-01T00:00:00.000Z';
const WORKLOAD_PROVIDER = 'runtime';
const WORKLOAD_RESOURCE_TYPE = 'Runtime::Workload';
const WORKLOAD_OPERATION = 'Materialize';
const LOOPBACK_HOSTS = new Set(['127.0.0.1', 'localhost', '::1']);

function requireText(value: string, label: string): void {
  if (!value.trim()) {
    throw new CoreError('ValidationFailed', `${label} must not be empty`);
  }
}

function validateWorldInput(input: CreateWorldInput): void {
  requireText(input.tenantId, 'tenantId');
  requireText(input.eventId, 'eventId');
  requireText(input.teamId, 'teamId');
  requireText(input.deploymentId, 'deploymentId');
  if (input.virtualTime && !Number.isFinite(Date.parse(input.virtualTime))) {
    throw new CoreError('ValidationFailed', 'virtualTime must be an ISO date');
  }
}

function runtimeTargets(
  runtime: DeploymentInput['runtime']
): readonly SingleRuntimeTarget[] {
  if ('kind' in runtime) {
    if (runtime.targets.length < 2 || runtime.targets.length > 8) {
      throw new CoreError(
        'ValidationFailed',
        'composite runtime must contain between 2 and 8 targets'
      );
    }
    const ids = new Set<string>();
    for (const target of runtime.targets) {
      requireText(target.id ?? '', 'composite target id');
      if (ids.has(target.id ?? '')) {
        throw new CoreError(
          'ValidationFailed',
          'composite target ids must be unique'
        );
      }
      ids.add(target.id ?? '');
    }
    return runtime.targets;
  }
  return [runtime];
}

function targetId(target: SingleRuntimeTarget, index: number): string {
  return target.id ?? (index === 0 ? 'default' : `target-${index + 1}`);
}

function bootstrapRequirement(
  target: SingleRuntimeTarget
): CapabilityRequirement {
  return {
    provider: target.provider,
    engine: target.engine,
    service: target.engine,
    resourceType: '*',
    operation: 'deploy',
    fidelity: ['L0'],
    source: { path: target.entry },
  };
}

function mergeResources(
  plan: ProviderTargetPlan,
  result: ProviderDeploymentResult
): readonly TargetResourceDeclaration[] {
  const resources = new Map<string, ResourceDeclaration>();
  for (const resource of [...plan.resources, ...result.resources]) {
    resources.set(`${resource.provider}:${resource.resourceId}`, resource);
  }
  return Array.from(resources.values())
    .sort((left, right) =>
      `${left.provider}:${left.resourceId}`.localeCompare(
        `${right.provider}:${right.resourceId}`
      )
    )
    .map((resource) => ({ ...resource, targetId: plan.targetId }));
}

function namespaceOf(world: WorldRecord): WorldNamespace {
  return {
    tenantId: world.tenantId,
    eventId: world.eventId,
    teamId: world.teamId,
  };
}

function sameNamespace(left: WorldNamespace, right: WorldNamespace): boolean {
  return (
    left.tenantId === right.tenantId &&
    left.eventId === right.eventId &&
    left.teamId === right.teamId
  );
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function isWorkloadRequirement(requirement: CapabilityRequirement): boolean {
  return (
    requirement.service === 'runtime' &&
    requirement.resourceType === WORKLOAD_RESOURCE_TYPE &&
    requirement.operation === WORKLOAD_OPERATION
  );
}

function unavailableWorkloadDiagnostic(
  requirement: CapabilityRequirement
): CapabilityDiagnostic {
  return {
    ...requirement,
    code: 'MissingCapability',
    availableFidelity: [],
  };
}

function workloadResourceId(declaration: WorkloadDeclaration): string {
  return deterministicId('workload', {
    targetId: declaration.targetId,
    workloadId: declaration.id,
  });
}

function workloadResource(
  worldId: string,
  deploymentId: string,
  declaration: WorkloadDeclaration
): ResourceRecord {
  return {
    worldId,
    deploymentId,
    targetId: declaration.targetId,
    provider: WORKLOAD_PROVIDER,
    resourceType: WORKLOAD_RESOURCE_TYPE,
    resourceId: workloadResourceId(declaration),
    properties: { declaration },
    status: 'pending',
  };
}

function storedWorkloadDeclaration(
  resource: ResourceRecord
): WorkloadDeclaration {
  const value = resource.properties['declaration'];
  if (!isRecord(value)) {
    throw new CoreError(
      'ValidationFailed',
      `workload resource ${resource.resourceId} has no declaration`
    );
  }
  const allowed = new Set([
    'id',
    'targetId',
    'resourceRef',
    'image',
    'command',
    'containerPort',
    'healthPath',
  ]);
  if (Object.keys(value).some((key) => !allowed.has(key))) {
    throw new CoreError(
      'ValidationFailed',
      `workload resource ${resource.resourceId} has an unknown declaration field`
    );
  }
  const command = value['command'];
  const containerPort = value['containerPort'];
  if (
    typeof value['id'] !== 'string' ||
    typeof value['targetId'] !== 'string' ||
    typeof value['resourceRef'] !== 'string' ||
    typeof value['image'] !== 'string' ||
    typeof containerPort !== 'number' ||
    !Number.isSafeInteger(containerPort) ||
    (command !== undefined &&
      (!Array.isArray(command) ||
        command.some((argument) => typeof argument !== 'string'))) ||
    (value['healthPath'] !== undefined &&
      typeof value['healthPath'] !== 'string')
  ) {
    throw new CoreError(
      'ValidationFailed',
      `workload resource ${resource.resourceId} has an invalid declaration`
    );
  }
  return {
    id: value['id'],
    targetId: value['targetId'],
    resourceRef: value['resourceRef'],
    image: value['image'],
    ...(command === undefined ? {} : { command }),
    containerPort,
    ...(value['healthPath'] === undefined
      ? {}
      : { healthPath: value['healthPath'] }),
  };
}

function loopbackEndpoint(value: unknown): string {
  if (typeof value !== 'string') {
    throw new CoreError(
      'WorkloadEffectFailed',
      'workload effect returned an invalid materialization result'
    );
  }
  let endpoint: URL;
  try {
    endpoint = new URL(value);
  } catch {
    throw new CoreError(
      'WorkloadEffectFailed',
      'workload effect returned an invalid materialization result'
    );
  }
  if (
    endpoint.protocol !== 'http:' ||
    !LOOPBACK_HOSTS.has(endpoint.hostname.replace(/^\[|\]$/g, '')) ||
    !endpoint.port ||
    endpoint.username ||
    endpoint.password ||
    endpoint.pathname !== '/' ||
    endpoint.search ||
    endpoint.hash ||
    endpoint.origin !== value.replace(/\/$/, '')
  ) {
    throw new CoreError(
      'WorkloadEffectFailed',
      'workload effect returned an invalid materialization result'
    );
  }
  return endpoint.origin;
}

function materializedEndpoints(
  worldId: string,
  declarations: readonly WorkloadDeclaration[],
  results: readonly MaterializedWorkload[]
): ReadonlyMap<string, string> {
  if (results.length !== declarations.length) {
    throw new CoreError(
      'WorkloadEffectFailed',
      'workload effect returned an invalid materialization result'
    );
  }
  const expected = new Map(
    declarations.map((declaration) => [
      `${declaration.targetId}\u0000${declaration.id}`,
      declaration,
    ])
  );
  const endpoints = new Map<string, string>();
  for (const result of results) {
    const identity = `${result.targetId}\u0000${result.workloadId}`;
    const declaration = expected.get(identity);
    if (
      !declaration ||
      endpoints.has(workloadResourceId(declaration)) ||
      result.worldId !== worldId ||
      result.targetId !== declaration.targetId ||
      result.workloadId !== declaration.id ||
      result.resourceRef !== declaration.resourceRef ||
      result.image !== declaration.image ||
      result.healthPath !== (declaration.healthPath ?? '/')
    ) {
      throw new CoreError(
        'WorkloadEffectFailed',
        'workload effect returned an invalid materialization result'
      );
    }
    endpoints.set(
      workloadResourceId(declaration),
      loopbackEndpoint(result.endpoint)
    );
  }
  if (endpoints.size !== expected.size) {
    throw new CoreError(
      'WorkloadEffectFailed',
      'workload effect returned an invalid materialization result'
    );
  }
  return endpoints;
}

function validateClockEvents(
  provider: string,
  events: readonly ProviderEvent[]
): void {
  for (const event of events) {
    requireText(event.type, 'provider clock event type');
    if (!isRecord(event.payload)) {
      throw new CoreError(
        'ValidationFailed',
        `provider ${provider} returned an invalid clock event payload`
      );
    }
  }
}

function validateTransitionIds(
  provider: string,
  transitionIds: readonly string[]
): void {
  const seen = new Set<string>();
  for (const transitionId of transitionIds) {
    requireText(transitionId, 'provider clock transition id');
    if (seen.has(transitionId)) {
      throw new CoreError(
        'ValidationFailed',
        `provider ${provider} returned duplicate transition ${transitionId}`
      );
    }
    seen.add(transitionId);
  }
}

function deploymentTargetKey(
  deploymentId: string,
  targetId: string,
  provider: string
): string {
  return JSON.stringify([deploymentId, targetId, provider]);
}

function resourceProjectionKey(
  resource: Pick<
    ResourceRecord,
    'deploymentId' | 'targetId' | 'provider' | 'resourceId'
  >
): string {
  return JSON.stringify([
    resource.deploymentId,
    resource.targetId,
    resource.provider,
    resource.resourceId,
  ]);
}

function validateClockEvaluation(
  evaluation: Omit<ProviderClockEvaluation, 'resolvedDeletedResources'>,
  view: ProviderWorldView,
  deploymentTargets: ReadonlySet<string>
): readonly ResourceRecord[] {
  const provider = evaluation.module.provider;
  const resourceIds = new Set<string>();
  const deletedResourceKeys = new Set<string>();
  const existingByIdentity = new Map(
    view.resources
      .filter(
        (resource) =>
          resource.provider === provider && resource.status !== 'deleted'
      )
      .map((resource) => [resourceProjectionKey(resource), resource])
  );
  const deletedResources: ResourceRecord[] = [];

  validateClockEvents(provider, evaluation.events);
  for (const resource of evaluation.resources) {
    requireText(resource.resourceId, 'provider clock resource id');
    const identity = resourceProjectionKey(resource);
    if (
      resource.provider !== provider ||
      resource.worldId !== view.world.worldId ||
      !deploymentTargets.has(
        deploymentTargetKey(resource.deploymentId, resource.targetId, provider)
      )
    ) {
      throw new CoreError(
        'ValidationFailed',
        `provider ${provider} returned a clock resource outside its world projection`
      );
    }
    if (resourceIds.has(identity)) {
      throw new CoreError(
        'ValidationFailed',
        `provider ${provider} returned duplicate clock resource ${resource.resourceId}`
      );
    }
    resourceIds.add(identity);
  }
  for (const reference of evaluation.deletedResourceRefs) {
    requireText(
      reference.deploymentId,
      'provider clock deleted resource deployment id'
    );
    requireText(
      reference.targetId,
      'provider clock deleted resource target id'
    );
    requireText(reference.resourceId, 'provider clock deleted resource id');
    const deploymentTarget = deploymentTargetKey(
      reference.deploymentId,
      reference.targetId,
      provider
    );
    const identity = resourceProjectionKey({ ...reference, provider });
    const resource = existingByIdentity.get(identity);
    if (
      deletedResourceKeys.has(identity) ||
      !deploymentTargets.has(deploymentTarget) ||
      !resource ||
      resourceIds.has(identity)
    ) {
      throw new CoreError(
        'ValidationFailed',
        `provider ${provider} returned an invalid deleted clock resource ${reference.resourceId}`
      );
    }
    deletedResourceKeys.add(identity);
    deletedResources.push(resource);
  }
  validateTransitionIds(provider, evaluation.appliedTransitionIds);
  return deletedResources;
}

function snapshotIncompatible(message: string): never {
  throw new CoreError('SnapshotIncompatible', message);
}

function assertSnapshotEvents(
  payload: SnapshotPayload,
  sourceWorldId: string
): void {
  if (payload.events.length === 0) {
    snapshotIncompatible('snapshot event graph is empty');
  }
  for (const [index, event] of payload.events.entries()) {
    if (
      event.worldId !== sourceWorldId ||
      event.sequence !== index + 1 ||
      event.payloadHash !== contentHash(event.payload)
    ) {
      snapshotIncompatible('snapshot event graph is inconsistent');
    }
  }
}

function snapshotDeploymentTargets(
  payload: SnapshotPayload,
  sourceWorldId: string
): ReadonlyMap<string, ReadonlyMap<string, DeploymentTargetIdentity>> {
  const targetsByDeployment = new Map<
    string,
    ReadonlyMap<string, DeploymentTargetIdentity>
  >();
  for (const deployment of payload.deployments) {
    if (
      deployment.worldId !== sourceWorldId ||
      !deployment.deploymentId.trim() ||
      targetsByDeployment.has(deployment.deploymentId) ||
      deployment.targets.length === 0
    ) {
      snapshotIncompatible('snapshot deployment graph is inconsistent');
    }
    const targets = new Map<string, DeploymentTargetIdentity>();
    for (const target of deployment.targets) {
      const identityFields = [target.id, target.provider, target.engine];
      if (
        identityFields.some((field) => !field.trim()) ||
        targets.has(target.id)
      ) {
        snapshotIncompatible('snapshot deployment targets are invalid');
      }
      targets.set(target.id, target);
    }
    for (const outputTargetId of Object.keys(deployment.outputs)) {
      if (!targets.has(outputTargetId)) {
        snapshotIncompatible('snapshot deployment output target is invalid');
      }
    }
    targetsByDeployment.set(deployment.deploymentId, targets);
  }
  return targetsByDeployment;
}

function assertSnapshotResources(
  payload: SnapshotPayload,
  sourceWorldId: string,
  targetsByDeployment: ReadonlyMap<
    string,
    ReadonlyMap<string, DeploymentTargetIdentity>
  >
): void {
  const resourceIdentities = new Set<string>();
  for (const resource of payload.resources) {
    const target = targetsByDeployment
      .get(resource.deploymentId)
      ?.get(resource.targetId);
    const coreWorkload =
      resource.provider === WORKLOAD_PROVIDER &&
      resource.resourceType === WORKLOAD_RESOURCE_TYPE;
    const identity = resourceProjectionKey(resource);
    if (
      resource.worldId !== sourceWorldId ||
      !target ||
      (resource.provider !== target.provider && !coreWorkload) ||
      resourceIdentities.has(identity)
    ) {
      snapshotIncompatible('snapshot resource graph is inconsistent');
    }
    resourceIdentities.add(identity);
  }
}

function isWorkloadResource(resource: ResourceRecord): boolean {
  return (
    resource.provider === WORKLOAD_PROVIDER &&
    resource.resourceType === WORKLOAD_RESOURCE_TYPE
  );
}

function assertSnapshotHasNoWorkloads(payload: SnapshotPayload): void {
  if (payload.resources.some(isWorkloadResource)) {
    snapshotIncompatible(
      'snapshot workload restore requires asynchronous rematerialization'
    );
  }
}

function assertSnapshotGraph(payload: SnapshotPayload): void {
  const sourceWorldId = payload.world.worldId;
  if (!sourceWorldId.trim()) {
    snapshotIncompatible('snapshot world identity is empty');
  }
  assertSnapshotEvents(payload, sourceWorldId);
  const targetsByDeployment = snapshotDeploymentTargets(payload, sourceWorldId);
  assertSnapshotResources(payload, sourceWorldId, targetsByDeployment);
}

export class SimulationCore {
  readonly #maxEventsPerWorld: number;
  readonly #maxResourcesPerWorld: number;
  readonly #workloadEffects: WorkloadEffectPort | undefined;

  constructor(
    readonly store: SimulationStore,
    readonly providers: ProviderRegistry,
    options: SimulationCoreOptions = {}
  ) {
    this.#maxEventsPerWorld = options.maxEventsPerWorld ?? 10_000;
    this.#maxResourcesPerWorld = options.maxResourcesPerWorld ?? 1_000;
    this.#workloadEffects = options.workloadEffects;
  }

  get workloadEffectsAvailable(): boolean {
    return this.#workloadEffects !== undefined;
  }

  createWorld(input: CreateWorldInput, idempotencyKey: string): WorldRecord {
    validateWorldInput(input);
    requireText(idempotencyKey, 'idempotency key');
    if (this.#maxEventsPerWorld < 1) {
      throw new CoreError(
        'QuotaExceeded',
        'world event quota would be exceeded'
      );
    }
    const scope = `create-world:${input.tenantId}:${input.eventId}:${input.teamId}`;
    const existing = this.store.idempotent<WorldRecord>(
      scope,
      idempotencyKey,
      input
    );
    if (existing) return existing;
    const seed = input.seed ?? contentHash({ input, idempotencyKey });
    const world: WorldRecord = {
      worldId: deterministicId('world', { input, idempotencyKey }),
      tenantId: input.tenantId,
      eventId: input.eventId,
      teamId: input.teamId,
      deploymentId: input.deploymentId,
      seed,
      virtualTime: new Date(
        input.virtualTime ?? INITIAL_VIRTUAL_TIME
      ).toISOString(),
      status: 'active',
    };
    return this.store.transaction(() => {
      this.store.insertWorld(world);
      this.store.appendEvent(
        world.worldId,
        'WorldCreated',
        deterministicId('command', { scope, idempotencyKey }),
        { namespace: namespaceOf(world), seed: world.seed }
      );
      this.store.saveIdempotent(scope, idempotencyKey, input, world);
      return world;
    });
  }

  world(worldId: string, expectedNamespace?: WorldNamespace): WorldRecord {
    const world = this.store.world(worldId);
    if (
      !world ||
      (expectedNamespace &&
        !sameNamespace(namespaceOf(world), expectedNamespace))
    ) {
      throw new CoreError('NotFound', 'world does not exist');
    }
    return world;
  }

  #view(worldId: string): ProviderWorldView {
    return {
      world: this.world(worldId),
      resources: this.store.resources(worldId),
    };
  }

  #targetView(
    worldId: string,
    deploymentId: string,
    targetId: string
  ): ProviderWorldView {
    const view = this.#view(worldId);
    return {
      world: view.world,
      resources: view.resources.filter(
        (resource) =>
          resource.deploymentId === deploymentId &&
          resource.targetId === targetId
      ),
    };
  }

  #compile(input: DeploymentInput): CompiledDeployment {
    requireText(input.deploymentId, 'deploymentId');
    requireText(input.problemId, 'problemId');
    const targets = runtimeTargets(input.runtime);
    const identifiedTargets = targets.map((target, index) => ({
      ...target,
      id: targetId(target, index),
    }));
    const resolvedSources = resolveTargetSources(
      identifiedTargets,
      input.templateBody
    );
    const sources = new Map(
      resolvedSources.map((source) => [source.targetId, source])
    );
    const overlay = resolveSimulationOverlay(
      input.simulationOverlay,
      identifiedTargets,
      resolvedSources
    );
    const plans: ProviderTargetPlan[] = [];
    const bootstrap: CapabilityRequirement[] = [];
    identifiedTargets.forEach((target) => {
      requireText(target.provider, 'provider');
      requireText(target.engine, 'engine');
      requireText(target.entry, 'entry');
      const module = this.providers.get(target.provider);
      if (!module?.engines.includes(target.engine)) {
        bootstrap.push(bootstrapRequirement(target));
        return;
      }
      const source = sources.get(target.id);
      if (!source) {
        throw new CoreError(
          'ValidationFailed',
          `artifact source is missing for target ${target.id}`
        );
      }
      plans.push(
        module.compile({
          target,
          targetId: target.id,
          problemId: input.problemId,
          templateBody: source.templateBody,
          artifacts: source.artifacts,
          ...(input.metadata === undefined ? {} : { metadata: input.metadata }),
          ...(overlay.document === undefined
            ? {}
            : { simulationOverlay: overlay.document }),
        })
      );
    });
    const requirements = [
      ...bootstrap,
      ...plans.flatMap((plan) => plan.requirements),
      ...overlay.requirements,
    ];
    const providerRequirements = requirements.filter(
      (requirement) => !isWorkloadRequirement(requirement)
    );
    const workloadRequirements = requirements.filter(isWorkloadRequirement);
    const diagnostics = [
      ...this.providers.diagnose(providerRequirements),
      ...(this.#workloadEffects
        ? []
        : workloadRequirements.map(unavailableWorkloadDiagnostic)),
    ];
    return {
      targets: identifiedTargets.map(({ id, provider, engine }) => ({
        id,
        provider,
        engine,
      })),
      plans,
      diagnostics,
      workloads: overlay.workloads,
    };
  }

  createDeployment(
    worldId: string,
    input: DeploymentInput,
    idempotencyKey: string
  ): DeploymentRecord {
    const world = this.world(worldId);
    if (world.status !== 'active') {
      throw new CoreError('Conflict', 'world is deleted');
    }
    requireText(idempotencyKey, 'idempotency key');
    const scope = `deployment:${worldId}`;
    const existing = this.store.idempotent<DeploymentRecord>(
      scope,
      idempotencyKey,
      input
    );
    if (existing) {
      if (existing.status === 'rejected') {
        throw new CoreError(
          'UnsupportedCapability',
          'deployment requires unavailable simulator capabilities',
          existing.diagnostics
        );
      }
      return this.deployment(worldId, existing.deploymentId);
    }
    const compiled = this.#compile(input);
    if (compiled.diagnostics.length > 0) {
      const rejected: DeploymentRecord = {
        worldId,
        deploymentId: input.deploymentId,
        problemId: input.problemId,
        status: 'rejected',
        targets: compiled.targets,
        outputs: {},
        diagnostics: compiled.diagnostics,
      };
      this.store.transaction(() => {
        this.#assertEventQuota(worldId, 1);
        this.store.saveDeployment(rejected);
        this.store.appendEvent(
          worldId,
          'DeploymentRejected',
          deterministicId('command', { scope, idempotencyKey }),
          {
            deploymentId: input.deploymentId,
            diagnostics: compiled.diagnostics,
          }
        );
        this.store.saveIdempotent(scope, idempotencyKey, input, rejected);
      });
      throw new CoreError(
        'UnsupportedCapability',
        'deployment requires unavailable simulator capabilities',
        compiled.diagnostics
      );
    }
    const view = this.#view(worldId);
    const results: TargetResult[] = compiled.plans.map((plan) => {
      const module = this.providers.get(plan.provider);
      if (!module) throw new CoreError('NotFound', 'provider disappeared');
      return { plan, result: module.deploy(plan, view) };
    });
    const providerResources = results.flatMap(({ plan, result }) =>
      mergeResources(plan, result)
    );
    const workloadResources = compiled.workloads.map((declaration) =>
      workloadResource(worldId, input.deploymentId, declaration)
    );
    const resources = [...providerResources, ...workloadResources];
    const eventsRequired =
      2 +
      resources.length +
      results.reduce((sum, item) => sum + item.result.events.length, 0);
    this.#assertEventQuota(worldId, eventsRequired);
    this.#assertResourceQuota(worldId, resources.length);
    const outputs = Object.fromEntries(
      results.map(({ plan, result }) => [plan.targetId, result.outputs])
    );
    const deployment: DeploymentRecord = {
      worldId,
      deploymentId: input.deploymentId,
      problemId: input.problemId,
      status: workloadResources.length === 0 ? 'ready' : 'deploying',
      targets: compiled.targets,
      outputs,
      diagnostics: [],
    };
    const commandId = deterministicId('command', { scope, idempotencyKey });
    return this.store.transaction(() => {
      this.store.appendEvent(worldId, 'DeploymentRequested', commandId, {
        deploymentId: input.deploymentId,
        problemId: input.problemId,
      });
      for (const resource of providerResources) {
        const stored: ResourceRecord = {
          ...resource,
          worldId,
          deploymentId: input.deploymentId,
          status: 'ready',
        };
        this.store.saveResource(stored);
        this.store.appendEvent(worldId, 'ResourceDeclared', commandId, {
          deploymentId: input.deploymentId,
          provider: resource.provider,
          resourceType: resource.resourceType,
          resourceId: resource.resourceId,
        });
      }
      for (const resource of workloadResources) {
        this.store.saveResource(resource);
        this.store.appendEvent(worldId, 'ResourceDeclared', commandId, {
          deploymentId: input.deploymentId,
          provider: resource.provider,
          resourceType: resource.resourceType,
          resourceId: resource.resourceId,
        });
      }
      for (const { result } of results) {
        for (const event of result.events) {
          this.store.appendEvent(worldId, event.type, commandId, event.payload);
        }
      }
      this.store.saveDeployment(deployment);
      if (deployment.status === 'ready') {
        this.store.appendEvent(worldId, 'DeploymentReady', commandId, {
          deploymentId: input.deploymentId,
          outputs,
        });
      } else {
        this.store.appendEvent(worldId, 'DeploymentDeploying', commandId, {
          deploymentId: input.deploymentId,
        });
      }
      this.store.saveIdempotent(scope, idempotencyKey, input, deployment);
      return deployment;
    });
  }

  deployment(worldId: string, deploymentId: string): DeploymentRecord {
    this.world(worldId);
    const deployment = this.store.deployment(worldId, deploymentId);
    if (!deployment)
      throw new CoreError('NotFound', 'deployment does not exist');
    return deployment;
  }

  async materializeWorkloads(
    worldId: string,
    deploymentId: string
  ): Promise<DeploymentRecord> {
    const world = this.world(worldId);
    if (world.status !== 'active') {
      throw new CoreError('Conflict', 'world is deleted');
    }
    const deployment = this.deployment(worldId, deploymentId);
    if (deployment.status === 'ready') return deployment;
    if (!['deploying', 'failed'].includes(deployment.status)) {
      throw new CoreError(
        'Conflict',
        'deployment cannot materialize workloads in its current state'
      );
    }
    const resources = this.store
      .resources(worldId)
      .filter(
        (resource) =>
          resource.deploymentId === deploymentId &&
          resource.provider === WORKLOAD_PROVIDER &&
          resource.resourceType === WORKLOAD_RESOURCE_TYPE &&
          resource.status !== 'deleted'
      );
    if (resources.length === 0) {
      throw new CoreError(
        'Conflict',
        'deployment has no materializable workload resources'
      );
    }
    const declarations = resources.map(storedWorkloadDeclaration);
    this.#assertEventQuota(worldId, declarations.length + 1);
    const attempt =
      this.store
        .events(worldId)
        .filter(
          (event) =>
            event.type === 'WorkloadMaterializationFailed' &&
            event.payload['deploymentId'] === deploymentId
        ).length + 1;
    const commandId = deterministicId('command', {
      worldId,
      deploymentId,
      operation: 'materialize-workloads',
      attempt,
    });
    try {
      if (!this.#workloadEffects) {
        throw new CoreError(
          'WorkloadEffectFailed',
          'workload effect port is unavailable'
        );
      }
      const results = await this.#workloadEffects.materialize(
        worldId,
        declarations
      );
      const endpoints = materializedEndpoints(worldId, declarations, results);
      const current = this.deployment(worldId, deploymentId);
      if (current.status === 'ready') return current;
      if (this.world(worldId).status !== 'active') {
        throw new CoreError('Conflict', 'world is deleted');
      }
      return this.store.transaction(() => {
        const outputs: Record<string, Readonly<Record<string, string>>> = {
          ...current.outputs,
        };
        for (const resource of resources) {
          const declaration = storedWorkloadDeclaration(resource);
          const endpoint = endpoints.get(resource.resourceId);
          if (!endpoint) {
            throw new CoreError(
              'WorkloadEffectFailed',
              'workload effect returned an invalid materialization result'
            );
          }
          this.store.saveResource({
            ...resource,
            properties: {
              ...resource.properties,
              materialization: { endpoint },
            },
            status: 'ready',
          });
          outputs[declaration.targetId] = {
            ...outputs[declaration.targetId],
            [`Workload.${declaration.id}.Endpoint`]: endpoint,
          };
          this.store.appendEvent(worldId, 'WorkloadMaterialized', commandId, {
            deploymentId,
            workloadId: declaration.id,
            targetId: declaration.targetId,
            resourceRef: declaration.resourceRef,
            resourceId: resource.resourceId,
            endpoint,
          });
        }
        const ready: DeploymentRecord = {
          ...current,
          status: 'ready',
          outputs,
        };
        this.store.saveDeployment(ready);
        this.store.appendEvent(worldId, 'DeploymentReady', commandId, {
          deploymentId,
          outputs,
        });
        return ready;
      });
    } catch {
      const current = this.deployment(worldId, deploymentId);
      if (current.status === 'ready') return current;
      this.store.transaction(() => {
        for (const resource of resources) {
          this.store.saveResource({ ...resource, status: 'failed' });
        }
        this.store.saveDeployment({ ...current, status: 'failed' });
        this.store.appendEvent(
          worldId,
          'WorkloadMaterializationFailed',
          commandId,
          {
            deploymentId,
            code: 'WorkloadEffectFailed',
            retryable: true,
            workloadIds: declarations.map((declaration) => declaration.id),
          }
        );
      });
      throw new CoreError(
        'WorkloadEffectFailed',
        'workload materialization failed and can be retried'
      );
    }
  }

  executeCommand(
    worldId: string,
    input: ExecuteCommandInput,
    idempotencyKey: string
  ): Readonly<Record<string, unknown>> {
    const prepared = this.#prepareProviderCommand(
      worldId,
      input,
      idempotencyKey
    );
    if (prepared.kind === 'existing') return prepared.response;
    const result = prepared.module.reduce(prepared.command, prepared.view);
    return this.#commitProviderCommand(
      worldId,
      input,
      idempotencyKey,
      prepared.scope,
      result
    );
  }

  async executeCommandAsync(
    worldId: string,
    input: ExecuteCommandInput,
    idempotencyKey: string
  ): Promise<Readonly<Record<string, unknown>>> {
    const prepared = this.#prepareProviderCommand(
      worldId,
      input,
      idempotencyKey
    );
    if (prepared.kind === 'existing') return prepared.response;
    const result = prepared.module.reduceAsync
      ? await prepared.module.reduceAsync(prepared.command, prepared.view)
      : prepared.module.reduce(prepared.command, prepared.view);
    return this.#commitProviderCommand(
      worldId,
      input,
      idempotencyKey,
      prepared.scope,
      result
    );
  }

  #prepareProviderCommand(
    worldId: string,
    input: ExecuteCommandInput,
    idempotencyKey: string
  ): ProviderCommandPreparation {
    const world = this.world(worldId);
    if (world.status !== 'active')
      throw new CoreError('Conflict', 'world is deleted');
    requireText(idempotencyKey, 'idempotency key');
    const scope = `command:${worldId}:${input.provider}`;
    const existing = this.store.idempotent<Readonly<Record<string, unknown>>>(
      scope,
      idempotencyKey,
      input
    );
    if (existing) return { kind: 'existing', response: existing };
    const deployment = this.deployment(worldId, input.deploymentId);
    const target = deployment.targets.find(
      (candidate) => candidate.id === input.targetId
    );
    if (
      !target ||
      target.provider !== input.provider ||
      target.engine !== input.engine
    ) {
      throw new CoreError(
        'ValidationFailed',
        'command target does not belong to the deployment'
      );
    }
    const requirement: CapabilityRequirement = {
      provider: input.provider,
      engine: input.engine,
      service: input.service,
      resourceType: input.resourceType,
      operation: input.operation,
      fidelity: ['L0'],
    };
    const diagnostics = this.providers.diagnose([requirement]);
    if (diagnostics.length > 0) {
      throw new CoreError(
        'UnsupportedCapability',
        'operation requires an unavailable simulator capability',
        diagnostics
      );
    }
    const module = this.providers.get(input.provider);
    if (!module) throw new CoreError('NotFound', 'provider does not exist');
    return {
      kind: 'prepared',
      scope,
      module,
      command: {
        worldId,
        deploymentId: input.deploymentId,
        service: input.service,
        operation: input.operation,
        resourceType: input.resourceType,
        input: input.input,
      },
      view: this.#targetView(worldId, input.deploymentId, input.targetId),
    };
  }

  #commitProviderCommand(
    worldId: string,
    input: ExecuteCommandInput,
    idempotencyKey: string,
    scope: string,
    result: ProviderCommandResult
  ): Readonly<Record<string, unknown>> {
    this.#assertEventQuota(worldId, result.events.length);
    this.#assertResourceQuota(worldId, result.resources.length);
    const commandId = deterministicId('command', { scope, idempotencyKey });
    return this.store.transaction(() => {
      for (const resource of result.resources) {
        this.store.saveResource({
          ...resource,
          worldId,
          deploymentId: input.deploymentId,
          targetId: input.targetId,
          status: 'ready',
        });
      }
      for (const resourceId of result.deletedResourceIds) {
        this.store.deleteResource(
          worldId,
          input.deploymentId,
          input.targetId,
          input.provider,
          resourceId
        );
      }
      for (const event of result.events) {
        this.store.appendEvent(worldId, event.type, commandId, event.payload);
      }
      const deployment = this.deployment(worldId, input.deploymentId);
      this.store.saveDeployment({
        ...deployment,
        outputs: {
          ...deployment.outputs,
          [input.targetId]: {
            ...deployment.outputs[input.targetId],
            ...result.outputs,
          },
        },
      });
      this.store.saveIdempotent(scope, idempotencyKey, input, result.response);
      return result.response;
    });
  }

  advanceClock(worldId: string, milliseconds: number): ClockAdvanceResult {
    if (!Number.isSafeInteger(milliseconds) || milliseconds <= 0) {
      throw new CoreError(
        'ValidationFailed',
        'clock advance must be a positive integer'
      );
    }
    const world = this.world(worldId);
    if (world.status !== 'active') {
      throw new CoreError('Conflict', 'world is deleted');
    }
    const virtualTime = new Date(
      Date.parse(world.virtualTime) + milliseconds
    ).toISOString();
    const view = this.#view(worldId);
    const deploymentTargets = new Set(
      this.store
        .deployments(worldId)
        .flatMap((deployment) =>
          deployment.targets.map((target) =>
            deploymentTargetKey(
              deployment.deploymentId,
              target.id,
              target.provider
            )
          )
        )
    );
    const rawEvaluations = this.providers.modules().flatMap((module) => {
      const result = module.advanceClock?.(
        {
          previousVirtualTime: world.virtualTime,
          virtualTime,
        },
        view
      );
      return result ? [{ module, ...result }] : [];
    });
    const evaluations: ProviderClockEvaluation[] = rawEvaluations.map(
      (evaluation) => ({
        ...evaluation,
        resolvedDeletedResources: validateClockEvaluation(
          evaluation,
          view,
          deploymentTargets
        ),
      })
    );
    const transitions: AppliedTransition[] = evaluations
      .flatMap((evaluation) =>
        evaluation.appliedTransitionIds.map((transitionId) => ({
          provider: evaluation.module.provider,
          transitionId,
        }))
      )
      .sort((left, right) =>
        `${left.provider}\u0000${left.transitionId}`.localeCompare(
          `${right.provider}\u0000${right.transitionId}`
        )
      );
    const providerEventCount = evaluations.reduce(
      (total, evaluation) => total + evaluation.events.length,
      0
    );
    this.#assertEventQuota(worldId, 1 + providerEventCount);
    const activeResourceKeys = new Set(
      view.resources
        .filter((resource) => resource.status === 'ready')
        .map(resourceProjectionKey)
    );
    const newResourceCount = evaluations.reduce(
      (total, evaluation) =>
        total +
        evaluation.resources.filter(
          (resource) =>
            resource.status === 'ready' &&
            !activeResourceKeys.has(resourceProjectionKey(resource))
        ).length,
      0
    );
    this.#assertResourceQuota(worldId, newResourceCount);
    const commandId = deterministicId('command', {
      worldId,
      previousVirtualTime: world.virtualTime,
      virtualTime,
      milliseconds,
      transitions,
    });
    return this.store.transaction(() => {
      this.store.setWorldState(worldId, virtualTime, world.status);
      this.store.appendEvent(worldId, 'ClockAdvanced', commandId, {
        milliseconds,
        virtualTime,
        appliedTransitions: transitions,
      });
      for (const evaluation of evaluations) {
        for (const resource of evaluation.resources) {
          this.store.saveResource(resource);
        }
        for (const resource of evaluation.resolvedDeletedResources) {
          this.store.deleteResource(
            worldId,
            resource.deploymentId,
            resource.targetId,
            resource.provider,
            resource.resourceId
          );
        }
        for (const event of evaluation.events) {
          this.store.appendEvent(worldId, event.type, commandId, event.payload);
        }
      }
      return { ...this.world(worldId), appliedTransitions: transitions };
    });
  }

  async deleteWorld(worldId: string): Promise<void> {
    const world = this.world(worldId);
    if (world.status === 'deleted') return;
    this.#assertEventQuota(worldId, 1);
    const hasWorkloads = this.store
      .resources(worldId)
      .some(
        (resource) =>
          resource.provider === WORKLOAD_PROVIDER &&
          resource.resourceType === WORKLOAD_RESOURCE_TYPE &&
          resource.status !== 'deleted'
      );
    if (hasWorkloads) {
      if (!this.#workloadEffects) {
        throw new CoreError(
          'WorkloadEffectFailed',
          'workload cleanup is unavailable and can be retried'
        );
      }
      try {
        await this.#workloadEffects.cleanup(worldId);
      } catch {
        throw new CoreError(
          'WorkloadEffectFailed',
          'workload cleanup failed and can be retried'
        );
      }
    }
    this.store.transaction(() => {
      for (const resource of this.store.resources(worldId)) {
        this.store.deleteResource(
          worldId,
          resource.deploymentId,
          resource.targetId,
          resource.provider,
          resource.resourceId
        );
      }
      for (const deployment of this.store.deployments(worldId)) {
        this.store.saveDeployment({ ...deployment, status: 'deleted' });
      }
      this.store.appendEvent(
        worldId,
        'WorldDeleted',
        deterministicId('command', { worldId, operation: 'delete' }),
        { worldId }
      );
      this.store.setWorldState(worldId, world.virtualTime, 'deleted');
    });
  }

  events(worldId: string): readonly EventRecord[] {
    this.world(worldId);
    return this.store.events(worldId);
  }

  resources(worldId: string): readonly ResourceRecord[] {
    this.world(worldId);
    return this.store.resources(worldId);
  }

  exportSnapshot(worldId: string): WorldSnapshot {
    const payload: SnapshotPayload = {
      snapshotVersion: '1',
      world: this.world(worldId),
      events: this.store.events(worldId),
      deployments: this.store.deployments(worldId),
      resources: this.store.resources(worldId),
    };
    return { payload, hash: contentHash(payload) };
  }

  restoreSnapshot(
    snapshot: WorldSnapshot,
    idempotencyKey: string
  ): WorldRecord {
    requireText(idempotencyKey, 'idempotency key');
    if (
      snapshot.payload.snapshotVersion !== '1' ||
      contentHash(snapshot.payload) !== snapshot.hash
    ) {
      throw new CoreError(
        'SnapshotIncompatible',
        'snapshot hash or version is invalid'
      );
    }
    assertSnapshotGraph(snapshot.payload);
    assertSnapshotHasNoWorkloads(snapshot.payload);
    const scope = `restore-snapshot:${snapshot.payload.world.worldId}`;
    const existing = this.store.idempotent<WorldRecord>(
      scope,
      idempotencyKey,
      snapshot
    );
    if (existing) return existing;
    const source = snapshot.payload.world;
    const restored: WorldRecord = {
      ...source,
      worldId: deterministicId('world', {
        sourceWorldId: source.worldId,
        snapshotHash: snapshot.hash,
        idempotencyKey,
      }),
    };
    this.#assertSnapshotQuota(snapshot.payload);
    return this.store.transaction(() => {
      this.store.insertWorld(restored);
      for (const event of snapshot.payload.events) {
        this.store.setWorldState(restored.worldId, event.virtualTime, 'active');
        this.store.appendEvent(
          restored.worldId,
          event.type,
          event.commandId,
          event.payload
        );
      }
      for (const deployment of snapshot.payload.deployments) {
        this.store.saveDeployment({ ...deployment, worldId: restored.worldId });
      }
      for (const resource of snapshot.payload.resources) {
        this.store.saveResource({ ...resource, worldId: restored.worldId });
      }
      this.store.setWorldState(
        restored.worldId,
        restored.virtualTime,
        restored.status
      );
      this.store.appendEvent(
        restored.worldId,
        'SnapshotRestored',
        deterministicId('command', { scope, idempotencyKey }),
        { sourceWorldId: source.worldId, snapshotHash: snapshot.hash }
      );
      this.store.saveIdempotent(scope, idempotencyKey, snapshot, restored);
      return restored;
    });
  }

  #assertEventQuota(worldId: string, additional: number): void {
    if (
      this.store.events(worldId).length + additional >
      this.#maxEventsPerWorld
    ) {
      throw new CoreError(
        'QuotaExceeded',
        'world event quota would be exceeded'
      );
    }
  }

  #assertResourceQuota(worldId: string, additional: number): void {
    const active = this.store
      .resources(worldId)
      .filter((resource) => resource.status !== 'deleted').length;
    if (active + additional > this.#maxResourcesPerWorld) {
      throw new CoreError(
        'QuotaExceeded',
        'world resource quota would be exceeded'
      );
    }
  }

  #assertSnapshotQuota(payload: SnapshotPayload): void {
    if (payload.events.length + 1 > this.#maxEventsPerWorld) {
      throw new CoreError(
        'QuotaExceeded',
        'snapshot event quota would be exceeded'
      );
    }
    if (payload.resources.length > this.#maxResourcesPerWorld) {
      throw new CoreError(
        'QuotaExceeded',
        'snapshot resource quota would be exceeded'
      );
    }
  }
}
