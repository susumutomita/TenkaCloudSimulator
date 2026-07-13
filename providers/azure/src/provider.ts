import {
  type CapabilityRequirement,
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
  BICEP_CONTAINER_APP,
  BICEP_MANAGED_ENVIRONMENT,
  BICEP_ROLE_ASSIGNMENT,
  compileBicep,
} from './bicep';

export const AZURE_CONTAINER_APP = BICEP_CONTAINER_APP;
export const AZURE_MANAGED_ENVIRONMENT = BICEP_MANAGED_ENVIRONMENT;
export const AZURE_ROLE_ASSIGNMENT = BICEP_ROLE_ASSIGNMENT;
export const HTTP_ENDPOINT = HTTP_ENDPOINT_RESOURCE;

const PROVIDER = 'azure';
const ENGINE = 'bicep';
const COMPILED_OUTPUTS = '$bicepOutputs';

const CAPABILITIES: readonly ProviderCapability[] = [
  {
    capabilityId: 'azure.bicep.deploy',
    provider: PROVIDER,
    engine: ENGINE,
    service: ENGINE,
    resourceType: '*',
    operation: 'deploy',
    fidelity: ['L0', 'L1'],
  },
  {
    capabilityId: 'azure.containerapps.lifecycle',
    provider: PROVIDER,
    engine: ENGINE,
    service: 'containerapps',
    resourceType: AZURE_CONTAINER_APP,
    operation: 'lifecycle',
    fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
  },
  {
    capabilityId: 'azure.containerapps.managed-environment.lifecycle',
    provider: PROVIDER,
    engine: ENGINE,
    service: 'containerapps',
    resourceType: AZURE_MANAGED_ENVIRONMENT,
    operation: 'lifecycle',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    capabilityId: 'azure.authorization.role-assignment.lifecycle',
    provider: PROVIDER,
    engine: ENGINE,
    service: 'authorization',
    resourceType: AZURE_ROLE_ASSIGNMENT,
    operation: 'lifecycle',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    capabilityId: 'azure.http.probe',
    provider: PROVIDER,
    engine: ENGINE,
    service: 'http',
    resourceType: HTTP_ENDPOINT,
    operation: 'Probe',
    fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
  },
  {
    capabilityId: 'azure.http.request',
    provider: PROVIDER,
    engine: ENGINE,
    service: 'http',
    resourceType: HTTP_ENDPOINT,
    operation: 'Request',
    fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
  },
  ...['GetContainerApp', 'UpdateContainerApp', 'DeleteContainerApp'].map(
    (operation): ProviderCapability => ({
      capabilityId: `azure.containerapps.${operation}`,
      provider: PROVIDER,
      engine: ENGINE,
      service: 'containerapps',
      resourceType: AZURE_CONTAINER_APP,
      operation,
      fidelity: ['L0', 'L1', 'L2'],
    })
  ),
  ...['GetRoleAssignment', 'SetRoleAssignment'].map(
    (operation): ProviderCapability => ({
      capabilityId: `azure.authorization.${operation}`,
      provider: PROVIDER,
      engine: ENGINE,
      service: 'authorization',
      resourceType: AZURE_ROLE_ASSIGNMENT,
      operation,
      fidelity: ['L0', 'L1', 'L2'],
    })
  ),
];

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

function resource(
  world: ProviderWorldView,
  resourceType: string,
  id: unknown,
  label: string
): ResourceDeclaration {
  const resourceId = requiredText(id, `${label} id`);
  const found = world.resources.find(
    (candidate) =>
      candidate.provider === PROVIDER &&
      candidate.resourceType === resourceType &&
      candidate.resourceId === resourceId &&
      candidate.status === 'ready'
  );
  if (!found) throw new CoreError('NotFound', `${label} does not exist`);
  return {
    provider: found.provider,
    resourceType: found.resourceType,
    resourceId: found.resourceId,
    properties: found.properties,
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
      `Azure operation ${command.operation} does not support ${command.service}/${command.resourceType}`
    );
  }
}

