import {
  CoreError,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
  type ResourceRecord,
} from '@tenkacloud/simulator-core';
import { compileCloudFormation } from './cloudformation';
import { deployCloudFormation } from './deploy';
import {
  AWS_PROVIDER,
  CLOUDFORMATION_ENGINE,
  OBJECT_RESOURCE,
  RUNTIME_ENDPOINT_RESOURCE,
  STACK_RESOURCE,
} from './model';
import { preserveBoundRuntimeEndpoint } from './runtime';
import {
  awsResources,
  findStack,
  resourcesForDeployment,
  result,
  stackProperties,
  storedProperties,
} from './state';
import { optionalString, stringValue } from './value';

function stackDocument(resource: ReturnType<typeof findStack>) {
  const stored = stackProperties(resource);
  return {
    StackId: resource.resourceId,
    StackName: stored.logicalId,
    Description: `TenkaCloud simulation stack for ${stored.problemId}`,
    StackStatus:
      stored.status === 'CREATE_COMPLETE' ? 'CREATE_COMPLETE' : stored.status,
    CreationTime: '1970-01-01T00:00:00.000Z',
    LastUpdatedTime:
      resource.properties['state'] &&
      typeof resource.properties['state'] === 'object'
        ? Reflect.get(resource.properties['state'], 'lastUpdatedTime')
        : undefined,
    Outputs: Object.entries(stored.outputs).map(([OutputKey, OutputValue]) => ({
      OutputKey,
      OutputValue,
    })),
  };
}

function selectedStacks(
  command: ProviderCommandInput,
  world: ProviderWorldView
) {
  const name = optionalString(command.input['StackName'], 'StackName');
  const stacks = awsResources(world, STACK_RESOURCE).filter((resource) => {
    const stored = storedProperties(resource);
    return (
      resource.deploymentId === command.deploymentId &&
      (name === undefined ||
        name === resource.resourceId ||
        name === stored.logicalId ||
        name === stored.refValue)
    );
  });
  if (name !== undefined && stacks.length === 0) {
    throw new CoreError('NotFound', 'stack does not exist');
  }
  return stacks;
}

function stackResourceDocuments(
  world: ProviderWorldView,
  deploymentId: string
) {
  const stack = findStack(world, deploymentId);
  const state = stackProperties(stack);
  const logicalIds = new Set(state.resourceLogicalIds);
  return resourcesForDeployment(world, deploymentId)
    .filter(
      (resource) =>
        resource.targetId === state.targetId &&
        resource.properties['ParticipantCreated'] !== true &&
        logicalIds.has(storedProperties(resource).logicalId)
    )
    .map((resource) => {
      const stored = storedProperties(resource);
      return {
        StackName: `${deploymentId}`,
        StackId: stack.resourceId,
        LogicalResourceId: stored.logicalId,
        PhysicalResourceId: stored.physicalId,
        ResourceType: resource.resourceType,
        ResourceStatus: stored.status,
      };
    })
    .sort((left, right) =>
      left.LogicalResourceId.localeCompare(right.LogicalResourceId)
    );
}

function stackOwnsResource(
  resource: ResourceRecord,
  stack: ReturnType<typeof findStack>,
  state: ReturnType<typeof stackProperties>
): boolean {
  if (
    resource.targetId !== state.targetId ||
    resource.properties['ParticipantCreated'] === true
  ) {
    return false;
  }
  if (resource.resourceId === stack.resourceId) return true;
  const properties = storedProperties(resource);
  const logicalIds = new Set(state.resourceLogicalIds);
  if (logicalIds.has(properties.logicalId)) return true;
  if (
    resource.resourceType === OBJECT_RESOURCE &&
    properties.dependsOn.length === 1 &&
    logicalIds.has(properties.dependsOn[0] ?? '') &&
    properties.logicalId === `${properties.dependsOn[0]}.Object`
  ) {
    return true;
  }
  return (
    resource.resourceType === RUNTIME_ENDPOINT_RESOURCE &&
    properties['ProblemId'] === state.problemId &&
    properties['TargetId'] === state.targetId &&
    properties.dependsOn.length === 1 &&
    properties.dependsOn[0] === state.logicalId &&
    properties.physicalId.startsWith(`${stack.resourceId}:`)
  );
}

