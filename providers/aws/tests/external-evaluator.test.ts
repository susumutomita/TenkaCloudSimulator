import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import { CoreError } from '@tenkacloud/simulator-core';
import {
  evaluateExternalWorker,
  externalEvaluatorFailureMessage,
  validatedTrustedWorkerOrigins,
  validatedWorkerBase,
} from '../src/external-evaluator';

let mode = 'success';
let calls = new Map<string, number>();
let server: Bun.Server<undefined>;

function callNumber(key: string): number {
  const count = (calls.get(key) ?? 0) + 1;
  calls.set(key, count);
  return count;
}

function json(value: unknown, status = 200): Response {
  return Response.json(value, { status });
}

function healthResponse(count: number): Response {
  if (mode === 'health-status') return new Response(null, { status: 503 });
  if (mode === 'health-body') return json({ status: 'starting' });
  if (mode === 'health-invalid-json') return new Response('not-json');
  if (mode === 'health-array') return json([]);
  return mode === 'regression-health' && count > 1
    ? json({ status: 'down' }, 503)
    : json({ status: 'ok' });
}

function profileResponse(
  authorization: string | null,
  count: number
): Response {
  if (!authorization) {
    if (mode === 'missing-auth') return json({ id: 'anonymous' });
    return mode === 'disclosure-leak' && count > 1
      ? new Response('Traceback /var/app.js', { status: 401 })
      : json({ error: 'unauthorized' }, 401);
  }
  if (authorization !== 'Bearer token-alice') {
    return mode === 'invalid-auth'
      ? json({ id: 'anonymous' })
      : json({ error: 'unauthorized' }, 401);
  }
  if (mode === 'own-status') return json({ error: 'down' }, 500);
  return mode === 'own-content'
    ? json({ id: 'bob', userId: 'bob' })
    : json({ id: 'alice' });
}

function malformedProfileResponse(): Response {
  if (mode === 'malformed-500') return json({ error: 'down' }, 500);
  if (mode === 'malformed-status') return json({ id: 'unexpected' });
  if (mode === 'malformed-leak') {
    return new Response('TypeError at Object /usr/app.js', { status: 400 });
  }
  return mode === 'large-body'
    ? new Response(' '.repeat(70_000), { status: 400 })
    : json({ error: 'invalid id' }, 400);
}

function identifiedProfileResponse(pathname: string, count: number): Response {
  if (pathname === '/api/profile/bob') {
    if (mode === 'cross-200') return json({ id: 'bob' });
    if (mode === 'cross-other') return json({ error: 'missing' }, 404);
    return mode === 'regression-cross' && count > 2
      ? json({ error: 'down' }, 500)
      : json({ error: 'forbidden' }, 403);
  }
  if (pathname === '/api/profile/alice') {
    return mode === 'own-id'
      ? json({ error: 'forbidden' }, 403)
      : json({ id: 'alice' });
  }
  if (pathname === '/api/profile/charlie') {
    return json({ error: 'forbidden' }, 403);
  }
  return malformedProfileResponse();
}

function workload(request: Request): Response {
  const url = new URL(request.url);
  const authorization = request.headers.get('authorization');
  const count = callNumber(`${url.pathname}\u0000${authorization ?? ''}`);
  if (url.pathname === '/healthz') return healthResponse(count);
  if (url.pathname === '/api/profile') {
    return profileResponse(authorization, count);
  }
  if (url.pathname.startsWith('/api/profile/')) {
    return identifiedProfileResponse(url.pathname, count);
  }
  return json({ error: 'not found' }, 404);
}

beforeEach(() => {
  mode = 'success';
  calls = new Map();
  server = Bun.serve({ hostname: '127.0.0.1', port: 0, fetch: workload });
});

afterEach(async () => {
  await server.stop(true);
});

function trusted(): ReadonlySet<string> {
  return validatedTrustedWorkerOrigins([server.url.origin]);
}

describe('Cloudflare external evaluator', () => {
  it('非Error throwもcredentialを足さずbounded診断へ正規化する', () => {
    expect(externalEvaluatorFailureMessage('closed')).toBe(
      'evaluator error: closed'
    );
    expect(externalEvaluatorFailureMessage(new Error('failed'))).toBe(
      'evaluator error: failed'
    );
  });

  it('loopbackの実HTTP workloadへ全stageを実行してflagを返す', async () => {
    const result = await evaluateExternalWorker(
      server.url.origin,
      'TC{external-evaluator}',
      trusted()
    );

    expect(result).toMatchObject({
      passed: true,
      flag: 'TC{external-evaluator}',
      message:
        'All 5 stages passed. Submit the flag in the Participant Portal.',
    });
    expect(result['results']).toHaveLength(5);
  });

  it('各stageの失敗を最初の不一致で止めてflagを返さない', async () => {
    for (const failureMode of [
      'health-status',
      'health-body',
      'health-invalid-json',
      'health-array',
      'malformed-500',
      'malformed-status',
      'malformed-leak',
      'missing-auth',
      'invalid-auth',
      'own-status',
      'own-content',
      'cross-200',
      'cross-other',
      'own-id',
      'disclosure-leak',
      'regression-health',
      'regression-cross',
    ]) {
      mode = failureMode;
      calls.clear();
      const result = await evaluateExternalWorker(
        server.url.origin,
        'TC{must-not-leak}',
        trusted()
      );
      expect(result['passed']).toBe(false);
      expect(result).not.toHaveProperty('flag');
    }
  });

  it('responseを64KiBで打ち切りredirectを追わず到達不能を失敗にする', async () => {
    mode = 'large-body';
    const bounded = await evaluateExternalWorker(
      server.url.origin,
      'TC{bounded}',
      trusted()
    );
    expect(bounded['passed']).toBe(true);

    const unavailableOrigin = 'http://127.0.0.1:9';
    const unavailable = await evaluateExternalWorker(
      unavailableOrigin,
      'TC{unavailable}',
      validatedTrustedWorkerOrigins([unavailableOrigin])
    );
    expect(unavailable).toMatchObject({
      passed: false,
      results: [
        {
          stage: '0-deploy',
          passed: false,
        },
      ],
    });
  });

  it('production URL境界と明示trust originをSSRF-safeに検証する', async () => {
    for (const value of [
      undefined,
      '',
      'relative',
      'https://user:pass@example.workers.dev',
      'https://example.workers.dev/?query=1',
      'http://example.workers.dev',
      'https://workers.dev',
      'https://example.com',
      'https://example.workers.dev:8443',
    ]) {
      const result = await evaluateExternalWorker(
        value,
        'TC{hidden}',
        new Set()
      );
      expect(result['passed']).toBe(false);
      expect(result).not.toHaveProperty('flag');
    }
    expect(
      validatedWorkerBase('https://safe.example.workers.dev', new Set())
    ).toBeInstanceOf(URL);

    for (const invalid of [
      'not-a-url',
      'https://example.com',
      'ftp://127.0.0.1',
      'http://user:pass@127.0.0.1',
      'http://127.0.0.1/path',
      'http://127.0.0.1/?query=1',
      'http://127.0.0.1/#fragment',
    ]) {
      expect(() => validatedTrustedWorkerOrigins([invalid])).toThrow(CoreError);
    }
  });
});
