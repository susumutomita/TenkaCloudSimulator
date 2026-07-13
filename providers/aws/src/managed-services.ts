import {
  CoreError,
  deterministicId,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
} from '@tenkacloud/simulator-core';
import {
  APP_RUNNER_SERVICE_RESOURCE,
  declaration,
  ECS_SERVICE_RESOURCE,
} from './model';
import { reviewedWorkloadForSlot } from './runtime';
import { awsResources, result, storedProperties } from './state';
import { numberValue, stringValue } from './value';

const LAMBDA_RESOURCE = 'AWS::Lambda::Function';
const SERVICE_NAME = /^[A-Za-z][A-Za-z0-9_-]{0,62}$/;

interface ManagedResourceDefinition {
  readonly operationLabel: string;
  readonly resourceType: string;
  readonly logicalIdPrefix: string;
  readonly arnService: 'apprunner' | 'ecs' | 'lambda';
  readonly eventService: 'AppRunner' | 'Ecs' | 'Lambda';
}

const MANAGED_LAMBDA: ManagedResourceDefinition = {
  operationLabel: 'Lambda CreateManagedFunction',
  resourceType: LAMBDA_RESOURCE,
  logicalIdPrefix: 'ParticipantManagedLambdaFunction',
  arnService: 'lambda',
  eventService: 'Lambda',
};

const MANAGED_ECS: ManagedResourceDefinition = {
  operationLabel: 'ECS CreateManagedService',
  resourceType: ECS_SERVICE_RESOURCE,
  logicalIdPrefix: 'ParticipantManagedEcsService',
  arnService: 'ecs',
  eventService: 'Ecs',
};

const MANAGED_APP_RUNNER: ManagedResourceDefinition = {
  operationLabel: 'App Runner CreateManagedService',
  resourceType: APP_RUNNER_SERVICE_RESOURCE,
  logicalIdPrefix: 'ParticipantManagedAppRunnerService',
  arnService: 'apprunner',
  eventService: 'AppRunner',
};

function unknownField(
  input: Readonly<Record<string, unknown>>,
  allowed: ReadonlySet<string>,
  label: string
): void {
  const field = Object.keys(input).find((name) => !allowed.has(name));
  if (field) {
    throw new CoreError(
      'UnsupportedCapability',
      `${label} field ${field} is not supported`
    );
  }
}

function serviceName(value: unknown, label: string): string {
  const name = stringValue(value, label);
  if (!SERVICE_NAME.test(name)) {
    throw new CoreError('ValidationFailed', `${label} is invalid`);
  }
  return name;
}

function participantResource(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  resourceType: string,
  name: string
) {
  const targetId = stringValue(command.targetId, 'command targetId');
  return awsResources(world, resourceType).find((resource) => {
    const properties = storedProperties(resource);
    return (
      resource.deploymentId === command.deploymentId &&
      resource.targetId === targetId &&
      properties['EligibleManagedPlacement'] === true &&
      properties.refValue === name
    );
  });
}

function resourceArn(
  definition: ManagedResourceDefinition,
  name: string,
  stableSuffix: string
): string {
  switch (definition.arnService) {
    case 'lambda':
      return `arn:aws:lambda:us-east-1:123456789012:function:${name}`;
    case 'ecs':
      return `arn:aws:ecs:us-east-1:123456789012:service/tenkacloud-simulator/${name}`;
    case 'apprunner':
      return `arn:aws:apprunner:us-east-1:123456789012:service/${name}/${stableSuffix}`;
  }
}

