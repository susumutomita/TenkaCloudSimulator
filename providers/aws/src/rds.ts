import {
  CoreError,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
} from '@tenkacloud/simulator-core';
import { awsResources, result, stateObject, storedProperties } from './state';
import { optionalString } from './value';

const DB_INSTANCE = 'AWS::RDS::DBInstance';

export function reduceRds(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  if (command.operation !== 'DescribeDBInstances') {
    throw new CoreError(
      'UnsupportedCapability',
      `RDS operation ${command.operation} is not supported`
    );
  }
  const requested = optionalString(
    command.input['DBInstanceIdentifier'],
    'DBInstanceIdentifier'
  );
  const instances = awsResources(world, DB_INSTANCE)
    .filter((resource) => {
      const properties = storedProperties(resource);
      return requested === undefined || properties.refValue === requested;
    })
    .map((resource) => {
      const properties = storedProperties(resource);
      const state = stateObject(properties);
      return {
        DBInstanceIdentifier: properties.refValue,
        DBInstanceClass: properties.templateProperties['DBInstanceClass'],
        Engine: properties.templateProperties['Engine'],
        DBInstanceStatus: state['dbInstanceStatus'] ?? 'available',
        MasterUsername: properties.templateProperties['MasterUsername'],
        DBName: properties.templateProperties['DBName'],
        Endpoint: {
          Address: properties.attributes['Endpoint.Address'],
          Port: properties.attributes['Endpoint.Port'],
        },
        VpcSecurityGroups:
          properties.templateProperties['VPCSecurityGroups'] ?? [],
      };
    });
  if (requested !== undefined && instances.length === 0) {
    throw new CoreError('NotFound', 'DB instance does not exist');
  }
  return result('AwsRdsInstancesDescribed', { DBInstances: instances });
}
