import { describe, expect, it } from 'bun:test';
import type { ProviderWorldView, ResourceRecord } from './domain';
import { CoreError } from './errors';
import {
  MAX_PROVIDER_HTTP_BODY_BYTES,
  providerHttpRequest,
  providerHttpResponse,
  singleReadyDeploymentResource,
} from './http-data-plane';

const VALID_INPUT = {
  Method: 'get',
  Path: '/hello?language=ja',
  Headers: { Accept: 'text/plain' },
  Body: 'request body',
};

function error(operation: () => unknown): CoreError {
  try {
    operation();
  } catch (cause) {
    if (cause instanceof CoreError) return cause;
    throw cause;
  }
  throw new Error('CoreError が発生しませんでした');
}

function resource(changes: Partial<ResourceRecord> = {}): ResourceRecord {
  return {
    worldId: 'world',
    deploymentId: 'deployment',
    provider: 'test',
    resourceType: 'Test::Endpoint',
    resourceId: 'endpoint',
    properties: {},
    status: 'ready',
    ...changes,
  };
}

function world(resources: readonly ResourceRecord[]): ProviderWorldView {
  return {
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
    resources,
  };
}

describe('provider-neutral HTTP data plane 境界', () => {
  it('request の method と header 名を正規化して bounded 値を維持する', () => {
    expect(providerHttpRequest(VALID_INPUT)).toEqual({
      method: 'GET',
      path: '/hello?language=ja',
      headers: { accept: 'text/plain' },
      body: 'request body',
    });
  });

  it('request field の不足と未知 field を拒否する', () => {
    for (const input of [
      { Method: 'GET', Path: '/', Headers: {} },
      { ...VALID_INPUT, Unknown: true },
    ]) {
      expect(error(() => providerHttpRequest(input)).code).toBe(
        'ValidationFailed'
      );
    }
  });

  it('method と path の型、構文、長さを拒否する', () => {
    for (const Method of [1, '', 'bad method', `G${'E'.repeat(32)}`]) {
      expect(
        error(() => providerHttpRequest({ ...VALID_INPUT, Method })).message
      ).toContain('Method');
    }
    for (const Path of [
      1,
      'relative',
      '//authority',
      '/white space',
      '/fragment#part',
      '/null\u0000byte',
      `/${'a'.repeat(2_048)}`,
    ]) {
      expect(
        error(() => providerHttpRequest({ ...VALID_INPUT, Path })).message
      ).toContain('Path');
    }
  });

  it('header container、件数、名前、値、大小文字重複を拒否する', () => {
    const tooMany = Object.fromEntries(
      Array.from({ length: 65 }, (_, index) => [`x-${index}`, 'value'])
    );
    const invalidHeaders: readonly unknown[] = [
      null,
      [],
      tooMany,
      { 'bad header': 'value' },
      { Valid: 1 },
      { Valid: 'x'.repeat(8_193) },
      { Valid: 'line\rbreak' },
      { Valid: 'line\nbreak' },
      { Valid: 'null\u0000byte' },
      { Authorization: 'Bearer secret' },
      { Connection: 'close' },
      { Accept: 'one', accept: 'two' },
    ];
    for (const Headers of invalidHeaders) {
      expect(() => providerHttpRequest({ ...VALID_INPUT, Headers })).toThrow(
        CoreError
      );
    }
  });

  it('body の型と UTF-8 byte 上限を検証する', () => {
    expect(
      error(() => providerHttpRequest({ ...VALID_INPUT, Body: 1 })).code
    ).toBe('ValidationFailed');
    expect(
      error(() =>
        providerHttpRequest({
          ...VALID_INPUT,
          Body: 'あ'.repeat(Math.floor(MAX_PROVIDER_HTTP_BODY_BYTES / 3) + 1),
        })
      ).code
    ).toBe('QuotaExceeded');
  });

  it('GET と HEAD は保存 representation を標準 response shape にする', () => {
    const representation = {
      statusCode: 201,
      body: 'created',
      contentType: 'text/plain; charset=utf-8',
    };
    const get = providerHttpRequest(VALID_INPUT);
    const head = providerHttpRequest({ ...VALID_INPUT, Method: 'HEAD' });
    expect(providerHttpResponse(get, representation)).toEqual({
      StatusCode: 201,
      Headers: { 'content-type': 'text/plain; charset=utf-8' },
      Body: 'created',
    });
    expect(providerHttpResponse(head, representation)).toMatchObject({
      StatusCode: 201,
      Body: '',
    });
  });

  it('未対応 method は Allow header 付き 405 にする', () => {
    const request = providerHttpRequest({ ...VALID_INPUT, Method: 'POST' });
    expect(
      providerHttpResponse(request, {
        statusCode: 0,
        body: '',
        contentType: '',
      })
    ).toEqual({
      StatusCode: 405,
      Headers: {
        allow: 'GET, HEAD',
        'content-type': 'text/plain; charset=utf-8',
      },
      Body: 'Method Not Allowed',
    });
  });

  it('endpoint response の status、body、content type を検証する', () => {
    const request = providerHttpRequest(VALID_INPUT);
    const invalid = [
      { statusCode: 99, body: '', contentType: 'text/plain' },
      { statusCode: 199, body: '', contentType: 'text/plain' },
      { statusCode: 600, body: '', contentType: 'text/plain' },
      { statusCode: 200.5, body: '', contentType: 'text/plain' },
      {
        statusCode: 200,
        body: 'あ'.repeat(Math.floor(MAX_PROVIDER_HTTP_BODY_BYTES / 3) + 1),
        contentType: 'text/plain',
      },
      { statusCode: 200, body: 1, contentType: 'text/plain' },
      { statusCode: 200, body: '', contentType: '' },
      { statusCode: 200, body: '', contentType: 'x'.repeat(129) },
      { statusCode: 200, body: '', contentType: 'text/plain\r\ninvalid' },
      { statusCode: 204, body: 'unexpected', contentType: 'text/plain' },
    ];
    for (const representation of invalid) {
      expect(() => providerHttpResponse(request, representation)).toThrow(
        CoreError
      );
    }
    expect(
      providerHttpResponse(request, {
        statusCode: 204,
        body: '',
        contentType: 'text/plain',
      })
    ).toMatchObject({ StatusCode: 204, Body: '' });
  });

  it('同じ deployment の resource が一件かつ ready の場合だけ返す', () => {
    const expected = resource();
    expect(
      singleReadyDeploymentResource(
        world([
          resource({ deploymentId: 'other' }),
          resource({ resourceId: 'deleted', status: 'deleted' }),
          expected,
        ]),
        'deployment',
        'test',
        'Test::Endpoint',
        'test'
      )
    ).toEqual(expected);
  });

  it('endpoint resource の不足、重複、非 ready を loud に拒否する', () => {
    const missing = error(() =>
      singleReadyDeploymentResource(
        world([]),
        'deployment',
        'test',
        'Test::Endpoint',
        'test'
      )
    );
    const deleted = error(() =>
      singleReadyDeploymentResource(
        world([resource({ status: 'deleted' })]),
        'deployment',
        'test',
        'Test::Endpoint',
        'test'
      )
    );
    const ambiguous = error(() =>
      singleReadyDeploymentResource(
        world([resource(), resource({ resourceId: 'second' })]),
        'deployment',
        'test',
        'Test::Endpoint',
        'test'
      )
    );
    const pending = error(() =>
      singleReadyDeploymentResource(
        world([resource({ status: 'pending' })]),
        'deployment',
        'test',
        'Test::Endpoint',
        'test'
      )
    );
    expect(missing.code).toBe('NotFound');
    expect(deleted.code).toBe('NotFound');
    expect(ambiguous.code).toBe('Conflict');
    expect(pending.code).toBe('Conflict');
  });
});
