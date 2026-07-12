import type {
  FidelityLevel,
  ProviderCapability,
  ResourceDeclaration,
} from '@tenkacloud/simulator-core';

export const AWS_PROVIDER = 'aws';
export const CLOUDFORMATION_ENGINE = 'cloudformation';
export const STACK_RESOURCE = 'AWS::CloudFormation::Stack';
export const OBJECT_RESOURCE = 'AWS::S3::Object';
export const LOG_STREAM_RESOURCE = 'AWS::Logs::LogStream';
export const COMMAND_RESOURCE = 'AWS::SSM::CommandInvocation';
export const SSM_COMMAND_RESOURCE = 'AWS::SSM::Command';
export const SSM_SESSION_RESOURCE = 'AWS::SSM::Session';
export const RUNTIME_ENDPOINT_RESOURCE = 'Runtime::Endpoint';
export const HTTP_ENDPOINT_RESOURCE = 'HTTP::Endpoint';

export const CLOUDFORMATION_RESOURCE_TYPES = [
  'AWS::CloudFormation::CustomResource',
  'AWS::EC2::Instance',
  'AWS::EC2::InternetGateway',
  'AWS::EC2::Route',
  'AWS::EC2::RouteTable',
  'AWS::EC2::SecurityGroup',
  'AWS::EC2::Subnet',
  'AWS::EC2::SubnetRouteTableAssociation',
  'AWS::EC2::VPC',
  'AWS::EC2::VPCGatewayAttachment',
  'AWS::ElasticLoadBalancingV2::Listener',
  'AWS::ElasticLoadBalancingV2::ListenerRule',
  'AWS::ElasticLoadBalancingV2::LoadBalancer',
  'AWS::ElasticLoadBalancingV2::TargetGroup',
  'AWS::IAM::InstanceProfile',
  'AWS::IAM::Role',
  'AWS::Lambda::Function',
  'AWS::Lambda::Permission',
  'AWS::Lambda::Url',
  'AWS::Logs::LogGroup',
  'AWS::RDS::DBInstance',
  'AWS::RDS::DBSubnetGroup',
  'AWS::S3::Bucket',
  'AWS::SSM::Parameter',
  'AWS::WAFv2::WebACL',
  'Custom::EmptyStackBuckets',
] as const;

export type CloudFormationResourceType =
  (typeof CLOUDFORMATION_RESOURCE_TYPES)[number];

const RESOURCE_TYPE_SET = new Set<string>(CLOUDFORMATION_RESOURCE_TYPES);

export function isCloudFormationResourceType(
  value: string
): value is CloudFormationResourceType {
  return RESOURCE_TYPE_SET.has(value);
}

export interface StoredResourceProperties {
  readonly [key: string]: unknown;
  readonly logicalId: string;
  readonly physicalId: string;
  readonly refValue: string;
  readonly dependsOn: readonly string[];
  readonly attributes: Readonly<Record<string, unknown>>;
  readonly templateProperties: Readonly<Record<string, unknown>>;
  readonly status: string;
}

export interface StackProperties extends StoredResourceProperties {
  readonly problemId: string;
  readonly targetId: string;
  readonly entry: string;
  readonly templateBody: string;
  readonly metadata?: unknown;
  readonly outputs: Readonly<Record<string, string>>;
  readonly resourceLogicalIds: readonly string[];
}

interface OperationCapability {
  readonly service: string;
  readonly operation: string;
  readonly resourceType: string;
  readonly fidelity: readonly FidelityLevel[];
}

