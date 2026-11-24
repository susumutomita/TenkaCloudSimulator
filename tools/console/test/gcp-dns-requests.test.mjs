import {test} from 'node:test';
import assert from 'node:assert/strict';
import {cloudDNS, zoneBody} from '../src/gcp-dns-requests.mjs';

const path = '/_simulator/providers/gcp/dns/v1/projects/tenkacloud-local/managedZones';
const form = {name: 'test-zone', dnsName: 'example.invalid', description: 'test'};
const fixture = (respond) => {
  const calls = [];
  const api = cloudDNS(async (url, options) => {
    calls.push([url, options]);
    return {json: async () => respond(url, options)};
  }, 'tenkacloud-local');
  return {api, calls};
};

test('create encodes only the four supported fields and adds a trailing dot', async () => {
  const {api, calls} = fixture(() => ({id: '4', name: 'test-zone'}));
  const controller = new AbortController();
  assert.equal((await api.create({...form, dnssecConfig: 'on'}, controller.signal)).id, '4');
  assert.deepEqual(calls, [[path, {
    method: 'POST', headers: {'Content-Type': 'application/json'}, redirect: 'error',
    body: JSON.stringify({...form, dnsName: 'example.invalid.', visibility: 'public'}),
    signal: controller.signal,
  }]]);
  assert.equal(zoneBody({...form, dnsName: ' a_b.example. '}).dnsName, 'a_b.example.');
  assert.equal(zoneBody({...form, description: ''}).description, '');
});

test('validation matches bounded backend names and UTF-8 description limit', () => {
  assert.equal(zoneBody({...form, description: 'é'.repeat(512)}).description.length, 512);
  for (const v of ['-zone', 'zone-', 'ZONE', 'a'.repeat(64), 'zone/path']) {
    assert.throws(() => zoneBody({...form, name: v}), /^Error: name$/);
  }
  for (const v of ['', '.', 'x..y', 'foo-.com', 'A.com.', '*.example.', 'http://test', 'é.com.']) {
    assert.throws(() => zoneBody({...form, dnsName: v}), /^Error: dnsName$/);
  }
  assert.throws(() => zoneBody({...form, description: 'é'.repeat(513)}), /description/);
});

test('list and zone details use authentic native paths and follow bounded RRset tokens', async () => {
  const {api, calls} = fixture(url => {
    if (url === path + '?maxResults=100') return {managedZones: [{id:'4'}]};
    if (url === path + '/4') return {id: '4', name: 'test-zone'};
    if (url === path + '/4/rrsets?maxResults=100') return {rrsets: [{type: 'NS'}], nextPageToken: 'first&other=bad'};
    if (url === path + '/4/rrsets?maxResults=100&pageToken=first%26other%3Dbad') return {rrsets: [{type: 'SOA'}]};
    assert.fail(url);
  });
  assert.deepEqual(await api.list(), [{id:'4'}]);
  assert.deepEqual(await api.details('4'), {zone: {id: '4', name: 'test-zone'}, records: [{type:'NS'}, {type:'SOA'}]});
  assert.equal(calls.length, 4);
  assert(calls.every(([, options]) => options.method === 'GET' && options.redirect === 'error'));
});

test('recreated names never redirect old detail selection; ID/path and server mismatches fail', async () => {
  const {api, calls} = fixture(url => url.includes('/rrsets?') ? {rrsets: []} : {id: '5', name: 'test-zone'});
  for (const v of ['//other', '../4', '4/rrsets', '04', 4, '0', 'test-zone']) {
    await assert.rejects(() => api.details(v), /zone ID/);
  }
  assert.equal(calls.length, 0);
  await assert.rejects(() => api.details('4'), /Unexpected local Cloud DNS zone ID/);
  assert.equal(calls.length, 2);
  assert.throws(() => cloudDNS(() => {}, '//example.com'), /Invalid local Cloud DNS project/);
});

test('checked pagination fails on repeated tokens and suppresses work after abort', async () => {
  const {api, calls} = fixture(url => url.includes('/rrsets?') ? {rrsets: [], nextPageToken: 'loop'} : {id: '1'});
  await assert.rejects(() => api.details('1'), /pagination/);
  assert.equal(calls.length, 3);
  const handle = new AbortController();
  handle.abort();
  await assert.rejects(() => api.list(handle.signal), {name: 'AbortError'});
  assert.equal(calls.length, 3);
});

test('a canceled read never starts a later page even if transport fulfills it', async () => {
  const handle = new AbortController();
  const {api, calls} = fixture(url => {
    if (!url.includes('/rrsets?')) return {id:'7'};
    handle.abort();
    return {rrsets: [], nextPageToken:'not-followed'};
  });
  await assert.rejects(() => api.details('7', handle.signal), {name:'AbortError'});
  assert.equal(calls.length, 2);
});
