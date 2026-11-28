import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const root = new URL('../../', import.meta.url);
const script = fs.readFileSync(new URL('internal/server/apidocs/service-quotas-init.js', root), 'utf8');
const spec = '/_simulator/docs/catalog/service-quotas.json';
const origin = 'http://127.0.0.1:4566';

async function setup() {
  const calls = [];
  let config;
  const context = {
    URL, Request, location: {origin, href: origin + '/_simulator/docs/service-quotas.html'},
    SimulatorAWSJSONRenderer: () => ({}),
    Scalar: {createApiReference(selector, value) { assert.equal(selector, '#reference'); config = value; }},
    fetch: async (input, init) => {
      calls.push({input, init});
      return {ok: true, json: async () => ({openapi: '3.1.0', paths: {}})};
    },
  };
  await vm.runInNewContext(script, context);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].input, spec);
  assert.equal(calls[0].init.credentials, 'same-origin');
  assert.equal(calls[0].init.redirect, 'error');
  assert.equal(JSON.parse(config.content).servers[0].url, origin);
  calls.length = 0;
  return {calls, config};
}

test('selected native target and typed body survive independently; no body-based operation rewrite', async () => {
  const {calls, config} = await setup();
  const body = '{"ServiceCode":"b2bi","QuotaCode":"L-05F6A9EF"}';
  await config.customFetch(origin + '/', {method: 'POST', headers: {
    'Content-Type': 'application/x-amz-json-1.1',
    'X-Amz-Target': 'ServiceQuotasV20190624.ListServices',
  }, body});
  assert.equal(calls.length, 1);
  const {input: request, init} = calls[0];
  assert.equal(request.url, origin + '/');
  assert.equal(request.headers.get('X-Amz-Target'), 'ServiceQuotasV20190624.ListServices');
  assert.equal(await request.text(), body);
  assert.equal(init.credentials, 'same-origin');
  assert.equal(init.redirect, 'error');
});

test('all nine generated native operation targets remain selectable', async () => {
  const {calls, config} = await setup();
  const document = JSON.parse(fs.readFileSync(new URL('internal/server/apidocs/catalog/service-quotas.json', root), 'utf8'));
  const targets = document.paths['/'].post.parameters.find((p) => p.name === 'X-Amz-Target').schema.enum;
  assert.equal(targets.length, 9);
  for (const target of targets) {
    await config.customFetch(origin + '/', {method: 'POST', headers: {
      'Content-Type': 'application/x-amz-json-1.1', 'X-Amz-Target': target,
    }, body: '{}'});
    assert.equal(calls.at(-1).input.headers.get('X-Amz-Target'), target);
  }
  assert.equal(calls.length, 9);
});

test('other origins, routes, methods, media, targets and credential overrides fail before fetch', async () => {
  const {calls, config} = await setup();
  const headers = {'Content-Type': 'application/x-amz-json-1.1', 'X-Amz-Target': 'ServiceQuotasV20190624.ListServices'};
  for (const [url, init] of [
    ['http://127.0.0.1:4567/', {method: 'POST', headers, body: '{}'}],
    [origin + '/?other=1', {method: 'POST', headers, body: '{}'}],
    [origin + '/?', {method: 'POST', headers, body: '{}'}],
    [origin + '/other', {method: 'POST', headers, body: '{}'}],
    [origin + '/', {method: 'GET', headers}],
    [origin + '/', {method: 'POST', headers: {...headers, 'Content-Type': 'application/json'}, body: '{}'}],
    [origin + '/', {method: 'POST', headers: {...headers, 'X-Amz-Target': 'ServiceQuotasV20190624.DeleteQuota'}, body: '{}'}],
    [origin + '/', {method: 'POST', headers: {...headers, 'X-Simulator-Region': 'us-west-2'}, body: '{}'}],
    [origin + '/', {method: 'POST', headers: {...headers, 'X-Simulator-Region': 'us-east-1'}, body: '{}'}],
    ...['Authorization', 'Proxy-Authorization', 'Cookie', 'X-Simulator-Token', 'X-Simulator-Principal',
      'X-Simulator-Access-Role', 'X-Simulator-Service', 'X-Simulator-Unknown-Role',
      'X-Amz-Security-Token', 'X-Amz-Region', 'X-Amz-Region-Set']
      .map((header) => [origin + '/', {method: 'POST', headers: {...headers, [header]: 'fixture-only'}, body: '{}'}]),
  ]) await assert.rejects(config.customFetch(url, init));
  assert.equal(calls.length, 0);
});
