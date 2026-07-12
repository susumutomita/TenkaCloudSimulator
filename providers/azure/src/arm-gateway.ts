import type { ExecuteCommandInput } from '@tenkacloud/simulator-core';
import { AZURE_CONTAINER_APP, AZURE_ROLE_ASSIGNMENT } from './provider';

export const AZURE_ARM_CONTAINER_API_VERSION = '2024-03-01';
export const AZURE_ARM_ROLE_API_VERSION = '2022-04-01';
export const AZURE_ARM_DEFAULT_BODY_LIMIT = 64 * 1024;
export const AZURE_ARM_WORLD_HEADER = 'x-tenkacloud-world-id';
export const AZURE_ARM_DEPLOYMENT_HEADER = 'x-tenkacloud-deployment-id';
export const AZURE_ARM_TARGET_HEADER = 'x-tenkacloud-target-id';

export type AzureArmGatewayErrorCode =
  | 'UnauthorizedOperation'
  | 'ValidationFailed'
  | 'QuotaExceeded'
  | 'UnsupportedCapability';

export class AzureArmGatewayError extends Error {
  readonly status: 400 | 401 | 413 | 422;

  constructor(
    readonly code: AzureArmGatewayErrorCode,
    message: string
  ) {
    super(message);
    this.name = 'AzureArmGatewayError';
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

export interface AzureArmGatewayOptions {
  readonly simulatorOrigin: string;
  readonly simulatorCredential: string;
  readonly maxBodyBytes?: number;
}

export interface AzureArmGatewayCommand {
  readonly worldId: string;
  readonly command: ExecuteCommandInput;
}

interface RoutingContext {
  readonly worldId: string;
  readonly deploymentId: string;
  readonly targetId: string;
}

interface ContainerRoute {
  readonly kind: 'container';
  readonly id: string;
}

interface RoleRoute {
  readonly kind: 'role';
  readonly id: string;
  readonly scopeId: string;
}

type ArmRoute = ContainerRoute | RoleRoute;

function gatewayError(code: AzureArmGatewayErrorCode, message: string): never {
  throw new AzureArmGatewayError(code, message);
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
  const limit = value ?? AZURE_ARM_DEFAULT_BODY_LIMIT;
  if (!Number.isInteger(limit) || limit < 1 || limit > 1024 * 1024) {
    return gatewayError(
      'ValidationFailed',
      'maxBodyBytes must be an integer from 1 through 1048576'
    );
  }
  return limit;
}

function authorize(request: Request, expectedCredential: string): void {
  const authorization = request.headers.get('authorization')?.trim();
  const match = authorization ? /^Bearer\s+(\S+)$/i.exec(authorization) : null;
  const credential = match?.[1];
  if (credential === expectedCredential) return;
  if (credential && !credential.startsWith('tcsim_')) {
    gatewayError(
      'UnauthorizedOperation',
      'real Azure bearer credentials are not accepted'
    );
  }
  gatewayError(
    'UnauthorizedOperation',
    credential
      ? 'simulator credential is invalid'
      : 'simulator-owned Bearer authorization is required'
  );
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
  const worldId = routingHeader(request.headers, AZURE_ARM_WORLD_HEADER, true);
  const deploymentId = routingHeader(
    request.headers,
    AZURE_ARM_DEPLOYMENT_HEADER,
    true
  );
  const targetId =
    routingHeader(request.headers, AZURE_ARM_TARGET_HEADER, false) ?? 'default';
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

const COMMON_SEGMENT = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const RESOURCE_GROUP_SEGMENT = /^[A-Za-z0-9][A-Za-z0-9._()-]{0,89}$/;
const CONTAINER_SEGMENT = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,30}[A-Za-z0-9])?$/;

function containerIdentity(match: RegExpExecArray): string {
  const subscription = pathSegment(
    match[1] ?? '',
    'subscription id',
    COMMON_SEGMENT
  );
  const resourceGroup = pathSegment(
    match[2] ?? '',
    'resource group name',
    RESOURCE_GROUP_SEGMENT
  );
  const name = pathSegment(
    match[3] ?? '',
    'Container App name',
    CONTAINER_SEGMENT
  );
  return `/subscriptions/${subscription}/resourceGroups/${resourceGroup}/providers/${AZURE_CONTAINER_APP}/${name}`;
}

function armRoute(url: URL): ArmRoute {
  const containerPattern =
    /^\/subscriptions\/([^/]+)\/resourceGroups\/([^/]+)\/providers\/Microsoft\.App\/containerApps\/([^/]+)$/i;
  const rolePattern =
    /^\/subscriptions\/([^/]+)\/resourceGroups\/([^/]+)\/providers\/Microsoft\.App\/containerApps\/([^/]+)\/providers\/Microsoft\.Authorization\/roleAssignments\/([^/]+)$/i;
  const role = rolePattern.exec(url.pathname);
  if (role) {
    const scopeId = containerIdentity(role);
    const name = pathSegment(
      role[4] ?? '',
      'role assignment name',
      COMMON_SEGMENT
    );
    return {
      kind: 'role',
      scopeId,
      id: `${scopeId}/providers/${AZURE_ROLE_ASSIGNMENT}/${name}`,
    };
  }
  const container = containerPattern.exec(url.pathname);
  if (container) {
    return { kind: 'container', id: containerIdentity(container) };
  }
  return gatewayError(
    'UnsupportedCapability',
    `Azure ARM path ${url.pathname} is not supported`
  );
}

function validateApiVersion(url: URL, route: ArmRoute): void {
  const expected =
    route.kind === 'container'
      ? AZURE_ARM_CONTAINER_API_VERSION
      : AZURE_ARM_ROLE_API_VERSION;
  const versions = url.searchParams.getAll('api-version');
  const extra = Array.from(url.searchParams.keys()).find(
    (key) => key !== 'api-version'
  );
  if (versions.length !== 1 || versions[0] !== expected || extra) {
    gatewayError('ValidationFailed', `api-version must be exactly ${expected}`);
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

async function jsonBody(
  request: Request,
  limit: number
): Promise<Record<string, unknown>> {
  const contentType = request.headers.get('content-type')?.toLowerCase();
  if (!contentType || !/^application\/json(?:\s*;|$)/.test(contentType)) {
    return gatewayError(
      'ValidationFailed',
      'content-type must be application/json'
    );
  }
  const bytes = await requestBytes(request, limit);
  if (bytes.byteLength === 0) {
    return gatewayError('ValidationFailed', 'JSON request body is required');
  }
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
  if (typeof value !== 'string' || !value.trim() || value.length > maximum) {
    return gatewayError('ValidationFailed', `${label} is invalid`);
  }
  return value;
}

function ingressPatch(value: unknown): Readonly<Record<string, unknown>> {
  if (value === undefined) return {};
  const configuration = record(value, 'properties.configuration');
  validateKeys(configuration, ['ingress'], 'properties.configuration');
  const ingress = record(configuration['ingress'], 'configuration.ingress');
  validateKeys(ingress, ['targetPort'], 'configuration.ingress');
  const targetPort = optionalInteger(
    ingress['targetPort'],
    'targetPort',
    1,
    65_535
  );
  return targetPort === undefined ? {} : { targetPort };
}

function containerImagePatch(
  value: unknown
): Readonly<Record<string, unknown>> {
  if (value === undefined) return {};
  if (!Array.isArray(value) || value.length !== 1) {
    return gatewayError(
      'ValidationFailed',
      'template.containers must contain exactly one container'
    );
  }
  const container = record(value[0], 'template container');
  validateKeys(container, ['name', 'image'], 'template container');
  optionalText(container['name'], 'container name', 128);
  const image = optionalText(container['image'], 'container image', 2048);
  return image === undefined ? {} : { image };
}

function scalePatch(value: unknown): Readonly<Record<string, unknown>> {
  if (value === undefined) return {};
  const scale = record(value, 'template.scale');
  validateKeys(scale, ['minReplicas', 'maxReplicas'], 'template.scale');
  const minReplicas = optionalInteger(
    scale['minReplicas'],
    'minReplicas',
    0,
    100
  );
  const maxReplicas = optionalInteger(
    scale['maxReplicas'],
    'maxReplicas',
    1,
    100
  );
  if (
    minReplicas !== undefined &&
    maxReplicas !== undefined &&
    minReplicas > maxReplicas
  ) {
    return gatewayError(
      'ValidationFailed',
      'minReplicas must not exceed maxReplicas'
    );
  }
  return {
    ...(minReplicas === undefined ? {} : { minReplicas }),
    ...(maxReplicas === undefined ? {} : { maxReplicas }),
  };
}

function templatePatch(value: unknown): Readonly<Record<string, unknown>> {
  if (value === undefined) return {};
  const template = record(value, 'properties.template');
  validateKeys(template, ['containers', 'scale'], 'properties.template');
  return {
    ...containerImagePatch(template['containers']),
    ...scalePatch(template['scale']),
  };
}

function containerPatch(
  body: Record<string, unknown>
): Readonly<Record<string, unknown>> {
  validateKeys(body, ['properties'], 'request body');
  const properties = record(body['properties'], 'request body.properties');
  validateKeys(
    properties,
    ['configuration', 'template'],
    'request body.properties'
  );
  const patch = {
    ...ingressPatch(properties['configuration']),
    ...templatePatch(properties['template']),
  };
  if (Object.keys(patch).length === 0) {
    return gatewayError('ValidationFailed', 'Container App patch is empty');
  }
  return patch;
}

function requiredText(value: unknown, label: string, maximum: number): string {
  const text = optionalText(value, label, maximum);
  if (text === undefined) {
    return gatewayError('ValidationFailed', `${label} is required`);
  }
  return text;
}

function roleInput(
  body: Record<string, unknown>,
  route: RoleRoute
): Readonly<Record<string, unknown>> {
  validateKeys(body, ['properties'], 'request body');
  const properties = record(body['properties'], 'request body.properties');
  validateKeys(
    properties,
    ['roleDefinitionId', 'principalId'],
    'role assignment properties'
  );
  return {
    id: route.id,
    scopeId: route.scopeId,
    roleDefinitionId: requiredText(
      properties['roleDefinitionId'],
      'roleDefinitionId',
      2048
    ),
    principalId: requiredText(properties['principalId'], 'principalId', 320),
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
    provider: 'azure',
    engine: 'bicep',
    service,
    operation,
    resourceType,
    input,
  };
}

async function containerCommand(
  request: Request,
  route: ContainerRoute,
  routing: RoutingContext,
  limit: number
): Promise<ExecuteCommandInput> {
  if (request.method === 'GET') {
    await assertNoBody(request, limit);
    return executeCommand(
      routing,
      'containerapps',
      'GetContainerApp',
      AZURE_CONTAINER_APP,
      { id: route.id }
    );
  }
  if (request.method === 'PATCH') {
    const patch = containerPatch(await jsonBody(request, limit));
    return executeCommand(
      routing,
      'containerapps',
      'UpdateContainerApp',
      AZURE_CONTAINER_APP,
      { id: route.id, patch }
    );
  }
  if (request.method === 'DELETE') {
    await assertNoBody(request, limit);
    return executeCommand(
      routing,
      'containerapps',
      'DeleteContainerApp',
      AZURE_CONTAINER_APP,
      { id: route.id }
    );
  }
  return gatewayError(
    'UnsupportedCapability',
    `Azure Container Apps method ${request.method} is not supported`
  );
}

async function roleCommand(
  request: Request,
  route: RoleRoute,
  routing: RoutingContext,
  limit: number
): Promise<ExecuteCommandInput> {
  if (request.method === 'GET') {
    await assertNoBody(request, limit);
    return executeCommand(
      routing,
      'authorization',
      'GetRoleAssignment',
      AZURE_ROLE_ASSIGNMENT,
      { id: route.id }
    );
  }
  if (request.method === 'PUT') {
    const input = roleInput(await jsonBody(request, limit), route);
    return executeCommand(
      routing,
      'authorization',
      'SetRoleAssignment',
      AZURE_ROLE_ASSIGNMENT,
      input
    );
  }
  return gatewayError(
    'UnsupportedCapability',
    `Azure role assignment method ${request.method} is not supported`
  );
}

export class AzureArmGateway {
  readonly #origin: string;
  readonly #credential: string;
  readonly #bodyLimit: number;

  constructor(options: AzureArmGatewayOptions) {
    this.#origin = simulatorOrigin(options.simulatorOrigin);
    this.#credential = simulatorCredential(options.simulatorCredential);
    this.#bodyLimit = bodyLimit(options.maxBodyBytes);
  }

  async translate(request: Request): Promise<AzureArmGatewayCommand> {
    if (request.url.length > 4096) {
      return gatewayError('ValidationFailed', 'Azure ARM URL is too long');
    }
    const url = new URL(request.url);
    if (url.origin !== this.#origin) {
      return gatewayError(
        'UnauthorizedOperation',
        'Azure ARM endpoint is not simulator-owned'
      );
    }
    authorize(request, this.#credential);
    const routing = routingContext(request);
    const route = armRoute(url);
    validateApiVersion(url, route);
    const command =
      route.kind === 'container'
        ? await containerCommand(request, route, routing, this.#bodyLimit)
        : await roleCommand(request, route, routing, this.#bodyLimit);
    return { worldId: routing.worldId, command };
  }
}
