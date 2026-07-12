import type { ExecuteCommandInput } from '@tenkacloud/simulator-core';
import {
  APPLICATION_RESOURCE,
  parseApplicationInput,
  VERSION_RESOURCE,
} from './application';

export const SAKURA_APPRUN_API_BASE_PATH = '/cloud/api/apprun/1.0/apprun/api';
export const SAKURA_APPRUN_DEFAULT_BODY_LIMIT = 64 * 1024;
export const SAKURA_APPRUN_WORLD_HEADER = 'x-tenkacloud-world-id';
export const SAKURA_APPRUN_DEPLOYMENT_HEADER = 'x-tenkacloud-deployment-id';
export const SAKURA_APPRUN_TARGET_HEADER = 'x-tenkacloud-target-id';

export type SakuraAppRunGatewayErrorCode =
  | 'UnauthorizedOperation'
  | 'ValidationFailed'
  | 'QuotaExceeded'
  | 'UnsupportedCapability';

export class SakuraAppRunGatewayError extends Error {
  readonly status: 400 | 401 | 413 | 422;

  constructor(
    readonly code: SakuraAppRunGatewayErrorCode,
    message: string
  ) {
    super(message);
    this.name = 'SakuraAppRunGatewayError';
    this.status =
      code === 'UnauthorizedOperation'
        ? 401
        : code === 'QuotaExceeded'
          ? 413
          : code === 'UnsupportedCapability'
            ? 422
            : 400;
  }
}

export interface SakuraAppRunGatewayOptions {
  readonly simulatorOrigin: string;
  readonly simulatorCredential: string;
  readonly maxBodyBytes?: number;
}

export interface SakuraAppRunGatewayCommand {
  readonly worldId: string;
  readonly command: ExecuteCommandInput;
}

interface RoutingContext {
  readonly worldId: string;
  readonly deploymentId: string;
  readonly targetId: string;
}

type AppRunRoute =
  | { readonly kind: 'applications' }
  | { readonly kind: 'application'; readonly id: string }
  | {
      readonly kind: 'version';
      readonly id: string;
      readonly versionId: string;
    }
  | { readonly kind: 'traffics'; readonly id: string }
  | { readonly kind: 'packet-filter'; readonly id: string };

function gatewayError(
  code: SakuraAppRunGatewayErrorCode,
  message: string
): never {
  throw new SakuraAppRunGatewayError(code, message);
}

function simulatorOrigin(value: string): string {
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

function simulatorCredential(value: string): string {
  if (
    !/^tcsim_[A-Za-z0-9_-]{16,128}:tcsim_[A-Za-z0-9_-]{16,128}$/.test(value)
  ) {
    return gatewayError(
      'ValidationFailed',
      'simulatorCredential must be a simulator-owned Basic credential pair'
    );
  }
  return value;
}

function bodyLimit(value: number | undefined): number {
  const limit = value ?? SAKURA_APPRUN_DEFAULT_BODY_LIMIT;
  if (!Number.isInteger(limit) || limit < 1 || limit > 1024 * 1024) {
    return gatewayError(
      'ValidationFailed',
      'maxBodyBytes must be an integer from 1 through 1048576'
    );
  }
  return limit;
}

function decodeBasicCredential(value: string): string | undefined {
  if (
    value.length % 4 !== 0 ||
    !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(
      value
    )
  ) {
    return undefined;
  }
  const binary = atob(value);
  const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0));
  let decoded: string;
  try {
    decoded = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  } catch {
    return undefined;
  }
  const separator = decoded.indexOf(':');
  if (separator < 1 || separator !== decoded.lastIndexOf(':')) return undefined;
  return decoded;
}

