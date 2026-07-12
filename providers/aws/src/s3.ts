import {
  CoreError,
  contentHash,
  deterministicId,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
} from '@tenkacloud/simulator-core';
import { declaration, OBJECT_RESOURCE } from './model';
import { awsResources, findBy, result } from './state';
import { optionalString, stringValue } from './value';

const BUCKET_RESOURCE = 'AWS::S3::Bucket';

function findBucket(world: ProviderWorldView, name: string) {
  return findBy(
    world,
    BUCKET_RESOURCE,
    (properties) =>
      properties.refValue === name ||
      properties.templateProperties['BucketName'] === name,
    'S3 bucket'
  );
}

function findObject(world: ProviderWorldView, bucket: string, key: string) {
  const object = awsResources(world, OBJECT_RESOURCE).find(
    (resource) =>
      resource.properties['Bucket'] === bucket &&
      resource.properties['Key'] === key
  );
  if (!object) throw new CoreError('NotFound', 'S3 object does not exist');
  return object;
}

export function reduceS3(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const bucket = stringValue(command.input['Bucket'], 'Bucket');
  findBucket(world, bucket);
  switch (command.operation) {
    case 'PutObject': {
      const key = stringValue(command.input['Key'], 'Key');
      const body = command.input['Body'];
      if (typeof body !== 'string') {
        throw new CoreError('ValidationFailed', 'Body must be a string');
      }
      const resourceId = deterministicId('s3object', { bucket, key });
      const etag = contentHash(body);
      const object = declaration({
        resourceType: OBJECT_RESOURCE,
        resourceId,
        properties: {
          logicalId: resourceId,
          physicalId: `${bucket}/${key}`,
          refValue: `${bucket}/${key}`,
          dependsOn: [],
          attributes: {},
          templateProperties: { Bucket: bucket, Key: key },
          status: 'AVAILABLE',
          Bucket: bucket,
          Key: key,
          Body: body,
          ContentType:
            optionalString(command.input['ContentType'], 'ContentType') ??
            'application/octet-stream',
          ETag: etag,
          LastModified: world.world.virtualTime,
          Metadata: command.input['Metadata'] ?? {},
        },
      });
      return result('AwsS3ObjectPut', { ETag: etag }, [object]);
    }
    case 'GetObject': {
      const key = stringValue(command.input['Key'], 'Key');
      const object = findObject(world, bucket, key);
      const body = stringValue(object.properties['Body'], 'stored object Body');
      return result('AwsS3ObjectRead', {
        Body: body,
        ContentLength: Buffer.byteLength(body),
        ContentType: object.properties['ContentType'],
        ETag: object.properties['ETag'],
        LastModified: object.properties['LastModified'],
        Metadata: object.properties['Metadata'] ?? {},
      });
    }
    case 'DeleteObject': {
      const key = stringValue(command.input['Key'], 'Key');
      const object = findObject(world, bucket, key);
      return result('AwsS3ObjectDeleted', {}, [], [object.resourceId]);
    }
    case 'ListBucket': {
      const prefix = optionalString(command.input['Prefix'], 'Prefix') ?? '';
      const contents = awsResources(world, OBJECT_RESOURCE)
        .filter(
          (resource) =>
            resource.properties['Bucket'] === bucket &&
            typeof resource.properties['Key'] === 'string' &&
            resource.properties['Key'].startsWith(prefix)
        )
        .sort((left, right) =>
          String(left.properties['Key']).localeCompare(
            String(right.properties['Key'])
          )
        )
        .map((resource) => ({
          Key: resource.properties['Key'],
          ETag: resource.properties['ETag'],
          Size: Buffer.byteLength(
            stringValue(resource.properties['Body'], 'Body')
          ),
          LastModified: resource.properties['LastModified'],
        }));
      return result('AwsS3BucketListed', {
        Name: bucket,
        Prefix: prefix,
        KeyCount: contents.length,
        MaxKeys: 1_000,
        IsTruncated: false,
        Contents: contents,
      });
    }
    case 'GetBucketLocation':
      return result('AwsS3BucketLocationRead', {
        LocationConstraint: 'us-east-1',
      });
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `S3 operation ${command.operation} is not supported`
      );
  }
}
