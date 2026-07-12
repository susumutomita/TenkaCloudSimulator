import {
  CoreError,
  deterministicId,
  type ProviderCompileInput,
  type ProviderTargetPlan,
  type ResourceDeclaration,
} from '@tenkacloud/simulator-core';
import { type CollectionTag, parseDocument, type ScalarTag } from 'yaml';
import {
  AWS_PROVIDER,
  CLOUDFORMATION_ENGINE,
  isCloudFormationResourceType,
  STACK_RESOURCE,
} from './model';
import { compileRuntimeEndpoints } from './runtime';
import {
  errorMessage,
  objectValue,
  optionalObject,
  stringValue,
} from './value';

const REGION = 'us-east-1';
const ACCOUNT_ID = '000000000000';
const NO_VALUE = Symbol('AWS::NoValue');

interface TemplateResource {
  readonly Type: string;
  readonly Properties?: Readonly<Record<string, unknown>>;
  readonly DependsOn?: string | readonly string[];
  readonly Condition?: string;
  readonly Metadata?: unknown;
  readonly DeletionPolicy?: unknown;
  readonly UpdateReplacePolicy?: unknown;
}

interface TemplateOutput {
  readonly Value: unknown;
  readonly Condition?: string;
}

interface TemplateDocument {
  readonly Parameters: Readonly<
    Record<string, Readonly<Record<string, unknown>>>
  >;
  readonly Conditions: Readonly<Record<string, unknown>>;
  readonly Resources: Readonly<Record<string, TemplateResource>>;
  readonly Outputs: Readonly<Record<string, TemplateOutput>>;
}

interface ResolvedResource {
  readonly logicalId: string;
  readonly resourceType: string;
  readonly resourceId: string;
  readonly refValue: string;
  readonly attributes: Readonly<Record<string, unknown>>;
  readonly properties: Readonly<Record<string, unknown>>;
}

interface ResolveContext {
  readonly parameters: Readonly<Record<string, unknown>>;
  readonly conditions: Readonly<Record<string, boolean>>;
  readonly resources: ReadonlyMap<string, ResolvedResource>;
  readonly stackName: string;
}

function scalarTag(tag: string, key: string): ScalarTag {
  return { tag, resolve: (value) => ({ [key]: value }) };
}

function collectionTag(
  tag: string,
  key: string,
  collection: 'map' | 'seq'
): CollectionTag {
  return {
    tag,
    collection,
    resolve: (value) => ({ [key]: value.toJSON() }),
  };
}

const INTRINSIC_TAGS: readonly (ScalarTag | CollectionTag)[] = [
  scalarTag('!Ref', 'Ref'),
  scalarTag('!Sub', 'Fn::Sub'),
  collectionTag('!Sub', 'Fn::Sub', 'seq'),
  scalarTag('!GetAtt', 'Fn::GetAtt'),
  collectionTag('!GetAtt', 'Fn::GetAtt', 'seq'),
  collectionTag('!Join', 'Fn::Join', 'seq'),
  collectionTag('!Select', 'Fn::Select', 'seq'),
  scalarTag('!Base64', 'Fn::Base64'),
  collectionTag('!Base64', 'Fn::Base64', 'seq'),
  collectionTag('!If', 'Fn::If', 'seq'),
  scalarTag('!GetAZs', 'Fn::GetAZs'),
  collectionTag('!Equals', 'Fn::Equals', 'seq'),
  collectionTag('!Not', 'Fn::Not', 'seq'),
  collectionTag('!And', 'Fn::And', 'seq'),
  collectionTag('!Or', 'Fn::Or', 'seq'),
  scalarTag('!Condition', 'Condition'),
];

function recordEntries(
  value: Readonly<Record<string, unknown>>
): readonly [string, unknown][] {
  return Object.entries(value).sort(([left], [right]) =>
    left.localeCompare(right)
  );
}

function parseParameters(
  value: unknown
): Readonly<Record<string, Readonly<Record<string, unknown>>>> {
  const parameters =
    value === undefined ? {} : objectValue(value, 'Parameters');
  return Object.fromEntries(
    recordEntries(parameters).map(([name, parameter]) => [
      name,
      objectValue(parameter, `Parameters.${name}`),
    ])
  );
}