function authorize(request: Request, expectedCredential: string): void {
  const authorization = request.headers.get('authorization')?.trim();
  const match = authorization
    ? /^Basic\s+([^\s]+)$/i.exec(authorization)
    : null;
  const decoded = match ? decodeBasicCredential(match[1] ?? '') : undefined;
  if (decoded === expectedCredential) return;
  if (decoded) {
    const separator = decoded.indexOf(':');
    const token = decoded.slice(0, separator);
    const secret = decoded.slice(separator + 1);
    if (!token.startsWith('tcsim_') || !secret.startsWith('tcsim_')) {
      gatewayError(
        'UnauthorizedOperation',
        'real Sakura AppRun credentials are not accepted'
      );
    }
    gatewayError('UnauthorizedOperation', 'simulator credential is invalid');
  }
  gatewayError(
    'UnauthorizedOperation',
    'simulator-owned Basic authorization is required'
  );
}

function rejectCredentialQuery(url: URL): void {
  const credentialKey = [
    'access_token',
    'access_token_secret',
    'api_key',
    'password',
  ].find((key) => url.searchParams.has(key));
  if (credentialKey) {
    gatewayError(
      'UnauthorizedOperation',
      'real Sakura query credentials are not accepted'
    );
  }
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

function routingContext(request: Request): RoutingContext {
  const worldId = routingHeader(
    request.headers,
    SAKURA_APPRUN_WORLD_HEADER,
    true
  );
  const deploymentId = routingHeader(
    request.headers,
    SAKURA_APPRUN_DEPLOYMENT_HEADER,
    true
  );
  const targetId =
    routingHeader(request.headers, SAKURA_APPRUN_TARGET_HEADER, false) ??
    'default';
  if (!worldId || !deploymentId) {
    return gatewayError('ValidationFailed', 'routing headers are required');
  }
  return { worldId, deploymentId, targetId };
}

function pathSegment(encoded: string, label: string): string {
  let decoded: string;
  try {
    decoded = decodeURIComponent(encoded);
  } catch {
    return gatewayError('ValidationFailed', `${label} is not URL encoded`);
  }
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(decoded)) {
    return gatewayError('ValidationFailed', `${label} is invalid`);
  }
  return decoded;
}

function appRunRoute(url: URL): AppRunRoute {
  const collectionPath = `${SAKURA_APPRUN_API_BASE_PATH}/applications`;
  if (url.pathname === collectionPath) return { kind: 'applications' };
  if (!url.pathname.startsWith(`${collectionPath}/`)) {
    return gatewayError(
      'UnsupportedCapability',
      `Sakura AppRun path ${url.pathname} is not supported`
    );
  }
  const suffix = url.pathname.slice(collectionPath.length);
  const version = /^\/([^/]+)\/versions\/([^/]+)$/.exec(suffix);
  if (version) {
    return {
      kind: 'version',
      id: pathSegment(version[1] ?? '', 'application id'),
      versionId: pathSegment(version[2] ?? '', 'version id'),
    };
  }
  const traffics = /^\/([^/]+)\/traffics$/.exec(suffix);
  if (traffics) {
    return {
      kind: 'traffics',
      id: pathSegment(traffics[1] ?? '', 'application id'),
    };
  }
  const packetFilter = /^\/([^/]+)\/packet_filter$/.exec(suffix);
  if (packetFilter) {
    return {
      kind: 'packet-filter',
      id: pathSegment(packetFilter[1] ?? '', 'application id'),
    };
  }
  const application = /^\/([^/]+)$/.exec(suffix);
  if (application) {
    return {
      kind: 'application',
      id: pathSegment(application[1] ?? '', 'application id'),
    };
  }
  return gatewayError(
    'UnsupportedCapability',
    `Sakura AppRun path ${url.pathname} is not supported`
  );
}

function ensureNoQuery(url: URL): void {
  if (Array.from(url.searchParams.keys()).length > 0) {
    gatewayError('ValidationFailed', 'query parameters are not supported');
  }
}

function contentLength(request: Request, limit: number): number | undefined {
  const header = request.headers.get('content-length');
  if (header === null) return undefined;
  if (!/^\d+$/.test(header)) {
    return gatewayError('ValidationFailed', 'content-length is invalid');
  }
  const length = Number(header);
  if (length > limit) {
    return gatewayError('QuotaExceeded', `request body exceeds ${limit} bytes`);
  }
  return length;
}

