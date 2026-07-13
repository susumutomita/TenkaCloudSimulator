import {
  CoreError,
  deterministicId,
  HTTP_ENDPOINT_RESOURCE,
  MAX_PROVIDER_HTTP_BODY_BYTES,
  type ProviderCapability,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderCompileInput,
  type ProviderDeploymentResult,
  type ProviderModule,
  type ProviderTargetPlan,
  type ProviderWorldView,
  providerHttpRequest,
  providerHttpResponse,
  type ResourceDeclaration,
  singleReadyDeploymentResource,
} from '@tenkacloud/simulator-core';
import {
  APPLICATION_RESOURCE,
  createStoredApplication,
  parseApplicationInput,
  type StoredApplication,
  storedApplication,
  VERSION_RESOURCE,
} from './application';

const PROVIDER = 'sakura';
const ENGINE = 'apprun';
const SERVICE = 'apprun';
export const HTTP_ENDPOINT = HTTP_ENDPOINT_RESOURCE;

const OPERATIONS = [
  ['deploy', APPLICATION_RESOURCE, ['L0', 'L1', 'L2']],
  ['postApplication', APPLICATION_RESOURCE, ['L0', 'L1']],
  ['listApplications', APPLICATION_RESOURCE, ['L0', 'L1']],
  ['getApplication', APPLICATION_RESOURCE, ['L0', 'L1']],
  ['patchApplication', APPLICATION_RESOURCE, ['L0', 'L1']],
  ['deleteApplication', APPLICATION_RESOURCE, ['L0', 'L1']],
  ['getVersion', VERSION_RESOURCE, ['L0', 'L1']],
  ['deleteVersion', VERSION_RESOURCE, ['L0', 'L1']],
  ['getTraffics', APPLICATION_RESOURCE, ['L0', 'L1']],
  ['putTraffics', APPLICATION_RESOURCE, ['L0', 'L1']],
  ['getPacketFilter', APPLICATION_RESOURCE, ['L0', 'L1', 'L3']],
  ['patchPacketFilter', APPLICATION_RESOURCE, ['L0', 'L1', 'L3']],
] as const;

const CAPABILITIES: readonly ProviderCapability[] = [
  ...OPERATIONS.map(([operation, resourceType, fidelity]) => ({
    capabilityId: `sakura.apprun.${operation}`,
    provider: PROVIDER,
    engine: ENGINE,
    service: SERVICE,
    resourceType,
    operation,
    fidelity,
  })),
  {
    capabilityId: 'sakura.http.request',
    provider: PROVIDER,
    engine: ENGINE,
    service: 'http',
    resourceType: HTTP_ENDPOINT,
    operation: 'Request',
    fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
  },
  {
    capabilityId: 'sakura.http.probe',
    provider: PROVIDER,
    engine: ENGINE,
    service: 'http',
    resourceType: HTTP_ENDPOINT,
    operation: 'Probe',
    fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
  },
];

const WORKLOAD_PROVIDER = 'runtime';
const WORKLOAD_RESOURCE = 'Runtime::Workload';
const WORKLOAD_OUTPUT = 'BaseUrl';
const LOOPBACK_HOSTS = new Set(['127.0.0.1', '::1']);
const HTTP_TIMEOUT_MILLISECONDS = 8_000;

function applicationResources(
  world: ProviderWorldView
): readonly ResourceDeclaration[] {
  return world.resources
    .filter(
      (resource) =>
        resource.provider === PROVIDER &&
        resource.resourceType === APPLICATION_RESOURCE &&
        resource.status === 'ready'
    )
    .map((resource) => ({
      provider: resource.provider,
      resourceType: resource.resourceType,
      resourceId: resource.resourceId,
      properties: resource.properties,
    }));
}

function requestedId(command: ProviderCommandInput): string {
  const id = command.input['id'];
  if (typeof id !== 'string' || !id.trim()) {
    throw new CoreError('ValidationFailed', 'application id must not be empty');
  }
  return id;
}