function parseResources(
  value: unknown
): Readonly<Record<string, TemplateResource>> {
  const resources = objectValue(value, 'Resources');
  if (Object.keys(resources).length === 0) {
    throw new CoreError('ValidationFailed', 'Resources must not be empty');
  }
  return Object.fromEntries(
    recordEntries(resources).map(([logicalId, candidate]) => [
      logicalId,
      parseTemplateResource(logicalId, candidate),
    ])
  );
}

function dependencyValue(
  value: unknown,
  logicalId: string
): string | readonly string[] | undefined {
  if (value === undefined || typeof value === 'string') return value;
  if (Array.isArray(value) && value.every((item) => typeof item === 'string')) {
    return value;
  }
  throw new CoreError(
    'ValidationFailed',
    `Resources.${logicalId}.DependsOn must be a string or string array`
  );
}

function conditionName(value: unknown, logicalId: string): string | undefined {
  if (value === undefined || typeof value === 'string') return value;
  throw new CoreError(
    'ValidationFailed',
    `Resources.${logicalId}.Condition must be a string`
  );
}

function parseTemplateResource(
  logicalId: string,
  candidate: unknown
): TemplateResource {
  const resource = objectValue(candidate, `Resources.${logicalId}`);
  const type = stringValue(resource['Type'], `Resources.${logicalId}.Type`);
  if (!isCloudFormationResourceType(type)) {
    throw new CoreError(
      'UnsupportedCapability',
      `CloudFormation resource type ${type} is not supported`
    );
  }
  const properties = optionalObject(
    resource['Properties'],
    `Resources.${logicalId}.Properties`
  );
  const dependsOn = dependencyValue(resource['DependsOn'], logicalId);
  const condition = conditionName(resource['Condition'], logicalId);
  return {
    Type: type,
    ...(properties === undefined ? {} : { Properties: properties }),
    ...(dependsOn === undefined ? {} : { DependsOn: dependsOn }),
    ...(condition === undefined ? {} : { Condition: condition }),
    ...(resource['Metadata'] === undefined
      ? {}
      : { Metadata: resource['Metadata'] }),
    ...(resource['DeletionPolicy'] === undefined
      ? {}
      : { DeletionPolicy: resource['DeletionPolicy'] }),
    ...(resource['UpdateReplacePolicy'] === undefined
      ? {}
      : { UpdateReplacePolicy: resource['UpdateReplacePolicy'] }),
  };
}

function parseOutputs(
  value: unknown
): Readonly<Record<string, TemplateOutput>> {
  const outputs = value === undefined ? {} : objectValue(value, 'Outputs');
  return Object.fromEntries(
    recordEntries(outputs).map(([name, candidate]) => {
      const output = objectValue(candidate, `Outputs.${name}`);
      if (!Object.hasOwn(output, 'Value')) {
        throw new CoreError(
          'ValidationFailed',
          `Outputs.${name}.Value is required`
        );
      }
      const condition = output['Condition'];
      if (condition !== undefined && typeof condition !== 'string') {
        throw new CoreError(
          'ValidationFailed',
          `Outputs.${name}.Condition must be a string`
        );
      }
      return [
        name,
        {
          Value: output['Value'],
          ...(condition === undefined ? {} : { Condition: condition }),
        },
      ];
    })
  );
}

export function parseCloudFormationTemplate(body: string): TemplateDocument {
  const document = parseDocument(body, { customTags: [...INTRINSIC_TAGS] });
  if (document.errors.length > 0 || document.warnings.length > 0) {
    const issue = document.errors[0] ?? document.warnings[0];
    throw new CoreError(
      'ValidationFailed',
      `CloudFormation YAML is invalid: ${errorMessage(issue)}`
    );
  }
  const root = objectValue(document.toJS(), 'template');
  return {
    Parameters: parseParameters(root['Parameters']),
    Conditions:
      root['Conditions'] === undefined
        ? {}
        : objectValue(root['Conditions'], 'Conditions'),
    Resources: parseResources(root['Resources']),
    Outputs: parseOutputs(root['Outputs']),
  };
}

function metadataParameters(
  metadata: unknown
): Readonly<Record<string, unknown>> {
  if (metadata === undefined) return {};
  const root = objectValue(metadata, 'metadata');
  const parameters = root['cfnParameters'];
  return parameters === undefined
    ? {}
    : objectValue(parameters, 'metadata.cfnParameters');
}

