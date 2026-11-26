const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const here = 'http://127.0.0.1:1234/_simulator/docs/azure-queue.html';
const provider = '/_simulator/providers/azure-queue';
const queue = provider + '/testaccount/sample-queue';
const url = '/_simulator/docs/azure-queue/openapi.json';
const selector = 'X-Simulator-Principal';
const spec = {servers: [{url: provider}], paths: {'/{account}/{queue}': {put: {
  parameters: [{in: 'path', name: 'account', schema: {default: 'testaccount'}}],
}}}};
const initScript = fs.readFileSync(path.resolve(__dirname, '../../../internal/server/apidocs/azure-queue-init.js'), 'utf8');
const shell = fs.readFileSync(path.resolve(__dirname, '../../../internal/server/apidocs/azure-queue.html'), 'utf8');

async function boot(suffix = '', specStatus = 200, nativeStatus = 200) {
  const seen = [], alerts = [];
  let scalar;
  const loc = new URL(here + suffix);
  await vm.runInNewContext(initScript, {
    URL, URLSearchParams, Request, Headers, location: loc,
    document: {
      createElement() {return {setAttribute(name, val) {assert.equal(name, 'role'); assert.equal(val, 'alert');}}},
      getElementById(id) {assert.equal(id, 'reference'); return {append(node) {alerts.push(node.textContent);}};},
    },
    Scalar: {createApiReference(target, config) {assert.equal(target, '#reference'); scalar = config;}},
    fetch: async (input, init) => {
      seen.push([input, init]);
      return typeof input === 'string'
        ? {ok: specStatus === 200, status: specStatus, json: async () => spec}
        : {ok: nativeStatus === 200, status: nativeStatus};
    },
  });
  return {loc, scalar, seen, alerts};
}

