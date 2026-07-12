import {
  CoreError,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
  type ResourceDeclaration,
} from '@tenkacloud/simulator-core';
import {
  awsResources,
  findBy,
  result,
  storedProperties,
  updateStoredResource,
} from './state';
import { optionalStringArray, stringValue } from './value';

const RULE = 'AWS::ElasticLoadBalancingV2::ListenerRule';

function findRule(world: ProviderWorldView, arn: string) {
  return findBy(
    world,
    RULE,
    (properties) =>
      properties.refValue === arn || properties.attributes['Arn'] === arn,
    'listener rule'
  );
}

function ruleDocument(
  resource: Pick<ResourceDeclaration, 'properties' | 'resourceId'>
) {
  const stored = storedProperties(resource);
  return {
    RuleArn: stored.refValue,
    Priority: String(stored.templateProperties['Priority'] ?? 'default'),
    Conditions: stored.templateProperties['Conditions'] ?? [],
    Actions: stored.templateProperties['Actions'] ?? [],
    IsDefault: stored.templateProperties['Priority'] === undefined,
  };
}

function describeRules(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const arns = optionalStringArray(command.input['RuleArns'], 'RuleArns');
  const listenerArn =
    command.input['ListenerArn'] === undefined
      ? undefined
      : stringValue(command.input['ListenerArn'], 'ListenerArn');
  const rules = awsResources(world, RULE)
    .filter((resource) => {
      const stored = storedProperties(resource);
      return (
        (!arns || arns.includes(stored.refValue)) &&
        (listenerArn === undefined ||
          stored.templateProperties['ListenerArn'] === listenerArn)
      );
    })
    .map(ruleDocument)
    .sort((left, right) => left.RuleArn.localeCompare(right.RuleArn));
  return result('AwsElbRulesDescribed', { Rules: rules });
}

function modifyRule(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const arn = stringValue(command.input['RuleArn'], 'RuleArn');
  const rule = findRule(world, arn);
  const stored = storedProperties(rule);
  const conditions = command.input['Conditions'];
  const actions = command.input['Actions'];
  if (conditions === undefined && actions === undefined) {
    throw new CoreError(
      'ValidationFailed',
      'ModifyRule requires Conditions or Actions'
    );
  }
  if (conditions !== undefined && !Array.isArray(conditions)) {
    throw new CoreError('ValidationFailed', 'Conditions must be an array');
  }
  if (actions !== undefined && !Array.isArray(actions)) {
    throw new CoreError('ValidationFailed', 'Actions must be an array');
  }
  const updated = updateStoredResource(rule, {
    templateProperties: {
      ...stored.templateProperties,
      ...(conditions === undefined ? {} : { Conditions: conditions }),
      ...(actions === undefined ? {} : { Actions: actions }),
    },
  });
  return result('AwsElbRuleModified', { Rules: [ruleDocument(updated)] }, [
    updated,
  ]);
}

export function reduceElb(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  switch (command.operation) {
    case 'DescribeRules':
      return describeRules(command, world);
    case 'ModifyRule':
      return modifyRule(command, world);
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `ELBv2 operation ${command.operation} is not supported`
      );
  }
}