function generatedParameter(
  name: string,
  definition: Readonly<Record<string, unknown>>,
  identity: Readonly<Record<string, unknown>>
): string {
  const hash = deterministicId('value', { ...identity, name }).slice(-16);
  if (name === 'NamePrefix')
    return `tc-${identity['problemId']}-${hash.slice(0, 8)}`;
  if (name === 'TenkaCloudAccountId') return ACCOUNT_ID;
  if (name === 'ExternalId') return `external-${hash}`;
  if (name === 'FlagSeed' || name === 'DbPassword') return `secret-${hash}`;
  const type = definition['Type'];
  if (
    typeof type === 'string' &&
    type.startsWith('AWS::SSM::Parameter::Value')
  ) {
    return `ami-${hash}`;
  }
  return `${name.toLowerCase()}-${hash}`;
}

function parameterValues(
  definitions: TemplateDocument['Parameters'],
  input: ProviderCompileInput
): Readonly<Record<string, unknown>> {
  const supplied = metadataParameters(input.metadata);
  const identity = {
    problemId: input.problemId,
    targetId: input.targetId,
  };
  return Object.fromEntries(
    recordEntries(definitions).map(([name, definitionValue]) => {
      const definition = objectValue(definitionValue, `Parameters.${name}`);
      const suppliedValue = supplied[name];
      if (suppliedValue !== undefined) {
        return [
          name,
          suppliedValue === '__RANDOM_PASSWORD__'
            ? generatedParameter(name, definition, identity)
            : suppliedValue,
        ];
      }
      if (Object.hasOwn(definition, 'Default')) {
        return [name, definition['Default']];
      }
      return [name, generatedParameter(name, definition, identity)];
    })
  );
}

function conditionItems(
  object: Readonly<Record<string, unknown>>,
  key: string,
  count: number
): readonly unknown[] | undefined {
  if (!Object.hasOwn(object, key)) return undefined;
  const values = object[key];
  if (!Array.isArray(values) || values.length < count) {
    throw new CoreError(
      'ValidationFailed',
      `${key} needs at least ${count} condition${count === 1 ? '' : 's'}`
    );
  }
  return values;
}

function equalsCondition(
  object: Readonly<Record<string, unknown>>,
  context: ResolveContext
): boolean | undefined {
  const values = conditionItems(object, 'Fn::Equals', 2);
  if (!values) return undefined;
  if (values.length !== 2) {
    throw new CoreError('ValidationFailed', 'Fn::Equals needs two values');
  }
  return resolveValue(values[0], context) === resolveValue(values[1], context);
}

function notCondition(
  object: Readonly<Record<string, unknown>>,
  context: ResolveContext
): boolean | undefined {
  const values = conditionItems(object, 'Fn::Not', 1);
  if (!values) return undefined;
  if (values.length !== 1) {
    throw new CoreError('ValidationFailed', 'Fn::Not needs one condition');
  }
  return !conditionValue(values[0], context);
}

function aggregateCondition(
  object: Readonly<Record<string, unknown>>,
  context: ResolveContext,
  key: 'Fn::And' | 'Fn::Or'
): boolean | undefined {
  const values = conditionItems(object, key, 2);
  if (!values) return undefined;
  return key === 'Fn::And'
    ? values.every((item) => conditionValue(item, context))
    : values.some((item) => conditionValue(item, context));
}

function conditionValue(value: unknown, context: ResolveContext): boolean {
  if (typeof value === 'boolean') return value;
  const object = objectValue(value, 'condition');
  const calculated =
    equalsCondition(object, context) ??
    notCondition(object, context) ??
    aggregateCondition(object, context, 'Fn::And') ??
    aggregateCondition(object, context, 'Fn::Or');
  if (calculated !== undefined) return calculated;
  const name = object['Condition'];
  if (typeof name === 'string' && context.conditions[name] !== undefined) {
    return context.conditions[name];
  }
  throw new CoreError(
    'ValidationFailed',
    'condition expression is unsupported'
  );
}

function stringResult(value: unknown, label: string): string {
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean')
    return String(value);
  throw new CoreError('ValidationFailed', `${label} must resolve to a scalar`);
}

