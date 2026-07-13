import { describe, expect, it } from 'bun:test';
import {
  CoreError,
  type ProviderCompileInput,
  type ProviderWorldView,
  type ResourceRecord,
} from '@tenkacloud/simulator-core';
import {
  AWS_CATALOG_CAPABILITY_MANIFEST,
  catalogCapabilityIdentity,
  unsupportedCatalogIdentities,
} from '../src/catalog-manifest';
import { compileCloudFormation } from '../src/cloudformation';
import { reduceEc2 } from '../src/ec2';
import { reduceElb } from '../src/elb';
import { reduceIam } from '../src/iam';
import * as publicApi from '../src/index';
import { reduceLambda } from '../src/lambda';
import { reduceLogs } from '../src/logs';
import {
  declaration,
  isCloudFormationResourceType,
  STACK_RESOURCE,
} from '../src/model';
import { reduceRuntime } from '../src/runtime';
import {
  customResourceObjects,
  stackProperties,
  storedProperties,
} from '../src/state';
import {
  booleanValue,
  errorMessage,
  numberValue,
  objectValue,
  optionalObject,
  optionalString,
  optionalStringArray,
  stringArray,
  stringValue,
} from '../src/value';
import { reduceWaf } from '../src/waf';

function compile(templateBody: string, metadata?: unknown) {
  const input: ProviderCompileInput = {
    target: {
      provider: 'aws',
      engine: 'cloudformation',
      entry: 'boundary.yaml',
    },
    targetId: 'default',
    problemId: 'boundary',
    templateBody,
    artifacts: [],
    ...(metadata === undefined ? {} : { metadata }),
  };
  return compileCloudFormation(input);
}

function resource(
  resourceType: string,
  logicalId: string,
  templateProperties: Readonly<Record<string, unknown>>,
  state: Readonly<Record<string, unknown>> = {}
): ResourceRecord {
  return {
    worldId: 'world',
    deploymentId: 'deployment',
    targetId: 'default',
    provider: 'aws',
    resourceType,
    resourceId: `resource-${logicalId}`,
    status: 'ready',
    properties: {
      logicalId,
      physicalId: logicalId,
      refValue: logicalId,
      dependsOn: [],
      attributes: {},
      templateProperties,
      status: 'CREATE_COMPLETE',
      state,
    },
  };
}

function world(resources: readonly ResourceRecord[]): ProviderWorldView {
  return {
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
    resources,
  };
}

