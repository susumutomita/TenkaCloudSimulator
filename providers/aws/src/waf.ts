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
  stateObject,
  storedProperties,
  updateStoredResource,
} from './state';
import { stringValue } from './value';

const WEB_ACL = 'AWS::WAFv2::WebACL';

function stringItems(value: unknown): readonly string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === 'string')
    : [];
}

function webAclArn(properties: ReturnType<typeof storedProperties>): string {
  return stringValue(properties.attributes['Arn'], 'Web ACL Arn');
}

function findWebAcl(world: ProviderWorldView, arn: string) {
  return findBy(
    world,
    WEB_ACL,
    (properties) =>
      webAclArn(properties) === arn || properties.refValue === arn,
    'Web ACL'
  );
}

function findAssociableResource(world: ProviderWorldView, arn: string) {
  const resource = awsResources(world).find((candidate) => {
    const properties = storedProperties(candidate);
    return (
      properties.refValue === arn ||
      properties.physicalId === arn ||
      properties.attributes['Arn'] === arn ||
      properties.attributes['LoadBalancerArn'] === arn
    );
  });
  if (!resource)
    throw new CoreError('NotFound', 'WAF target resource does not exist');
  return resource;
}

function webAclDocument(resource: ReturnType<typeof findWebAcl>) {
  const stored = storedProperties(resource);
  return {
    Name: stored.templateProperties['Name'] ?? stored.refValue,
    Id: stored.attributes['Id'],
    ARN: stored.attributes['Arn'],
    DefaultAction: stored.templateProperties['DefaultAction'] ?? {},
    Description: stored.templateProperties['Description'] ?? '',
    Rules: stored.templateProperties['Rules'] ?? [],
    VisibilityConfig: stored.templateProperties['VisibilityConfig'] ?? {},
  };
}

export function reduceWaf(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  switch (command.operation) {
    case 'AssociateWebACL': {
      const arn = stringValue(command.input['WebACLArn'], 'WebACLArn');
      const resourceArn = stringValue(
        command.input['ResourceArn'],
        'ResourceArn'
      );
      const webAcl = findWebAcl(world, arn);
      const target = findAssociableResource(world, resourceArn);
      const targetStored = storedProperties(target);
      const aclStored = storedProperties(webAcl);
      const associated = new Set(
        stringItems(stateObject(aclStored)['associatedResources'])
      );
      associated.add(resourceArn);
      return result('AwsWafWebAclAssociated', {}, [
        updateStoredResource(target, {
          state: { ...stateObject(targetStored), webAclArn: arn },
        }),
        updateStoredResource(webAcl, {
          state: {
            ...stateObject(aclStored),
            associatedResources: [...associated].sort(),
          },
        }),
      ]);
    }
    case 'DisassociateWebACL': {
      const resourceArn = stringValue(
        command.input['ResourceArn'],
        'ResourceArn'
      );
      const target = findAssociableResource(world, resourceArn);
      const targetStored = storedProperties(target);
      const arn = stateObject(targetStored)['webAclArn'];
      if (typeof arn !== 'string') {
        throw new CoreError('NotFound', 'resource has no Web ACL association');
      }
      const webAcl = findWebAcl(world, arn);
      const aclStored = storedProperties(webAcl);
      const associated = stringItems(
        stateObject(aclStored)['associatedResources']
      );
      return result('AwsWafWebAclDisassociated', {}, [
        updateStoredResource(target, {
          state: Object.fromEntries(
            Object.entries(stateObject(targetStored)).filter(
              ([key]) => key !== 'webAclArn'
            )
          ),
        }),
        updateStoredResource(webAcl, {
          state: {
            ...stateObject(aclStored),
            associatedResources: associated.filter(
              (item) => item !== resourceArn
            ),
          },
        }),
      ]);
    }
    case 'GetWebACLForResource': {
      const resourceArn = stringValue(
        command.input['ResourceArn'],
        'ResourceArn'
      );
      const target = findAssociableResource(world, resourceArn);
      const arn = stateObject(storedProperties(target))['webAclArn'];
      return result('AwsWafAssociationRead', {
        ...(typeof arn === 'string'
          ? { WebACL: webAclDocument(findWebAcl(world, arn)) }
          : {}),
      });
    }
    case 'GetWebACL': {
      const id = stringValue(command.input['Id'], 'Id');
      const name = stringValue(command.input['Name'], 'Name');
      const webAcl = findBy(
        world,
        WEB_ACL,
        (properties) =>
          properties.attributes['Id'] === id &&
          (properties.templateProperties['Name'] ?? properties.refValue) ===
            name,
        'Web ACL'
      );
      return result('AwsWafWebAclRead', {
        WebACL: webAclDocument(webAcl),
        LockToken: webAcl.resourceId,
      });
    }
    case 'ListWebACLs': {
      const webAcls = awsResources(world, WEB_ACL)
        .map((resource) => webAclDocument(resource))
        .map((webAcl) => ({
          Name: webAcl.Name,
          Id: webAcl.Id,
          ARN: webAcl.ARN,
          Description: webAcl.Description,
          LockToken: String(webAcl.Id),
        }));
      return result('AwsWafWebAclsListed', { WebACLs: webAcls });
    }
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `WAFv2 operation ${command.operation} is not supported`
      );
  }
}