function refValue(name: string, context: ResolveContext): unknown {
  if (name === 'AWS::NoValue') return NO_VALUE;
  const pseudo: Readonly<Record<string, string>> = {
    'AWS::AccountId': ACCOUNT_ID,
    'AWS::Partition': 'aws',
    'AWS::Region': REGION,
    'AWS::StackName': context.stackName,
    'AWS::URLSuffix': 'amazonaws.com',
  };
  if (pseudo[name] !== undefined) return pseudo[name];
  if (context.parameters[name] !== undefined) return context.parameters[name];
  const resource = context.resources.get(name);
  if (resource) return resource.refValue;
  throw new CoreError('ValidationFailed', `Ref target ${name} does not exist`);
}

function getAttValue(value: unknown, context: ResolveContext): unknown {
  const parts = Array.isArray(value)
    ? value
    : typeof value === 'string'
      ? [
          value.slice(0, value.indexOf('.')),
          value.slice(value.indexOf('.') + 1),
        ]
      : [];
  if (
    parts.length < 2 ||
    typeof parts[0] !== 'string' ||
    parts.slice(1).some((part) => typeof part !== 'string')
  ) {
    throw new CoreError('ValidationFailed', 'Fn::GetAtt is invalid');
  }
  const resource = context.resources.get(parts[0]);
  if (!resource) {
    throw new CoreError(
      'ValidationFailed',
      `GetAtt target ${parts[0]} does not exist`
    );
  }
  const attribute = parts.slice(1).join('.');
  if (!Object.hasOwn(resource.attributes, attribute)) {
    throw new CoreError(
      'ValidationFailed',
      `GetAtt attribute ${parts[0]}.${attribute} is not supported`
    );
  }
  return resource.attributes[attribute];
}

function substitutionValue(
  name: string,
  variables: Readonly<Record<string, unknown>>,
  context: ResolveContext
): unknown {
  if (Object.hasOwn(variables, name))
    return resolveValue(variables[name], context);
  if (name.includes('.')) return getAttValue(name, context);
  try {
    return refValue(name, context);
  } catch {
    return `\${${name}}`;
  }
}

function subValue(value: unknown, context: ResolveContext): string {
  const values = Array.isArray(value) ? value : [value, {}];
  if (values.length !== 2 || typeof values[0] !== 'string') {
    throw new CoreError('ValidationFailed', 'Fn::Sub is invalid');
  }
  const variables = objectValue(values[1] ?? {}, 'Fn::Sub variables');
  return values[0].replace(/\$\{(!?)([^}]+)\}/g, (_match, escaped, name) => {
    if (escaped === '!') return `\${${name}}`;
    return stringResult(
      substitutionValue(String(name), variables, context),
      `Fn::Sub variable ${name}`
    );
  });
}

function joinValue(value: unknown, context: ResolveContext): string {
  if (!Array.isArray(value) || value.length !== 2 || !Array.isArray(value[1])) {
    throw new CoreError('ValidationFailed', 'Fn::Join is invalid');
  }
  const delimiter = stringResult(
    resolveValue(value[0], context),
    'Fn::Join delimiter'
  );
  return value[1]
    .map((item) => stringResult(resolveValue(item, context), 'Fn::Join item'))
    .join(delimiter);
}

function selectValue(value: unknown, context: ResolveContext): unknown {
  if (!Array.isArray(value) || value.length !== 2) {
    throw new CoreError('ValidationFailed', 'Fn::Select is invalid');
  }
  const indexValue = resolveValue(value[0], context);
  const list = resolveValue(value[1], context);
  const index =
    typeof indexValue === 'number' ? indexValue : Number(indexValue);
  if (
    !Number.isSafeInteger(index) ||
    index < 0 ||
    !Array.isArray(list) ||
    index >= list.length
  ) {
    throw new CoreError('ValidationFailed', 'Fn::Select index is invalid');
  }
  return list[index];
}

function ifValue(value: unknown, context: ResolveContext): unknown {
  if (
    !Array.isArray(value) ||
    value.length !== 3 ||
    typeof value[0] !== 'string'
  ) {
    throw new CoreError('ValidationFailed', 'Fn::If is invalid');
  }
  const condition = context.conditions[value[0]];
  if (condition === undefined) {
    throw new CoreError(
      'ValidationFailed',
      `condition ${value[0]} does not exist`
    );
  }
  return resolveValue(condition ? value[1] : value[2], context);
}

