import {
  type CapabilityRequirement,
  CoreError,
  deterministicId,
  HTTP_ENDPOINT_RESOURCE,
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
  terraformNumber,
  terraformResources,
  terraformString,
} from './terraform';

export const CLOUD_RUN_SERVICE = 'google_cloud_run_v2_service';
export const CLOUD_RUN_IAM_MEMBER = 'google_cloud_run_v2_service_iam_member';
export const HTTP_ENDPOINT = HTTP_ENDPOINT_RESOURCE;

const PROVIDER = 'gcp';
const ENGINE = 'infra-manager';

const CAPABILITIES: readonly ProviderCapability[] = [
  {
    capabilityId: 'gcp.infra-manager.deploy',
    provider: PROVIDER,
    engine: ENGINE,
    service: ENGINE,
    resourceType: '*',
    operation: 'deploy',
    fidelity: ['L0', 'L1'],
  },
  {
    capabilityId: 'gcp.run.service.lifecycle',
    provider: PROVIDER,
    engine: ENGINE,
    service: 'run',
    resourceType: CLOUD_RUN_SERVICE,
    operation: 'lifecycle',
    fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
  },
  {
    capabilityId: 'gcp.run.iam.lifecycle',
    provider: PROVIDER,
    engine: ENGINE,
    service: 'run',
    resourceType: CLOUD_RUN_IAM_MEMBER,
    operation: 'lifecycle',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    capabilityId: 'gcp.http.probe',
    provider: PROVIDER,
    engine: ENGINE,
    service: 'http',
    resourceType: HTTP_ENDPOINT,
    operation: 'Probe',
    fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
  },
  {
    capabilityId: 'gcp.http.request',
    provider: PROVIDER,
    engine: ENGINE,
    service: 'http',
    resourceType: HTTP_ENDPOINT,
    operation: 'Request',
    fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
  },
  ...['GetService', 'UpdateService', 'DeleteService'].map((operation) => ({
    capabilityId: `gcp.run.${operation}`,
    provider: PROVIDER,
    engine: ENGINE,
    service: 'run',
    resourceType: CLOUD_RUN_SERVICE,
    operation,
    fidelity: ['L0', 'L1'] as const,
  })),
  ...['GetIamPolicy', 'SetIamPolicy'].map((operation) => ({
    capabilityId: `gcp.run.${operation}`,
    provider: PROVIDER,
    engine: ENGINE,
    service: 'run',
    resourceType: CLOUD_RUN_IAM_MEMBER,
    operation,
    fidelity: ['L0', 'L1', 'L2'] as const,
  })),
];

function serviceResource(
  world: ProviderWorldView,
  id: unknown
): ResourceDeclaration {
  if (typeof id !== 'string' || !id.trim()) {
    throw new CoreError(
      'ValidationFailed',
      'Cloud Run service id must be a string'
    );
  }
  const resource = world.resources.find(
    (candidate) =>
      candidate.provider === PROVIDER &&
      candidate.resourceType === CLOUD_RUN_SERVICE &&
      candidate.resourceId === id &&
      candidate.status === 'ready'
  );
  if (!resource)
    throw new CoreError('NotFound', 'Cloud Run service does not exist');
  return {
    provider: resource.provider,
    resourceType: resource.resourceType,
    resourceId: resource.resourceId,
    properties: resource.properties,
  };
}

function assertCommandIdentity(
  command: ProviderCommandInput,
  service: string,
  resourceType: string
): void {
  if (command.service !== service || command.resourceType !== resourceType) {
    throw new CoreError(
      'UnsupportedCapability',
      `GCP operation ${command.operation} does not support ${command.service}/${command.resourceType}`
    );
  }
}

function objectValue(
  value: unknown,
  label: string
): Readonly<Record<string, unknown>> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new CoreError('ValidationFailed', `${label} must be an object`);
  }
  return value as Readonly<Record<string, unknown>>;
}

function requiredText(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new CoreError('ValidationFailed', `${label} must not be empty`);
  }
  return value;
}

function instanceCount(
  patch: Readonly<Record<string, unknown>>,
  key: string,
  fallback: unknown,
  minimum: number
): number {
  const value = patch[key] ?? fallback;
  if (
    typeof value !== 'number' ||
    !Number.isInteger(value) ||
    value < minimum ||
    value > 100
  ) {
    throw new CoreError(
      'ValidationFailed',
      `${key} must be an integer between ${minimum} and 100`
    );
  }
  return value;
}

