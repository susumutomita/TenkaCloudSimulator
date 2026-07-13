import { afterAll, describe, expect, it } from 'bun:test';
import {
  CoreError,
  type ExecuteCommandInput,
  type ProviderCommandInput,
  type ProviderWorldView,
  type ResourceRecord,
} from '@tenkacloud/simulator-core';
import { AWS_CAPABILITIES } from '../src/model';
import { AwsProvider } from '../src/provider';
import { compileRuntimeEndpoints } from '../src/runtime';
import {
  cleanupContexts,
  createContext,
  execute,
  type TestContext,
} from './support';

const LAMBDA_RESOURCE = 'AWS::Lambda::Function';
const ECS_SERVICE_RESOURCE = 'AWS::ECS::Service';
const APP_RUNNER_SERVICE_RESOURCE = 'AWS::AppRunner::Service';
const RUNTIME_ENDPOINT_RESOURCE = 'Runtime::Endpoint';

const MANAGED_RESOURCE_CASES = [
  {
    service: 'lambda',
    resourceType: LAMBDA_RESOURCE,
    name: 'identity-lambda',
    slot: 'users',
    nameField: 'FunctionName',
    describeOperation: 'DescribeManagedFunction',
  },
  {
    service: 'ecs',
    resourceType: ECS_SERVICE_RESOURCE,
    name: 'identity-ecs',
    slot: 'orders',
    nameField: 'ServiceName',
    describeOperation: 'DescribeManagedService',
  },
  {
    service: 'apprunner',
    resourceType: APP_RUNNER_SERVICE_RESOURCE,
    name: 'identity-apprunner',
    slot: 'catalog',
    nameField: 'ServiceName',
    describeOperation: 'DescribeManagedService',
  },
] as const;

type ManagedResourceCase = (typeof MANAGED_RESOURCE_CASES)[number];

afterAll(cleanupContexts);

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
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

function reviewedWorkload(
  context: TestContext,
  endpoint = 'http://127.0.0.1:43123'
): void {
  context.store.saveResource({
    worldId: context.worldId,
    deploymentId: context.deploymentId,
    targetId: 'default',
    provider: 'runtime',
    resourceType: 'Runtime::Workload',
    resourceId: 'reviewed-migration-workload',
    properties: {
      declaration: {
        id: 'migration-gateway',
        targetId: 'default',
        resourceRef: 'FunctionUrl',
        image:
          'ghcr.io/susumutomita/migration@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
        command: ['bun', 'run', 'start'],
        containerPort: 8080,
        healthPath: '/healthz',
      },
      materialization: { endpoint },
    },
    status: 'ready',
  });
}

function createLambda(context: TestContext, name: string, slot: string) {
  const response = execute(
    context,
    'lambda',
    'CreateManagedFunction',
    {
      FunctionName: name,
      Slot: slot,
    },
    LAMBDA_RESOURCE
  );
  const resource = context.core
    .resources(context.worldId)
    .find(
      (candidate) =>
        candidate.resourceType === LAMBDA_RESOURCE &&
        candidate.properties['refValue'] === name
    );
  if (!resource) throw new Error('created Lambda resource is missing');
  return { ...response, ManagedResourceId: resource.resourceId };
}

function createEcs(context: TestContext, name: string, slot: string) {
  return execute(
    context,
    'ecs',
    'CreateManagedService',
    {
      ServiceName: name,
      Slot: slot,
      DesiredCount: 1,
      LaunchType: 'FARGATE',
    },
    ECS_SERVICE_RESOURCE
  );
}

function createAppRunner(context: TestContext, name: string, slot: string) {
  return execute(
    context,
    'apprunner',
    'CreateManagedService',
    {
      ServiceName: name,
      Slot: slot,
    },
    APP_RUNNER_SERVICE_RESOURCE
  );
}

function createManagedResource(
  context: TestContext,
  definition: ManagedResourceCase
) {
  switch (definition.service) {
    case 'lambda':
      return createLambda(context, definition.name, definition.slot);
    case 'ecs':
      return createEcs(context, definition.name, definition.slot);
    case 'apprunner':
      return createAppRunner(context, definition.name, definition.slot);
  }
}

function describeManagedResource(
  context: TestContext,
  definition: ManagedResourceCase
) {
  return execute(
    context,
    definition.service,
    definition.describeOperation,
    { [definition.nameField]: definition.name },
    definition.resourceType
  );
}

function saveOrdinaryResource(
  context: TestContext,
  definition: ManagedResourceCase,
  resourceId: string
): void {
  context.store.saveResource({
    worldId: context.worldId,
    deploymentId: context.deploymentId,
    targetId: 'default',
    provider: 'aws',
    resourceType: definition.resourceType,
    resourceId,
    properties: {
      logicalId: `Ordinary.${definition.name}`,
      physicalId: definition.name,
      refValue: definition.name,
      dependsOn: [],
      attributes: {},
      templateProperties: {},
      status: 'CREATE_COMPLETE',
    },
    status: 'ready',
  });
}

function createOrdinaryLambda(context: TestContext, functionName: string) {
  return execute(context, 'lambda', 'CreateFunction', {
    FunctionName: functionName,
    Runtime: 'nodejs22.x',
    Role: 'arn:aws:iam::123456789012:role/tc-fixture-role',
    Handler: 'index.handler',
    Code: { ZipFile: Buffer.from('zip').toString('base64') },
  });
}

