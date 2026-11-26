/* Actual locally served Cloudscape JS mounted in jsdom, not in Chromium.
 * jsdom has no fetch/cookie transport integration: bridge only the already
 * signed-in fixture session and the expected Query operations to loopback.
 * No fabricated requests or responses, DOM layout, or visual claims.
 */
const assert = require('node:assert/strict');
const {createHash} = require('node:crypto');
const {readFileSync} = require('node:fs');
const {join, isAbsolute} = require('node:path');

assert.equal(process.version, 'v24.22.3');
const jsdomModule = process.env.JSDOM_MODULE;
assert.ok(isAbsolute(jsdomModule));
assert.equal(JSON.parse(readFileSync(join(jsdomModule, 'package.json'), 'utf8')).version, '30.0.1');
const {JSDOM, VirtualConsole} = require(jsdomModule);

const {port, cookie, hashes, domain, item, before, mode, attribute, value} = JSON.parse(readFileSync(0, 'utf8'));
assert.ok(Number.isInteger(port) && port > 0 && port <= 65535);
assert.ok(typeof cookie === 'string' && cookie && !/[\r\n]/.test(cookie));
assert.ok(['read', 'write', 'deny', 'delete', 'empty'].includes(mode));
const origin = 'http://127.0.0.1:' + port;
const consoleRoot = '/_simulator/console/';
const actions = ['ListDomains', 'GetAttributes'];
if (['write', 'deny'].includes(mode)) actions.push('PutAttributes');
if (mode === 'delete') actions.push('DeleteAttributes');

const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const wait = async (condition, reason) => {
  for (const end = Date.now() + 5500; Date.now() < end;) {
    const result = condition();
    if (result) return result;
    await pause(20);
  }
  assert.fail(reason);
};
const button = (root, name) => [...root.querySelectorAll('button')].find(b => b.textContent.trim() === name);
const click = async (root, name) => (await wait(() => {
  const candidate = button(root, name);
  return candidate && !candidate.disabled && candidate;
}, 'Missing enabled control: ' + name)).click();

async function asset(path) {
  const response = await fetch(origin + path, {headers: {Cookie: cookie}, redirect:'error', signal:AbortSignal.timeout(5000)});
  assert.equal(response.status, 200);
  const raw = Buffer.from(await response.arrayBuffer());
  const text = raw.toString('utf8');
  let canonical = raw;
  if (path === consoleRoot) {
    // The server injects one CSP style nonce into the immutable HTML source.
    // Verify exactly that change; mount the original reply including its nonce.
    const [nonce, ...duplicate] = [...text.matchAll(/<meta name="editor-style-nonce" content="([A-Za-z0-9+/]{32})">/g)];
    assert.ok(nonce && duplicate.length === 0);
    assert.ok(response.headers.get('Content-Security-Policy')?.includes("'nonce-" + nonce[1] + "'"));
    canonical = text.replace(nonce[0], '');
  }
  assert.equal(createHash('sha256').update(canonical).digest('hex'), hashes[path], 'Served asset changed: ' + path);
  return text;
}

