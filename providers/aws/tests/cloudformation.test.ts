import { afterEach, describe, expect, it } from 'bun:test';
import {
  CoreError,
  ProviderRegistry,
  type ProviderWorldView,
} from '@tenkacloud/simulator-core';
import {
  compileCloudFormation,
  parseCloudFormationTemplate,
} from '../src/cloudformation';
import { deployCloudFormation } from '../src/deploy';
import { CLOUDFORMATION_RESOURCE_TYPES, STACK_RESOURCE } from '../src/model';
import { AwsProvider } from '../src/provider';
import {
  cleanupContexts,
  createContext,
  execute,
  fixtureBody,
} from './support';

afterEach(cleanupContexts);

function compile(body: string) {
  return compileCloudFormation({
    target: {
      provider: 'aws',
      engine: 'cloudformation',
      entry: 'fixture.yaml',
    },
    targetId: 'default',
    problemId: 'fixture',
    templateBody: body,
    artifacts: [],
    metadata: { cfnParameters: { FlagSeed: '__RANDOM_PASSWORD__' } },
  });
}

describe('CloudFormation compiler', () => {
  it('package fixture の26 resource typeを依存順かつ決定的に宣言する', async () => {
    const body = await fixtureBody();
    const first = compile(body);
    const second = compile(body);
    expect(first).toEqual(second);
    expect(
      [
        ...new Set(
          first.resources.slice(1).map((resource) => resource.resourceType)
        ),
      ].sort()
    ).toEqual([...CLOUDFORMATION_RESOURCE_TYPES].sort());
    const logicalIds = first.resources.map(
      (resource) => resource.properties['logicalId']
    );
    expect(logicalIds.indexOf('Vpc')).toBeLessThan(
      logicalIds.indexOf('Subnet')
    );
    expect(logicalIds.indexOf('GatewayAttachment')).toBeLessThan(
      logicalIds.indexOf('Route')
    );
  });

  it('Ref Sub GetAtt Join Select Base64 If と Outputs を解決する', async () => {
    const plan = compile(await fixtureBody());
    const stack = plan.resources[0];
    expect(stack?.resourceType).toBe(STACK_RESOURCE);
    expect(stack?.properties['outputs']).toMatchObject({
      BucketName: 'tc-fixture-bucket-000000000000',
      BucketArn: 'arn:aws:s3:::tc-fixture-bucket-000000000000',
      Joined: 'tc-fixture:prod',
      Selected: 'one',
      Encoded: 'aGVsbG8=',
      Conditional: 'production',
      Substituted: 'mapped-tc-fixture-bucket-000000000000-us-east-1',
    });
    const outputs = stack?.properties['outputs'];
    if (outputs === null || typeof outputs !== 'object') {
      throw new Error('outputs are missing');
    }
    expect(String(Reflect.get(outputs, 'FunctionUrl'))).toContain(
      '.lambda-url.us-east-1.on.aws/'
    );
    const instance = plan.resources.find(
      (resource) => resource.properties['logicalId'] === 'Instance'
    );
    expect(instance?.properties['dependsOn']).toEqual([
      'InstanceProfile',
      'Route',
      'RouteAssociation',
      'SecurityGroup',
      'Subnet',
    ]);
  });

  it('real SQLite worldへdeployし custom resourceのS3 objectも保存する', async () => {
    const context = await createContext();
    const resources = context.store.resources(context.worldId);
    expect(
      resources.some((resource) => resource.resourceType === STACK_RESOURCE)
    ).toBe(true);
    expect(
      resources.find((resource) => resource.resourceType === 'AWS::S3::Object')
        ?.properties['Key']
    ).toBe('tool.sh');
    expect(
      resources.every(
        (resource) => resource.properties['status'] !== 'CREATE_PENDING'
      )
    ).toBe(true);
    expect(
      context.core.deployment(context.worldId, context.deploymentId).outputs[
        'default'
      ]
    ).toMatchObject({ BucketName: 'tc-fixture-bucket-000000000000' });
  });

  it('CloudFormation describe list resources と updateを実stateへ反映する', async () => {
    const context = await createContext();
    const described = execute(context, 'cloudformation', 'DescribeStacks', {});
    expect(Reflect.get(described, 'Stacks')).toHaveLength(1);
    expect(() =>
      execute(context, 'cloudformation', 'DescribeStacks', {
        StackName: 'missing',
      })
    ).toThrow('stack does not exist');
    const listed = execute(context, 'cloudformation', 'ListStacks', {});
    expect(Reflect.get(listed, 'StackSummaries')).toHaveLength(1);
    expect(
      Reflect.get(
        execute(context, 'cloudformation', 'DescribeStackResources', {}),
        'StackResources'
      )
    ).toBeArray();
    expect(
      Reflect.get(
        execute(context, 'cloudformation', 'ListStackResources', {}),
        'StackResourceSummaries'
      )
    ).toBeArray();
    const updateBody = await fixtureBody('update-stack.yaml');
    execute(context, 'cloudformation', 'UpdateStack', {
      TemplateBody: updateBody,
    });
    const resources = context.store.resources(context.worldId);
    expect(
      resources.some(
        (resource) =>
          resource.properties['logicalId'] === 'ReplacementParameter'
      )
    ).toBe(true);
    expect(
      resources.find((resource) => resource.properties['logicalId'] === 'Vpc')
        ?.status
    ).toBe('deleted');
    expect(
      context.core.deployment(context.worldId, context.deploymentId).outputs[
        'default'
      ]
    ).toMatchObject({ Updated: 'updated' });
  });

  it('壊れたYAML 未対応type 空Resources dependency cycleを明示的に拒否する', async () => {
    expect(() => parseCloudFormationTemplate('Resources: [')).toThrow(
      'CloudFormation YAML is invalid'
    );
    const invalid = await fixtureBody('invalid.yaml');
    expect(() => compile(invalid)).toThrow('AWS::DynamoDB::Table');
    expect(() => compile('Resources: {}')).toThrow(
      'Resources must not be empty'
    );
    expect(() =>
      compile(`Resources:
  A:
    Type: AWS::S3::Bucket
    DependsOn: B
  B:
    Type: AWS::S3::Bucket
    DependsOn: A
`)
    ).toThrow('dependency cycle');
  });

  it('不正なresource属性 condition output intrinsicをvalidation errorにする', () => {
    const invalidDocuments = [
      `Resources:\n  A:\n    Type: AWS::S3::Bucket\n    DependsOn: 3\n`,
      `Resources:\n  A:\n    Type: AWS::S3::Bucket\n    Condition: 3\n`,
      `Resources:\n  A:\n    Type: AWS::S3::Bucket\nOutputs:\n  Missing: {}\n`,
      `Resources:\n  A:\n    Type: AWS::S3::Bucket\nOutputs:\n  Bad:\n    Condition: 3\n    Value: x\n`,
      `Resources:\n  A:\n    Type: AWS::S3::Bucket\nOutputs:\n  Bad:\n    Value: !GetAtt A.Unknown\n`,
      `Resources:\n  A:\n    Type: AWS::S3::Bucket\nOutputs:\n  Bad:\n    Value: { Fn::Join: bad }\n`,
      `Resources:\n  A:\n    Type: AWS::S3::Bucket\nOutputs:\n  Bad:\n    Value: { Fn::Select: [4, [one]] }\n`,
      `Resources:\n  A:\n    Type: AWS::S3::Bucket\nOutputs:\n  Bad:\n    Value: { Fn::If: [Missing, one, two] }\n`,
    ];
    for (const body of invalidDocuments)
      expect(() => compile(body)).toThrow(CoreError);
  });

  it('Provider engine と deploy plan boundaryを拒否する', async () => {
    const provider = new AwsProvider();
    expect(() =>
      provider.compile({
        target: { provider: 'aws', engine: 'other', entry: 'x' },
        targetId: 'default',
        problemId: 'problem',
        templateBody: 'Resources: {}',
        artifacts: [],
      })
    ).toThrow('AWS engine other');
    const world: ProviderWorldView = {
      world: {
        worldId: 'world',
        tenantId: 'tenant',
        eventId: 'event',
        teamId: 'team',
        deploymentId: 'deployment',
        seed: 'seed',
        virtualTime: '2026-07-12T00:00:00.000Z',
        status: 'active',
      },
      resources: [],
    };
    const validBody = await fixtureBody('update-stack.yaml');
    const validPlan = provider.compile({
      target: {
        provider: 'aws',
        engine: 'cloudformation',
        entry: 'update-stack.yaml',
      },
      targetId: 'default',
      problemId: 'valid',
      templateBody: validBody,
      artifacts: [],
    });
    expect(provider.deploy(validPlan, world).resources).not.toHaveLength(0);
    expect(() =>
      deployCloudFormation(
        {
          targetId: 'default',
          provider: 'gcp',
          engine: 'cloudformation',
          requirements: [],
          resources: [],
        },
        world
      )
    ).toThrow('target is invalid');
    expect(() =>
      deployCloudFormation(
        {
          targetId: 'default',
          provider: 'aws',
          engine: 'cloudformation',
          requirements: [],
          resources: [],
        },
        world
      )
    ).toThrow('has no stack');
    expect(() => new ProviderRegistry([provider, provider])).toThrow(
      'already registered'
    );
  });
});
