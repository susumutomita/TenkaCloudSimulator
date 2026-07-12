import type { ExecuteCommandInput } from '@tenkacloud/simulator-core';
import {
  CLOUD_RUN_IAM_MEMBER,
  CLOUD_RUN_SERVICE,
  HTTP_ENDPOINT,
} from './provider';

export const GCP_REST_DEFAULT_BODY_LIMIT = 64 * 1024;
export const GCP_REST_WORLD_HEADER = 'x-tenkacloud-world-id';
export const GCP_REST_DEPLOYMENT_HEADER = 'x-tenkacloud-deployment-id';
export const GCP_REST_TARGET_HEADER = 'x-tenkacloud-target-id';

export type GcpRestGatewayErrorCode =
  | 'UnauthorizedOperation'
  | 'ValidationFailed'
  | 'QuotaExceeded'
  | 'UnsupportedCapability';

export class GcpRestGatewayError extends Error {
  readonly status: 400 | 401 | 413 | 422;

  constructor(
    readonly code: GcpRestGatewayErrorCode,
    message: string
  ) {
    super(message);
    this.name = 'GcpRestGatewayError';
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

export interface GcpRestGatewayOptions {
  readonly simulatorOrigin: string;
  readonly simulatorCredential: string;
  readonly maxBodyBytes?: number;
}

export interface GcpRestGatewayCommand {
  readonly worldId: string;
  readonly command: ExecuteCommandInput;
}

interface RoutingContext {
  readonly worldId: string;
  readonly deploymentId: string;
  readonly targetId: string;
}

type RouteAction = 'service' | 'getIamPolicy' | 'setIamPolicy' | 'probe';

interface CloudRunRoute {
  readonly action: RouteAction;
  readonly project: string;
  readonly location: string;
  readonly serviceId: string;
  readonly fullName: string;
}

function gatewayError(code: GcpRestGatewayErrorCode, message: string): never {
  throw new GcpRestGatewayError(code, message);
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
  if (!/^tcsim_[A-Za-z0-9_-]{16,128}$/.test(value)) {
    return gatewayError(
      'ValidationFailed',
      'simulatorCredential must be a simulator-owned token'
    );
  }
  return value;
}

function bodyLimit(value: number | undefined): number {
  const limit = value ?? GCP_REST_DEFAULT_BODY_LIMIT;
  if (!Number.isInteger(limit) || limit < 1 || limit > 1024 * 1024) {
    return gatewayError(
      'ValidationFailed',
      'maxBodyBytes must be an integer from 1 through 1048576'
    );
  }
  return limit;
}

function hasRealCredentialHeader(headers: Headers): boolean {
  return [
    'x-goog-api-key',
    'x-goog-credential',
    'x-goog-iam-authorization-token',
  ].some((name) => Boolean(headers.get(name)?.trim()));
}

function authorize(request: Request, expectedCredential: string): void {
  if (hasRealCredentialHeader(request.headers)) {
    gatewayError(
      'UnauthorizedOperation',
      'real Google credentials are not accepted'
    );
  }
  const authorization = request.headers.get('authorization')?.trim();
  const match = authorization ? /^Bearer\s+(\S+)$/i.exec(authorization) : null;
  const credential = match?.[1];
  if (credential === expectedCredential) return;
  if (credential && !credential.startsWith('tcsim_')) {
    gatewayError(
      'UnauthorizedOperation',
      'real Google OAuth credentials are not accepted'
    );
  }
  gatewayError(
    'UnauthorizedOperation',
    credential
      ? 'simulator credential is invalid'
      : 'simulator-owned Bearer authorization is required'
  );
}

function rejectCredentialQuery(url: URL): void {
  const credentialKey = ['key', 'access_token', 'oauth_token'].find((key) =>
    url.searchParams.has(key)
  );
  if (credentialKey) {
    gatewayError(
      'UnauthorizedOperation',
      'real Google query credentials are not accepted'
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
  const worldId = routingHeader(request.headers, GCP_REST_WORLD_HEADER, true);
  const deploymentId = routingHeader(
    request.headers,
    GCP_REST_DEPLOYMENT_HEADER,
    true
  );
  const targetId =
    routingHeader(request.headers, GCP_REST_TARGET_HEADER, false) ?? 'default';
  if (!worldId || !deploymentId) {
    return gatewayError('ValidationFailed', 'routing headers are required');
  }
  return { worldId, deploymentId, targetId };
}

function pathSegment(encoded: string, label: string, pattern: RegExp): string {
  let decoded: string;
  try {
    decoded = decodeURIComponent(encoded);
  } catch {
    return gatewayError('ValidationFailed', `${label} is not URL encoded`);
  }
  if (!pattern.test(decoded)) {
    return gatewayError('ValidationFailed', `${label} is invalid`);
  }
  return decoded;
}

const PROJECT_SEGMENT = /^(?:[a-z][a-z0-9-]{4,28}[a-z0-9]|[0-9]{6,20})$/;
const LOCATION_SEGMENT = /^[a-z][a-z0-9-]{0,62}$/;
const SERVICE_SEGMENT = /^[a-z](?:[a-z0-9-]{0,47}[a-z0-9])?$/;

function cloudRunRoute(url: URL): CloudRunRoute {
  const match =
    /^\/v2\/projects\/([^/]+)\/locations\/([^/]+)\/services\/([^/:]+)(?::(getIamPolicy|setIamPolicy|probe))?$/.exec(
      url.pathname
    );
  if (!match) {
    return gatewayError(
      'UnsupportedCapability',
      `Google Cloud REST path ${url.pathname} is not supported`
    );
  }
  const project = pathSegment(match[1] ?? '', 'project', PROJECT_SEGMENT);
  const location = pathSegment(match[2] ?? '', 'location', LOCATION_SEGMENT);
  const serviceId = pathSegment(
    match[3] ?? '',
    'Cloud Run service',
    SERVICE_SEGMENT
  );
  const action = (match[4] ?? 'service') as RouteAction;
  return {
    action,
    project,
    location,
    serviceId,
    fullName: `projects/${project}/locations/${location}/services/${serviceId}`,
  };
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

function validateContentType(request: Request): void {
  const contentType = request.headers.get('content-type')?.toLowerCase();
  if (!contentType || !/^application\/json(?:\s*;|$)/.test(contentType)) {
    gatewayError('ValidationFailed', 'content-type must be application/json');
  }
}

function containsServiceAccountCredential(text: string): boolean {
  return (
    /"type"\s*:\s*"service_account"/.test(text) ||
    /"(?:private_key|private_key_id|client_email)"\s*:/.test(text)
  );
}

async function jsonBody(
  request: Request,
  limit: number,
  required: boolean
): Promise<Record<string, unknown>> {
  const bytes = await requestBytes(request, limit);
  if (bytes.byteLength === 0 && !required) return {};
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
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return gatewayError('ValidationFailed', 'request body must be valid JSON');
  }
  if (containsServiceAccountCredential(text)) {
    return gatewayError(
      'UnauthorizedOperation',
      'Google service-account credentials are not accepted'
    );
  }
  return record(parsed, 'request body');
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

function optionalText(
  value: unknown,
  label: string,
  maximum: number
): string | undefined {
  if (value === undefined) return undefined;
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

function containerPatch(value: unknown): Readonly<Record<string, unknown>> {
  if (value === undefined) return {};
  if (!Array.isArray(value) || value.length !== 1) {
    return gatewayError(
      'ValidationFailed',
      'template.containers must contain exactly one container'
    );
  }
  const container = record(value[0], 'template container');
  validateKeys(container, ['name', 'image'], 'template container');
  optionalText(container['name'], 'container name', 63);
  const image = optionalText(container['image'], 'container image', 2048);
  return image === undefined ? {} : { image };
}

function scalingPatch(value: unknown): Readonly<Record<string, unknown>> {
  if (value === undefined) return {};
  const scaling = record(value, 'template.scaling');
  validateKeys(
    scaling,
    ['minInstanceCount', 'maxInstanceCount'],
    'template.scaling'
  );
  const minInstanceCount = optionalInteger(
    scaling['minInstanceCount'],
    'minInstanceCount',
    0,
    100
  );
  const maxInstanceCount = optionalInteger(
    scaling['maxInstanceCount'],
    'maxInstanceCount',
    1,
    100
  );
  if (
    minInstanceCount !== undefined &&
    maxInstanceCount !== undefined &&
    minInstanceCount > maxInstanceCount
  ) {
    return gatewayError(
      'ValidationFailed',
      'minInstanceCount must not exceed maxInstanceCount'
    );
  }
  return {
    ...(minInstanceCount === undefined ? {} : { minInstanceCount }),
    ...(maxInstanceCount === undefined ? {} : { maxInstanceCount }),
  };
}

function servicePatch(
  body: Readonly<Record<string, unknown>>,
  route: CloudRunRoute
): Readonly<Record<string, unknown>> {
  validateKeys(body, ['name', 'template'], 'request body');
  const name = optionalText(body['name'], 'service name', 512);
  if (name !== undefined && name !== route.fullName) {
    return gatewayError(
      'ValidationFailed',
      'service name must match the request path'
    );
  }
  const template = record(body['template'], 'request body.template');
  validateKeys(template, ['containers', 'scaling'], 'request body.template');
  const patch = {
    ...containerPatch(template['containers']),
    ...scalingPatch(template['scaling']),
  };
  if (Object.keys(patch).length === 0) {
    return gatewayError('ValidationFailed', 'Cloud Run patch is empty');
  }
  return patch;
}

const PATCH_MASKS: Readonly<Record<string, string>> = {
  image: 'template.containers',
  minInstanceCount: 'template.scaling.minInstanceCount',
  maxInstanceCount: 'template.scaling.maxInstanceCount',
};

function validateUpdateMask(
  url: URL,
  patch: Readonly<Record<string, unknown>>
): void {
  const masks = url.searchParams.getAll('updateMask');
  const extra = Array.from(url.searchParams.keys()).find(
    (key) => key !== 'updateMask'
  );
  if (masks.length !== 1 || extra) {
    gatewayError('ValidationFailed', 'updateMask is required exactly once');
  }
  const fields = (masks[0] ?? '').split(',').filter(Boolean);
  if (fields.length === 0 || new Set(fields).size !== fields.length) {
    gatewayError('ValidationFailed', 'updateMask is invalid');
  }
  const unsupported = fields.find(
    (field) => !Object.values(PATCH_MASKS).includes(field)
  );
  if (unsupported) {
    gatewayError(
      'UnsupportedCapability',
      `updateMask field ${unsupported} is not supported`
    );
  }
  for (const [key, mask] of Object.entries(PATCH_MASKS)) {
    if (key in patch !== fields.includes(mask)) {
      gatewayError('ValidationFailed', `updateMask does not match ${key}`);
    }
  }
}

function requiredText(value: unknown, label: string, maximum: number): string {
  const text = optionalText(value, label, maximum);
  if (text === undefined) {
    return gatewayError('ValidationFailed', `${label} is required`);
  }
  return text;
}

function iamInput(
  body: Readonly<Record<string, unknown>>,
  route: CloudRunRoute
): Readonly<Record<string, unknown>> {
  validateKeys(body, ['policy'], 'request body');
  const policy = record(body['policy'], 'request body.policy');
  validateKeys(policy, ['bindings'], 'policy');
  const bindings = policy['bindings'];
  if (!Array.isArray(bindings) || bindings.length !== 1) {
    return gatewayError(
      'ValidationFailed',
      'policy.bindings must contain exactly one binding'
    );
  }
  const binding = record(bindings[0], 'policy binding');
  validateKeys(binding, ['role', 'members'], 'policy binding');
  const members = binding['members'];
  if (!Array.isArray(members) || members.length !== 1) {
    return gatewayError(
      'ValidationFailed',
      'policy binding members must contain exactly one member'
    );
  }
  return {
    serviceId: route.serviceId,
    role: requiredText(binding['role'], 'IAM role', 256),
    member: requiredText(members[0], 'IAM member', 1024),
  };
}

function executeCommand(
  routing: RoutingContext,
  service: string,
  operation: string,
  resourceType: string,
  input: Readonly<Record<string, unknown>>
): ExecuteCommandInput {
  return {
    deploymentId: routing.deploymentId,
    targetId: routing.targetId,
    provider: 'gcp',
    engine: 'infra-manager',
    service,
    operation,
    resourceType,
    input,
  };
}

async function serviceCommand(
  request: Request,
  url: URL,
  route: CloudRunRoute,
  routing: RoutingContext,
  limit: number
): Promise<ExecuteCommandInput> {
  if (request.method === 'GET') {
    ensureNoQuery(url);
    await assertNoBody(request, limit);
    return executeCommand(routing, 'run', 'GetService', CLOUD_RUN_SERVICE, {
      id: route.serviceId,
    });
  }
  if (request.method === 'PATCH') {
    const patch = servicePatch(await jsonBody(request, limit, true), route);
    validateUpdateMask(url, patch);
    return executeCommand(routing, 'run', 'UpdateService', CLOUD_RUN_SERVICE, {
      id: route.serviceId,
      patch,
    });
  }
  if (request.method === 'DELETE') {
    ensureNoQuery(url);
    await assertNoBody(request, limit);
    return executeCommand(routing, 'run', 'DeleteService', CLOUD_RUN_SERVICE, {
      id: route.serviceId,
    });
  }
  return gatewayError(
    'UnsupportedCapability',
    `Cloud Run method ${request.method} is not supported`
  );
}

async function iamGetCommand(
  request: Request,
  url: URL,
  route: CloudRunRoute,
  routing: RoutingContext,
  limit: number
): Promise<ExecuteCommandInput> {
  if (request.method !== 'POST') {
    return gatewayError(
      'UnsupportedCapability',
      `getIamPolicy method ${request.method} is not supported`
    );
  }
  ensureNoQuery(url);
  const body = await jsonBody(request, limit, false);
  validateKeys(body, [], 'getIamPolicy body');
  return executeCommand(routing, 'run', 'GetIamPolicy', CLOUD_RUN_IAM_MEMBER, {
    serviceId: route.serviceId,
  });
}

async function iamSetCommand(
  request: Request,
  url: URL,
  route: CloudRunRoute,
  routing: RoutingContext,
  limit: number
): Promise<ExecuteCommandInput> {
  if (request.method !== 'POST') {
    return gatewayError(
      'UnsupportedCapability',
      `setIamPolicy method ${request.method} is not supported`
    );
  }
  ensureNoQuery(url);
  const input = iamInput(await jsonBody(request, limit, true), route);
  return executeCommand(
    routing,
    'run',
    'SetIamPolicy',
    CLOUD_RUN_IAM_MEMBER,
    input
  );
}

async function probeCommand(
  request: Request,
  url: URL,
  route: CloudRunRoute,
  routing: RoutingContext,
  limit: number
): Promise<ExecuteCommandInput> {
  if (request.method !== 'GET') {
    return gatewayError(
      'UnsupportedCapability',
      `Cloud Run probe method ${request.method} is not supported`
    );
  }
  ensureNoQuery(url);
  await assertNoBody(request, limit);
  return executeCommand(routing, 'http', 'Probe', HTTP_ENDPOINT, {
    id: route.serviceId,
  });
}

async function routeCommand(
  request: Request,
  url: URL,
  route: CloudRunRoute,
  routing: RoutingContext,
  limit: number
): Promise<ExecuteCommandInput> {
  switch (route.action) {
    case 'service':
      return serviceCommand(request, url, route, routing, limit);
    case 'getIamPolicy':
      return iamGetCommand(request, url, route, routing, limit);
    case 'setIamPolicy':
      return iamSetCommand(request, url, route, routing, limit);
    case 'probe':
      return probeCommand(request, url, route, routing, limit);
  }
}

export class GcpRestGateway {
  readonly #origin: string;
  readonly #credential: string;
  readonly #bodyLimit: number;

  constructor(options: GcpRestGatewayOptions) {
    this.#origin = simulatorOrigin(options.simulatorOrigin);
    this.#credential = simulatorCredential(options.simulatorCredential);
    this.#bodyLimit = bodyLimit(options.maxBodyBytes);
  }

  async translate(request: Request): Promise<GcpRestGatewayCommand> {
    if (request.url.length > 4096) {
      return gatewayError(
        'ValidationFailed',
        'Google Cloud REST URL is too long'
      );
    }
    const url = new URL(request.url);
    if (url.origin !== this.#origin) {
      return gatewayError(
        'UnauthorizedOperation',
        'Google Cloud REST endpoint is not simulator-owned'
      );
    }
    authorize(request, this.#credential);
    rejectCredentialQuery(url);
    const routing = routingContext(request);
    const route = cloudRunRoute(url);
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
