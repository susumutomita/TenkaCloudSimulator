import {
  CoreError,
  type ExecuteCommandInput,
  type SimulationCore,
} from '@tenkacloud/simulator-core';
import { AWS_CAPABILITIES, AWS_PROVIDER, CLOUDFORMATION_ENGINE } from './model';

export const AWS_NATIVE_DEFAULT_BODY_LIMIT = 1024 * 1024;
export const AWS_NATIVE_WORLD_HEADER = 'x-tenkacloud-world-id';
export const AWS_NATIVE_DEPLOYMENT_HEADER = 'x-tenkacloud-deployment-id';
export const AWS_NATIVE_TARGET_HEADER = 'x-tenkacloud-target-id';

export type AwsNativeProtocol = 'aws-json' | 'query' | 'rest-json' | 'rest-xml';

export type AwsNativeGatewayErrorCode =
  | 'UnauthorizedOperation'
  | 'ValidationFailed'
  | 'QuotaExceeded'
  | 'UnsupportedCapability';

export class AwsNativeGatewayError extends Error {
  readonly status: 400 | 403 | 413 | 422;

  constructor(
    readonly code: AwsNativeGatewayErrorCode,
    message: string
  ) {
    super(message);
    this.name = 'AwsNativeGatewayError';
    this.status =
      code === 'UnauthorizedOperation'
        ? 403
        : code === 'QuotaExceeded'
          ? 413
          : code === 'UnsupportedCapability'
            ? 422
            : 400;
  }
}

export interface AwsNativeGatewayOptions {
  readonly simulatorOrigin: string;
  readonly simulatorAccessKeyId: string;
  readonly maxBodyBytes?: number;
  readonly onCommandSuccess?: (
    command: AwsNativeGatewayCommand,
    response: Readonly<Record<string, unknown>>
  ) => void | Promise<void>;
  readonly beforeCommand?: (
    command: AwsNativeGatewayCommand
  ) => void | Promise<void>;
}

export interface AwsNativeGatewayCommand {
  readonly worldId: string;
  readonly protocol: AwsNativeProtocol;
  readonly service: string;
  readonly operation: string;
  readonly requestId: string;
  readonly idempotencyKey: string;
  readonly headOnly?: boolean;
  readonly command: ExecuteCommandInput;
}

interface AuthorizationContext {
  readonly accessKeyId: string;
  readonly date: string;
  readonly region: string;
  readonly service: string;
  readonly signature: string;
  readonly signedHeaders: readonly string[];
}

interface RoutingContext extends AuthorizationContext {
  readonly worldId: string;
  readonly deploymentId: string;
  readonly targetId: string;
  readonly requestId: string;
  readonly endpointPath: string;
}

interface ProtocolRoute {
  readonly protocol: AwsNativeProtocol;
  readonly operation: string;
  readonly input: Readonly<Record<string, unknown>>;
  readonly headOnly?: boolean;
}

interface QueryService {
  readonly version: string;
  readonly namespace: string;
}

const QUERY_SERVICES: Readonly<Record<string, QueryService>> = {
  cloudformation: {
    version: '2010-05-15',
    namespace: 'http://cloudformation.amazonaws.com/doc/2010-05-15/',
  },
  iam: {
    version: '2010-05-08',
    namespace: 'https://iam.amazonaws.com/doc/2010-05-08/',
  },
  ec2: {
    version: '2016-11-15',
    namespace: 'http://ec2.amazonaws.com/doc/2016-11-15/',
  },
  elasticloadbalancing: {
    version: '2015-12-01',
    namespace: 'http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/',
  },
  rds: {
    version: '2014-10-31',
    namespace: 'http://rds.amazonaws.com/doc/2014-10-31/',
  },
  sts: {
    version: '2011-06-15',
    namespace: 'https://sts.amazonaws.com/doc/2011-06-15/',
  },
};

const JSON_TARGETS: Readonly<Record<string, string>> = {
  http: 'TenkaCloudHTTP',
  ssm: 'AmazonSSM',
  logs: 'Logs_20140328',
  runtime: 'TenkaCloudRuntime',
  wafv2: 'AWSWAF_20190729',
};

const LOGS_INPUT_KEYS: Readonly<Record<string, string>> = {
  logGroupName: 'LogGroupName',
  logGroupNamePrefix: 'LogGroupNamePrefix',
  logStreamName: 'LogStreamName',
  logStreamNamePrefix: 'LogStreamNamePrefix',
  logEvents: 'LogEvents',
  filterPattern: 'FilterPattern',
  retentionInDays: 'RetentionInDays',
  sequenceToken: 'SequenceToken',
};

const SIGNATURE_PATTERN =
  /^AWS4-HMAC-SHA256 Credential=([^/\s,]+)\/(\d{8})\/([a-z0-9-]+)\/([a-z0-9-]+)\/aws4_request,\s*SignedHeaders=([a-z0-9;-]+),\s*Signature=([0-9a-f]{64})$/;
const LOOSE_SERVICE_PATTERN =
  /Credential=[^/\s,]+\/\d{8}\/[a-z0-9-]+\/([a-z0-9-]+)\/aws4_request/;

function gatewayError(code: AwsNativeGatewayErrorCode, message: string): never {
  throw new AwsNativeGatewayError(code, message);
}

function normalizedOrigin(value: string): string {
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    return gatewayError('ValidationFailed', 'simulatorOrigin must be a URL');
  }
  if (
    parsed.username ||
    parsed.password ||
    parsed.pathname !== '/' ||
    parsed.search ||
    parsed.hash
  ) {
    return gatewayError(
      'ValidationFailed',
      'simulatorOrigin must contain only an origin'
    );
  }
  return parsed.origin;
}

function simulatorAccessKey(value: string): string {
  if (!/^TCSIM[A-Z0-9]{11,123}$/.test(value)) {
    return gatewayError(
      'ValidationFailed',
      'simulatorAccessKeyId must be a simulator-owned TCSIM access key'
    );
  }
  return value;
}

function bodyLimit(value: number | undefined): number {
  const limit = value ?? AWS_NATIVE_DEFAULT_BODY_LIMIT;
  if (!Number.isInteger(limit) || limit < 1 || limit > 1024 * 1024) {
    return gatewayError(
      'ValidationFailed',
      'maxBodyBytes must be an integer from 1 through 1048576'
    );
  }
  return limit;
}

