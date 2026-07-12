import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import type {
  ProviderCommandInput,
  ProviderWorldView,
} from '@tenkacloud/simulator-core';
import { reduceHttpProbe } from '../src/http-probe';
import { AwsProvider } from '../src/provider';

let server: ReturnType<typeof Bun.serve>;

beforeEach(() => {
  server = Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    async fetch(request) {
      const url = new URL(request.url);
      if (url.pathname === '/redirect') {
        return Response.redirect(`${server.url.origin}/ok`, 302);
      }
      if (url.pathname === '/large') {
        return new Response('x'.repeat(70 * 1024));
      }
      if (url.pathname === '/slow') {
        await Bun.sleep(50);
        return new Response('late');
      }
      if (url.pathname === '/empty') return new Response(null, { status: 204 });
      return Response.json(
        {
          method: request.method,
          body: await request.text(),
          trace: request.headers.get('x-trace'),
        },
        { status: 201, headers: { 'x-workload': 'real-http' } }
      );
    },
  });
});

afterEach(async () => {
  await server.stop(true);
});

function command(
  operation: string,
  input: Readonly<Record<string, unknown>>
): ProviderCommandInput {
  return {
    worldId: 'world',
    deploymentId: 'deployment',
    service: 'http',
    operation,
    resourceType: 'HTTP::Endpoint',
    input,
  };
}

const world: ProviderWorldView = {
  world: {
    worldId: 'world',
    tenantId: 'tenant',
    eventId: 'event',
    teamId: 'team',
    deploymentId: 'deployment',
    seed: 'seed',
    virtualTime: '2026-07-12T00:00:00.000Z',
    status: 'active',
  },
  resources: [],
};

describe('AWS HTTP scorer probe', () => {
  it('numeric loopback workloadを実HTTPでprobeしredirectとbody上限を保つ', async () => {
    const provider = new AwsProvider();
    const response = await provider.reduceAsync(
      command('Probe', {
        Url: `${server.url.origin}/ok`,
        Method: 'POST',
        Headers: { 'X-Trace': 'probe-1' },
        Body: '{"query":"tenka"}',
      }),
      world
    );
    expect(response.response).toMatchObject({
      Ok: true,
      StatusCode: 201,
      Truncated: false,
      Headers: { 'x-workload': 'real-http' },
      ResponseTimeMilliseconds: expect.any(Number),
    });
    expect(JSON.parse(String(response.response['Body']))).toEqual({
      method: 'POST',
      body: '{"query":"tenka"}',
      trace: 'probe-1',
    });

    const redirected = await reduceHttpProbe(
      command('Probe', { Url: `${server.url.origin}/redirect` })
    );
    expect(redirected.response).toMatchObject({
      Ok: false,
      StatusCode: 302,
    });
    const large = await reduceHttpProbe(
      command('Probe', { Url: `${server.url.origin}/large` })
    );
    expect(large.response).toMatchObject({ Truncated: true });
    expect(String(large.response['Body'])).toHaveLength(64 * 1024);
    const empty = await reduceHttpProbe(
      command('Probe', { Url: `${server.url.origin}/empty`, Method: 'HEAD' })
    );
    expect(empty.response).toMatchObject({ StatusCode: 204, Body: '' });
  });

  it('Pollが複数requestを実行しtimeoutと到達不能を失敗結果にする', async () => {
    const polled = await reduceHttpProbe(
      command('Poll', {
        Requests: [
          { Url: `${server.url.origin}/ok` },
          {
            Url: `${server.url.origin}/ok`,
            Method: 'QUERY',
            Body: '{}',
          },
          { Url: 'http://127.0.0.1:1/unreachable' },
        ],
      })
    );
    const responses = polled.response['Responses'];
    expect(responses).toBeArray();
    expect(responses).toHaveLength(3);
    expect(responses).toMatchObject([
      { Ok: true, StatusCode: 201 },
      { Ok: true, StatusCode: 201 },
      { Ok: false, Error: 'unreachable' },
    ]);
    expect(
      (
        await reduceHttpProbe(
          command('Probe', { Url: `${server.url.origin}/slow` }),
          { timeoutMilliseconds: 1 }
        )
      ).response
    ).toMatchObject({ Ok: false, Error: 'unreachable' });
  });

  it('SSRF URL header method body poll timeoutの境界をloudに拒否する', async () => {
    const invalidRequests: readonly Readonly<Record<string, unknown>>[] = [
      { Url: 'not-a-url' },
      { Url: 'https://127.0.0.1:443/' },
      { Url: 'http://localhost:8080/' },
      { Url: 'http://user:secret@127.0.0.1:8080/' },
      { Url: 'http://127.0.0.1:8080/#fragment' },
      { Url: `http://127.0.0.1:8080/${'x'.repeat(2048)}` },
      { Url: `${server.url.origin}/`, Method: 'DELETE' },
      { Url: `${server.url.origin}/`, Method: 'GET', Body: '' },
      { Url: `${server.url.origin}/`, Body: 1 },
      {
        Url: `${server.url.origin}/`,
        Method: 'POST',
        Body: 'x'.repeat(64 * 1024 + 1),
      },
      { Url: `${server.url.origin}/`, Headers: { Connection: 'close' } },
      { Url: `${server.url.origin}/`, Headers: { Trace: 'bad\nvalue' } },
      {
        Url: `${server.url.origin}/`,
        Headers: Object.fromEntries(
          Array.from({ length: 65 }, (_, index) => [`x-${index}`, 'value'])
        ),
      },
    ];
    for (const input of invalidRequests) {
      await expect(reduceHttpProbe(command('Probe', input))).rejects.toThrow();
    }
    for (const Requests of [[], Array.from({ length: 33 }, () => ({})), {}]) {
      await expect(
        reduceHttpProbe(command('Poll', { Requests }))
      ).rejects.toThrow('Requests');
    }
    await expect(
      reduceHttpProbe(command('Probe', { Url: `${server.url.origin}/` }), {
        timeoutMilliseconds: 0,
      })
    ).rejects.toThrow('timeout');
    await expect(reduceHttpProbe(command('Unknown', {}))).rejects.toThrow(
      'async operation'
    );
  });
});