async function requestBytes(
  request: Request,
  limit: number
): Promise<Uint8Array> {
  const declared = contentLength(request, limit);
  let bytes: Uint8Array;
  try {
    bytes = new Uint8Array(await request.arrayBuffer());
  } catch {
    return gatewayError('ValidationFailed', 'request body cannot be read');
  }
  if (bytes.byteLength > limit) {
    return gatewayError('QuotaExceeded', `request body exceeds ${limit} bytes`);
  }
  if (declared !== undefined && declared !== bytes.byteLength) {
    return gatewayError(
      'ValidationFailed',
      'content-length does not match request body'
    );
  }
  return bytes;
}

async function assertNoBody(request: Request, limit: number): Promise<void> {
  if ((await requestBytes(request, limit)).byteLength > 0) {
    gatewayError(
      'ValidationFailed',
      `${request.method} request body is not supported`
    );
  }
}

function validateContentType(request: Request): void {
  const contentType = request.headers.get('content-type')?.toLowerCase();
  if (!contentType || !/^application\/json(?:\s*;|$)/.test(contentType)) {
    gatewayError('ValidationFailed', 'content-type must be application/json');
  }
}

async function jsonBody(request: Request, limit: number): Promise<unknown> {
  const bytes = await requestBytes(request, limit);
  if (bytes.byteLength === 0) {
    return gatewayError('ValidationFailed', 'JSON request body is required');
  }
  validateContentType(request);
  let text: string;
  try {
    text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  } catch {
    return gatewayError('ValidationFailed', 'request body must be UTF-8');
  }
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return gatewayError('ValidationFailed', 'request body must be valid JSON');
  }
}

function record(value: unknown, label: string): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return gatewayError('ValidationFailed', `${label} must be an object`);
  }
  return value as Record<string, unknown>;
}

function validateKeys(
  value: Readonly<Record<string, unknown>>,
  allowed: readonly string[],
  label: string
): void {
  const unsupported = Object.keys(value).find((key) => !allowed.includes(key));
  if (unsupported) {
    gatewayError(
      'UnsupportedCapability',
      `${label}.${unsupported} is not supported`
    );
  }
}

function gatewayValidation<T>(operation: () => T): T {
  try {
    return operation();
  } catch (error) {
    return gatewayError(
      'ValidationFailed',
      error instanceof Error ? error.message : 'request body is invalid'
    );
  }
}

const APPLICATION_KEYS = [
  'name',
  'timeout_seconds',
  'port',
  'min_scale',
  'max_scale',
  'scale_target_concurrency',
  'components',
] as const;
const APPLICATION_PATCH_KEYS = [
  'timeout_seconds',
  'port',
  'min_scale',
  'max_scale',
  'scale_target_concurrency',
  'components',
  'all_traffic_available',
] as const;

function arrayRecords(
  value: unknown,
  label: string,
  allowed: readonly string[]
): readonly Record<string, unknown>[] {
  if (!Array.isArray(value)) {
    return gatewayError('ValidationFailed', `${label} must be an array`);
  }
  return value.map((entry, index) => {
    const object = record(entry, `${label}[${index}]`);
    validateKeys(object, allowed, `${label}[${index}]`);
    return object;
  });
}

