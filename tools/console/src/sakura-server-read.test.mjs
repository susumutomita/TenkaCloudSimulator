import {strict as assert} from 'node:assert';
import {test} from 'node:test';
import {readSakuraServers, sakuraServerEndpoint} from './sakura-server-read.mjs';

const endpoint = '/_simulator/providers/sakura/cloud/zone/is1a/api/cloud/1.1/server';
const capability = {endpoint, zone: 'is1a', apiVersion: '1.1', operations: ['GET /server', 'GET /server/{id}'], console: false};
const server = {ID: '9223372036854775807', Name: 'saved', Description: 'configured',
  ServerPlan: {CPU: 2, MemoryMB: 4096}, CreatedAt: '2026-11-24T23:00:00.123456789Z', ModifiedAt: '2026-11-24T23:01:00Z'};
const response = body => async () => ({ok: true, json: async () => body});
const list = {is_ok: true, From: 0, Count: 1, Total: 1, Servers: [server]};

test('advertised same-origin fixed zone and List/Get subset only', () => {
  assert.equal(sakuraServerEndpoint(capability), endpoint);
  for (const candidate of [null, {}, {...capability, endpoint: 'https://example.invalid' + endpoint},
    {...capability, endpoint: '//' + endpoint}, {...capability, endpoint: endpoint.replace('server', 'disk')},
    {...capability, endpoint: endpoint.replace('is1a', '%69s1a')}, {...capability, endpoint: endpoint + '?a=1'},
    {...capability, endpoint: endpoint + '\n'}, {...capability, zone: 'tk1a'},
    {...capability, apiVersion: '1.2'}, {...capability, operations: ['GET /server', 'GET /server']},
    {...capability, operations: [...capability.operations, 'POST /server']}]) {
    assert.equal(sakuraServerEndpoint(candidate), null);
  }
});

test('actual List/Get request shape, abort signal and no identity override', async () => {
  const signal = new AbortController().signal;
  const calls = [];
  const request = async (...args) => {
    calls.push(args);
    return {ok: true, json: async () => calls.length === 1 ? list : {is_ok: true, Server: server}};
  };
  const rows = await readSakuraServers(request, capability, '', signal);
  const detail = await readSakuraServers(request, capability, rows[0].ID, signal);
  assert.deepEqual(rows, [detail]);
  const headers = {Authorization: 'Basic ' + btoa('TCSIMSAKURA:dummy-local-only')};
  const options = {method: 'GET', redirect: 'error', headers, signal};
  assert.deepEqual(calls, [[endpoint + '?%7B%22From%22%3A0%2C%22Count%22%3A100%7D', options],
    [endpoint + '/' + server.ID, options]]);
  assert.equal(detail.MemoryMB, 4096);
});

test('rejects path, overflow and leading-zero IDs before dispatch', async () => {
  let called = 0;
  for (const id of ['0', '-1', '01', '9223372036854775808', '1/2', '1?away', '1\n', '1%2F2', 1, undefined]) {
    await assert.rejects(readSakuraServers(async () => { ++called; }, capability, id), /Invalid Sakura Server target/);
  }
  assert.equal(called, 0);
});

test('empty list is valid, partial, duplicate, malformed and failed reads are not', async () => {
  assert.deepEqual(await readSakuraServers(response({...list, Count: 0, Total: 0, Servers: []}), capability, ''), []);
  for (const body of [null, {...list, is_ok: false}, {...list, Total: 2}, {...list, From: 1},
    {...list, Total: 2, Count: 2, Servers: [server, server]}, {...list, Servers: [{...server, Availability: 'up'}]},
    {...list, Servers: [{...server, ID: 123}]}, {...list, Servers: [{...server, CreatedAt: 'last tuesday'}]},
    {...list, Servers: [{...server, ServerPlan: {CPU: 1, MemoryMB: 4096}}]}]) {
    await assert.rejects(readSakuraServers(response(body), capability, ''), /Invalid Sakura Server metadata/);
  }
  await assert.rejects(readSakuraServers(response({is_ok: true, Server: server}), capability, '2'), /metadata/);
  await assert.rejects(readSakuraServers(async () => ({ok: false}), capability, ''), /read failed/);
});
