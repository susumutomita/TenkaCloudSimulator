const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const loc = new URL('http://127.0.0.1:1234/_simulator/docs/azure-queue.html');
const provider = '/_simulator/providers/azure-queue';
const queue = provider + '/testaccount/sample-queue';
const spec = {servers: [{url: provider}], paths: {'/{account}/{queue}': {put: {
  parameters: [{in: 'path', name: 'account', schema: {default: 'testaccount'}}],
}}}};
(async () => {
  const seen = [];
  let scalar;
  await vm.runInNewContext(fs.readFileSync(path.resolve(__dirname, '../../../internal/server/apidocs/azure-queue-init.js'), 'utf8'), {
    URL, Request, Headers, location: loc,
    document: {createElement() { throw new Error('unexpected app failure'); }},
    Scalar: {createApiReference(selector, config) { assert.equal(selector, '#reference'); scalar = config; }},
    fetch: async (input, init) => {
      seen.push([input, init]);
      return typeof input === 'string' ? {ok: true, json: async () => spec} : {ok: true};
    },
  });
  assert.equal(seen[0][0], '/_simulator/docs/azure-queue/openapi.json');
  assert.equal(seen[0][1].redirect, 'error');
  assert.equal(scalar.persistAuth, false);
  const uuid = '00112233-4455-6677-8899-aabbccddeeff';
  const paths = [[queue, 'PUT'], [queue, 'DELETE'], [queue+'/messages', 'POST'], [queue+'/messages?peekonly=true','GET'],
    [queue+'/messages','DELETE'], [queue+'/messages/'+uuid+'?popreceipt=abc','PUT'], [queue+'/messages/'+uuid,'DELETE']];
  for (const [path, method] of paths) await scalar.customFetch(path, {method});
  const payload = '<QueueMessage><MessageText>hello</MessageText></QueueMessage>';
  await scalar.customFetch(new Request(loc.origin+queue+'/messages', {method:'POST',body:payload,headers:{
    'x-ms-version':'2023-11-03','content-type':'application/xml','Authorization':'discard','X-Simulator-Token':'discard',
  }}));
  const [req] = seen.at(-1);
  assert.equal(await req.text(), payload);
  assert.equal(req.headers.get('x-ms-version'), '2023-11-03');
  assert.equal(req.headers.get('content-type'), 'application/xml');
  assert.equal(req.headers.get('Authorization'), null);
  assert.equal(req.headers.get('X-Simulator-Token'), null);
  assert.equal(req.redirect, 'error');
  assert.equal(req.credentials, 'same-origin');
  const count = seen.length;
  const denied = [['https://example.invalid'+queue,'PUT'], [provider+'/wrongaccount/sample-queue','PUT'],
    [provider+'/testaccount/sample--queue','PUT'],[queue+'/messages/bad-id','PUT'],[queue+'/messages','PATCH'],
    [queue+'/messages/extra/segment','GET'],[queue+'#fragment','PUT'],
    [queue+'/messages/../../../../azure/testaccount/sample-queue','PUT']];
  for (const [path, method] of denied)
    await assert.rejects(() => Promise.resolve().then(() => scalar.customFetch(path,{method})));
  assert.equal(seen.length, count);
  console.log('Queue Scalar adapter: 7 allowed operations, body/header preservation, 8 local-scope rejection checks passed');
})().catch((e) => {console.error(e); process.exitCode = 1});