function validateApplicationStructure(
  body: Readonly<Record<string, unknown>>
): void {
  const components = arrayRecords(body['components'], 'components', [
    'name',
    'max_cpu',
    'max_memory',
    'deploy_source',
    'env',
    'secret',
    'probe',
  ]);
  components.forEach((component, index) => {
    const deploySource = record(
      component['deploy_source'],
      `components[${index}].deploy_source`
    );
    validateKeys(
      deploySource,
      ['container_registry'],
      `components[${index}].deploy_source`
    );
    const registry = record(
      deploySource['container_registry'],
      `components[${index}].deploy_source.container_registry`
    );
    validateKeys(
      registry,
      ['image', 'server', 'username', 'password'],
      `components[${index}].deploy_source.container_registry`
    );
    if (component['env'] !== undefined) {
      arrayRecords(component['env'], `components[${index}].env`, [
        'key',
        'value',
      ]);
    }
    if (component['secret'] !== undefined) {
      arrayRecords(component['secret'], `components[${index}].secret`, [
        'key',
        'value',
      ]);
    }
    if (component['probe'] === undefined) return;
    const probe = record(component['probe'], `components[${index}].probe`);
    validateKeys(probe, ['http_get'], `components[${index}].probe`);
    const httpGet = record(
      probe['http_get'],
      `components[${index}].probe.http_get`
    );
    validateKeys(
      httpGet,
      ['path', 'port', 'headers'],
      `components[${index}].probe.http_get`
    );
    if (httpGet['headers'] !== undefined) {
      arrayRecords(
        httpGet['headers'],
        `components[${index}].probe.http_get.headers`,
        ['name', 'value']
      );
    }
  });
}

function createApplicationBody(
  value: unknown
): Readonly<Record<string, unknown>> {
  const body = record(value, 'request body');
  validateKeys(body, APPLICATION_KEYS, 'request body');
  validateApplicationStructure(body);
  return gatewayValidation(() => parseApplicationInput(body));
}

function optionalInteger(
  value: unknown,
  label: string,
  minimum: number,
  maximum: number
): number | undefined {
  if (value === undefined) return undefined;
  if (
    typeof value !== 'number' ||
    !Number.isInteger(value) ||
    value < minimum ||
    value > maximum
  ) {
    return gatewayError(
      'ValidationFailed',
      `${label} must be an integer from ${minimum} through ${maximum}`
    );
  }
  return value;
}

const RESERVED_PORTS = new Set([8008, 8012, 8013, 8022, 9090, 9091]);

function applicationPatchBody(
  value: unknown
): Readonly<Record<string, unknown>> {
  const body = record(value, 'request body');
  validateKeys(body, APPLICATION_PATCH_KEYS, 'request body');
  if (Object.keys(body).length === 0) {
    return gatewayError('ValidationFailed', 'application patch is empty');
  }
  const timeout = optionalInteger(
    body['timeout_seconds'],
    'timeout_seconds',
    1,
    300
  );
  const port = optionalInteger(body['port'], 'port', 1, 65_535);
  if (port !== undefined && RESERVED_PORTS.has(port)) {
    return gatewayError('ValidationFailed', 'port is reserved');
  }
  const minScale = optionalInteger(body['min_scale'], 'min_scale', 0, 10);
  const maxScale = optionalInteger(body['max_scale'], 'max_scale', 1, 10);
  if (minScale !== undefined && maxScale !== undefined && minScale > maxScale) {
    return gatewayError(
      'ValidationFailed',
      'min_scale must not exceed max_scale'
    );
  }
  const concurrency = optionalInteger(
    body['scale_target_concurrency'],
    'scale_target_concurrency',
    50,
    200
  );
  const allTraffic = body['all_traffic_available'];
  if (allTraffic !== undefined && typeof allTraffic !== 'boolean') {
    return gatewayError(
      'ValidationFailed',
      'all_traffic_available must be a boolean'
    );
  }
  let components: unknown;
  if (body['components'] !== undefined) {
    validateApplicationStructure({ components: body['components'] });
    components = gatewayValidation(
      () =>
        parseApplicationInput({
          name: 'gateway-validation',
          timeout_seconds: 60,
          port: 8080,
          min_scale: 0,
          max_scale: 1,
          components: body['components'],
        }).components
    );
  }
  return {
    ...(timeout === undefined ? {} : { timeout_seconds: timeout }),
    ...(port === undefined ? {} : { port }),
    ...(minScale === undefined ? {} : { min_scale: minScale }),
    ...(maxScale === undefined ? {} : { max_scale: maxScale }),
    ...(concurrency === undefined
      ? {}
      : { scale_target_concurrency: concurrency }),
    ...(components === undefined ? {} : { components }),
    ...(allTraffic === undefined ? {} : { all_traffic_available: allTraffic }),
  };
}

