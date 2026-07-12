import {
  CoreError,
  deterministicId,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
} from '@tenkacloud/simulator-core';
import { declaration, LOG_STREAM_RESOURCE } from './model';
import {
  awsResources,
  findBy,
  result,
  storedProperties,
  updateStoredResource,
} from './state';
import { numberValue, objectValue, optionalString, stringValue } from './value';

const LOG_GROUP = 'AWS::Logs::LogGroup';

function groupName(resource: ReturnType<typeof findGroup>): string {
  const properties = storedProperties(resource);
  const name = properties.templateProperties['LogGroupName'];
  return typeof name === 'string' ? name : properties.refValue;
}

function findGroup(world: ProviderWorldView, name: string) {
  return findBy(
    world,
    LOG_GROUP,
    (properties) =>
      properties.refValue === name ||
      properties.templateProperties['LogGroupName'] === name,
    'log group'
  );
}

function findStream(world: ProviderWorldView, group: string, stream: string) {
  return findBy(
    world,
    LOG_STREAM_RESOURCE,
    (properties) =>
      properties.templateProperties['LogGroupName'] === group &&
      properties.templateProperties['LogStreamName'] === stream,
    'log stream'
  );
}

function putLogEvents(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const group = stringValue(command.input['LogGroupName'], 'LogGroupName');
  const stream = stringValue(command.input['LogStreamName'], 'LogStreamName');
  const resource = findStream(world, group, stream);
  const inputEvents = command.input['LogEvents'];
  if (!Array.isArray(inputEvents) || inputEvents.length === 0) {
    throw new CoreError(
      'ValidationFailed',
      'LogEvents must be a non-empty array'
    );
  }
  const parsed = inputEvents.map((entry, index) => {
    const event = objectValue(entry, `LogEvents[${index}]`);
    return {
      timestamp: numberValue(
        event['timestamp'],
        `LogEvents[${index}].timestamp`
      ),
      message: stringValue(event['message'], `LogEvents[${index}].message`),
    };
  });
  const previous = resource.properties['events'];
  if (!Array.isArray(previous)) {
    throw new CoreError('ValidationFailed', 'stored log events are invalid');
  }
  const events = [...previous, ...parsed].sort((left, right) => {
    const leftEvent = objectValue(left, 'log event');
    const rightEvent = objectValue(right, 'log event');
    return (
      numberValue(leftEvent['timestamp'], 'timestamp') -
      numberValue(rightEvent['timestamp'], 'timestamp')
    );
  });
  const token = String(events.length);
  const updated = updateStoredResource(resource, {
    events,
    uploadSequenceToken: token,
  });
  return result('AwsLogsEventsPut', { nextSequenceToken: token }, [updated]);
}

function readLogEvents(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const group = stringValue(command.input['LogGroupName'], 'LogGroupName');
  const streamName = optionalString(
    command.input['LogStreamName'],
    'LogStreamName'
  );
  const pattern = optionalString(
    command.input['FilterPattern'],
    'FilterPattern'
  );
  const streams = awsResources(world, LOG_STREAM_RESOURCE).filter(
    (resource) =>
      resource.properties['LogGroupName'] === group &&
      (streamName === undefined ||
        resource.properties['LogStreamName'] === streamName)
  );
  if (streamName !== undefined && streams.length === 0) {
    throw new CoreError('NotFound', 'log stream does not exist');
  }
  const events = streams
    .flatMap((resource) => {
      const value = resource.properties['events'];
      if (!Array.isArray(value)) {
        throw new CoreError(
          'ValidationFailed',
          'stored log events are invalid'
        );
      }
      return value.map((entry) => ({
        ...objectValue(entry, 'log event'),
        logStreamName: resource.properties['LogStreamName'],
      }));
    })
    .filter((event) => {
      const message = Reflect.get(event, 'message');
      return pattern === undefined || String(message ?? '').includes(pattern);
    });
  return result(
    command.operation === 'GetLogEvents'
      ? 'AwsLogsEventsRead'
      : 'AwsLogsEventsFiltered',
    { events }
  );
}

