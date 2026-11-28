import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const origin = 'http://127.0.0.1:12345';
const collection = '/2020-05-31/distribution';
const id = 'D' + 'a'.repeat(24), invalidation = 'I' + 'b'.repeat(24);
const asset = name => new URL('../../internal/server/apidocs/' + name, import.meta.url);
const spec = JSON.parse(readFileSync(asset('cloudfront-local.json'), 'utf8'));
async function load() {
  let options;
  const calls = [];
  await vm.runInNewContext(readFileSync(asset('cloudfront-local-init.js'), 'utf8'), {
    Request, URL, location: new URL(origin + '/_simulator/docs/cloudfront-local.html'),
    Scalar: {createApiReference(selector, value) { assert.equal(selector, '#reference'); options = value; }},
    fetch(url, init) { calls.push([url, init]); return Promise.resolve({ok: true, status: 200, json: async () => structuredClone(spec)}); },
  });
  assert.equal(calls.length, 1);
  assert.equal(calls[0][0], '/_simulator/docs/cloudfront-local.json');
  assert.equal(calls[0][1].credentials, 'same-origin'); assert.equal(calls[0][1].redirect, 'error');
  assert.deepEqual(JSON.parse(options.content).servers, [{url: origin, description: 'This simulator only'}]);
  calls.length = 0;
  return {options, calls};
}

test('the separate reference contains exactly eight native CloudFront operations', () => {
  const operations = Object.values(spec.paths).flatMap(path => Object.values(path));
  const names = operations.filter(operation => !operation['x-simulator-only']).map(operation => operation.operationId);
  assert.deepEqual(names.sort(), ['CreateDistribution', 'GetDistribution', 'GetDistributionConfig', 'UpdateDistribution',
    'DeleteDistribution', 'ListDistributions', 'CreateInvalidation', 'GetInvalidation'].sort());
  assert.deepEqual(spec.servers, [{url: '/'}]);
  assert.equal(operations.filter(operation => operation['x-simulator-only']).length, 1);
  for (const operation of operations.filter(operation => !operation['x-simulator-only'])) {
    const route = operation.parameters.filter(parameter => parameter.name === 'X-Simulator-Service');
    assert.equal(route.length, 1); assert.equal(route[0].required, true);
    assert.deepEqual(route[0].schema.enum, ['cloudfront']);
    assert.equal(route[0].schema.default, 'cloudfront');
    assert.match(operation.externalDocs.url, /^https:\/\/docs\.aws\.amazon\.com\/cloudfront\/latest\/APIReference\/API_[A-Za-z]+\.html$/);
  }
});

test('actual native methods preserve body and If-Match while replacing authority with only the browser session', async () => {
  const {options, calls} = await load();
  assert.equal(options.persistAuth, false); assert.equal(options.telemetry, false); assert.equal(options.proxyUrl, '');
  const cases = [
    ['POST', collection], ['GET', collection + '?MaxItems=1&Marker=' + id],
    ['GET', collection + '/' + id], ['GET', collection + '/' + id + '/config'],
    ['PUT', collection + '/' + id + '/config'], ['DELETE', collection + '/' + id],
    ['POST', collection + '/' + id + '/invalidation'], ['GET', collection + '/' + id + '/invalidation/' + invalidation],
  ];
  for (const [method, path] of cases) {
    const body = ['POST', 'PUT'].includes(method) ? '<owned>exact XML</owned>' : undefined;
    await options.customFetch(origin + path, {method, body, headers: {
      Authorization: 'discard', 'X-Simulator-Token': 'discard', 'X-Simulator-Principal': 'discard',
      'X-Simulator-Region': 'discard', 'X-Simulator-Service': 'discard', 'If-Match': 'saved-etag',
    }});
    const [url, sent] = calls.at(-1);
    assert.equal(url.href, origin + path); assert.equal(sent.method, method); assert.equal(sent.body, body);
    assert.equal(sent.credentials, 'same-origin'); assert.equal(sent.redirect, 'error');
    assert.equal(sent.headers['X-Simulator-Service'], 'cloudfront'); assert.equal(sent.headers['X-Simulator-Region'], 'us-east-1');
    assert.equal(sent.headers['If-Match'], 'saved-etag');
    assert.deepEqual(Object.keys(sent.headers).sort(), ['If-Match', 'X-Simulator-Region', 'X-Simulator-Service',
      ...(['POST', 'PUT'].includes(method) ? ['Content-Type'] : [])].sort());
  }
  assert.equal(calls.length, cases.length);
});

test('unrelated native paths, methods, origins and list query overrides never dispatch', async () => {
  for (const [url, method] of [
    ['https://example.invalid' + collection, 'GET'], [collection + '?MaxItems=1&MaxItems=2', 'GET'],
    [collection + '?unexpected=value', 'GET'], [collection + '/' + id + '?MaxItems=1', 'GET'],
    [collection, 'PATCH'], [collection + '/' + id + '/invalidation', 'GET'],
    ['/_simulator/cloudfront/distributions/' + id + '/objects/asset.txt', 'POST'],
    ['/_simulator/cloudfront/distributions/' + id + '/objects/asset%2Fother.txt', 'GET'],
    ['/_simulator/v1/access', 'POST'], ['/2020-05-31/vpc-origin', 'POST'],
  ]) {
    const {options, calls} = await load();
    await assert.rejects(options.customFetch(url.startsWith('/') ? origin + url : url, {method}));
    assert.equal(calls.length, 0);
  }
});

test('the explicit simulator-only GET preserves the native response and uses no supplied authority headers', async () => {
  const {options, calls} = await load();
  const path = '/_simulator/cloudfront/distributions/' + id + '/objects/asset.txt';
  const response = await options.customFetch(origin + path, {headers: {
    Authorization: 'discard', 'X-Simulator-Token': 'discard', 'X-Simulator-Principal': 'discard',
    'X-Simulator-Service': 'discard',
  }});
  assert.equal(response.status, 200);
  assert.equal(calls.length, 1); assert.equal(calls[0][0].href, origin + path);
  assert.equal(calls[0][1].method, 'GET'); assert.equal(calls[0][1].credentials, 'same-origin');
  assert.equal(calls[0][1].redirect, 'error'); assert.equal(calls[0][1].headers, undefined);
});