function rejectCredentialTransport(request: Request): void {
  const url = new URL(request.url);
  if (
    [...url.searchParams.keys()].some((key) =>
      ['x-amz-credential', 'x-amz-signature', 'x-amz-security-token'].includes(
        key.toLowerCase()
      )
    )
  ) {
    gatewayError(
      'UnauthorizedOperation',
      'presigned or session AWS credentials are not accepted'
    );
  }
  if (request.headers.has('x-amz-security-token')) {
    gatewayError(
      'UnauthorizedOperation',
      'AWS session credentials are not accepted'
    );
  }
}

function parseAuthorization(
  request: Request,
  expectedAccessKey: string
): AuthorizationContext {
  const authorization = request.headers.get('authorization')?.trim();
  const match = authorization ? SIGNATURE_PATTERN.exec(authorization) : null;
  if (!match) {
    gatewayError(
      'UnauthorizedOperation',
      'syntactically valid SigV4 Authorization is required'
    );
  }
  const accessKeyId = match[1] ?? '';
  if (accessKeyId.startsWith('AKIA') || accessKeyId.startsWith('ASIA')) {
    gatewayError(
      'UnauthorizedOperation',
      'real AWS AKIA/ASIA credentials are not accepted'
    );
  }
  if (accessKeyId !== expectedAccessKey) {
    gatewayError('UnauthorizedOperation', 'simulator access key is invalid');
  }
  const date = match[2] ?? '';
  const region = match[3] ?? '';
  const service = match[4] ?? '';
  const signedHeaders = (match[5] ?? '').split(';');
  const signature = match[6] ?? '';
  return { accessKeyId, date, region, service, signature, signedHeaders };
}

function validateSignedHeaders(
  request: Request,
  authorization: AuthorizationContext
): void {
  const { signedHeaders } = authorization;
  const sortedHeaders = [...new Set(signedHeaders)].sort();
  if (
    sortedHeaders.length !== signedHeaders.length ||
    sortedHeaders.some((name, index) => name !== signedHeaders[index])
  ) {
    gatewayError(
      'UnauthorizedOperation',
      'SigV4 SignedHeaders must be unique and sorted'
    );
  }
  const routeCarriesNamespace = /^\/v1\/native\/aws\/[^/]+\/[^/]+$/.test(
    new URL(request.url).pathname
  );
  const requiredHeaders = routeCarriesNamespace
    ? ['host', 'x-amz-date']
    : [
        'host',
        'x-amz-date',
        AWS_NATIVE_WORLD_HEADER,
        AWS_NATIVE_DEPLOYMENT_HEADER,
      ];
  if (
    requiredHeaders.some((name) => !signedHeaders.includes(name)) ||
    signedHeaders.some((name) => name !== 'host' && !request.headers.has(name))
  ) {
    gatewayError(
      'UnauthorizedOperation',
      'SigV4 must sign host, date, and all routing headers'
    );
  }
  if (
    request.headers.has(AWS_NATIVE_TARGET_HEADER) &&
    !signedHeaders.includes(AWS_NATIVE_TARGET_HEADER)
  ) {
    gatewayError(
      'UnauthorizedOperation',
      'SigV4 must sign the target routing header'
    );
  }
  for (const name of [AWS_NATIVE_WORLD_HEADER, AWS_NATIVE_DEPLOYMENT_HEADER]) {
    if (request.headers.has(name) && !signedHeaders.includes(name)) {
      gatewayError('UnauthorizedOperation', `SigV4 must sign ${name}`);
    }
  }
  const unsignedAwsHeader = ['x-amz-target', 'x-amz-content-sha256'].find(
    (name) => request.headers.has(name) && !signedHeaders.includes(name)
  );
  if (unsignedAwsHeader) {
    gatewayError(
      'UnauthorizedOperation',
      `SigV4 must sign ${unsignedAwsHeader}`
    );
  }
}

function validateSignatureMetadata(
  request: Request,
  authorization: AuthorizationContext
): void {
  const amzDate = request.headers.get('x-amz-date')?.trim() ?? '';
  if (
    !/^\d{8}T\d{6}Z$/.test(amzDate) ||
    !amzDate.startsWith(authorization.date)
  ) {
    gatewayError(
      'UnauthorizedOperation',
      'SigV4 scope date must match x-amz-date'
    );
  }
  const payloadHash = request.headers.get('x-amz-content-sha256');
  if (
    payloadHash !== null &&
    payloadHash !== 'UNSIGNED-PAYLOAD' &&
    !/^[0-9a-f]{64}$/.test(payloadHash)
  ) {
    gatewayError(
      'UnauthorizedOperation',
      'x-amz-content-sha256 is not a valid SigV4 payload hash'
    );
  }
}

function authorizationContext(
  request: Request,
  expectedAccessKey: string
): AuthorizationContext {
  rejectCredentialTransport(request);
  const authorization = parseAuthorization(request, expectedAccessKey);
  validateSignedHeaders(request, authorization);
  validateSignatureMetadata(request, authorization);
  return authorization;
}

function routingHeader(
  headers: Headers,
  name: string,
  required: boolean
): string | undefined {
  const value = headers.get(name)?.trim();
  if (!value && !required) return undefined;
  if (!value || !/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(value)) {
    return gatewayError('ValidationFailed', `${name} is invalid or missing`);
  }
  return value;
}

interface EndpointRouting {
  readonly endpointPath: string;
  readonly worldId?: string;
  readonly deploymentId?: string;
}

function endpointRouting(url: URL): EndpointRouting {
  const match = /^\/v1\/native\/aws\/([^/]+)\/([^/]+)$/.exec(url.pathname);
  if (!match) return { endpointPath: '/' };
  try {
    return {
      endpointPath: url.pathname,
      worldId: decodeURIComponent(match[1] ?? ''),
      deploymentId: decodeURIComponent(match[2] ?? ''),
    };
  } catch {
    return gatewayError(
      'ValidationFailed',
      'AWS native endpoint route is invalid'
    );
  }
}

function assertMatchingRoute(
  headerValue: string | undefined,
  endpointValue: string | undefined
): void {
  if (headerValue && endpointValue && headerValue !== endpointValue) {
    gatewayError(
      'UnauthorizedOperation',
      'AWS native endpoint route does not match routing headers'
    );
  }
}

function requiredRouteValue(
  name: string,
  headerValue: string | undefined,
  endpointValue: string | undefined
): string {
  const value = headerValue ?? endpointValue;
  if (!value || !/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(value)) {
    return gatewayError('ValidationFailed', `${name} is invalid or missing`);
  }
  return value;
}