function commandResult(
  type: string,
  response: Readonly<Record<string, unknown>>,
  resources: readonly ResourceDeclaration[] = [],
  deletedResourceIds: readonly string[] = [],
  outputs: Readonly<Record<string, string>> = {}
): ProviderCommandResult {
  return {
    events: [
      {
        type,
        payload: {
          operation: type,
          resourceIds: resources.map((item) => item.resourceId),
          deletedResourceIds,
        },
      },
    ],
    resources,
    deletedResourceIds,
    outputs,
    response,
  };
}

function outputRecord(value: unknown): Readonly<Record<string, string>> {
  const object = objectValue(value, 'compiled Bicep outputs');
  const outputs: Record<string, string> = {};
  for (const [key, output] of Object.entries(object)) {
    if (typeof output !== 'string') {
      throw new CoreError(
        'ValidationFailed',
        `compiled Bicep output ${key} must be a string`
      );
    }
    outputs[key] = output;
  }
  return outputs;
}

function runtimeProperties(
  properties: Readonly<Record<string, unknown>>
): Readonly<Record<string, unknown>> {
  return Object.fromEntries(
    Object.entries(properties).filter(([key]) => key !== COMPILED_OUTPUTS)
  );
}

function deploymentOutputs(
  plan: ProviderTargetPlan
): Readonly<Record<string, string>> {
  const metadata = plan.resources
    .map((item) => item.properties[COMPILED_OUTPUTS])
    .find((value) => value !== undefined);
  return metadata === undefined ? {} : outputRecord(metadata);
}

function integerPatch(
  patch: Readonly<Record<string, unknown>>,
  key: string,
  fallback: unknown,
  minimum: number,
  maximum: number
): number {
  const value = patch[key] ?? fallback;
  if (
    typeof value !== 'number' ||
    !Number.isInteger(value) ||
    value < minimum ||
    value > maximum
  ) {
    throw new CoreError(
      'ValidationFailed',
      `${key} must be an integer between ${minimum} and ${maximum}`
    );
  }
  return value;
}

function patchContainerApp(
  current: Readonly<Record<string, unknown>>,
  value: unknown
): Readonly<Record<string, unknown>> {
  const patch = objectValue(value, 'Container App patch');
  const allowed = new Set([
    'image',
    'minReplicas',
    'maxReplicas',
    'targetPort',
    'responseStatus',
    'responseBody',
  ]);
  const unsupported = Object.keys(patch).find((key) => !allowed.has(key));
  if (unsupported) {
    throw new CoreError(
      'ValidationFailed',
      `Container App patch field ${unsupported} is not supported`
    );
  }
  const minReplicas = integerPatch(
    patch,
    'minReplicas',
    current['minReplicas'],
    0,
    100
  );
  const maxReplicas = integerPatch(
    patch,
    'maxReplicas',
    current['maxReplicas'],
    1,
    100
  );
  if (minReplicas > maxReplicas) {
    throw new CoreError(
      'ValidationFailed',
      'minReplicas must not exceed maxReplicas'
    );
  }
  const targetPort = integerPatch(
    patch,
    'targetPort',
    current['targetPort'],
    1,
    65_535
  );
  const responseStatus = integerPatch(
    patch,
    'responseStatus',
    current['responseStatus'],
    200,
    599
  );
  const image = patch['image'] ?? current['image'];
  const responseBody = patch['responseBody'] ?? current['responseBody'];
  requiredText(image, 'Container App image');
  if (
    typeof responseBody !== 'string' ||
    new TextEncoder().encode(responseBody).byteLength >
      MAX_PROVIDER_HTTP_BODY_BYTES
  ) {
    throw new CoreError(
      'ValidationFailed',
      'Container App responseBody is invalid'
    );
  }
  if (
    (responseStatus === 204 ||
      responseStatus === 205 ||
      responseStatus === 304) &&
    responseBody.length > 0
  ) {
    throw new CoreError(
      'ValidationFailed',
      `Container App response status ${responseStatus} forbids a body`
    );
  }
  return {
    ...current,
    ...patch,
    image,
    responseBody,
    minReplicas,
    maxReplicas,
    targetPort,
    responseStatus,
    status: 'Running',
  };
}