function templateWithLambda(
  templateBody: string,
  logicalId: string,
  functionName: string
): string {
  const resourcesMarker = 'Resources:\n';
  if (!templateBody.includes(resourcesMarker)) {
    throw new Error('CloudFormation template に Resources がありません');
  }
  return templateBody.replace(
    resourcesMarker,
    `${resourcesMarker}  ${logicalId}:\n    Type: AWS::Lambda::Function\n    Properties:\n      FunctionName: ${functionName}\n      Runtime: nodejs22.x\n      Handler: index.handler\n      Role: arn:aws:iam::123456789012:role/tc-fixture-role\n      Code:\n        ZipFile: "exports.handler = async () => ({ statusCode: 200 });"\n`
  );
}

function resourceId(response: Readonly<Record<string, unknown>>): string {
  const value = response['ManagedResourceId'];
  if (typeof value !== 'string') throw new Error('resource id is missing');
  return value;
}

function bind(context: TestContext, slot: string, managedResourceId: string) {
  return execute(
    context,
    'runtime',
    'BindManagedResource',
    { Slot: slot, ManagedResourceId: managedResourceId },
    RUNTIME_ENDPOINT_RESOURCE
  );
}

function placement(context: TestContext, slot: string) {
  return execute(
    context,
    'runtime',
    'DescribeEndpointPlacement',
    { Slot: slot },
    RUNTIME_ENDPOINT_RESOURCE
  );
}

function participantResource(
  context: TestContext,
  overrides: Partial<ResourceRecord>
): ResourceRecord {
  return {
    worldId: context.worldId,
    deploymentId: context.deploymentId,
    targetId: 'default',
    provider: 'aws',
    resourceType: ECS_SERVICE_RESOURCE,
    resourceId: 'participant-resource',
    properties: {
      logicalId: 'ParticipantManagedResource.fixture',
      physicalId: 'fixture',
      refValue: 'fixture',
      dependsOn: [],
      attributes: {},
      templateProperties: {},
      status: 'ACTIVE',
      ParticipantCreated: true,
      EligibleManagedPlacement: true,
      PlacementSlot: 'users',
      ReviewedArtifactHash: 'fixture-artifact-hash',
      ReviewedWorkloadId: 'reviewed-migration-workload',
    },
    status: 'ready',
    ...overrides,
  };
}

function bindCommand(
  context: TestContext,
  slot: string,
  managedResourceId: string
): ExecuteCommandInput {
  return {
    deploymentId: context.deploymentId,
    targetId: 'default',
    provider: 'aws',
    engine: 'cloudformation',
    service: 'runtime',
    operation: 'BindManagedResource',
    resourceType: RUNTIME_ENDPOINT_RESOURCE,
    input: { Slot: slot, ManagedResourceId: managedResourceId },
  };
}

function providerCommand(
  context: TestContext,
  service: string,
  operation: string,
  resourceType: string,
  input: Readonly<Record<string, unknown>>
): ProviderCommandInput {
  return {
    worldId: context.worldId,
    deploymentId: context.deploymentId,
    targetId: 'default',
    service,
    operation,
    resourceType,
    input,
  };
}

function providerWorld(context: TestContext): ProviderWorldView {
  return {
    world: context.core.world(context.worldId),
    resources: context.core.resources(context.worldId),
  };
}

