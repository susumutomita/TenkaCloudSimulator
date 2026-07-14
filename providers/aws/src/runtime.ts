import {
  CoreError,
  contentHash,
  deterministicId,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
  type ResourceDeclaration,
  type ResourceRecord,
  type WorkloadDeclaration,
} from '@tenkacloud/simulator-core';
import {
  APP_RUNNER_SERVICE_RESOURCE,
  declaration,
  ECS_SERVICE_RESOURCE,
  RUNTIME_ENDPOINT_RESOURCE,
} from './model';
import {
  resourcesForDeployment,
  result,
  stateObject,
  storedProperties,
  updateStoredResource,
} from './state';
import {
  booleanValue,
  objectValue,
  optionalString,
  stringValue,
} from './value';

export interface RuntimeEndpointCompileInput {
  readonly metadata?: unknown;
  readonly outputs: Readonly<Record<string, string>>;
  readonly problemId: string;
  readonly targetId: string;
  readonly stackId: string;
  readonly stackName: string;
}

const RUNTIME_WORKLOAD_RESOURCE = 'Runtime::Workload';
const LOOPBACK_HOSTS = new Set(['127.0.0.1', 'localhost', '::1']);
const REVIEWED_WORKLOAD_FIELDS = new Set([
  'id',
  'targetId',
  'resourceRef',
  'image',
  'command',
  'containerPort',
  'healthPath',
]);
const MANAGED_PLATFORM_BY_RESOURCE_TYPE: Readonly<Record<string, string>> = {
  'AWS::Lambda::Function': 'lambda',
  [ECS_SERVICE_RESOURCE]: 'ecs',
  [APP_RUNNER_SERVICE_RESOURCE]: 'apprunner',
};

interface ManagedPlacement extends Readonly<Record<string, unknown>> {
  readonly DeploymentId: string;
  readonly TargetId: string;
  readonly Slot: string;
  readonly EffectiveUrl: string;
  readonly ReviewedWorkloadId: string;
  readonly ReviewedArtifactHash: string;
  readonly ManagedResourceId: string;
  readonly ManagedResourceType: string;
  readonly VerifiedPlatform: string;
}