async function checkFirstRender() {
  const React = require('react');
  const {renderToStaticMarkup} = require('react-dom/server');
  const {build} = require('esbuild');
  const base = path.resolve(__dirname, '..');
  const {outputFiles} = await build({
    absWorkingDir: base, entryPoints: ['src/azure-queue.jsx'], bundle: true, write: false,
    platform: 'node', format: 'cjs', external: ['react', 'react-dom'], loader: {'.css': 'empty'},
  });
  const component = {exports: {}};
  vm.runInNewContext(outputFiles[0].text, {module: component, exports: component.exports, require});
  const View = component.exports.default;
  const render = principal => renderToStaticMarkup(React.createElement(View, {
    account: 'testaccount', principal, request() {assert.fail('initial render must not call the API');},
  }));
  const bootstrap = render(undefined);
  assert.match(bootstrap, /<h1[^>]*>Azure Queue Storage<\/h1>/);
  assert.match(bootstrap, /href="\/_simulator\/docs\/azure-queue\.html"/);
  assert.match(bootstrap, /referrerpolicy="no-referrer"/i);
  const selected = render('learner');
  assert.match(selected, /href="\/_simulator\/docs\/azure-queue\.html\?principal=learner"/);
  assert.match(selected, /referrerpolicy="no-referrer"/i);
  const invalid = render('learner\n');
  assert.doesNotMatch(invalid, /href="\/_simulator\/docs\/azure-queue\.html/);
  assert.match(invalid, /Queue name/);
}

(async () => {
  await checkFirstRender();
  const selected = await boot('?principal=learner#tag/queue');
  assert.equal(selected.seen[0][0], url);
  assert.equal(new Headers(selected.seen[0][1].headers).get(selector), 'learner');
  assert.equal(selected.seen[0][1].referrerPolicy, 'no-referrer');
  assert.equal(selected.seen[0][1].mode, 'same-origin');
  assert.equal(selected.seen[0][1].redirect, 'error');
  assert.equal(selected.seen[0][1].credentials, 'same-origin');
  assert.deepEqual(selected.alerts, []);
  await selected.scalar.customFetch(queue + '/messages', {
    method: 'GET', headers: {[selector]: 'ignored', Authorization: 'ignored', 'X-Simulator-Token': 'ignored'},
  });
  assert.equal(selected.seen.at(-1)[0].headers.get(selector), 'learner');
  assert.equal(selected.seen.at(-1)[0].headers.get('Authorization'), null);
  assert.equal(selected.seen.at(-1)[0].headers.get('X-Simulator-Token'), null);
  assert.equal(selected.seen.at(-1)[0].referrerPolicy, 'no-referrer');
  assert.equal(selected.seen.at(-1)[0].mode, 'same-origin');
  const selectedCount = selected.seen.length;
  await assert.rejects(() => selected.scalar.customFetch('https://example.invalid' + queue, {method:'PUT'}));
  assert.equal(selected.seen.length, selectedCount);

  for (const invalid of ['?principal=', '?principal=a&principal=b', '?principal=a%20b', '?principal=a%0A', '?principal=' + 'a'.repeat(65)]) {
    const rejected = await boot(invalid);
    assert.equal(rejected.scalar, undefined);
    assert.equal(rejected.seen.length, 0);
    assert.equal(rejected.alerts.length, 1);
  }
  const deniedSelection = await boot('?principal=learner', 403);
  assert.equal(deniedSelection.scalar, undefined);
  assert.equal(deniedSelection.seen.length, 1);
  assert.equal(new Headers(deniedSelection.seen[0][1].headers).get(selector), 'learner');
  assert.equal(deniedSelection.alerts.length, 1);
  const deniedNative = await boot('?principal=learner', 200, 403);
  assert.equal((await deniedNative.scalar.customFetch(queue + '/messages', {method:'GET'})).status, 403);
  assert.equal(deniedNative.seen.length, 2);
  assert.equal(deniedNative.seen[1][0].headers.get(selector), 'learner');

  const {loc, scalar, seen} = await boot();
  assert.equal(seen[0][0], url);
  assert.equal(new Headers(seen[0][1].headers).has(selector), false);
  assert.equal(seen[0][1].redirect, 'error');
  assert.equal(scalar.persistAuth, false);
  const uuid = '00112233-4455-6677-8899-aabbccddeeff';
  const paths = [[queue, 'PUT'], [queue, 'DELETE'], [queue+'/messages', 'POST'], [queue+'/messages?peekonly=true','GET'],
    [queue+'/messages','DELETE'], [queue+'/messages/'+uuid+'?popreceipt=abc','PUT'], [queue+'/messages/'+uuid,'DELETE']];
  for (const [p, method] of paths) await scalar.customFetch(p, {method});
  const payload = '<QueueMessage><MessageText>hello</MessageText></QueueMessage>';
  await scalar.customFetch(new Request(loc.origin+queue+'/messages', {method:'POST',body:payload,headers:{
    'x-ms-version':'2023-11-03','content-type':'application/xml','Authorization':'discard','X-Simulator-Token':'discard',
    [selector]: 'ignored',
  }}));
  const [req] = seen.at(-1);
  assert.equal(await req.text(), payload);
  assert.equal(req.headers.get('x-ms-version'), '2023-11-03');
  assert.equal(req.headers.get('content-type'), 'application/xml');
  assert.equal(req.headers.get('Authorization'), null);
  assert.equal(req.headers.get('X-Simulator-Token'), null);
  assert.equal(req.headers.get(selector), null);
  assert.equal(req.redirect, 'error');
  assert.equal(req.credentials, 'same-origin');
  assert.equal(req.referrerPolicy, 'no-referrer');
  const count = seen.length;
  const denied = [['https://example.invalid'+queue,'PUT'], [provider+'/wrongaccount/sample-queue','PUT'],
    [provider+'/testaccount/sample--queue','PUT'],[queue+'/messages/bad-id','PUT'],[queue+'/messages','PATCH'],
    [queue+'/messages/extra/segment','GET'],[queue+'#fragment','PUT'],
    [queue+'/messages/../../../../azure/testaccount/sample-queue','PUT']];
  for (const [p, method] of denied)
    await assert.rejects(() => Promise.resolve().then(() => scalar.customFetch(p,{method})));
  assert.equal(seen.length, count);
  assert.match(shell, /<meta name="referrer" content="no-referrer"\s*\/?>/);
  console.log('Queue Scalar adapter: default/selected, no fallback, header/referrer isolation and local scope controls passed');
})().catch((e) => {console.error(e); process.exitCode = 1});