function routingContext(
  request: Request,
  expectedAccessKey: string
): RoutingContext {
  const authorization = authorizationContext(request, expectedAccessKey);
  const url = new URL(request.url);
  const endpoint = endpointRouting(url);
  const headerWorldId = routingHeader(
    request.headers,
    AWS_NATIVE_WORLD_HEADER,
    false
  );
  const headerDeploymentId = routingHeader(
    request.headers,
    AWS_NATIVE_DEPLOYMENT_HEADER,
    false
  );
  assertMatchingRoute(headerWorldId, endpoint.worldId);
  assertMatchingRoute(headerDeploymentId, endpoint.deploymentId);
  const worldId = requiredRouteValue(
    AWS_NATIVE_WORLD_HEADER,
    headerWorldId,
    endpoint.worldId
  );
  const deploymentId = requiredRouteValue(
    AWS_NATIVE_DEPLOYMENT_HEADER,
    headerDeploymentId,
    endpoint.deploymentId
  );
  const targetId =
    routingHeader(request.headers, AWS_NATIVE_TARGET_HEADER, false) ??
    'default';
  return {
    ...authorization,
    worldId,
    deploymentId,
    targetId,
    requestId: `tcsim-${authorization.signature.slice(0, 24)}`,
    endpointPath: endpoint.endpointPath,
  };
}

function record(value: unknown, label: string): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return gatewayError('ValidationFailed', `${label} must be an object`);
  }
  return { ...value } as Record<string, unknown>;
}

async function requestBytes(
  request: Request,
  limit: number
): Promise<Uint8Array> {
  const declaredLength = request.headers.get('content-length');
  if (
    declaredLength !== null &&
    (!/^\d+$/.test(declaredLength) || Number(declaredLength) > limit)
  ) {
    return gatewayError(
      Number(declaredLength) > limit ? 'QuotaExceeded' : 'ValidationFailed',
      'request Content-Length is invalid or exceeds the body limit'
    );
  }
  const bytes = new Uint8Array(await request.arrayBuffer());
  if (bytes.byteLength > limit) {
    return gatewayError('QuotaExceeded', 'request body exceeds the body limit');
  }
  return bytes;
}

function utf8(bytes: Uint8Array, label: string): string {
  try {
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  } catch {
    return gatewayError('ValidationFailed', `${label} must be UTF-8 text`);
  }
}

async function assertNoBody(request: Request, limit: number): Promise<void> {
  if ((await requestBytes(request, limit)).byteLength !== 0) {
    gatewayError('ValidationFailed', 'request body must be empty');
  }
}

function operationResourceType(service: string, operation: string): string {
  const capability = AWS_CAPABILITIES.find(
    (candidate) =>
      candidate.service === service &&
      candidate.operation === operation &&
      operation !== 'deploy'
  );
  if (!capability) {
    return gatewayError(
      'UnsupportedCapability',
      `AWS ${service} operation ${operation} is not implemented`
    );
  }
  return capability.resourceType;
}

function command(
  routing: RoutingContext,
  operation: string,
  input: Readonly<Record<string, unknown>>,
  simulatorOrigin: string
): ExecuteCommandInput {
  const contextualInput =
    routing.service === 'sts'
      ? { ...input, AccessKeyId: routing.accessKeyId }
      : routing.service === 'ssm' &&
          (operation === 'StartSession' || operation === 'ResumeSession')
        ? {
            ...input,
            __SimulatorOrigin: simulatorOrigin,
            __SimulatorRequestId: routing.requestId,
          }
        : routing.service === 'runtime' || routing.service === 'http'
          ? { ...input, TargetId: routing.targetId }
          : input;
  return {
    deploymentId: routing.deploymentId,
    targetId: routing.targetId,
    provider: AWS_PROVIDER,
    engine: CLOUDFORMATION_ENGINE,
    service: routing.service,
    operation,
    resourceType: operationResourceType(routing.service, operation),
    input: contextualInput,
  };
}

function normalizeLogsInput(
  input: Readonly<Record<string, unknown>>
): Readonly<Record<string, unknown>> {
  return Object.fromEntries(
    Object.entries(input).map(([key, value]) => [
      LOGS_INPUT_KEYS[key] ?? key,
      value,
    ])
  );
}

async function awsJsonRoute(
  request: Request,
  url: URL,
  routing: RoutingContext,
  limit: number
): Promise<ProtocolRoute> {
  if (
    request.method !== 'POST' ||
    url.pathname !== routing.endpointPath ||
    url.search
  ) {
    return gatewayError(
      'UnsupportedCapability',
      'AWS JSON requests must use POST / without query parameters'
    );
  }
  if (
    request.headers.get('content-type')?.split(';')[0]?.trim().toLowerCase() !==
    'application/x-amz-json-1.1'
  ) {
    return gatewayError(
      'ValidationFailed',
      'AWS JSON requests require application/x-amz-json-1.1'
    );
  }
  const expectedTarget = JSON_TARGETS[routing.service];
  const target = request.headers.get('x-amz-target')?.trim() ?? '';
  const separator = target.lastIndexOf('.');
  if (
    !expectedTarget ||
    separator < 1 ||
    target.slice(0, separator) !== expectedTarget
  ) {
    return gatewayError(
      'UnsupportedCapability',
      `x-amz-target ${target || '(missing)'} does not match ${routing.service}`
    );
  }
  const operation = target.slice(separator + 1);
  operationResourceType(routing.service, operation);
  const text = utf8(await requestBytes(request, limit), 'AWS JSON body');
  let parsed: unknown;
  try {
    parsed = JSON.parse(text || '{}');
  } catch {
    return gatewayError('ValidationFailed', 'AWS JSON body is invalid');
  }
  const input = record(parsed, 'AWS JSON body');
  return {
    protocol: 'aws-json',
    operation,
    input: routing.service === 'logs' ? normalizeLogsInput(input) : input,
  };
}

type QueryContainer = Record<string, unknown> | unknown[];

function queryTokens(name: string): readonly (string | number)[] {
  const tokens = name
    .split('.')
    .filter((token) => token !== 'member')
    .map((token): string | number =>
      /^\d+$/.test(token) ? Number(token) - 1 : token
    );
  if (
    tokens.length === 0 ||
    tokens.some((token) => typeof token === 'number' && token < 0)
  ) {
    return gatewayError('ValidationFailed', `Query key ${name} is invalid`);
  }
  return tokens;
}

function queryEntry(
  current: QueryContainer,
  token: string | number,
  name: string
): unknown {
  if (typeof token === 'number') {
    if (!Array.isArray(current)) {
      gatewayError('ValidationFailed', `Query key ${name} is invalid`);
    }
    return current[token];
  }
  if (Array.isArray(current)) {
    gatewayError('ValidationFailed', `Query key ${name} is invalid`);
  }
  return current[token];
}

