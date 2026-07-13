import { SIMULATOR_RUNTIME_TARGET_ID_PATTERN } from '@tenkacloud/simulator-contracts';
import { resolveTargetSources } from './artifact-bundle';
import { canonicalJson, contentHash, deterministicId } from './canonical';
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
  ProviderCompileInput,
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

interface ProviderCommandMutation {
  readonly worldId: string;
  readonly input: ExecuteCommandInput;
  readonly idempotencyKey: string;
  readonly scope: string;
  readonly result: ProviderCommandResult;
  readonly commandId: string;
  readonly storedResources: readonly ResourceRecord[];
  readonly deletedResourceKeys: ReadonlySet<string>;
  readonly deployment: DeploymentRecord;
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
  readonly commitStateHash: string;
  readonly projectionRead: boolean;
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
const RUNTIME_ENDPOINT_RESOURCE_TYPE = 'Runtime::Endpoint';
const LOOPBACK_HOSTS = new Set(['127.0.0.1', 'localhost', '::1']);
const SNAPSHOT_HASH = /^[a-f0-9]{64}$/;

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
      const id = target.id ?? '';
      requireText(id, 'composite target id');
      if (!SIMULATOR_RUNTIME_TARGET_ID_PATTERN.test(id)) {
        throw new CoreError(
          'ValidationFailed',
          'composite target id must match ^[a-z][a-z0-9-]{0,31}$'
        );
      }
      if (ids.has(id)) {
        throw new CoreError(
          'ValidationFailed',
          'composite target ids must be unique'
        );
      }
      ids.add(id);
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

function assertProviderResources(
  provider: string,
  resources: readonly ResourceDeclaration[],
  boundary: string
): void {
  const invalid = resources.find((resource) => resource.provider !== provider);
  if (invalid) {
    throw new CoreError(
      'ValidationFailed',
      `${boundary} resource ${invalid.resourceId} does not belong to provider ${provider}`
    );
  }
}

function assertProviderPlan(
  plan: ProviderTargetPlan,
  target: Readonly<{
    id: string;
    provider: string;
    engine: string;
  }>
): void {
  if (
    plan.targetId !== target.id ||
    plan.provider !== target.provider ||
    plan.engine !== target.engine
  ) {
    throw new CoreError(
      'ValidationFailed',
      `provider plan identity does not match target ${target.id}`
    );
  }
  assertProviderResources(plan.provider, plan.resources, 'provider plan');
}

function assertProviderPlanUnchanged(
  expectedHash: string,
  plan: ProviderTargetPlan
): void {
  let actualHash: string;
  try {
    actualHash = contentHash(plan);
  } catch {
    throw new CoreError(
      'ValidationFailed',
      'provider deploy mutated provider plan'
    );
  }
  if (actualHash !== expectedHash) {
    throw new CoreError(
      'ValidationFailed',
      'provider deploy mutated provider plan'
    );
  }
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

function deleteWorldCommandId(worldId: string): string {
  return deterministicId('command', {
    worldId,
    operation: 'delete',
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

function assertSnapshotLifecycleStatus(payload: SnapshotPayload): void {
  const deployments = new Map(
    payload.deployments.map((deployment) => [
      deployment.deploymentId,
      deployment,
    ])
  );
  if (
    payload.world.status === 'deleted' &&
    (payload.deployments.some(
      (deployment) => deployment.status !== 'deleted'
    ) ||
      payload.resources.some((resource) => resource.status !== 'deleted'))
  ) {
    snapshotIncompatible('deleted snapshot projection is inconsistent');
  }
  if (
    payload.world.status === 'active' &&
    payload.deployments.some((deployment) => deployment.status === 'deleted')
  ) {
    snapshotIncompatible('active snapshot deployment is inconsistent');
  }
  for (const resource of payload.resources) {
    const deployment = deployments.get(resource.deploymentId);
    if (
      (deployment?.status === 'deleted' && resource.status !== 'deleted') ||
      (payload.world.status === 'active' &&
        isWorkloadResource(resource) &&
        resource.status === 'deleted')
    ) {
      snapshotIncompatible('snapshot resource lifecycle is inconsistent');
    }
  }
}

function isWorkloadResource(resource: ResourceRecord): boolean {
  return (
    resource.provider === WORKLOAD_PROVIDER &&
    resource.resourceType === WORKLOAD_RESOURCE_TYPE
  );
}

interface PortableSnapshotProjection {
  readonly deployments: readonly DeploymentRecord[];
  readonly events: readonly EventRecord[];
  readonly resources: readonly ResourceRecord[];
  readonly workloadDeploymentIds: readonly string[];
  readonly materializationEvents: number;
}

interface MaterializationFlight {
  readonly worldId: string;
  readonly promise: Promise<DeploymentRecord>;
}

interface AsyncCommandFlight {
  readonly worldId: string;
  readonly requestHash: string;
  readonly promise: Promise<Readonly<Record<string, unknown>>>;
}

interface WorldLifecycleCoordinator {
  readonly materializationFlights: Map<string, MaterializationFlight>;
  readonly asyncCommandFlights: Map<string, AsyncCommandFlight>;
  readonly worldDeletions: Map<string, Promise<void>>;
  readonly worldOperationTails: Map<string, Promise<void>>;
}

const WORLD_LIFECYCLE_COORDINATORS = new WeakMap<
  SimulationStore,
  WorldLifecycleCoordinator
>();

function worldLifecycleCoordinator(
  store: SimulationStore
): WorldLifecycleCoordinator {
  const existing = WORLD_LIFECYCLE_COORDINATORS.get(store);
  if (existing) return existing;
  const created: WorldLifecycleCoordinator = {
    materializationFlights: new Map(),
    asyncCommandFlights: new Map(),
    worldDeletions: new Map(),
    worldOperationTails: new Map(),
  };
  WORLD_LIFECYCLE_COORDINATORS.set(store, created);
  return created;
}

function snapshotWorkloadDeclarations(
  payload: SnapshotPayload
): ReadonlyMap<string, readonly WorkloadDeclaration[]> {
  const byDeployment = new Map<string, WorkloadDeclaration[]>();
  const identities = new Set<string>();
  for (const resource of payload.resources.filter(isWorkloadResource)) {
    let declaration: WorkloadDeclaration;
    try {
      declaration = storedWorkloadDeclaration(resource);
    } catch {
      snapshotIncompatible('snapshot workload declaration is invalid');
    }
    if (
      resource.targetId !== declaration.targetId ||
      resource.resourceId !== workloadResourceId(declaration)
    ) {
      snapshotIncompatible('snapshot workload identity is inconsistent');
    }
    const identity = `${resource.deploymentId}\u0000${declaration.targetId}\u0000${declaration.id}`;
    if (identities.has(identity)) {
      snapshotIncompatible('snapshot workload identity is duplicated');
    }
    identities.add(identity);
    if (payload.world.status === 'active' && resource.status !== 'deleted') {
      const declarations = byDeployment.get(resource.deploymentId) ?? [];
      declarations.push(declaration);
      byDeployment.set(resource.deploymentId, declarations);
    }
  }
  return byDeployment;
}

function portableEndpointProperties(
  properties: Readonly<Record<string, unknown>>
): Readonly<Record<string, unknown>> {
  const sanitized = { ...properties };
  Reflect.deleteProperty(sanitized, 'ManagedPlacement');
  const state = sanitized['state'];
  if (isRecord(state)) {
    const portableState = { ...state };
    Reflect.deleteProperty(portableState, 'overrideUrl');
    sanitized['state'] = portableState;
  }
  return sanitized;
}

function isWorkloadEndpointOutput(key: string): boolean {
  return key.startsWith('Workload.') && key.endsWith('.Endpoint');
}

function deploymentEndpointValues(
  deployment: DeploymentRecord
): readonly unknown[] {
  return Object.values(deployment.outputs).flatMap((output) =>
    Object.entries(output).flatMap(([key, value]) =>
      isWorkloadEndpointOutput(key) ? [value] : []
    )
  );
}

function resourceEndpointValues(resource: ResourceRecord): readonly unknown[] {
  const values: unknown[] = [];
  const materialization = resource.properties['materialization'];
  if (isWorkloadResource(resource) && isRecord(materialization)) {
    values.push(materialization['endpoint']);
  }
  if (resource.resourceType === RUNTIME_ENDPOINT_RESOURCE_TYPE) {
    const placement = resource.properties['ManagedPlacement'];
    const state = resource.properties['state'];
    if (isRecord(placement)) values.push(placement['EffectiveUrl']);
    if (isRecord(state)) values.push(state['overrideUrl']);
  }
  return values;
}

function portableEndpointValues(payload: SnapshotPayload): readonly string[] {
  const values = [
    ...payload.deployments.flatMap(deploymentEndpointValues),
    ...payload.resources.flatMap(resourceEndpointValues),
  ].filter(
    (value): value is string => typeof value === 'string' && value.length > 0
  );
  return [...new Set(values)].sort((left, right) => right.length - left.length);
}

function portableEventValue(
  value: unknown,
  endpointValues: readonly string[]
): unknown {
  if (typeof value === 'string') {
    return endpointValues.reduce(
      (portable, endpoint) =>
        portable.replaceAll(endpoint, '<portable-endpoint-removed>'),
      value
    );
  }
  if (Array.isArray(value)) {
    return value.map((item) => portableEventValue(item, endpointValues));
  }
  if (!isRecord(value)) return value;
  return Object.fromEntries(
    Object.entries(value)
      .filter(([key]) => !isWorkloadEndpointOutput(key))
      .map(([key, item]) => [key, portableEventValue(item, endpointValues)])
  );
}

function portableSnapshotEvents(
  payload: SnapshotPayload
): readonly EventRecord[] {
  const endpointValues = portableEndpointValues(payload);
  return payload.events.map((event) => {
    const portablePayload = portableEventValue(event.payload, endpointValues);
    if (!isRecord(portablePayload)) {
      snapshotIncompatible('snapshot event payload is invalid');
    }
    return {
      ...event,
      payload: portablePayload,
      payloadHash: contentHash(portablePayload),
    };
  });
}

function withoutWorkloadEndpointOutputs(
  deployment: DeploymentRecord
): DeploymentRecord['outputs'] {
  return Object.fromEntries(
    Object.entries(deployment.outputs).map(([targetId, output]) => {
      const portable = { ...output };
      for (const key of Object.keys(portable)) {
        if (isWorkloadEndpointOutput(key)) {
          Reflect.deleteProperty(portable, key);
        }
      }
      return [targetId, portable];
    })
  );
}

function portableSnapshotProjection(
  payload: SnapshotPayload
): PortableSnapshotProjection {
  const workloads = snapshotWorkloadDeclarations(payload);
  const workloadDeploymentIds = [...workloads.keys()].sort();
  for (const deploymentId of workloadDeploymentIds) {
    const deployment = payload.deployments.find(
      (candidate) => candidate.deploymentId === deploymentId
    );
    if (!deployment || deployment.status === 'deleted') {
      snapshotIncompatible('snapshot workload deployment is inconsistent');
    }
  }
  const resources = payload.resources.map((resource): ResourceRecord => {
    if (isWorkloadResource(resource)) {
      const declaration = storedWorkloadDeclaration(resource);
      return {
        ...resource,
        properties: { declaration },
        status:
          payload.world.status === 'active' && resource.status !== 'deleted'
            ? 'pending'
            : 'deleted',
      };
    }
    if (resource.resourceType === RUNTIME_ENDPOINT_RESOURCE_TYPE) {
      return {
        ...resource,
        properties: portableEndpointProperties(resource.properties),
      };
    }
    return resource;
  });
  const deployments = payload.deployments.map((deployment) => {
    const declarations = workloads.get(deployment.deploymentId);
    const outputs = withoutWorkloadEndpointOutputs(deployment);
    if (!declarations) return { ...deployment, outputs };
    return {
      ...deployment,
      status: 'deploying' as const,
      outputs,
    };
  });
  return {
    deployments,
    events: portableSnapshotEvents(payload),
    resources,
    workloadDeploymentIds,
    materializationEvents:
      workloadDeploymentIds.length +
      workloadDeploymentIds.length +
      [...workloads.values()].reduce(
        (total, declarations) => total + declarations.length,
        0
      ),
  };
}

function assertSnapshotHasNoConnectionCredential(
  properties: Readonly<Record<string, unknown>>
): void {
  const state = properties['state'];
  if (
    isRecord(state) &&
    typeof state['tokenValue'] === 'string' &&
    state['tokenValue'].length > 0
  ) {
    snapshotIncompatible(
      'snapshot contains an unredacted connection credential'
    );
  }
}

function assertSnapshotHasNoConnectionCredentials(
  payload: SnapshotPayload
): void {
  for (const resource of payload.resources) {
    assertSnapshotHasNoConnectionCredential(resource.properties);
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
  assertSnapshotLifecycleStatus(payload);
}

export class SimulationCore {
  readonly #maxEventsPerWorld: number;
  readonly #maxResourcesPerWorld: number;
  readonly #workloadEffects: WorkloadEffectPort | undefined;
  readonly #lifecycle: WorldLifecycleCoordinator;

  constructor(
    readonly store: SimulationStore,
    readonly providers: ProviderRegistry,
    options: SimulationCoreOptions = {}
  ) {
    this.#maxEventsPerWorld = options.maxEventsPerWorld ?? 10_000;
    this.#maxResourcesPerWorld = options.maxResourcesPerWorld ?? 1_000;
    this.#workloadEffects = options.workloadEffects;
    this.#lifecycle = worldLifecycleCoordinator(store);
  }

  get workloadEffectsAvailable(): boolean {
    return this.#workloadEffects !== undefined;
  }

  #enqueueWorldOperation<T>(
    worldId: string,
    operation: () => Promise<T>
  ): Promise<T> {
    const previous =
      this.#lifecycle.worldOperationTails.get(worldId) ?? Promise.resolve();
    const promise = previous.then(operation);
    const tail = promise.then(
      () => undefined,
      () => undefined
    );
    this.#lifecycle.worldOperationTails.set(worldId, tail);
    void tail.then(() => {
      if (this.#lifecycle.worldOperationTails.get(worldId) === tail) {
        this.#lifecycle.worldOperationTails.delete(worldId);
      }
    });
    return promise;
  }

  #assertSynchronousWorldMutationAvailable(worldId: string): void {
    if (
      this.#lifecycle.worldDeletions.has(worldId) ||
      this.#lifecycle.worldOperationTails.has(worldId)
    ) {
      throw new CoreError('Conflict', 'asynchronous world mutation is active');
    }
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
      const committed = this.store.idempotent<WorldRecord>(
        scope,
        idempotencyKey,
        input
      );
      if (committed) return committed;
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

  worldByDeployment(
    expectedNamespace: WorldNamespace,
    deploymentId: string,
    idempotencyKey: string
  ): WorldRecord {
    requireText(expectedNamespace.tenantId, 'tenantId');
    requireText(expectedNamespace.eventId, 'eventId');
    requireText(expectedNamespace.teamId, 'teamId');
    requireText(deploymentId, 'deploymentId');
    requireText(idempotencyKey, 'idempotency key');
    const scope = `create-world:${expectedNamespace.tenantId}:${expectedNamespace.eventId}:${expectedNamespace.teamId}`;
    const pointer = this.store.idempotentResponse(scope, idempotencyKey);
    if (!isRecord(pointer) || typeof pointer['worldId'] !== 'string') {
      throw new CoreError('NotFound', 'world does not exist');
    }
    const world = this.store.world(pointer['worldId']);
    if (
      !world ||
      world.deploymentId !== deploymentId ||
      !sameNamespace(namespaceOf(world), expectedNamespace)
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
      const expectedTarget = {
        id: target.id,
        provider: target.provider,
        engine: target.engine,
      } as const;
      const compileInput: ProviderCompileInput = {
        target,
        targetId: expectedTarget.id,
        problemId: input.problemId,
        templateBody: source.templateBody,
        artifacts: source.artifacts,
        ...(input.metadata === undefined ? {} : { metadata: input.metadata }),
        ...(overlay.document === undefined
          ? {}
          : { simulationOverlay: overlay.document }),
      };
      const plan = structuredClone(
        module.compile(structuredClone(compileInput))
      );
      assertProviderPlan(plan, expectedTarget);
      plans.push(plan);
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

  #assertDeploymentCommitState(
    worldId: string,
    deploymentId: string,
    expectedProjectionHash: string
  ): void {
    if (this.store.deployment(worldId, deploymentId)) {
      throw new CoreError(
        'Conflict',
        'deployment identity already exists in this world'
      );
    }
    if (this.world(worldId).status !== 'active') {
      throw new CoreError('Conflict', 'world is deleted');
    }
    if (
      contentHash({
        view: this.#view(worldId),
        deployments: this.store.deployments(worldId),
      }) !== expectedProjectionHash
    ) {
      throw new CoreError(
        'Conflict',
        'world projection changed during deployment evaluation'
      );
    }
  }

  #saveDeploymentResources(
    worldId: string,
    commandId: string,
    resources: readonly ResourceRecord[]
  ): void {
    for (const resource of resources) {
      this.store.saveResource(resource);
      this.store.appendEvent(worldId, 'ResourceDeclared', commandId, {
        deploymentId: resource.deploymentId,
        provider: resource.provider,
        resourceType: resource.resourceType,
        resourceId: resource.resourceId,
      });
    }
  }

  #appendDeploymentProviderEvents(
    worldId: string,
    commandId: string,
    results: readonly TargetResult[]
  ): void {
    for (const { result } of results) {
      for (const event of result.events) {
        this.store.appendEvent(worldId, event.type, commandId, event.payload);
      }
    }
  }

  #appendDeploymentStatusEvent(
    worldId: string,
    commandId: string,
    deployment: DeploymentRecord
  ): void {
    if (deployment.status === 'ready') {
      this.store.appendEvent(worldId, 'DeploymentReady', commandId, {
        deploymentId: deployment.deploymentId,
        outputs: deployment.outputs,
      });
      return;
    }
    this.store.appendEvent(worldId, 'DeploymentDeploying', commandId, {
      deploymentId: deployment.deploymentId,
    });
  }

  createDeployment(
    worldId: string,
    input: DeploymentInput,
    idempotencyKey: string
  ): DeploymentRecord {
    this.#assertSynchronousWorldMutationAvailable(worldId);
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
      const committed = this.store.transaction(() => {
        const replayed = this.store.idempotent<DeploymentRecord>(
          scope,
          idempotencyKey,
          input
        );
        if (replayed) return { deployment: replayed, replayed: true };
        if (this.store.deployment(worldId, input.deploymentId)) {
          throw new CoreError(
            'Conflict',
            'deployment identity already exists in this world'
          );
        }
        if (this.world(worldId).status !== 'active') {
          throw new CoreError('Conflict', 'world is deleted');
        }
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
        return { deployment: rejected, replayed: false };
      });
      if (committed.deployment.status !== 'rejected') {
        return this.deployment(worldId, committed.deployment.deploymentId);
      }
      throw new CoreError(
        'UnsupportedCapability',
        'deployment requires unavailable simulator capabilities',
        committed.deployment.diagnostics
      );
    }
    const view = this.#view(worldId);
    const expectedProjectionHash = contentHash({
      view,
      deployments: this.store.deployments(worldId),
    });
    const results: TargetResult[] = compiled.plans.map((plan) => {
      const module = this.providers.get(plan.provider);
      if (!module) throw new CoreError('NotFound', 'provider disappeared');
      const expectedPlanHash = contentHash(plan);
      const deployPlan = structuredClone(plan);
      const result = module.deploy(deployPlan, view);
      assertProviderPlanUnchanged(expectedPlanHash, deployPlan);
      assertProviderResources(
        plan.provider,
        result.resources,
        'provider deploy'
      );
      return { plan, result };
    });
    const providerResources: readonly ResourceRecord[] = results.flatMap(
      ({ plan, result }) =>
        mergeResources(plan, result).map((resource) => ({
          ...resource,
          worldId,
          deploymentId: input.deploymentId,
          status: 'ready' as const,
        }))
    );
    const workloadResources = compiled.workloads.map((declaration) =>
      workloadResource(worldId, input.deploymentId, declaration)
    );
    const resources = [...providerResources, ...workloadResources];
    const eventsRequired =
      2 +
      resources.length +
      results.reduce((sum, item) => sum + item.result.events.length, 0);
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
    const committed = this.store.transaction(() => {
      const replayed = this.store.idempotent<DeploymentRecord>(
        scope,
        idempotencyKey,
        input
      );
      if (replayed) return { deployment: replayed, replayed: true };
      this.#assertDeploymentCommitState(
        worldId,
        input.deploymentId,
        expectedProjectionHash
      );
      this.#assertEventQuota(worldId, eventsRequired);
      this.#assertResourceMutationQuota(worldId, resources);
      this.store.appendEvent(worldId, 'DeploymentRequested', commandId, {
        deploymentId: input.deploymentId,
        problemId: input.problemId,
      });
      this.#saveDeploymentResources(worldId, commandId, resources);
      this.#appendDeploymentProviderEvents(worldId, commandId, results);
      this.store.saveDeployment(deployment);
      this.#appendDeploymentStatusEvent(worldId, commandId, deployment);
      this.store.saveIdempotent(scope, idempotencyKey, input, deployment);
      return { deployment, replayed: false };
    });
    if (committed.deployment.status === 'rejected') {
      throw new CoreError(
        'UnsupportedCapability',
        'deployment requires unavailable simulator capabilities',
        committed.deployment.diagnostics
      );
    }
    return committed.replayed
      ? this.deployment(worldId, committed.deployment.deploymentId)
      : committed.deployment;
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
    if (this.#lifecycle.worldDeletions.has(worldId)) {
      throw new CoreError('Conflict', 'world deletion is in progress');
    }
    const flightKey = contentHash({ worldId, deploymentId });
    const existing = this.#lifecycle.materializationFlights.get(flightKey);
    if (existing) return existing.promise;
    const promise = this.#enqueueWorldOperation(worldId, () =>
      this.#materializeWorkloadsOnce(worldId, deploymentId)
    );
    this.#lifecycle.materializationFlights.set(flightKey, { worldId, promise });
    try {
      return await promise;
    } finally {
      if (
        this.#lifecycle.materializationFlights.get(flightKey)?.promise ===
        promise
      ) {
        this.#lifecycle.materializationFlights.delete(flightKey);
      }
    }
  }

