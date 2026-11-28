import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const origin = 'http://127.0.0.1:12345';
const collection = '/2020-05-31/distribution';
const id = 'D' + 'a'.repeat(24), invalidation = 'I' + 'b'.repeat(24);
const asset = name => new URL('../../internal/server/apidocs/' + name, import.meta.url);
const spec = JSON.parse(readFileSync(asset('cloudfront-local.json'), 'utf8'));
function load() {
  let options;
  const calls = [];
  vm.runInNewContext(readFileSync(asset('cloudfront-local-init.js'), 'utf8'), {
    Request, URL, location: new URL(origin + '/_simulator/docs/cloudfront-local.html'),
    Scalar: {createApiReference(selector, value) { assert.equal(selector, '#reference'); options = value; }},
    fetch(url, init) { calls.push([url, init]); return Promise.resolve({status: 200}); },
  });
  return {options, calls};
}

test('the separate reference contains exactly eight native CloudFront operations', () => {
  const names = Object.values(spec.paths).flatMap(path => Object.values(path).map(operation => operation.operationId));
  assert.deepEqual(names.sort(), ['CreateDistribution', 'GetDistribution', 'GetDistributionConfig', 'UpdateDistribution',
    'DeleteDistribution', 'ListDistributions', 'CreateInvalidation', 'GetInvalidation'].sort());
  assert.deepEqual(spec.servers, [{url: '/'}]);
  for (const path of Object.values(spec.paths)) for (const operation of Object.values(path)) {
    const route = operation.parameters.filter(parameter => parameter.name === 'X-Simulator-Service');
    assert.equal(route.length, 1); assert.equal(route[0].required, true);
    assert.deepEqual(route[0].schema.enum, ['cloudfront']);
    assert.equal(route[0].schema.default, 'cloudfront');
    assert.match(operation.externalDocs.url, /^https:\/\/docs\.aws\.amazon\.com\/cloudfront\/latest\/APIReference\/API_[A-Za-z]+\.html$/);
  }
});

test('actual native methods preserve body and If-Match while replacing authority with only the browser session', async () => {
  const {options, calls} = load();
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
    ['/_simulator/cloudfront/distributions/' + id + '/objects/asset.txt', 'GET'],
    ['/_simulator/v1/access', 'POST'], ['/2020-05-31/vpc-origin', 'POST'],
  ]) {
    const {options, calls} = load();
    await assert.rejects(options.customFetch(url.startsWith('/') ? origin + url : url, {method}));
    assert.equal(calls.length, 0);
  }
});
