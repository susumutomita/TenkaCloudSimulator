import { randomUUID } from 'node:crypto';
import {
  CoreError,
  type ExecuteCommandInput,
  type ProviderRegistry,
  type SimulationCore,
} from '@tenkacloud/simulator-core';
import type { Context } from 'hono';
import { RequestValidationError } from './errors.js';

export const MAX_DATA_PLANE_BODY_BYTES = 64 * 1024;
const MAX_DATA_PLANE_HEADERS = 64;
const MAX_DATA_PLANE_PATH_LENGTH = 2048;
const HOP_BY_HOP_HEADERS = new Set([
  'connection',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'te',
  'trailer',
  'transfer-encoding',
  'upgrade',
]);
const INTERNAL_REQUEST_HEADERS = new Set([
  'authorization',
  'connection',
  'content-length',
  'host',
  'idempotency-key',
  'x-request-id',
  'x-tenkacloud-simulator-protocol',
]);
const HTTP_TOKEN = /^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$/;

interface DataPlaneOptions {
  readonly core: SimulationCore;
  readonly registry: ProviderRegistry;
}

export function dataPlaneIdentifier(
  value: string | undefined,
  label: string
): string {
  if (!value || !/^[a-z][a-z0-9-]{0,63}$/.test(value)) {
    throw new RequestValidationError(`${label} is invalid`);
  }
  return value;
}

export function dataPlaneMethod(value: string): string {
  const method = value.toUpperCase();
  if (!/^[A-Z][A-Z0-9-]{0,31}$/.test(method)) {
    throw new RequestValidationError('data-plane method is invalid');
  }
  return method;
}