function queryValue(url: URL, name: string): string | undefined {
  const values = url.searchParams.getAll(name);
  if (values.length > 1) {
    return gatewayError('ValidationFailed', `${name} must appear at most once`);
  }
  return values[0];
}

function queryInteger(
  value: string | undefined,
  name: string,
  defaultValue: number,
  maximum?: number
): number {
  if (value === undefined) return defaultValue;
  if (!/^[1-9]\d*$/.test(value)) {
    return gatewayError('ValidationFailed', `${name} is invalid`);
  }
  const parsed = Number(value);
  if (
    !Number.isSafeInteger(parsed) ||
    (maximum !== undefined && parsed > maximum)
  ) {
    return gatewayError('ValidationFailed', `${name} is invalid`);
  }
  return parsed;
}

const SORT_FIELDS = new Set([
  'id',
  'name',
  'status',
  'public_url',
  'created_at',
]);

function listInput(url: URL): Readonly<Record<string, unknown>> {
  const allowed = ['page_num', 'page_size', 'sort_field', 'sort_order'];
  const unsupported = Array.from(url.searchParams.keys()).find(
    (key) => !allowed.includes(key)
  );
  if (unsupported) {
    return gatewayError(
      'UnsupportedCapability',
      `list query ${unsupported} is not supported`
    );
  }
  const pageNum = queryInteger(queryValue(url, 'page_num'), 'page_num', 1);
  const pageSize = queryInteger(
    queryValue(url, 'page_size'),
    'page_size',
    50,
    100
  );
  const sortField = queryValue(url, 'sort_field') ?? 'created_at';
  if (!SORT_FIELDS.has(sortField)) {
    return gatewayError('UnsupportedCapability', 'sort_field is not supported');
  }
  const sortOrder = queryValue(url, 'sort_order') ?? 'desc';
  if (sortOrder !== 'asc' && sortOrder !== 'desc') {
    return gatewayError('ValidationFailed', 'sort_order is invalid');
  }
  return {
    page_num: pageNum,
    page_size: pageSize,
    sort_field: sortField,
    sort_order: sortOrder,
  };
}

function requiredText(value: unknown, label: string, maximum: number): string {
  if (
    typeof value !== 'string' ||
    !value.trim() ||
    value !== value.trim() ||
    value.length > maximum
  ) {
    return gatewayError('ValidationFailed', `${label} is invalid`);
  }
  return value;
}

function trafficBody(value: unknown): readonly Record<string, unknown>[] {
  if (!Array.isArray(value) || value.length < 1 || value.length > 4) {
    return gatewayError(
      'ValidationFailed',
      'traffic body must contain from 1 through 4 entries'
    );
  }
  const entries = value.map((entry, index) => {
    const traffic = record(entry, `traffic[${index}]`);
    validateKeys(
      traffic,
      ['version_name', 'is_latest_version', 'percent'],
      `traffic[${index}]`
    );
    const versionName = traffic['version_name'];
    const latest = traffic['is_latest_version'];
    if (
      !(
        (versionName !== undefined && latest === undefined) ||
        (versionName === undefined && latest === true)
      )
    ) {
      return gatewayError(
        'ValidationFailed',
        `traffic[${index}] must select exactly one version`
      );
    }
    const percent = optionalInteger(
      traffic['percent'],
      `traffic[${index}].percent`,
      0,
      100
    );
    if (percent === undefined) {
      return gatewayError(
        'ValidationFailed',
        `traffic[${index}].percent is required`
      );
    }
    return {
      ...(versionName === undefined
        ? { is_latest_version: true }
        : {
            version_name: requiredText(
              versionName,
              `traffic[${index}].version_name`,
              255
            ),
          }),
      percent,
    };
  });
  if (
    entries.reduce(
      (sum, entry) => sum + Number(Reflect.get(entry, 'percent')),
      0
    ) !== 100
  ) {
    return gatewayError(
      'ValidationFailed',
      'traffic percentages must total 100'
    );
  }
  return entries;
}

