import type { FidelityLevel } from '@tenkacloud/simulator-core';
import {
  AWS_CAPABILITIES,
  AWS_PROVIDER,
  CLOUDFORMATION_RESOURCE_TYPES,
  STACK_RESOURCE,
} from './model';

export interface CatalogCapabilityEntry {
  readonly provider: string;
  readonly service: string;
  readonly resourceType: string;
  readonly operation: string;
  readonly fidelity: FidelityLevel;
}

export interface AwsCatalogCapabilityManifest {
  readonly schemaVersion: '1';
  readonly version: string;
  readonly capabilities: readonly CatalogCapabilityEntry[];
}

export interface CatalogIdentityRequirement extends CatalogCapabilityEntry {
  readonly problemId?: string;
}

export interface UnsupportedCatalogIdentity {
  readonly identity: string;
  readonly status: 'missing' | 'insufficient';
  readonly requiredFidelity: FidelityLevel;
  readonly availableFidelity?: FidelityLevel;
  readonly problemIds: readonly string[];
}

const FIDELITY_RANK: Readonly<Record<FidelityLevel, number>> = {
  L0: 0,
  L1: 1,
  L2: 2,
  L3: 3,
  L4: 4,
};

const NETWORK_LIFECYCLE_RESOURCE_TYPES = new Set([
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
  'AWS::WAFv2::WebACL',
]);

function maximumFidelity(levels: readonly FidelityLevel[]): FidelityLevel {
  return (
    [...levels].sort(
      (left, right) => FIDELITY_RANK[right] - FIDELITY_RANK[left]
    )[0] ?? 'L0'
  );
}

function serviceForResourceType(resourceType: string): string {
  if (resourceType.startsWith('AWS::EC2::')) return 'ec2';
  if (resourceType.startsWith('AWS::ElasticLoadBalancingV2::')) {
    return 'elasticloadbalancing';
  }
  if (resourceType.startsWith('AWS::IAM::')) return 'iam';
  if (resourceType.startsWith('AWS::Lambda::')) return 'lambda';
  if (resourceType.startsWith('AWS::Logs::')) return 'logs';
  if (resourceType.startsWith('AWS::RDS::')) return 'rds';
  if (resourceType.startsWith('AWS::S3::')) return 's3';
  if (resourceType.startsWith('AWS::SSM::')) return 'ssm';
  if (resourceType.startsWith('AWS::WAFv2::')) return 'wafv2';
  return 'cloudformation';
}

function lifecycleFidelity(resourceType: string): FidelityLevel {
  if (
    resourceType === 'AWS::CloudFormation::CustomResource' ||
    resourceType === 'Custom::EmptyStackBuckets'
  ) {
    return 'L4';
  }
  if (NETWORK_LIFECYCLE_RESOURCE_TYPES.has(resourceType)) return 'L3';
  return resourceType.startsWith('AWS::IAM::') ? 'L2' : 'L1';
}

export function catalogCapabilityIdentity(entry: {
  readonly provider: string;
  readonly service: string;
  readonly resourceType: string;
  readonly operation: string;
}): string {
  return [
    entry.provider,
    entry.service,
    entry.resourceType,
    entry.operation,
  ].join('|');
}

const lifecycleCapabilities: readonly CatalogCapabilityEntry[] =
  CLOUDFORMATION_RESOURCE_TYPES.map(
    (resourceType): CatalogCapabilityEntry => ({
      provider: AWS_PROVIDER,
      service: serviceForResourceType(resourceType),
      resourceType,
      operation: 'lifecycle',
      fidelity: lifecycleFidelity(resourceType),
    })
  );

const commandCapabilities: readonly CatalogCapabilityEntry[] =
  AWS_CAPABILITIES.filter(
    (capability) => capability.operation !== 'deploy'
  ).map((capability) => ({
    provider: capability.provider,
    service: capability.service,
    resourceType: capability.resourceType,
    operation: capability.operation,
    fidelity: maximumFidelity(capability.fidelity),
  }));

export const AWS_CATALOG_CAPABILITY_MANIFEST: AwsCatalogCapabilityManifest = {
  schemaVersion: '1',
  version: 'aws-provider-0.1.0',
  capabilities: (
    [
      {
        provider: AWS_PROVIDER,
        service: 'cloudformation',
        resourceType: STACK_RESOURCE,
        operation: 'deploy',
        fidelity: 'L1',
      },
      ...lifecycleCapabilities,
      ...commandCapabilities,
    ] satisfies CatalogCapabilityEntry[]
  ).sort((left, right) =>
    catalogCapabilityIdentity(left).localeCompare(
      catalogCapabilityIdentity(right)
    )
  ),
};

export function unsupportedCatalogIdentities(
  requirements: readonly CatalogIdentityRequirement[],
  manifest: AwsCatalogCapabilityManifest = AWS_CATALOG_CAPABILITY_MANIFEST
): readonly UnsupportedCatalogIdentity[] {
  const capabilities = new Map(
    manifest.capabilities.map((capability) => [
      catalogCapabilityIdentity(capability),
      capability,
    ])
  );
  const grouped = new Map<
    string,
    {
      requiredFidelity: FidelityLevel;
      availableFidelity?: FidelityLevel;
      problemIds: Set<string>;
    }
  >();
  for (const requirement of requirements) {
    const identity = catalogCapabilityIdentity(requirement);
    const capability = capabilities.get(identity);
    if (
      capability &&
      FIDELITY_RANK[capability.fidelity] >= FIDELITY_RANK[requirement.fidelity]
    ) {
      continue;
    }
    const current = grouped.get(identity);
    const requiredFidelity =
      current &&
      FIDELITY_RANK[current.requiredFidelity] >=
        FIDELITY_RANK[requirement.fidelity]
        ? current.requiredFidelity
        : requirement.fidelity;
    const problemIds = current?.problemIds ?? new Set<string>();
    if (requirement.problemId) problemIds.add(requirement.problemId);
    grouped.set(identity, {
      requiredFidelity,
      ...(capability === undefined
        ? {}
        : { availableFidelity: capability.fidelity }),
      problemIds,
    });
  }
  return [...grouped.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([identity, value]) => ({
      identity,
      status:
        value.availableFidelity === undefined ? 'missing' : 'insufficient',
      requiredFidelity: value.requiredFidelity,
      ...(value.availableFidelity === undefined
        ? {}
        : { availableFidelity: value.availableFidelity }),
      problemIds: [...value.problemIds].sort(),
    }));
}
