import type { ProviderWorldView, ResourceRecord } from './domain';
import { CoreError } from './errors';

export const HTTP_ENDPOINT_RESOURCE = 'HTTP::Endpoint';
export const MAX_PROVIDER_HTTP_BODY_BYTES = 64 * 1024;

const MAX_HEADERS = 64;
const MAX_PATH_LENGTH = 2_048;
const HTTP_TOKEN = /^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$/;
const REQUEST_FIELDS = new Set(['Method', 'Path', 'Headers', 'Body']);
const FORBIDDEN_REQUEST_HEADERS = new Set([
  'authorization',
  'connection',
  'content-length',
  'host',
  'idempotency-key',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'te',
  'trailer',
  'transfer-encoding',
  'upgrade',
  'x-request-id',
  'x-tenkacloud-simulator-protocol',
]);

export interface ProviderHttpRequest {
  readonly method: string;
  readonly path: string;
  readonly headers: Readonly<Record<string, string>>;
  readonly body: string;
}

export interface ProviderHttpRepresentation {
  readonly statusCode: unknown;
  readonly body: unknown;
  readonly contentType: unknown;
}

function recordValue(
  value: unknown,
  label: string
): Readonly<Record<string, unknown>> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new CoreError('ValidationFailed', `${label} must be an object`);
  }
  return value as Readonly<Record<string, unknown>>;
}

function containsControlCharacter(value: string, allowTab: boolean): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code === 127 || (code < 32 && !(allowTab && code === 9))) return true;
  }
  return false;
}

function requestHeaders(value: unknown): Readonly<Record<string, string>> {
  const headers = recordValue(value, 'HTTP Headers');
  if (Object.keys(headers).length > MAX_HEADERS) {
    throw new CoreError('QuotaExceeded', 'HTTP request has too many headers');
  }
  const normalized = new Map<string, string>();
  for (const [rawName, rawValue] of Object.entries(headers)) {
    const name = rawName.toLowerCase();
    if (
      !HTTP_TOKEN.test(rawName) ||
      FORBIDDEN_REQUEST_HEADERS.has(name) ||
      typeof rawValue !== 'string' ||
      rawValue.length > 8_192 ||
      containsControlCharacter(rawValue, true)
    ) {
      throw new CoreError(
        'ValidationFailed',
        `HTTP request header ${rawName} is invalid`
      );
    }
    if (normalized.has(name)) {
      throw new CoreError(
        'ValidationFailed',
        `HTTP request header ${rawName} is duplicated`
      );
    }
    normalized.set(name, rawValue);
  }
  return Object.fromEntries(normalized);
}

export function providerHttpRequest(
  input: Readonly<Record<string, unknown>>
): ProviderHttpRequest {
  const fields = Object.keys(input);
  if (
    fields.length !== REQUEST_FIELDS.size ||
    fields.some((field) => !REQUEST_FIELDS.has(field))
  ) {
    throw new CoreError(
      'ValidationFailed',
      'HTTP request must contain exactly Method, Path, Headers, and Body'
    );
  }
  const rawMethod = input['Method'];
  const method = typeof rawMethod === 'string' ? rawMethod.toUpperCase() : '';
  if (!/^[A-Z][A-Z0-9-]{0,31}$/.test(method)) {
    throw new CoreError('ValidationFailed', 'HTTP Method is invalid');
  }
  const path = input['Path'];
  if (
    typeof path !== 'string' ||
    !path.startsWith('/') ||
    path.startsWith('//') ||
    path.length > MAX_PATH_LENGTH ||
    /[\s#]/.test(path) ||
    containsControlCharacter(path, false)
  ) {
    throw new CoreError(
      'ValidationFailed',
      'HTTP Path must be a bounded origin-relative path'
    );
  }
  const body = input['Body'];
  if (typeof body !== 'string') {
    throw new CoreError('ValidationFailed', 'HTTP Body must be a string');
  }
  if (
    new TextEncoder().encode(body).byteLength > MAX_PROVIDER_HTTP_BODY_BYTES
  ) {
    throw new CoreError('QuotaExceeded', 'HTTP request body is too long');
  }
  return {
    method,
    path,
    headers: requestHeaders(input['Headers']),
    body,
  };
}

function validContentType(value: unknown): value is string {
  return (
    typeof value === 'string' &&
    value.length > 0 &&
    value.length <= 128 &&
    !value.includes('\r') &&
    !value.includes('\n')
  );
}

export function providerHttpResponse(
  request: ProviderHttpRequest,
  representation: ProviderHttpRepresentation
): Readonly<Record<string, unknown>> {
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    return {
      StatusCode: 405,
      Headers: {
        allow: 'GET, HEAD',
        'content-type': 'text/plain; charset=utf-8',
      },
      Body: 'Method Not Allowed',
    };
  }
  const statusCode = representation.statusCode;
  if (
    typeof statusCode !== 'number' ||
    !Number.isSafeInteger(statusCode) ||
    statusCode < 200 ||
    statusCode > 599
  ) {
    throw new CoreError(
      'ValidationFailed',
      'HTTP endpoint response status is invalid'
    );
  }
  if (
    typeof representation.body !== 'string' ||
    new TextEncoder().encode(representation.body).byteLength >
      MAX_PROVIDER_HTTP_BODY_BYTES
  ) {
    throw new CoreError(
      'ValidationFailed',
      'HTTP endpoint response body is invalid'
    );
  }
  if (!validContentType(representation.contentType)) {
    throw new CoreError(
      'ValidationFailed',
      'HTTP endpoint response content type is invalid'
    );
  }
  const statusForbidsBody =
    statusCode === 204 || statusCode === 205 || statusCode === 304;
  if (statusForbidsBody && representation.body.length > 0) {
    throw new CoreError(
      'ValidationFailed',
      `HTTP endpoint status ${statusCode} forbids a response body`
    );
  }
  return {
    StatusCode: statusCode,
    Headers: { 'content-type': representation.contentType },
    Body:
      request.method === 'HEAD' || statusForbidsBody ? '' : representation.body,
  };
}

export function singleReadyDeploymentResource(
  world: ProviderWorldView,
  deploymentId: string,
  provider: string,
  resourceType: string,
  label: string
): ResourceRecord {
  const resources = world.resources.filter(
    (resource) =>
      resource.deploymentId === deploymentId &&
      resource.provider === provider &&
      resource.resourceType === resourceType &&
      resource.status !== 'deleted'
  );
  if (resources.length === 0) {
    throw new CoreError('NotFound', `${label} endpoint does not exist`);
  }
  if (resources.length !== 1) {
    throw new CoreError('Conflict', `${label} endpoint resource is ambiguous`);
  }
  const resource = resources[0];
  if (resource?.status !== 'ready') {
    throw new CoreError('Conflict', `${label} endpoint is not ready`);
  }
  return resource;
}