function assertCommandIdentity(command: ProviderCommandInput): void {
  if (command.operation === 'Request' || command.operation === 'Probe') {
    if (command.service === 'http' && command.resourceType === HTTP_ENDPOINT) {
      return;
    }
    throw new CoreError(
      'UnsupportedCapability',
      `AppRun ${command.operation} command identity is not supported`
    );
  }
  const versionOperation =
    command.operation === 'getVersion' || command.operation === 'deleteVersion';
  const resourceType = versionOperation
    ? VERSION_RESOURCE
    : APPLICATION_RESOURCE;
  if (command.service !== SERVICE || command.resourceType !== resourceType) {
    throw new CoreError(
      'UnsupportedCapability',
      `AppRun ${command.operation} command identity is not supported`
    );
  }
}

function findApplication(
  command: ProviderCommandInput,
  world: ProviderWorldView
): {
  readonly resource: ResourceDeclaration;
  readonly application: StoredApplication;
} {
  const id = requestedId(command);
  const resource = applicationResources(world).find(
    (candidate) => candidate.resourceId === id
  );
  if (!resource) throw new CoreError('NotFound', 'application does not exist');
  return { resource, application: storedApplication(resource.properties) };
}

function response(
  eventType: string,
  resource: ResourceDeclaration,
  body: Readonly<Record<string, unknown>>,
  outputs: Readonly<Record<string, string>> = {}
): ProviderCommandResult {
  return {
    events: [
      {
        type: eventType,
        payload: { resourceId: resource.resourceId, operation: eventType },
      },
    ],
    resources: [resource],
    deletedResourceIds: [],
    outputs,
    response: body,
  };
}

function recordValue(
  value: unknown,
  label: string
): Readonly<Record<string, unknown>> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new CoreError('ValidationFailed', `${label} must be an object`);
  }
  return value as Readonly<Record<string, unknown>>;
}

function validatedLoopbackEndpoint(value: unknown): URL {
  if (typeof value !== 'string') {
    throw new CoreError('Conflict', 'AppRun workload endpoint is unavailable');
  }
  let endpoint: URL;
  try {
    endpoint = new URL(value);
  } catch {
    throw new CoreError('Conflict', 'AppRun workload endpoint is invalid');
  }
  if (
    endpoint.protocol !== 'http:' ||
    !LOOPBACK_HOSTS.has(endpoint.hostname.replace(/^\[|\]$/g, '')) ||
    !endpoint.port ||
    endpoint.username ||
    endpoint.password ||
    endpoint.pathname !== '/' ||
    endpoint.search ||
    endpoint.hash
  ) {
    throw new CoreError(
      'Conflict',
      'AppRun workload endpoint is not loopback HTTP'
    );
  }
  return endpoint;
}

function workloadEndpoint(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  path: string
): string {
  const applicationResource = singleReadyDeploymentResource(
    world,
    command.deploymentId,
    PROVIDER,
    APPLICATION_RESOURCE,
    'AppRun application'
  );
  if (applicationResource.properties['status'] !== 'Healthy') {
    throw new CoreError('Conflict', 'AppRun application endpoint is not ready');
  }
  const application = storedApplication(applicationResource.properties);
  if (!application.public_url.trim()) {
    throw new CoreError(
      'ValidationFailed',
      'AppRun application endpoint projection is invalid'
    );
  }
  const component = application.components[0];
  if (!component || application.components.length !== 1) {
    throw new CoreError(
      'Conflict',
      'AppRun application workload binding is ambiguous'
    );
  }
  const expectedImage = component.deploy_source.container_registry.image;
  const expectedHealthPath = component.probe?.http_get.path ?? '/';
  const workloads = world.resources.filter(
    (resource) =>
      resource.deploymentId === command.deploymentId &&
      resource.targetId === applicationResource.targetId &&
      resource.provider === WORKLOAD_PROVIDER &&
      resource.resourceType === WORKLOAD_RESOURCE &&
      resource.status !== 'deleted'
  );
  if (workloads.length === 0) {
    throw new CoreError(
      'NotFound',
      'AppRun materialized workload does not exist'
    );
  }
  if (workloads.length !== 1) {
    throw new CoreError(
      'Conflict',
      'AppRun materialized workload is ambiguous'
    );
  }
  const workload = workloads[0];
  if (workload?.status !== 'ready') {
    throw new CoreError(
      'Conflict',
      'AppRun materialized workload is not ready'
    );
  }
  const declaration = recordValue(
    workload.properties['declaration'],
    'AppRun workload declaration'
  );
  if (
    declaration['targetId'] !== applicationResource.targetId ||
    declaration['resourceRef'] !== WORKLOAD_OUTPUT ||
    declaration['image'] !== expectedImage ||
    declaration['containerPort'] !== application.port ||
    (declaration['healthPath'] ?? '/') !== expectedHealthPath
  ) {
    throw new CoreError(
      'Conflict',
      'AppRun workload binding does not match application'
    );
  }
  const materialization = recordValue(
    workload.properties['materialization'],
    'AppRun workload materialization'
  );
  const endpoint = validatedLoopbackEndpoint(materialization['endpoint']);
  return new URL(path, `${endpoint.origin}/`).toString();
}