function intrinsicValue(
  key: string,
  value: unknown,
  context: ResolveContext
): unknown {
  switch (key) {
    case 'Ref':
      return refValue(stringValue(value, 'Ref'), context);
    case 'Fn::GetAtt':
      return getAttValue(value, context);
    case 'Fn::Sub':
      return subValue(value, context);
    case 'Fn::Join':
      return joinValue(value, context);
    case 'Fn::Select':
      return selectValue(value, context);
    case 'Fn::Base64':
      return Buffer.from(
        stringResult(resolveValue(value, context), 'Fn::Base64 value')
      ).toString('base64');
    case 'Fn::If':
      return ifValue(value, context);
    case 'Fn::GetAZs':
      return [`${REGION}a`, `${REGION}b`, `${REGION}c`];
    default:
      throw new CoreError(
        'ValidationFailed',
        `intrinsic ${key} is unsupported`
      );
  }
}

export function resolveValue(value: unknown, context: ResolveContext): unknown {
  if (Array.isArray(value)) {
    return value
      .map((item) => resolveValue(item, context))
      .filter((item) => item !== NO_VALUE);
  }
  if (value === null || typeof value !== 'object') return value;
  const object = objectValue(value, 'intrinsic value');
  const intrinsic = Object.keys(object).find(
    (key) => key === 'Ref' || key.startsWith('Fn::')
  );
  if (intrinsic && Object.keys(object).length === 1) {
    return intrinsicValue(intrinsic, object[intrinsic], context);
  }
  return Object.fromEntries(
    recordEntries(object)
      .map(([key, child]) => [key, resolveValue(child, context)] as const)
      .filter((entry) => entry[1] !== NO_VALUE)
  );
}

function directReference(
  object: Readonly<Record<string, unknown>>,
  logicalIds: ReadonlySet<string>,
  found: Set<string>
): void {
  const ref = object['Ref'];
  if (typeof ref === 'string' && logicalIds.has(ref)) found.add(ref);
  const getAtt = object['Fn::GetAtt'];
  const target = Array.isArray(getAtt) ? getAtt[0] : getAtt;
  if (typeof target !== 'string') return;
  const logicalId = target.split('.')[0];
  if (logicalId && logicalIds.has(logicalId)) found.add(logicalId);
}

function substitutionReferences(
  object: Readonly<Record<string, unknown>>,
  logicalIds: ReadonlySet<string>,
  found: Set<string>
): void {
  const sub = object['Fn::Sub'];
  const text = Array.isArray(sub) ? sub[0] : sub;
  if (typeof text !== 'string') return;
  const variables =
    Array.isArray(sub) && sub[1] !== undefined
      ? objectValue(sub[1], 'Fn::Sub variables')
      : {};
  for (const match of text.matchAll(/\$\{!?([^}.]+)(?:\.[^}]+)?\}/g)) {
    const name = match[1];
    if (name && !Object.hasOwn(variables, name) && logicalIds.has(name)) {
      found.add(name);
    }
  }
}

function visitReferences(
  candidate: unknown,
  logicalIds: ReadonlySet<string>,
  found: Set<string>
): void {
  if (Array.isArray(candidate)) {
    for (const item of candidate) visitReferences(item, logicalIds, found);
    return;
  }
  if (candidate === null || typeof candidate !== 'object') return;
  const object = objectValue(candidate, 'reference');
  directReference(object, logicalIds, found);
  substitutionReferences(object, logicalIds, found);
  for (const child of Object.values(object)) {
    visitReferences(child, logicalIds, found);
  }
}

function references(
  value: unknown,
  logicalIds: ReadonlySet<string>
): readonly string[] {
  const found = new Set<string>();
  visitReferences(value, logicalIds, found);
  return [...found].sort();
}

function explicitDependencies(resource: TemplateResource): readonly string[] {
  if (resource.DependsOn === undefined) return [];
  return typeof resource.DependsOn === 'string'
    ? [resource.DependsOn]
    : [...resource.DependsOn];
}