function roleAssignmentIdentity(
  command: ProviderCommandInput,
  scopeId: string,
  roleDefinitionId: string,
  principalId: string
): { readonly id: string; readonly name: string } {
  const requestedId = command.input['id'];
  if (requestedId === undefined) {
    const name = deterministicId('azure-role-assignment', {
      worldId: command.worldId,
      deploymentId: command.deploymentId,
      scopeId,
      roleDefinitionId,
      principalId,
    });
    return {
      id: `${scopeId}/providers/${AZURE_ROLE_ASSIGNMENT}/${name}`,
      name,
    };
  }
  const id = requiredText(requestedId, 'role assignment id');
  const prefix = `${scopeId}/providers/${AZURE_ROLE_ASSIGNMENT}/`;
  const name = id.slice(prefix.length);
  if (
    !id.toLowerCase().startsWith(prefix.toLowerCase()) ||
    !/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(name)
  ) {
    throw new CoreError(
      'ValidationFailed',
      'role assignment id must be a direct child of scopeId'
    );
  }
  return { id, name };
}

function setRoleAssignment(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const scopeId = requiredText(
    command.input['scopeId'],
    'role assignment scopeId'
  );
  resource(world, AZURE_CONTAINER_APP, scopeId, 'Container App');
  const roleDefinitionId = requiredText(
    command.input['roleDefinitionId'],
    'roleDefinitionId'
  );
  const principalId = requiredText(command.input['principalId'], 'principalId');
  const { id, name } = roleAssignmentIdentity(
    command,
    scopeId,
    roleDefinitionId,
    principalId
  );
  const assignment: ResourceDeclaration = {
    provider: PROVIDER,
    resourceType: AZURE_ROLE_ASSIGNMENT,
    resourceId: id,
    properties: {
      id,
      name,
      scopeId,
      dependencies: [scopeId],
      roleDefinitionId,
      principalId,
      status: 'Assigned',
    },
  };
  return commandResult(
    'AzureRoleAssignmentSet',
    assignment.properties,
    [assignment],
    [],
    { RoleAssignmentId: id }
  );
}