async function boundedResponseBody(response: Response): Promise<string> {
  const reader = response.body?.getReader();
  if (!reader) return '';
  const chunks: Uint8Array[] = [];
  let total = 0;
  while (true) {
    const next = await reader.read();
    if (next.done) break;
    total += next.value.byteLength;
    if (total > MAX_PROVIDER_HTTP_BODY_BYTES) {
      await reader.cancel();
      throw new CoreError(
        'QuotaExceeded',
        'AppRun workload response body is too large'
      );
    }
    chunks.push(next.value);
  }
  const body = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    body.set(chunk, offset);
    offset += chunk.byteLength;
  }
  try {
    return new TextDecoder('utf-8', { fatal: true }).decode(body);
  } catch {
    throw new CoreError(
      'ValidationFailed',
      'AppRun workload response body must be UTF-8'
    );
  }
}

async function requestApplicationEndpoint(
  command: ProviderCommandInput,
  world: ProviderWorldView
): Promise<ProviderCommandResult> {
  const request = providerHttpRequest(command.input);
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    throw new CoreError(
      'UnsupportedCapability',
      `AppRun HTTP method ${request.method} is not supported`
    );
  }
  const endpoint = workloadEndpoint(command, world, request.path);
  let response: Response;
  try {
    response = await fetch(endpoint, {
      method: request.method,
      headers: request.headers,
      redirect: 'manual',
      signal: AbortSignal.timeout(HTTP_TIMEOUT_MILLISECONDS),
    });
  } catch {
    throw new CoreError('Conflict', 'AppRun workload endpoint is unreachable');
  }
  if (response.status >= 300 && response.status < 400) {
    throw new CoreError(
      'Conflict',
      'AppRun workload endpoint redirect is forbidden'
    );
  }
  const body = await boundedResponseBody(response);
  const endpointResponse = providerHttpResponse(request, {
    statusCode: response.status,
    body,
    contentType:
      response.headers.get('content-type') ?? 'application/octet-stream',
  });
  return {
    events: [
      {
        type: 'SakuraApplicationRequestExecuted',
        payload: {
          operation: command.operation,
        },
      },
    ],
    resources: [],
    deletedResourceIds: [],
    outputs: {},
    response: endpointResponse,
  };
}

function createApplication(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const input = parseApplicationInput(command.input['application']);
  const duplicate = applicationResources(world)
    .map((resource) => storedApplication(resource.properties))
    .some((application) => application.name === input.name);
  if (duplicate)
    throw new CoreError('Conflict', 'application name already exists');
  const application = createStoredApplication(
    input,
    {
      worldId: command.worldId,
      deploymentId: command.deploymentId,
      name: input.name,
    },
    world.world.virtualTime
  );
  const resource: ResourceDeclaration = {
    provider: PROVIDER,
    resourceType: APPLICATION_RESOURCE,
    resourceId: application.id,
    properties: application,
  };
  return response('SakuraApplicationCreated', resource, application, {
    ApplicationId: application.id,
    ApplicationUrl: application.public_url,
  });
}