function setQueryEntry(
  current: QueryContainer,
  token: string | number,
  value: unknown,
  name: string
): void {
  if (typeof token === 'number') {
    if (!Array.isArray(current)) {
      gatewayError('ValidationFailed', `Query key ${name} is invalid`);
    }
    current[token] = value;
    return;
  }
  if (Array.isArray(current)) {
    gatewayError('ValidationFailed', `Query key ${name} is invalid`);
  }
  current[token] = value;
}

function queryChild(
  current: QueryContainer,
  token: string | number,
  next: string | number | undefined,
  name: string
): QueryContainer {
  const child = queryEntry(current, token, name);
  if (child === undefined) {
    const created: QueryContainer =
      typeof next === 'number' ? [] : Object.create(null);
    setQueryEntry(current, token, created, name);
    return created;
  }
  if (
    child === null ||
    typeof child !== 'object' ||
    (typeof next === 'number') !== Array.isArray(child)
  ) {
    gatewayError('ValidationFailed', `Query key ${name} conflicts`);
  }
  return child as QueryContainer;
}

function setQueryLeaf(
  current: QueryContainer,
  token: string | number,
  value: string,
  name: string
): void {
  if (queryEntry(current, token, name) !== undefined) {
    gatewayError('ValidationFailed', `Query key ${name} is duplicated`);
  }
  setQueryEntry(current, token, value, name);
}

function assignQueryValue(
  root: Record<string, unknown>,
  name: string,
  value: string
): void {
  const tokens = queryTokens(name);
  let current: QueryContainer = root;
  for (let index = 0; index < tokens.length; index += 1) {
    const token = tokens[index];
    if (token === undefined) {
      gatewayError('ValidationFailed', `Query key ${name} is invalid`);
    }
    if (index === tokens.length - 1) {
      setQueryLeaf(current, token, value, name);
    } else {
      current = queryChild(current, token, tokens[index + 1], name);
    }
  }
}

function assertCompleteQueryArrays(value: unknown, label = 'Query'): void {
  if (Array.isArray(value)) {
    if (value.some((entry) => entry === undefined)) {
      gatewayError(
        'ValidationFailed',
        `${label} array indices must be contiguous`
      );
    }
    for (const [index, entry] of value.entries()) {
      assertCompleteQueryArrays(entry, `${label}[${index}]`);
    }
    return;
  }
  if (value !== null && typeof value === 'object') {
    for (const [key, entry] of Object.entries(value)) {
      assertCompleteQueryArrays(entry, `${label}.${key}`);
    }
  }
}

function parsePolicyDocument(value: unknown): unknown {
  if (typeof value !== 'string') return value;
  try {
    return record(JSON.parse(value), 'PolicyDocument');
  } catch (error) {
    if (error instanceof AwsNativeGatewayError) throw error;
    return gatewayError('ValidationFailed', 'PolicyDocument is invalid JSON');
  }
}

const EC2_PERMISSION_COLLECTIONS = [
  ['IpRanges', 'CidrIp', 'CidrIp'],
  ['Ipv6Ranges', 'CidrIpv6', 'CidrIpv6'],
  ['UserIdGroupPairs', 'GroupId', 'SourceSecurityGroupId'],
  ['PrefixListIds', 'PrefixListId', 'DestinationPrefixListId'],
] as const;

const EC2_PERMISSION_COLLECTION_KEYS = new Set<string>(
  EC2_PERMISSION_COLLECTIONS.map(([collection]) => collection)
);

function ec2PermissionBase(
  permission: Readonly<Record<string, unknown>>
): Readonly<Record<string, unknown>> {
  const base: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(permission)) {
    if (EC2_PERMISSION_COLLECTION_KEYS.has(key)) continue;
    const numericPort =
      (key === 'FromPort' || key === 'ToPort') &&
      typeof value === 'string' &&
      /^-?\d+$/.test(value);
    base[key] = numericPort ? Number(value) : value;
  }
  return base;
}

function flattenEc2Permission(
  permission: Readonly<Record<string, unknown>>,
  index: number
): readonly Readonly<Record<string, unknown>>[] {
  const base = ec2PermissionBase(permission);
  const flattened: Readonly<Record<string, unknown>>[] = [];
  for (const [
    collectionKey,
    sourceKey,
    targetKey,
  ] of EC2_PERMISSION_COLLECTIONS) {
    const collection = permission[collectionKey];
    if (collection === undefined) continue;
    if (!Array.isArray(collection)) {
      gatewayError(
        'ValidationFailed',
        `IpPermissions[${index}].${collectionKey} must be an array`
      );
    }
    for (const [itemIndex, rawItem] of collection.entries()) {
      const item = record(
        rawItem,
        `IpPermissions[${index}].${collectionKey}[${itemIndex}]`
      );
      const value = item[sourceKey];
      if (typeof value !== 'string' || !value) {
        gatewayError(
          'ValidationFailed',
          `IpPermissions[${index}].${collectionKey}[${itemIndex}].${sourceKey} is required`
        );
      }
      flattened.push({ ...base, [targetKey]: value });
    }
  }
  return flattened.length > 0 ? flattened : [base];
}

function normalizeEc2Permissions(
  value: readonly unknown[]
): readonly Readonly<Record<string, unknown>>[] {
  const permissions: Readonly<Record<string, unknown>>[] = [];
  for (const [index, rawPermission] of value.entries()) {
    permissions.push(
      ...flattenEc2Permission(
        record(rawPermission, `IpPermissions[${index}]`),
        index
      )
    );
  }
  return permissions;
}

function renameEc2List(
  input: Record<string, unknown>,
  source: string,
  target: string
): void {
  if (!Array.isArray(input[source])) return;
  input[target] = input[source];
  delete input[source];
}

function normalizeEc2Input(
  input: Record<string, unknown>
): Readonly<Record<string, unknown>> {
  renameEc2List(input, 'GroupId', 'GroupIds');
  renameEc2List(input, 'InstanceId', 'InstanceIds');
  const permissions = input['IpPermissions'];
  if (Array.isArray(permissions)) {
    input['IpPermissions'] = normalizeEc2Permissions(permissions);
  }
  return input;
}

function normalizeQueryInput(
  service: string,
  operation: string,
  input: Record<string, unknown>
): Readonly<Record<string, unknown>> {
  if (service === 'ec2') return normalizeEc2Input(input);
  if (service === 'iam' && operation === 'PutRolePolicy') {
    input['PolicyDocument'] = parsePolicyDocument(input['PolicyDocument']);
  }
  return input;
}

