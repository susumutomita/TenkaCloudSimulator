import {
  CoreError,
  deterministicId,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
} from '@tenkacloud/simulator-core';
import { result } from './state';
import { stringValue } from './value';

export function reduceSts(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  if (command.operation !== 'GetCallerIdentity') {
    throw new CoreError(
      'UnsupportedCapability',
      `STS operation ${command.operation} is not supported`
    );
  }
  const accessKeyId = stringValue(command.input['AccessKeyId'], 'AccessKeyId');
  if (!accessKeyId.startsWith('TCSIM')) {
    throw new CoreError(
      'ValidationFailed',
      'STS caller must use a simulator-owned access key'
    );
  }
  const account = '000000000000';
  return result('AwsStsCallerIdentified', {
    Account: account,
    Arn: `arn:aws:iam::${account}:user/simulator/${world.world.teamId}`,
    UserId: deterministicId('aws-user', {
      worldId: world.world.worldId,
      accessKeyId,
    }),
  });
}