function dependencyOrder(resources: TemplateDocument['Resources']): readonly {
  readonly logicalId: string;
  readonly dependsOn: readonly string[];
}[] {
  const logicalIds = new Set(Object.keys(resources));
  const dependencies = new Map<string, Set<string>>();
  for (const [logicalId, resource] of Object.entries(resources)) {
    const values = new Set([
      ...explicitDependencies(resource),
      ...references(resource.Properties, logicalIds),
    ]);
    values.delete(logicalId);
    for (const dependency of values) {
      if (!logicalIds.has(dependency)) {
        throw new CoreError(
          'ValidationFailed',
          `resource ${logicalId} depends on unknown resource ${dependency}`
        );
      }
    }
    dependencies.set(logicalId, values);
  }
  const remaining = new Set(logicalIds);
  const ordered: { logicalId: string; dependsOn: readonly string[] }[] = [];
  while (remaining.size > 0) {
    const ready = [...remaining]
      .filter((logicalId) =>
        [...(dependencies.get(logicalId) ?? [])].every(
          (dependency) => !remaining.has(dependency)
        )
      )
      .sort();
    if (ready.length === 0) {
      throw new CoreError(
        'ValidationFailed',
        `CloudFormation resource dependency cycle: ${[...remaining].sort().join(', ')}`
      );
    }
    for (const logicalId of ready) {
      remaining.delete(logicalId);
      ordered.push({
        logicalId,
        dependsOn: [...(dependencies.get(logicalId) ?? [])].sort(),
      });
    }
  }
  return ordered;
}

function shortHash(resourceId: string): string {
  return resourceId.slice(-12);
}

function propertyText(
  properties: Readonly<Record<string, unknown>>,
  key: string
): string | undefined {
  const value = properties[key];
  return typeof value === 'string' && value ? value : undefined;
}

function physicalRef(
  type: string,
  resourceId: string,
  properties: Readonly<Record<string, unknown>>
): string {
  const suffix = shortHash(resourceId);
  switch (type) {
    case 'AWS::EC2::VPC':
      return `vpc-${suffix}`;
    case 'AWS::EC2::Subnet':
      return `subnet-${suffix}`;
    case 'AWS::EC2::InternetGateway':
      return `igw-${suffix}`;
    case 'AWS::EC2::RouteTable':
      return `rtb-${suffix}`;
    case 'AWS::EC2::SecurityGroup':
      return `sg-${suffix}`;
    case 'AWS::EC2::Instance':
      return `i-${suffix}`;
    case 'AWS::S3::Bucket':
      return propertyText(properties, 'BucketName') ?? `bucket-${suffix}`;
    case 'AWS::SSM::Parameter':
      return propertyText(properties, 'Name') ?? `/parameter/${suffix}`;
    case 'AWS::IAM::Role':
      return propertyText(properties, 'RoleName') ?? `role-${suffix}`;
    case 'AWS::IAM::InstanceProfile':
      return (
        propertyText(properties, 'InstanceProfileName') ?? `profile-${suffix}`
      );
    case 'AWS::Lambda::Function':
      return propertyText(properties, 'FunctionName') ?? `function-${suffix}`;
    case 'AWS::Logs::LogGroup':
      return (
        propertyText(properties, 'LogGroupName') ?? `/aws/simulator/${suffix}`
      );
    case 'AWS::RDS::DBInstance':
      return propertyText(properties, 'DBInstanceIdentifier') ?? `db-${suffix}`;
    case 'AWS::RDS::DBSubnetGroup':
      return (
        propertyText(properties, 'DBSubnetGroupName') ?? `dbsubnet-${suffix}`
      );
    case 'AWS::WAFv2::WebACL':
      return propertyText(properties, 'Name') ?? `webacl-${suffix}`;
    case 'AWS::ElasticLoadBalancingV2::LoadBalancer':
      return `arn:aws:elasticloadbalancing:${REGION}:${ACCOUNT_ID}:loadbalancer/app/simulator/${suffix}`;
    case 'AWS::ElasticLoadBalancingV2::Listener':
      return `arn:aws:elasticloadbalancing:${REGION}:${ACCOUNT_ID}:listener/app/simulator/${suffix}`;
    case 'AWS::ElasticLoadBalancingV2::ListenerRule':
      return `arn:aws:elasticloadbalancing:${REGION}:${ACCOUNT_ID}:listener-rule/app/simulator/${suffix}`;
    case 'AWS::ElasticLoadBalancingV2::TargetGroup':
      return `arn:aws:elasticloadbalancing:${REGION}:${ACCOUNT_ID}:targetgroup/simulator/${suffix}`;
    default:
      return resourceId;
  }
}

