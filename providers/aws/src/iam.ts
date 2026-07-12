import {
  CoreError,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
} from '@tenkacloud/simulator-core';
import {
  awsResources,
  findBy,
  result,
  storedProperties,
  updateStoredResource,
} from './state';
import { objectValue, stringValue } from './value';

const ROLE_RESOURCE = 'AWS::IAM::Role';

function roleName(properties: ReturnType<typeof storedProperties>): string {
  const value = properties.templateProperties['RoleName'];
  return typeof value === 'string' ? value : properties.refValue;
}

function findRole(command: ProviderCommandInput, world: ProviderWorldView) {
  const name = stringValue(command.input['RoleName'], 'RoleName');
  return findBy(
    world,
    ROLE_RESOURCE,
    (properties) => roleName(properties) === name,
    'IAM role'
  );
}

function roleDocument(resource: ReturnType<typeof findRole>) {
  const stored = storedProperties(resource);
  return {
    Path: stored.templateProperties['Path'] ?? '/',
    RoleName: roleName(stored),
    RoleId: resource.resourceId,
    Arn: stored.attributes['Arn'],
    CreateDate: '1970-01-01T00:00:00.000Z',
    AssumeRolePolicyDocument:
      stored.templateProperties['AssumeRolePolicyDocument'] ?? {},
    Description: stored.templateProperties['Description'] ?? '',
    Tags: stored.templateProperties['Tags'] ?? [],
  };
}

function policies(
  properties: ReturnType<typeof storedProperties>
): readonly Readonly<Record<string, unknown>>[] {
  const value = properties.templateProperties['Policies'];
  if (value === undefined) return [];
  if (!Array.isArray(value)) {
    throw new CoreError(
      'ValidationFailed',
      'IAM role Policies must be an array'
    );
  }
  return value.map((policy, index) =>
    objectValue(policy, `Policies[${index}]`)
  );
}

export function reduceIam(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  switch (command.operation) {
    case 'GetRole': {
      const role = findRole(command, world);
      return result('AwsIamRoleRead', { Role: roleDocument(role) });
    }
    case 'ListRoles': {
      const roles = awsResources(world, ROLE_RESOURCE)
        .map((resource) => roleDocument(resource))
        .sort((left, right) => left.RoleName.localeCompare(right.RoleName));
      return result('AwsIamRolesListed', { Roles: roles, IsTruncated: false });
    }
    case 'ListRolePolicies': {
      const role = findRole(command, world);
      const names = policies(storedProperties(role))
        .map((policy) => stringValue(policy['PolicyName'], 'PolicyName'))
        .sort();
      return result('AwsIamRolePoliciesListed', {
        PolicyNames: names,
        IsTruncated: false,
      });
    }
    case 'GetRolePolicy': {
      const role = findRole(command, world);
      const policyName = stringValue(command.input['PolicyName'], 'PolicyName');
      const policy = policies(storedProperties(role)).find(
        (candidate) => candidate['PolicyName'] === policyName
      );
      if (!policy)
        throw new CoreError('NotFound', 'inline role policy does not exist');
      return result('AwsIamRolePolicyRead', {
        RoleName: roleName(storedProperties(role)),
        PolicyName: policyName,
        PolicyDocument: policy['PolicyDocument'] ?? {},
      });
    }
    case 'PutRolePolicy': {
      const role = findRole(command, world);
      const policyName = stringValue(command.input['PolicyName'], 'PolicyName');
      const policyDocument = objectValue(
        command.input['PolicyDocument'],
        'PolicyDocument'
      );
      const stored = storedProperties(role);
      const next = [
        ...policies(stored).filter(
          (candidate) => candidate['PolicyName'] !== policyName
        ),
        { PolicyName: policyName, PolicyDocument: policyDocument },
      ].sort((left, right) =>
        stringValue(left['PolicyName'], 'PolicyName').localeCompare(
          stringValue(right['PolicyName'], 'PolicyName')
        )
      );
      const updated = updateStoredResource(role, {
        templateProperties: { ...stored.templateProperties, Policies: next },
      });
      return result('AwsIamRolePolicyPut', {}, [updated]);
    }
    case 'DeleteRolePolicy': {
      const role = findRole(command, world);
      const policyName = stringValue(command.input['PolicyName'], 'PolicyName');
      const stored = storedProperties(role);
      const existing = policies(stored);
      if (!existing.some((policy) => policy['PolicyName'] === policyName)) {
        throw new CoreError('NotFound', 'inline role policy does not exist');
      }
      const updated = updateStoredResource(role, {
        templateProperties: {
          ...stored.templateProperties,
          Policies: existing.filter(
            (candidate) => candidate['PolicyName'] !== policyName
          ),
        },
      });
      return result('AwsIamRolePolicyDeleted', {}, [updated]);
    }
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `IAM operation ${command.operation} is not supported`
      );
  }
}