async function run() {
  const [html, bundle] = await Promise.all([asset(consoleRoot), asset(consoleRoot + 'app.js')]);
  const errors = [], violations = [], operations = [];
  const virtualConsole = new VirtualConsole();
  virtualConsole.on('jsdomError', error => errors.push(error.message));
  const dom = new JSDOM(html, {url: origin + consoleRoot + '#/simpledb', pretendToBeVisual:true,
    runScripts:'outside-only', virtualConsole});
  const w = dom.window, d = w.document;
  const sent = action => operations.filter(call => call.action === action);
  try {
    // jsdom stubs for layout APIs only. The bundle, query responses and DOM
    // state from production code remain untouched.
    Object.assign(w, {TextEncoder, TextDecoder, matchMedia:() => ({
      matches:false, addListener(){}, removeListener(){}, addEventListener(){}, removeEventListener(){},
    }), ResizeObserver:class{observe(){} unobserve(){} disconnect(){}},
    IntersectionObserver:class{observe(){} unobserve(){} disconnect(){}}, scrollTo(){}});
    w.HTMLElement.prototype.scrollIntoView = function(){};
    w.fetch = async (path, options = {}) => {
      const h = new Headers(options.headers);
      const body = new URLSearchParams(options.body?.toString());
      const action = body.get('Action');
      try {
        if (path === '/_simulator/v1/capabilities') {
          assert.ok(!options.method || options.method === 'GET');
          assert.ok(!options.body);
        } else {
          assert.equal(path, '/');
          assert.ok(actions.includes(action), 'unexpected Query operation');
          assert.equal(options.method, 'POST');
          assert.equal(options.redirect, 'error');
          assert.equal(options.credentials, 'same-origin');
          assert.equal(h.get('content-type'), 'application/x-www-form-urlencoded');
          assert.equal(h.get('x-simulator-service'), 'sdb');
          assert.equal(h.get('x-simulator-region'), 'us-east-1');
          assert.equal(body.get('Version'), '2009-04-15');
          assert.equal(new Set(body.keys()).size, [...body.keys()].length);
          if (action !== 'ListDomains') {
            assert.equal(body.get('DomainName'), domain);
            assert.equal(body.get('ItemName'), item);
          }
          if (action === 'GetAttributes') assert.equal(body.get('ConsistentRead'), 'true');
        }
        for (const name of ['cookie','authorization','x-simulator-token','x-amz-target']) assert.ok(!h.has(name));
      } catch (e) {violations.push(e.message); throw e;}
      h.set('Cookie', cookie);
      const transport = new AbortController();
      if (options.signal?.aborted) transport.abort();
      options.signal?.addEventListener('abort', () => transport.abort(), {once:true});
      const response = await fetch(origin + path, {...options, body:options.body?.toString(), headers:h,
        signal:AbortSignal.any([transport.signal, AbortSignal.timeout(5000)]), redirect:'error'});
      if (action) operations.push({action, status:response.status, fields:Object.fromEntries(body)});
      return response;
    };
    // No jsdom network resources are loaded and the only transport used here
    // is the allowlisted fetch above. Fail if alternative browser IO is added.
    for (const name of ['XMLHttpRequest', 'WebSocket', 'EventSource']) {
      w[name] = function unavailable() {violations.push(name); throw Error('Unexpected IO');};
    }

    const input = async (label, value) => {
      const element = await wait(() => [...d.querySelectorAll('input,textarea')].find(el =>
        el.getAttribute('aria-label') === label
        || [...(el.labels ?? [])].some(l => l.textContent.trim() === label)
        || (el.getAttribute('aria-labelledby') ?? '').split(' ').some(id => d.getElementById(id)?.textContent.trim() === label)),
      'Missing input: ' + label);
      assert.ok(!element.disabled);
      const prototype = element.tagName === 'TEXTAREA' ? w.HTMLTextAreaElement.prototype : w.HTMLInputElement.prototype;
      Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, value);
      element.dispatchEvent(new w.Event('input', {bubbles:true}));
    };
    const table = () => d.querySelector('table[aria-label="Saved attributes"]');
    const rows = () => [...(table()?.querySelectorAll('tbody tr') ?? [])].map(tr =>
      [...tr.querySelectorAll('td')].slice(0,2).map(td => td.textContent.trim()));
    const normalized = values => JSON.stringify([...values].sort((a,b) => a[0].localeCompare(b[0])));
    const see = async values => wait(() => table() && normalized(rows()) === normalized(values), 'Saved attributes differed');
    const statusText = () => d.querySelector('[aria-label="SimpleDB action status"]')?.textContent ?? '';

    w.eval(bundle);
    if (mode === 'empty') {
      await wait(() => d.body.textContent.includes('No domains on this page.'), 'Cleanup not visible in Console');
      assert.deepEqual(operations.map(({action,status}) => [action,status]), [['ListDomains',200]]);
    } else {
      await click(d, domain);
      await input('Item name', item);
      await click(d, 'Read attributes');
      await see(before);
      if (mode === 'write' || mode === 'deny') {
        await input('Attribute to change', attribute);
        await input('New value', value);
        await click(d, 'Save attribute');
        if (mode === 'write') {
          await see([...before, [attribute,value]]);
          assert.match(statusText(), /Attribute saved\./);
        } else {
          await wait(() => /AccessDenied|HTTP 403/.test(statusText()), 'Denied write did not render a refusal');
          assert.equal(table(), null);
        }
        assert.deepEqual(sent('PutAttributes'), [{
          action:'PutAttributes', status:mode === 'write' ? 200 : 403, fields:{
            Action:'PutAttributes',Version:'2009-04-15',DomainName:domain,ItemName:item,
            'Attribute.1.Name':attribute,'Attribute.1.Value':value,'Attribute.1.Replace':'true',
            'Expected.Name':attribute,'Expected.Exists':'false',
          },
        }]);
      }
      if (mode === 'delete') {
        const matching = [...table().querySelectorAll('tbody tr')].filter(tr => tr.querySelector('td')?.textContent.trim() === attribute);
        assert.equal(matching.length, 1);
        await click(matching[0], 'Delete attribute');
        await click(d, 'Confirm deletion');
        await see(before.filter(([name]) => name !== attribute));
        assert.match(statusText(), /Attribute deleted\./);
        assert.deepEqual(sent('DeleteAttributes'), [{
          action:'DeleteAttributes', status:200, fields:{
            Action:'DeleteAttributes',Version:'2009-04-15',DomainName:domain,ItemName:item,
            'Attribute.1.Name':attribute,'Attribute.1.Value':value,'Expected.Name':attribute,'Expected.Value':value,
          },
        }]);
      }
      assert.deepEqual(sent('ListDomains').map(op => op.status), [200]);
      assert.deepEqual(sent('GetAttributes').map(op => op.status), ['write','delete'].includes(mode) ? [200,200] : [200]);
    }
    assert.deepEqual(violations, []);
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({mode, operations:operations.map(({action,status}) => ({action,status})),
      node:process.version, jsdom:'30.0.1'}));
  } finally {
    w.close();
  }
}

run().catch(error => {console.error(error); process.exitCode = 1;});