function preserveBoundEndpoints(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  existing: ReturnType<typeof findStack>,
  state: ReturnType<typeof stackProperties>,
  resources: ProviderCommandResult['resources']
) {
  const replacements = new Map(
    resources.map((resource) => [resource.resourceId, resource])
  );
  for (const endpoint of resourcesForDeployment(
    world,
    command.deploymentId,
    RUNTIME_ENDPOINT_RESOURCE
  )) {
    if (
      !stackOwnsResource(endpoint, existing, state) ||
      endpoint.properties['ManagedPlacement'] === undefined
    ) {
      continue;
    }
    const replacement = replacements.get(endpoint.resourceId);
    if (!replacement) {
      throw new CoreError(
        'Conflict',
        'bound runtime endpoint cannot be removed from the stack'
      );
    }
    replacements.set(
      endpoint.resourceId,
      preserveBoundRuntimeEndpoint(command, world, endpoint, replacement)
    );
  }
  return resources.map(
    (resource) => replacements.get(resource.resourceId) ?? resource
  );
}

function updateStack(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const existing = findStack(world, command.deploymentId);
  const existingState = stackProperties(existing);
  const templateBody = stringValue(
    command.input['TemplateBody'],
    'TemplateBody'
  );
  const metadata = command.input['Metadata'] ?? existingState.metadata;
  const plan = compileCloudFormation({
    target: {
      provider: AWS_PROVIDER,
      engine: CLOUDFORMATION_ENGINE,
      entry: existingState.entry,
    },
    targetId: existingState.targetId,
    problemId: existingState.problemId,
    templateBody,
    artifacts: [],
    ...(metadata === undefined ? {} : { metadata }),
  });
  const deployed = deployCloudFormation(plan, world);
  const resources = preserveBoundEndpoints(
    command,
    world,
    existing,
    existingState,
    deployed.resources
  );
  const nextIds = new Set(resources.map((resource) => resource.resourceId));
  const deletedResourceIds = resourcesForDeployment(world, command.deploymentId)
    .filter(
      (resource) =>
        stackOwnsResource(resource, existing, existingState) &&
        !nextIds.has(resource.resourceId)
    )
    .map((resource) => resource.resourceId)
    .sort();
  const stack = deployed.resources.find(
    (resource) => resource.resourceType === STACK_RESOURCE
  );
  if (!stack)
    throw new CoreError('ValidationFailed', 'updated stack is missing');
  return {
    events: [
      {
        type: 'AwsCloudFormationStackUpdated',
        payload: {
          stackId: stack.resourceId,
          resourceCount: deployed.resources.length - 1,
          deletedResourceIds,
        },
      },
      ...deployed.events,
    ],
    resources,
    deletedResourceIds,
    outputs: deployed.outputs,
    response: { StackId: stack.resourceId },
  };
}

export function reduceCloudFormation(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  switch (command.operation) {
    case 'DescribeStacks':
      return result('AwsCloudFormationStacksDescribed', {
        Stacks: selectedStacks(command, world).map(stackDocument),
      });
    case 'ListStacks':
      return result('AwsCloudFormationStacksListed', {
        StackSummaries: selectedStacks(command, world).map((stack) => {
          const document = stackDocument(stack);
          return {
            StackId: document.StackId,
            StackName: document.StackName,
            StackStatus: document.StackStatus,
            CreationTime: document.CreationTime,
          };
        }),
      });
    case 'DescribeStackResources':
      return result('AwsCloudFormationStackResourcesDescribed', {
        StackResources: stackResourceDocuments(world, command.deploymentId),
      });
    case 'ListStackResources':
      return result('AwsCloudFormationStackResourcesListed', {
        StackResourceSummaries: stackResourceDocuments(
          world,
          command.deploymentId
        ),
      });
    case 'UpdateStack':
      return updateStack(command, world);
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `CloudFormation operation ${command.operation} is not supported`
      );
  }
}