function patchService(
  current: Readonly<Record<string, unknown>>,
  input: unknown
): Readonly<Record<string, unknown>> {
  const patch = objectValue(input, 'Cloud Run patch');
  const allowed = new Set(['image', 'minInstanceCount', 'maxInstanceCount']);
  const unsupported = Object.keys(patch).find((key) => !allowed.has(key));
  if (unsupported) {
    throw new CoreError(
      'ValidationFailed',
      `Cloud Run patch field ${unsupported} is not supported`
    );
  }
  if (Object.keys(patch).length === 0) {
    throw new CoreError('ValidationFailed', 'Cloud Run patch is empty');
  }
  const minInstanceCount = instanceCount(
    patch,
    'minInstanceCount',
    current['minInstanceCount'],
    0
  );
  const maxInstanceCount = instanceCount(
    patch,
    'maxInstanceCount',
    current['maxInstanceCount'],
    1
  );
  if (minInstanceCount > maxInstanceCount) {
    throw new CoreError(
      'ValidationFailed',
      'minInstanceCount must not exceed maxInstanceCount'
    );
  }
  const image = patch['image'] ?? current['image'];
  requiredText(image, 'Cloud Run image');
  return {
    ...current,
    ...patch,
    image,
    minInstanceCount,
    maxInstanceCount,
    status: 'Ready',
  };
}

function commandResult(
  type: string,
  response: Readonly<Record<string, unknown>>,
  resources: readonly ResourceDeclaration[] = [],
  deletedResourceIds: readonly string[] = []
): ProviderCommandResult {
  return {
    events: [{ type, payload: { operation: type } }],
    resources,
    deletedResourceIds,
    outputs: {},
    response,
  };
}

function serviceProperties(
  resource: ReturnType<typeof terraformResources>[number],
  identity: Readonly<Record<string, unknown>>
): Readonly<Record<string, unknown>> {
  const id = deterministicId('run', identity).replace('_', '-');
  const configuredName = terraformString(resource.body, 'name');
  const name = configuredName?.includes('${') ? id : (configuredName ?? id);
  const minimum = terraformNumber(resource.body, 'min_instance_count') ?? 0;
  const maximum = terraformNumber(resource.body, 'max_instance_count') ?? 1;
  return {
    id,
    name,
    location: terraformString(resource.body, 'location') ?? 'asia-northeast1',
    minInstanceCount: minimum,
    maxInstanceCount: maximum,
    image: terraformString(resource.body, 'image') ?? 'gcr.io/cloudrun/hello',
    status: 'Ready',
    uri: `https://${id}.run.gcp.local`,
    responseStatus: 200,
    responseBody: 'Hello from TenkaCloud Simulator',
    sourceLine: resource.line,
  };
}