function containerAppEndpoint(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ResourceDeclaration {
  const app = singleReadyDeploymentResource(
    world,
    command.deploymentId,
    PROVIDER,
    AZURE_CONTAINER_APP,
    'Container App'
  );
  if (app.properties['status'] !== 'Running') {
    throw new CoreError('Conflict', 'Container App endpoint is not running');
  }
  if (app.properties['external'] !== true) {
    throw new CoreError(
      'NotFound',
      'Container App external endpoint does not exist'
    );
  }
  if (
    typeof app.properties['fqdn'] !== 'string' ||
    !app.properties['fqdn'].trim()
  ) {
    throw new CoreError(
      'ValidationFailed',
      'Container App endpoint projection is invalid'
    );
  }
  return app;
}

function requestContainerApp(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const request = providerHttpRequest(command.input);
  const app = containerAppEndpoint(command, world);
  return commandResult(
    'AzureContainerAppRequestExecuted',
    providerHttpResponse(request, {
      statusCode: app.properties['responseStatus'],
      body: app.properties['responseBody'],
      contentType: 'text/plain; charset=utf-8',
    })
  );
}

export class AzureProvider implements ProviderModule {
  readonly provider: string;
  readonly engines: readonly string[];
  readonly capabilities: readonly ProviderCapability[];

  constructor() {
    this.provider = PROVIDER;
    this.engines = [ENGINE];
    this.capabilities = CAPABILITIES;
  }

  compile(input: ProviderCompileInput): ProviderTargetPlan {
    const compilation = compileBicep(input.templateBody, {
      problemId: input.problemId,
      targetId: input.targetId,
    });
    const requirements: CapabilityRequirement[] = [];
    const resources = compilation.resources.map((item, index) => {
      requirements.push({
        provider: PROVIDER,
        engine: ENGINE,
        service:
          item.type === AZURE_ROLE_ASSIGNMENT
            ? 'authorization'
            : 'containerapps',
        resourceType: item.type,
        operation: 'lifecycle',
        fidelity:
          item.type === AZURE_CONTAINER_APP
            ? (['L0', 'L1', 'L2', 'L3', 'L4'] as const)
            : (['L0', 'L1', 'L2'] as const),
        source: { path: input.target.entry, line: item.line },
      });
      return {
        provider: PROVIDER,
        resourceType: item.type,
        resourceId: item.resourceId,
        properties: {
          ...item.properties,
          ...(index === 0 ? { [COMPILED_OUTPUTS]: compilation.outputs } : {}),
        },
      };
    });
    if (resources.some((item) => item.resourceType === AZURE_CONTAINER_APP)) {
      requirements.push({
        provider: PROVIDER,
        engine: ENGINE,
        service: 'http',
        resourceType: HTTP_ENDPOINT,
        operation: 'Probe',
        fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
        source: { path: input.target.entry },
      });
    }
    if (
      resources.some(
        (item) =>
          item.resourceType === AZURE_CONTAINER_APP &&
          Reflect.get(item.properties, 'external') === true
      )
    ) {
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
      resources,
    };
  }

  deploy(
    plan: ProviderTargetPlan,
    _world: ProviderWorldView
  ): ProviderDeploymentResult {
    if (plan.resources.length === 0) {
      throw new CoreError('ValidationFailed', 'Azure deployment plan is empty');
    }
    const resources = plan.resources.map((item) => ({
      ...item,
      properties: runtimeProperties(item.properties),
    }));
    return {
      events: resources.map((item) => ({
        type: 'AzureResourceCreated',
        payload: {
          resourceId: item.resourceId,
          resourceType: item.resourceType,
          dependencies: item.properties['dependencies'] ?? [],
        },
      })),
      resources,
      outputs: deploymentOutputs(plan),
    };
  }

  reduce(
    command: ProviderCommandInput,
    world: ProviderWorldView
  ): ProviderCommandResult {
    switch (command.operation) {
      case 'GetContainerApp': {
        assertCommandIdentity(command, 'containerapps', AZURE_CONTAINER_APP);
        const app = resource(
          world,
          AZURE_CONTAINER_APP,
          command.input['id'],
          'Container App'
        );
        return commandResult('AzureContainerAppRead', app.properties);
      }
      case 'UpdateContainerApp': {
        assertCommandIdentity(command, 'containerapps', AZURE_CONTAINER_APP);
        const app = resource(
          world,
          AZURE_CONTAINER_APP,
          command.input['id'],
          'Container App'
        );
        const updated: ResourceDeclaration = {
          ...app,
          properties: patchContainerApp(app.properties, command.input['patch']),
        };
        return commandResult('AzureContainerAppUpdated', updated.properties, [
          updated,
        ]);
      }
      case 'DeleteContainerApp': {
        assertCommandIdentity(command, 'containerapps', AZURE_CONTAINER_APP);
        const app = resource(
          world,
          AZURE_CONTAINER_APP,
          command.input['id'],
          'Container App'
        );
        return commandResult(
          'AzureContainerAppDeleted',
          { id: app.resourceId, deleted: true },
          [],
          [app.resourceId]
        );
      }
      case 'GetRoleAssignment': {
        assertCommandIdentity(command, 'authorization', AZURE_ROLE_ASSIGNMENT);
        const assignment = resource(
          world,
          AZURE_ROLE_ASSIGNMENT,
          command.input['id'],
          'role assignment'
        );
        return commandResult('AzureRoleAssignmentRead', assignment.properties);
      }
      case 'SetRoleAssignment':
        assertCommandIdentity(command, 'authorization', AZURE_ROLE_ASSIGNMENT);
        return setRoleAssignment(command, world);
      case 'Probe': {
        assertCommandIdentity(command, 'http', HTTP_ENDPOINT);
        const app = resource(
          world,
          AZURE_CONTAINER_APP,
          command.input['id'],
          'Container App'
        );
        return commandResult('AzureContainerAppProbed', {
          status: app.properties['responseStatus'],
          body: app.properties['responseBody'],
          url: `https://${app.properties['fqdn']}`,
        });
      }
      case 'Request':
        assertCommandIdentity(command, 'http', HTTP_ENDPOINT);
        return requestContainerApp(command, world);
      default:
        throw new CoreError(
          'UnsupportedCapability',
          `Azure operation ${command.operation} is not supported`
        );
    }
  }
}