const APPLICATION_SORT_FIELDS = new Set([
  'id',
  'name',
  'status',
  'public_url',
  'created_at',
]);

function listInteger(
  value: unknown,
  label: string,
  defaultValue: number,
  maximum?: number
): number {
  const resolved = value ?? defaultValue;
  if (
    typeof resolved !== 'number' ||
    !Number.isInteger(resolved) ||
    resolved < 1 ||
    (maximum !== undefined && resolved > maximum)
  ) {
    throw new CoreError('ValidationFailed', `${label} is invalid`);
  }
  return resolved;
}

function listApplications(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const pageNum = listInteger(command.input['page_num'], 'page_num', 1);
  const pageSize = listInteger(
    command.input['page_size'],
    'page_size',
    50,
    100
  );
  const sortField = command.input['sort_field'] ?? 'created_at';
  const sortOrder = command.input['sort_order'] ?? 'desc';
  if (
    typeof sortField !== 'string' ||
    !APPLICATION_SORT_FIELDS.has(sortField)
  ) {
    throw new CoreError('ValidationFailed', 'sort_field is invalid');
  }
  if (sortOrder !== 'asc' && sortOrder !== 'desc') {
    throw new CoreError('ValidationFailed', 'sort_order is invalid');
  }
  const allApplications = applicationResources(world)
    .map((resource) => storedApplication(resource.properties))
    .sort((left, right) => {
      const order = String(left[sortField]).localeCompare(
        String(right[sortField])
      );
      const stableOrder = order || left.id.localeCompare(right.id);
      return sortOrder === 'asc' ? stableOrder : -stableOrder;
    });
  const start = (pageNum - 1) * pageSize;
  const applications = allApplications.slice(start, start + pageSize);
  return {
    events: [
      {
        type: 'SakuraApplicationsListed',
        payload: { count: applications.length },
      },
    ],
    resources: [],
    deletedResourceIds: [],
    outputs: {},
    response: {
      applications,
      page_num: pageNum,
      page_size: pageSize,
      total: allApplications.length,
    },
  };
}

function getApplication(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const { application } = findApplication(command, world);
  return {
    events: [
      {
        type: 'SakuraApplicationRead',
        payload: { resourceId: application.id },
      },
    ],
    resources: [],
    deletedResourceIds: [],
    outputs: {},
    response: application,
  };
}

function patchApplication(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const { resource, application } = findApplication(command, world);
  const patch = command.input['application'];
  if (patch === null || typeof patch !== 'object' || Array.isArray(patch)) {
    throw new CoreError(
      'ValidationFailed',
      'application patch must be an object'
    );
  }
  const allTrafficAvailable = Reflect.get(patch, 'all_traffic_available');
  if (
    allTrafficAvailable !== undefined &&
    typeof allTrafficAvailable !== 'boolean'
  ) {
    throw new CoreError(
      'ValidationFailed',
      'all_traffic_available must be a boolean'
    );
  }
  const merged = parseApplicationInput({ ...application, ...patch });
  const version = {
    id: deterministicId('version', {
      applicationId: application.id,
      version: application.versions.length + 1,
      body: merged,
    }),
    name: `${merged.name}-v${application.versions.length + 1}`,
    created_at: world.world.virtualTime,
  };
  const updated: StoredApplication = {
    ...application,
    ...merged,
    versions: [...application.versions, version],
    ...(allTrafficAvailable === true
      ? { traffics: [{ version_name: version.name, percent: 100 }] }
      : {}),
  };
  return response(
    'SakuraApplicationPatched',
    { ...resource, properties: updated },
    updated
  );
}

function deleteApplication(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const { application } = findApplication(command, world);
  return {
    events: [
      {
        type: 'SakuraApplicationDeleted',
        payload: { resourceId: application.id },
      },
    ],
    resources: [],
    deletedResourceIds: [application.id],
    outputs: {},
    response: { id: application.id, deleted: true },
  };
}