function cloudRunEndpoint(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ResourceDeclaration {
  const service = singleReadyDeploymentResource(
    world,
    command.deploymentId,
    PROVIDER,
    CLOUD_RUN_SERVICE,
    'Cloud Run'
  );
  if (service.properties['status'] !== 'Ready') {
    throw new CoreError('Conflict', 'Cloud Run endpoint is not ready');
  }
  if (
    typeof service.properties['uri'] !== 'string' ||
    !service.properties['uri'].trim()
  ) {
    throw new CoreError(
      'ValidationFailed',
      'Cloud Run endpoint projection is invalid'
    );
  }
  return service;
}

function requestCloudRun(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const request = providerHttpRequest(command.input);
  const service = cloudRunEndpoint(command, world);
  return commandResult(
    'GcpServiceRequestExecuted',
    providerHttpResponse(request, {
      statusCode: service.properties['responseStatus'],
      body: service.properties['responseBody'],
      contentType: 'text/plain; charset=utf-8',
    })
  );
}

export class GcpProvider implements ProviderModule {
  readonly provider: string;
  readonly engines: readonly string[];
  readonly capabilities: readonly ProviderCapability[];

  constructor() {
    this.provider = PROVIDER;
    this.engines = [ENGINE];
    this.capabilities = CAPABILITIES;
  }

  compile(input: ProviderCompileInput): ProviderTargetPlan {
    const resources = terraformResources(input.templateBody);
    const declarations: ResourceDeclaration[] = [];
    const requirements: CapabilityRequirement[] = [];
    for (const resource of resources) {
      if (
        resource.type !== CLOUD_RUN_SERVICE &&
        resource.type !== CLOUD_RUN_IAM_MEMBER
      ) {
        throw new CoreError(
          'UnsupportedCapability',
          `Terraform resource ${resource.type} is not supported`
        );
      }
      const identity = {
        problemId: input.problemId,
        targetId: input.targetId,
        type: resource.type,
        name: resource.name,
      };
      const properties =
        resource.type === CLOUD_RUN_SERVICE
          ? serviceProperties(resource, identity)
          : {
              id: deterministicId('run-iam', identity),
              role:
                terraformString(resource.body, 'role') ?? 'roles/run.invoker',
              member: terraformString(resource.body, 'member') ?? 'allUsers',
              sourceLine: resource.line,
            };
      const resourceId = Reflect.get(properties, 'id');
      if (typeof resourceId !== 'string') {
        throw new CoreError(
          'ValidationFailed',
          'compiled resource id is invalid'
        );
      }
      declarations.push({
        provider: PROVIDER,
        resourceType: resource.type,
        resourceId,
        properties,
      });
      requirements.push({
        provider: PROVIDER,
        engine: ENGINE,
        service: 'run',
        resourceType: resource.type,
        operation: 'lifecycle',
        fidelity:
          resource.type === CLOUD_RUN_SERVICE
            ? (['L0', 'L1', 'L2', 'L3', 'L4'] as const)
            : (['L0', 'L1', 'L2'] as const),
        source: { path: input.target.entry, line: resource.line },
      });
    }
    if (
      declarations.some(
        (resource) => resource.resourceType === CLOUD_RUN_SERVICE
      )
    ) {
      requirements.push({
        provider: PROVIDER,
        engine: ENGINE,
        service: 'http',
        resourceType: HTTP_ENDPOINT,
        operation: 'Probe',
        fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
        source: { path: input.target.entry },
      });
      requirements.push({
        provider: PROVIDER,
        engine: ENGINE,
        service: 'http',
        resourceType: HTTP_ENDPOINT,
        operation: 'Request',
        fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
        source: { path: input.target.entry },
      });
    }
    return {
      targetId: input.targetId,
      provider: PROVIDER,
      engine: ENGINE,
      requirements,
      resources: declarations,
    };
  }

  deploy(
    plan: ProviderTargetPlan,
    _world: ProviderWorldView
  ): ProviderDeploymentResult {
    const service = plan.resources.find(
      (resource) => resource.resourceType === CLOUD_RUN_SERVICE
    );
    const uri = service ? Reflect.get(service.properties, 'uri') : undefined;
    return {
      events: plan.resources.map((resource) => ({
        type: 'GcpResourceCreated',
        payload: {
          resourceId: resource.resourceId,
          resourceType: resource.resourceType,
        },
      })),
      resources: plan.resources,
      outputs: typeof uri === 'string' ? { GcpHelloUrl: uri } : {},
    };
  }

  reduce(
    command: ProviderCommandInput,
    world: ProviderWorldView
  ): ProviderCommandResult {
    switch (command.operation) {
      case 'GetService': {
        assertCommandIdentity(command, 'run', CLOUD_RUN_SERVICE);
        const resource = serviceResource(world, command.input['id']);
        return commandResult('GcpServiceRead', resource.properties);
      }
      case 'UpdateService': {
        assertCommandIdentity(command, 'run', CLOUD_RUN_SERVICE);
        const resource = serviceResource(world, command.input['id']);
        const updated = {
          ...resource,
          properties: patchService(resource.properties, command.input['patch']),
        };
        return commandResult('GcpServiceUpdated', updated.properties, [
          updated,
        ]);
      }
      case 'DeleteService': {
        assertCommandIdentity(command, 'run', CLOUD_RUN_SERVICE);
        const resource = serviceResource(world, command.input['id']);
        return commandResult(
          'GcpServiceDeleted',
          { id: resource.resourceId, deleted: true },
          [],
          [resource.resourceId]
        );
      }
      case 'Probe': {
        assertCommandIdentity(command, 'http', HTTP_ENDPOINT);
        const resource = serviceResource(world, command.input['id']);
        return commandResult('GcpServiceProbed', {
          status: Reflect.get(resource.properties, 'responseStatus'),
          body: Reflect.get(resource.properties, 'responseBody'),
          uri: Reflect.get(resource.properties, 'uri'),
        });
      }
      case 'Request':
        assertCommandIdentity(command, 'http', HTTP_ENDPOINT);
        return requestCloudRun(command, world);
      case 'GetIamPolicy': {
        assertCommandIdentity(command, 'run', CLOUD_RUN_IAM_MEMBER);
        const serviceId = command.input['serviceId'];
        if (serviceId !== undefined) {
          serviceResource(world, serviceId);
        }
        const members = world.resources
          .filter(
            (resource) =>
              resource.provider === PROVIDER &&
              resource.resourceType === CLOUD_RUN_IAM_MEMBER &&
              resource.status === 'ready' &&
              (serviceId === undefined ||
                resource.properties['serviceId'] === undefined ||
                resource.properties['serviceId'] === serviceId)
          )
          .map((resource) => resource.properties);
        return commandResult('GcpIamPolicyRead', { bindings: members });
      }
      case 'SetIamPolicy': {
        assertCommandIdentity(command, 'run', CLOUD_RUN_IAM_MEMBER);
        const role = requiredText(command.input['role'], 'IAM role');
        const member = requiredText(command.input['member'], 'IAM member');
        const serviceId = command.input['serviceId'];
        if (serviceId !== undefined) {
          serviceResource(world, serviceId);
        }
        const id = deterministicId('run-iam', {
          worldId: command.worldId,
          deploymentId: command.deploymentId,
          role,
          member,
          ...(serviceId === undefined ? {} : { serviceId }),
        });
        const resource: ResourceDeclaration = {
          provider: PROVIDER,
          resourceType: CLOUD_RUN_IAM_MEMBER,
          resourceId: id,
          properties: {
            id,
            role,
            member,
            ...(serviceId === undefined ? {} : { serviceId }),
          },
        };
        return commandResult('GcpIamPolicyUpdated', resource.properties, [
          resource,
        ]);
      }
      default:
        throw new CoreError(
          'UnsupportedCapability',
          `GCP operation ${command.operation} is not supported`
        );
    }
  }
}
