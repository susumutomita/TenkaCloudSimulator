const assert = require('node:assert/strict');
const {createHash} = require('node:crypto');
const {readFileSync} = require('node:fs');
const {JSDOM, VirtualConsole} = require(process.env.JSDOM_MODULE);
const {port, cookie, selected, asset} = JSON.parse(readFileSync(0, 'utf8'));
assert.ok(Number.isInteger(port) && port > 0 && port < 65536);
const origin = 'http://127.0.0.1:' + port;
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(predicate, message) {
  for (const end = Date.now() + 12000; Date.now() < end;) {
    const hit = predicate();
    if (hit) return hit;
    await sleep(15);
  }
  assert.fail(message);
}
const exactQuery = subnet => ({
  Action: 'DescribeRouteTables', Version: '2016-11-15', MaxResults: '100',
  ...(subnet ? {'Filter.1.Name': 'association.subnet-id', 'Filter.1.Value.1': subnet} : {}),
});

(async () => {
  const [html, bundle] = await Promise.all(['/', '/app.js'].map(tail =>
    fetch(origin + '/_simulator/console' + tail, {headers: {Cookie: cookie}, redirect: 'error'}).then(async response => {
      assert.equal(response.status, 200);
      return response.text();
    })));
  assert.equal(createHash('sha256').update(bundle).digest('hex'), asset);
  const dom = new JSDOM(html, {url: origin + '/_simulator/console/#/vpc',
    runScripts: 'outside-only', pretendToBeVisual: true, virtualConsole: new VirtualConsole()});
  const w = dom.window, d = w.document, reads = [];
  let hold, release, oldSignal;
  try {
    Object.assign(w, {TextEncoder, TextDecoder,
      matchMedia: () => ({matches: false, addListener(){}, removeListener(){}, addEventListener(){}, removeEventListener(){}}),
      ResizeObserver: class {observe(){} unobserve(){} disconnect(){}},
      IntersectionObserver: class {observe(){} unobserve(){} disconnect(){}},
      scrollTo(){}});
    w.HTMLElement.prototype.scrollIntoView = function(){};
    w.fetch = async (path, init = {}) => {
      assert.ok(path === '/' || path === '/_simulator/v1/capabilities');
      const headers = new Headers(init.headers);
      headers.set('Cookie', cookie);
      const body = init.body?.toString();
      const form = new URLSearchParams(body);
      if (form.get('Action') === 'DescribeRouteTables') {
        assert.equal(headers.get('X-Simulator-Region'), 'us-east-1');
        reads.push(Object.fromEntries(form));
      }
      const response = await fetch(origin + path, {...init, headers, body, signal: undefined, redirect: 'error'});
      if (hold && form.get('Filter.1.Value.1') === selected.Subnet) {
        hold = false;
        oldSignal = init.signal;
        await new Promise(resolve => {release = resolve;});
      }
      return response;
    };
    w.eval(bundle);
    const display = text => d.body.textContent.includes(text);
    const none = 'No explicit route table is associated with this subnet in the selected VPC.';
    const vpc = await until(() => d.querySelector('input[aria-label="Select ' + selected.Vpc + '"]'), 'missing CFN VPC');
    vpc.click();
    const input = await until(() => d.querySelector('input[placeholder="subnet-…"]'), 'missing subnet filter');
    const fill = (value) => {
      Object.getOwnPropertyDescriptor(w.HTMLInputElement.prototype, 'value').set.call(input, value);
      input.dispatchEvent(new w.InputEvent('input', {bubbles: true, composed: true, inputType: 'insertText', data: value}));
      input.dispatchEvent(new w.Event('change', {bubbles: true}));
    };
    await until(() => reads.length === 1 && (display(selected.Table) || display('No explicit subnet associations are recorded.')),
      'initial table read was not published');
    assert.deepEqual(reads[0], exactQuery());
    fill(selected.Subnet);
    await until(() => display(selected.Association) && display(selected.Table), 'matching table not published');
    assert.deepEqual(reads[1], exactQuery(selected.Subnet));
    fill('subnet-12');
    await until(() => display('Enter a complete subnet ID.'), 'incomplete-input feedback missing');
    assert.equal(reads.length, 2, 'incomplete input issued a query');
    assert.ok(!display(selected.Association));
    fill(selected.Unassociated);
    await until(() => display(none), 'explicit no-match not displayed');
    assert.deepEqual(reads[2], exactQuery(selected.Unassociated));

    // Delay a received matching reply; a newer no-match must win.
    hold = true;
    fill(selected.Subnet);
    await until(() => release, 'the older request was not held');
    fill(selected.Unassociated);
    await until(() => oldSignal.aborted && reads.length === 5 && display(none), 'older filter was not superseded');
    release();
    await sleep(100);
    assert.ok(display(none) && !display(selected.Association));
    fill('');
    await until(() => reads.length === 6 && !display(none) &&
      (display(selected.Table) || display('No explicit subnet associations are recorded.')), 'clear did not restore unfiltered');
    assert.deepEqual(reads, [exactQuery(), exactQuery(selected.Subnet), exactQuery(selected.Unassociated),
      exactQuery(selected.Subnet), exactQuery(selected.Unassociated), exactQuery()]);
  } finally {
    dom.window.close();
  }
})().catch(error => {console.error(error); process.exitCode = 1;});