function createManagedResource(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  definition: ManagedResourceDefinition,
  nameField: 'FunctionName' | 'ServiceName',
  extraAllowedFields: readonly string[] = []
): ProviderCommandResult {
  unknownField(
    command.input,
    new Set([nameField, 'Slot', ...extraAllowedFields]),
    definition.operationLabel
  );
  const name = serviceName(command.input[nameField], nameField);
  const slot = stringValue(command.input['Slot'], 'Slot');
  const reviewed = reviewedWorkloadForSlot(command, world, slot);
  if (participantResource(command, world, definition.resourceType, name)) {
    throw new CoreError('Conflict', 'managed resource already exists');
  }
  const resourceId = deterministicId('aws-managed-resource', {
    worldId: command.worldId,
    deploymentId: command.deploymentId,
    targetId: reviewed.endpoint.targetId,
    resourceType: definition.resourceType,
    name,
    slot,
    reviewedArtifactHash: reviewed.artifactHash,
  });
  const suffix = deterministicId('managed-service', {
    worldId: command.worldId,
    deploymentId: command.deploymentId,
    name,
  }).slice(-32);
  const arn = resourceArn(definition, name, suffix);
  const resource = declaration({
    resourceType: definition.resourceType,
    resourceId,
    properties: {
      logicalId: `${definition.logicalIdPrefix}.${name}`,
      physicalId: arn,
      refValue: name,
      dependsOn: [reviewed.workloadId],
      attributes: { Arn: arn },
      templateProperties: {
        PlacementSlot: slot,
        ReviewedArtifactHash: reviewed.artifactHash,
        ReviewedWorkloadId: reviewed.workloadId,
      },
      status: 'PLACEMENT_ELIGIBLE',
      ParticipantCreated: true,
      EligibleManagedPlacement: true,
      PlacementSlot: slot,
      ReviewedArtifactHash: reviewed.artifactHash,
      ReviewedWorkloadId: reviewed.workloadId,
    },
  });
  return result(
    `Aws${definition.eventService}ManagedResourceCreated`,
    {
      ManagedResourceId: resourceId,
      ResourceArn: arn,
      ResourceName: name,
      ResourceType: definition.resourceType,
      Slot: slot,
      PlacementEligibility: 'ELIGIBLE',
      ReviewedArtifactHash: reviewed.artifactHash,
      ReviewedWorkloadId: reviewed.workloadId,
      ContainerPort: reviewed.containerPort,
    },
    [resource]
  );
}

function describeManagedResource(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  definition: ManagedResourceDefinition,
  nameField: 'FunctionName' | 'ServiceName'
): ProviderCommandResult {
  unknownField(
    command.input,
    new Set([nameField]),
    definition.operationLabel.replace('Create', 'Describe')
  );
  const name = serviceName(command.input[nameField], nameField);
  const resource = participantResource(
    command,
    world,
    definition.resourceType,
    name
  );
  if (!resource) {
    throw new CoreError('NotFound', 'managed resource does not exist');
  }
  const properties = storedProperties(resource);
  return result(`Aws${definition.eventService}ManagedResourceDescribed`, {
    ManagedResourceId: resource.resourceId,
    ResourceArn: properties.attributes['Arn'],
    ResourceName: properties.refValue,
    ResourceType: resource.resourceType,
    Slot: properties['PlacementSlot'],
    PlacementEligibility: 'ELIGIBLE',
    ReviewedArtifactHash: properties['ReviewedArtifactHash'],
    ReviewedWorkloadId: properties['ReviewedWorkloadId'],
  });
}

function createManagedEcsService(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const desiredCount = numberValue(
    command.input['DesiredCount'],
    'DesiredCount'
  );
  if (desiredCount !== 1) {
    throw new CoreError(
      'UnsupportedCapability',
      'ECS managed service requires DesiredCount 1'
    );
  }
  if (command.input['LaunchType'] !== 'FARGATE') {
    throw new CoreError(
      'UnsupportedCapability',
      'ECS managed service supports only FARGATE launch type'
    );
  }
  const created = createManagedResource(
    command,
    world,
    MANAGED_ECS,
    'ServiceName',
    ['DesiredCount', 'LaunchType']
  );
  return {
    ...created,
    response: {
      ...created.response,
      DesiredCount: 1,
      LaunchType: 'FARGATE',
    },
  };
}

export function reduceManagedLambda(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  switch (command.operation) {
    case 'CreateManagedFunction':
      return createManagedResource(
        command,
        world,
        MANAGED_LAMBDA,
        'FunctionName'
      );
    case 'DescribeManagedFunction':
      return describeManagedResource(
        command,
        world,
        MANAGED_LAMBDA,
        'FunctionName'
      );
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `Lambda managed operation ${command.operation} is not supported`
      );
  }
}

export function reduceEcs(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  switch (command.operation) {
    case 'CreateManagedService':
      return createManagedEcsService(command, world);
    case 'DescribeManagedService':
      return describeManagedResource(
        command,
        world,
        MANAGED_ECS,
        'ServiceName'
      );
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `ECS operation ${command.operation} is not supported`
      );
  }
}

export function reduceAppRunner(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  switch (command.operation) {
    case 'CreateManagedService':
      return createManagedResource(
        command,
        world,
        MANAGED_APP_RUNNER,
        'ServiceName'
      );
    case 'DescribeManagedService':
      return describeManagedResource(
        command,
        world,
        MANAGED_APP_RUNNER,
        'ServiceName'
      );
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `App Runner operation ${command.operation} is not supported`
      );
  }
}