function endpointUrl(base: string, appendPath: string | undefined): string {
  if (!base || appendPath === undefined) return base;
  let parsed: URL;
  try {
    parsed = new URL(base);
  } catch {
    throw new CoreError(
      'ValidationFailed',
      'runtime endpoint CloudFormation output must be an HTTP URL'
    );
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    throw new CoreError(
      'ValidationFailed',
      'runtime endpoint CloudFormation output must use HTTP or HTTPS'
    );
  }
  return new URL(
    appendPath.replace(/^\//, ''),
    parsed.toString().endsWith('/')
      ? parsed.toString()
      : `${parsed.toString()}/`
  ).toString();
}

export function compileRuntimeEndpoints(
  input: RuntimeEndpointCompileInput
): readonly ResourceDeclaration[] {
  if (input.metadata === undefined) return [];
  const metadata = objectValue(input.metadata, 'deployment metadata');
  const value = metadata['endpoints'];
  if (value === undefined) return [];
  if (!Array.isArray(value)) {
    throw new CoreError(
      'ValidationFailed',
      'deployment metadata endpoints must be an array'
    );
  }
  const slots = new Set<string>();
  return value.map((entry, index) => {
    const endpoint = objectValue(entry, `metadata.endpoints[${index}]`);
    const slot = stringValue(
      endpoint['slot'],
      `metadata.endpoints[${index}].slot`
    );
    if (slots.has(slot)) {
      throw new CoreError(
        'ValidationFailed',
        `runtime endpoint slot ${slot} is duplicated`
      );
    }
    slots.add(slot);
    const defaultValue = objectValue(
      endpoint['default'],
      `metadata.endpoints[${index}].default`
    );
    if (defaultValue['from'] !== 'cfn-output') {
      throw new CoreError(
        'UnsupportedCapability',
        `runtime endpoint slot ${slot} source is not supported`
      );
    }
    const outputKey = stringValue(
      defaultValue['key'],
      `metadata.endpoints[${index}].default.key`
    );
    const output = input.outputs[outputKey];
    if (output === undefined) {
      throw new CoreError(
        'ValidationFailed',
        `runtime endpoint slot ${slot} references missing output ${outputKey}`
      );
    }
    const appendPath = optionalString(
      defaultValue['appendPath'],
      `metadata.endpoints[${index}].default.appendPath`
    );
    const overridable = booleanValue(
      endpoint['overridable'] ?? false,
      `metadata.endpoints[${index}].overridable`
    );
    const resourceId = deterministicId('runtime-endpoint', {
      problemId: input.problemId,
      targetId: input.targetId,
      slot,
    });
    return declaration({
      resourceType: RUNTIME_ENDPOINT_RESOURCE,
      resourceId,
      properties: {
        logicalId: `RuntimeEndpoint.${slot}`,
        physicalId: `${input.stackId}:${slot}`,
        refValue: slot,
        dependsOn: [input.stackName],
        attributes: {},
        templateProperties: {
          Slot: slot,
          OutputKey: outputKey,
          ...(appendPath === undefined ? {} : { AppendPath: appendPath }),
          Overridable: overridable,
        },
        status: 'CREATE_PENDING',
        Slot: slot,
        ProblemId: input.problemId,
        TargetId: input.targetId,
        OutputKey: outputKey,
        DefaultUrl: endpointUrl(output, appendPath),
        Overridable: overridable,
      },
    });
  });
}

function validatedOverride(value: unknown): string {
  const text = stringValue(value, 'OverrideUrl');
  if (text.length > 2048) {
    throw new CoreError('ValidationFailed', 'OverrideUrl is too long');
  }
  let parsed: URL;
  try {
    parsed = new URL(text);
  } catch {
    throw new CoreError('ValidationFailed', 'OverrideUrl must be a URL');
  }
  if (
    (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') ||
    parsed.username ||
    parsed.password ||
    parsed.hash
  ) {
    throw new CoreError(
      'ValidationFailed',
      'OverrideUrl must be a credential-free HTTP URL without a fragment'
    );
  }
  return parsed.toString();
}

function endpointResponse(
  resource: ReturnType<typeof findEndpoint>,
  url: string,
  source = 'override'
): Readonly<Record<string, unknown>> {
  const properties = storedProperties(resource);
  return {
    Slot: properties.refValue,
    Url: url,
    Source: source,
    Overridable: properties.templateProperties['Overridable'] === true,
    OutputKey: properties.templateProperties['OutputKey'],
  };
}

function placementValue(
  resource: ResourceRecord
): ManagedPlacement | undefined {
  const raw = resource.properties['ManagedPlacement'];
  if (raw === undefined) return undefined;
  const value = objectValue(raw, 'ManagedPlacement');
  const fields = new Set([
    'DeploymentId',
    'TargetId',
    'Slot',
    'EffectiveUrl',
    'ReviewedWorkloadId',
    'ReviewedArtifactHash',
    'ManagedResourceId',
    'ManagedResourceType',
    'VerifiedPlatform',
  ]);
  if (
    Object.keys(value).length !== fields.size ||
    Object.keys(value).some((field) => !fields.has(field))
  ) {
    throw new CoreError(
      'ValidationFailed',
      'managed endpoint placement is invalid'
    );
  }
  return {
    DeploymentId: stringValue(value['DeploymentId'], 'DeploymentId'),
    TargetId: stringValue(value['TargetId'], 'TargetId'),
    Slot: stringValue(value['Slot'], 'Slot'),
    EffectiveUrl: stringValue(value['EffectiveUrl'], 'EffectiveUrl'),
    ReviewedWorkloadId: stringValue(
      value['ReviewedWorkloadId'],
      'ReviewedWorkloadId'
    ),
    ReviewedArtifactHash: stringValue(
      value['ReviewedArtifactHash'],
      'ReviewedArtifactHash'
    ),
    ManagedResourceId: stringValue(
      value['ManagedResourceId'],
      'ManagedResourceId'
    ),
    ManagedResourceType: stringValue(
      value['ManagedResourceType'],
      'ManagedResourceType'
    ),
    VerifiedPlatform: stringValue(
      value['VerifiedPlatform'],
      'VerifiedPlatform'
    ),
  };
}

function managedResource(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  resourceId: string
): { readonly resource: ResourceRecord; readonly platform: string } {
  const targetId = stringValue(command.targetId, 'command targetId');
  const resource = world.resources.find(
    (candidate) =>
      candidate.provider === 'aws' &&
      candidate.deploymentId === command.deploymentId &&
      candidate.targetId === targetId &&
      candidate.resourceId === resourceId
  );
  if (!resource) {
    throw new CoreError('NotFound', 'managed resource does not exist');
  }
  if (resource.status !== 'ready') {
    throw new CoreError('Conflict', 'managed resource is not ready');
  }
  if (resource.properties['ParticipantCreated'] !== true) {
    throw new CoreError(
      'Conflict',
      'managed resource was not participant-created'
    );
  }
  if (resource.properties['EligibleManagedPlacement'] !== true) {
    throw new CoreError(
      'Conflict',
      'managed resource is not eligible for endpoint placement'
    );
  }
  const platform = MANAGED_PLATFORM_BY_RESOURCE_TYPE[resource.resourceType];
  if (!platform) {
    throw new CoreError(
      'UnsupportedCapability',
      'managed resource type is not supported'
    );
  }
  return { resource, platform };
}

export interface ReviewedWorkloadPlacement {
  readonly endpoint: ResourceRecord;
  readonly url: string;
  readonly workloadId: string;
  readonly artifactHash: string;
  readonly containerPort: number;
}

function reviewedWorkloadDeclaration(value: unknown): WorkloadDeclaration {
  const declaration = objectValue(value, 'reviewed workload declaration');
  const command = declaration['command'];
  const containerPort = declaration['containerPort'];
  const healthPath = declaration['healthPath'];
  if (
    Object.keys(declaration).some(
      (field) => !REVIEWED_WORKLOAD_FIELDS.has(field)
    ) ||
    (command !== undefined &&
      (!Array.isArray(command) ||
        command.length < 1 ||
        command.length > 32 ||
        command.some(
          (argument) =>
            typeof argument !== 'string' ||
            argument.length < 1 ||
            argument.length > 512 ||
            argument.includes('\u0000')
        ))) ||
    typeof containerPort !== 'number' ||
    !Number.isSafeInteger(containerPort) ||
    containerPort < 1024 ||
    containerPort > 65_535 ||
    (healthPath !== undefined &&
      (typeof healthPath !== 'string' ||
        healthPath.length > 256 ||
        !/^\/(?!\/)[^\s?#]*$/.test(healthPath)))
  ) {
    throw new CoreError(
      'ValidationFailed',
      'reviewed workload declaration is invalid'
    );
  }
  const reviewed: WorkloadDeclaration = {
    id: stringValue(declaration['id'], 'reviewed workload id'),
    targetId: stringValue(
      declaration['targetId'],
      'reviewed workload target id'
    ),
    resourceRef: stringValue(
      declaration['resourceRef'],
      'reviewed workload resource ref'
    ),
    image: stringValue(declaration['image'], 'reviewed workload image'),
    ...(command === undefined ? {} : { command }),
    containerPort,
    ...(healthPath === undefined ? {} : { healthPath }),
  };
  if (
    !/^[a-z][a-z0-9-]{0,63}$/.test(reviewed.id) ||
    !/^(default|[a-z][a-z0-9-]{0,31})$/.test(reviewed.targetId) ||
    reviewed.resourceRef.length > 256 ||
    reviewed.resourceRef.includes('\u0000') ||
    !/^[a-z0-9][a-z0-9./_-]*@sha256:[a-f0-9]{64}$/.test(reviewed.image)
  ) {
    throw new CoreError(
      'ValidationFailed',
      'reviewed workload declaration is invalid'
    );
  }
  return reviewed;
}

function reviewedWorkloadPlacement(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  endpoint: ResourceRecord
): ReviewedWorkloadPlacement {
  const endpointProperties = storedProperties(endpoint);
  const commandTargetId = stringValue(command.targetId, 'command targetId');
  const outputKey = stringValue(
    endpointProperties.templateProperties['OutputKey'],
    'runtime endpoint OutputKey'
  );
  const matches = world.resources.filter((candidate) => {
    if (
      candidate.provider !== 'runtime' ||
      candidate.resourceType !== RUNTIME_WORKLOAD_RESOURCE ||
      candidate.deploymentId !== command.deploymentId ||
      candidate.targetId !== commandTargetId ||
      candidate.status !== 'ready'
    ) {
      return false;
    }
    const declaration = reviewedWorkloadDeclaration(
      candidate.properties['declaration']
    );
    return declaration.resourceRef === outputKey;
  });
  const [workload] = matches;
  if (!workload || matches.length !== 1) {
    throw new CoreError(
      'Conflict',
      'reviewed workload endpoint is unavailable'
    );
  }
  const materialization = objectValue(
    workload.properties['materialization'],
    'reviewed workload materialization'
  );
  const origin = stringValue(
    materialization['endpoint'],
    'reviewed workload endpoint'
  );
  let parsed: URL;
  try {
    parsed = new URL(origin);
  } catch {
    throw new CoreError(
      'ValidationFailed',
      'reviewed workload endpoint is invalid'
    );
  }
  if (
    parsed.protocol !== 'http:' ||
    !LOOPBACK_HOSTS.has(parsed.hostname.replace(/^\[|\]$/g, '')) ||
    !parsed.port ||
    parsed.username ||
    parsed.password ||
    parsed.pathname !== '/' ||
    parsed.search ||
    parsed.hash ||
    parsed.origin !== origin.replace(/\/$/, '')
  ) {
    throw new CoreError(
      'ValidationFailed',
      'reviewed workload endpoint is invalid'
    );
  }
  const appendPath = optionalString(
    endpointProperties.templateProperties['AppendPath'],
    'runtime endpoint AppendPath'
  );
  const declaration = reviewedWorkloadDeclaration(
    workload.properties['declaration']
  );
  if (
    declaration.targetId !== commandTargetId ||
    declaration.targetId !== endpoint.properties['TargetId'] ||
    declaration.resourceRef !== outputKey
  ) {
    throw new CoreError(
      'ValidationFailed',
      'reviewed workload declaration is invalid'
    );
  }
  return {
    endpoint,
    url: endpointUrl(parsed.origin, appendPath),
    workloadId: workload.resourceId,
    artifactHash: contentHash({
      declaration,
      workloadResourceId: workload.resourceId,
    }),
    containerPort: declaration.containerPort,
  };
}

export function reviewedWorkloadForSlot(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  slot: string,
  targetId?: string
): ReviewedWorkloadPlacement {
  return reviewedWorkloadPlacement(
    command,
    world,
    findEndpoint(command, world, slot, targetId)
  );
}

function assertManagedResourceMatchesReviewed(
  resource: ResourceRecord,
  slot: string,
  reviewed: ReviewedWorkloadPlacement
): void {
  if (
    resource.properties['PlacementSlot'] !== slot ||
    resource.properties['ReviewedWorkloadId'] !== reviewed.workloadId ||
    resource.properties['ReviewedArtifactHash'] !== reviewed.artifactHash
  ) {
    throw new CoreError(
      'Conflict',
      'managed resource does not match the reviewed workload artifact'
    );
  }
}

function currentPlacement(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  endpoint: ResourceRecord
): ManagedPlacement {
  const stored = placementValue(endpoint);
  if (!stored) {
    throw new CoreError(
      'NotFound',
      'runtime endpoint placement does not exist'
    );
  }
  const endpointProperties = storedProperties(endpoint);
  const slot = endpointProperties.refValue;
  const targetId = stringValue(endpointProperties['TargetId'], 'TargetId');
  const { resource, platform } = managedResource(
    command,
    world,
    stored.ManagedResourceId
  );
  const reviewed = reviewedWorkloadPlacement(command, world, endpoint);
  assertManagedResourceMatchesReviewed(resource, slot, reviewed);
  const expected: ManagedPlacement = {
    DeploymentId: command.deploymentId,
    TargetId: targetId,
    Slot: slot,
    EffectiveUrl: reviewed.url,
    ReviewedWorkloadId: reviewed.workloadId,
    ReviewedArtifactHash: reviewed.artifactHash,
    ManagedResourceId: resource.resourceId,
    ManagedResourceType: resource.resourceType,
    VerifiedPlatform: platform,
  };
  if (contentHash(stored) !== contentHash(expected)) {
    throw new CoreError(
      'Conflict',
      'managed endpoint placement no longer matches the resource graph'
    );
  }
  return expected;
}

function endpointImmutableShape(
  resource: Pick<
    ResourceDeclaration,
    'resourceId' | 'resourceType' | 'properties'
  >
): Readonly<Record<string, unknown>> {
  const properties = storedProperties(resource);
  return {
    resourceId: resource.resourceId,
    resourceType: resource.resourceType,
    logicalId: properties.logicalId,
    physicalId: properties.physicalId,
    refValue: properties.refValue,
    dependsOn: properties.dependsOn,
    templateProperties: properties.templateProperties,
    Slot: properties['Slot'],
    ProblemId: properties['ProblemId'],
    TargetId: properties['TargetId'],
    OutputKey: properties['OutputKey'],
    Overridable: properties['Overridable'],
  };
}

export function preserveBoundRuntimeEndpoint(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  existing: ResourceRecord,
  replacement: ResourceDeclaration
): ResourceDeclaration {
  if (
    existing.resourceType !== RUNTIME_ENDPOINT_RESOURCE ||
    replacement.resourceType !== RUNTIME_ENDPOINT_RESOURCE ||
    contentHash(endpointImmutableShape(existing)) !==
      contentHash(endpointImmutableShape(replacement))
  ) {
    throw new CoreError(
      'Conflict',
      'bound runtime endpoint immutable shape cannot be changed'
    );
  }
  const placement = currentPlacement(command, world, existing);
  return {
    ...replacement,
    properties: {
      ...replacement.properties,
      ManagedPlacement: placement,
      state: stateObject(storedProperties(existing)),
    },
  };
}

function bindManagedResource(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  endpoint: ResourceRecord
): ProviderCommandResult {
  if (placementValue(endpoint)) {
    throw new CoreError('Conflict', 'runtime endpoint is already bound');
  }
  const endpointProperties = storedProperties(endpoint);
  if (endpointProperties.templateProperties['Overridable'] !== true) {
    throw new CoreError('Conflict', 'runtime endpoint is not overridable');
  }
  const resourceId = stringValue(
    command.input['ManagedResourceId'],
    'ManagedResourceId'
  );
  for (const candidate of world.resources) {
    if (
      candidate.provider === 'aws' &&
      candidate.deploymentId === command.deploymentId &&
      candidate.targetId ===
        stringValue(command.targetId, 'command targetId') &&
      candidate.resourceType === RUNTIME_ENDPOINT_RESOURCE &&
      placementValue(candidate)?.ManagedResourceId === resourceId
    ) {
      throw new CoreError('Conflict', 'managed resource is already bound');
    }
  }
  const { resource, platform } = managedResource(command, world, resourceId);
  const reviewed = reviewedWorkloadPlacement(command, world, endpoint);
  assertManagedResourceMatchesReviewed(
    resource,
    endpointProperties.refValue,
    reviewed
  );
  const placement: ManagedPlacement = {
    DeploymentId: command.deploymentId,
    TargetId: stringValue(endpointProperties['TargetId'], 'TargetId'),
    Slot: endpointProperties.refValue,
    EffectiveUrl: reviewed.url,
    ReviewedWorkloadId: reviewed.workloadId,
    ReviewedArtifactHash: reviewed.artifactHash,
    ManagedResourceId: resource.resourceId,
    ManagedResourceType: resource.resourceType,
    VerifiedPlatform: platform,
  };
  return result('AwsRuntimeEndpointManagedResourceBound', placement, [
    updateStoredResource(endpoint, {
      ManagedPlacement: placement,
      state: {},
    }),
  ]);
}

function findEndpoint(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  slot: string,
  targetId: string | undefined
) {
  const commandTargetId = stringValue(command.targetId, 'command targetId');
  if (targetId !== undefined && targetId !== commandTargetId) {
    throw new CoreError(
      'ValidationFailed',
      'TargetId must match the command target'
    );
  }
  const endpoint = resourcesForDeployment(
    world,
    command.deploymentId,
    RUNTIME_ENDPOINT_RESOURCE
  ).find(
    (resource) =>
      resource.properties['Slot'] === slot &&
      resource.targetId === commandTargetId &&
      resource.properties['TargetId'] === commandTargetId
  );
  if (!endpoint) {
    throw new CoreError('NotFound', 'runtime endpoint does not exist');
  }
  return endpoint;
}

export function reduceRuntime(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  if (
    ![
      'BindManagedResource',
      'DescribeEndpointPlacement',
      'ResolveEndpoint',
    ].includes(command.operation)
  ) {
    throw new CoreError(
      'UnsupportedCapability',
      `Runtime operation ${command.operation} is not supported`
    );
  }
  const slot = stringValue(command.input['Slot'], 'Slot');
  const targetId = optionalString(command.input['TargetId'], 'TargetId');
  const endpoint = findEndpoint(command, world, slot, targetId);
  if (command.operation === 'BindManagedResource') {
    const allowed = new Set(['ManagedResourceId', 'Slot', 'TargetId']);
    const unknown = Object.keys(command.input).find(
      (field) => !allowed.has(field)
    );
    if (unknown) {
      throw new CoreError(
        'UnsupportedCapability',
        `Runtime BindManagedResource field ${unknown} is not supported`
      );
    }
    return bindManagedResource(command, world, endpoint);
  }
  if (command.operation === 'DescribeEndpointPlacement') {
    const allowed = new Set(['Slot', 'TargetId']);
    const unknown = Object.keys(command.input).find(
      (field) => !allowed.has(field)
    );
    if (unknown) {
      throw new CoreError(
        'UnsupportedCapability',
        `Runtime DescribeEndpointPlacement field ${unknown} is not supported`
      );
    }
    return {
      events: [],
      resources: [],
      deletedResourceIds: [],
      outputs: {},
      response: currentPlacement(command, world, endpoint),
    };
  }
  const properties = storedProperties(endpoint);
  const state = stateObject(properties);
  if (command.input['OverrideUrl'] !== undefined) {
    if (placementValue(endpoint)) {
      throw new CoreError('Conflict', 'managed endpoint override is immutable');
    }
    if (properties.templateProperties['Overridable'] !== true) {
      throw new CoreError('Conflict', 'runtime endpoint is not overridable');
    }
    const overrideUrl = validatedOverride(command.input['OverrideUrl']);
    return result(
      'AwsRuntimeEndpointOverridden',
      endpointResponse(endpoint, overrideUrl),
      [updateStoredResource(endpoint, { state: { ...state, overrideUrl } })]
    );
  }
  if (placementValue(endpoint)) {
    const placement = currentPlacement(command, world, endpoint);
    return result('AwsRuntimeEndpointResolved', {
      ...endpointResponse(
        endpoint,
        placement.EffectiveUrl,
        'managed-placement'
      ),
      ...placement,
    });
  }
  const overrideUrl = state['overrideUrl'];
  if (typeof overrideUrl !== 'string') {
    throw new CoreError('Conflict', 'runtime endpoint workload is unavailable');
  }
  return result(
    'AwsRuntimeEndpointResolved',
    endpointResponse(endpoint, overrideUrl)
  );
}
