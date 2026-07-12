import {
  CoreError,
  type ProviderCommandInput,
  type ProviderCommandResult,
} from '@tenkacloud/simulator-core';
import { result } from './state';
import { objectValue, optionalString, stringValue } from './value';

const MAX_BODY_BYTES = 64 * 1024;
const MAX_POLL_REQUESTS = 32;
const DEFAULT_TIMEOUT_MILLISECONDS = 8_000;
const LOOPBACK_HOSTS = new Set(['127.0.0.1', '[::1]']);
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

export interface HttpProbeOptions {
  readonly timeoutMilliseconds?: number;
}

interface ProbeRequest {
  readonly url: string;
  readonly method: string;
  readonly headers: Readonly<Record<string, string>>;
  readonly body?: string;
}

function probeUrl(value: unknown): string {
  const text = stringValue(value, 'Url');
  if (text.length > 2048) {
    throw new CoreError('ValidationFailed', 'HTTP probe URL is too long');
  }
  let url: URL;
  try {
    url = new URL(text);
  } catch {
    throw new CoreError('ValidationFailed', 'HTTP probe URL must be a URL');
  }
  if (
    url.protocol !== 'http:' ||
    !LOOPBACK_HOSTS.has(url.hostname) ||
    url.username ||
    url.password ||
    url.hash
  ) {
    throw new CoreError(
      'ValidationFailed',
      'HTTP probe URL must be credential-free numeric loopback HTTP without a fragment'
    );
  }
  return url.toString();
}

function probeHeaders(value: unknown): Readonly<Record<string, string>> {
  const headers = objectValue(value ?? {}, 'Headers');
  if (Object.keys(headers).length > 64) {
    throw new CoreError('QuotaExceeded', 'HTTP probe has too many headers');
  }
  return Object.fromEntries(
    Object.entries(headers).map(([name, entry]) => {
      const normalized = name.toLowerCase();
      if (
        !/^[!#$%&'*+.^_`|~0-9a-z-]{1,128}$/.test(normalized) ||
        HOP_BY_HOP_HEADERS.has(normalized) ||
        typeof entry !== 'string' ||
        entry.length > 8192 ||
        entry.includes('\r') ||
        entry.includes('\n')
      ) {
        throw new CoreError(
          'ValidationFailed',
          `HTTP probe header ${name} is invalid`
        );
      }
      return [normalized, entry];
    })
  );
}

function probeRequest(value: unknown): ProbeRequest {
  const input = objectValue(value, 'HTTP probe request');
  const method =
    optionalString(input['Method'], 'Method')?.toUpperCase() ?? 'GET';
  if (!['GET', 'HEAD', 'POST', 'QUERY'].includes(method)) {
    throw new CoreError(
      'UnsupportedCapability',
      `HTTP probe method ${method} is not supported`
    );
  }
  const body = input['Body'];
  if (body !== undefined && typeof body !== 'string') {
    throw new CoreError('ValidationFailed', 'HTTP probe Body must be a string');
  }
  if (typeof body === 'string' && body.length > MAX_BODY_BYTES) {
    throw new CoreError('QuotaExceeded', 'HTTP probe Body is too long');
  }
  if ((method === 'GET' || method === 'HEAD') && body !== undefined) {
    throw new CoreError(
      'ValidationFailed',
      `HTTP probe ${method} must not contain a body`
    );
  }
  return {
    url: probeUrl(input['Url']),
    method,
    headers: probeHeaders(input['Headers']),
    ...(body === undefined ? {} : { body }),
  };
}

function timeoutMilliseconds(value: number | undefined): number {
  const timeout = value ?? DEFAULT_TIMEOUT_MILLISECONDS;
  if (!Number.isSafeInteger(timeout) || timeout < 1 || timeout > 8_000) {
    throw new CoreError(
      'ValidationFailed',
      'HTTP probe timeout must be between 1 and 8000 milliseconds'
    );
  }
  return timeout;
}

async function boundedBody(response: Response): Promise<{
  readonly body: string;
  readonly truncated: boolean;
}> {
  const reader = response.body?.getReader();
  if (!reader) return { body: '', truncated: false };
  const chunks: Uint8Array[] = [];
  let total = 0;
  let truncated = false;
  while (true) {
    const next = await reader.read();
    if (next.done) break;
    const remaining = MAX_BODY_BYTES - total;
    if (next.value.byteLength > remaining) truncated = true;
    if (remaining > 0) {
      const chunk = next.value.subarray(0, remaining);
      chunks.push(chunk);
      total += chunk.byteLength;
    }
    if (total === MAX_BODY_BYTES) {
      const extra = await reader.read();
      if (!extra.done) truncated = true;
      break;
    }
  }
  await reader.cancel();
  const joined = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    joined.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return { body: new TextDecoder().decode(joined), truncated };
}

async function executeProbe(
  request: ProbeRequest,
  timeout: number
): Promise<Readonly<Record<string, unknown>>> {
  const startedAt = performance.now();
  let response: Response;
  try {
    response = await fetch(request.url, {
      method: request.method,
      headers: request.headers,
      redirect: 'manual',
      signal: AbortSignal.timeout(timeout),
      ...(request.body === undefined ? {} : { body: request.body }),
    });
  } catch {
    return {
      Ok: false,
      Error: 'unreachable',
      ResponseTimeMilliseconds: Math.max(
        0,
        Math.round(performance.now() - startedAt)
      ),
    };
  }
  const { body, truncated } = await boundedBody(response);
  return {
    Ok: response.status >= 200 && response.status < 300,
    StatusCode: response.status,
    Headers: Object.fromEntries(response.headers.entries()),
    Body: body,
    Truncated: truncated,
    ResponseTimeMilliseconds: Math.max(
      0,
      Math.round(performance.now() - startedAt)
    ),
  };
}

function pollRequests(command: ProviderCommandInput): readonly ProbeRequest[] {
  const raw = command.input['Requests'];
  if (!Array.isArray(raw) || raw.length < 1 || raw.length > MAX_POLL_REQUESTS) {
    throw new CoreError(
      'ValidationFailed',
      `HTTP Poll Requests must contain 1 to ${MAX_POLL_REQUESTS} entries`
    );
  }
  return raw.map(probeRequest);
}

export async function reduceHttpProbe(
  command: ProviderCommandInput,
  options: HttpProbeOptions = {}
): Promise<ProviderCommandResult> {
  const timeout = timeoutMilliseconds(options.timeoutMilliseconds);
  if (command.operation === 'Probe') {
    return result(
      'AwsHttpEndpointProbed',
      await executeProbe(probeRequest(command.input), timeout)
    );
  }
  if (command.operation === 'Poll') {
    const responses = [];
    for (const request of pollRequests(command)) {
      responses.push(await executeProbe(request, timeout));
    }
    return result('AwsHttpEndpointsPolled', { Responses: responses });
  }
  throw new CoreError(
    'UnsupportedCapability',
    `HTTP async operation ${command.operation} is not supported`
  );
}