const OPERATION_CAPABILITIES: readonly OperationCapability[] = [
  {
    service: 'cloudformation',
    operation: 'DescribeStacks',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'cloudformation',
    operation: 'ListStacks',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'cloudformation',
    operation: 'DescribeStackResources',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'cloudformation',
    operation: 'ListStackResources',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'cloudformation',
    operation: 'UpdateStack',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'iam',
    operation: 'GetRole',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    service: 'iam',
    operation: 'ListRoles',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    service: 'iam',
    operation: 'GetRolePolicy',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    service: 'iam',
    operation: 'ListRolePolicies',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    service: 'iam',
    operation: 'PutRolePolicy',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    service: 'iam',
    operation: 'DeleteRolePolicy',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    service: 'ssm',
    operation: 'GetParameter',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    service: 'ssm',
    operation: 'GetParameters',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    service: 'ssm',
    operation: 'GetParametersByPath',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    service: 'ssm',
    operation: 'DescribeParameters',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    service: 'ssm',
    operation: 'PutParameter',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    service: 'ssm',
    operation: 'SendCommand',
    resourceType: 'AWS::EC2::Instance',
    fidelity: ['L0', 'L1', 'L4'],
  },
  {
    service: 'ssm',
    operation: 'SendCommand',
    resourceType: SSM_COMMAND_RESOURCE,
    fidelity: ['L0', 'L1', 'L4'],
  },
  {
    service: 'ssm',
    operation: 'StartSession',
    resourceType: SSM_SESSION_RESOURCE,
    fidelity: ['L0', 'L1', 'L4'],
  },
  {
    service: 'ssm',
    operation: 'ResumeSession',
    resourceType: SSM_SESSION_RESOURCE,
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'ssm',
    operation: 'TerminateSession',
    resourceType: SSM_SESSION_RESOURCE,
    fidelity: ['L0', 'L1'],
  },
  {
    service: 's3',
    operation: 'PutObject',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L4'],
  },
  {
    service: 's3',
    operation: 'GetObject',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L4'],
  },
  {
    service: 's3',
    operation: 'DeleteObject',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L4'],
  },
  {
    service: 's3',
    operation: 'ListBucket',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L4'],
  },
  {
    service: 's3',
    operation: 'GetBucketLocation',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L4'],
  },
  {
    service: 'lambda',
    operation: 'CreateFunction',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'lambda',
    operation: 'InvokeFunction',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L4'],
  },
  {
    service: 'lambda',
    operation: 'GetFunction',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'elasticloadbalancing',
    operation: 'DescribeRules',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L3'],
  },
  {
    service: 'elasticloadbalancing',
    operation: 'ModifyRule',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L3'],
  },
  {
    service: 'ec2',
    operation: 'DescribeSecurityGroups',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L3'],
  },
  {
    service: 'ec2',
    operation: 'RevokeSecurityGroupIngress',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L3'],
  },
  {
    service: 'ec2',
    operation: 'RevokeSecurityGroupEgress',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L3'],
  },
  {
    service: 'ec2',
    operation: 'DescribeInstances',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'ec2',
    operation: 'EvaluateReachability',
    resourceType: 'AWS::EC2::Instance',
    fidelity: ['L0', 'L1', 'L3'],
  },
  {
    service: 'rds',
    operation: 'DescribeDBInstances',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'wafv2',
    operation: 'AssociateWebACL',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L3'],
  },
  {
    service: 'wafv2',
    operation: 'DisassociateWebACL',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L3'],
  },
  {
    service: 'wafv2',
    operation: 'GetWebACLForResource',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L3'],
  },
  {
    service: 'wafv2',
    operation: 'GetWebACL',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'wafv2',
    operation: 'ListWebACLs',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'logs',
    operation: 'CreateLogGroup',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'logs',
    operation: 'CreateLogStream',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'logs',
    operation: 'PutLogEvents',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'logs',
    operation: 'DescribeLogGroups',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'logs',
    operation: 'DescribeLogStreams',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'logs',
    operation: 'GetLogEvents',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'logs',
    operation: 'FilterLogEvents',
    resourceType: '*',
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'sts',
    operation: 'GetCallerIdentity',
    resourceType: '*',
    fidelity: ['L0', 'L1', 'L2'],
  },
  {
    service: 'runtime',
    operation: 'ResolveEndpoint',
    resourceType: RUNTIME_ENDPOINT_RESOURCE,
    fidelity: ['L0', 'L1'],
  },
  {
    service: 'http',
    operation: 'Probe',
    resourceType: HTTP_ENDPOINT_RESOURCE,
    fidelity: ['L0', 'L1', 'L4'],
  },
  {
    service: 'http',
    operation: 'Poll',
    resourceType: HTTP_ENDPOINT_RESOURCE,
    fidelity: ['L0', 'L1', 'L4'],
  },
  {
    service: 'http',
    operation: 'Request',
    resourceType: HTTP_ENDPOINT_RESOURCE,
    fidelity: ['L0', 'L1', 'L4'],
  },
  {
    service: 'http',
    operation: 'AttackProbe',
    resourceType: HTTP_ENDPOINT_RESOURCE,
    fidelity: ['L0', 'L1', 'L4'],
  },
];

const DEPLOY_CAPABILITIES: readonly ProviderCapability[] = [
  {
    capabilityId: 'aws.cloudformation.deploy',
    provider: AWS_PROVIDER,
    engine: CLOUDFORMATION_ENGINE,
    service: 'cloudformation',
    resourceType: STACK_RESOURCE,
    operation: 'deploy',
    fidelity: ['L0', 'L1'],
  },
  ...CLOUDFORMATION_RESOURCE_TYPES.map((resourceType) => ({
    capabilityId: `aws.cloudformation.deploy.${resourceType}`,
    provider: AWS_PROVIDER,
    engine: CLOUDFORMATION_ENGINE,
    service: 'cloudformation',
    resourceType,
    operation: 'deploy',
    fidelity: ['L0', 'L1'] as const,
  })),
];

export const AWS_CAPABILITIES: readonly ProviderCapability[] = [
  ...DEPLOY_CAPABILITIES,
  ...OPERATION_CAPABILITIES.map((capability) => ({
    capabilityId: `aws.${capability.service}.${capability.operation}.${capability.resourceType}`,
    provider: AWS_PROVIDER,
    engine: CLOUDFORMATION_ENGINE,
    ...capability,
  })),
].sort((left, right) => left.capabilityId.localeCompare(right.capabilityId));

export function declaration(
  resource: Pick<
    ResourceDeclaration,
    'resourceType' | 'resourceId' | 'properties'
  >
): ResourceDeclaration {
  return { provider: AWS_PROVIDER, ...resource };
}