async function queryRoute(
  request: Request,
  url: URL,
  routing: RoutingContext,
  limit: number
): Promise<ProtocolRoute> {
  if (request.method !== 'POST' || url.pathname !== '/' || url.search) {
    return gatewayError(
      'UnsupportedCapability',
      'AWS Query requests must use POST / without URL query parameters'
    );
  }
  if (
    request.headers.get('content-type')?.split(';')[0]?.trim().toLowerCase() !==
    'application/x-www-form-urlencoded'
  ) {
    return gatewayError(
      'ValidationFailed',
      'AWS Query requests require application/x-www-form-urlencoded'
    );
  }
  const queryService = QUERY_SERVICES[routing.service];
  if (!queryService) {
    return gatewayError(
      'UnsupportedCapability',
      `AWS Query service ${routing.service} is not implemented`
    );
  }
  const body = utf8(await requestBytes(request, limit), 'AWS Query body');
  const parameters = new URLSearchParams(body);
  const action = parameters.get('Action')?.trim() ?? '';
  const version = parameters.get('Version')?.trim() ?? '';
  if (!action || version !== queryService.version) {
    return gatewayError(
      'ValidationFailed',
      `AWS Query Action or Version is invalid for ${routing.service}`
    );
  }
  operationResourceType(routing.service, action);
  const input: Record<string, unknown> = Object.create(null);
  for (const [name, value] of parameters) {
    if (name !== 'Action' && name !== 'Version') {
      assignQueryValue(input, name, value);
    }
  }
  assertCompleteQueryArrays(input);
  return {
    protocol: 'query',
    operation: action,
    input: normalizeQueryInput(routing.service, action, input),
  };
}

function decodedPathSegment(value: string, label: string): string {
  try {
    const decoded = decodeURIComponent(value);
    if (!decoded || decoded.includes('\0')) {
      return gatewayError('ValidationFailed', `${label} is invalid`);
    }
    return decoded;
  } catch {
    return gatewayError('ValidationFailed', `${label} is not URL encoded`);
  }
}

async function lambdaCreateRoute(
  request: Request,
  url: URL,
  limit: number
): Promise<ProtocolRoute> {
  if (request.method !== 'POST' || url.search) {
    return gatewayError(
      'UnsupportedCapability',
      'Lambda CreateFunction requires POST without query parameters'
    );
  }
  const contentType = request.headers
    .get('content-type')
    ?.split(';')[0]
    ?.trim()
    .toLowerCase();
  if (
    contentType !== 'application/json' &&
    contentType !== 'application/x-amz-json-1.1'
  ) {
    return gatewayError(
      'ValidationFailed',
      'Lambda CreateFunction requires a JSON content type'
    );
  }
  const text = utf8(
    await requestBytes(request, limit),
    'Lambda CreateFunction body'
  );
  let parsed: unknown;
  try {
    parsed = JSON.parse(text || '{}');
  } catch {
    return gatewayError(
      'ValidationFailed',
      'Lambda CreateFunction body is invalid'
    );
  }
  return {
    protocol: 'rest-json',
    operation: 'CreateFunction',
    input: record(parsed, 'Lambda CreateFunction body'),
  };
}

async function lambdaRoute(
  request: Request,
  url: URL,
  limit: number
): Promise<ProtocolRoute> {
  if (url.pathname === '/2015-03-31/functions') {
    return lambdaCreateRoute(request, url, limit);
  }
  const match = /^\/2015-03-31\/functions\/([^/]+)(\/invocations)?$/.exec(
    url.pathname
  );
  if (!match) {
    return gatewayError(
      'UnsupportedCapability',
      `Lambda REST-JSON path ${url.pathname} is not implemented`
    );
  }
  const functionName = decodedPathSegment(match[1] ?? '', 'FunctionName');
  if (match[2]) {
    if (request.method !== 'POST') {
      return gatewayError(
        'UnsupportedCapability',
        'Lambda InvokeFunction requires POST'
      );
    }
    const unsupported = [...url.searchParams.keys()].find(
      (key) => key !== 'Qualifier'
    );
    if (unsupported) {
      return gatewayError(
        'UnsupportedCapability',
        `Lambda query parameter ${unsupported} is not implemented`
      );
    }
    const payload = utf8(
      await requestBytes(request, limit),
      'Lambda invocation payload'
    );
    return {
      protocol: 'rest-json',
      operation: 'InvokeFunction',
      input: {
        FunctionName: functionName,
        Payload: payload || '{}',
        ...(url.searchParams.has('Qualifier')
          ? { Qualifier: url.searchParams.get('Qualifier') ?? '' }
          : {}),
      },
    };
  }
  if (request.method !== 'GET' || url.search) {
    return gatewayError(
      'UnsupportedCapability',
      'Lambda GetFunction requires GET without query parameters'
    );
  }
  await assertNoBody(request, limit);
  return {
    protocol: 'rest-json',
    operation: 'GetFunction',
    input: { FunctionName: functionName },
  };
}

function s3Path(url: URL): { bucket: string; key?: string } {
  const match = /^\/([^/]+)(?:\/(.*))?$/.exec(url.pathname);
  if (!match) {
    return gatewayError(
      'UnsupportedCapability',
      'S3 REST-XML requires a path-style bucket'
    );
  }
  const bucket = decodedPathSegment(match[1] ?? '', 'S3 bucket');
  if (!/^[a-z0-9][a-z0-9.-]{1,62}$/.test(bucket)) {
    return gatewayError('ValidationFailed', 'S3 bucket name is invalid');
  }
  const rawKey = match[2];
  return {
    bucket,
    ...(rawKey === undefined
      ? {}
      : { key: decodedPathSegment(rawKey, 'S3 object key') }),
  };
}

function s3Metadata(headers: Headers): Readonly<Record<string, string>> {
  return Object.fromEntries(
    [...headers.entries()]
      .filter(([name]) => name.startsWith('x-amz-meta-'))
      .map(([name, value]) => [name.slice('x-amz-meta-'.length), value])
  );
}

function s3BucketGetRoute(url: URL, bucket: string): ProtocolRoute {
  const queryKeys = [...new Set(url.searchParams.keys())];
  if (queryKeys.length === 1 && queryKeys[0] === 'location') {
    return {
      protocol: 'rest-xml',
      operation: 'GetBucketLocation',
      input: { Bucket: bucket },
    };
  }
  const unsupported = queryKeys.find(
    (name) => !['list-type', 'prefix'].includes(name)
  );
  if (
    unsupported ||
    (url.searchParams.has('list-type') &&
      url.searchParams.get('list-type') !== '2')
  ) {
    return gatewayError(
      'UnsupportedCapability',
      `S3 bucket query ${unsupported ?? 'list-type'} is not implemented`
    );
  }
  return {
    protocol: 'rest-xml',
    operation: 'ListBucket',
    input: {
      Bucket: bucket,
      ...(url.searchParams.has('prefix')
        ? { Prefix: url.searchParams.get('prefix') ?? '' }
        : {}),
    },
  };
}