function versionOperation(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  remove: boolean
): ProviderCommandResult {
  const { resource, application } = findApplication(command, world);
  const versionId = command.input['versionId'];
  if (typeof versionId !== 'string') {
    throw new CoreError('ValidationFailed', 'version id must be a string');
  }
  const version = application.versions.find(
    (candidate) => candidate.id === versionId
  );
  if (!version)
    throw new CoreError('NotFound', 'application version does not exist');
  if (!remove) {
    return {
      events: [
        {
          type: 'SakuraApplicationVersionRead',
          payload: { resourceId: application.id, versionId },
        },
      ],
      resources: [],
      deletedResourceIds: [],
      outputs: {},
      response: version,
    };
  }
  if (application.versions.length === 1) {
    throw new CoreError(
      'Conflict',
      'the active application version cannot be deleted'
    );
  }
  const updated: StoredApplication = {
    ...application,
    versions: application.versions.filter(
      (candidate) => candidate.id !== versionId
    ),
  };
  return response(
    'SakuraApplicationVersionDeleted',
    { ...resource, properties: updated },
    { id: versionId, deleted: true }
  );
}

function trafficOperation(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  update: boolean
): ProviderCommandResult {
  const { resource, application } = findApplication(command, world);
  if (!update) {
    return {
      events: [
        {
          type: 'SakuraApplicationTrafficRead',
          payload: { resourceId: application.id },
        },
      ],
      resources: [],
      deletedResourceIds: [],
      outputs: {},
      response: { traffics: application.traffics },
    };
  }
  const traffics = command.input['traffics'];
  if (!Array.isArray(traffics) || traffics.length < 1 || traffics.length > 4) {
    throw new CoreError('ValidationFailed', 'traffics must be an array');
  }
  const parsed = traffics.map((traffic) => {
    if (traffic === null || typeof traffic !== 'object') {
      throw new CoreError('ValidationFailed', 'traffic must be an object');
    }
    const versionName = Reflect.get(traffic, 'version_name');
    const isLatestVersion = Reflect.get(traffic, 'is_latest_version');
    const percent = Reflect.get(traffic, 'percent');
    if (
      !(
        (typeof versionName === 'string' && isLatestVersion === undefined) ||
        (versionName === undefined && isLatestVersion === true)
      ) ||
      typeof percent !== 'number' ||
      !Number.isInteger(percent) ||
      percent < 0 ||
      percent > 100
    ) {
      throw new CoreError('ValidationFailed', 'traffic entry is invalid');
    }
    const latest = application.versions.at(-1);
    if (!latest) {
      throw new CoreError('Conflict', 'application has no version');
    }
    return {
      version_name: typeof versionName === 'string' ? versionName : latest.name,
      percent,
    };
  });
  if (parsed.reduce((sum, traffic) => sum + traffic.percent, 0) !== 100) {
    throw new CoreError(
      'ValidationFailed',
      'traffic percentages must total 100'
    );
  }
  const names = new Set(application.versions.map((version) => version.name));
  if (parsed.some((traffic) => !names.has(traffic.version_name))) {
    throw new CoreError(
      'ValidationFailed',
      'traffic references an unknown version'
    );
  }
  if (
    new Set(parsed.map((traffic) => traffic.version_name)).size !==
    parsed.length
  ) {
    throw new CoreError(
      'ValidationFailed',
      'traffic destinations must be unique'
    );
  }
  const updated: StoredApplication = { ...application, traffics: parsed };
  return response(
    'SakuraApplicationTrafficUpdated',
    { ...resource, properties: updated },
    { traffics: parsed }
  );
}

function validIpv4(value: string): boolean {
  const octets = value.split('.');
  return (
    octets.length === 4 &&
    octets.every(
      (octet) => /^(?:0|[1-9]\d{0,2})$/.test(octet) && Number(octet) <= 255
    )
  );
}