describe('AWS provider boundary', () => {
  it('scanner互換manifestはdeployと実装済みoperationだけをfidelity付きで公開する', () => {
    expect(publicApi.AwsProvider).toBeFunction();
    expect(AWS_CATALOG_CAPABILITY_MANIFEST.schemaVersion).toBe('1');
    expect(
      AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.some(
        (capability) =>
          capability.resourceType === STACK_RESOURCE &&
          capability.operation === 'deploy'
      )
    ).toBe(true);
    expect(
      AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.find(
        (capability) =>
          capability.service === 'lambda' &&
          capability.operation === 'InvokeFunction'
      )?.fidelity
    ).toEqual(['L0', 'L1', 'L4']);
    expect(
      AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.find(
        (capability) =>
          capability.service === 'lambda' &&
          capability.operation === 'CreateFunction'
      )?.fidelity
    ).toEqual(['L0', 'L1']);
    expect(
      AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.find(
        (capability) =>
          capability.resourceType === 'AWS::Lambda::Function' &&
          capability.operation === 'lifecycle'
      )?.fidelity
    ).toEqual(['L1']);
    expect(
      AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.find(
        (capability) =>
          capability.resourceType === 'AWS::IAM::Role' &&
          capability.operation === 'lifecycle'
      )?.fidelity
    ).toEqual(['L1', 'L2']);
    expect(
      AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.find(
        (capability) =>
          capability.resourceType === 'AWS::EC2::Route' &&
          capability.operation === 'lifecycle'
      )?.fidelity
    ).toEqual(['L1', 'L3']);
    expect(
      AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.find(
        (capability) =>
          capability.resourceType ===
            'AWS::ElasticLoadBalancingV2::ListenerRule' &&
          capability.operation === 'lifecycle'
      )?.fidelity
    ).toEqual(['L1', 'L3']);
    expect(
      AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.find(
        (capability) =>
          capability.resourceType === 'AWS::WAFv2::WebACL' &&
          capability.operation === 'lifecycle'
      )?.fidelity
    ).toEqual(['L1', 'L3']);
    for (const resourceType of [
      'AWS::CloudFormation::CustomResource',
      'Custom::EmptyStackBuckets',
    ]) {
      expect(
        AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.find(
          (capability) =>
            capability.resourceType === resourceType &&
            capability.operation === 'lifecycle'
        )?.fidelity
      ).toEqual(['L1', 'L4']);
    }
    expect(
      AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.find(
        (capability) =>
          capability.service === 's3' &&
          capability.operation === 'GetBucketLocation'
      )?.fidelity
    ).toEqual(['L0', 'L1', 'L4']);
    expect(
      AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.find(
        (capability) =>
          capability.service === 'sts' &&
          capability.operation === 'GetCallerIdentity'
      )?.fidelity
    ).toEqual(['L0', 'L1', 'L2']);
    expect(
      AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.find(
        (capability) =>
          capability.service === 'runtime' &&
          capability.resourceType === 'Runtime::Endpoint' &&
          capability.operation === 'ResolveEndpoint'
      )?.fidelity
    ).toEqual(['L0', 'L1']);
    expect(
      new Set(
        AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.map(
          catalogCapabilityIdentity
        )
      ).size
    ).toBe(AWS_CATALOG_CAPABILITY_MANIFEST.capabilities.length);
  });

  it('不足identityをmissingとinsufficientに集約しproblemを決定順で返す', () => {
    const unsupported = unsupportedCatalogIdentities([
      {
        provider: 'aws',
        engine: 'cloudformation',
        service: 'lambda',
        resourceType: '*',
        operation: 'CreateFunction',
        fidelity: ['L4'],
        problemId: 'z-problem',
      },
      {
        provider: 'aws',
        engine: 'cloudformation',
        service: 'lambda',
        resourceType: '*',
        operation: 'CreateFunction',
        fidelity: ['L4'],
        problemId: 'a-problem',
      },
      {
        provider: 'aws',
        engine: 'cloudformation',
        service: 'ecs',
        resourceType: '*',
        operation: 'RunTask',
        fidelity: ['L1'],
        problemId: 'migration',
      },
      {
        provider: 'aws',
        engine: 'cloudformation',
        service: 'ssm',
        resourceType: '*',
        operation: 'GetParameter',
        fidelity: ['L2'],
      },
    ]);
    expect(unsupported).toEqual([
      {
        identity: 'aws|cloudformation|ecs|*|RunTask',
        status: 'missing',
        requiredFidelity: ['L1'],
        problemIds: ['migration'],
      },
      {
        identity: 'aws|cloudformation|lambda|*|CreateFunction',
        status: 'insufficient',
        requiredFidelity: ['L4'],
        availableFidelity: ['L0', 'L1'],
        problemIds: ['a-problem', 'z-problem'],
      },
    ]);
  });

  it('EC2 lifecycle は network の大小ではなく明示した control membership で coverage を満たす', () => {
    const requirement = {
      provider: 'aws',
      engine: 'cloudformation',
      service: 'ec2',
      resourceType: 'AWS::EC2::Instance',
      operation: 'lifecycle',
      fidelity: ['L1'],
      problemId: 'control-plane-instance',
    } as const;

    expect(unsupportedCatalogIdentities([requirement])).toEqual([]);
    expect(
      unsupportedCatalogIdentities([requirement], {
        schemaVersion: '1',
        version: 'network-only-fixture',
        capabilities: [
          {
            provider: 'aws',
            engine: 'cloudformation',
            service: 'ec2',
            resourceType: 'AWS::EC2::Instance',
            operation: 'lifecycle',
            fidelity: ['L3'],
          },
        ],
      })
    ).toEqual([
      {
        identity: 'aws|cloudformation|ec2|AWS::EC2::Instance|lifecycle',
        status: 'insufficient',
        requiredFidelity: ['L1'],
        availableFidelity: ['L3'],
        problemIds: ['control-plane-instance'],
      },
    ]);
  });

  it('required parameterとpseudo parameterを決定値へ解決する', () => {
    const plan = compile(`Parameters:
  NamePrefix: { Type: String }
  TenkaCloudAccountId: { Type: String }
  ExternalId: { Type: String }
  DbPassword: { Type: String }
  AmiId: { Type: "AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>" }
  Generic: { Type: String }
Resources:
  Parameter:
    Type: AWS::SSM::Parameter
    Properties:
      Name: !Sub "/\${NamePrefix}/\${Generic}"
      Type: String
      Value: !Join [":", [!Ref TenkaCloudAccountId, !Ref ExternalId, !Ref DbPassword, !Ref AmiId]]
Outputs:
  Stack:
    Value: !Sub "\${AWS::StackName}-\${AWS::Partition}-\${AWS::URLSuffix}"
`);
    const parameter = plan.resources.find(
      (candidate) => candidate.properties['logicalId'] === 'Parameter'
    );
    expect(parameter?.properties['templateProperties']).toMatchObject({
      Type: 'String',
    });
    const outputs = objectValue(
      plan.resources[0]?.properties['outputs'],
      'outputs'
    );
    expect(outputs['Stack']).toContain('boundary-default-aws-amazonaws.com');
  });

  it('NoValue escaped Sub unknown variable GetAtt配列を安全に処理する', () => {
    const plan = compile(`Parameters:
  Remove: { Type: String, Default: remove }
Conditions:
  IsRemove: !Equals [!Ref Remove, remove]
Resources:
  Bucket:
    Type: AWS::S3::Bucket
    Properties:
      Tags:
        - !If [IsRemove, !Ref AWS::NoValue, { Key: keep, Value: yes }]
      MetadataValue: !Sub "\${!literal}-\${SHELL_VALUE}"
  Parameter:
    Type: AWS::SSM::Parameter
    Properties:
      Name: /value
      Type: String
      Value: ok
Outputs:
  Value:
    Value: !GetAtt [Parameter, Value]
`);
    const bucket = plan.resources.find(
      (candidate) => candidate.properties['logicalId'] === 'Bucket'
    );
    const templateProperties = objectValue(
      bucket?.properties['templateProperties'],
      'templateProperties'
    );
    expect(templateProperties['Tags']).toEqual([]);
    expect(templateProperties['MetadataValue']).toBe(
      ['$', '{literal}-$', '{SHELL_VALUE}'].join('')
    );
  });

  it('conditionとintrinsicの不正shapeをすべてloudに拒否する', () => {
    const invalid = [
      `Conditions:\n  Bad: { Fn::Equals: [a] }\nResources:\n  A: { Type: AWS::S3::Bucket }`,
      `Conditions:\n  Bad: { Fn::Equals: [a, a, a] }\nResources:\n  A: { Type: AWS::S3::Bucket }`,
      `Conditions:\n  Bad: { Fn::Not: [true, false] }\nResources:\n  A: { Type: AWS::S3::Bucket }`,
      `Conditions:\n  Bad: { Fn::And: [true] }\nResources:\n  A: { Type: AWS::S3::Bucket }`,
      `Conditions:\n  Bad: { Unsupported: true }\nResources:\n  A: { Type: AWS::S3::Bucket }`,
      `Resources:\n  A: { Type: AWS::S3::Bucket }\nOutputs:\n  X: { Value: { Ref: Missing } }`,
      `Resources:\n  A: { Type: AWS::S3::Bucket }\nOutputs:\n  X: { Value: { Fn::GetAtt: 3 } }`,
      `Resources:\n  A: { Type: AWS::S3::Bucket }\nOutputs:\n  X: { Value: { Fn::GetAtt: Missing.Arn } }`,
      `Resources:\n  A: { Type: AWS::S3::Bucket }\nOutputs:\n  X: { Value: { Fn::Sub: [bad] } }`,
      `Resources:\n  A: { Type: AWS::S3::Bucket }\nOutputs:\n  X: { Value: { Fn::Join: [",", [{ x: y }]] } }`,
      `Resources:\n  A: { Type: AWS::S3::Bucket }\nOutputs:\n  X: { Value: { Fn::Select: bad } }`,
      `Resources:\n  A: { Type: AWS::S3::Bucket }\nOutputs:\n  X: { Value: { Fn::Base64: { x: y } } }`,
      `Resources:\n  A: { Type: AWS::S3::Bucket }\nOutputs:\n  X: { Value: { Fn::If: bad } }`,
      `Resources:\n  A: { Type: AWS::S3::Bucket }\nOutputs:\n  X: { Value: { Fn::Unknown: bad } }`,
      `Resources:\n  A:\n    Type: AWS::S3::Bucket\n    DependsOn: Missing`,
    ];
    for (const body of invalid) expect(() => compile(body)).toThrow(CoreError);
  });

  it('state helperとvalue parserの不正入力をvalidation errorにする', () => {
    expect(isCloudFormationResourceType('AWS::S3::Bucket')).toBe(true);
    expect(isCloudFormationResourceType('AWS::DynamoDB::Table')).toBe(false);
    expect(
      declaration({ resourceType: 'X', resourceId: 'id', properties: {} })
        .provider
    ).toBe('aws');
    expect(optionalObject(undefined, 'optional')).toBeUndefined();
    expect(optionalString(undefined, 'optional')).toBeUndefined();
    expect(optionalStringArray(undefined, 'optional')).toBeUndefined();
    expect(errorMessage('plain')).toBe('plain');
    expect(errorMessage(new Error('error'))).toBe('error');
    for (const operation of [
      () => objectValue([], 'object'),
      () => stringValue('', 'string'),
      () => stringArray([1], 'array'),
      () => numberValue(Number.NaN, 'number'),
      () => booleanValue('true', 'boolean'),
      () => storedProperties({ properties: {} }),
    ]) {
      expect(operation).toThrow(CoreError);
    }
    const stack = resource(STACK_RESOURCE, 'stack', {});
    const stackBase = {
      ...stack.properties,
      problemId: 'problem',
      targetId: 'default',
      entry: 'template.yaml',
      templateBody: 'Resources: {}',
      resourceLogicalIds: [],
    };
    expect(
      stackProperties({ properties: { ...stackBase, outputs: { Empty: '' } } })
        .outputs['Empty']
    ).toBe('');
    expect(() =>
      stackProperties({ properties: { ...stackBase, outputs: { Invalid: 3 } } })
    ).toThrow('stack output Invalid must be a string');
  });

  it('壊れたservice stateは reducerで成功扱いしない', () => {
    const securityGroup = resource(
      'AWS::EC2::SecurityGroup',
      'sg-id',
      { GroupName: 'group' },
      { ipPermissions: 'invalid' }
    );
    expect(() =>
      reduceEc2(
        {
          worldId: 'world',
          deploymentId: 'deployment',
          service: 'ec2',
          operation: 'RevokeSecurityGroupIngress',
          resourceType: '*',
          input: { GroupId: 'sg-id', IpPermissions: [] },
        },
        world([securityGroup])
      )
    ).toThrow('rule state is invalid');
    const firstRole = resource('AWS::IAM::Role', 'z-role', {});
    const secondRole = resource('AWS::IAM::Role', 'a-role', {});
    expect(
      Reflect.get(
        reduceIam(
          {
            worldId: 'world',
            deploymentId: 'deployment',
            service: 'iam',
            operation: 'ListRoles',
            resourceType: '*',
            input: {},
          },
          world([firstRole, secondRole])
        ).response,
        'Roles'
      )
    ).toHaveLength(2);
    const role = resource('AWS::IAM::Role', 'role', { Policies: {} });
    expect(() =>
      reduceIam(
        {
          worldId: 'world',
          deploymentId: 'deployment',
          service: 'iam',
          operation: 'ListRolePolicies',
          resourceType: '*',
          input: { RoleName: 'role' },
        },
        world([role])
      )
    ).toThrow('Policies must be an array');
    const validStream = resource('AWS::Logs::LogStream', 'stream', {
      LogGroupName: 'group',
      LogStreamName: 'stream',
    });
    const stream: ResourceRecord = {
      ...validStream,
      properties: {
        ...validStream.properties,
        LogGroupName: 'group',
        LogStreamName: 'stream',
        events: 'invalid',
      },
    };
    expect(() =>
      reduceLogs(
        {
          worldId: 'world',
          deploymentId: 'deployment',
          service: 'logs',
          operation: 'GetLogEvents',
          resourceType: '*',
          input: { LogGroupName: 'group' },
        },
        world([stream])
      )
    ).toThrow('stored log events are invalid');
    expect(() =>
      reduceLogs(
        {
          worldId: 'world',
          deploymentId: 'deployment',
          service: 'logs',
          operation: 'PutLogEvents',
          resourceType: '*',
          input: {
            LogGroupName: 'group',
            LogStreamName: 'stream',
            LogEvents: [{ timestamp: 1, message: 'message' }],
          },
        },
        world([stream])
      )
    ).toThrow('stored log events are invalid');
  });

  it('ELB sort と WAF empty association state の境界を処理する', () => {
    const firstRule = resource(
      'AWS::ElasticLoadBalancingV2::ListenerRule',
      'z-rule',
      { Priority: 20 }
    );
    const secondRule = resource(
      'AWS::ElasticLoadBalancingV2::ListenerRule',
      'a-rule',
      { Priority: 10 }
    );
    expect(
      Reflect.get(
        reduceElb(
          {
            worldId: 'world',
            deploymentId: 'deployment',
            service: 'elasticloadbalancing',
            operation: 'DescribeRules',
            resourceType: '*',
            input: {},
          },
          world([firstRule, secondRule])
        ).response,
        'Rules'
      )
    ).toHaveLength(2);
    const webAcl = {
      ...resource(
        'AWS::WAFv2::WebACL',
        'acl',
        { Name: 'acl' },
        { associatedResources: 'invalid' }
      ),
      properties: {
        ...resource('AWS::WAFv2::WebACL', 'acl', { Name: 'acl' }).properties,
        attributes: { Arn: 'arn:acl', Id: 'acl-id' },
        state: { associatedResources: 'invalid' },
      },
    };
    const target = {
      ...resource('AWS::ElasticLoadBalancingV2::LoadBalancer', 'lb', {}),
      properties: {
        ...resource('AWS::ElasticLoadBalancingV2::LoadBalancer', 'lb', {})
          .properties,
        attributes: { LoadBalancerArn: 'arn:lb' },
      },
    };
    expect(() =>
      reduceWaf(
        {
          worldId: 'world',
          deploymentId: 'deployment',
          service: 'wafv2',
          operation: 'AssociateWebACL',
          resourceType: '*',
          input: { WebACLArn: 'arn:acl', ResourceArn: 'arn:lb' },
        },
        world([webAcl, target])
      )
    ).not.toThrow();
  });

  it('custom resource不足とLambda payload handler不足を明示する', () => {
    const incomplete = resource(
      'AWS::CloudFormation::CustomResource',
      'custom',
      { Bucket: 'bucket' }
    );
    expect(
      customResourceObjects([incomplete], '2026-07-12T00:00:00.000Z')
    ).toEqual([]);
    const lambda = resource('AWS::Lambda::Function', 'UnknownFunction', {
      Runtime: 'nodejs22.x',
      Handler: 'index.handler',
    });
    expect(() =>
      reduceLambda(
        {
          worldId: 'world',
          deploymentId: 'deployment',
          service: 'lambda',
          operation: 'InvokeFunction',
          resourceType: '*',
          input: { FunctionName: 'UnknownFunction', Payload: '{' },
        },
        world([lambda])
      )
    ).toThrow('Payload must contain valid JSON');
    expect(() =>
      reduceLambda(
        {
          worldId: 'world',
          deploymentId: 'deployment',
          service: 'lambda',
          operation: 'InvokeFunction',
          resourceType: '*',
          input: { FunctionName: 'UnknownFunction', Payload: {} },
        },
        world([lambda])
      )
    ).toThrow('handler UnknownFunction');
  });

  it('runtime endpoint metadata の不正source output URL 重複をloudに拒否する', () => {
    const body = `Resources:
  Bucket:
    Type: AWS::S3::Bucket
Outputs:
  Web:
    Value: https://example.test/
  Invalid:
    Value: not-a-url
  Ftp:
    Value: ftp://example.test/
`;
    const endpoint = (
      slot: string,
      key: string,
      from = 'cfn-output',
      appendPath: string | undefined = undefined
    ) => ({
      slot,
      default: {
        from,
        key,
        ...(appendPath === undefined ? {} : { appendPath }),
      },
      overridable: true,
    });
    const invalidMetadata = [
      { endpoints: {} },
      { endpoints: [endpoint('app', 'Invalid', 'cfn-output', '/health')] },
      { endpoints: [endpoint('app', 'Ftp', 'cfn-output', '/health')] },
      { endpoints: [endpoint('app', 'Web'), endpoint('app', 'Web')] },
      { endpoints: [endpoint('app', 'Web', 'external')] },
      { endpoints: [endpoint('app', 'Missing')] },
    ];
    for (const metadata of invalidMetadata) {
      expect(() => compile(body, metadata)).toThrow(CoreError);
    }
    expect(() =>
      reduceRuntime(
        {
          worldId: 'world',
          deploymentId: 'deployment',
          service: 'runtime',
          operation: 'Unknown',
          resourceType: 'Runtime::Endpoint',
          input: {},
        },
        world([])
      )
    ).toThrow('Runtime operation Unknown');
  });
});