function validIpv4(value: string): boolean {
  const octets = value.split('.');
  return (
    octets.length === 4 &&
    octets.every(
      (octet) => /^(?:0|[1-9]\d{0,2})$/.test(octet) && Number(octet) <= 255
    )
  );
}

function packetFilterBody(value: unknown): Readonly<Record<string, unknown>> {
  const body = record(value, 'request body');
  validateKeys(body, ['is_enabled', 'settings'], 'request body');
  if (Object.keys(body).length === 0) {
    return gatewayError('ValidationFailed', 'packet filter patch is empty');
  }
  const isEnabled = body['is_enabled'];
  if (isEnabled !== undefined && typeof isEnabled !== 'boolean') {
    return gatewayError('ValidationFailed', 'is_enabled must be a boolean');
  }
  let settings: readonly Record<string, unknown>[] | undefined;
  if (body['settings'] !== undefined) {
    settings = arrayRecords(body['settings'], 'settings', [
      'from_ip',
      'from_ip_prefix_length',
    ]);
    if (settings.length > 10) {
      return gatewayError(
        'ValidationFailed',
        'packet filter accepts at most 10 settings'
      );
    }
    settings = settings.map((setting, index) => {
      const fromIp = requiredText(
        setting['from_ip'],
        `settings[${index}].from_ip`,
        15
      );
      if (!validIpv4(fromIp)) {
        return gatewayError(
          'ValidationFailed',
          `settings[${index}].from_ip is invalid`
        );
      }
      const prefix = optionalInteger(
        setting['from_ip_prefix_length'],
        `settings[${index}].from_ip_prefix_length`,
        0,
        32
      );
      if (prefix === undefined) {
        return gatewayError(
          'ValidationFailed',
          `settings[${index}].from_ip_prefix_length is required`
        );
      }
      return { from_ip: fromIp, from_ip_prefix_length: prefix };
    });
  }
  return {
    ...(isEnabled === undefined ? {} : { is_enabled: isEnabled }),
    ...(settings === undefined ? {} : { settings }),
  };
}

function executeCommand(
  routing: RoutingContext,
  operation: string,
  resourceType: string,
  input: Readonly<Record<string, unknown>>
): ExecuteCommandInput {
  return {
    deploymentId: routing.deploymentId,
    targetId: routing.targetId,
    provider: 'sakura',
    engine: 'apprun',
    service: 'apprun',
    operation,
    resourceType,
    input,
  };
}

async function applicationsCommand(
  request: Request,
  url: URL,
  routing: RoutingContext,
  limit: number
): Promise<ExecuteCommandInput> {
  if (request.method === 'GET') {
    const input = listInput(url);
    await assertNoBody(request, limit);
    return executeCommand(
      routing,
      'listApplications',
      APPLICATION_RESOURCE,
      input
    );
  }
  if (request.method === 'POST') {
    ensureNoQuery(url);
    const application = createApplicationBody(await jsonBody(request, limit));
    return executeCommand(routing, 'postApplication', APPLICATION_RESOURCE, {
      application,
    });
  }
  return gatewayError(
    'UnsupportedCapability',
    `applications method ${request.method} is not supported`
  );
}

async function applicationCommand(
  request: Request,
  url: URL,
  route: Extract<AppRunRoute, { readonly kind: 'application' }>,
  routing: RoutingContext,
  limit: number
): Promise<ExecuteCommandInput> {
  ensureNoQuery(url);
  if (request.method === 'GET' || request.method === 'DELETE') {
    await assertNoBody(request, limit);
    return executeCommand(
      routing,
      request.method === 'GET' ? 'getApplication' : 'deleteApplication',
      APPLICATION_RESOURCE,
      { id: route.id }
    );
  }
  if (request.method === 'PATCH') {
    const application = applicationPatchBody(await jsonBody(request, limit));
    return executeCommand(routing, 'patchApplication', APPLICATION_RESOURCE, {
      id: route.id,
      application,
    });
  }
  return gatewayError(
    'UnsupportedCapability',
    `application method ${request.method} is not supported`
  );
}