async function s3ObjectRoute(
  request: Request,
  bucket: string,
  key: string,
  limit: number
): Promise<ProtocolRoute> {
  switch (request.method) {
    case 'GET':
    case 'HEAD':
      await assertNoBody(request, limit);
      return {
        protocol: 'rest-xml',
        operation: 'GetObject',
        input: { Bucket: bucket, Key: key },
        ...(request.method === 'HEAD' ? { headOnly: true } : {}),
      };
    case 'PUT': {
      const body = utf8(await requestBytes(request, limit), 'S3 object body');
      return {
        protocol: 'rest-xml',
        operation: 'PutObject',
        input: {
          Bucket: bucket,
          Key: key,
          Body: body,
          ContentType:
            request.headers.get('content-type') ?? 'application/octet-stream',
          Metadata: s3Metadata(request.headers),
        },
      };
    }
    case 'DELETE':
      await assertNoBody(request, limit);
      return {
        protocol: 'rest-xml',
        operation: 'DeleteObject',
        input: { Bucket: bucket, Key: key },
      };
    default:
      return gatewayError(
        'UnsupportedCapability',
        `S3 REST-XML object method ${request.method} is not implemented`
      );
  }
}

async function s3Route(
  request: Request,
  url: URL,
  limit: number
): Promise<ProtocolRoute> {
  const { bucket, key } = s3Path(url);
  if (request.method === 'GET' && key === undefined) {
    return s3BucketGetRoute(url, bucket);
  }
  if (key !== undefined && [...url.searchParams.keys()].length === 0) {
    return s3ObjectRoute(request, bucket, key, limit);
  }
  return gatewayError(
    'UnsupportedCapability',
    `S3 REST-XML ${request.method} ${url.pathname}${url.search} is not implemented`
  );
}

async function protocolRoute(
  request: Request,
  url: URL,
  routing: RoutingContext,
  limit: number
): Promise<ProtocolRoute> {
  if (JSON_TARGETS[routing.service]) {
    return awsJsonRoute(request, url, routing, limit);
  }
  if (QUERY_SERVICES[routing.service]) {
    return queryRoute(request, url, routing, limit);
  }
  if (routing.service === 'lambda') {
    return lambdaRoute(request, url, limit);
  }
  if (routing.service === 's3') return s3Route(request, url, limit);
  return gatewayError(
    'UnsupportedCapability',
    `AWS native service ${routing.service} is not implemented`
  );
}

function xmlEscape(value: unknown): string {
  return String(value)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&apos;');
}

function queryListMember(service: string, name: string): string {
  if (service === 'rds' && name === 'DBInstances') return 'DBInstance';
  return 'member';
}

function xmlElement(name: string, value: unknown, service: string): string {
  if (value === undefined || value === null) return '';
  if (
    service === 'iam' &&
    name.endsWith('PolicyDocument') &&
    typeof value === 'object' &&
    !Array.isArray(value)
  ) {
    return `<${name}>${xmlEscape(
      encodeURIComponent(JSON.stringify(value))
    )}</${name}>`;
  }
  if (Array.isArray(value)) {
    const member = queryListMember(service, name);
    return `<${name}>${value.map((entry) => xmlElement(member, entry, service)).join('')}</${name}>`;
  }
  if (typeof value === 'object') {
    const children = Object.entries(value)
      .map(([key, entry]) => xmlElement(key, entry, service))
      .join('');
    return `<${name}>${children}</${name}>`;
  }
  return `<${name}>${xmlEscape(
    typeof value === 'boolean' ? String(value).toLowerCase() : value
  )}</${name}>`;
}

function lowerFirst(value: string): string {
  return `${value.slice(0, 1).toLowerCase()}${value.slice(1)}`;
}

const EC2_LOCATIONS: Readonly<Record<string, string>> = {
  SecurityGroups: 'securityGroupInfo',
  Reservations: 'reservationSet',
  Instances: 'instancesSet',
  IpPermissions: 'ipPermissions',
  IpPermissionsEgress: 'ipPermissionsEgress',
  Tags: 'tagSet',
  Description: 'groupDescription',
  Return: 'return',
};

function ec2PermissionXml(value: unknown): string {
  const permission = record(value, 'EC2 permission response');
  const direct = Object.entries(permission)
    .filter(
      ([key]) =>
        ![
          'CidrIp',
          'CidrIpv6',
          'SourceSecurityGroupId',
          'DestinationPrefixListId',
        ].includes(key)
    )
    .map(([key, entry]) => ec2XmlElement(key, entry))
    .join('');
  const ranges = [
    ['CidrIp', 'ipRanges', 'cidrIp'],
    ['CidrIpv6', 'ipv6Ranges', 'cidrIpv6'],
    ['SourceSecurityGroupId', 'groups', 'groupId'],
    ['DestinationPrefixListId', 'prefixListIds', 'prefixListId'],
  ] as const;
  let nested = '';
  for (const [sourceKey, collection, member] of ranges) {
    const entry = permission[sourceKey];
    if (entry !== undefined) {
      nested += `<${collection}><item><${member}>${xmlEscape(
        entry
      )}</${member}></item></${collection}>`;
    }
  }
  return `<item>${direct}${nested}</item>`;
}

function ec2XmlElement(name: string, value: unknown): string {
  const location =
    name === 'SecurityGroups' &&
    Array.isArray(value) &&
    value.every((entry) => typeof entry === 'string')
      ? 'groupSet'
      : (EC2_LOCATIONS[name] ?? lowerFirst(name));
  if (value === undefined || value === null) return '';
  if (Array.isArray(value)) {
    if (name === 'IpPermissions' || name === 'IpPermissionsEgress') {
      return `<${location}>${value.map(ec2PermissionXml).join('')}</${location}>`;
    }
    if (name === 'SecurityGroups') {
      return `<${location}>${value
        .map((entry) =>
          ec2XmlElement(
            'item',
            typeof entry === 'string' ? { GroupId: entry } : entry
          )
        )
        .join('')}</${location}>`;
    }
    return `<${location}>${value
      .map((entry) => ec2XmlElement('item', entry))
      .join('')}</${location}>`;
  }
  if (typeof value === 'object') {
    return `<${location}>${Object.entries(value)
      .map(([key, entry]) => ec2XmlElement(key, entry))
      .join('')}</${location}>`;
  }
  return `<${location}>${xmlEscape(value)}</${location}>`;
}