function requestPath(c: Context): string {
  const url = new URL(c.req.url);
  const tail = /^\/v1\/worlds\/[^/]+\/data-plane\/[^/]+\/[^/]+(\/.*)$/.exec(
    url.pathname
  )?.[1];
  if (!tail) throw new RequestValidationError('data-plane path is invalid');
  const path = `${tail}${url.search}`;
  if (
    path.startsWith('//') ||
    path.length > MAX_DATA_PLANE_PATH_LENGTH ||
    /[\s#]/.test(path)
  ) {
    throw new RequestValidationError('data-plane path is invalid');
  }
  return path;
}

export function dataPlaneHeaders(
  rawHeaders: Headers
): Readonly<Record<string, string>> {
  const headers: Record<string, string> = {};
  for (const [rawName, value] of rawHeaders) {
    const name = rawName.toLowerCase();
    if (INTERNAL_REQUEST_HEADERS.has(name)) continue;
    if (HOP_BY_HOP_HEADERS.has(name)) {
      throw new RequestValidationError(
        `data-plane hop-by-hop header ${name} is forbidden`
      );
    }
    if (
      !HTTP_TOKEN.test(name) ||
      value.length > 8192 ||
      value.includes('\r') ||
      value.includes('\n')
    ) {
      throw new RequestValidationError(
        `data-plane request header ${name} is invalid`
      );
    }
    headers[name] = value;
  }
  if (Object.keys(headers).length > MAX_DATA_PLANE_HEADERS) {
    throw new RequestValidationError('data-plane request has too many headers');
  }
  return headers;
}

async function requestBody(c: Context): Promise<string> {
  const reader = c.req.raw.body?.getReader();
  if (!reader) return '';
  const chunks: Uint8Array[] = [];
  let total = 0;
  while (true) {
    const next = await reader.read();
    if (next.done) break;
    total += next.value.byteLength;
    if (total > MAX_DATA_PLANE_BODY_BYTES) {
      await reader.cancel();
      throw new CoreError(
        'QuotaExceeded',
        'data-plane request body is too large'
      );
    }
    chunks.push(next.value);
  }
  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  try {
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  } catch {
    throw new RequestValidationError('data-plane request body must be UTF-8');
  }
}

function dataPlaneEngine(registry: ProviderRegistry, provider: string): string {
  const engines = new Set(
    registry
      .capabilities()
      .filter(
        (capability) =>
          capability.provider === provider &&
          capability.service === 'http' &&
          capability.resourceType === 'HTTP::Endpoint' &&
          capability.operation === 'Request'
      )
      .map((capability) => capability.engine)
  );
  const [engine] = engines;
  if (engines.size !== 1 || engine === undefined) {
    throw new CoreError(
      'UnsupportedCapability',
      'provider does not expose one unambiguous HTTP data-plane capability'
    );
  }
  return engine;
}

function responseStatus(value: unknown): number {
  if (
    typeof value !== 'number' ||
    !Number.isSafeInteger(value) ||
    value < 200 ||
    value > 599
  ) {
    throw new CoreError(
      'ValidationFailed',
      'provider data-plane StatusCode is invalid'
    );
  }
  return value;
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function responseHeaders(value: unknown): Headers {
  if (!isRecord(value) || Object.keys(value).length > MAX_DATA_PLANE_HEADERS) {
    throw new CoreError(
      'ValidationFailed',
      'provider data-plane Headers are invalid'
    );
  }
  const headers = new Headers();
  for (const [rawName, rawValue] of Object.entries(value)) {
    const name = rawName.toLowerCase();
    if (
      HOP_BY_HOP_HEADERS.has(name) ||
      name === 'content-length' ||
      !HTTP_TOKEN.test(name) ||
      typeof rawValue !== 'string' ||
      rawValue.length > 8192 ||
      rawValue.includes('\r') ||
      rawValue.includes('\n')
    ) {
      throw new CoreError(
        'ValidationFailed',
        `provider data-plane response header ${name} is invalid`
      );
    }
    headers.set(name, rawValue);
  }
  return headers;
}

function rawResponse(
  method: string,
  result: Readonly<Record<string, unknown>>
): Response {
  const { Body: body, Headers: headerValues, StatusCode } = result;
  const status = responseStatus(StatusCode);
  const headers = responseHeaders(headerValues);
  if (
    typeof body !== 'string' ||
    new TextEncoder().encode(body).byteLength > MAX_DATA_PLANE_BODY_BYTES
  ) {
    throw new CoreError(
      'ValidationFailed',
      'provider data-plane Body is invalid'
    );
  }
  const statusForbidsBody = status === 204 || status === 205 || status === 304;
  if (statusForbidsBody && body.length > 0) {
    throw new CoreError(
      'ValidationFailed',
      `provider data-plane status ${status} forbids a response body`
    );
  }
  return new Response(method === 'HEAD' || statusForbidsBody ? null : body, {
    status,
    headers,
  });
}

export function isDataPlanePath(path: string): boolean {
  return /^\/v1\/worlds\/[^/]+\/data-plane\/[^/]+\/[^/]+\//.test(path);
}

export async function executeDataPlaneRequest(
  c: Context,
  options: DataPlaneOptions
): Promise<Response> {
  const worldId = c.req.param('worldId');
  if (!worldId) throw new RequestValidationError('worldId is invalid');
  const provider = dataPlaneIdentifier(c.req.param('provider'), 'provider');
  const targetId = dataPlaneIdentifier(c.req.param('targetId'), 'targetId');
  const world = options.core.world(worldId);
  const deployment = options.core.deployment(worldId, world.deploymentId);
  if (deployment.status !== 'ready') {
    throw new CoreError(
      'Conflict',
      'deployment is not ready for data-plane I/O'
    );
  }
  if (!Object.hasOwn(deployment.outputs, targetId)) {
    throw new CoreError('NotFound', 'deployment target does not exist');
  }
  const method = dataPlaneMethod(c.req.method);
  const path = requestPath(c);
  const headers = dataPlaneHeaders(c.req.raw.headers);
  const body = await requestBody(c);
  const command: ExecuteCommandInput = {
    deploymentId: deployment.deploymentId,
    targetId,
    provider,
    engine: dataPlaneEngine(options.registry, provider),
    service: 'http',
    operation: 'Request',
    resourceType: 'HTTP::Endpoint',
    input: { Method: method, Path: path, Headers: headers, Body: body },
  };
  const explicitKey = c.req.header('idempotency-key')?.trim();
  const key = explicitKey || `data-plane:${randomUUID()}`;
  const result = await options.core.executeCommandAsync(worldId, command, key);
  return rawResponse(method, result);
}
