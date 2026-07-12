import {
  CoreError,
  canonicalJson,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
} from '@tenkacloud/simulator-core';
import { evaluateReachability } from './reachability';
import {
  awsResources,
  findBy,
  result,
  stateObject,
  storedProperties,
  updateStoredResource,
} from './state';
import { objectValue, optionalStringArray, stringValue } from './value';

const SECURITY_GROUP = 'AWS::EC2::SecurityGroup';
const INSTANCE = 'AWS::EC2::Instance';

function groupId(resource: ReturnType<typeof findGroup>): string {
  return storedProperties(resource).refValue;
}

function findGroup(world: ProviderWorldView, id: string) {
  return findBy(
    world,
    SECURITY_GROUP,
    (properties) =>
      properties.refValue === id ||
      properties.templateProperties['GroupName'] === id,
    'security group'
  );
}

function permissions(
  command: ProviderCommandInput
): readonly Readonly<Record<string, unknown>>[] {
  const entries = command.input['IpPermissions'];
  if (Array.isArray(entries)) {
    return entries.map((entry, index) =>
      objectValue(entry, `IpPermissions[${index}]`)
    );
  }
  const permission = Object.fromEntries(
    ['IpProtocol', 'FromPort', 'ToPort', 'CidrIp', 'SourceSecurityGroupId']
      .filter((key) => command.input[key] !== undefined)
      .map((key) => [key, command.input[key]])
  );
  if (Object.keys(permission).length === 0) {
    throw new CoreError('ValidationFailed', 'IpPermissions must not be empty');
  }
  return [permission];
}

function revoke(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  egress: boolean
): ProviderCommandResult {
  const id = stringValue(command.input['GroupId'], 'GroupId');
  const group = findGroup(world, id);
  const stored = storedProperties(group);
  const state = stateObject(stored);
  const key = egress ? 'ipPermissionsEgress' : 'ipPermissions';
  const current = state[key];
  if (!Array.isArray(current)) {
    throw new CoreError(
      'ValidationFailed',
      'security group rule state is invalid'
    );
  }
  const removed = new Set(
    permissions(command).map((entry) => canonicalJson(entry))
  );
  const next = current.filter((entry) => !removed.has(canonicalJson(entry)));
  const updated = updateStoredResource(group, {
    state: { ...state, [key]: next },
  });
  return result(
    egress
      ? 'AwsEc2SecurityGroupEgressRevoked'
      : 'AwsEc2SecurityGroupIngressRevoked',
    { Return: true },
    [updated]
  );
}

export function reduceEc2(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  switch (command.operation) {
    case 'DescribeSecurityGroups': {
      const ids = optionalStringArray(command.input['GroupIds'], 'GroupIds');
      const groups = awsResources(world, SECURITY_GROUP)
        .filter((resource) => !ids || ids.includes(groupId(resource)))
        .map((resource) => {
          const stored = storedProperties(resource);
          const state = stateObject(stored);
          return {
            GroupId: stored.refValue,
            GroupName:
              stored.templateProperties['GroupName'] ?? stored.logicalId,
            Description: stored.templateProperties['GroupDescription'] ?? '',
            VpcId: stored.templateProperties['VpcId'],
            IpPermissions: state['ipPermissions'] ?? [],
            IpPermissionsEgress: state['ipPermissionsEgress'] ?? [],
            Tags: stored.templateProperties['Tags'] ?? [],
          };
        });
      return result('AwsEc2SecurityGroupsDescribed', {
        SecurityGroups: groups,
      });
    }
    case 'RevokeSecurityGroupIngress':
      return revoke(command, world, false);
    case 'RevokeSecurityGroupEgress':
      return revoke(command, world, true);
    case 'DescribeInstances': {
      const ids = optionalStringArray(
        command.input['InstanceIds'],
        'InstanceIds'
      );
      const instances = awsResources(world, INSTANCE)
        .filter((resource) => {
          const id = storedProperties(resource).refValue;
          return !ids || ids.includes(id);
        })
        .map((resource) => {
          const stored = storedProperties(resource);
          const state = stateObject(stored);
          return {
            InstanceId: stored.refValue,
            ImageId: stored.templateProperties['ImageId'],
            InstanceType: stored.templateProperties['InstanceType'],
            SubnetId: stored.templateProperties['SubnetId'],
            SecurityGroups: stored.templateProperties['SecurityGroupIds'] ?? [],
            PublicDnsName: stored.attributes['PublicDnsName'],
            PublicIpAddress: stored.attributes['PublicIp'],
            PrivateIpAddress: stored.attributes['PrivateIp'],
            State: { Name: state['instanceState'] ?? 'running' },
            Tags: stored.templateProperties['Tags'] ?? [],
            Simulation: state,
          };
        });
      return result('AwsEc2InstancesDescribed', {
        Reservations: instances.map((instance) => ({ Instances: [instance] })),
      });
    }
    case 'EvaluateReachability':
      return evaluateReachability(command, world);
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `EC2 operation ${command.operation} is not supported`
      );
  }
}