function packetFilterOperation(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  update: boolean
): ProviderCommandResult {
  const { resource, application } = findApplication(command, world);
  if (!update) {
    return {
      events: [
        {
          type: 'SakuraApplicationPacketFilterRead',
          payload: { resourceId: application.id },
        },
      ],
      resources: [],
      deletedResourceIds: [],
      outputs: {},
      response: application.packet_filter,
    };
  }
  const filter = command.input['packet_filter'];
  if (filter === null || typeof filter !== 'object' || Array.isArray(filter)) {
    throw new CoreError('ValidationFailed', 'packet filter must be an object');
  }
  const requestedEnabled = Reflect.get(filter, 'is_enabled');
  const requestedSettings = Reflect.get(filter, 'settings');
  if (requestedEnabled === undefined && requestedSettings === undefined) {
    throw new CoreError('ValidationFailed', 'packet filter patch is empty');
  }
  const isEnabled = requestedEnabled ?? application.packet_filter.is_enabled;
  const settings = requestedSettings ?? application.packet_filter.settings;
  if (
    typeof isEnabled !== 'boolean' ||
    !Array.isArray(settings) ||
    settings.length > 10
  ) {
    throw new CoreError('ValidationFailed', 'packet filter is invalid');
  }
  const parsedSettings = settings.map((setting) => {
    if (setting === null || typeof setting !== 'object') {
      throw new CoreError(
        'ValidationFailed',
        'packet filter setting is invalid'
      );
    }
    const fromIp = Reflect.get(setting, 'from_ip');
    const prefix = Reflect.get(setting, 'from_ip_prefix_length');
    if (
      typeof fromIp !== 'string' ||
      !validIpv4(fromIp) ||
      typeof prefix !== 'number' ||
      !Number.isInteger(prefix) ||
      prefix < 0 ||
      prefix > 32
    ) {
      throw new CoreError(
        'ValidationFailed',
        'packet filter setting is invalid'
      );
    }
    return { from_ip: fromIp, from_ip_prefix_length: prefix };
  });
  const updated: StoredApplication = {
    ...application,
    packet_filter: { is_enabled: isEnabled, settings: parsedSettings },
  };
  return response(
    'SakuraApplicationPacketFilterUpdated',
    { ...resource, properties: updated },
    updated.packet_filter
  );
}

function matchingOverlayWorkload(
  input: ProviderCompileInput,
  application: ReturnType<typeof parseApplicationInput>
): boolean {
  if (input.simulationOverlay === undefined) return false;
  const overlay = recordValue(input.simulationOverlay, 'simulation overlay');
  const values = overlay['workloads'];
  if (values === undefined) return false;
  if (!Array.isArray(values)) {
    throw new CoreError(
      'ValidationFailed',
      'simulation overlay workloads must be an array'
    );
  }
  const candidates = values.filter(
    (value) =>
      value !== null &&
      typeof value === 'object' &&
      !Array.isArray(value) &&
      Reflect.get(value, 'targetId') === input.targetId
  );
  if (candidates.length === 0) return false;
  if (candidates.length !== 1 || application.components.length !== 1) {
    throw new CoreError(
      'ValidationFailed',
      'AppRun simulation workload binding must be unambiguous'
    );
  }
  const workload = recordValue(candidates[0], 'AppRun simulation workload');
  const component = application.components[0];
  if (!component) {
    throw new CoreError(
      'ValidationFailed',
      'AppRun simulation workload has no matching component'
    );
  }
  const image = component.deploy_source.container_registry.image;
  const probe = component.probe?.http_get;
  if (
    workload['resourceRef'] !== WORKLOAD_OUTPUT ||
    typeof workload['image'] !== 'string' ||
    !/@sha256:[a-f0-9]{64}$/.test(workload['image']) ||
    workload['image'] !== image ||
    workload['image'] !== input.target.entry ||
    workload['containerPort'] !== application.port ||
    (workload['healthPath'] ?? '/') !== (probe?.path ?? '/') ||
    (probe !== undefined && probe.port !== application.port)
  ) {
    throw new CoreError(
      'ValidationFailed',
      'AppRun simulation workload does not match the application descriptor'
    );
  }
  return true;
}

