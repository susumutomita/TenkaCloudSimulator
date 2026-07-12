import { CoreError } from '@tenkacloud/simulator-core';

const REQUEST_TIMEOUT_MS = 8_000;
const MAX_RESPONSE_BYTES = 64 * 1024;
const ALICE_HEADERS = { authorization: 'Bearer token-alice' } as const;
const LEAK_MARKERS = [
  'stack',
  'Error:',
  'TypeError',
  'ReferenceError',
  'at Object',
  '/usr/',
  '/var/',
  'node_modules',
  'token-',
  'Bearer ',
  'SECRET',
  'Traceback',
  'syntax error',
] as const;

export interface ExternalEvaluatorOptions {
  readonly trustedWorkerOrigins?: readonly string[];
}

interface HttpResult {
  readonly status: number;
  readonly body: string;
}

interface StageResult {
  readonly stage: string;
  readonly passed: boolean;
  readonly detail: string;
}

type Stage = (base: URL) => Promise<Omit<StageResult, 'stage'>>;

class ExternalRequestError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ExternalRequestError';
  }
}

function isLoopback(hostname: string): boolean {
  return (
    hostname === '127.0.0.1' || hostname === '::1' || hostname === 'localhost'
  );
}

export function validatedTrustedWorkerOrigins(
  values: readonly string[] = []
): ReadonlySet<string> {
  const origins = new Set<string>();
  for (const value of values) {
    let url: URL;
    try {
      url = new URL(value);
    } catch {
      throw new CoreError(
        'ValidationFailed',
        'trusted worker origin must be an absolute URL'
      );
    }
    if (
      !isLoopback(url.hostname) ||
      (url.protocol !== 'http:' && url.protocol !== 'https:') ||
      url.username ||
      url.password ||
      url.pathname !== '/' ||
      url.search ||
      url.hash
    ) {
      throw new CoreError(
        'ValidationFailed',
        'trusted worker origin must be a credential-free loopback origin'
      );
    }
    origins.add(url.origin);
  }
  return origins;
}

export function validatedWorkerBase(
  value: unknown,
  trustedOrigins: ReadonlySet<string>
): URL | string {
  if (typeof value !== 'string' || !value) return 'workerUrl is required';
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    return 'workerUrl must be an absolute URL';
  }
  if (url.username || url.password) {
    return 'workerUrl must not contain credentials';
  }
  if (url.search || url.hash) {
    return 'workerUrl must not contain a query or fragment';
  }
  if (trustedOrigins.has(url.origin)) return url;
  if (url.protocol !== 'https:') return 'workerUrl must be https';
  const host = url.hostname.toLowerCase();
  if (
    !host.endsWith('.workers.dev') ||
    host === 'workers.dev' ||
    host.split('.').length < 3
  ) {
    return 'workerUrl host must be a <name>.workers.dev subdomain';
  }
  if (url.port) return 'workerUrl must use the default HTTPS port';
  return url;
}

async function boundedBody(response: Response): Promise<string> {
  if (!response.body) return '';
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  while (total < MAX_RESPONSE_BYTES) {
    const next = await reader.read();
    if (next.done) break;
    const remaining = MAX_RESPONSE_BYTES - total;
    const chunk = next.value.subarray(0, remaining);
    chunks.push(chunk);
    total += chunk.byteLength;
    if (chunk.byteLength < next.value.byteLength) {
      await reader.cancel();
      break;
    }
  }
  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(bytes);
}