function responseHeaders(requestId: string, contentType?: string): Headers {
  const headers = new Headers({
    'x-amzn-requestid': requestId,
    'x-amz-request-id': requestId,
  });
  if (contentType) headers.set('content-type', contentType);
  return headers;
}

function querySuccess(
  translated: AwsNativeGatewayCommand,
  response: Readonly<Record<string, unknown>>
): Response {
  const service = QUERY_SERVICES[translated.service];
  if (!service) {
    return gatewayError(
      'UnsupportedCapability',
      `AWS Query service ${translated.service} has no serializer`
    );
  }
  const body =
    translated.service === 'ec2'
      ? `<${translated.operation}Response xmlns="${service.namespace}"><requestId>${translated.requestId}</requestId>${Object.entries(
          response
        )
          .map(([key, value]) => ec2XmlElement(key, value))
          .join('')}</${translated.operation}Response>`
      : `<${translated.operation}Response xmlns="${service.namespace}"><${translated.operation}Result>${Object.entries(
          response
        )
          .map(([key, value]) => xmlElement(key, value, translated.service))
          .join(
            ''
          )}</${translated.operation}Result><ResponseMetadata><RequestId>${translated.requestId}</RequestId></ResponseMetadata></${translated.operation}Response>`;
  return new Response(body, {
    status: 200,
    headers: responseHeaders(translated.requestId, 'text/xml; charset=utf-8'),
  });
}

function awsJsonSuccess(
  translated: AwsNativeGatewayCommand,
  response: Readonly<Record<string, unknown>>
): Response {
  return new Response(JSON.stringify(response), {
    status: 200,
    headers: responseHeaders(
      translated.requestId,
      'application/x-amz-json-1.1'
    ),
  });
}

function lambdaSuccess(
  translated: AwsNativeGatewayCommand,
  response: Readonly<Record<string, unknown>>
): Response {
  const headers = responseHeaders(translated.requestId, 'application/json');
  if (translated.operation === 'InvokeFunction') {
    const version = response['ExecutedVersion'];
    if (typeof version === 'string') {
      headers.set('x-amz-executed-version', version);
    }
    return new Response(JSON.stringify(response['Payload'] ?? null), {
      status: 200,
      headers,
    });
  }
  return new Response(JSON.stringify(response), { status: 200, headers });
}

function s3ListXml(response: Readonly<Record<string, unknown>>): string {
  const contents = Array.isArray(response['Contents'])
    ? response['Contents']
        .map((entry) => xmlElement('Contents', entry, 's3'))
        .join('')
    : '';
  const scalar = Object.entries(response)
    .filter(([key]) => key !== 'Contents')
    .map(([key, value]) => xmlElement(key, value, 's3'))
    .join('');
  return `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">${scalar}${contents}</ListBucketResult>`;
}

function s3ObjectHeaders(
  requestId: string,
  response: Readonly<Record<string, unknown>>
): Headers {
  const contentType = response['ContentType'];
  const headers = responseHeaders(
    requestId,
    typeof contentType === 'string' ? contentType : 'application/octet-stream'
  );
  const values = [
    ['ContentLength', 'content-length'],
    ['ETag', 'etag'],
    ['LastModified', 'last-modified'],
  ] as const;
  for (const [source, target] of values) {
    const value = response[source];
    if (value === undefined) continue;
    const headerValue =
      source === 'ETag'
        ? `"${String(value).replace(/^"|"$/g, '')}"`
        : source === 'LastModified'
          ? new Date(String(value)).toUTCString()
          : String(value);
    headers.set(target, headerValue);
  }
  const metadata = response['Metadata'];
  if (metadata && typeof metadata === 'object' && !Array.isArray(metadata)) {
    for (const [name, value] of Object.entries(metadata)) {
      headers.set(`x-amz-meta-${name}`, String(value));
    }
  }
  return headers;
}

function s3GetSuccess(
  translated: AwsNativeGatewayCommand,
  response: Readonly<Record<string, unknown>>
): Response {
  return new Response(
    translated.headOnly ? null : String(response['Body'] ?? ''),
    { status: 200, headers: s3ObjectHeaders(translated.requestId, response) }
  );
}

function s3LocationSuccess(
  translated: AwsNativeGatewayCommand,
  response: Readonly<Record<string, unknown>>
): Response {
  return new Response(
    `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">${xmlEscape(
      response['LocationConstraint'] ?? ''
    )}</LocationConstraint>`,
    {
      status: 200,
      headers: responseHeaders(translated.requestId, 'application/xml'),
    }
  );
}

function s3Success(
  translated: AwsNativeGatewayCommand,
  response: Readonly<Record<string, unknown>>
): Response {
  const headers = responseHeaders(translated.requestId);
  switch (translated.operation) {
    case 'PutObject': {
      const etag = response['ETag'];
      if (typeof etag === 'string') headers.set('etag', `"${etag}"`);
      return new Response(null, { status: 200, headers });
    }
    case 'GetObject':
      return s3GetSuccess(translated, response);
    case 'DeleteObject':
      return new Response(null, { status: 204, headers });
    case 'ListBucket':
      headers.set('content-type', 'application/xml');
      return new Response(s3ListXml(response), { status: 200, headers });
    case 'GetBucketLocation':
      return s3LocationSuccess(translated, response);
    default:
      return gatewayError(
        'UnsupportedCapability',
        `S3 operation ${translated.operation} has no serializer`
      );
  }
}

function successResponse(
  translated: AwsNativeGatewayCommand,
  response: Readonly<Record<string, unknown>>
): Response {
  switch (translated.protocol) {
    case 'aws-json':
      return awsJsonSuccess(translated, response);
    case 'query':
      return querySuccess(translated, response);
    case 'rest-json':
      return lambdaSuccess(translated, response);
    case 'rest-xml':
      return s3Success(translated, response);
  }
}

function looseService(request: Request): string | undefined {
  const authorization = request.headers.get('authorization') ?? '';
  return LOOSE_SERVICE_PATTERN.exec(authorization)?.[1];
}

function protocolHint(request: Request): AwsNativeProtocol {
  const service = looseService(request);
  if (service === 'lambda') return 'rest-json';
  if (service === 's3') return 'rest-xml';
  if (service && QUERY_SERVICES[service]) return 'query';
  return 'aws-json';
}