function resourceAttributes(
  type: string,
  ref: string,
  properties: Readonly<Record<string, unknown>>
): Readonly<Record<string, unknown>> {
  const suffix = ref.replaceAll('/', '-').slice(-24);
  const generic = {
    Arn: `arn:aws:cloudformation:${REGION}:${ACCOUNT_ID}:resource/${suffix}`,
  };
  switch (type) {
    case 'AWS::EC2::SecurityGroup':
      return { GroupId: ref, VpcId: properties['VpcId'] };
    case 'AWS::EC2::VPC':
      return { VpcId: ref, CidrBlock: properties['CidrBlock'] };
    case 'AWS::EC2::Subnet':
      return {
        SubnetId: ref,
        VpcId: properties['VpcId'],
        AvailabilityZone: properties['AvailabilityZone'],
      };
    case 'AWS::S3::Bucket':
      return {
        Arn: `arn:aws:s3:::${ref}`,
        DomainName: `${ref}.s3.amazonaws.com`,
      };
    case 'AWS::SSM::Parameter':
      return {
        Arn: `arn:aws:ssm:${REGION}:${ACCOUNT_ID}:parameter${ref}`,
        Value: properties['Value'] ?? '',
      };
    case 'AWS::IAM::Role':
      return { Arn: `arn:aws:iam::${ACCOUNT_ID}:role/${ref}`, RoleId: ref };
    case 'AWS::IAM::InstanceProfile':
      return { Arn: `arn:aws:iam::${ACCOUNT_ID}:instance-profile/${ref}` };
    case 'AWS::Lambda::Function':
      return { Arn: `arn:aws:lambda:${REGION}:${ACCOUNT_ID}:function:${ref}` };
    case 'AWS::Lambda::Url':
      return { FunctionUrl: `https://${suffix}.lambda-url.${REGION}.on.aws/` };
    case 'AWS::EC2::Instance':
      return {
        PublicDnsName: `${ref}.${REGION}.compute.internal`,
        PublicIp: '198.51.100.10',
        PrivateIp: '10.0.0.10',
      };
    case 'AWS::ElasticLoadBalancingV2::LoadBalancer':
      return {
        LoadBalancerArn: ref,
        DNSName: `${suffix}.elb.${REGION}.amazonaws.com`,
        CanonicalHostedZoneID: 'Z35SXDOTRQ7X7K',
      };
    case 'AWS::ElasticLoadBalancingV2::Listener':
    case 'AWS::ElasticLoadBalancingV2::ListenerRule':
    case 'AWS::ElasticLoadBalancingV2::TargetGroup':
      return { Arn: ref };
    case 'AWS::RDS::DBInstance':
      return {
        'Endpoint.Address': `${ref}.${suffix}.${REGION}.rds.amazonaws.com`,
        'Endpoint.Port': properties['Port'] ?? 5432,
      };
    case 'AWS::WAFv2::WebACL':
      return {
        Arn: `arn:aws:wafv2:${REGION}:${ACCOUNT_ID}:regional/webacl/${ref}/${suffix}`,
        Id: suffix,
      };
    case 'AWS::Logs::LogGroup':
      return { Arn: `arn:aws:logs:${REGION}:${ACCOUNT_ID}:log-group:${ref}` };
    default:
      return generic;
  }
}

function resolvedConditions(
  definitions: TemplateDocument['Conditions'],
  parameters: Readonly<Record<string, unknown>>,
  stackName: string
): Readonly<Record<string, boolean>> {
  const resolved: Record<string, boolean> = {};
  for (const [name, expression] of recordEntries(definitions)) {
    resolved[name] = conditionValue(expression, {
      parameters,
      conditions: resolved,
      resources: new Map(),
      stackName,
    });
  }
  return resolved;
}

function outputValues(
  outputs: TemplateDocument['Outputs'],
  context: ResolveContext
): Readonly<Record<string, string>> {
  return Object.fromEntries(
    recordEntries(outputs)
      .filter(([, output]) => {
        const parsed = output as TemplateOutput;
        return (
          parsed.Condition === undefined ||
          context.conditions[parsed.Condition] === true
        );
      })
      .map(([name, output]) => {
        const value = resolveValue((output as TemplateOutput).Value, context);
        return [name, stringResult(value, `Outputs.${name}.Value`)];
      })
  );
}