describe('AWS managed placement の resource graph 投影', () => {
  it('managed create read bind projection capability identity を公開する', () => {
    expect(
      AWS_CAPABILITIES.filter((capability) =>
        [
          'BindManagedResource',
          'CreateManagedFunction',
          'CreateManagedService',
          'DescribeEndpointPlacement',
          'DescribeManagedFunction',
          'DescribeManagedService',
        ].includes(capability.operation)
      ).map(
        (capability) =>
          `${capability.provider}/${capability.engine}/${capability.service}/${capability.resourceType}/${capability.operation}`
      )
    ).toEqual([
      'aws/cloudformation/apprunner/AWS::AppRunner::Service/CreateManagedService',
      'aws/cloudformation/apprunner/AWS::AppRunner::Service/DescribeManagedService',
      'aws/cloudformation/ecs/AWS::ECS::Service/CreateManagedService',
      'aws/cloudformation/ecs/AWS::ECS::Service/DescribeManagedService',
      'aws/cloudformation/lambda/AWS::Lambda::Function/CreateManagedFunction',
      'aws/cloudformation/lambda/AWS::Lambda::Function/DescribeManagedFunction',
      'aws/cloudformation/runtime/Runtime::Endpoint/BindManagedResource',
      'aws/cloudformation/runtime/Runtime::Endpoint/DescribeEndpointPlacement',
    ]);
  });

  it('Lambda ECS App Runner の participant resource を event と snapshot に永続化する', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    const lambda = createLambda(context, 'participant-users', 'users');
    const ecs = createEcs(context, 'participant-orders', 'orders');
    const appRunner = createAppRunner(
      context,
      'participant-catalog',
      'catalog'
    );

    expect(
      execute(
        context,
        'ecs',
        'DescribeManagedService',
        { ServiceName: 'participant-orders' },
        ECS_SERVICE_RESOURCE
      )
    ).toMatchObject({
      ResourceName: 'participant-orders',
      PlacementEligibility: 'ELIGIBLE',
      Slot: 'orders',
    });
    expect(
      execute(
        context,
        'apprunner',
        'DescribeManagedService',
        { ServiceName: 'participant-catalog' },
        APP_RUNNER_SERVICE_RESOURCE
      )
    ).toMatchObject({
      ResourceName: 'participant-catalog',
      PlacementEligibility: 'ELIGIBLE',
      Slot: 'catalog',
    });

    const ids = [lambda, ecs, appRunner].map(resourceId);
    const resources = context.core
      .resources(context.worldId)
      .filter((resource) => ids.includes(resource.resourceId));
    expect(resources).toHaveLength(3);
    expect(resources.map((resource) => resource.resourceType).sort()).toEqual(
      [
        APP_RUNNER_SERVICE_RESOURCE,
        ECS_SERVICE_RESOURCE,
        LAMBDA_RESOURCE,
      ].sort()
    );
    expect(
      resources.every(
        (resource) =>
          resource.status === 'ready' &&
          resource.properties['ParticipantCreated'] === true
      )
    ).toBe(true);

    const events = context.core.events(context.worldId);
    expect(events.map((event) => event.type)).toEqual(
      expect.arrayContaining([
        'AwsLambdaManagedResourceCreated',
        'AwsEcsManagedResourceCreated',
        'AwsAppRunnerManagedResourceCreated',
      ])
    );
    const snapshot = context.core.exportSnapshot(context.worldId);
    expect(
      snapshot.payload.resources.filter((resource) =>
        ids.includes(resource.resourceId)
      )
    ).toEqual(resources);
  });

  it('resource type 由来 tier と reviewed workload URL だけを slot projection に保存する', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    const lambdaId = resourceId(
      createLambda(context, 'participant-users', 'users')
    );
    const ecsId = resourceId(
      createEcs(context, 'participant-orders', 'orders')
    );
    const appRunnerId = resourceId(
      createAppRunner(context, 'participant-catalog', 'catalog')
    );

    expect(bind(context, 'users', lambdaId)).toEqual({
      DeploymentId: context.deploymentId,
      TargetId: 'default',
      Slot: 'users',
      EffectiveUrl: 'http://127.0.0.1:43123/users',
      ReviewedWorkloadId: 'reviewed-migration-workload',
      ReviewedArtifactHash: expect.any(String),
      ManagedResourceId: lambdaId,
      ManagedResourceType: LAMBDA_RESOURCE,
      VerifiedPlatform: 'lambda',
    });
    expect(bind(context, 'orders', ecsId)).toMatchObject({
      Slot: 'orders',
      EffectiveUrl: 'http://127.0.0.1:43123/orders',
      ManagedResourceType: ECS_SERVICE_RESOURCE,
      VerifiedPlatform: 'ecs',
    });
    expect(bind(context, 'catalog', appRunnerId)).toMatchObject({
      Slot: 'catalog',
      EffectiveUrl: 'http://127.0.0.1:43123/catalog',
      ManagedResourceType: APP_RUNNER_SERVICE_RESOURCE,
      VerifiedPlatform: 'apprunner',
    });

    expect(placement(context, 'users')).toMatchObject({
      Slot: 'users',
      ManagedResourceId: lambdaId,
      VerifiedPlatform: 'lambda',
    });
    const endpoint = context.core
      .resources(context.worldId)
      .find(
        (resource) =>
          resource.resourceType === RUNTIME_ENDPOINT_RESOURCE &&
          resource.properties['Slot'] === 'users'
      );
    expect(endpoint?.properties['ManagedPlacement']).toEqual(
      placement(context, 'users')
    );
    expect(JSON.stringify(endpoint)).not.toContain('participant-controlled');
  });

  it('同一 slot の未 binding 候補は複数許可し binding と resource 再利用を拒否する', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    const first = resourceId(
      createEcs(context, 'participant-orders', 'orders')
    );
    const second = resourceId(
      createAppRunner(context, 'participant-catalog', 'orders')
    );
    const command = bindCommand(context, 'orders', first);

    const created = context.core.executeCommand(
      context.worldId,
      command,
      'stable-bind-key'
    );
    expect(
      context.core.executeCommand(context.worldId, command, 'stable-bind-key')
    ).toEqual(created);
    expect(() => bind(context, 'orders', second)).toThrow(
      'runtime endpoint is already bound'
    );
    expect(() => bind(context, 'catalog', first)).toThrow(
      'managed resource is already bound'
    );
    expect(() =>
      execute(
        context,
        'runtime',
        'DescribeEndpointPlacement',
        { Slot: 'orders', TargetId: 'other-target' },
        RUNTIME_ENDPOINT_RESOURCE
      )
    ).toThrow('TargetId must match the command target');
    expect(() =>
      execute(
        context,
        'runtime',
        'ResolveEndpoint',
        { Slot: 'orders', OverrideUrl: 'http://127.0.0.1:9999/spoof' },
        RUNTIME_ENDPOINT_RESOURCE
      )
    ).toThrow('managed endpoint override is immutable');
  });

  it('別 scope 未 ready 非 participant unknown tier と不正 workload を fail closed にする', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    const otherWorld = await createContext('managed-placement-metadata.json');
    reviewedWorkload(otherWorld);
    const foreign = resourceId(
      createEcs(otherWorld, 'foreign-service', 'users')
    );
    expect(() => bind(context, 'users', foreign)).toThrow(
      'managed resource does not exist'
    );

    for (const [resource, message] of [
      [
        participantResource(context, {
          resourceId: 'other-deployment',
          deploymentId: 'different-deployment',
        }),
        'managed resource does not exist',
      ],
      [
        participantResource(context, {
          resourceId: 'other-target',
          targetId: 'different-target',
        }),
        'managed resource does not exist',
      ],
      [
        participantResource(context, {
          resourceId: 'pending-resource',
          status: 'pending',
        }),
        'managed resource is not ready',
      ],
      [
        participantResource(context, {
          resourceId: 'unknown-tier',
          resourceType: 'AWS::RDS::DBInstance',
        }),
        'managed resource type is not supported',
      ],
      [
        participantResource(context, {
          resourceId: 'not-participant',
          properties: {
            ...participantResource(context, {}).properties,
            ParticipantCreated: false,
          },
        }),
        'managed resource was not participant-created',
      ],
    ] as const) {
      context.store.saveResource(resource);
      expect(() => bind(context, 'users', resource.resourceId)).toThrow(
        message
      );
    }

    context.store.saveResource({
      ...participantResource(context, { resourceId: 'valid-resource' }),
    });
    const storedWorkload = context.store
      .resources(context.worldId)
      .find(
        (resource) => resource.resourceId === 'reviewed-migration-workload'
      );
    if (!storedWorkload) throw new Error('reviewed workload is missing');
    context.store.saveResource({
      ...storedWorkload,
      properties: {
        declaration: {
          id: 'migration-gateway',
          targetId: 'default',
          resourceRef: 'WrongOutput',
          image:
            'reviewed@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
          containerPort: 8080,
        },
        materialization: { endpoint: 'http://127.0.0.1:43123' },
      },
    });
    expect(() => bind(context, 'users', 'valid-resource')).toThrow(
      'reviewed workload endpoint is unavailable'
    );
  });

  it('participant platform field と URL field を create operation で受理しない', async () => {
    const context = await createContext();
    expect(() =>
      execute(
        context,
        'ecs',
        'CreateManagedService',
        {
          ServiceName: 'spoofed-ecs',
          Slot: 'users',
          DesiredCount: 1,
          LaunchType: 'FARGATE',
          Platform: 'lambda',
        },
        ECS_SERVICE_RESOURCE
      )
    ).toThrow('ECS CreateManagedService field Platform is not supported');
    expect(() =>
      execute(
        context,
        'apprunner',
        'CreateManagedService',
        {
          ServiceName: 'spoofed-apprunner',
          Slot: 'catalog',
          EndpointUrl: 'http://127.0.0.1:9999/spoof',
        },
        APP_RUNNER_SERVICE_RESOURCE
      )
    ).toThrow(
      'App Runner CreateManagedService field EndpointUrl is not supported'
    );
  });

  it('通常の Lambda CreateFunction は managed placement eligibility を持たない', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    execute(context, 'lambda', 'CreateFunction', {
      FunctionName: 'participant-unverified',
      Runtime: 'nodejs22.x',
      Role: 'arn:aws:iam::123456789012:role/tc-fixture-role',
      Handler: 'index.handler',
      Code: { ZipFile: Buffer.from('zip').toString('base64') },
      Environment: { Variables: { PLATFORM: 'lambda' } },
    });
    const resource = context.core
      .resources(context.worldId)
      .find(
        (candidate) =>
          candidate.resourceType === LAMBDA_RESOURCE &&
          candidate.properties['refValue'] === 'participant-unverified'
      );
    if (!resource) throw new Error('participant Lambda is missing');
    expect(() => bind(context, 'users', resource.resourceId)).toThrow(
      'managed resource is not eligible for endpoint placement'
    );
  });

  it('通常 Lambda が先にあると同名の managed create を実 product operation で拒否する', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    const lambda = MANAGED_RESOURCE_CASES[0];
    createOrdinaryLambda(context, lambda.name);

    expect(() => createManagedResource(context, lambda)).toThrow(
      'managed resource already exists'
    );
    expect(() => describeManagedResource(context, lambda)).toThrow(
      'managed resource does not exist'
    );
  });

  it('同一 scope の real SQLite graph にある通常 ECS App Runner を managed collision guard が検出する', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    for (const definition of MANAGED_RESOURCE_CASES.slice(1)) {
      saveOrdinaryResource(
        context,
        definition,
        `ordinary-before-${definition.service}`
      );
      expect(() => createManagedResource(context, definition)).toThrow(
        'managed resource already exists'
      );
      expect(() => describeManagedResource(context, definition)).toThrow(
        'managed resource does not exist'
      );
    }
  });

  it('先に managed Lambda があると同名の通常 CreateFunction を実 product operation で拒否する', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    const lambda = MANAGED_RESOURCE_CASES[0];
    createManagedResource(context, lambda);

    expect(() => createOrdinaryLambda(context, lambda.name)).toThrow(
      'Lambda function already exists'
    );
  });

  it('先に managed Lambda があると同名 Function を追加する stack update を原子的に拒否する', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    const lambda = MANAGED_RESOURCE_CASES[0];
    const managedResourceId = resourceId(
      createManagedResource(context, lambda)
    );
    const templateBody = templateWithLambda(
      context.templateBody,
      'CollisionFunction',
      lambda.name
    );
    const eventsBefore = context.core.events(context.worldId);
    const resourcesBefore = context.core.resources(context.worldId);
    const deploymentBefore = context.core.deployment(
      context.worldId,
      context.deploymentId
    );
    const idempotencyKey = `cloudformation-UpdateStack-${context.sequence + 1}`;
    const idempotencyScope = `command:${context.worldId}:aws`;
    expect(
      context.store.idempotentResponse(idempotencyScope, idempotencyKey)
    ).toBeUndefined();

    const error = captureCoreError(() =>
      execute(context, 'cloudformation', 'UpdateStack', {
        TemplateBody: templateBody,
      })
    );

    expect(error.code).toBe('Conflict');
    expect(error.message).toBe(
      'CloudFormation resource identity conflicts with a participant resource'
    );
    expect(context.core.events(context.worldId)).toEqual(eventsBefore);
    expect(context.core.resources(context.worldId)).toEqual(resourcesBefore);
    expect(
      context.core.deployment(context.worldId, context.deploymentId)
    ).toEqual(deploymentBefore);
    expect(
      context.store.idempotentResponse(idempotencyScope, idempotencyKey)
    ).toBeUndefined();
    expect(
      context.core
        .resources(context.worldId)
        .filter(
          (resource) =>
            resource.deploymentId === context.deploymentId &&
            resource.targetId === 'default' &&
            resource.resourceType === LAMBDA_RESOURCE &&
            resource.properties['refValue'] === lambda.name &&
            resource.status === 'ready'
        )
        .map((resource) => resource.resourceId)
    ).toEqual([managedResourceId]);
    expect(describeManagedResource(context, lambda)).toMatchObject({
      ManagedResourceId: managedResourceId,
      ResourceName: lambda.name,
    });
  });

  it('同一 scope の real SQLite graph で ECS App Runner の eligible describe と同名 collision guard を分離する', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);

    for (const definition of MANAGED_RESOURCE_CASES.slice(1)) {
      const managedResourceId = resourceId(
        createManagedResource(context, definition)
      );
      saveOrdinaryResource(
        context,
        definition,
        `000-ordinary-after-${definition.service}`
      );
      expect(describeManagedResource(context, definition)).toMatchObject({
        ManagedResourceId: managedResourceId,
        ResourceName: definition.name,
      });
      context.store.deleteResource(
        context.worldId,
        context.deploymentId,
        'default',
        'aws',
        managedResourceId
      );
      expect(() => createManagedResource(context, definition)).toThrow(
        'managed resource already exists'
      );
    }
  });

  it('stack update の前後で participant resource と bound endpoint の一貫性を保つ', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    execute(context, 'lambda', 'CreateFunction', {
      FunctionName: 'participant-unverified',
      Runtime: 'nodejs22.x',
      Role: 'arn:aws:iam::123456789012:role/tc-fixture-role',
      Handler: 'index.handler',
      Code: { ZipFile: Buffer.from('zip').toString('base64') },
    });
    const normal = context.core
      .resources(context.worldId)
      .find(
        (candidate) =>
          candidate.resourceType === LAMBDA_RESOURCE &&
          candidate.properties['refValue'] === 'participant-unverified'
      );
    if (!normal) throw new Error('participant Lambda is missing');
    const managedResourceId = resourceId(
      createLambda(context, 'participant-users', 'users')
    );
    bind(context, 'users', managedResourceId);
    const describe: ExecuteCommandInput = {
      deploymentId: context.deploymentId,
      targetId: 'default',
      provider: 'aws',
      engine: 'cloudformation',
      service: 'runtime',
      operation: 'DescribeEndpointPlacement',
      resourceType: RUNTIME_ENDPOINT_RESOURCE,
      input: { Slot: 'users' },
    };
    const eventsBeforeRead = context.core.events(context.worldId).length;
    const first = await context.core.executeCommandAsync(
      context.worldId,
      describe,
      'aws-native:test:stable-signature'
    );
    expect(context.core.events(context.worldId)).toHaveLength(eventsBeforeRead);

    const stackResources = execute(
      context,
      'cloudformation',
      'DescribeStackResources',
      {}
    )['StackResources'];
    expect(Array.isArray(stackResources)).toBe(true);
    expect(
      (stackResources as readonly Readonly<Record<string, unknown>>[]).some(
        (resource) => {
          const logicalId = resource['LogicalResourceId'];
          return (
            logicalId === 'ParticipantFunction.participant-unverified' ||
            logicalId === 'ParticipantManagedLambdaFunction.participant-users'
          );
        }
      )
    ).toBe(false);

    execute(context, 'cloudformation', 'UpdateStack', {
      TemplateBody: context.templateBody,
    });
    const afterUpdateEvents = context.core.events(context.worldId).length;
    expect(
      await context.core.executeCommandAsync(
        context.worldId,
        describe,
        'aws-native:test:stable-signature'
      )
    ).toEqual(first);
    expect(context.core.events(context.worldId)).toHaveLength(
      afterUpdateEvents
    );
    expect(
      context.store.idempotentResponse(
        `command:${context.worldId}:aws`,
        'aws-native:test:stable-signature'
      )
    ).toBeUndefined();
    const resources = context.core.resources(context.worldId);
    expect(
      resources.find((resource) => resource.resourceId === normal.resourceId)
    ).toMatchObject({
      deploymentId: context.deploymentId,
      targetId: 'default',
      status: 'ready',
    });
    expect(
      resources.find((resource) => resource.resourceId === managedResourceId)
    ).toMatchObject({
      deploymentId: context.deploymentId,
      targetId: 'default',
      status: 'ready',
    });
    const endpoint = resources.find(
      (resource) =>
        resource.resourceType === RUNTIME_ENDPOINT_RESOURCE &&
        resource.properties['Slot'] === 'users'
    );
    expect(endpoint?.properties['ManagedPlacement']).toEqual(first);
    expect(endpoint?.properties['state']).toEqual({});

    const metadata = {
      cfnParameters: { FlagSeed: 'fixture-seed' },
      endpoints: [
        {
          slot: 'users',
          default: {
            from: 'cfn-output',
            key: 'FunctionUrl',
            appendPath: '/changed-users',
          },
          overridable: true,
        },
        {
          slot: 'orders',
          default: {
            from: 'cfn-output',
            key: 'FunctionUrl',
            appendPath: '/orders',
          },
          overridable: true,
        },
        {
          slot: 'catalog',
          default: {
            from: 'cfn-output',
            key: 'FunctionUrl',
            appendPath: '/catalog',
          },
          overridable: true,
        },
      ],
    };
    expect(() =>
      execute(context, 'cloudformation', 'UpdateStack', {
        TemplateBody: context.templateBody,
        Metadata: metadata,
      })
    ).toThrow('bound runtime endpoint immutable shape cannot be changed');
    expect(() =>
      execute(context, 'cloudformation', 'UpdateStack', {
        TemplateBody: context.templateBody,
        Metadata: { ...metadata, endpoints: metadata.endpoints.slice(1) },
      })
    ).toThrow('bound runtime endpoint cannot be removed from the stack');
    expect(
      context.core
        .resources(context.worldId)
        .find((resource) => resource.resourceId === managedResourceId)?.status
    ).toBe('ready');
    expect(
      await context.core.executeCommandAsync(
        context.worldId,
        describe,
        'aws-native:test:stable-signature'
      )
    ).toEqual(first);

    const managed = context.core
      .resources(context.worldId)
      .find((resource) => resource.resourceId === managedResourceId);
    if (!managed) throw new Error('managed Lambda is missing');
    context.store.saveResource({ ...managed, status: 'failed' });
    await expect(
      context.core.executeCommandAsync(
        context.worldId,
        describe,
        'aws-native:test:stable-signature'
      )
    ).rejects.toThrow('managed resource is not ready');
  });

  it('participant-controlled IaC properties から managed eligibility を偽造できない', async () => {
    const context = await createContext(
      'managed-placement-metadata.json',
      'managed-placement-spoof',
      'managed-placement-spoof.yaml'
    );
    reviewedWorkload(context);
    const resource = context.core
      .resources(context.worldId)
      .find(
        (candidate) =>
          candidate.resourceType === LAMBDA_RESOURCE &&
          candidate.properties['logicalId'] === 'SpoofedFunction'
      );
    if (!resource) throw new Error('spoofed IaC Lambda is missing');
    expect(resource.properties['templateProperties']).toMatchObject({
      ParticipantCreated: true,
      EligibleManagedPlacement: true,
      PlacementSlot: 'users',
    });
    expect(resource.properties['ParticipantCreated']).toBeUndefined();
    expect(resource.properties['EligibleManagedPlacement']).toBeUndefined();
    expect(() => bind(context, 'users', resource.resourceId)).toThrow(
      'managed resource was not participant-created'
    );
  });

  it('ready でない reviewed workload を managed placement 候補にしない', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    const managedResourceId = resourceId(
      createLambda(context, 'participant-users', 'users')
    );
    const workload = context.core
      .resources(context.worldId)
      .find(
        (candidate) => candidate.resourceId === 'reviewed-migration-workload'
      );
    if (!workload) throw new Error('reviewed workload is missing');
    context.store.saveResource({ ...workload, status: 'failed' });
    expect(() => bind(context, 'users', managedResourceId)).toThrow(
      'reviewed workload endpoint is unavailable'
    );
  });

  it('同じ world と deployment の別 target resource を参照できない', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    const managedResourceId = resourceId(
      createEcs(context, 'participant-orders', 'orders')
    );
    const deployment = context.core.deployment(
      context.worldId,
      context.deploymentId
    );
    context.store.saveDeployment({
      ...deployment,
      targets: [
        ...deployment.targets,
        {
          id: 'other-target',
          provider: 'aws',
          engine: 'cloudformation',
        },
      ],
      outputs: { ...deployment.outputs, 'other-target': {} },
    });
    const endpoint = context.core
      .resources(context.worldId)
      .find(
        (candidate) =>
          candidate.resourceType === RUNTIME_ENDPOINT_RESOURCE &&
          candidate.properties['Slot'] === 'users'
      );
    const workload = context.core
      .resources(context.worldId)
      .find(
        (candidate) => candidate.resourceId === 'reviewed-migration-workload'
      );
    const declaration = workload?.properties['declaration'];
    if (!endpoint || !workload || !isRecord(declaration)) {
      throw new Error('default target graph is missing');
    }
    context.store.saveResource({
      ...endpoint,
      targetId: 'other-target',
      resourceId: 'other-target-users-endpoint',
      properties: { ...endpoint.properties, TargetId: 'other-target' },
    });
    context.store.saveResource({
      ...workload,
      targetId: 'other-target',
      resourceId: 'other-target-reviewed-workload',
      properties: {
        ...workload.properties,
        declaration: {
          ...declaration,
          targetId: 'other-target',
        },
      },
    });
    expect(() =>
      context.core.executeCommand(
        context.worldId,
        {
          ...bindCommand(context, 'users', managedResourceId),
          targetId: 'other-target',
        },
        'cross-target-bind'
      )
    ).toThrow('managed resource does not exist');
  });

  it('reviewed workload の image path endpoint と binding hash の改ざんを拒否する', async () => {
    for (const mutation of [
      {
        image:
          'GHCR.IO/tenkacloud/migration@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
      },
      { healthPath: '//attacker.example/path' },
    ]) {
      const context = await createContext('managed-placement-metadata.json');
      reviewedWorkload(context);
      const workload = context.core
        .resources(context.worldId)
        .find(
          (candidate) => candidate.resourceId === 'reviewed-migration-workload'
        );
      const declaration = workload?.properties['declaration'];
      if (!workload || !isRecord(declaration)) {
        throw new Error('reviewed workload is missing');
      }
      context.store.saveResource({
        ...workload,
        properties: {
          ...workload.properties,
          declaration: { ...declaration, ...mutation },
        },
      });
      expect(() =>
        createAppRunner(context, 'participant-catalog', 'catalog')
      ).toThrow('reviewed workload declaration is invalid');
    }

    const external = await createContext('managed-placement-metadata.json');
    reviewedWorkload(external, 'http://example.com:43123');
    expect(() =>
      createAppRunner(external, 'participant-catalog', 'catalog')
    ).toThrow('reviewed workload endpoint is invalid');

    const bound = await createContext('managed-placement-metadata.json');
    reviewedWorkload(bound);
    const managedResourceId = resourceId(
      createLambda(bound, 'participant-users', 'users')
    );
    bind(bound, 'users', managedResourceId);
    const managed = bound.core
      .resources(bound.worldId)
      .find((candidate) => candidate.resourceId === managedResourceId);
    if (!managed) throw new Error('managed resource is missing');
    bound.store.saveResource({
      ...managed,
      properties: {
        ...managed.properties,
        ReviewedArtifactHash: 'tampered',
      },
    });
    expect(() => placement(bound, 'users')).toThrow(
      'managed resource does not match the reviewed workload artifact'
    );
  });

  it('reviewed workload command を artifact identity に含め改ざんと不正値を拒否する', async () => {
    const binding = await createContext('managed-placement-metadata.json');
    reviewedWorkload(binding);
    const managedResourceId = resourceId(
      createLambda(binding, 'participant-users', 'users')
    );
    const workload = binding.core
      .resources(binding.worldId)
      .find(
        (candidate) => candidate.resourceId === 'reviewed-migration-workload'
      );
    const declaration = workload?.properties['declaration'];
    if (!workload || !isRecord(declaration)) {
      throw new Error('reviewed workload is missing');
    }
    binding.store.saveResource({
      ...workload,
      properties: {
        ...workload.properties,
        declaration: {
          ...declaration,
          command: ['bun', 'run', 'alternate'],
        },
      },
    });
    expect(() => bind(binding, 'users', managedResourceId)).toThrow(
      'managed resource does not match the reviewed workload artifact'
    );

    const described = await createContext('managed-placement-metadata.json');
    reviewedWorkload(described);
    const describedResourceId = resourceId(
      createLambda(described, 'participant-users', 'users')
    );
    bind(described, 'users', describedResourceId);
    const describedWorkload = described.core
      .resources(described.worldId)
      .find(
        (candidate) => candidate.resourceId === 'reviewed-migration-workload'
      );
    const describedDeclaration = describedWorkload?.properties['declaration'];
    if (!describedWorkload || !isRecord(describedDeclaration)) {
      throw new Error('described workload is missing');
    }
    described.store.saveResource({
      ...describedWorkload,
      properties: {
        ...describedWorkload.properties,
        declaration: {
          ...describedDeclaration,
          command: ['bun', 'run', 'alternate'],
        },
      },
    });
    expect(() => placement(described, 'users')).toThrow(
      'managed resource does not match the reviewed workload artifact'
    );

    for (const mutation of [
      { command: [] },
      { command: [''] },
      { command: ['contains\u0000nul'] },
      { command: Array.from({ length: 33 }, () => 'argument') },
      { command: ['x'.repeat(513)] },
      { targetId: 'other-target' },
      { participantControlled: true },
    ]) {
      const invalid = await createContext('managed-placement-metadata.json');
      reviewedWorkload(invalid);
      const invalidWorkload = invalid.core
        .resources(invalid.worldId)
        .find(
          (candidate) => candidate.resourceId === 'reviewed-migration-workload'
        );
      const invalidDeclaration = invalidWorkload?.properties['declaration'];
      if (!invalidWorkload || !isRecord(invalidDeclaration)) {
        throw new Error('invalid workload is missing');
      }
      invalid.store.saveResource({
        ...invalidWorkload,
        properties: {
          ...invalidWorkload.properties,
          declaration: { ...invalidDeclaration, ...mutation },
        },
      });
      expect(() =>
        createAppRunner(invalid, 'participant-catalog', 'catalog')
      ).toThrow('reviewed workload declaration is invalid');
    }
  });

  it('managed operation の name duplicate read lifecycle と reducer default を fail closed にする', async () => {
    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    createLambda(context, 'participant-users', 'users');
    expect(
      execute(
        context,
        'lambda',
        'DescribeManagedFunction',
        { FunctionName: 'participant-users' },
        LAMBDA_RESOURCE
      )
    ).toMatchObject({ ResourceName: 'participant-users', Slot: 'users' });
    expect(() => createLambda(context, 'participant-users', 'users')).toThrow(
      'managed resource already exists'
    );
    expect(() =>
      execute(
        context,
        'lambda',
        'DescribeManagedFunction',
        { FunctionName: 'missing' },
        LAMBDA_RESOURCE
      )
    ).toThrow('managed resource does not exist');
    expect(() => createLambda(context, 'invalid name', 'users')).toThrow(
      'FunctionName is invalid'
    );
    for (const input of [
      {
        ServiceName: 'invalid-count',
        Slot: 'orders',
        DesiredCount: 2,
        LaunchType: 'FARGATE',
      },
      {
        ServiceName: 'invalid-launch',
        Slot: 'orders',
        DesiredCount: 1,
        LaunchType: 'EC2',
      },
    ]) {
      expect(() =>
        execute(
          context,
          'ecs',
          'CreateManagedService',
          input,
          ECS_SERVICE_RESOURCE
        )
      ).toThrow('ECS managed service');
    }

    const provider = new AwsProvider();
    const view = providerWorld(context);
    for (const [service, operation, resourceType, message] of [
      [
        'lambda',
        'UnknownManagedLambda',
        LAMBDA_RESOURCE,
        'Lambda managed operation',
      ],
      ['ecs', 'UnknownEcs', ECS_SERVICE_RESOURCE, 'ECS operation'],
      [
        'apprunner',
        'UnknownAppRunner',
        APP_RUNNER_SERVICE_RESOURCE,
        'App Runner operation',
      ],
    ] as const) {
      expect(() =>
        provider.reduce(
          providerCommand(context, service, operation, resourceType, {}),
          view
        )
      ).toThrow(message);
    }
  });

  it('placement projection の malformed state immutable URL と operation field を拒否する', async () => {
    expect(
      compileRuntimeEndpoints({
        metadata: {
          endpoints: [
            {
              slot: 'nested',
              default: {
                from: 'cfn-output',
                key: 'NestedUrl',
                appendPath: '/healthz',
              },
              overridable: true,
            },
          ],
        },
        outputs: { NestedUrl: 'https://example.test/base' },
        problemId: 'nested-runtime',
        targetId: 'default',
        stackId: 'stack-id',
        stackName: 'stack-name',
      })[0]?.properties['DefaultUrl']
    ).toBe('https://example.test/base/healthz');

    const context = await createContext('managed-placement-metadata.json');
    reviewedWorkload(context);
    const managedResourceId = resourceId(
      createLambda(context, 'participant-users', 'users')
    );
    expect(() => placement(context, 'users')).toThrow(
      'runtime endpoint placement does not exist'
    );
    expect(() =>
      execute(
        context,
        'runtime',
        'BindManagedResource',
        {
          Slot: 'users',
          ManagedResourceId: managedResourceId,
          EndpointUrl: 'http://127.0.0.1:9999/spoof',
        },
        RUNTIME_ENDPOINT_RESOURCE
      )
    ).toThrow('Runtime BindManagedResource field EndpointUrl is not supported');
    bind(context, 'users', managedResourceId);
    expect(
      execute(
        context,
        'runtime',
        'ResolveEndpoint',
        { Slot: 'users' },
        RUNTIME_ENDPOINT_RESOURCE
      )
    ).toMatchObject({
      Source: 'managed-placement',
      Url: 'http://127.0.0.1:43123/users',
      VerifiedPlatform: 'lambda',
    });
    expect(() =>
      execute(
        context,
        'runtime',
        'DescribeEndpointPlacement',
        { Slot: 'users', Extra: true },
        RUNTIME_ENDPOINT_RESOURCE
      )
    ).toThrow('Runtime DescribeEndpointPlacement field Extra is not supported');

    const endpoint = context.core
      .resources(context.worldId)
      .find(
        (candidate) =>
          candidate.resourceType === RUNTIME_ENDPOINT_RESOURCE &&
          candidate.properties['Slot'] === 'users'
      );
    const rawPlacement = endpoint?.properties['ManagedPlacement'];
    if (!endpoint || !isRecord(rawPlacement)) {
      throw new Error('managed endpoint is missing');
    }
    context.store.saveResource({
      ...endpoint,
      properties: {
        ...endpoint.properties,
        ManagedPlacement: { ...rawPlacement, EffectiveUrl: 'tampered' },
      },
    });
    expect(() => placement(context, 'users')).toThrow(
      'managed endpoint placement no longer matches the resource graph'
    );
    context.store.saveResource({
      ...endpoint,
      properties: {
        ...endpoint.properties,
        ManagedPlacement: { ...rawPlacement, Unexpected: true },
      },
    });
    expect(() => placement(context, 'users')).toThrow(
      'managed endpoint placement is invalid'
    );
  });

  it('non-overridable slot invalid origin と direct target mismatch を拒否する', async () => {
    const fixed = await createContext('runtime-metadata.json');
    reviewedWorkload(fixed);
    const managedResourceId = resourceId(
      createLambda(fixed, 'participant-app', 'app')
    );
    expect(() => bind(fixed, 'fixed', managedResourceId)).toThrow(
      'runtime endpoint is not overridable'
    );

    const invalidOrigin = await createContext(
      'managed-placement-metadata.json'
    );
    reviewedWorkload(invalidOrigin, 'not-a-url');
    expect(() =>
      createLambda(invalidOrigin, 'participant-users', 'users')
    ).toThrow('reviewed workload endpoint is invalid');

    const provider = new AwsProvider();
    const direct = await createContext('managed-placement-metadata.json');
    reviewedWorkload(direct);
    expect(() =>
      provider.reduce(
        {
          ...providerCommand(
            direct,
            'runtime',
            'DescribeEndpointPlacement',
            RUNTIME_ENDPOINT_RESOURCE,
            { Slot: 'users' }
          ),
          targetId: 'other-target',
        },
        providerWorld(direct)
      )
    ).toThrow('runtime endpoint does not exist');
  });
});