function requestIdHint(request: Request): string {
  const signature = /Signature=([0-9a-f]{64})/.exec(
    request.headers.get('authorization') ?? ''
  )?.[1];
  return `tcsim-${(signature ?? 'request').slice(0, 24)}`;
}

function errorDetails(error: AwsNativeGatewayError | CoreError): {
  code: string;
  message: string;
  status: number;
} {
  if (error instanceof AwsNativeGatewayError) {
    return { code: error.code, message: error.message, status: error.status };
  }
  const status =
    error.code === 'NotFound'
      ? 404
      : error.code === 'Conflict' || error.code === 'IdempotencyConflict'
        ? 409
        : error.code === 'QuotaExceeded'
          ? 429
          : error.code === 'UnsupportedCapability'
            ? 422
            : 400;
  return { code: error.code, message: error.message, status };
}

const AUTHORIZATION_ERROR_CODES: Readonly<Record<AwsNativeProtocol, string>> = {
  'aws-json': 'UnrecognizedClientException',
  query: 'InvalidClientTokenId',
  'rest-json': 'UnrecognizedClientException',
  'rest-xml': 'InvalidAccessKeyId',
};

const VALIDATION_ERROR_CODES: Readonly<Record<AwsNativeProtocol, string>> = {
  'aws-json': 'ValidationException',
  query: 'ValidationError',
  'rest-json': 'ValidationException',
  'rest-xml': 'InvalidRequest',
};

function notFoundErrorCode(
  protocol: AwsNativeProtocol,
  service: string | undefined,
  message: string
): string {
  if (protocol === 'rest-xml') {
    return message.includes('object') ? 'NoSuchKey' : 'NoSuchBucket';
  }
  return service === 'iam' ? 'NoSuchEntity' : 'ResourceNotFoundException';
}

function awsErrorCode(
  protocol: AwsNativeProtocol,
  service: string | undefined,
  code: string,
  message: string
): string {
  switch (code) {
    case 'UnauthorizedOperation':
      return AUTHORIZATION_ERROR_CODES[protocol];
    case 'NotFound':
      return notFoundErrorCode(protocol, service, message);
    case 'ValidationFailed':
      return VALIDATION_ERROR_CODES[protocol];
    case 'QuotaExceeded':
      return 'ThrottlingException';
    case 'Conflict':
    case 'IdempotencyConflict':
      return 'ConflictException';
    default:
      return `${code}${protocol === 'query' ? '' : 'Exception'}`;
  }
}

function failureResponse(
  request: Request,
  error: AwsNativeGatewayError | CoreError,
  translated?: AwsNativeGatewayCommand
): Response {
  const protocol = translated?.protocol ?? protocolHint(request);
  const service = translated?.service ?? looseService(request);
  const requestId = translated?.requestId ?? requestIdHint(request);
  const details = errorDetails(error);
  const code = awsErrorCode(protocol, service, details.code, details.message);
  const headers = responseHeaders(requestId);
  const status = details.status === 422 ? 400 : details.status;
  if (protocol === 'query') {
    headers.set('content-type', 'text/xml; charset=utf-8');
    const namespace = service ? QUERY_SERVICES[service]?.namespace : undefined;
    return new Response(
      `<Response${namespace ? ` xmlns="${namespace}"` : ''}><Errors><Error><Type>Sender</Type><Code>${xmlEscape(
        code
      )}</Code><Message>${xmlEscape(
        details.message
      )}</Message></Error></Errors><RequestId>${requestId}</RequestId></Response>`,
      { status, headers }
    );
  }
  if (protocol === 'rest-xml') {
    headers.set('content-type', 'application/xml');
    return new Response(
      `<Error><Code>${xmlEscape(code)}</Code><Message>${xmlEscape(
        details.message
      )}</Message><RequestId>${requestId}</RequestId></Error>`,
      { status, headers }
    );
  }
  headers.set(
    'content-type',
    protocol === 'aws-json' ? 'application/x-amz-json-1.1' : 'application/json'
  );
  headers.set('x-amzn-errortype', code);
  return new Response(
    JSON.stringify({
      __type: code,
      ...(protocol === 'rest-json' ? { Type: 'User' } : {}),
      message: details.message,
    }),
    { status, headers }
  );
}

function isKnownError(
  error: unknown
): error is AwsNativeGatewayError | CoreError {
  return error instanceof AwsNativeGatewayError || error instanceof CoreError;
}

export class AwsNativeGateway {
  readonly #origin: string;
  readonly #accessKeyId: string;
  readonly #bodyLimit: number;
  readonly #onCommandSuccess:
    | AwsNativeGatewayOptions['onCommandSuccess']
    | undefined;
  readonly #beforeCommand: AwsNativeGatewayOptions['beforeCommand'] | undefined;

  constructor(options: AwsNativeGatewayOptions) {
    this.#origin = normalizedOrigin(options.simulatorOrigin);
    this.#accessKeyId = simulatorAccessKey(options.simulatorAccessKeyId);
    this.#bodyLimit = bodyLimit(options.maxBodyBytes);
    this.#onCommandSuccess = options.onCommandSuccess;
    this.#beforeCommand = options.beforeCommand;
  }

  async translate(request: Request): Promise<AwsNativeGatewayCommand> {
    if (request.url.length > 4096) {
      return gatewayError('ValidationFailed', 'AWS native URL is too long');
    }
    const url = new URL(request.url);
    if (url.origin !== this.#origin) {
      return gatewayError(
        'UnauthorizedOperation',
        'AWS native endpoint is not simulator-owned'
      );
    }
    const routing = routingContext(request, this.#accessKeyId);
    const route = await protocolRoute(request, url, routing, this.#bodyLimit);
    const translatedCommand = command(
      routing,
      route.operation,
      route.input,
      this.#origin
    );
    return {
      worldId: routing.worldId,
      protocol: route.protocol,
      service: routing.service,
      operation: route.operation,
      requestId: routing.requestId,
      idempotencyKey: `aws-native:${routing.accessKeyId}:${routing.signature}`,
      ...(route.headOnly === undefined ? {} : { headOnly: route.headOnly }),
      command: translatedCommand,
    };
  }

  async handle(request: Request, core: SimulationCore): Promise<Response> {
    let translated: AwsNativeGatewayCommand | undefined;
    try {
      translated = await this.translate(request);
      await this.#beforeCommand?.(translated);
      const response = await core.executeCommandAsync(
        translated.worldId,
        translated.command,
        translated.idempotencyKey
      );
      await this.#onCommandSuccess?.(translated, response);
      return successResponse(translated, response);
    } catch (error) {
      if (!isKnownError(error)) throw error;
      return failureResponse(request, error, translated);
    }
  }
}
