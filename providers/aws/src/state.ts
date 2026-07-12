import {
  CoreError,
  contentHash,
  deterministicId,
  type ProviderCommandResult,
  type ProviderWorldView,
  type ResourceDeclaration,
  type ResourceRecord,
} from '@tenkacloud/simulator-core';
import {
  AWS_PROVIDER,
  declaration,
  OBJECT_RESOURCE,
  RUNTIME_ENDPOINT_RESOURCE,
  STACK_RESOURCE,
  type StackProperties,
  type StoredResourceProperties,
} from './model';
import { objectValue, stringArray, stringValue } from './value';

export function asDeclaration(resource: ResourceRecord): ResourceDeclaration {
  return {
    provider: resource.provider,
    resourceType: resource.resourceType,
    resourceId: resource.resourceId,
    properties: resource.properties,
  };
}

export function storedProperties(
  resource: Pick<ResourceDeclaration, 'properties'>
): StoredResourceProperties {
  const properties = objectValue(resource.properties, 'stored resource');
  return {
    ...properties,
    logicalId: stringValue(properties['logicalId'], 'logicalId'),
    physicalId: stringValue(properties['physicalId'], 'physicalId'),
    refValue: stringValue(properties['refValue'], 'refValue'),
    dependsOn: stringArray(properties['dependsOn'], 'dependsOn'),
    attributes: objectValue(properties['attributes'], 'attributes'),
    templateProperties: objectValue(
      properties['templateProperties'],
      'templateProperties'
    ),
    status: stringValue(properties['status'], 'status'),
  };
}

export function stackProperties(
  resource: Pick<ResourceDeclaration, 'properties'>
): StackProperties {
  const properties = storedProperties(resource);
  const outputs = objectValue(properties['outputs'], 'stack outputs');
  const stringOutputs = Object.fromEntries(
    Object.entries(outputs).map(([name, value]) => {
      if (typeof value !== 'string') {
        throw new CoreError(
          'ValidationFailed',
          `stack output ${name} must be a string`
        );
      }
      return [name, value];
    })
  );
  return {
    ...properties,
    problemId: stringValue(properties['problemId'], 'problemId'),
    targetId: stringValue(properties['targetId'], 'targetId'),
    entry: stringValue(properties['entry'], 'entry'),
    templateBody: stringValue(properties['templateBody'], 'templateBody'),
    ...(properties['metadata'] === undefined
      ? {}
      : { metadata: properties['metadata'] }),
    outputs: stringOutputs,
    resourceLogicalIds: stringArray(
      properties['resourceLogicalIds'],
      'resourceLogicalIds'
    ),
  };
}

export function awsResources(
  world: ProviderWorldView,
  resourceType?: string
): readonly ResourceRecord[] {
  return world.resources.filter(
    (resource) =>
      resource.provider === AWS_PROVIDER &&
      resource.status === 'ready' &&
      (resourceType === undefined || resource.resourceType === resourceType)
  );
}

export function resourcesForDeployment(
  world: ProviderWorldView,
  deploymentId: string,
  resourceType?: string
): readonly ResourceRecord[] {
  return awsResources(world, resourceType).filter(
    (resource) => resource.deploymentId === deploymentId
  );
}

export function findBy(
  world: ProviderWorldView,
  resourceType: string,
  predicate: (properties: StoredResourceProperties) => boolean,
  label: string
): ResourceRecord {
  const resource = awsResources(world, resourceType).find((candidate) =>
    predicate(storedProperties(candidate))
  );
  if (!resource) throw new CoreError('NotFound', `${label} does not exist`);
  return resource;
}

export function findStack(
  world: ProviderWorldView,
  deploymentId: string
): ResourceRecord {
  const stack = resourcesForDeployment(world, deploymentId, STACK_RESOURCE)[0];
  if (!stack) throw new CoreError('NotFound', 'stack does not exist');
  return stack;
}

export function updateStoredResource(
  resource: ResourceRecord,
  properties: Readonly<Record<string, unknown>>
): ResourceDeclaration {
  return asDeclaration({
    ...resource,
    properties: { ...resource.properties, ...properties },
  });
}