async function versionCommand(
  request: Request,
  url: URL,
  route: Extract<AppRunRoute, { readonly kind: 'version' }>,
  routing: RoutingContext,
  limit: number
): Promise<ExecuteCommandInput> {
  ensureNoQuery(url);
  if (request.method !== 'GET' && request.method !== 'DELETE') {
    return gatewayError(
      'UnsupportedCapability',
      `version method ${request.method} is not supported`
    );
  }
  await assertNoBody(request, limit);
  return executeCommand(
    routing,
    request.method === 'GET' ? 'getVersion' : 'deleteVersion',
    VERSION_RESOURCE,
    { id: route.id, versionId: route.versionId }
  );
}

async function trafficsCommand(
  request: Request,
  url: URL,
  route: Extract<AppRunRoute, { readonly kind: 'traffics' }>,
  routing: RoutingContext,
  limit: number
): Promise<ExecuteCommandInput> {
  ensureNoQuery(url);
  if (request.method === 'GET') {
    await assertNoBody(request, limit);
    return executeCommand(routing, 'getTraffics', APPLICATION_RESOURCE, {
      id: route.id,
    });
  }
  if (request.method === 'PUT') {
    const traffics = trafficBody(await jsonBody(request, limit));
    return executeCommand(routing, 'putTraffics', APPLICATION_RESOURCE, {
      id: route.id,
      traffics,
    });
  }
  return gatewayError(
    'UnsupportedCapability',
    `traffics method ${request.method} is not supported`
  );
}

async function packetFilterCommand(
  request: Request,
  url: URL,
  route: Extract<AppRunRoute, { readonly kind: 'packet-filter' }>,
  routing: RoutingContext,
  limit: number
): Promise<ExecuteCommandInput> {
  ensureNoQuery(url);
  if (request.method === 'GET') {
    await assertNoBody(request, limit);
    return executeCommand(routing, 'getPacketFilter', APPLICATION_RESOURCE, {
      id: route.id,
    });
  }
  if (request.method === 'PATCH') {
    const packetFilter = packetFilterBody(await jsonBody(request, limit));
    return executeCommand(routing, 'patchPacketFilter', APPLICATION_RESOURCE, {
      id: route.id,
      packet_filter: packetFilter,
    });
  }
  return gatewayError(
    'UnsupportedCapability',
    `packet_filter method ${request.method} is not supported`
  );
}

async function routeCommand(
  request: Request,
  url: URL,
  route: AppRunRoute,
  routing: RoutingContext,
  limit: number
): Promise<ExecuteCommandInput> {
  switch (route.kind) {
    case 'applications':
      return applicationsCommand(request, url, routing, limit);
    case 'application':
      return applicationCommand(request, url, route, routing, limit);
    case 'version':
      return versionCommand(request, url, route, routing, limit);
    case 'traffics':
      return trafficsCommand(request, url, route, routing, limit);
    case 'packet-filter':
      return packetFilterCommand(request, url, route, routing, limit);
  }
}

export class SakuraAppRunGateway {
  readonly #origin: string;
  readonly #credential: string;
  readonly #bodyLimit: number;

  constructor(options: SakuraAppRunGatewayOptions) {
    this.#origin = simulatorOrigin(options.simulatorOrigin);
    this.#credential = simulatorCredential(options.simulatorCredential);
    this.#bodyLimit = bodyLimit(options.maxBodyBytes);
  }

  async translate(request: Request): Promise<SakuraAppRunGatewayCommand> {
    if (request.url.length > 4096) {
      return gatewayError('ValidationFailed', 'Sakura AppRun URL is too long');
    }
    const url = new URL(request.url);
    if (url.origin !== this.#origin) {
      return gatewayError(
        'UnauthorizedOperation',
        'Sakura AppRun endpoint is not simulator-owned'
      );
    }
    authorize(request, this.#credential);
    rejectCredentialQuery(url);
    const routing = routingContext(request);
    const route = appRunRoute(url);
    const command = await routeCommand(
      request,
      url,
      route,
      routing,
      this.#bodyLimit
    );
    return { worldId: routing.worldId, command };
  }
}