  async #materializeWorkloadsOnce(
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
    const resources = this.#activeWorkloadResources(worldId, deploymentId);
    if (resources.length === 0) {
      throw new CoreError(
        'Conflict',
        'deployment has no materializable workload resources'
      );
    }
    const declarations = resources.map(storedWorkloadDeclaration);
    const successEventCount = declarations.length + 1;
    const commandId = this.store.transaction(() => {
      if (this.world(worldId).status !== 'active') {
        throw new CoreError('Conflict', 'world is deleted');
      }
      const current = this.deployment(worldId, deploymentId);
      if (current.status === 'ready') return undefined;
      if (!['deploying', 'failed'].includes(current.status)) {
        throw new CoreError(
          'Conflict',
          'deployment cannot materialize workloads in its current state'
        );
      }
      this.#assertWorkloadProjectionUnchanged(
        resources,
        this.#activeWorkloadResources(worldId, deploymentId)
      );
      const attempt =
        this.store
          .events(worldId)
          .filter(
            (event) =>
              event.type === 'WorkloadMaterializationFailed' &&
              event.payload['deploymentId'] === deploymentId
          ).length + 1;
      const reservedCommandId = deterministicId('command', {
        worldId,
        deploymentId,
        operation: 'materialize-workloads',
        attempt,
      });
      this.store.reserveEvents(
        worldId,
        reservedCommandId,
        successEventCount,
        'materialization'
      );
      if (this.store.hasOtherEventReservation(worldId, reservedCommandId)) {
        throw new CoreError(
          'Conflict',
          'another lifecycle operation is active for this world'
        );
      }
      this.#assertEventQuota(worldId, 0);
      return reservedCommandId;
    });
    if (commandId === undefined) return this.deployment(worldId, deploymentId);
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
      return this.#commitMaterializedWorkloads(
        worldId,
        deploymentId,
        resources,
        endpoints,
        successEventCount,
        commandId
      );
    } catch {
      return this.#handleWorkloadMaterializationFailure(
        worldId,
        deploymentId,
        declarations,
        commandId
      );
    } finally {
      this.#releaseEventReservation(worldId, commandId);
    }
  }

  #activeWorkloadResources(
    worldId: string,
    deploymentId: string
  ): readonly ResourceRecord[] {
    return this.store
      .resources(worldId)
      .filter(
        (resource) =>
          resource.deploymentId === deploymentId &&
          resource.provider === WORKLOAD_PROVIDER &&
          resource.resourceType === WORKLOAD_RESOURCE_TYPE &&
          resource.status !== 'deleted'
      );
  }

  #releaseEventReservation(worldId: string, reservationId: string): void {
    this.store.transaction(() => {
      this.store.releaseEvents(worldId, reservationId);
    });
  }

  #commitMaterializedWorkloads(
    worldId: string,
    deploymentId: string,
    expectedResources: readonly ResourceRecord[],
    endpoints: ReadonlyMap<string, string>,
    successEventCount: number,
    commandId: string
  ): DeploymentRecord {
    return this.store.transaction(() => {
      const commitWorld = this.world(worldId);
      const current = this.deployment(worldId, deploymentId);
      this.store.releaseEvents(worldId, commandId);
      if (current.status === 'ready') return current;
      if (
        commitWorld.status !== 'active' ||
        !['deploying', 'failed'].includes(current.status)
      ) {
        throw new CoreError(
          'Conflict',
          'workload projection changed during materialization'
        );
      }
      const currentResources = this.#activeWorkloadResources(
        worldId,
        deploymentId
      );
      this.#assertWorkloadProjectionUnchanged(
        expectedResources,
        currentResources
      );
      this.#assertEventQuota(worldId, successEventCount);
      const outputs: Record<string, Readonly<Record<string, string>>> = {
        ...current.outputs,
      };
      for (const resource of currentResources) {
        this.#commitMaterializedWorkload(
          worldId,
          deploymentId,
          resource,
          endpoints,
          outputs,
          commandId
        );
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
  }

  #assertWorkloadProjectionUnchanged(
    expected: readonly ResourceRecord[],
    current: readonly ResourceRecord[]
  ): void {
    const projectionHash = (resources: readonly ResourceRecord[]): string => {
      const canonicalProjections: string[] = [];
      for (const resource of resources) {
        canonicalProjections.push(
          canonicalJson([resourceProjectionKey(resource), resource])
        );
      }
      return contentHash(canonicalProjections.sort());
    };
    if (projectionHash(current) !== projectionHash(expected)) {
      throw new CoreError(
        'Conflict',
        'workload resources changed during materialization'
      );
    }
  }

  #commitMaterializedWorkload(
    worldId: string,
    deploymentId: string,
    resource: ResourceRecord,
    endpoints: ReadonlyMap<string, string>,
    outputs: Record<string, Readonly<Record<string, string>>>,
    commandId: string
  ): void {
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

  async #handleWorkloadMaterializationFailure(
    worldId: string,
    deploymentId: string,
    declarations: readonly WorkloadDeclaration[],
    commandId: string
  ): Promise<DeploymentRecord> {
    if (this.world(worldId).status === 'deleted') {
      await this.#cleanupLostMaterialization(worldId, commandId);
    }
    const current = this.deployment(worldId, deploymentId);
    if (current.status === 'ready') {
      this.#releaseEventReservation(worldId, commandId);
      return current;
    }
    const recordedFailure = this.store.transaction(() => {
      this.store.releaseEvents(worldId, commandId);
      if (this.world(worldId).status !== 'active') {
        throw new CoreError('Conflict', 'world is deleted');
      }
      const commitDeployment = this.deployment(worldId, deploymentId);
      if (commitDeployment.status === 'ready') return false;
      if (!['deploying', 'failed'].includes(commitDeployment.status)) {
        throw new CoreError(
          'Conflict',
          'deployment cannot record materialization failure in its current state'
        );
      }
      this.#assertEventQuota(worldId, 1);
      for (const resource of this.#activeWorkloadResources(
        worldId,
        deploymentId
      )) {
        this.store.saveResource({ ...resource, status: 'failed' });
      }
      this.store.saveDeployment({
        ...commitDeployment,
        status: 'failed',
      });
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
      return true;
    });
    if (!recordedFailure) return this.deployment(worldId, deploymentId);
    throw new CoreError(
      'WorkloadEffectFailed',
      'workload materialization failed and can be retried'
    );
  }

  async #cleanupLostMaterialization(
    worldId: string,
    commandId: string
  ): Promise<never> {
    this.#releaseEventReservation(worldId, commandId);
    if (this.#workloadEffects) {
      try {
        await this.#workloadEffects.cleanup(worldId);
      } catch {
        throw new CoreError(
          'WorkloadEffectFailed',
          'workload materialization cleanup failed and can be retried'
        );
      }
    }
    throw new CoreError(
      'WorkloadEffectFailed',
      'workload materialization lost its active world and was cleaned up'
    );
  }

  executeCommand(
    worldId: string,
    input: ExecuteCommandInput,
    idempotencyKey: string
  ): Readonly<Record<string, unknown>> {
    this.#assertSynchronousWorldMutationAvailable(worldId);
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
      result,
      prepared.projectionRead,
      prepared.commitStateHash
    );
  }

  async executeCommandAsync(
    worldId: string,
    input: ExecuteCommandInput,
    idempotencyKey: string
  ): Promise<Readonly<Record<string, unknown>>> {
    if (this.#lifecycle.worldDeletions.has(worldId)) {
      throw new CoreError('Conflict', 'world deletion is in progress');
    }
    requireText(idempotencyKey, 'idempotency key');
    const flightKey = contentHash({
      worldId,
      provider: input.provider,
      idempotencyKey,
    });
    const requestHash = contentHash(input);
    const existing = this.#lifecycle.asyncCommandFlights.get(flightKey);
    if (existing) {
      if (existing.requestHash !== requestHash) {
        throw new CoreError(
          'IdempotencyConflict',
          'idempotency key was reused with a different request'
        );
      }
      return existing.promise;
    }
    const promise = this.#enqueueWorldOperation(worldId, () =>
      this.#executeCommandAsyncOnce(worldId, input, idempotencyKey)
    );
    this.#lifecycle.asyncCommandFlights.set(flightKey, {
      worldId,
      requestHash,
      promise,
    });
    try {
      return await promise;
    } finally {
      if (
        this.#lifecycle.asyncCommandFlights.get(flightKey)?.promise === promise
      ) {
        this.#lifecycle.asyncCommandFlights.delete(flightKey);
      }
    }
  }

  async #executeCommandAsyncOnce(
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
      result,
      prepared.projectionRead,
      prepared.commitStateHash
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
    const module = this.providers.get(input.provider);
    if (!module) {
      const existing = this.store.idempotent<Readonly<Record<string, unknown>>>(
        scope,
        idempotencyKey,
        input
      );
      if (existing) return { kind: 'existing', response: existing };
      throw new CoreError('NotFound', 'provider does not exist');
    }
    const command: ProviderCommandInput = {
      worldId,
      deploymentId: input.deploymentId,
      targetId: input.targetId,
      service: input.service,
      operation: input.operation,
      resourceType: input.resourceType,
      input: input.input,
    };
    const projectionRead = module.commandMode?.(command) === 'projection-read';
    if (!projectionRead) {
      const existing = this.store.idempotent<Readonly<Record<string, unknown>>>(
        scope,
        idempotencyKey,
        input
      );
      if (existing) return { kind: 'existing', response: existing };
    }
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
    return {
      kind: 'prepared',
      scope,
      module,
      command,
      commitStateHash: contentHash({
        world,
        deployment,
        resources: this.store.resources(worldId),
      }),
      projectionRead,
      view: this.#targetView(worldId, input.deploymentId, input.targetId),
    };
  }

  #providerCommandCommitDeployment(
    worldId: string,
    input: ExecuteCommandInput,
    commitStateHash: string
  ): DeploymentRecord {
    const world = this.world(worldId);
    if (world.status !== 'active') {
      throw new CoreError('Conflict', 'world is deleted');
    }
    const deployment = this.deployment(worldId, input.deploymentId);
    if (deployment.status !== 'ready') {
      throw new CoreError('Conflict', 'deployment is not ready');
    }
    if (
      contentHash({
        world,
        deployment,
        resources: this.store.resources(worldId),
      }) !== commitStateHash
    ) {
      throw new CoreError(
        'Conflict',
        'world projection changed during provider evaluation'
      );
    }
    return deployment;
  }

  #applyProviderCommandMutation(mutation: ProviderCommandMutation): void {
    this.#assertEventQuota(mutation.worldId, mutation.result.events.length);
    this.#assertResourceMutationQuota(
      mutation.worldId,
      mutation.storedResources,
      mutation.deletedResourceKeys
    );
    for (const resource of mutation.storedResources) {
      this.store.saveResource(resource);
    }
    for (const resourceId of mutation.result.deletedResourceIds) {
      this.store.deleteResource(
        mutation.worldId,
        mutation.input.deploymentId,
        mutation.input.targetId,
        mutation.input.provider,
        resourceId
      );
    }
    for (const event of mutation.result.events) {
      this.store.appendEvent(
        mutation.worldId,
        event.type,
        mutation.commandId,
        event.payload
      );
    }
    this.store.saveDeployment({
      ...mutation.deployment,
      outputs: {
        ...mutation.deployment.outputs,
        [mutation.input.targetId]: {
          ...mutation.deployment.outputs[mutation.input.targetId],
          ...mutation.result.outputs,
        },
      },
    });
    this.store.saveIdempotent(
      mutation.scope,
      mutation.idempotencyKey,
      mutation.input,
      mutation.result.response
    );
  }

  #commitProviderCommand(
    worldId: string,
    input: ExecuteCommandInput,
    idempotencyKey: string,
    scope: string,
    result: ProviderCommandResult,
    projectionRead: boolean,
    commitStateHash: string
  ): Readonly<Record<string, unknown>> {
    assertProviderResources(
      input.provider,
      result.resources,
      'provider command'
    );
    if (
      projectionRead &&
      (result.events.length > 0 ||
        result.resources.length > 0 ||
        result.deletedResourceIds.length > 0 ||
        Object.keys(result.outputs).length > 0)
    ) {
      throw new CoreError(
        'ValidationFailed',
        'projection read command returned state mutations'
      );
    }
    const commandId = deterministicId('command', { scope, idempotencyKey });
    const storedResources: readonly ResourceRecord[] = result.resources.map(
      (resource) => ({
        ...resource,
        worldId,
        deploymentId: input.deploymentId,
        targetId: input.targetId,
        status: 'ready',
      })
    );
    const deletedResourceKeys = new Set(
      result.deletedResourceIds.map((resourceId) =>
        resourceProjectionKey({
          deploymentId: input.deploymentId,
          targetId: input.targetId,
          provider: input.provider,
          resourceId,
        })
      )
    );
    return this.store.transaction(() => {
      if (!projectionRead) {
        const replayed = this.store.idempotent<
          Readonly<Record<string, unknown>>
        >(scope, idempotencyKey, input);
        if (replayed) return replayed;
      }
      const deployment = this.#providerCommandCommitDeployment(
        worldId,
        input,
        commitStateHash
      );
      if (projectionRead) return result.response;
      this.#applyProviderCommandMutation({
        worldId,
        input,
        idempotencyKey,
        scope,
        result,
        commandId,
        storedResources,
        deletedResourceKeys,
        deployment,
      });
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
    this.#assertSynchronousWorldMutationAvailable(worldId);
    const world = this.world(worldId);
    if (world.status !== 'active') {
      throw new CoreError('Conflict', 'world is deleted');
    }
    const virtualTime = new Date(
      Date.parse(world.virtualTime) + milliseconds
    ).toISOString();
    const view = this.#view(worldId);
    const deployments = this.store.deployments(worldId);
    const deploymentTargets = new Set(
      deployments.flatMap((deployment) =>
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
    const commandId = deterministicId('command', {
      worldId,
      previousVirtualTime: world.virtualTime,
      virtualTime,
      milliseconds,
      transitions,
    });
    const expectedProjectionHash = contentHash({ view, deployments });
    const resources = evaluations.flatMap((evaluation) => evaluation.resources);
    const deletedResourceKeys = new Set(
      evaluations.flatMap((evaluation) =>
        evaluation.resolvedDeletedResources.map(resourceProjectionKey)
      )
    );
    return this.store.transaction(() => {
      const commitWorld = this.world(worldId);
      if (commitWorld.status !== 'active') {
        throw new CoreError('Conflict', 'world is deleted');
      }
      if (
        contentHash({
          view: this.#view(worldId),
          deployments: this.store.deployments(worldId),
        }) !== expectedProjectionHash
      ) {
        throw new CoreError(
          'Conflict',
          'world projection changed during clock evaluation'
        );
      }
      this.#assertEventQuota(worldId, 1 + providerEventCount);
      this.#assertResourceMutationQuota(
        worldId,
        resources,
        deletedResourceKeys
      );
      this.store.setWorldState(worldId, virtualTime, commitWorld.status);
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
    const existing = this.#lifecycle.worldDeletions.get(worldId);
    if (existing) return existing;
    const promise = this.#deleteWorldAfterMaterialization(worldId);
    this.#lifecycle.worldDeletions.set(worldId, promise);
    try {
      await promise;
    } finally {
      if (this.#lifecycle.worldDeletions.get(worldId) === promise) {
        this.#lifecycle.worldDeletions.delete(worldId);
      }
    }
  }

  async reconcilePendingLifecycleOperations(): Promise<void> {
    for (const worldId of this.store.pendingDeletionWorldIds()) {
      await this.deleteWorld(worldId);
    }
  }

  async #deleteWorldAfterMaterialization(worldId: string): Promise<void> {
    await (this.#lifecycle.worldOperationTails.get(worldId) ??
      Promise.resolve());
    const commandId = deleteWorldCommandId(worldId);
    const status = this.store.transaction(() => {
      const world = this.world(worldId);
      this.store.reserveEvents(worldId, commandId, 1, 'deletion');
      if (this.store.hasOtherEventReservation(worldId, commandId)) {
        throw new CoreError(
          'Conflict',
          'another lifecycle operation is active for this world'
        );
      }
      if (world.status === 'active') this.#assertEventQuota(worldId, 0);
      return world.status;
    });
    if (status === 'deleted') {
      await this.#cleanupDeletedWorldWorkloads(worldId);
      this.#releaseEventReservation(worldId, commandId);
      return;
    }
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
      const commitWorld = this.world(worldId);
      if (commitWorld.status === 'deleted') {
        this.store.releaseEvents(worldId, commandId);
        return;
      }
      this.#assertEventQuota(worldId, 0);
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
      this.store.appendEvent(worldId, 'WorldDeleted', commandId, {
        worldId,
      });
      this.store.setWorldState(worldId, commitWorld.virtualTime, 'deleted');
      this.store.releaseEvents(worldId, commandId);
    });
  }

  async #cleanupDeletedWorldWorkloads(worldId: string): Promise<void> {
    const hadWorkloads = this.store
      .resources(worldId)
      .some(
        (resource) =>
          resource.provider === WORKLOAD_PROVIDER &&
          resource.resourceType === WORKLOAD_RESOURCE_TYPE
      );
    if (!hadWorkloads) return;
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
      resources: this.store.resources(worldId).map((resource) => {
        const properties =
          this.providers
            .get(resource.provider)
            ?.snapshotProperties?.(resource) ?? resource.properties;
        assertSnapshotHasNoConnectionCredential(properties);
        return properties === resource.properties
          ? resource
          : { ...resource, properties };
      }),
    };
    return { payload, hash: contentHash(payload) };
  }

  restoredWorld(
    sourceWorldId: string,
    snapshotHash: string,
    idempotencyKey: string,
    expectedNamespace?: WorldNamespace
  ): WorldRecord {
    requireText(sourceWorldId, 'source worldId');
    requireText(idempotencyKey, 'idempotency key');
    if (!SNAPSHOT_HASH.test(snapshotHash)) {
      throw new CoreError('NotFound', 'restored world does not exist');
    }
    const source = this.world(sourceWorldId, expectedNamespace);
    const scope = `restore-snapshot:${sourceWorldId}`;
    const pointer = this.store.idempotentResponse(scope, idempotencyKey);
    const expectedWorldId = deterministicId('world', {
      sourceWorldId,
      snapshotHash,
      idempotencyKey,
    });
    if (!isRecord(pointer) || pointer['worldId'] !== expectedWorldId) {
      throw new CoreError('NotFound', 'restored world does not exist');
    }
    const restored = this.store.world(expectedWorldId);
    if (
      !restored ||
      restored.deploymentId !== source.deploymentId ||
      !sameNamespace(namespaceOf(restored), namespaceOf(source))
    ) {
      throw new CoreError('NotFound', 'restored world does not exist');
    }
    return restored;
  }

  async restoreSnapshot(
    snapshot: WorldSnapshot,
    idempotencyKey: string
  ): Promise<WorldRecord> {
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
    assertSnapshotHasNoConnectionCredentials(snapshot.payload);
    const projection = portableSnapshotProjection(snapshot.payload);
    const source = snapshot.payload.world;
    const restoredWorldId = deterministicId('world', {
      sourceWorldId: source.worldId,
      snapshotHash: snapshot.hash,
      idempotencyKey,
    });
    const scope = `restore-snapshot:${snapshot.payload.world.worldId}`;
    const existing = this.store.idempotent<WorldRecord>(
      scope,
      idempotencyKey,
      snapshot
    );
    if (existing) {
      if (existing.worldId !== restoredWorldId) {
        throw new CoreError(
          'SnapshotIncompatible',
          'stored snapshot restore identity is inconsistent'
        );
      }
      const current = this.world(restoredWorldId);
      await this.#resumeSnapshotWorkloads(
        current.worldId,
        projection.workloadDeploymentIds
      );
      return this.world(current.worldId);
    }
    if (projection.workloadDeploymentIds.length > 0 && !this.#workloadEffects) {
      snapshotIncompatible(
        'snapshot workload restore requires an available workload runner'
      );
    }
    const restored: WorldRecord = {
      ...source,
      worldId: restoredWorldId,
    };
    const committed = this.store.transaction(() => {
      const replayed = this.store.idempotent<WorldRecord>(
        scope,
        idempotencyKey,
        snapshot
      );
      if (replayed) {
        if (replayed.worldId !== restoredWorldId) {
          throw new CoreError(
            'SnapshotIncompatible',
            'stored snapshot restore identity is inconsistent'
          );
        }
        return replayed;
      }
      this.#assertSnapshotQuota(
        snapshot.payload,
        projection.materializationEvents
      );
      this.store.insertWorld(restored);
      for (const event of projection.events) {
        this.store.setWorldState(restored.worldId, event.virtualTime, 'active');
        this.store.appendEvent(
          restored.worldId,
          event.type,
          event.commandId,
          event.payload
        );
      }
      for (const deployment of projection.deployments) {
        this.store.saveDeployment({ ...deployment, worldId: restored.worldId });
      }
      for (const resource of projection.resources) {
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
    await this.#resumeSnapshotWorkloads(
      committed.worldId,
      projection.workloadDeploymentIds
    );
    return this.world(committed.worldId);
  }

  async #resumeSnapshotWorkloads(
    worldId: string,
    deploymentIds: readonly string[]
  ): Promise<void> {
    if (this.world(worldId).status === 'deleted') return;
    for (const deploymentId of deploymentIds) {
      if (this.deployment(worldId, deploymentId).status === 'ready') continue;
      await this.materializeWorkloads(worldId, deploymentId);
    }
  }

  #assertEventQuota(worldId: string, additional: number): void {
    this.store.recoverDeadEventReservations();
    if (
      this.store.events(worldId).length +
        this.store.reservedEventCount(worldId) +
        additional >
      this.#maxEventsPerWorld
    ) {
      throw new CoreError(
        'QuotaExceeded',
        'world event quota would be exceeded'
      );
    }
  }

  #assertResourceMutationQuota(
    worldId: string,
    upserts: readonly ResourceRecord[],
    deletedResourceKeys: ReadonlySet<string> = new Set()
  ): void {
    const projectedActive = new Set(
      this.store
        .resources(worldId)
        .filter((resource) => resource.status !== 'deleted')
        .map(resourceProjectionKey)
    );
    for (const resource of upserts) {
      const key = resourceProjectionKey(resource);
      if (resource.status === 'deleted') projectedActive.delete(key);
      else projectedActive.add(key);
    }
    for (const key of deletedResourceKeys) projectedActive.delete(key);
    if (projectedActive.size > this.#maxResourcesPerWorld) {
      throw new CoreError(
        'QuotaExceeded',
        'world resource quota would be exceeded'
      );
    }
  }

  #assertSnapshotQuota(
    payload: SnapshotPayload,
    materializationEvents = 0
  ): void {
    if (
      payload.events.length + 1 + materializationEvents >
      this.#maxEventsPerWorld
    ) {
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