export function compileCloudFormation(
  input: ProviderCompileInput
): ProviderTargetPlan {
  const template = parseCloudFormationTemplate(input.templateBody);
  const parameters = parameterValues(template.Parameters, input);
  const stackName = `${input.problemId}-${input.targetId}`;
  const conditions = resolvedConditions(
    template.Conditions,
    parameters,
    stackName
  );
  const order = dependencyOrder(template.Resources);
  const resolved = new Map<string, ResolvedResource>();
  const declarations: ResourceDeclaration[] = [];
  for (const item of order) {
    const resource = template.Resources[item.logicalId];
    if (!resource)
      throw new CoreError('ValidationFailed', 'resource disappeared');
    if (
      resource.Condition !== undefined &&
      conditions[resource.Condition] !== true
    )
      continue;
    const properties = resolveValue(resource.Properties ?? {}, {
      parameters,
      conditions,
      resources: resolved,
      stackName,
    });
    const parsedProperties = objectValue(
      properties,
      `Resources.${item.logicalId}.Properties`
    );
    const resourceId = deterministicId('aws', {
      problemId: input.problemId,
      targetId: input.targetId,
      logicalId: item.logicalId,
      resourceType: resource.Type,
    });
    const ref = physicalRef(resource.Type, resourceId, parsedProperties);
    const attributes = resourceAttributes(resource.Type, ref, parsedProperties);
    resolved.set(item.logicalId, {
      logicalId: item.logicalId,
      resourceType: resource.Type,
      resourceId,
      refValue: ref,
      attributes,
      properties: parsedProperties,
    });
    declarations.push({
      provider: AWS_PROVIDER,
      resourceType: resource.Type,
      resourceId,
      properties: {
        logicalId: item.logicalId,
        physicalId: ref,
        refValue: ref,
        dependsOn: item.dependsOn,
        attributes,
        templateProperties: parsedProperties,
        status: 'CREATE_PENDING',
        ...(resource.Metadata === undefined
          ? {}
          : { metadata: resource.Metadata }),
        ...(resource.DeletionPolicy === undefined
          ? {}
          : { deletionPolicy: resource.DeletionPolicy }),
        ...(resource.UpdateReplacePolicy === undefined
          ? {}
          : { updateReplacePolicy: resource.UpdateReplacePolicy }),
      },
    });
  }
  const outputs = outputValues(template.Outputs, {
    parameters,
    conditions,
    resources: resolved,
    stackName,
  });
  const stackId = deterministicId('stack', {
    problemId: input.problemId,
    targetId: input.targetId,
  });
  const stack: ResourceDeclaration = {
    provider: AWS_PROVIDER,
    resourceType: STACK_RESOURCE,
    resourceId: stackId,
    properties: {
      logicalId: stackName,
      physicalId: stackId,
      refValue: stackId,
      dependsOn: declarations.map((resource) =>
        stringValue(resource.properties['logicalId'], 'logicalId')
      ),
      attributes: {},
      templateProperties: {},
      status: 'REVIEW_IN_PROGRESS',
      problemId: input.problemId,
      targetId: input.targetId,
      entry: input.target.entry,
      templateBody: input.templateBody,
      ...(input.metadata === undefined ? {} : { metadata: input.metadata }),
      parameters,
      outputs,
      resourceLogicalIds: declarations.map((resource) =>
        stringValue(resource.properties['logicalId'], 'logicalId')
      ),
    },
  };
  const runtimeEndpoints = compileRuntimeEndpoints({
    ...(input.metadata === undefined ? {} : { metadata: input.metadata }),
    outputs,
    problemId: input.problemId,
    targetId: input.targetId,
    stackId,
    stackName,
  });
  const types = [
    ...new Set(declarations.map((resource) => resource.resourceType)),
  ].sort();
  return {
    targetId: input.targetId,
    provider: AWS_PROVIDER,
    engine: CLOUDFORMATION_ENGINE,
    requirements: [STACK_RESOURCE, ...types].map((resourceType) => ({
      provider: AWS_PROVIDER,
      engine: CLOUDFORMATION_ENGINE,
      service: 'cloudformation',
      resourceType,
      operation: 'deploy',
      fidelity: ['L0', 'L1'],
      source: { path: input.target.entry },
    })),
    resources: [stack, ...declarations, ...runtimeEndpoints],
  };
}