async function get(
  base: URL,
  path: string,
  headers: Readonly<Record<string, string>> = {}
): Promise<HttpResult> {
  let response: Response;
  try {
    response = await fetch(new URL(path.replace(/^\//, ''), base), {
      method: 'GET',
      headers,
      redirect: 'manual',
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    });
  } catch (error) {
    throw new ExternalRequestError(
      error instanceof Error ? error.message : 'request failed'
    );
  }
  return { status: response.status, body: await boundedBody(response) };
}

function jsonObject(
  value: string
): Readonly<Record<string, unknown>> | undefined {
  try {
    const parsed: unknown = JSON.parse(value);
    return parsed !== null &&
      typeof parsed === 'object' &&
      !Array.isArray(parsed)
      ? Object.fromEntries(Object.entries(parsed))
      : undefined;
  } catch {
    return undefined;
  }
}

function leaks(value: string): readonly string[] {
  return LEAK_MARKERS.filter((marker) => value.includes(marker));
}

function passed(detail: string): Omit<StageResult, 'stage'> {
  return { passed: true, detail };
}

function failed(detail: string): Omit<StageResult, 'stage'> {
  return { passed: false, detail };
}

async function deployStage(base: URL) {
  const response = await get(base, '/healthz');
  if (response.status !== 200) {
    return failed(`GET /healthz expected 200, got ${response.status}`);
  }
  const body = jsonObject(response.body);
  return body?.['status'] === 'ok'
    ? passed('healthz ok')
    : failed('GET /healthz must return JSON {"status":"ok"}');
}

async function inputStage(base: URL) {
  const payloads = [
    '%27%20OR%201%3D1',
    '..%2F..%2F..%2Fetc%2Fpasswd',
    '%3Cscript%3E',
    'a'.repeat(600),
  ];
  for (const payload of payloads) {
    const response = await get(base, `/api/profile/${payload}`, ALICE_HEADERS);
    if (response.status >= 500) {
      return failed(
        `malformed id caused ${response.status} (must reject gracefully, no 5xx)`
      );
    }
    if (response.status !== 400 && response.status !== 404) {
      return failed(
        `malformed id returned ${response.status} (expected 400/404 reject)`
      );
    }
    const found = leaks(response.body);
    if (found.length > 0) return failed(`error body leaked ${found.join(',')}`);
  }
  return passed('malformed input rejected with no leakage');
}

async function authorizationStage(base: URL) {
  const missing = await get(base, '/api/profile');
  if (missing.status !== 401) {
    return failed(
      `GET /api/profile without token expected 401, got ${missing.status}`
    );
  }
  const invalid = await get(base, '/api/profile', {
    authorization: 'Bearer nope',
  });
  if (invalid.status !== 401) {
    return failed(`invalid token expected 401, got ${invalid.status}`);
  }
  const own = await get(base, '/api/profile', ALICE_HEADERS);
  if (own.status !== 200) {
    return failed(
      `valid token GET /api/profile expected 200, got ${own.status}`
    );
  }
  const profile = jsonObject(own.body) ?? {};
  if (
    profile['id'] !== undefined &&
    profile['id'] !== 'alice' &&
    profile['userId'] !== undefined &&
    profile['userId'] !== 'alice'
  ) {
    return failed("own profile returned someone else's data");
  }
  const crossUser = await get(base, '/api/profile/bob', ALICE_HEADERS);
  if (crossUser.status === 200) {
    return failed('IDOR: alice could read /api/profile/bob (expected 403)');
  }
  if (crossUser.status !== 403) {
    return failed(`cross-user access expected 403, got ${crossUser.status}`);
  }
  const ownById = await get(base, '/api/profile/alice', ALICE_HEADERS);
  return ownById.status === 200
    ? passed('authorization enforced and IDOR blocked')
    : failed(
        `alice reading /api/profile/alice expected 200, got ${ownById.status}`
      );
}

async function disclosureStage(base: URL) {
  for (const [path, headers] of [
    ['/api/profile', undefined],
    ['/api/profile/charlie', ALICE_HEADERS],
    ['/api/profile/bob', ALICE_HEADERS],
  ] as const) {
    const response = await get(base, path, headers);
    const found = leaks(response.body);
    if (found.length > 0) return failed(`${path} leaked ${found.join(',')}`);
  }
  return passed('no internal details leaked in error responses');
}

async function regressionStage(base: URL) {
  for (let index = 0; index < 3; index += 1) {
    const health = await get(base, '/healthz');
    if (health.status !== 200) {
      return failed(`healthz not consistent (got ${health.status})`);
    }
    const crossUser = await get(base, '/api/profile/bob', ALICE_HEADERS);
    if (crossUser.status !== 403) {
      return failed(`IDOR guard not consistent (got ${crossUser.status})`);
    }
  }
  return passed('behavior consistent under repetition');
}

const STAGES: readonly [string, Stage][] = [
  ['0-deploy', deployStage],
  ['1-input-validation', inputStage],
  ['2-authorization-idor', authorizationStage],
  ['3-info-disclosure', disclosureStage],
  ['4-regression', regressionStage],
];

export function externalEvaluatorFailureMessage(error: unknown): string {
  if (error instanceof ExternalRequestError) {
    return `could not reach worker: ${error.message}`;
  }
  return `evaluator error: ${error instanceof Error ? error.message : String(error)}`;
}

export async function evaluateExternalWorker(
  workerUrl: unknown,
  flag: string,
  trustedOrigins: ReadonlySet<string>
): Promise<Readonly<Record<string, unknown>>> {
  const base = validatedWorkerBase(workerUrl, trustedOrigins);
  if (typeof base === 'string') return { passed: false, error: base };
  const results: StageResult[] = [];
  for (const [stage, evaluate] of STAGES) {
    let outcome: Omit<StageResult, 'stage'>;
    try {
      outcome = await evaluate(base);
    } catch (error) {
      outcome = failed(externalEvaluatorFailureMessage(error));
    }
    results.push({ stage, ...outcome });
    if (!outcome.passed) break;
  }
  const allPassed =
    results.length === STAGES.length && results.every((item) => item.passed);
  return allPassed
    ? {
        passed: true,
        results,
        flag,
        message:
          'All 5 stages passed. Submit the flag in the Participant Portal.',
      }
    : {
        passed: false,
        results,
        message: 'Fix the failing stage and re-run after `wrangler deploy`.',
      };
}