export function reduceLogs(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  switch (command.operation) {
    case 'CreateLogGroup': {
      const name = stringValue(command.input['LogGroupName'], 'LogGroupName');
      if (
        awsResources(world, LOG_GROUP).some(
          (resource) => groupName(resource) === name
        )
      ) {
        throw new CoreError('Conflict', 'log group already exists');
      }
      const id = deterministicId('loggroup', {
        worldId: command.worldId,
        name,
      });
      const resource = declaration({
        resourceType: LOG_GROUP,
        resourceId: id,
        properties: {
          logicalId: id,
          physicalId: name,
          refValue: name,
          dependsOn: [],
          attributes: {
            Arn: `arn:aws:logs:us-east-1:000000000000:log-group:${name}`,
          },
          templateProperties: { LogGroupName: name },
          status: 'CREATE_COMPLETE',
          state: { retentionInDays: command.input['RetentionInDays'] ?? null },
        },
      });
      return result('AwsLogsGroupCreated', {}, [resource]);
    }
    case 'CreateLogStream': {
      const group = stringValue(command.input['LogGroupName'], 'LogGroupName');
      const stream = stringValue(
        command.input['LogStreamName'],
        'LogStreamName'
      );
      findGroup(world, group);
      const duplicate = awsResources(world, LOG_STREAM_RESOURCE).some(
        (resource) =>
          resource.properties['LogGroupName'] === group &&
          resource.properties['LogStreamName'] === stream
      );
      if (duplicate)
        throw new CoreError('Conflict', 'log stream already exists');
      const id = deterministicId('logstream', { group, stream });
      const resource = declaration({
        resourceType: LOG_STREAM_RESOURCE,
        resourceId: id,
        properties: {
          logicalId: id,
          physicalId: `${group}:${stream}`,
          refValue: `${group}:${stream}`,
          dependsOn: [],
          attributes: {},
          templateProperties: { LogGroupName: group, LogStreamName: stream },
          status: 'AVAILABLE',
          LogGroupName: group,
          LogStreamName: stream,
          events: [],
          uploadSequenceToken: '0',
        },
      });
      return result('AwsLogsStreamCreated', {}, [resource]);
    }
    case 'PutLogEvents':
      return putLogEvents(command, world);
    case 'DescribeLogGroups': {
      const prefix =
        optionalString(
          command.input['LogGroupNamePrefix'],
          'LogGroupNamePrefix'
        ) ?? '';
      const groups = awsResources(world, LOG_GROUP)
        .filter((resource) => groupName(resource).startsWith(prefix))
        .map((resource) => {
          const properties = storedProperties(resource);
          return {
            logGroupName: groupName(resource),
            arn: properties.attributes['Arn'],
            storedBytes: 0,
          };
        });
      return result('AwsLogsGroupsDescribed', { logGroups: groups });
    }
    case 'DescribeLogStreams': {
      const group = stringValue(command.input['LogGroupName'], 'LogGroupName');
      findGroup(world, group);
      const prefix =
        optionalString(
          command.input['LogStreamNamePrefix'],
          'LogStreamNamePrefix'
        ) ?? '';
      const streams = awsResources(world, LOG_STREAM_RESOURCE)
        .filter(
          (resource) =>
            resource.properties['LogGroupName'] === group &&
            typeof resource.properties['LogStreamName'] === 'string' &&
            resource.properties['LogStreamName'].startsWith(prefix)
        )
        .map((resource) => ({
          logStreamName: resource.properties['LogStreamName'],
          uploadSequenceToken: resource.properties['uploadSequenceToken'],
        }));
      return result('AwsLogsStreamsDescribed', { logStreams: streams });
    }
    case 'GetLogEvents':
    case 'FilterLogEvents':
      return readLogEvents(command, world);
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `Logs operation ${command.operation} is not supported`
      );
  }
}
