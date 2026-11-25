import assert from 'node:assert/strict';
import test from 'node:test';
import {cloudDNS, saveAndReadSRV} from '../src/gcp-dns-requests.mjs';
import {sameSRV, srvForm, srvRecord, verifySRVReceipt} from '../src/gcp-dns-srv.mjs';

const zone = 'example.test.', name = '_web._tcp.example.test.';
const original = {kind:'dns#resourceRecordSet', name, type:'SRV', ttl:300,
  rrdatas:['0 5 443 primary.example.test.', '1 0 443 backup.other.']};
const next = {name, type:'SRV', ttl:0, rrdatas:['0 0 0 .']};
const done = (previous, current) => ({kind:'dns#change', status:'done', isServing:false, id:'9',
  deletions:previous ? [previous] : [], additions:current ? [current] : []});

test('input matches native bounds, preserves multiple endpoints and supports the RFC unavailable target', () => {
  const draft = srvForm(original);
  assert.deepEqual(draft.endpoints[1], {priority:'1',weight:'0',port:'443',target:'backup.other.'});
  assert.ok(sameSRV(srvRecord(draft,zone), original));
  assert.deepEqual(srvRecord(srvForm(next),zone), next);
  assert.equal(srvRecord({...draft, ttl:'2147483647', endpoints:[{priority:'65535',weight:'65535',port:'65535',target:'.'}]},zone).ttl,2147483647);
  assert.deepEqual(srvForm(null).endpoints,[{priority:'0',weight:'0',port:'',target:''}]);
});

test('invalid domain boundaries, numbers, duplicate and excessive endpoints are rejected locally', () => {
  const draft = srvForm(original);
  for (const v of ['x.notexample.test.','x.example.test','x..example.test.','X.example.test.'])
    assert.throws(() => srvRecord({...draft,name:v},zone), /name/);
  for (const ttl of ['', '00', '-1', '1e3', '2147483648']) assert.throws(() => srvRecord({...draft,ttl},zone),/ttl/);
  for (const key of ['priority','weight','port']) for (const v of ['', '01', '-1', '65536'])
    assert.throws(() => srvRecord({...draft,endpoints:[{...draft.endpoints[0],[key]:v}]},zone),/numbers/);
  for (const target of ['host','bad..test.','UP.test.','https://bad.invalid/'])
    assert.throws(() => srvRecord({...draft,endpoints:[{...draft.endpoints[0],target}]},zone),/target/);
  assert.throws(() => srvRecord({...draft,endpoints:Array(33).fill(draft.endpoints[0])},zone),/endpoints/);
  assert.throws(() => srvRecord({...draft,endpoints:[]},zone),/endpoints/);
  assert.throws(() => srvRecord({...draft,endpoints:[draft.endpoints[0],draft.endpoints[0]]},zone),/duplicate/);
});

test('all three SRV mutations use a single request to the selected immutable zone ID; replacement sends the whole pre-edit snapshot', async () => {
  const calls = [], handle = new AbortController();
  const project = 'test-project', expected = '/_simulator/providers/gcp/dns/v1/projects/test-project/managedZones/9/changes';
  const api = cloudDNS(async (path, opts) => {
    const body = JSON.parse(opts.body);
    calls.push({path,opts,body}); return {json: async () => done(body.deletions[0], body.additions[0])};
  },project);
  for (const [prev, after] of [[null,original],[original,next],[next,null]]) {
    await api.changeSRV('9',prev,after,handle.signal);
    const {path,opts,body} = calls.at(-1);
    assert.equal(path,expected); assert.equal(opts.method,'POST'); assert.equal(opts.redirect,'error');
    assert.equal(opts.signal,handle.signal); assert.deepEqual(opts.headers,{'Content-Type':'application/json'});
    assert.deepEqual(body,{additions:after ? [after] : [], deletions:prev ? [prev] : []});
  }
  await assert.rejects(() => api.changeSRV('human-name',null,next),/zone ID/);
  await assert.rejects(() => api.changeSRV('9',original,{...next,name:'changed.example.test.'}),/Invalid SRV/);
  await assert.rejects(() => api.changeSRV('9',null,{...next,type:'A'}),/Invalid SRV/);
  await assert.rejects(() => api.changeSRV('9',null,null),/Invalid SRV/);
  handle.abort(); await assert.rejects(() => api.changeSRV('9',null,next,handle.signal),{name:'AbortError'});
  assert.equal(calls.length,3);
});

test('unconfirmed, stale and ambiguous writes never replay or silently adopt a fresh version', async () => {
  for (const bad of [{}, {...done(original,next),isServing:true}, {...done(original,next),status:'pending'},
    {...done(original,next),id:'-1'}, done({...original,ttl:42},next),done(original,{...next,rrdatas:[]})]) {
    assert.equal(verifySRVReceipt(bad,original,next),false);
    let count = 0;
    const api = cloudDNS(async () => { count++; return {json:async () => bad}; },'test-project');
    await assert.rejects(() => api.changeSRV('9',original,next),/Unconfirmed/); assert.equal(count,1);
  }
  for (const problem of ['412: stale data','connection interrupted']) {
    let count=0;
    const api=cloudDNS(async () => { count++; throw Error(problem); },'test-project');
    await assert.rejects(() => api.changeSRV('9',original,next),new RegExp(problem)); assert.equal(count,1);
  }
});

test('the form save path accepts only a receipt followed by matching native zone readback, never a replay', async () => {
  for (const [old, after] of [[null, original], [original, next], [next, null]]) {
    for (const mismatch of [false, true]) {
      const calls=[], data = mismatch ? (after ? {...after, ttl:37} : old) : after;
      const api = cloudDNS(async (path, opts) => {
        calls.push(opts.method + ' ' + path);
        const reply = opts.method === 'POST' ? done(old,after) :
          path.includes('/rrsets?') ? {rrsets: data ? [data] : []} : {id:'9'};
        return {json: async () => reply};
      }, 'local-project');
      const saving = saveAndReadSRV(api, '9', old, after, new AbortController().signal);
      if (mismatch) await assert.rejects(saving, /readback differs/);
      else assert.deepEqual((await saving).records, after ? [after] : []);
      assert.deepEqual(calls.map(c => c.split('/managedZones/')[1]), ['9/changes','9','9/rrsets?maxResults=100']);
    }
  }
});

test('a canceled or denied form save cannot publish or start a follow-up read', async () => {
  for (const deny of [false,true]) {
    const handle = new AbortController();
    const api = {changeSRV: async () => { if(deny) throw Error('not authorized'); handle.abort(); },
      details: () => assert.fail('Unexpected read after aborted or denied change')};
    await assert.rejects(() => saveAndReadSRV(api,'9',null,next,handle.signal), deny ? /not authorized/ : {name:'AbortError'});
  }
});