export function stateObject(
  properties: StoredResourceProperties
): Readonly<Record<string, unknown>> {
  return properties['state'] === undefined
    ? {}
    : objectValue(properties['state'], 'resource state');
}

export function result(
  eventType: string,
  response: Readonly<Record<string, unknown>>,
  resources: readonly ResourceDeclaration[] = [],
  deletedResourceIds: readonly string[] = [],
  outputs: Readonly<Record<string, string>> = {}
): ProviderCommandResult {
  return {
    events: [
      {
        type: eventType,
        payload: {
          operation: eventType,
          resources: resources.map((resource) => resource.resourceId),
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

function initialState(
  resource: ResourceDeclaration,
  virtualTime: string
): Readonly<Record<string, unknown>> {
  const stored = storedProperties(resource);
  const template = stored.templateProperties;
  switch (resource.resourceType) {
    case STACK_RESOURCE:
      return { stackStatus: 'CREATE_COMPLETE', lastUpdatedTime: virtualTime };
    case 'AWS::SSM::Parameter':
      return { version: 1, lastModifiedDate: virtualTime };
    case 'AWS::EC2::SecurityGroup':
      return {
        ipPermissions: Array.isArray(template['SecurityGroupIngress'])
          ? template['SecurityGroupIngress']
          : [],
        ipPermissionsEgress: Array.isArray(template['SecurityGroupEgress'])
          ? template['SecurityGroupEgress']
          : [],
      };
    case 'AWS::EC2::Instance': {
      const userData =
        typeof template['UserData'] === 'string' ? template['UserData'] : '';
      return {
        instanceState: 'running',
        networkDelayMs: 0,
        databaseWiped: false,
        authRequired: false,
        authTokenConfigured: false,
        boardClean: true,
        sqliParameterized: false,
        nginxRateLimitConfigured: false,
        rateLimitEnabled: false,
        loadActive: false,
        siteDefaced: false,
        backdoorInstalled: false,
        services: {
          nginx: userData.includes('nginx') ? 'running' : 'unknown',
          'tenkacloud-vibe': userData.includes('tenkacloud-vibe')
            ? 'running'
            : 'unknown',
        },
      };
    }
    case 'AWS::Lambda::Function':
      return { invocationCount: 0 };
    case 'AWS::WAFv2::WebACL':
      return { associatedResources: [] };
    case 'AWS::RDS::DBInstance':
      return { dbInstanceStatus: 'available' };
    case 'AWS::Logs::LogGroup':
      return { retentionInDays: template['RetentionInDays'] ?? null };
    case RUNTIME_ENDPOINT_RESOURCE:
      return { overrideUrl: null };
    default:
      return {};
  }
}

export function initializeResource(
  resource: ResourceDeclaration,
  virtualTime: string
): ResourceDeclaration {
  const stored = storedProperties(resource);
  return declaration({
    ...resource,
    properties: {
      ...stored,
      status: 'CREATE_COMPLETE',
      state: initialState(resource, virtualTime),
    },
  });
}

export function customResourceObjects(
  resources: readonly ResourceDeclaration[],
  virtualTime: string
): readonly ResourceDeclaration[] {
  return resources.flatMap((resource) => {
    if (resource.resourceType !== 'AWS::CloudFormation::CustomResource')
      return [];
    const stored = storedProperties(resource);
    const bucket = stored.templateProperties['Bucket'];
    const key = stored.templateProperties['Key'];
    const body = stored.templateProperties['Body'];
    if (
      typeof bucket !== 'string' ||
      typeof key !== 'string' ||
      typeof body !== 'string'
    ) {
      return [];
    }
    const resourceId = deterministicId('s3object', { bucket, key });
    return [
      declaration({
        resourceType: OBJECT_RESOURCE,
        resourceId,
        properties: {
          logicalId: `${stored.logicalId}.Object`,
          physicalId: `${bucket}/${key}`,
          refValue: `${bucket}/${key}`,
          dependsOn: [stored.logicalId],
          attributes: {},
          templateProperties: { Bucket: bucket, Key: key },
          status: 'AVAILABLE',
          Bucket: bucket,
          Key: key,
          Body: body,
          ContentType: 'text/x-shellscript',
          ETag: contentHash(body),
          LastModified: virtualTime,
        },
      }),
    ];
  });
}