export class SakuraProvider implements ProviderModule {
  readonly provider: string;
  readonly engines: readonly string[];
  readonly capabilities: readonly ProviderCapability[];

  constructor() {
    this.provider = PROVIDER;
    this.engines = [ENGINE];
    this.capabilities = CAPABILITIES;
  }

  compile(input: ProviderCompileInput): ProviderTargetPlan {
    let raw: unknown;
    try {
      raw = JSON.parse(input.templateBody);
    } catch {
      throw new CoreError(
        'ValidationFailed',
        'AppRun entry must contain valid JSON'
      );
    }
    const application = parseApplicationInput(raw);
    matchingOverlayWorkload(input, application);
    const stored = createStoredApplication(
      application,
      {
        problemId: input.problemId,
        targetId: input.targetId,
        name: application.name,
      },
      '1970-01-01T00:00:00.000Z'
    );
    return {
      targetId: input.targetId,
      provider: PROVIDER,
      engine: ENGINE,
      requirements: [
        {
          provider: PROVIDER,
          engine: ENGINE,
          service: SERVICE,
          resourceType: APPLICATION_RESOURCE,
          operation: 'deploy',
          fidelity: ['L0', 'L1', 'L2'],
          source: { path: input.target.entry },
        },
      ],
      resources: [
        {
          provider: PROVIDER,
          resourceType: APPLICATION_RESOURCE,
          resourceId: stored.id,
          properties: application,
        },
      ],
    };
  }

  deploy(
    plan: ProviderTargetPlan,
    world: ProviderWorldView
  ): ProviderDeploymentResult {
    const resource = plan.resources[0];
    if (!resource)
      throw new CoreError('ValidationFailed', 'AppRun plan is empty');
    const input = parseApplicationInput(resource.properties);
    const created = createStoredApplication(
      input,
      {
        worldId: world.world.worldId,
        targetId: plan.targetId,
        name: input.name,
      },
      world.world.virtualTime
    );
    const application: StoredApplication = {
      ...created,
      id: resource.resourceId,
      public_url: `https://${resource.resourceId}.apprun.sakura.local`,
    };
    return {
      events: [
        {
          type: 'SakuraApplicationCreated',
          payload: { resourceId: application.id, name: application.name },
        },
      ],
      resources: [
        {
          provider: PROVIDER,
          resourceType: APPLICATION_RESOURCE,
          resourceId: application.id,
          properties: application,
        },
      ],
      outputs: {
        ApplicationId: application.id,
        ApplicationUrl: application.public_url,
        BaseUrl: application.public_url,
      },
    };
  }

  reduce(
    command: ProviderCommandInput,
    world: ProviderWorldView
  ): ProviderCommandResult {
    assertCommandIdentity(command);
    switch (command.operation) {
      case 'Request':
      case 'Probe':
        throw new CoreError(
          'UnsupportedCapability',
          `AppRun ${command.operation} requires async execution`
        );
      case 'postApplication':
        return createApplication(command, world);
      case 'listApplications':
        return listApplications(command, world);
      case 'getApplication':
        return getApplication(command, world);
      case 'patchApplication':
        return patchApplication(command, world);
      case 'deleteApplication':
        return deleteApplication(command, world);
      case 'getVersion':
        return versionOperation(command, world, false);
      case 'deleteVersion':
        return versionOperation(command, world, true);
      case 'getTraffics':
        return trafficOperation(command, world, false);
      case 'putTraffics':
        return trafficOperation(command, world, true);
      case 'getPacketFilter':
        return packetFilterOperation(command, world, false);
      case 'patchPacketFilter':
        return packetFilterOperation(command, world, true);
      default:
        throw new CoreError(
          'UnsupportedCapability',
          `AppRun operation ${command.operation} is not supported`
        );
    }
  }

  async reduceAsync(
    command: ProviderCommandInput,
    world: ProviderWorldView
  ): Promise<ProviderCommandResult> {
    assertCommandIdentity(command);
    if (command.operation === 'Request' || command.operation === 'Probe') {
      return requestApplicationEndpoint(command, world);
    }
    return this.reduce(command, world);
  }
}
