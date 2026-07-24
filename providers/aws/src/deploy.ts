import {
  CoreError,
  type ProviderDeploymentResult,
  type ProviderTargetPlan,
  type ProviderWorldView,
} from '@tenkacloud/simulator-core';
import {
  AWS_PROVIDER,
  CLOUDFORMATION_ENGINE,
  RUNTIME_ENDPOINT_RESOURCE,
  STACK_RESOURCE,
} from './model';
import {
  customResourceObjects,
  initializeResource,
  stackProperties,
  webAclAssociationEffects,
} from './state';

export function deployCloudFormation(
  plan: ProviderTargetPlan,
  world: ProviderWorldView
): ProviderDeploymentResult {
  if (plan.provider !== AWS_PROVIDER || plan.engine !== CLOUDFORMATION_ENGINE) {
    throw new CoreError(
      'ValidationFailed',
      'CloudFormation plan target is invalid'
    );
  }
  const stack = plan.resources.find(
    (resource) => resource.resourceType === STACK_RESOURCE
  );
  if (!stack)
    throw new CoreError('ValidationFailed', 'CloudFormation plan has no stack');
  const initialized = plan.resources.map((resource) =>
    initializeResource(resource, world.world.virtualTime)
  );
  const associated = webAclAssociationEffects(initialized);
  const objects = customResourceObjects(associated, world.world.virtualTime);
  const state = stackProperties(
    associated.find((resource) => resource.resourceType === STACK_RESOURCE) ??
      stack
  );
  const endpointCount = associated.filter(
    (resource) => resource.resourceType === RUNTIME_ENDPOINT_RESOURCE
  ).length;
  return {
    events: [
      {
        type: 'AwsCloudFormationStackCreated',
        payload: {
          stackId: stack.resourceId,
          targetId: plan.targetId,
          resourceCount: associated.length - 1 - endpointCount,
          customObjectCount: objects.length,
          endpointCount,
        },
      },
    ],
    resources: [...associated, ...objects],
    outputs: state.outputs,
  };
}
