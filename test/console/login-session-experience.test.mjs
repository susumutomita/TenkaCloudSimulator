// Pure presentation controls only; no React, DOM, browser, storage or network.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
const source=readFileSync(new URL('../../tools/console/src/login.jsx',import.meta.url),'utf8');
const start=source.indexOf('export function loginPresentationReason(');
assert.notEqual(start,-1);
const end=source.indexOf('\n}',start)+2;
assert.ok(end>start);
const declaration=source.slice(start,end).replace(/^export /,'');
const reason=vm.runInNewContext('('+declaration+')',{URLSearchParams});

test('only closed sign-out and session-required hints are selected',()=>{
  assert.equal(reason('?reason=signed-out'),'signed-out');
  assert.equal(reason('?reason=session-required'),'session-required');
  assert.equal(reason('?reason=session%2Drequired'),'session-required');
});
test('missing empty unknown or duplicate reason never supplies an auth diagnosis',()=>{
  for(const search of ['', '?reason=', '?reason=expired', '?reason=SIGNED-OUT', '?reason=signed-out&reason=signed-out', '?reason=unknown&reason=session-required'])assert.equal(reason(search),'');
});
test('unrelated query data is never returned or interpreted as a redirect',()=>{
  assert.equal(reason('?next=https%3A%2F%2Fexample.invalid&namespace=DUMMY'), '');
  assert.equal(reason('?reason=%3Cscript%3EDUMMY%3C%2Fscript%3E'), '');
  assert.equal(reason('?next=https%3A%2F%2Fexample.invalid&reason=session-required'), 'session-required');
});
